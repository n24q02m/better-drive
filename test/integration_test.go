//go:build integration

package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
)

// TestRoundTrip exercises a real bisync round-trip against a live Drive
// remote. It is gated behind BD_TEST_REMOTE_PATH because it requires a
// gdrive: remote already authenticated via a user-gated OAuth flow (see
// engine.CreateDriveRemote) - not something this test can set up itself.
//
// Run manually with:
//
//	BD_TEST_REMOTE_PATH="gdrive:better-drive-e2e" go test -tags integration ./test/ -v -run TestRoundTrip
func TestRoundTrip(t *testing.T) {
	remotePath := os.Getenv("BD_TEST_REMOTE_PATH") // e.g. "gdrive:better-drive-e2e"
	if remotePath == "" {
		t.Skip("set BD_TEST_REMOTE_PATH to a configured gdrive: remote+path to run")
	}
	local := t.TempDir()
	workdir := t.TempDir()
	e := engine.New()
	defer e.Close()

	// baseline
	if _, err := e.Bisync(engine.BisyncParams{Path1: local, Path2: remotePath, Workdir: workdir, Resync: true}); err != nil {
		t.Fatalf("resync: %v", err)
	}

	// local files: one to keep, one that .driveignore excludes
	if err := os.WriteFile(filepath.Join(local, "keep.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "skip.tmp"), []byte("no"), 0o600); err != nil {
		t.Fatalf("write skip.tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, ".driveignore"), []byte("*.tmp\n"), 0o600); err != nil {
		t.Fatalf("write .driveignore: %v", err)
	}

	// exercise the real .driveignore -> rclone filter translation path
	filters, err := config.TranslateDriveIgnore(local)
	if err != nil {
		t.Fatalf("translate driveignore: %v", err)
	}
	if _, err := e.Bisync(engine.BisyncParams{Path1: local, Path2: remotePath, Workdir: workdir, Filters: filters}); err != nil {
		t.Fatalf("sync up: %v", err)
	}

	// real assert: list the Drive folder and check keep.txt is present,
	// skip.tmp is not (filtered out by the translated .driveignore rule).
	names, err := e.ListRemote(remotePath)
	if err != nil {
		t.Fatalf("list remote: %v", err)
	}
	var hasKeep, hasSkip bool
	for _, n := range names {
		switch n {
		case "keep.txt":
			hasKeep = true
		case "skip.tmp":
			hasSkip = true
		}
	}
	if !hasKeep {
		t.Errorf("keep.txt missing from remote listing %v", names)
	}
	if hasSkip {
		t.Errorf("skip.tmp present in remote listing %v, want filtered by .driveignore", names)
	}
}
