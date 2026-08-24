package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/runlog"
	"github.com/n24q02m/better-drive/internal/state"
	"github.com/spf13/cobra"
)

type cliBindingResolver struct {
	role     config.BindingReadback
	policy   config.BindingReadback
	category config.BindingReadback
}

func (r cliBindingResolver) ReadRoleBinding(string) (config.BindingReadback, error) {
	return r.role, nil
}

func (r cliBindingResolver) ReadPolicyBinding(ref string) (config.BindingReadback, error) {
	if ref == r.category.Ref {
		return r.category, nil
	}
	return r.policy, nil
}

func TestValidateExecutionConfigWithBindingsRejectsFreshBindingDrift(t *testing.T) {
	source := t.TempDir()
	roleDigest := "sha256:" + strings.Repeat("a", 64)
	policyDigest := "sha256:" + strings.Repeat("b", 64)
	cfg := &config.Config{
		SchemaVersion: config.CurrentSchemaVersion,
		RoleBinding:   config.RoleBinding{RoleRef: "profile:home", RoleDigest: roleDigest, PolicyRef: "policy:home", PolicyDigest: policyDigest},
		RcloneRuntime: config.RcloneRuntime{
			Executable: filepath.Join(t.TempDir(), "rclone"), ExecutableFileID: "exe-id", ExecutableDigest: "sha256:" + strings.Repeat("c", 64),
			Version: "1.67.0", Provenance: "release", Signature: "sig", Owner: "role", ACL: "owner-only",
			Config: filepath.Join(t.TempDir(), "rclone.conf"), ConfigFileID: "cfg-id", ConfigDigest: "sha256:" + strings.Repeat("d", 64),
			AllowedRemotes: []string{"gdrive"}, AllowedBackends: []string{"drive"},
		},
		CategoryPolicies: []config.CategoryPolicy{{
			ID: "policy", Version: 1, Digest: policyDigest, BindingRef: "category-policy:policy", AllowlistedRoot: source,
			MandatoryDenylist: []string{"node_modules/"}, SizeGuard: config.CategorySizeGuard{MaxBytes: 1 << 20},
			RestoreExpectation: "empty-or-exact-hash",
		}},
		Jobs: []config.Job{{
			ID: "job", Source: source, Direction: "push", Mode: "copy", Required: true,
			CategoryPolicyID: "policy", CategoryPolicyVersion: 1, CategoryPolicyDigest: policyDigest,
			SymlinkPolicy: "preserve", Schedule: "30s", Interval: 30 * time.Second, Exclude: []string{"node_modules/"},
			Destinations: []config.Destination{{Backend: "drive", Path: "Backups/job", AccountID: "account", RootID: "root", CredentialRef: "rclone:gdrive", Required: true, MinCompleteRestoreSets: 2, DeletePolicy: "none"}},
		}},
	}
	resolver := cliBindingResolver{
		role:     config.BindingReadback{Ref: cfg.RoleBinding.RoleRef, Digest: cfg.RoleBinding.RoleDigest},
		policy:   config.BindingReadback{Ref: cfg.RoleBinding.PolicyRef, Digest: cfg.RoleBinding.PolicyDigest},
		category: config.BindingReadback{Ref: cfg.CategoryPolicies[0].BindingRef, Digest: cfg.CategoryPolicies[0].Digest},
	}
	if err := validateExecutionConfigWithBindings(cfg, resolver, resolver); err != nil {
		t.Fatalf("validateExecutionConfigWithBindings: %v", err)
	}
	resolver.role.Digest = "sha256:" + strings.Repeat("e", 64)
	if err := validateExecutionConfigWithBindings(cfg, resolver, resolver); err == nil || !strings.Contains(err.Error(), "role binding") {
		t.Fatalf("drift error = %v, want role binding rejection", err)
	}
}

// TestRootCmd_HasCompletionCommand verifies the root command registers the
// standard cobra shell-completion subcommand (`completion bash|zsh|fish|
// powershell`), so an agent or a shell can discover and script against it.
func TestRootCmd_HasCompletionCommand(t *testing.T) {
	root := newRootCmd()
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	found := false
	for _, n := range names {
		if n == "completion" {
			found = true
		}
	}
	if !found {
		t.Errorf("root commands = %v, want \"completion\" among them", names)
	}
}

// TestEveryCommandHasLongAndExample verifies every user-facing command
// documents itself beyond its one-line Short description, so `--help` (and an
// agent reading it) has enough context to use the command correctly.
func TestEveryCommandHasLongAndExample(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "completion" || c.Name() == "help" {
			continue
		}
		if c.Long == "" {
			t.Errorf("command %q needs a Long description", c.Name())
		}
		if c.Example == "" {
			t.Errorf("command %q needs an Example", c.Name())
		}
	}
}

func TestRootHasSubcommands(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"setup", "run", "status", "sync", "mount", "install", "uninstall"} {
		if !bytes.Contains(buf.Bytes(), []byte(sub)) {
			t.Errorf("help missing subcommand %q", sub)
		}
	}
}

func TestRootTaglineIncludesSyncAndMount(t *testing.T) {
	short := strings.ToLower(newRootCmd().Short)
	for _, capability := range []string{"sync", "mount"} {
		if !strings.Contains(short, capability) {
			t.Errorf("root tagline %q does not mention %s", short, capability)
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
schema_version = 2

[[job]]
id = "job-pair0"
source = "C:/pair0"
direction = "push"
mode = "copy"
required = true
category_policy_id = "test-policy"
category_policy_version = 1
category_policy_digest = "sha256:test"
symlink_policy = "preserve"
schedule = "30s"
[[job.destination]]
backend = "drive"
path = "pair0"
account_id = "test-account"
root_id = "test-root"
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"

[[job]]
id = "job-pair1"
source = "C:/pair1"
direction = "push"
mode = "copy"
required = true
category_policy_id = "test-policy"
category_policy_version = 1
category_policy_digest = "sha256:test"
symlink_policy = "preserve"
schedule = "1m"
exclude = ["node_modules/"]
[[job.destination]]
backend = "drive"
path = "pair1"
account_id = "test-account"
root_id = "test-root-1"
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"
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
	for _, want := range []string{"job-pair0", "C:/pair0", "gdrive:pair0", "[mode=copy]", "job-pair1", "C:/pair1", "gdrive:pair1"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

// statusFixtureConfig writes a single-job v2 config to a temp file and points
// BETTER_DRIVE_CONFIG at it, returning the path (unused by callers so far,
// kept for symmetry with the other fixture helpers in this file).
func statusFixtureConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	body := `
schema_version = 2
[[job]]
id = "job-pair0"
source = "C:/pair0"
direction = "push"
mode = "copy"
required = true
category_policy_id = "test-policy"
category_policy_version = 1
category_policy_digest = "sha256:test"
symlink_policy = "preserve"
schedule = "30s"
[[job.destination]]
backend = "drive"
path = "pair0"
account_id = "test-account"
root_id = "test-root"
credential_ref = "rclone:gdrive"
required = true
min_complete_restore_sets = 2
delete_policy = "none"
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
	// SetArgs(nil) would make cobra fall back to the REAL os.Args[1:] of the
	// test binary process (e.g. "-covermode=atomic" under `go test -cover`),
	// which pflag then tries to parse as a flag on this command - pass an
	// explicit empty (non-nil) slice to mean "no args" instead.
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}

	if matched, _ := regexp.MatchString(`^job .+: .+ <-> .+ every .+ \[mode=.+\]\n`, out.String()); !matched {
		t.Errorf("table output does not match the expected shape; got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "{") {
		t.Errorf("table format must not emit JSON; got:\n%s", out.String())
	}
}

// TestStatusCmd_JSONFormat verifies --format json emits a nested StatusEnvelope
// with scheduler and pairs decodable by a machine consumer.
func TestStatusCmd_JSONFormat(t *testing.T) {
	statusFixtureConfig(t)

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v", err)
	}

	var got output.StatusEnvelope
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got.Pairs) == 0 {
		t.Fatal("want at least one pair, got none")
	}
	if got.Pairs[0].Local == "" {
		t.Error("want a non-empty Local field")
	}
	if got.Scheduler.Health == "" {
		t.Error("want a non-empty Scheduler.Health field")
	}
}

func TestStatusCmdShowsLegacyConfigWithValidationWarning(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[pair]]
local = "C:/legacy"
remote = "gdrive:Legacy"
interval = "1h"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)
	t.Setenv("BETTER_DRIVE_STATE", filepath.Join(t.TempDir(), "missing-state.json"))

	var out, errOut bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v; stderr=%s", err, errOut.String())
	}
	var got output.StatusEnvelope
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if len(got.Pairs) != 1 || got.Pairs[0].JobID == "" || len(got.Pairs[0].Warnings) == 0 {
		t.Fatalf("legacy status=%#v, want one pair with validation warning", got)
	}
}

func TestStatusCmdMissingStateUsesCanonicalMissingSchedulerEnvelope(t *testing.T) {
	statusFixtureConfig(t)
	t.Setenv("BETTER_DRIVE_STATE", filepath.Join(t.TempDir(), "missing-state.json"))

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v", err)
	}
	var got output.StatusEnvelope
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; output=%s", err, out.String())
	}
	if got.Scheduler.Health != state.HealthMissing {
		t.Fatalf("scheduler health = %q, want %q", got.Scheduler.Health, state.HealthMissing)
	}
	if got.Scheduler.Enabled || got.Scheduler.ActiveInstance != "" || got.Scheduler.Owner != "" {
		t.Fatalf("missing scheduler fabricated active state: %#v", got.Scheduler)
	}
	if len(got.Pairs) != 1 || got.Pairs[0].Health != state.HealthMissing {
		t.Fatalf("pair health = %#v, want canonical missing", got.Pairs)
	}
}

func TestStatusCmdWarnsWhenConfigIsNotExecutionReady(t *testing.T) {
	statusFixtureConfig(t)
	t.Setenv("BETTER_DRIVE_STATE", filepath.Join(t.TempDir(), "missing-state.json"))

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v", err)
	}
	var got output.StatusEnvelope
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if len(got.Pairs) != 1 || len(got.Pairs[0].Warnings) == 0 || !strings.Contains(got.Pairs[0].Warnings[0], "category policy registry") {
		t.Fatalf("status = %#v, want execution-readiness warning", got)
	}
}

func TestStatusCmdReevaluatesPersistedSchedulerFreshness(t *testing.T) {
	statusFixtureConfig(t)
	now := time.Now().UTC()
	persisted := state.State{
		SchemaVersion: state.CurrentSchemaVersion,
		EngineVersion: "test",
		Jobs:          []state.JobState{{JobID: "job-pair0", Status: "ok", LastSuccess: now.Add(-2 * time.Hour), NextDue: now.Add(30 * time.Minute), ObjectCount: 3, ByteCount: 42}},
		Scheduler: state.SchedulerState{
			Owner: "better-drive", OwnerJobID: "job-pair0", Enabled: true,
			ObservedAt: now.Add(-2 * time.Hour), FreshnessWindow: time.Minute,
			CatchUpGrace: time.Hour, ActiveInstance: "one-shot", OverlapState: state.OverlapNone,
			OverlapHealth: "ok", Health: state.HealthHealthy,
		},
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := state.Save(statePath, persisted); err != nil {
		t.Fatalf("save state: %v", err)
	}
	t.Setenv("BETTER_DRIVE_STATE", statePath)

	var out bytes.Buffer
	cmd := statusCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --format json: %v", err)
	}
	var got output.StatusEnvelope
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if len(got.Pairs) != 1 || got.Pairs[0].Health != state.HealthStale || got.Scheduler.Health != state.HealthStale {
		t.Fatalf("status=%#v, want stale pair and scheduler", got)
	}

	if got.Pairs[0].ObjectCount != 3 || got.Pairs[0].ByteCount != 42 || got.Pairs[0].NextDue == nil {
		t.Fatalf("status evidence=%#v, want persisted counts and next_due", got.Pairs[0])
	}
}

// TestStatusCmd_BadConfigTOML_ErrorHasRemediation verifies an unparseable
// config.toml fails with a ConfigError (code 2) carrying a remediation hint
// that names the resolved config path, so a --format json caller gets an
// actionable "remediation" field instead of just the raw decode error.
func TestStatusCmd_BadConfigTOML_ErrorHasRemediation(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("not valid toml [["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)

	cmd := statusCmd()
	cmd.SetOut(&bytes.Buffer{})
	// statusCmd() alone (not the full newRootCmd() tree) has no SilenceErrors
	// of its own - that's set once on root in newRootCmd() - so without
	// capturing stderr here, cobra's own default error print would leak onto
	// this test process's real stderr instead of just returning err.
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want error for unparseable config.toml")
	}
	if got := exitcode.Code(err); got != exitcode.ConfigErrorCode {
		t.Errorf("Code = %d, want %d", got, exitcode.ConfigErrorCode)
	}
	hint := exitcode.RemediationOf(err)
	if hint == "" {
		t.Fatal("want a non-empty remediation hint")
	}
	if !strings.Contains(hint, cfgPath) {
		t.Errorf("remediation %q does not mention the config path %q", hint, cfgPath)
	}
}

// TestSyncCmd_InvalidConfig_ErrorHasRemediation verifies cfg.Validate()
// failures (e.g. 0 pairs) also carry a remediation hint pointing at the
// config path, mirroring the config.Load failure case above.
func TestSyncCmd_InvalidConfig_ErrorHasRemediation(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil { // 0 pairs
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", cfgPath)

	cmd := syncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{}) // see the matching comment in the statusCmd test above
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want error: config has 0 pairs")
	}
	if got := exitcode.Code(err); got != exitcode.ConfigErrorCode {
		t.Errorf("Code = %d, want %d", got, exitcode.ConfigErrorCode)
	}
	if hint := exitcode.RemediationOf(err); hint == "" || !strings.Contains(hint, cfgPath) {
		t.Errorf("remediation = %q, want a hint mentioning %q", hint, cfgPath)
	}
}

// TestStatusCmd_BadFormatFlag_ErrorHasRemediation and its sync counterpart
// verify an unknown --format value fails with a remediation hint naming the
// two accepted values, so a caller doesn't have to guess.
func TestStatusCmd_BadFormatFlag_ErrorHasRemediation(t *testing.T) {
	statusFixtureConfig(t)
	cmd := statusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{}) // see the matching comment in TestStatusCmd_BadConfigTOML_ErrorHasRemediation
	cmd.SetArgs([]string{"--format", "yaml"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want error for unknown --format value")
	}
	hint := exitcode.RemediationOf(err)
	if !strings.Contains(hint, "table") || !strings.Contains(hint, "json") {
		t.Errorf("remediation = %q, want it to mention both table and json", hint)
	}
}

func TestSyncCmd_BadFormatFlag_ErrorHasRemediation(t *testing.T) {
	statusFixtureConfig(t)
	cmd := syncCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--format", "yaml"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("want error for unknown --format value")
	}
	hint := exitcode.RemediationOf(err)
	if !strings.Contains(hint, "table") || !strings.Contains(hint, "json") {
		t.Errorf("remediation = %q, want it to mention both table and json", hint)
	}
}

// TestRemoteNotConfiguredErr verifies the helper behind runCmd's
// remote-configured gate: code 3, and a remediation hint that is the exact
// command to fix it (not just a description) -- extracted to a pure function
// so this doesn't need a real engine.Engine/rclone binary to test, the same
// reasoning runSyncOnce documents for its own Syncer injection.
func TestRemoteNotConfiguredErr(t *testing.T) {
	err := remoteNotConfiguredErr("gdrive-work")
	if got := exitcode.Code(err); got != exitcode.RemoteNotConfigured {
		t.Errorf("Code = %d, want %d", got, exitcode.RemoteNotConfigured)
	}
	if !strings.Contains(err.Error(), "gdrive-work") {
		t.Errorf("Error() = %q, want it to mention the remote name", err.Error())
	}
	if want := "run: better-drive setup --remote gdrive-work"; exitcode.RemediationOf(err) != want {
		t.Errorf("remediation = %q, want %q", exitcode.RemediationOf(err), want)
	}
}

// TestRunSyncOnce_SyncFailedErrorHasRemediation verifies the error
// runSyncOnce returns when a pair fails carries a remediation hint (not just
// the bare "one or more pairs failed" message).
func TestRunSyncOnce_SyncFailedErrorHasRemediation(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJob(t.TempDir(), "gdrive:bad")}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
	if err == nil {
		t.Fatal("want non-nil error")
	}
	if got := exitcode.Code(err); got != exitcode.SyncFailedCode {
		t.Errorf("Code = %d, want %d", got, exitcode.SyncFailedCode)
	}
	if exitcode.RemediationOf(err) == "" {
		t.Error("want a non-empty remediation hint")
	}
}

// TestNewRootCmd_SilencesCobraDefaultErrorOutput verifies the root command
// disables cobra's own "Error: ..." + Usage auto-print on a failing RunE, so
// cli.RenderError (via Execute) is the ONLY thing that writes to stderr on
// failure -- otherwise a --format json caller would see cobra's plain-text
// "Error: ..." + a full Usage: block ahead of (or instead of) the JSON
// envelope, exactly as reproduced manually before this change:
// `better-drive status --format json` with a bad config printed cobra's
// "Error: ..." + Usage, THEN main.go's own "error: ..." line - never JSON.
func TestNewRootCmd_SilencesCobraDefaultErrorOutput(t *testing.T) {
	root := newRootCmd()
	if !root.SilenceErrors {
		t.Error("SilenceErrors = false, want true")
	}
	if !root.SilenceUsage {
		t.Error("SilenceUsage = false, want true")
	}
}

// TestExecute_ReadsFormatOffTheCommandThatRan verifies execute() (the
// unexported, args-injectable body behind Execute()) returns the --format
// value actually parsed for the subcommand that ran, using a controlled args
// slice rather than SetArgs(nil) -- see the os.Args[1:] hazard noted on
// TestStatusCmd_TableFormatUnchanged's SetArgs([]string{}) comment above:
// bare os.Args[1:] under `go test` carries -test.* flags that pflag would
// choke on.
func TestExecute_ReadsFormatOffTheCommandThatRan(t *testing.T) {
	statusFixtureConfig(t)
	format, err := execute([]string{"status", "--format", "json"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if format != output.FormatJSON {
		t.Errorf("format = %q, want %q", format, output.FormatJSON)
	}
}

// TestExecute_FormatDefaultsToTableWithoutAFormatFlag verifies a command with
// no --format flag at all (or one that never resolved to a real subcommand)
// reports "table" rather than leaving format empty, since RenderError treats
// anything other than "json" as the table format.
func TestExecute_FormatDefaultsToTableWithoutAFormatFlag(t *testing.T) {
	format, err := execute([]string{"not-a-real-subcommand"})
	if err == nil {
		t.Fatal("want an error for an unknown subcommand")
	}
	if format != output.FormatTable {
		t.Errorf("format = %q, want %q", format, output.FormatTable)
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
	onCall       func()
}

func (f *fakeCLISyncer) CountSourceObjects(context.Context, string, []string, io.Writer) (int64, error) {
	return 1, nil
}

func (f *fakeCLISyncer) Bisync(p engine.BisyncParams) (engine.BisyncResult, error) {
	f.bisyncParams = append(f.bisyncParams, p)
	if f.onCall != nil {
		f.onCall()
	}
	return engine.BisyncResult{}, f.errByRemote[p.Path2]
}
func (f *fakeCLISyncer) Copy(p engine.CopyParams) error {
	f.copyParams = append(f.copyParams, p)
	if f.onCall != nil {
		f.onCall()
	}
	return f.errByRemote[p.Remote]
}
func (f *fakeCLISyncer) Sync(p engine.CopyParams) error {
	f.copyParams = append(f.copyParams, p)
	if f.onCall != nil {
		f.onCall()
	}
	return f.errByRemote[p.Remote]
}

// TestRunSyncOnceReportsPerPairAndFailsOnAnyError verifies runSyncOnce (the
// shared body behind the `sync` CLI command) runs one RunOnce cycle per
// configured pair, prints an OK/FAILED line for each, and returns a non-nil
// error when any pair fails - while still running (and reporting) every pair,
// not stopping at the first failure.
func TestRunSyncOnceReportsPerPairAndFailsOnAnyError(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{
		testJob(t.TempDir(), "gdrive:ok"),
		testJobMode(t.TempDir(), "gdrive:bad", "sync"),
	}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
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

func TestRunSyncOnceStopsImmediatelyOnContextCanceled(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{
		testJob(t.TempDir(), "gdrive:first"),
		testJob(t.TempDir(), "gdrive:second"),
	}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:first": context.Canceled}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
	if err != context.Canceled {
		t.Fatalf("runSyncOnce error = %v, want original context.Canceled", err)
	}
	if len(s.copyParams) != 1 || s.copyParams[0].Remote != "gdrive:first" {
		t.Fatalf("copy calls = %+v, want only the canceled first pair", s.copyParams)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want cancellation returned before failure bookkeeping", results)
	}
	if strings.Contains(errOut.String(), "FAILED") {
		t.Fatalf("cancellation was reported as a sync failure:\n%s", errOut.String())
	}
}

// TestRunSyncOnce_FailuresGoToStderr verifies the AX contract: stdout carries
// only success (OK) lines, while FAILED (and SKIPPED) diagnostics go to
// stderr. Every agent consumer of `sync` (and Task 3's JSON renderer) depends
// on stdout staying success-only.
func TestRunSyncOnce_FailuresGoToStderr(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJob(t.TempDir(), "gdrive:bad")}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
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
	cfg := &config.Config{Jobs: []config.Job{
		testJob(t.TempDir(), "gdrive:a"),
		testJobMode(t.TempDir(), "gdrive:b", "bisync"),
	}}
	s := &fakeCLISyncer{}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
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

// TestRunSyncOnceSingleFilePairDoesNotTreatSourceAsDirectory exercises the
// same CLI path as `better-drive sync --dry-run --format json` for a
// ~/.claude.json-style pair. Filter discovery must accept the file before the
// engine's file-local dispatch turns the copy into rclone copyto.
func TestRunSyncOnceSingleFilePairDoesNotTreatSourceAsDirectory(t *testing.T) {
	local := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(local, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Jobs: []config.Job{testJob(local, "gdrive:Backups/claude")}}
	s := &fakeCLISyncer{}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatJSON, true, false, RuntimeDependencies{})
	if err != nil {
		t.Fatalf("runSyncOnce(single file): %v; stderr=%q", err, errOut.String())
	}
	if len(s.copyParams) != 1 {
		t.Fatalf("copy calls = %d, want exactly one", len(s.copyParams))
	}
	got := s.copyParams[0]
	if got.Local != local || got.Remote != "gdrive:Backups/claude" || !got.DryRun {
		t.Fatalf("copy params = %+v, want file pair with dry-run preserved", got)
	}
	if len(got.Filters) != 0 {
		t.Fatalf("copy filters = %#v, want none for a single-file pair", got.Filters)
	}
	if len(results) != 1 || results[0].Status != "ok" || !results[0].DryRun {
		t.Fatalf("results = %+v, want one successful dry-run result", results)
	}
	if strings.Contains(errOut.String(), "not a directory") {
		t.Fatalf("single-file pair was treated as a directory: %s", errOut.String())
	}
}

// TestRunSyncOnce_WorkdirFollowsPairIdentityNotConfigOrder verifies each pair
// keeps its own bisync workdir when the [[pair]] blocks are reordered. This is
// the regression test for a pair that syncs once and then fails forever:
// workdirs used to be keyed by a pair's index in the config, so swapping two
// blocks (or inserting/deleting one) pointed a pair at another pair's baseline
// listings - which rclone rejects with "must run --resync", while the loop, in
// turn, saw *.lst files present and never asked for one.
func TestRunSyncOnce_WorkdirFollowsPairIdentityNotConfigOrder(t *testing.T) {
	a := testJobMode(t.TempDir(), "gdrive:a", "bisync")
	b := testJobMode(t.TempDir(), "gdrive:b", "bisync")

	// workdirs runs one full sync pass over pairs (in the given order) and
	// reports the workdir each pair's Bisync call received, keyed by remote.
	workdirs := func(jobs ...config.Job) map[string]string {
		t.Helper()
		s := &fakeCLISyncer{}
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if _, err := runSyncOnce(cmd, s, &config.Config{Jobs: jobs}, output.FormatTable, false, false, RuntimeDependencies{}); err != nil {
			t.Fatalf("runSyncOnce: %v", err)
		}
		got := make(map[string]string, len(s.bisyncParams))
		for _, p := range s.bisyncParams {
			got[p.Path2] = p.Workdir
		}
		return got
	}

	inOrder := workdirs(a, b)
	swapped := workdirs(b, a)
	for _, remote := range []string{"gdrive:a", "gdrive:b"} {
		if inOrder[remote] != swapped[remote] {
			t.Errorf("pair %s changed workdir when the config was reordered: %q -> %q", remote, inOrder[remote], swapped[remote])
		}
	}
	if inOrder["gdrive:a"] == inOrder["gdrive:b"] {
		t.Errorf("both pairs share workdir %q; per-pair baselines would corrupt each other", inOrder["gdrive:a"])
	}
}

// TestRunSyncOnce_JSONFormatEmitsResultsNotPerPairLines verifies the json
// format writes nothing per pair to stdout during the loop, then renders the
// full []output.PairResult once at the end - the table format's per-pair OK
// line must not leak into json mode.
func TestRunSyncOnce_JSONFormatEmitsResultsNotPerPairLines(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJob(t.TempDir(), "gdrive:a")}}
	s := &fakeCLISyncer{}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatJSON, false, false, RuntimeDependencies{})
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
	cfg := &config.Config{Jobs: []config.Job{testJobMode(t.TempDir(), "gdrive:a", "bisync")}}
	s := &fakeCLISyncer{}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	results, err := runSyncOnce(cmd, s, cfg, output.FormatTable, true, false, RuntimeDependencies{})
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

func TestRunSyncOnceThreadsCommandContextAndProgressWriter(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "cli-sync")
	cfg := &config.Config{Jobs: []config.Job{testJob(t.TempDir(), "gdrive:a")}}
	s := &fakeCLISyncer{}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	if _, err := runSyncOnce(cmd, s, cfg, output.FormatTable, true, false, RuntimeDependencies{}); err != nil {
		t.Fatalf("runSyncOnce: %v", err)
	}
	if len(s.copyParams) != 1 {
		t.Fatalf("copy params = %+v, want exactly one call", s.copyParams)
	}
	if s.copyParams[0].Context != ctx {
		t.Fatal("runSyncOnce did not thread the command context to the sync engine")
	}
	if s.copyParams[0].Stderr != &errOut {
		t.Fatal("runSyncOnce did not thread command stderr as the live progress writer")
	}
}

func TestRunSyncOnceReportsPairRunningBeforeRcloneCompletes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s := &fakeCLISyncer{onCall: func() {
		close(started)
		<-release
	}}
	cfg := &config.Config{Jobs: []config.Job{testJob(t.TempDir(), "gdrive:slow")}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	done := make(chan error, 1)
	go func() {
		_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, true, false, RuntimeDependencies{})
		done <- err
	}()

	select {
	case <-started:
		if got := errOut.String(); !strings.Contains(got, "gdrive:slow") || !strings.Contains(got, "RUNNING") {
			close(release)
			t.Fatalf("stderr before rclone completion = %q, want pair RUNNING status", got)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("syncer was not called")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("runSyncOnce: %v", err)
	}
}

// TestRunSyncOnce_ResyncForcesABaselineRebuild verifies the --resync flag
// reaches every bisync pair as BisyncParams.Resync, so a user whose baseline
// was lost or replaced can rebuild it from the CLI instead of deleting the
// workdir by hand.
func TestRunSyncOnce_ResyncForcesABaselineRebuild(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJobMode(t.TempDir(), "gdrive:a", "bisync")}}
	s := &fakeCLISyncer{}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if _, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, true, RuntimeDependencies{}); err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	if len(s.bisyncParams) != 1 || !s.bisyncParams[0].Resync {
		t.Fatalf("bisyncParams = %+v, want exactly 1 call with Resync=true", s.bisyncParams)
	}
}

// TestRunSyncOnce_ResyncWithDryRunWritesNothing verifies the two flags compose
// rather than cancel: both reach the Syncer, and the CLI layer itself creates
// nothing on disk - not even the pair's workdir, which a real resync would
// otherwise populate with a fresh baseline. (engine.Bisync's own guard on the
// resync mkdir/ensureRemoteDir step is pinned by
// TestBisyncResyncDryRunSkipsRealMkdir.)
func TestRunSyncOnce_ResyncWithDryRunWritesNothing(t *testing.T) {
	local := t.TempDir() // unique per run, so the job's workdir cannot pre-exist
	job := testJobMode(local, "gdrive:a", "bisync")
	cfg := &config.Config{Jobs: []config.Job{job}}
	s := &fakeCLISyncer{}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if _, err := runSyncOnce(cmd, s, cfg, output.FormatTable, true, true, RuntimeDependencies{}); err != nil {
		t.Fatalf("runSyncOnce err = %v, want nil", err)
	}
	if len(s.bisyncParams) != 1 {
		t.Fatalf("bisyncParams = %+v, want exactly 1 call", s.bisyncParams)
	}
	if !s.bisyncParams[0].Resync || !s.bisyncParams[0].DryRun {
		t.Errorf("params = %+v, want both Resync and DryRun true", s.bisyncParams[0])
	}
	if _, err := os.Stat(paths.JobWorkdir(job.ID)); !os.IsNotExist(err) {
		t.Errorf("workdir %q exists after a dry-run resync, want nothing written", paths.JobWorkdir(job.ID))
	}
}

// TestRunSyncOnce_NeedsResyncFailureNamesTheRecoveryCommand verifies a pair
// that fails with engine.ErrNeedsResync produces an error naming
// `better-drive sync --resync`. The command has to be in the error MESSAGE,
// not only in the remediation field: RenderError prints a remediation for a
// --format json caller only, so a terminal user would otherwise be told the
// baseline is lost and never told how to rebuild it - and the pair would fail
// identically on every subsequent run.
func TestRunSyncOnce_NeedsResyncFailureNamesTheRecoveryCommand(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJobMode(t.TempDir(), "gdrive:stuck", "bisync")}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:stuck": engine.ErrNeedsResync}}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
	if err == nil {
		t.Fatal("want non-nil error when a pair needs a resync")
	}
	const want = "better-drive sync --resync"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error message = %q, want it to name %q", err.Error(), want)
	}
	if hint := exitcode.RemediationOf(err); !strings.Contains(hint, want) {
		t.Errorf("remediation = %q, want it to name %q", hint, want)
	}
	if got := exitcode.Code(err); got != exitcode.SyncFailedCode {
		t.Errorf("Code = %d, want %d", got, exitcode.SyncFailedCode)
	}
}

// TestRunSyncOnce_OrdinaryFailureKeepsItsOwnRemediation verifies the resync
// hint is specific to the lost-baseline failure: an ordinary pair failure must
// keep pointing at the per-pair diagnostics instead of telling the user to
// rebuild a baseline that is not the problem (and whose rebuild does not
// propagate deletions).
func TestRunSyncOnce_OrdinaryFailureKeepsItsOwnRemediation(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{testJobMode(t.TempDir(), "gdrive:bad", "bisync")}}
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:bad": errors.New("boom")}}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	_, err := runSyncOnce(cmd, s, cfg, output.FormatTable, false, false, RuntimeDependencies{})
	if err == nil {
		t.Fatal("want non-nil error when a pair fails")
	}
	if strings.Contains(err.Error(), "--resync") || strings.Contains(exitcode.RemediationOf(err), "--resync") {
		t.Errorf("a generic failure must not advise a resync; error = %q, remediation = %q", err.Error(), exitcode.RemediationOf(err))
	}
}

func TestRunSyncOnceAttemptsMultipleReplicas(t *testing.T) {
	job := testJob(t.TempDir(), "gdrive:primary")
	job.Destinations = append(job.Destinations, config.Destination{
		Backend: "r2", Path: "backup", AccountID: "r2-account", RootID: "r2-root",
		CredentialRef: "rclone:r2", Required: false, MinCompleteRestoreSets: 2, DeletePolicy: "none",
	})
	s := &fakeCLISyncer{}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	results, err := runSyncOnce(cmd, s, &config.Config{Jobs: []config.Job{job}}, output.FormatTable, false, false, RuntimeDependencies{})
	if err != nil {
		t.Fatalf("runSyncOnce: %v", err)
	}
	if len(s.copyParams) != 2 {
		t.Fatalf("copy calls = %#v, want both replicas attempted", s.copyParams)
	}
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("results = %#v, want one successful job result", results)
	}
}

func TestRunSyncOnceSupportsPullDirection(t *testing.T) {
	job := testJob(t.TempDir(), "gdrive:backup")
	job.Direction = "pull"
	s := &fakeCLISyncer{}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if _, err := runSyncOnce(cmd, s, fakeConfigWithJob(job), output.FormatTable, false, false, RuntimeDependencies{}); err != nil {
		t.Fatalf("runSyncOnce pull: %v", err)
	}
	if len(s.copyParams) != 1 || s.copyParams[0].Local != "gdrive:backup" || s.copyParams[0].Remote != job.Source {
		t.Fatalf("copy params = %#v, want pull direction", s.copyParams)
	}
}

func TestRunSyncOnceRequiredReplicaFailureStillAttemptsOptional(t *testing.T) {
	job := testJob(t.TempDir(), "gdrive:required")
	job.Destinations = append(job.Destinations, config.Destination{
		Backend: "r2", Path: "optional", AccountID: "r2-account", RootID: "r2-root",
		CredentialRef: "rclone:r2", Required: false, MinCompleteRestoreSets: 2, DeletePolicy: "none",
	})
	s := &fakeCLISyncer{errByRemote: map[string]error{"gdrive:required": errors.New("required failed")}}
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	_, err := runSyncOnce(cmd, s, fakeConfigWithJob(job), output.FormatTable, false, false, RuntimeDependencies{})
	if err == nil {
		t.Fatalf("runSyncOnce error = nil, want required replica failure")
	}
	if !strings.Contains(errOut.String(), "required failed") {
		t.Fatalf("stderr = %q, want required replica diagnostic", errOut.String())
	}
	if len(s.copyParams) != 2 {
		t.Fatalf("copy calls = %#v, want optional attempted after required failure", s.copyParams)
	}
}

func fakeConfigWithJob(job config.Job) *config.Config {
	return &config.Config{SchemaVersion: config.CurrentSchemaVersion, Jobs: []config.Job{job}}
}

func TestRunSyncOnceJSONIncludesReplicaRequiredStatus(t *testing.T) {
	job := testJob(t.TempDir(), "gdrive:primary")
	job.Destinations = append(job.Destinations, config.Destination{
		Backend: "r2", Path: "optional", AccountID: "r2-account", RootID: "r2-root",
		CredentialRef: "rclone:r2", Required: false, MinCompleteRestoreSets: 2, DeletePolicy: "none",
	})
	s := &fakeCLISyncer{}
	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if _, err := runSyncOnce(cmd, s, fakeConfigWithJob(job), output.FormatJSON, false, false, RuntimeDependencies{}); err != nil {
		t.Fatalf("runSyncOnce: %v; stderr=%q", err, errOut.String())
	}
	var got []output.PairResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("JSON: %v; output=%s", err, out.String())
	}
	if len(got) != 1 || len(got[0].Replicas) != 2 {
		t.Fatalf("results = %#v, want two replica results", got)
	}
	if !got[0].Replicas[0].Required || got[0].Replicas[1].Required {
		t.Fatalf("replica required bits = %#v, want true,false", got[0].Replicas)
	}
}

func TestBuildStateFromResultsPersistsJobAndReplicaOutcomes(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	got := buildStateFromResults([]output.PairResult{{JobID: "job-1", Status: "degraded", ObjectCount: 3, ByteCount: 42, NextDue: now.Add(time.Hour), Replicas: []output.ReplicaResult{{ID: "r1", Target: "gdrive:backup", Required: true, Status: "ok"}}}}, now)
	if got.SchemaVersion != state.CurrentSchemaVersion || len(got.Jobs) != 1 {
		t.Fatalf("state = %#v, want versioned job state", got)
	}
	if got.Jobs[0].JobID != "job-1" || got.Jobs[0].Status != "degraded" || len(got.Jobs[0].ReplicaOutcomes) != 1 {
		t.Fatalf("job state = %#v, want degraded replica evidence", got.Jobs[0])
	}

	if got.Jobs[0].ObjectCount != 3 || got.Jobs[0].ByteCount != 42 || got.Jobs[0].NextDue.IsZero() {
		t.Fatalf("job counters = %#v, want persisted source stats and next_due", got.Jobs[0])
	}
	if got.Scheduler.Health != state.HealthHealthy || got.Scheduler.OwnerJobID != "job-1" {
		t.Fatalf("scheduler state = %#v, want healthy job-1 owner", got.Scheduler)
	}
}

func TestBuildStateFromResultsLeavesAggregateWithoutSingularOwner(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		results []output.PairResult
	}{
		{name: "zero jobs"},
		{name: "multiple jobs", results: []output.PairResult{
			{JobID: "job-2", Status: "ok"},
			{JobID: "job-1", Status: "ok"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildStateFromResults(tc.results, now)
			if got.Scheduler.OwnerJobID != "" {
				t.Fatalf("scheduler owner_job_id = %q, want empty aggregate owner", got.Scheduler.OwnerJobID)
			}
			if got.Scheduler.Health != state.HealthMissing {
				t.Fatalf("scheduler health = %q, want canonical missing aggregate health", got.Scheduler.Health)
			}
		})
	}
}

func TestPersistDaemonResultSerializesConcurrentSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	stateResults := make(map[string]output.PairResult)
	var stateMu sync.Mutex
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	results := []output.PairResult{
		{JobID: "job-2", Status: "ok"},
		{JobID: "job-1", Status: "degraded"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(results))
	for _, result := range results {
		result := result
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- persistDaemonResult(&stateMu, stateResults, path, result, now)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("persist daemon result: %v", err)
		}
	}
	got, err := state.Load(path)
	if err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if len(got.Jobs) != 2 || got.Jobs[0].JobID != "job-1" || got.Jobs[1].JobID != "job-2" {
		t.Fatalf("persisted jobs = %#v, want deterministic complete snapshot [job-1 job-2]", got.Jobs)
	}
}

// TestSyncCmd_HasResyncFlag verifies the `sync` command registers --resync
// (defaulting to false, so a plain `sync` keeps honouring existing baselines
// and therefore keeps propagating deletions).
func TestSyncCmd_HasResyncFlag(t *testing.T) {
	c := syncCmd()
	f := c.Flags().Lookup("resync")
	if f == nil {
		t.Fatal("sync command has no --resync flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--resync default = %q, want %q", f.DefValue, "false")
	}
	if f.Usage == "" {
		t.Error("--resync needs a usage string: it is the documented recovery path for a lost baseline")
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

func TestFinalizeDaemonLogWritesOneTerminalAndCloses(t *testing.T) {
	cases := []struct {
		name    string
		runErr  error
		outcome string
	}{
		{name: "success", outcome: "success"},
		{name: "error", runErr: errors.New("tray failed"), outcome: "error"},
		{name: "cancelled", runErr: context.Canceled, outcome: "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "daemon.jsonl")
			fileLog, err := runlog.OpenFile(path, "run-test", runlog.RotationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fileLog.Sink.Emit(runlog.StreamSystem, "started"); err != nil {
				t.Fatal(err)
			}

			gotErr := finalizeDaemonLog(fileLog, tc.runErr)
			if !errors.Is(gotErr, tc.runErr) {
				t.Fatalf("finalize error = %v, want run error %v", gotErr, tc.runErr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var terminals int
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var event runlog.Event
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatalf("decode event: %v", err)
				}
				if event.Terminal {
					terminals++
					if event.Outcome != tc.outcome {
						t.Fatalf("terminal outcome = %q, want %q", event.Outcome, tc.outcome)
					}
				}
			}
			if terminals != 1 {
				t.Fatalf("terminal count = %d, want 1", terminals)
			}
			if err := os.Remove(path); err != nil {
				t.Fatalf("audit log handle remained open: %v", err)
			}
		})
	}
}
