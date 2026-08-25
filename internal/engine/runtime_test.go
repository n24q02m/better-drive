package engine

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
)

func validRuntimeForEngine(t *testing.T) config.RcloneRuntime {
	t.Helper()
	runtimeDir := t.TempDir()
	return config.RcloneRuntime{
		Executable: filepath.Join(runtimeDir, "rclone"), ExecutableFileID: "exe-id", ExecutableDigest: "sha256:" + strings.Repeat("b", 64),
		Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: "owner-only",
		Config: filepath.Join(runtimeDir, "rclone.conf"), ConfigFileID: "cfg-id", ConfigDigest: "sha256:" + strings.Repeat("c", 64),
		AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
		Environment: map[string]string{"RCLONE_LOCAL_NO_CHECK_UPDATED": "true"},
	}
}

func enrolledRuntimeForEngine(t *testing.T) config.RcloneRuntime {
	t.Helper()
	runtime := validRuntimeForEngine(t)
	executableBody := []byte("rclone executable fixture")
	configBody := []byte("[gdrive]\ntype = drive\ntoken = mutable-oauth-token\n")
	if err := os.WriteFile(runtime.Executable, executableBody, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime.Config, configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	enrollment, err := enrollRuntimeFiles(runtime.Executable, runtime.Config)
	if err != nil {
		t.Fatal(err)
	}
	executableDigest := sha256.Sum256(executableBody)
	configDigest := sha256.Sum256(configBody)
	runtime.ExecutableFileID = enrollment.executableFileID
	runtime.ExecutableDigest = fmt.Sprintf("sha256:%x", executableDigest)
	runtime.ConfigFileID = enrollment.configFileID
	runtime.ConfigDigest = fmt.Sprintf("sha256:%x", configDigest)
	runtime.ACL = enrollment.acl
	return runtime
}

func TestNewVerifiedRejectsRuntimeBeforeAnyRunnerCanSpawn(t *testing.T) {
	runtime := validRuntimeForEngine(t)
	runtime.Executable = "rclone"
	if _, err := NewVerified(runtime); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("NewVerified error = %v, want absolute executable rejection", err)
	}
}

func TestNewVerifiedRejectsAmbientRcloneEnvironment(t *testing.T) {
	runtime := validRuntimeForEngine(t)
	runtime.Environment = map[string]string{"RCLONE_CONFIG": "C:/ambient.conf"}
	if _, err := NewVerified(runtime); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("NewVerified error = %v, want ambient environment rejection", err)
	}
}

func TestNewVerifiedPinsConfigBeforeEndpointCalls(t *testing.T) {
	goos := runtime.GOOS
	runtime := validRuntimeForEngine(t)
	e, err := NewVerified(runtime)
	if !runtimeChildImageVerificationSupported(goos) {
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("NewVerified on unsupported platform error = %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("NewVerified: %v", err)
	}
	if got := e.args("listremotes"); len(got) < 2 || got[0] != "--config" || got[1] != runtime.Config {
		t.Fatalf("args = %#v, want explicit --config before command", got)
	}
}

func TestNewTransferVerifiedStagesMutableConfigWithoutChangingEnrolledSource(t *testing.T) {
	if !runtimeChildImageVerificationSupported(runtime.GOOS) {
		t.Skipf("runtime child verification unsupported on %s", runtime.GOOS)
	}
	enrolled := enrolledRuntimeForEngine(t)
	sourceBefore, err := os.ReadFile(enrolled.Config)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewTransferVerified(enrolled)
	if err != nil {
		t.Fatalf("NewTransferVerified: %v", err)
	}
	if e.cfg == enrolled.Config {
		t.Fatal("transfer engine uses enrolled config directly; OAuth refresh would mutate or lock the evidence source")
	}
	workingDir := filepath.Dir(e.cfg)
	if got, err := os.ReadFile(e.cfg); err != nil || string(got) != string(sourceBefore) {
		t.Fatalf("working config = %q, %v; want exact enrolled source copy", got, err)
	}
	oldPath := e.cfg + ".old"
	if err := os.Rename(e.cfg, oldPath); err != nil {
		t.Fatalf("working config cannot be atomically replaced: %v", err)
	}
	if err := os.WriteFile(e.cfg, []byte("[gdrive]\ntoken = refreshed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(enrolled.Config); err != nil || string(got) != string(sourceBefore) {
		t.Fatalf("enrolled config changed = %q, %v", got, err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(workingDir); !os.IsNotExist(err) {
		t.Fatalf("working config directory survived Close: %v", err)
	}
}

func TestRuntimeChildImageVerificationPlatformPolicy(t *testing.T) {
	tests := map[string]bool{
		"linux":   true,
		"windows": true,
		"darwin":  false,
		"freebsd": false,
	}
	for goos, want := range tests {
		if got := runtimeChildImageVerificationSupported(goos); got != want {
			t.Errorf("runtimeChildImageVerificationSupported(%q) = %v, want %v", goos, got, want)
		}
	}
}
