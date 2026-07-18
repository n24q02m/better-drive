//go:build windows

package main

import (
	"os"
	"syscall"
)

const (
	attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS = -1 (DWORD 0xFFFFFFFF)
	fileTypeDisk        = 0x0001      // FILE_TYPE_DISK  - a real file (redirect)
	fileTypePipe        = 0x0003      // FILE_TYPE_PIPE  - a pipe (redirect)
)

// attachParentConsole lets a GUI-subsystem (-H windowsgui) build still print to
// the terminal for CLI subcommands. A windowsgui binary starts with no console,
// so its output would vanish; attaching to the parent process's console and
// rebinding os.Stdout/os.Stderr makes interactive CLI output appear. Launched
// with no parent console (at login via the Run key, or the tray daemon),
// AttachConsole fails and this is a harmless no-op - the tray stays
// console-less, exactly as wanted.
//
// Crucially, if stdout is ALREADY a real file or pipe (the caller redirected
// it, e.g. `better-drive sync > log` or a CI pipe), we must NOT rebind: doing
// so would send our output to the console and silently bypass the redirect. So
// we only attach when stdout is not a usable stream.
func attachParentConsole() {
	if isRedirected(syscall.STD_OUTPUT_HANDLE) {
		return // caller redirected stdout; leave os.Stdout/os.Stderr alone
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := k32.NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return // no parent console (login autostart / tray daemon) - stay silent
	}
	if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil && h != 0 {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, err := syscall.GetStdHandle(syscall.STD_ERROR_HANDLE); err == nil && h != 0 {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
}

// isRedirected reports whether the given std handle is backed by a real file or
// pipe (the caller redirected it), as opposed to a console or nothing.
func isRedirected(stdHandle int) bool {
	h, err := syscall.GetStdHandle(stdHandle)
	if err != nil || h == 0 || h == syscall.InvalidHandle {
		return false
	}
	k32 := syscall.NewLazyDLL("kernel32.dll")
	ft, _, _ := k32.NewProc("GetFileType").Call(uintptr(h))
	return ft == fileTypeDisk || ft == fileTypePipe
}
