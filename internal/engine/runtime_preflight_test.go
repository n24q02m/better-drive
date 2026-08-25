package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
)

func requireRuntimeChildImageSupport(t *testing.T) {
	t.Helper()
	if !runtimeChildImageVerificationSupported(runtime.GOOS) {
		t.Skipf("runtime child image verification unsupported on %s", runtime.GOOS)
	}
}

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
	requireRuntimeChildImageSupport(t)
	t.Helper()
	enrollment, err := enrollRuntimeFiles(exe, cfg)
	if err != nil {
		t.Fatalf("enrollRuntimeFiles: %v", err)
	}
	return config.RcloneRuntime{
		Executable: exe, ExecutableFileID: enrollment.executableFileID, ExecutableDigest: runtimeDigestForTest(t, exe),
		Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: enrollment.acl,
		Config: cfg, ConfigFileID: enrollment.configFileID, ConfigDigest: runtimeDigestForTest(t, cfg),
		AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
	}
}

func TestVerifyRuntimeFilesAcceptsComputedIdentityAndACL(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(exe, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeFiles(enrolledRuntimeForFiles(t, exe, cfg)); err != nil {
		t.Fatalf("verifyRuntimeFiles: %v", err)
	}
}

func TestVerifyRuntimeFilesRejectsSameContentReplacementIdentityDrift(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	original := []byte("same executable bytes")
	if err := os.WriteFile(exe, original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := enrolledRuntimeForFiles(t, exe, cfg)
	replacement := exe + ".replacement"
	if err := os.WriteFile(replacement, original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, exe); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeFiles(runtime); err == nil || !strings.Contains(err.Error(), "executable_file_id") {
		t.Fatalf("verifyRuntimeFiles replacement error = %v, want executable identity mismatch", err)
	}
}

func TestVerifyRuntimeFilesRejectsACLOrModeDrift(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(exe, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := enrolledRuntimeForFiles(t, exe, cfg)
	runtime.ACL = runtime.ACL + "-drift"
	if err := verifyRuntimeFiles(runtime); err == nil || !strings.Contains(err.Error(), "acl") {
		t.Fatalf("verifyRuntimeFiles ACL drift error = %v, want ACL mismatch", err)
	}
}

func TestVerifyRuntimeFilesRejectsSymlink(t *testing.T) {
	requireRuntimeChildImageSupport(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	target := filepath.Join(dir, "target.exe")
	if err := os.WriteFile(target, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, exe); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runtime := config.RcloneRuntime{
		Executable: exe, ExecutableFileID: "unused", ExecutableDigest: runtimeDigestForTest(t, target),
		Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: runtimeACLBinding("unused", "unused"),
		Config: cfg, ConfigFileID: "unused", ConfigDigest: runtimeDigestForTest(t, cfg),
		AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
	}
	if err := verifyRuntimeFiles(runtime); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("verifyRuntimeFiles symlink error = %v, want symlink refusal", err)
	}
}

func TestOpenRuntimeFilesReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rclone.exe")
	cfg := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(exe, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[gdrive]\ntype = drive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err := openRuntimeFiles(enrolledRuntimeForFiles(t, exe, cfg))
	if err != nil {
		t.Fatalf("openRuntimeFiles: %v", err)
	}
	if guard == nil || guard.executable == nil || guard.config == nil {
		t.Fatal("openRuntimeFiles returned incomplete guard")
	}
	if guard.executable.file == nil || guard.config.file == nil {
		t.Fatal("guard did not retain executable/config handles")
	}
	guard.release()
	guard.release()
	if _, err := guard.executable.file.Stat(); err == nil {
		t.Fatal("executable handle remained open after release")
	}
	if _, err := guard.config.file.Stat(); err == nil {
		t.Fatal("config handle remained open after release")
	}
}
