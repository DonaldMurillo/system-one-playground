//go:build windows

package sos

import (
	"fmt"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
	"unsafe"
)

const (
	windowsCreateSuspended             = 0x4
	windowsJobExtendedLimitInformation = 9
	windowsJobKillOnClose              = 0x2000
)

var (
	windowsKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	windowsNTDLL                    = syscall.NewLazyDLL("ntdll.dll")
	windowsCreateJobObject          = windowsKernel32.NewProc("CreateJobObjectW")
	windowsSetInformationJobObject  = windowsKernel32.NewProc("SetInformationJobObject")
	windowsAssignProcessToJobObject = windowsKernel32.NewProc("AssignProcessToJobObject")
	windowsCloseHandle              = windowsKernel32.NewProc("CloseHandle")
	windowsNtResumeProcess          = windowsNTDLL.NewProc("NtResumeProcess")
	windowsJobs                     sync.Map // *exec.Cmd -> syscall.Handle
)

type windowsBasicLimitInformation struct {
	PerProcessUserTimeLimit, PerJobUserTimeLimit int64
	LimitFlags                                   uint32
	MinimumWorkingSetSize, MaximumWorkingSetSize uintptr
	ActiveProcessLimit                           uint32
	Affinity                                     uintptr
	PriorityClass, SchedulingClass               uint32
}
type windowsIOCounters struct{ ReadOperationCount, WriteOperationCount, OtherOperationCount, ReadTransferCount, WriteTransferCount, OtherTransferCount uint64 }
type windowsExtendedLimitInformation struct {
	BasicLimitInformation                                                        windowsBasicLimitInformation
	IOInfo                                                                       windowsIOCounters
	ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed uintptr
}

func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windowsCreateSuspended}
}

// startProcessTree assigns the suspended child to a kill-on-close Job Object
// before it can spawn descendants. All later termination uses retained handles,
// eliminating Toolhelp PID snapshot and PID-reuse races.
func startProcessTree(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	job, _, callErr := windowsCreateJobObject.Call(0, 0)
	if job == 0 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("CreateJobObjectW: %w", callErr)
	}
	info := windowsExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = windowsJobKillOnClose
	if ok, _, err := windowsSetInformationJobObject.Call(job, windowsJobExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		windowsCloseHandle.Call(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}
	process, err := retainedWindowsProcessHandle(cmd)
	if err != nil {
		windowsCloseHandle.Call(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if ok, _, err := windowsAssignProcessToJobObject.Call(job, uintptr(process)); ok == 0 {
		windowsCloseHandle.Call(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	windowsJobs.Store(cmd, syscall.Handle(job))
	if status, _, err := windowsNtResumeProcess.Call(uintptr(process)); int32(status) < 0 {
		releaseProcessTree(cmd)
		_ = cmd.Wait()
		return fmt.Errorf("NtResumeProcess status %#x: %w", status, err)
	}
	return nil
}

// retainedWindowsProcessHandle returns the handle os.StartProcess received
// directly from CreateProcess. In Go 1.25 os.Process intentionally retains this
// handle for its whole lifetime, but does not expose it. Reading that retained
// handle is preferable to reopening the PID: reopening introduces a PID-reuse
// race between process creation and Job assignment.
func retainedWindowsProcessHandle(cmd *exec.Cmd) (syscall.Handle, error) {
	if cmd == nil || cmd.Process == nil {
		return 0, fmt.Errorf("process was not started")
	}
	field := reflect.ValueOf(cmd.Process).Elem().FieldByName("handle")
	if !field.IsValid() || field.IsNil() {
		return 0, fmt.Errorf("Go runtime did not retain the Windows process handle")
	}
	// os.processHandle starts with its native handle. The module pins Go 1.25;
	// the Windows cross-build test deliberately catches runtime layout changes.
	type processHandlePrefix struct{ handle uintptr }
	owned := (*processHandlePrefix)(unsafe.Pointer(field.Pointer())).handle
	if owned == 0 {
		return 0, fmt.Errorf("Go runtime retained an invalid Windows process handle")
	}
	return syscall.Handle(owned), nil
}

func releaseProcessTree(cmd *exec.Cmd) {
	if value, ok := windowsJobs.LoadAndDelete(cmd); ok {
		windowsCloseHandle.Call(uintptr(value.(syscall.Handle)))
	}
}
func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if _, ok := windowsJobs.Load(cmd); ok {
		releaseProcessTree(cmd)
		return nil
	}
	return cmd.Process.Kill()
}
func requestProcessTreeStop(_ *exec.Cmd) error { return syscall.Errno(1) }
