package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/output"
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

// statusFixtureConfig writes a single-pair config to a temp file and points
// BETTER_DRIVE_CONFIG at it, returning the path (unused by callers so far,
// kept for symmetry with the other fixture helpers in this file).
func statusFixtureConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[pair]]
local = "C:/pair0"
remote = "gdrive:pair0"
interval = "30s"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)
	return cfgPath
}

// TestStatusCmd_TableFormatUnchanged verifies the default (no --format)
// output is byte-shape-identical to the pre-change format, so existing users
// and scripts see no difference.
func TestStatusCmd_TableFormatUnchanged(t *testing.T) {
	statusFixtureConfig(t)

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}

	if matched, _ := regexp.MatchString(`^pair: .+ <-> .+ every .+ \[mode=.+\]\n`, out.String()); !matched {
		t.Errorf("table output does not match the expected shape; got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "{") {
		t.Errorf("table format must not emit JSON; got:\n%s", out.String())
	}
}

// TestStatusCmd_JSONFormat verifies --format json emits a JSON array of
// output.PairStatus decodable by a machine consumer.
func TestStatusCmd_JSONFormat(t *testing.T) {
	statusFixtureConfig(t)

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v", err)
	}

	var got []output.PairStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got) == 0 {
		t.Fatal("want at least one pair, got none")
	}
	if got[0].Local == "" {
		t.Error("want a non-empty Local field")
	}
}

// fakeCLISyncer is a syncloop.Syncer test double for runSyncOnce: it never
// makes a real rc/network call, so `sync` command tests stay offline. errByRemote
// lets a specific pair (keyed by its Remote string) fail while others succeed.
// bisyncParams/copyParams record the params each call received, so a test can
// assert on e.g. DryRun without a real rclone invocation.
type fakeCLISyncer struct {
	errByRemote  map[string]error
	bisyncParams []engine.BisyncParams
	copyParams   []engine.CopyParams
}

func (f *fakeCLISyncer) Bisync(p engine.BisyncParams) (engine.BisyncResult, error) {
	f.bisyncParams = append(f.bisyncParams, p)
	return engine.BisyncResult{}, f.errByRemote[p.Path2]
}
func (f *fakeCLISyncer) Copy(p engine.CopyParams) error {
	f.copyParams = append(f.copyParams, p)
	return f.errByRemote[p.Remote]
}
func (f *fakeCLISyncer) Sync(p engine.CopyParams) error {
	f.copyParams = append(f.copyParams, p)
	return f.errByRemote[p.Remote]
}

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
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false)
	if err == nil {
		t.Fatal("want non-nil error when a pair fails")
	}
	if !strings.Contains(out.String(), "gdrive:ok") || !strings.Contains(out.String(), "OK") {
		t.Errorf("missing ok-pair line on stdout; got:\n%s", out.String())
	}
	// FAILED is a diagnostic, not a success result, so it belongs on stderr -
	// this assertion used to check stdout, which encoded the bug fixed here.
	if !strings.Contains(errOut.String(), "gdrive:bad") || !strings.Contains(errOut.String(), "FAILED") || !strings.Contains(errOut.String(), "boom") {
		t.Errorf("missing failed-pair line with its error on stderr; got:\n%s", errOut.String())
	}
}

// TestRunSyncOnce_FailuresGoToStderr verifies the AX contract: stdout carries
// only success (OK) lines, while FAILED (and SKIPPED) diagnostics go to
// stderr. Every agent consumer of `sync` (and Task 3's JSON renderer) depends
// on stdout staying success-only.
func TestRunSyncOnce_FailuresGoToStderr(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: t.TempDir(), Remote: "gdrive:bad", Interval: time.Second, Mode: "bisync"},
	}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false)
	if err == nil {
		t.Fatal("want non-nil error when a pair fails")
	}
	if strings.Contains(out.String(), "FAILED") {
		t.Errorf("stdout must not carry failure lines; got:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "FAILED") {
		t.Errorf("failures belong on stderr; got:\n%s", errOut.String())
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

	results, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false)
	if err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "gdrive:a") || !strings.Contains(out, "gdrive:b") {
		t.Errorf("missing a per-pair line; got:\n%s", out)
	}
	if len(results) != 2 || results[0].Status != "ok" || results[1].Status != "ok" {
		t.Errorf("results = %#v, want 2 ok results", results)
	}
}

// TestRunSyncOnce_JSONFormatEmitsResultsNotPerPairLines verifies the json
// format writes nothing per pair to stdout during the loop, then renders the
// full []output.PairResult once at the end - the table format's per-pair OK
// line must not leak into json mode.
func TestRunSyncOnce_JSONFormatEmitsResultsNotPerPairLines(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: t.TempDir(), Remote: "gdrive:a", Interval: time.Second, Mode: "copy"},
	}}
	s := &fakeCLISyncer{}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatJSON, false)
	if err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	if strings.Contains(out.String(), "OK\n") {
		t.Errorf("json format must not print a table-style OK line; got:\n%s", out.String())
	}
	var got []output.PairResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got) != 1 || got[0].Status != "ok" || got[0].Remote != "gdrive:a" {
		t.Errorf("got %#v, want one ok result for gdrive:a", got)
	}
	if len(results) != len(got) {
		t.Errorf("returned results len = %d, rendered json len = %d", len(results), len(got))
	}
}

// TestRunSyncOnce_DryRunThreadsToSyncerAndWarnsOnStderr verifies dryRun=true
// (a) prints the "dry-run: no changes will be made" banner to stderr before
// any pair runs, (b) is forwarded as DryRun on the params the Syncer
// receives, and (c) is echoed on each PairResult - all without applying any
// real change (the fake Syncer here never shells out to rclone).
func TestRunSyncOnce_DryRunThreadsToSyncerAndWarnsOnStderr(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: t.TempDir(), Remote: "gdrive:a", Interval: time.Second, Mode: "bisync"},
	}}
	s := &fakeCLISyncer{}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatTable, true)
	if err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	if !strings.Contains(errOut.String(), "dry-run: no changes will be made") {
		t.Errorf("missing dry-run banner on stderr; got:\n%s", errOut.String())
	}
	if len(s.bisyncParams) != 1 || !s.bisyncParams[0].DryRun {
		t.Fatalf("bisyncParams = %+v, want exactly 1 call with DryRun=true", s.bisyncParams)
	}
	if len(results) != 1 || !results[0].DryRun {
		t.Errorf("results = %+v, want DryRun=true", results)
	}
}

// TestSyncCmd_HasDryRunFlag verifies the `sync` command registers --dry-run
// (defaulting to false, so a plain `sync` keeps applying real changes).
func TestSyncCmd_HasDryRunFlag(t *testing.T) {
	c := syncCmd()
	f := c.Flags().Lookup("dry-run")
	if f == nil {
		t.Fatal("sync command has no --dry-run flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--dry-run default = %q, want %q", f.DefValue, "false")
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
