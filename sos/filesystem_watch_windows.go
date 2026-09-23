//go:build windows

package sos

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// The Windows backend turns ReadDirectoryChangesW notifications into
// reconciliation triggers. One recursive handle on the root reports the whole
// subtree — including per-file writes — so there is no per-entry registration
// and sync is a no-op. The read is issued synchronously; close cancels the
// in-flight read with CancelIoEx before closing the handle, so shutdown never
// leaks a blocked reader.

const (
	// FILE_LIST_DIRECTORY: the access required for directory-change reads.
	watchFileListDirectory = 0x0001
	// FILE_NOTIFY_CHANGE_FILE_NAME | DIR_NAME | ATTRIBUTES | SIZE |
	// LAST_WRITE | CREATION: everything a snapshot diff can observe.
	watchNotifyFilter = 0x0001 | 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0040
	// ERROR_NOTIFY_ENUM_DIR: the kernel's notify buffer overflowed and the
	// reported change list is incomplete.
	watchNotifyEnumDir = syscall.Errno(1022)
)

type windowsBackend struct {
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
	handle, err := syscall.CreateFile(root, watchFileListDirectory,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	backend := &windowsBackend{
		handle:     handle,
		triggers:   make(chan nativeWatchEvent, 64),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go backend.read()
	return backend, nil
}

func (b *windowsBackend) events() <-chan nativeWatchEvent { return b.triggers }

// sync is a no-op: the recursive root handle already reports the whole tree.
func (b *windowsBackend) sync(traversalSpec, map[string]watchEntry) error { return nil }

func (b *windowsBackend) close() error {
	b.closeOnce.Do(func() {
		close(b.done)
		_ = syscall.CancelIoEx(b.handle, nil)
		<-b.readerDone
		_ = syscall.CloseHandle(b.handle)
	})
	return nil
}

func (b *windowsBackend) read() {
	defer close(b.readerDone)
	defer close(b.triggers)
	// ReadDirectoryChangesW requires a DWORD-aligned buffer (otherwise it
	// fails with ERROR_NOACCESS). A byte array has no such alignment promise.
	var buffer [64 * 1024 / 4]uint32
	for {
		var filled uint32
		err := syscall.ReadDirectoryChanges(b.handle, (*byte)(unsafe.Pointer(&buffer[0])), uint32(unsafe.Sizeof(buffer)), true, watchNotifyFilter, &filled, nil, 0)
		select {
		case <-b.done:
			return
		default:
		}
		if err != nil {
			if err == syscall.ERROR_OPERATION_ABORTED {
				return
			}
			if err != watchNotifyEnumDir {
				// Preserve the Windows error for the stream's terminal
				// failure; a scan cannot repair a failed native handle.
				select {
				case b.triggers <- nativeWatchEvent{failure: err}:
				case <-b.done:
				}
				return
			}
			select {
			case b.triggers <- nativeWatchEvent{overflow: true}:
			case <-b.done:
				return
			}
			continue
		}
		if filled == 0 {
			// Windows can report an overflowing notification buffer as a
			// successful read with zero bytes. A rescan is mandatory.
			select {
			case b.triggers <- nativeWatchEvent{overflow: true}:
			case <-b.done:
				return
			}
			continue
		}
		select {
		case b.triggers <- nativeWatchEvent{}:
		case <-b.done:
			return
		}
	}
}
