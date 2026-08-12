package engine

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// runner runs an rclone subcommand (already including any leading --config
// flag) and returns its captured stdout, stderr, and exit error. Engine calls
// through this seam instead of os/exec directly so tests can inject a fake
// that asserts the constructed argv without a real rclone binary.
type runner func(args ...string) (stdout string, stderr string, err error)

// streamRunner runs a long-lived rclone subcommand with a context-aware
// foreground lifecycle, streaming stdout/stderr to the provided writers.
type streamRunner func(ctx context.Context, stdout, stderr io.Writer, args ...string) error

// execRunner returns a runner that shells out to the rclone binary at bin via
// os/exec, capturing stdout and stderr into separate buffers.
func execRunner(bin string) runner {
	return func(args ...string) (string, string, error) {
		/* #nosec G204 */
		cmd := exec.Command(bin, args...)
		hideConsole(cmd) // Windows: no console window flash per rclone invocation
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
}

// execStreamRunner returns a streaming runner that shells out to the rclone
// binary at bin via exec.CommandContext, keeping stdout/stderr live until the
// context is canceled or the child process exits.
func execStreamRunner(bin string) streamRunner {
	return func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		/* #nosec G204 */
		cmd := exec.CommandContext(ctx, bin, args...)
		hideConsole(cmd) // Windows: no console window flash per rclone invocation
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err := cmd.Run()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		return err
	}
}
