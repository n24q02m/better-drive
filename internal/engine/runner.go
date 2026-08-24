package engine

import (
	"bytes"
	"context"
	"fmt"
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

type runtimePreflight func() (*runtimeGuard, error)

func (g *runtimeGuard) verifyChild(cmd *exec.Cmd) error {
	if g == nil {
		return nil
	}
	if g.executable == nil {
		return fmt.Errorf("runtime guard executable evidence is missing")
	}
	return verifyRuntimeChildImage(cmd, g.executable)
}

// execRunner returns a runner that inherits the caller environment. It remains
// available only for the explicit foreground mount compatibility path; sync
// commands use execRunnerWithEnvironment through NewVerified.
func execRunner(bin string) runner {
	return execRunnerWithEnvironment(bin, nil)
}

func execRunnerWithEnvironment(bin string, env []string) runner {
	return execRunnerWithEnvironmentAndPreflight(bin, env, nil)
}

func execRunnerWithEnvironmentAndPreflight(bin string, env []string, preflight runtimePreflight) runner {
	return func(args ...string) (string, string, error) {
		var guard *runtimeGuard
		if preflight != nil {
			var err error
			guard, err = preflight()
			if err != nil {
				return "", "", err
			}
		}
		defer guard.release()
		/* #nosec G204 */
		cmd := exec.Command(bin, args...)
		if env != nil {
			cmd.Env = append([]string(nil), env...)
		}
		hideConsole(cmd) // Windows: no console window flash per rclone invocation
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := runCommand(cmd, guard)
		return stdout.String(), stderr.String(), err
	}
}

// execStreamRunner returns a streaming runner that inherits the caller
// environment. It is retained for the explicit foreground mount path.
func execStreamRunner(bin string) streamRunner {
	return execStreamRunnerWithEnvironment(bin, nil)
}

func execStreamRunnerWithEnvironment(bin string, env []string) streamRunner {
	return execStreamRunnerWithEnvironmentAndPreflight(bin, env, nil)
}

func execStreamRunnerWithEnvironmentAndPreflight(bin string, env []string, preflight runtimePreflight) streamRunner {
	return func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		var guard *runtimeGuard
		if preflight != nil {
			var err error
			guard, err = preflight()
			if err != nil {
				return err
			}
		}
		defer guard.release()
		/* #nosec G204 */
		cmd := exec.CommandContext(ctx, bin, args...)
		if env != nil {
			cmd.Env = append([]string(nil), env...)
		}
		hideConsole(cmd) // Windows: no console window flash per rclone invocation
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err := runCommand(cmd, guard)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		return err
	}
}
