//go:build integration

package test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
)

// TestRoundTrip exercises a real bisync round-trip against a live Drive remote.
// It is gated behind BD_TEST_REMOTE_PATH because it requires a gdrive: remote
// already authenticated via a user-gated OAuth flow (see engine.CreateDriveRemote)
// - not something this test can set up itself.
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
	e := engine.NewForeground("")
	defer e.Close()

	// The .driveignore and files exist BEFORE the first sync, exactly as in real
	// use: the daemon always passes the current filters, including on the resync
	// run, so the baseline already reflects them.
	// Several keep files so that deleting one later stays well under bisync's
	// default --max-delete safety threshold (50%).
	const nKeep = 4
	for i := 0; i < nKeep; i++ {
		mustWrite(t, filepath.Join(local, fmt.Sprintf("keep%d.txt", i)), "hi")
	}
	mustWrite(t, filepath.Join(local, "skip.tmp"), "no") // excluded by .driveignore
	mustWrite(t, filepath.Join(local, ".driveignore"), "*.tmp\n")

	// exercise the real .driveignore -> rclone filter translation path
	filters, err := config.TranslateDriveIgnore(local)
	if err != nil {
		t.Fatalf("translate driveignore: %v", err)
	}

	// first run: resync baseline WITH filters -> syncs keep files up, excludes skip.tmp
	if _, err := e.Bisync(engine.BisyncParams{Path1: local, Path2: remotePath, Workdir: workdir, Resync: true, Filters: filters, Stderr: os.Stderr}); err != nil {
		t.Fatalf("resync: %v", err)
	}

	names, err := e.ListRemote(remotePath)
	if err != nil {
		t.Fatalf("list remote: %v", err)
	}
	if !contains(names, "keep0.txt") {
		t.Errorf("keep0.txt missing from remote listing %v", names)
	}
	if contains(names, "skip.tmp") {
		t.Errorf("skip.tmp present in remote listing %v, want filtered by .driveignore", names)
	}

	// Add a remote-only file through the same verified engine, then run a normal
	// bisync cycle and prove it arrives locally. The provider fixture remains
	// intact for the caller's exact-ID active-quarantine teardown.
	remoteSource := filepath.Join(t.TempDir(), "remote-only.txt")
	mustWrite(t, remoteSource, "from remote")
	if err := e.Copy(engine.CopyParams{Local: remoteSource, Remote: remotePath}); err != nil {
		t.Fatalf("seed remote-only file: %v", err)
	}
	if _, err := e.Bisync(engine.BisyncParams{Path1: local, Path2: remotePath, Workdir: workdir, Filters: filters, Stderr: os.Stderr}); err != nil {
		t.Fatalf("sync remote-only file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(local, "remote-only.txt"))
	if err != nil {
		t.Fatalf("read pulled remote-only file: %v", err)
	}
	if string(data) != "from remote" {
		t.Fatalf("pulled remote-only content = %q", data)
	}
	names2, err := e.ListRemote(remotePath)
	if err != nil {
		t.Fatalf("list remote after pull: %v", err)
	}
	for _, want := range []string{"keep0.txt", "keep1.txt", "remote-only.txt"} {
		if !contains(names2, want) {
			t.Errorf("%s missing from intact remote fixture %v", want, names2)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
