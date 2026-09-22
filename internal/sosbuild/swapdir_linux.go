//go:build linux && (amd64 || arm64)

package sosbuild

import (
	"syscall"
	"unsafe"
)

const (
	renameExchange = 0x2
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
	number := uintptr(renameAt2Syscall)
	atFDCWD := ^uintptr(99)
	_, _, errno := syscall.Syscall6(number, atFDCWD, uintptr(unsafe.Pointer(ap)), atFDCWD, uintptr(unsafe.Pointer(bp)), renameExchange, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
