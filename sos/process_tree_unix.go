//go:build !windows && !js && !wasip1

package sos

import (
	"os/exec"
	"syscall"
)

func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startProcessTree(cmd *exec.Cmd) error { return cmd.Start() }

func releaseProcessTree(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// The process group is created atomically with the child, and descendants
	// keep that group alive if the leader exits before cancellation. Signaling
	// the negative PGID therefore avoids the much larger race in enumerating
	// descendants and killing recycled child PIDs individually. POSIX exposes no
	// portable retained process-group handle, so a kernel-level PGID reuse race
	// after every member has exited cannot be eliminated here; at that point
	// there are no descendants left for SOS to clean up. Linux pidfds do not
	// address process groups, while Windows uses retained process handles below.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func requestProcessTreeStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}
