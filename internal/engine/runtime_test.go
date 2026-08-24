package engine

import (
	"path/filepath"
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
	runtime := validRuntimeForEngine(t)
	e, err := NewVerified(runtime)
	if err != nil {
		t.Fatalf("NewVerified: %v", err)
	}
	if got := e.args("listremotes"); len(got) < 2 || got[0] != "--config" || got[1] != runtime.Config {
		t.Fatalf("args = %#v, want explicit --config before command", got)
	}
}
