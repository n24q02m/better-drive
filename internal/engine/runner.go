package engine

import (
	"bytes"
	"os/exec"
)

// runner runs an rclone subcommand (already including any leading --config
// flag) and returns its captured stdout, stderr, and exit error. Engine calls
// through this seam instead of os/exec directly so tests can inject a fake
// that asserts the constructed argv without a real rclone binary.
type runner func(args ...string) (stdout string, stderr string, err error)

// execRunner returns a runner that shells out to the rclone binary at bin via
// os/exec, capturing stdout and stderr into separate buffers.
func execRunner(bin string) runner {
	return func(args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		hideConsole(cmd) // Windows: no console window flash per rclone invocation
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
}
