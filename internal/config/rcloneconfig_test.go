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
	if got := resolveFrom("", []string{filepath.Join(dir, "nope.conf"), conf}); got != conf {
		t.Fatalf("want first existing candidate %q, got %q", conf, got)
	}
}

func TestResolveRcloneConfigNoneExists(t *testing.T) {
	if got := resolveFrom("", []string{"X:/a", "X:/b"}); got != "" {
		t.Fatalf("want empty when none exist, got %q", got)
	}
}
