//go:build windows

package sos

import (
	"os/exec"
	"reflect"
	"testing"
)

func TestWindowsProcessTreeLaunchIsSuspendedForJobAssignment(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureProcessTree(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windowsCreateSuspended == 0 {
		t.Fatal("external process was not configured for suspended Job Object assignment")
	}
	info := windowsExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = windowsJobKillOnClose
	if info.BasicLimitInformation.LimitFlags&windowsJobKillOnClose == 0 {
		t.Fatal("Job Object does not use kill-on-close semantics")
	}
}

func TestWindowsProcessTreeUsesRetainedProcessHandle(t *testing.T) {
	// Guard the Go 1.25 os.Process layout used to avoid reopening a PID. A
	// missing retained handle field must fail at build/test time after upgrades.
	if _, ok := reflect.TypeOf(exec.Cmd{}.Process).Elem().FieldByName("handle"); !ok {
		t.Fatal("os.Process no longer exposes the retained handle layout")
	}
}
