//go:build !windows

package cli

import (
	"os"
	"syscall"
	"testing"
)

func duplicateInheritedTestDescriptor(t *testing.T, file *os.File) uintptr {
	t.Helper()
	descriptor, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return uintptr(descriptor)
}
