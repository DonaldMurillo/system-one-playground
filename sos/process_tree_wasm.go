//go:build js || wasip1

package sos

import "os/exec"

func configureProcessTree(_ *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
