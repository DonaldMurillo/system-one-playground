//go:build !darwin && !linux

package sosbuild

import "fmt"

func atomicSwapDirectories(_, _ string) error {
	return fmt.Errorf("atomic replacement of an existing bundle directory is unsupported on this platform; choose a new output path")
}
