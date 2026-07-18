package config

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestValidateAcceptsMultiplePairs verifies N (>1) pairs are accepted and all
// are loaded in order - the multi-pair replacement for backup-to-gdrive.ps1
// needs several independent [[pair]] blocks in one config.
func TestValidateAcceptsMultiplePairs(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local="a"
remote="gdrive:a"
interval="30s"
[[pair]]
local="b"
remote="gdrive:b"
interval="1m"
mode="copy"
[[pair]]
local="c"
remote="gdrive:c"
interval="5m"
mode="sync"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(c.Pairs) != 3 {
		t.Fatalf("len(Pairs)=%d, want 3", len(c.Pairs))
	}
	wantLocal := []string{"a", "b", "c"}
	wantMode := []string{"bisync", "copy", "sync"}
	for i, p := range c.Pairs {
		if p.Local != wantLocal[i] {
			t.Errorf("pair %d local=%q, want %q", i, p.Local, wantLocal[i])
		}
		if p.Mode != wantMode[i] {
			t.Errorf("pair %d mode=%q, want %q", i, p.Mode, wantMode[i])
		}
	}
}

// TestValidateRejectsAnyInvalidPairAmongMany verifies Validate rejects the
// whole config when ANY one of several pairs has an invalid mode, not just
// when there is a single pair.
func TestValidateRejectsAnyInvalidPairAmongMany(t *testing.T) {
	c := &Config{Pairs: []Pair{
		{Local: "a", Remote: "gdrive:a", Interval: 30 * time.Second, Mode: "bisync"},
		{Local: "b", Remote: "gdrive:b", Interval: 30 * time.Second, Mode: "mirror"}, // invalid
	}}
	if err := c.Validate(); err == nil {
		t.Fatal("want error when one of several pairs has an invalid mode, got nil")
	}
}

// TestValidateRejectsZeroPairs verifies an empty config (no [[pair]] blocks
// at all) is rejected.
func TestValidateRejectsZeroPairs(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err == nil {
		t.Fatal("want error for 0 pairs, got nil")
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

// TestLoadDefaultsModeToBisync verifies an omitted "mode" key defaults to
// "bisync" (v1 behaviour before mode support existed).
func TestLoadDefaultsModeToBisync(t *testing.T) {
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
	if got := c.Pairs[0].Mode; got != "bisync" {
		t.Fatalf("mode = %q, want bisync", got)
	}
}

// TestLoadAcceptsValidModes verifies "copy" and "sync" round-trip through
// Load unchanged and pass Validate.
func TestLoadAcceptsValidModes(t *testing.T) {
	for _, mode := range []string{"copy", "sync", "bisync"} {
		p := writeTemp(t, `
[[pair]]
local = "C:/Users/x/DriveSync"
remote = "gdrive:Backup"
interval = "30s"
mode = "`+mode+`"
`)
		c, err := Load(p)
		if err != nil {
			t.Fatalf("mode %q: load: %v", mode, err)
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("mode %q: validate: %v", mode, err)
		}
		if got := c.Pairs[0].Mode; got != mode {
			t.Fatalf("mode = %q, want %q", got, mode)
		}
	}
}

// TestValidateRejectsInvalidMode verifies an unrecognized mode string is
// rejected by Validate.
func TestValidateRejectsInvalidMode(t *testing.T) {
	c := &Config{Pairs: []Pair{{Local: "a", Remote: "gdrive:a", Interval: 30 * time.Second, Mode: "mirror"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("want error for invalid mode, got nil")
	}
}

// TestLoadParsesExclude verifies the config-level "exclude" toml key (a list
// of gitignore-syntax patterns) is parsed onto Pair.Exclude.
func TestLoadParsesExclude(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local = "C:/Users/x/.claude"
remote = "gdrive:Backups/claude"
interval = "30s"
mode = "copy"
exclude = ["node_modules/", ".venv/", "__pycache__/", ".git/"]
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := []string{"node_modules/", ".venv/", "__pycache__/", ".git/"}
	if !reflect.DeepEqual(c.Pairs[0].Exclude, want) {
		t.Fatalf("Exclude = %#v, want %#v", c.Pairs[0].Exclude, want)
	}
}

// TestLoadDefaultsExcludeToNil verifies a pair with no "exclude" key gets a
// nil Exclude (not an empty-but-non-nil slice, and not an error).
func TestLoadDefaultsExcludeToNil(t *testing.T) {
	p := writeTemp(t, `
[[pair]]
local = "a"
remote = "gdrive:a"
interval = "30s"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Pairs[0].Exclude != nil {
		t.Fatalf("Exclude = %#v, want nil", c.Pairs[0].Exclude)
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
