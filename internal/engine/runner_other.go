//go:build !windows

package engine

import "os/exec"

// hideConsole is a no-op off Windows: only Windows pops a console window when a
// GUI-subsystem process execs a console-mode child.
func hideConsole(*exec.Cmd) {}
