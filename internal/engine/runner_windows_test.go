//go:build windows

package engine

import (
	"os"
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
