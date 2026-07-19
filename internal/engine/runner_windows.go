//go:build windows

package engine

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the CREATE_NO_WINDOW process-creation flag: run a
// console-mode child without allocating a console window.
const createNoWindow = 0x08000000

// hideConsole stops each rclone invocation from popping a visible console
// window. The daemon runs as a GUI-subsystem (-H windowsgui) process with no
// console of its own, so without this Windows would allocate a fresh console
// window for the console-mode rclone.exe on every sync cycle.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
