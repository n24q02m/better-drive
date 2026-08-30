//go:build windows

package cli

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func duplicateInheritedTestDescriptor(t *testing.T, file *os.File) uintptr {
	t.Helper()
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		process,
		windows.Handle(file.Fd()),
		process,
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		t.Fatal(err)
	}
	return uintptr(duplicate)
}
