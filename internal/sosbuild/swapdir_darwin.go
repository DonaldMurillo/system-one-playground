//go:build darwin

package sosbuild

import (
	"syscall"
	"unsafe"
)

const (
	darwinRenameAtXNP = 488
	renameSwap        = 0x2
)

func atomicSwapDirectories(a, b string) error {
	ap, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	bp, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	atFDCWD := ^uintptr(1)
	_, _, errno := syscall.Syscall6(darwinRenameAtXNP, atFDCWD, uintptr(unsafe.Pointer(ap)), atFDCWD, uintptr(unsafe.Pointer(bp)), renameSwap, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
