//go:build linux

package sos

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// The Linux backend turns inotify notifications into reconciliation
// triggers. Directories are watched for entry changes and regular files for
// content writes, because writing an existing file never touches its parent
// directory entry. Watch descriptors are keyed by inode, so a rename may
// already hold a descriptor under its previous path: sync retires a
// descriptor only when no desired path uses it anymore.

const inotifyWatchMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_DELETE_SELF |
	syscall.IN_MOVE_SELF | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_CLOSE_WRITE | syscall.IN_MODIFY | syscall.IN_ATTRIB

// watchPollInterval bounds how long the reader may sleep in epoll before
// rechecking shutdown; notifications themselves wake it immediately.
const watchPollInterval = 100

type inotifyBackend struct {
	fd         int
	epoll      int
	triggers   chan nativeWatchEvent
	watches    map[string]int // host path → watch descriptor; driver-owned
	done       chan struct{}
	readerDone chan struct{}
	closeOnce  sync.Once
}

func newNativeWatchBackend(spec traversalSpec, _ time.Duration, snapshot map[string]watchEntry) (nativeWatchBackend, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	epoll, err := syscall.EpollCreate1(syscall.IN_CLOEXEC)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	backend := &inotifyBackend{
		fd:         fd,
		epoll:      epoll,
		triggers:   make(chan nativeWatchEvent, 64),
		watches:    map[string]int{},
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	if err := backend.addWatch(spec.root); err != nil {
		_ = syscall.Close(epoll)
		_ = syscall.Close(fd)
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	for path := range watchTargets(spec, snapshot) {
		if err := backend.addWatch(path); err != nil {
			_ = syscall.Close(epoll)
			_ = syscall.Close(fd)
			return nil, fileOpError(err, spec.rootDisplay, traverseOp)
		}
	}
	go backend.read()
	return backend, nil
}

func (b *inotifyBackend) addWatch(path string) error {
	descriptor, err := syscall.InotifyAddWatch(b.fd, path, inotifyWatchMask)
	if err != nil {
		return err
	}
	b.watches[path] = descriptor
	return nil
}

func (b *inotifyBackend) events() <-chan nativeWatchEvent { return b.triggers }

// sync reconciles the watch map with a fresh snapshot. Registering a watch
// replaces its mask, so re-adding a watched path is idempotent.
func (b *inotifyBackend) sync(spec traversalSpec, snapshot map[string]watchEntry) error {
	desired := watchTargets(spec, snapshot)
	for path := range desired {
		if err := b.addWatch(path); err != nil {
			return err
		}
	}
	kept := map[int]bool{}
	for path := range desired {
		if descriptor, watched := b.watches[path]; watched {
			kept[descriptor] = true
		}
	}
	for path, descriptor := range b.watches {
		if _, wanted := desired[path]; wanted {
			continue
		}
		delete(b.watches, path)
		if !kept[descriptor] {
			_, _ = syscall.InotifyRmWatch(b.fd, uint32(descriptor))
		}
	}
	return nil
}

func (b *inotifyBackend) close() error {
	b.closeOnce.Do(func() {
		close(b.done)
		<-b.readerDone
		_ = syscall.Close(b.epoll)
		_ = syscall.Close(b.fd)
	})
	return nil
}

// read parks in epoll so shutdown stays bounded and reads never block the
// closer; every kernel event batch becomes one trigger.
func (b *inotifyBackend) read() {
	defer close(b.readerDone)
	defer close(b.triggers)
	pending := make([]syscall.EpollEvent, 8)
	var buffer [64 * 1024]byte
	for {
		select {
		case <-b.done:
			return
		default:
		}
		count, err := syscall.EpollWait(b.epoll, pending, watchPollInterval)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		if count == 0 {
			continue
		}
		changed, overflow := b.drain(buffer[:])
		if !changed && !overflow {
			continue
		}
		select {
		case b.triggers <- nativeWatchEvent{overflow: overflow}:
		case <-b.done:
			return
		}
	}
}

// drain reads every queued event without blocking and reports whether any
// change — or an overflow, which the kernel signals in-band — was observed.
func (b *inotifyBackend) drain(buffer []byte) (changed, overflow bool) {
	for {
		read, err := syscall.Read(b.fd, buffer)
		if read < syscall.SizeofInotifyEvent {
			return changed, overflow
		}
		_ = err
		changed = true
		for offset := 0; offset+syscall.SizeofInotifyEvent <= read; {
			event := (*syscall.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
			if event.Mask&syscall.IN_Q_OVERFLOW != 0 {
				overflow = true
			}
			offset += syscall.SizeofInotifyEvent + int(event.Len)
		}
	}
}
