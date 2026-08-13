//go:build windows

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// resolveRcloneExecutable bypasses a Scoop shim when its adjacent .shim file
// names a valid target. CommandContext can then own the real rclone process;
// killing only the shim otherwise leaves its child running after cancellation.
func resolveRcloneExecutable(bin string) string {
	ext := filepath.Ext(bin)
	shimPath := strings.TrimSuffix(bin, ext) + ".shim"
	data, err := os.ReadFile(shimPath)
	if err != nil {
		return bin
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(strings.TrimPrefix(key, "\ufeff")) != "path" {
			continue
		}
		target := strings.Trim(strings.TrimSpace(value), "\"")
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(shimPath), target)
		}
		info, statErr := os.Stat(target)
		if statErr == nil && !info.IsDir() {
			return target
		}
	}
	return bin
}
