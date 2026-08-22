package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
)

func runtimeDigestForTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func enrolledRuntimeForFiles(t *testing.T, exe, cfg string) config.RcloneRuntime {
	t.Helper()
	return config.RcloneRuntime{
		Executable: exe, ExecutableFileID: "exe-id", ExecutableDigest: runtimeDigestForTest(t, exe),
		Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: "owner-only",
		Config: cfg, ConfigFileID: "cfg-id", ConfigDigest: runtimeDigestForTest(t, cfg),
		AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
	}
}

func TestVerifyRuntimeFilesRejectsExecutableReplacement(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(exe, []byte("original executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := enrolledRuntimeForFiles(t, exe, cfg)
	if err := verifyRuntimeFiles(runtime); err != nil {
		t.Fatalf("verifyRuntimeFiles initial: %v", err)
	}
	if err := os.WriteFile(exe, []byte("replacement executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeFiles(runtime); err == nil || !strings.Contains(err.Error(), "executable_digest") {
		t.Fatalf("verifyRuntimeFiles replacement error = %v, want digest mismatch", err)
	}
}

func TestVerifyRuntimeFilesRejectsNonRegularConfig(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(exe, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime := config.RcloneRuntime{
		Executable: exe, ExecutableFileID: "exe-id", ExecutableDigest: runtimeDigestForTest(t, exe),
		Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: "owner-only",
		Config: cfg, ConfigFileID: "cfg-id", ConfigDigest: "sha256:cfg", AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
	}
	if err := verifyRuntimeFiles(runtime); err == nil || !strings.Contains(err.Error(), "config") {
		t.Fatalf("verifyRuntimeFiles directory error = %v, want config regular-file rejection", err)
	}
}
