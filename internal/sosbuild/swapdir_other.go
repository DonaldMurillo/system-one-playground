//go:build !darwin && !(linux && (amd64 || arm64))

package sosbuild

import "fmt"

func atomicSwapDirectories(_, _ string) error {
	return fmt.Errorf("safe replacement of an existing bundle directory is unsupported on this platform; choose a new output path (the existing artifact was preserved)")
}
