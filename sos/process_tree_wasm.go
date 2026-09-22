//go:build js || wasip1

package sos

import (
	"fmt"
	"os/exec"
)

func configureProcessTree(_ *exec.Cmd) {}

func startProcessTree(cmd *exec.Cmd) error { return cmd.Start() }

func releaseProcessTree(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func requestProcessTreeStop(_ *exec.Cmd) error {
	return fmt.Errorf("process control is unavailable on this target")
}
