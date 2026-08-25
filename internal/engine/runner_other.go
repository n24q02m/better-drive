//go:build !windows

package engine

import (
	"fmt"
	"os/exec"
)

// GUI-subsystem process execs a console-mode child.
func hideConsole(*exec.Cmd) {}

func resolveRcloneExecutable(bin string) string { return bin }

func runCommand(cmd *exec.Cmd, guard *runtimeGuard) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := guard.verifyChild(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("rclone child image verification: %w", err)
	}
	return cmd.Wait()
}
