//go:build windows

package main

import (
	"os"
	"syscall"
)

// attachParentConsole lets a GUI-subsystem (-H windowsgui) build still print to
// the terminal for CLI subcommands: it attaches to the parent process's console
// and rebinds os.Stdout/os.Stderr to it. When launched with no parent console
// (e.g. at login via the Run key, or the tray daemon), AttachConsole fails and
// this is a harmless no-op - the tray has no console, exactly as wanted.
func attachParentConsole() {
	const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS = -1 (DWORD 0xFFFFFFFF)
	k32 := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := k32.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return
	}
	if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil && h != 0 {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, err := syscall.GetStdHandle(syscall.STD_ERROR_HANDLE); err == nil && h != 0 {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
}
