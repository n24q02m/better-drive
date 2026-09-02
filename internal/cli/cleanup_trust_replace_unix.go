//go:build !windows

package cli

import "os"

func atomicReplaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
