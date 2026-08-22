package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/n24q02m/better-drive/internal/config"
)

// NewVerified constructs the transfer engine only from an enrolled runtime.
// It never performs PATH lookup and never inherits the caller's environment.
func NewVerified(runtime config.RcloneRuntime) (*Engine, error) {
	if err := runtime.Validate(); err != nil {
		return nil, fmt.Errorf("rclone runtime: %w", err)
	}
	env := explicitEnvironment(runtime.Environment)
	preflight := func() (func(), error) { return openRuntimeFiles(runtime) }
	return &Engine{
		bin:    runtime.Executable,
		cfg:    runtime.Config,
		run:    execRunnerWithEnvironmentAndPreflight(runtime.Executable, env, preflight),
		stream: execStreamRunnerWithEnvironmentAndPreflight(runtime.Executable, env, preflight),
	}, nil
}

func explicitEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func verifyRuntimeFiles(runtime config.RcloneRuntime) error {
	release, err := openRuntimeFiles(runtime)
	if err != nil {
		return err
	}
	release()
	return nil
}

func openRuntimeFiles(runtime config.RcloneRuntime) (func(), error) {
	executable, err := openRuntimeFile("executable", runtime.Executable, runtime.ExecutableDigest)
	if err != nil {
		return nil, err
	}
	configFile, err := openRuntimeFile("config", runtime.Config, runtime.ConfigDigest)
	if err != nil {
		_ = executable.Close()
		return nil, err
	}
	return func() {
		_ = executable.Close()
		_ = configFile.Close()
	}, nil
}

func openRuntimeFile(label, path, expected string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("rclone_runtime.%s readback: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("rclone_runtime.%s must not be a symlink", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rclone_runtime.%s must be a regular file", label)
	}
	if !strings.HasPrefix(expected, "sha256:") {
		return nil, fmt.Errorf("rclone_runtime.%s_digest must use sha256:<hex>", label)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(expected, "sha256:"))
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("rclone_runtime.%s_digest is invalid", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rclone_runtime.%s open: %w", label, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s hash: %w", label, err)
	}
	if !equalBytes(hash.Sum(nil), want) {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_digest mismatch", label)
	}
	return file, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
