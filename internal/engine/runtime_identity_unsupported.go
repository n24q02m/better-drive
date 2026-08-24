//go:build !windows && !linux

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func openRuntimeHandle(path string) (*os.File, error) {
	return nil, fmt.Errorf("runtime identity handles unsupported on %s", runtimeUnsupportedOS())
}

func probeRuntimeHandle(path string, file *os.File) (runtimeFileEvidence, error) {
	return runtimeFileEvidence{}, fmt.Errorf("runtime identity probe unsupported on %s", runtimeUnsupportedOS())
}

func sameRuntimePath(expected, actual string) bool {
	return filepath.Clean(cleanRuntimePath(expected)) == filepath.Clean(cleanRuntimePath(actual))
}

func verifyRuntimeChildImage(cmd *exec.Cmd, expected *runtimeFile) error {
	return fmt.Errorf("child image verification unsupported on %s", runtimeUnsupportedOS())
}

func runtimeUnsupportedOS() string {
	return runtime.GOOS
}
