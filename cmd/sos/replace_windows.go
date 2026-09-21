//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32MoveFile = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

func replaceFile(from, to string) error {
	fromPtr, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	r1, _, callErr := kernel32MoveFile.Call(uintptr(unsafe.Pointer(fromPtr)), uintptr(unsafe.Pointer(toPtr)), moveFileReplaceExisting|moveFileWriteThrough)
	if r1 == 0 {
		return callErr
	}
	return nil
}
