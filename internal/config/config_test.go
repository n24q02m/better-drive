package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValidOnePair(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local = "C:/Users/x/DriveSync"
remote = "gdrive:Backup"
interval = "30s"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := c.Pairs[0].Interval; got != 30*time.Second {
		t.Fatalf("interval = %v, want 30s", got)
	}
}

func TestValidateRejectsMultiplePairs(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local="a"
remote="gdrive:a"
interval="30s"
[[pair]]
local="b"
remote="gdrive:b"
interval="30s"
`)
	c, _ := Load(p)
	if err := c.Validate(); err == nil {
		t.Fatal("want error for >1 pair, got nil")
	}
}

func TestLoadBadInterval(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local = "C:/Users/x/DriveSync"
remote = "gdrive:Backup"
interval = "notaduration"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("want error for bad interval, got nil")
	}
}

func TestValidateRejectsEmptyLocal(t *testing.T) {
	c := &Config{Pairs: []Pair{{Local: "", Remote: "gdrive:a", Interval: 30 * time.Second}}}
	if err := c.Validate(); err == nil {
		t.Fatal("want error for empty local, got nil")
	}
}

func TestValidateRejectsZeroInterval(t *testing.T) {
	c := &Config{Pairs: []Pair{{Local: "a", Remote: "gdrive:a", Interval: 0}}}
	if err := c.Validate(); err == nil {
		t.Fatal("want error for zero interval, got nil")
	}
}
