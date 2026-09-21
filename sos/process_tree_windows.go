//go:build windows

package sos

import (
	"os/exec"
	"syscall"
	"unsafe"
)

func configureProcessTree(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	children, snapshotErr := windowsDescendants(uint32(cmd.Process.Pid))
	// os.Process.Kill uses the handle retained from process creation, so a
	// recycled PID can never target an unrelated root process.
	rootErr := cmd.Process.Kill()
	var childErr error
	for i := len(children) - 1; i >= 0; i-- {
		if err := terminateWindowsPID(children[i]); err != nil && childErr == nil {
			childErr = err
		}
	}
	if snapshotErr != nil {
		return snapshotErr
	}
	if childErr != nil {
		return childErr
	}
	return rootErr
}

func windowsDescendants(root uint32) ([]uint32, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snapshot)
	children := map[uint32][]uint32{}
	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	if err = syscall.Process32First(snapshot, &entry); err == nil {
		for {
			children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
			entry.Size = uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))
			if err = syscall.Process32Next(snapshot, &entry); err != nil {
				break
			}
		}
	}
	var result []uint32
	var collect func(uint32)
	collect = func(pid uint32) {
		for _, child := range children[pid] {
			result = append(result, child)
			collect(child)
		}
	}
	collect(root)
	return result, nil
}

func terminateWindowsPID(pid uint32) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	return syscall.TerminateProcess(handle, 1)
}
