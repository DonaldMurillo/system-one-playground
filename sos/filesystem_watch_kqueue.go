//go:build darwin

package sos

import (
	"sync"
	"syscall"
	"time"
)

// The macOS backend turns kqueue vnode notifications into reconciliation
// triggers. Every observed path — the root, each folder, and each regular
// file, since content writes never touch a parent directory entry — holds
// one O_EVTONLY descriptor registered for vnode notes, and descriptors are
// reconciled against each scan. An EVFILT_USER event lets close() wake the
// blocked reader immediately, so shutdown never waits on a timeout.

const kqueueNoteMask = syscall.NOTE_WRITE | syscall.NOTE_DELETE | syscall.NOTE_RENAME |
	syscall.NOTE_EXTEND | syscall.NOTE_ATTRIB | syscall.NOTE_REVOKE

type kqueueBackend struct {
	kq         int
	triggers   chan nativeWatchEvent
	watches    map[string]int // host path → O_EVTONLY descriptor; driver-owned
	done       chan struct{}
	readerDone chan struct{}
	closeOnce  sync.Once
}

func newNativeWatchBackend(spec traversalSpec, _ time.Duration, snapshot map[string]watchEntry) (nativeWatchBackend, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	backend := &kqueueBackend{
		kq:         kq,
		triggers:   make(chan nativeWatchEvent, 64),
		watches:    map[string]int{},
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{{
		Ident:  0,
		Filter: syscall.EVFILT_USER,
		Flags:  syscall.EV_ADD,
	}}, nil, nil); err != nil {
		_ = syscall.Close(kq)
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	if err := backend.addWatch(spec.root); err != nil {
		_ = syscall.Close(kq)
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	for path := range watchTargets(spec, snapshot) {
		if err := backend.addWatch(path); err != nil {
			for _, descriptor := range backend.watches {
				_ = syscall.Close(descriptor)
			}
			_ = syscall.Close(kq)
			return nil, fileOpError(err, spec.rootDisplay, traverseOp)
		}
	}
	go backend.read()
	return backend, nil
}

func (b *kqueueBackend) addWatch(path string) error {
	descriptor, err := syscall.Open(path, syscall.O_EVTONLY, 0)
	if err != nil {
		return err
	}
	if _, err := syscall.Kevent(b.kq, []syscall.Kevent_t{{
		Ident:  uint64(descriptor),
		Filter: syscall.EVFILT_VNODE,
		Flags:  syscall.EV_ADD | syscall.EV_CLEAR,
		Fflags: kqueueNoteMask,
	}}, nil, nil); err != nil {
		_ = syscall.Close(descriptor)
		return err
	}
	if stale, replaced := b.watches[path]; replaced {
		_ = syscall.Close(stale)
	}
	b.watches[path] = descriptor
	return nil
}

func (b *kqueueBackend) events() <-chan nativeWatchEvent { return b.triggers }

// sync reconciles the descriptors with a fresh snapshot: new paths are
// opened and registered, stale paths are closed and forgotten.
func (b *kqueueBackend) sync(spec traversalSpec, snapshot map[string]watchEntry) error {
	desired := watchTargets(spec, snapshot)
	for path := range desired {
		if err := b.addWatch(path); err != nil {
			return err
		}
	}
	for path, descriptor := range b.watches {
		if _, wanted := desired[path]; !wanted {
			_ = syscall.Close(descriptor)
			delete(b.watches, path)
		}
	}
	return nil
}

func (b *kqueueBackend) close() error {
	b.closeOnce.Do(func() {
		close(b.done)
		_, _ = syscall.Kevent(b.kq, []syscall.Kevent_t{{
			Ident:  0,
			Filter: syscall.EVFILT_USER,
			Flags:  syscall.EV_ADD,
			Fflags: syscall.NOTE_TRIGGER,
		}}, nil, nil)
		<-b.readerDone
		for _, descriptor := range b.watches {
			_ = syscall.Close(descriptor)
		}
		_ = syscall.Close(b.kq)
	})
	return nil
}

func (b *kqueueBackend) read() {
	defer close(b.readerDone)
	defer close(b.triggers)
	notes := make([]syscall.Kevent_t, 64)
	for {
		count, err := syscall.Kevent(b.kq, nil, notes, nil)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		if count == 0 {
			continue
		}
		overflow := false
		for _, note := range notes[:count] {
			if note.Filter == syscall.EVFILT_USER {
				return // shutdown wake from close()
			}
			if note.Flags&syscall.EV_ERROR != 0 && syscall.Errno(note.Data) == syscall.ENOBUFS {
				overflow = true
			}
		}
		select {
		case b.triggers <- nativeWatchEvent{overflow: overflow}:
		case <-b.done:
			return
		}
	}
}
