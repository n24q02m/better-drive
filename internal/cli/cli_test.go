package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/spf13/cobra"
)

func TestRootHasSubcommands(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"setup", "run", "status", "sync", "install", "uninstall"} {
		if !bytes.Contains(buf.Bytes(), []byte(sub)) {
			t.Errorf("help missing subcommand %q", sub)
		}
	}
}

// TestStatusCmdPrintsAllPairs verifies `better-drive status` with a
// multi-pair config prints one "pair: ..." line per [[pair]] block (not just
// the first, as the pre-multi-pair implementation did with Pairs[0]).
// BETTER_DRIVE_CONFIG points paths.ConfigFile() at a throwaway config so this
// never touches a real user config and works cross-platform (CI runs on Linux
// where os.UserConfigDir uses $HOME/.config, not AppData).
func TestStatusCmdPrintsAllPairs(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[pair]]
local = "C:/pair0"
remote = "gdrive:pair0"
interval = "30s"

[[pair]]
local = "C:/pair1"
remote = "gdrive:pair1"
interval = "1m"
mode = "copy"
exclude = ["node_modules/"]
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"C:/pair0", "gdrive:pair0", "[mode=bisync]", "C:/pair1", "gdrive:pair1", "[mode=copy]"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

// fakeCLISyncer is a syncloop.Syncer test double for runSyncOnce: it never
// makes a real rc/network call, so `sync` command tests stay offline. errByRemote
// lets a specific pair (keyed by its Remote string) fail while others succeed.
type fakeCLISyncer struct {
	errByRemote map[string]error
}

func (f *fakeCLISyncer) Bisync(p engine.BisyncParams) (engine.BisyncResult, error) {
	return engine.BisyncResult{}, f.errByRemote[p.Path2]
}
func (f *fakeCLISyncer) Copy(p engine.CopyParams) error { return f.errByRemote[p.Remote] }
func (f *fakeCLISyncer) Sync(p engine.CopyParams) error { return f.errByRemote[p.Remote] }

// TestRunSyncOnceReportsPerPairAndFailsOnAnyError verifies runSyncOnce (the
// shared body behind the `sync` CLI command) runs one RunOnce cycle per
// configured pair, prints an OK/FAILED line for each, and returns a non-nil
// error when any pair fails - while still running (and reporting) every pair,
// not stopping at the first failure.
func TestRunSyncOnceReportsPerPairAndFailsOnAnyError(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: t.TempDir(), Remote: "gdrive:ok", Interval: time.Second, Mode: "copy"},
		{Local: t.TempDir(), Remote: "gdrive:bad", Interval: time.Second, Mode: "sync"},
	}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runSyncOnce(cmd, s, cfg)
	if err == nil {
		t.Fatal("want non-nil error when a pair fails")
	}
	out := buf.String()
	if !strings.Contains(out, "gdrive:ok") || !strings.Contains(out, "OK") {
		t.Errorf("missing ok-pair line; got:\n%s", out)
	}
	if !strings.Contains(out, "gdrive:bad") || !strings.Contains(out, "FAILED") || !strings.Contains(out, "boom") {
		t.Errorf("missing failed-pair line with its error; got:\n%s", out)
	}
}

// TestRunSyncOnceAllOkReturnsNil verifies runSyncOnce returns nil (exit 0)
// when every pair's RunOnce succeeds.
func TestRunSyncOnceAllOkReturnsNil(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: t.TempDir(), Remote: "gdrive:a", Interval: time.Second, Mode: "copy"},
		{Local: t.TempDir(), Remote: "gdrive:b", Interval: time.Second, Mode: "bisync"},
	}}
	s := &fakeCLISyncer{}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	if err := runSyncOnce(cmd, s, cfg); err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "gdrive:a") || !strings.Contains(out, "gdrive:b") {
		t.Errorf("missing a per-pair line; got:\n%s", out)
	}
}

// TestSyncCmdFailsOnInvalidConfigWithoutNetworkCall verifies `better-drive
// sync` is wired into the real cobra command tree and returns an error for an
// invalid config (0 pairs) BEFORE ever constructing a real engine.Engine - so
// this stays offline (no rc/network call reached) while still exercising the
// actual syncCmd RunE, not just runSyncOnce directly.
func TestSyncCmdFailsOnInvalidConfigWithoutNetworkCall(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil { // 0 pairs -> Validate fails
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"sync"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want error: config has 0 pairs, cfg.Validate() must fail")
	}
}
