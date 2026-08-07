package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRcloneConfigExplicitWins(t *testing.T) {
	if got := ResolveRcloneConfig("X:/custom.conf"); got != "X:/custom.conf" {
		t.Fatalf("explicit path must win, got %q", got)
	}
}

func TestResolveRcloneConfigAutoDetectsExisting(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(conf, []byte("[gdrive]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// We can't directly call resolveFrom anymore, so we'll test ResolveRcloneConfig
	// by overriding APPDATA to our temp dir. Since scoop candidate won't exist in home,
	// it should fall back to APPDATA/rclone/rclone.conf.

	// Create the APPDATA structure
	appData := filepath.Join(dir, "AppData")
	rcloneDir := filepath.Join(appData, "rclone")
	if err := os.MkdirAll(rcloneDir, 0o700); err != nil {
		t.Fatal(err)
	}
	conf2 := filepath.Join(rcloneDir, "rclone.conf")
	if err := os.WriteFile(conf2, []byte("[gdrive]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("APPDATA", appData)

	// Temporarily override HOME to something empty so scoop doesn't match by accident
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // For Windows

	if got := ResolveRcloneConfig(""); got != conf2 {
		t.Fatalf("want first existing candidate %q, got %q", conf2, got)
	}
}

func TestResolveRcloneConfigNoneExists(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir()) // Empty dir, no rclone/rclone.conf
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	if got := ResolveRcloneConfig(""); got != "" {
		t.Fatalf("want empty when none exist, got %q", got)
	}
}
