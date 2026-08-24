package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"

	"github.com/n24q02m/better-drive/internal/config"
)

type runtimeFileEvidence struct {
	identity string
	acl      string
}

type runtimeChildImage struct {
	path     string
	identity string
}

type runtimeFile struct {
	path     string
	file     *os.File
	evidence runtimeFileEvidence
}

// runtimeGuard keeps the enrolled executable and config handles alive until
// the child has exited. The Windows implementation opens both paths without
// delete/write sharing, so replacement cannot race a scheduled invocation.
type runtimeGuard struct {
	executable *runtimeFile
	config     *runtimeFile
	once       sync.Once
}

func (g *runtimeGuard) release() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		if g.executable != nil && g.executable.file != nil {
			_ = g.executable.file.Close()
		}
		if g.config != nil && g.config.file != nil {
			_ = g.config.file.Close()
		}
	})
}

func runtimeACLBinding(executable, config string) string {
	return "executable=" + url.PathEscape(executable) + ";config=" + url.PathEscape(config)
}

func parseRuntimeACLBinding(value string) (executable, config string, err error) {
	parts := strings.Split(value, ";")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("rclone_runtime.acl must use executable=<evidence>;config=<evidence>")
	}
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		key, encoded, ok := strings.Cut(part, "=")
		if !ok || (key != "executable" && key != "config") || strings.TrimSpace(encoded) == "" {
			return "", "", fmt.Errorf("rclone_runtime.acl has invalid binding %q", part)
		}
		decoded, decodeErr := url.PathUnescape(encoded)
		if decodeErr != nil || decoded == "" {
			return "", "", fmt.Errorf("rclone_runtime.acl has invalid escaped evidence")
		}
		if _, exists := values[key]; exists {
			return "", "", fmt.Errorf("rclone_runtime.acl repeats %s evidence", key)
		}
		values[key] = decoded
	}
	executable, executableOK := values["executable"]
	config, configOK := values["config"]
	if !executableOK || !configOK {
		return "", "", fmt.Errorf("rclone_runtime.acl must bind executable and config evidence")
	}
	return executable, config, nil
}

// NewVerified constructs the transfer engine only from an enrolled runtime.
// It never performs PATH lookup and never inherits the caller's environment.
func NewVerified(runtime config.RcloneRuntime) (*Engine, error) {
	if !runtimeChildImageVerificationSupported(goruntime.GOOS) {
		return nil, fmt.Errorf("rclone runtime: child image verification unsupported on %s", goruntime.GOOS)
	}
	if err := runtime.Validate(); err != nil {
		return nil, fmt.Errorf("rclone runtime: %w", err)
	}
	env := explicitEnvironment(runtime.Environment)
	preflight := func() (*runtimeGuard, error) { return openRuntimeFiles(runtime) }
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
	guard, err := openRuntimeFiles(runtime)
	if err != nil {
		return err
	}
	guard.release()
	return nil
}

func runtimeEvidenceForPath(path string) (runtimeFileEvidence, error) {
	file, err := openRuntimeHandle(path)
	if err != nil {
		return runtimeFileEvidence{}, err
	}
	defer file.Close()
	return probeRuntimeHandle(path, file)
}

type runtimeEnrollment struct {
	executableFileID string
	configFileID     string
	acl              string
}

func enrollRuntimeFiles(executable, config string) (runtimeEnrollment, error) {
	executableEvidence, err := runtimeEvidenceForPath(executable)
	if err != nil {
		return runtimeEnrollment{}, fmt.Errorf("executable enrollment: %w", err)
	}
	configEvidence, err := runtimeEvidenceForPath(config)
	if err != nil {
		return runtimeEnrollment{}, fmt.Errorf("config enrollment: %w", err)
	}
	return runtimeEnrollment{
		executableFileID: executableEvidence.identity,
		configFileID:     configEvidence.identity,
		acl:              runtimeACLBinding(executableEvidence.acl, configEvidence.acl),
	}, nil
}

func openRuntimeFiles(runtime config.RcloneRuntime) (*runtimeGuard, error) {
	executableACL, configACL, err := parseRuntimeACLBinding(runtime.ACL)
	if err != nil {
		return nil, err
	}
	executable, err := openRuntimeFile("executable", runtime.Executable, runtime.ExecutableFileID, runtime.ExecutableDigest, executableACL)
	if err != nil {
		return nil, err
	}
	configFile, err := openRuntimeFile("config", runtime.Config, runtime.ConfigFileID, runtime.ConfigDigest, configACL)
	if err != nil {
		_ = executable.file.Close()
		return nil, err
	}
	return &runtimeGuard{executable: executable, config: configFile}, nil
}

func openRuntimeFile(label, path, expectedID, expectedDigest, expectedACL string) (*runtimeFile, error) {
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
	want, err := parseRuntimeDigest(label, expectedDigest)
	if err != nil {
		return nil, err
	}
	file, err := openRuntimeHandle(path)
	if err != nil {
		return nil, fmt.Errorf("rclone_runtime.%s open: %w", label, err)
	}
	evidence, err := probeRuntimeHandle(path, file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s evidence: %w", label, err)
	}
	if strings.TrimSpace(expectedID) == "" {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_file_id is unknown", label)
	}
	if evidence.identity != expectedID {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_file_id mismatch: got %q, want %q", label, evidence.identity, expectedID)
	}
	if strings.TrimSpace(expectedACL) == "" {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_acl is unknown", label)
	}
	if evidence.acl != expectedACL {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_acl mismatch: got %q, want %q", label, evidence.acl, expectedACL)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s seek: %w", label, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s hash: %w", label, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s rewind: %w", label, err)
	}
	if !equalBytes(hash.Sum(nil), want) {
		_ = file.Close()
		return nil, fmt.Errorf("rclone_runtime.%s_digest mismatch", label)
	}
	return &runtimeFile{path: path, file: file, evidence: evidence}, nil
}

func parseRuntimeDigest(label, expected string) ([]byte, error) {
	if !strings.HasPrefix(expected, "sha256:") {
		return nil, fmt.Errorf("rclone_runtime.%s_digest must use sha256:<hex>", label)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(expected, "sha256:"))
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("rclone_runtime.%s_digest is invalid", label)
	}
	return want, nil
}

func compareRuntimeChildImage(expectedPath, expectedIdentity string, actual runtimeChildImage) error {
	if strings.TrimSpace(actual.path) == "" {
		return fmt.Errorf("child image path is unknown")
	}
	if !sameRuntimePath(expectedPath, actual.path) {
		return fmt.Errorf("child image path mismatch: got %q, want %q", actual.path, expectedPath)
	}
	if strings.TrimSpace(actual.identity) == "" {
		return fmt.Errorf("child image identity is unknown")
	}
	if actual.identity != expectedIdentity {
		return fmt.Errorf("child image identity mismatch: got %q, want %q", actual.identity, expectedIdentity)
	}
	return nil
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

func cleanRuntimePath(path string) string {
	return filepath.Clean(strings.TrimSuffix(path, " (deleted)"))
}

func runtimeChildImageVerificationSupported(goos string) bool {
	return goos == "linux" || goos == "windows"
}
