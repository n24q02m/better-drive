//go:build windows

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
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

// runCommand owns the complete Windows child process tree. CommandContext can
// kill the direct child when its context is canceled, but an abrupt parent
// termination bypasses that path. A kill-on-close Job Object prevents a
// foreground mount from leaving rclone and its filesystem driver behind.
func runCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create rclone job object: %w", err)
	}
	defer windows.CloseHandle(job)

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return fmt.Errorf("configure rclone job object: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("open rclone process for job object: %w", err)
	}
	assignErr := windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if assignErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("assign rclone process to job object: %w", assignErr)
	}

	return cmd.Wait()
}

// resolveRcloneExecutable bypasses a Scoop shim only for an absolute executable
// path returned by a successful lookup. CommandContext can then own the real
// rclone process; killing only the shim otherwise leaves its child running after
// cancellation. Rejecting relative paths also prevents a cwd-controlled
// rclone.shim from being read after LookPath fails or returns ErrDot.
func resolveRcloneExecutable(bin string) string {
	if !filepath.IsAbs(bin) {
		return bin
	}
	ext := filepath.Ext(bin)
	shimPath := strings.TrimSuffix(bin, ext) + ".shim"
	data, err := os.ReadFile(shimPath)
	if err != nil {
		return bin
	}
	strData := string(data)
	// ⚡ Bolt: Use strings.Cut iteratively instead of strings.Split to eliminate
	// slice allocations and GC overhead when parsing the shim configuration file.
	for len(strData) > 0 {
		var line string
		line, strData, _ = strings.Cut(strData, "\n")
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
