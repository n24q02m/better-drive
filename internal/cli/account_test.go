package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/config"
	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/spf13/cobra"
)

// fakeAccountEngine is an accountEngine test double, so the account command
// tests never shell out to rclone or touch the network. configured lists the
// remotes that have a token; aboutByRemote supplies each remote's quota (a
// missing entry means About fails). aboutCalls and deleted record what the
// command actually asked for, which is what the "never calls About for an
// unconfigured remote" and the removal-guard tests assert on.
type fakeAccountEngine struct {
	remotes       []string
	listErr       error
	configured    map[string]bool
	aboutByRemote map[string]engine.Quota
	aboutCalls    []string
	deleted       []string
	deleteErr     error
}

func (f *fakeAccountEngine) ListDriveRemotes() ([]string, error) {
	return f.remotes, f.listErr
}

func (f *fakeAccountEngine) RemoteConfigured(name string) (bool, error) {
	return f.configured[name], nil
}

func (f *fakeAccountEngine) About(name string) (engine.Quota, error) {
	f.aboutCalls = append(f.aboutCalls, name)
	q, ok := f.aboutByRemote[name]
	if !ok {
		return engine.Quota{}, errors.New("couldn't connect")
	}
	return q, nil
}

func (f *fakeAccountEngine) DeleteRemote(name string) error {
	f.deleted = append(f.deleted, name)
	return f.deleteErr
}

// newAccountTestCmd returns a bare cobra command with both streams captured,
// for calling the runAccount* helpers directly - the same shape the
// runSyncOnce tests in cli_test.go use.
func newAccountTestCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return cmd, &out, &errOut
}

// TestAccountList_TableShowsPairCount verifies the default table format names
// every Drive remote, reports whether each one is usable, and says how many
// configured pairs depend on it - the three facts a user needs before adding
// or removing an account.
func TestAccountList_TableShowsPairCount(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive", "work"},
		configured: map[string]bool{"gdrive": true},
	}
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: "C:/pair0", Remote: "gdrive:Backups", Interval: time.Second, Mode: "bisync"},
	}}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, cfg, output.FormatTable, false); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"gdrive", "work", "ready", "not set up", "1 pair(s)", "0 pair(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q; got:\n%s", want, got)
		}
	}
}

// TestAccountList_JSONShape verifies --format json emits a decodable
// []output.AccountStatus carrying each pair's local path, and that quota is
// omitted entirely when --quota was not asked for (rather than rendered as a
// zero-valued object, which would read as an empty Drive).
func TestAccountList_JSONShape(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive"},
		configured: map[string]bool{"gdrive": true},
	}
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: "C:/pair0", Remote: "gdrive:Backups", Interval: time.Second, Mode: "bisync"},
	}}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, cfg, output.FormatJSON, false); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	var got []output.AccountStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d accounts, want 1", len(got))
	}
	if got[0].Name != "gdrive" || !got[0].Configured {
		t.Errorf("got %+v, want gdrive reported as configured", got[0])
	}
	if len(got[0].Pairs) != 1 || got[0].Pairs[0] != "C:/pair0" {
		t.Errorf("pairs = %v, want [C:/pair0]", got[0].Pairs)
	}
	if got[0].Quota != nil {
		t.Errorf("quota = %+v, want nil without --quota", got[0].Quota)
	}
	if strings.Contains(out.String(), "quota") {
		t.Errorf("json must omit the quota key entirely without --quota; got:\n%s", out.String())
	}
}

// TestAccountList_JSONPairsIsEmptyArrayNotNull verifies an account no pair
// uses renders "pairs":[] rather than "pairs":null, so a caller can range over
// the field without first having to test it for absence.
func TestAccountList_JSONPairsIsEmptyArrayNotNull(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive"},
		configured: map[string]bool{"gdrive": true},
	}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatJSON, false); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	if !strings.Contains(out.String(), `"pairs": []`) {
		t.Errorf("want an empty pairs array; got:\n%s", out.String())
	}
}

// TestAccountList_QuotaFailureIsWarningNotError verifies a failing quota read
// degrades to a stderr warning instead of failing the command: list is the
// diagnostic command, so it has to keep working exactly when something else
// is broken.
func TestAccountList_QuotaFailureIsWarningNotError(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive"},
		configured: map[string]bool{"gdrive": true},
		// No aboutByRemote entry, so the fake's About fails.
	}
	cmd, out, errOut := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatJSON, true); err != nil {
		t.Fatalf("runAccountList error = %v, want nil (a quota failure must not fail the command)", err)
	}

	if strings.Contains(out.String(), "warning") {
		t.Errorf("stdout must not carry the warning; got:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "warning") || !strings.Contains(errOut.String(), "gdrive") {
		t.Errorf("stderr missing the quota warning naming the remote; got:\n%s", errOut.String())
	}
	var got []output.AccountStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got) != 1 || got[0].Quota != nil {
		t.Errorf("got %+v, want the account listed with a nil quota", got)
	}
}

// TestAccountList_NoAccountsHintsOnStderr verifies the first-run case: with no
// Drive remote configured, stdout stays empty (there is no data to report) and
// the actionable hint goes to stderr, keeping stdout parseable by a caller
// that pipes it.
func TestAccountList_NoAccountsHintsOnStderr(t *testing.T) {
	e := &fakeAccountEngine{}
	cmd, out, errOut := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatTable, false); err != nil {
		t.Fatalf("runAccountList error = %v, want nil", err)
	}

	if out.String() != "" {
		t.Errorf("stdout = %q, want empty when there is no account to report", out.String())
	}
	if !strings.Contains(errOut.String(), "better-drive account add") {
		t.Errorf("stderr missing the account-add hint; got:\n%s", errOut.String())
	}
}

// TestAccountList_NoAccountsJSONPrintsEmptyArray verifies the same empty case
// still emits a valid, decodable [] on stdout in the json format - a machine
// caller must never have to distinguish "no output" from "no accounts".
func TestAccountList_NoAccountsJSONPrintsEmptyArray(t *testing.T) {
	e := &fakeAccountEngine{}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatJSON, false); err != nil {
		t.Fatalf("runAccountList error = %v, want nil", err)
	}

	var got []output.AccountStatus
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v; got:\n%s", err, out.String())
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want an empty array", got)
	}
}

// TestAccountList_NeverCallsAboutForUnconfiguredRemote verifies --quota skips
// remotes with no token. An `rclone about` against a token-less remote can
// only fail, so the call would spend a round trip to produce a warning the
// "not set up" state already explains.
func TestAccountList_NeverCallsAboutForUnconfiguredRemote(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:       []string{"gdrive", "work"},
		configured:    map[string]bool{"gdrive": true},
		aboutByRemote: map[string]engine.Quota{"gdrive": {Total: 1024, Used: 512, Free: 512}},
	}
	cmd, _, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatTable, true); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	for _, name := range e.aboutCalls {
		if name == "work" {
			t.Errorf("About called for the token-less remote %q; calls = %v", name, e.aboutCalls)
		}
	}
	if len(e.aboutCalls) != 1 || e.aboutCalls[0] != "gdrive" {
		t.Errorf("aboutCalls = %v, want exactly [gdrive]", e.aboutCalls)
	}
}

// TestAccountList_QuotaRendersInTable verifies --quota appends the human
// reading to the table line, since the raw byte counts of the json format are
// unreadable at a glance.
func TestAccountList_QuotaRendersInTable(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:       []string{"gdrive"},
		configured:    map[string]bool{"gdrive": true},
		aboutByRemote: map[string]engine.Quota{"gdrive": {Total: 5497558138880, Used: 79762245855, Free: 5164644398654}},
	}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatTable, true); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "74.3 GiB") || !strings.Contains(got, "5.0 TiB") || !strings.Contains(got, "used") {
		t.Errorf("table output missing the human quota reading; got:\n%s", got)
	}
}

// TestAccountList_ReportsListFailure verifies a failing `rclone listremotes`
// is an error, not an empty list: reporting "no accounts" when rclone could
// not be reached would send the user off to re-run setup for no reason.
func TestAccountList_ReportsListFailure(t *testing.T) {
	e := &fakeAccountEngine{listErr: errors.New("rclone listremotes: exit status 1")}
	cmd, _, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, nil, output.FormatTable, false); err == nil {
		t.Fatal("runAccountList error = nil, want the listremotes failure surfaced")
	}
}

// TestPairsUsingRemote_MatchesOnRemoteNameOnly verifies the pair lookup splits
// on the first ":" rather than matching a prefix: "gdrive-work:" starts with
// "gdrive" as a string but is a different account, and treating it as the same
// one would let `account remove gdrive` be refused (or allowed) for the wrong
// reason.
func TestPairsUsingRemote_MatchesOnRemoteNameOnly(t *testing.T) {
	cfg := &config.Config{Pairs: []config.Pair{
		{Local: "C:/a", Remote: "gdrive:Backups/x"},
		{Local: "C:/b", Remote: "gdrive-work:/"},
		{Local: "C:/c", Remote: "gdrive:/"},
	}}

	got := pairsUsingRemote(cfg, "gdrive")
	want := []string{"C:/a", "C:/c"}
	if len(got) != len(want) {
		t.Fatalf("pairsUsingRemote = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pairsUsingRemote = %v, want %v", got, want)
		}
	}
}

// TestPairsUsingRemote_NilConfigIsNoPairs verifies a missing config.toml is
// treated as "no pair uses this remote". An account can exist before any pair
// does, so `account list` must survive a config that is not there yet.
func TestPairsUsingRemote_NilConfigIsNoPairs(t *testing.T) {
	if got := pairsUsingRemote(nil, "gdrive"); got != nil {
		t.Errorf("pairsUsingRemote(nil, ...) = %v, want nil", got)
	}
}

// TestHumanBytes covers the boundaries of the unit ladder: zero, the last
// value that stays in bytes, the first that rolls over to KiB, and a
// terabyte-scale reading like a real Drive quota.
func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{5497558138880, "5.0 TiB"},
		{79762245855, "74.3 GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAccountCmd_HasListSubcommandWithFlags verifies the group is wired the
// way the root command tree expects: a documented `account` command with a
// `list` subcommand carrying both --format and --quota.
func TestAccountCmd_HasListSubcommandWithFlags(t *testing.T) {
	c := accountCmd()
	if c.Long == "" || c.Example == "" {
		t.Error("account command needs both a Long description and an Example")
	}
	var list *cobra.Command
	for _, sub := range c.Commands() {
		if sub.Name() == "list" {
			list = sub
		}
	}
	if list == nil {
		t.Fatal("account command has no list subcommand")
	}
	for _, flag := range []string{"format", "quota"} {
		if list.Flags().Lookup(flag) == nil {
			t.Errorf("account list has no --%s flag", flag)
		}
	}
}

// TestAccountList_BadFormatFlag_ErrorHasRemediation verifies an unknown
// --format value is rejected with the same actionable hint status and sync
// give, rather than silently falling back to the table format.
func TestAccountList_BadFormatFlag_ErrorHasRemediation(t *testing.T) {
	e := &fakeAccountEngine{}
	cmd, _, _ := newAccountTestCmd()

	err := runAccountList(cmd, e, nil, "yaml", false)
	if err == nil {
		t.Fatal("want error for unknown --format value")
	}
	hint := exitcode.RemediationOf(err)
	if !strings.Contains(hint, "table") || !strings.Contains(hint, "json") {
		t.Errorf("remediation = %q, want it to mention both table and json", hint)
	}
}
