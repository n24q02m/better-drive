//go:build windows

package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveRcloneExecutableUsesScoopShimTarget(t *testing.T) {
	dir := t.TempDir()
	shimExe := filepath.Join(dir, "rclone.exe")
	realExe := filepath.Join(dir, "rclone-real.exe")
	for _, path := range []string{shimExe, realExe} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shimConfig := filepath.Join(dir, "rclone.shim")
	if err := os.WriteFile(shimConfig, []byte("path = \""+realExe+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := resolveRcloneExecutable(shimExe); got != realExe {
		t.Fatalf("resolveRcloneExecutable(%q) = %q, want Scoop target %q", shimExe, got, realExe)
	}
}

func TestResolveRcloneExecutableFallsBackForInvalidShim(t *testing.T) {
	dir := t.TempDir()
	shimExe := filepath.Join(dir, "rclone.exe")
	if err := os.WriteFile(shimExe, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rclone.shim"), []byte("path = \"missing.exe\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := resolveRcloneExecutable(shimExe); got != shimExe {
		t.Fatalf("resolveRcloneExecutable(%q) = %q, want safe fallback", shimExe, got)
	}
}

func TestNewDoesNotResolveCWDShimAfterLookPathFailure(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("PATH", t.TempDir())

	attackerTarget := filepath.Join(cwd, "attacker-rclone.exe")
	if err := os.WriteFile(attackerTarget, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "rclone.shim"), []byte("path = \""+attackerTarget+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := New("").bin; got != "rclone" {
		t.Fatalf("New().bin = %q after LookPath failure, want bare fallback without reading cwd shim", got)
	}
}

func TestNewDoesNotResolveCWDShimAfterLookPathErrDot(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("PATH", "."+string(os.PathListSeparator)+t.TempDir())

	cwdRclone := filepath.Join(cwd, "rclone.exe")
	attackerTarget := filepath.Join(cwd, "attacker-rclone.exe")
	for _, path := range []string{cwdRclone, attackerTarget} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "rclone.shim"), []byte("path = \""+attackerTarget+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("rclone"); !errors.Is(err, exec.ErrDot) {
		t.Fatalf("adversarial fixture LookPath error = %v, want exec.ErrDot", err)
	}

	if got := New("").bin; got != "rclone" {
		t.Fatalf("New().bin = %q after LookPath ErrDot, want bare fallback without reading cwd shim", got)
	}
}
