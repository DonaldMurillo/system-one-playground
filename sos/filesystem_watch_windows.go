//go:build windows

package sos

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// A recursive change-notification handle is armed synchronously before the
// watcher is returned. This avoids losing a change made immediately after
// startup (a blocked ReadDirectoryChangesW goroutine cannot promise that).
// The handle only triggers reconciliation; snapshot diffs define the records.
// WaitForSingleObject uses a short bounded wait so close never depends on
// canceling a synchronous kernel read.

const watchNotifyFilter = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0040
const watchWaitMilliseconds = 100

var (
	watchFirstChange = syscall.NewLazyDLL("kernel32.dll").NewProc("FindFirstChangeNotificationW")
	watchNextChange  = syscall.NewLazyDLL("kernel32.dll").NewProc("FindNextChangeNotification")
	watchCloseChange = syscall.NewLazyDLL("kernel32.dll").NewProc("FindCloseChangeNotification")
)

type windowsBackend struct {
	root       string
	handle     syscall.Handle
	triggers   chan nativeWatchEvent
	done       chan struct{}
	readerDone chan struct{}
	closeOnce  sync.Once
}

func newNativeWatchBackend(spec traversalSpec, _ time.Duration, _ map[string]watchEntry) (nativeWatchBackend, error) {
	root, err := syscall.UTF16PtrFromString(spec.root)
	if err != nil {
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	raw, _, callErr := watchFirstChange.Call(uintptr(unsafe.Pointer(root)), 1, watchNotifyFilter)
	if syscall.Handle(raw) == syscall.InvalidHandle {
		return nil, fileOpError(callErr, spec.rootDisplay, traverseOp)
	}
	backend := &windowsBackend{
		root:       spec.root,
		handle:     syscall.Handle(raw),
		triggers:   make(chan nativeWatchEvent, 64),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go backend.read()
	return backend, nil
}

func (b *windowsBackend) events() <-chan nativeWatchEvent { return b.triggers }

// One recursive notification handle already covers entries created later.
func (b *windowsBackend) sync(traversalSpec, map[string]watchEntry) error { return nil }

func (b *windowsBackend) close() error {
	b.closeOnce.Do(func() {
		close(b.done)
		<-b.readerDone
		_, _, _ = watchCloseChange.Call(uintptr(b.handle))
	})
	return nil
}

func (b *windowsBackend) read() {
	defer close(b.readerDone)
	defer close(b.triggers)
	for {
		select {
		case <-b.done:
			return
		default:
		}
		state, err := syscall.WaitForSingleObject(b.handle, watchWaitMilliseconds)
		if err != nil {
			b.fail(err)
			return
		}
		switch state {
		case syscall.WAIT_TIMEOUT:
			// The API does not report removal of the watched directory itself.
			// A bounded root check makes that a terminal stream failure.
			if _, err := os.Stat(b.root); err != nil {
				b.fail(err)
				return
			}
			continue
		case syscall.WAIT_OBJECT_0:
			// Rearm before handing the trigger to the scanner. Windows retains
			// changes between the signal and this call, so none are lost there.
			ok, _, callErr := watchNextChange.Call(uintptr(b.handle))
			if ok == 0 {
				b.fail(callErr)
				return
			}
			select {
			case b.triggers <- nativeWatchEvent{}:
			case <-b.done:
				return
			}
		default:
			b.fail(fmt.Errorf("unexpected directory notification wait state %d", state))
			return
		}
	}
}

func (b *windowsBackend) fail(err error) {
	select {
	case b.triggers <- nativeWatchEvent{failure: err}:
	case <-b.done:
	}
}
