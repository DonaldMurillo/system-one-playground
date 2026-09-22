//go:build !windows

package sosbuild

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
