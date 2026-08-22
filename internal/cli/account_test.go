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
	"github.com/spf13/pflag"
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

func testJob(local, remote string) config.Job {
	return testJobMode(local, remote, "copy")
}

func testJobMode(local, remote, mode string) config.Job {
	name, path, _ := strings.Cut(remote, ":")
	direction := "push"
	if mode == "bisync" {
		direction = "bidirectional"
	}
	return config.Job{
		ID: "test-" + name + "-" + strings.ReplaceAll(path, "/", "-"), Source: local,
		Direction: direction, Mode: mode, Required: true, CategoryPolicyID: "test-policy",
		CategoryPolicyVersion: 1, CategoryPolicyDigest: "sha256:test", SymlinkPolicy: "preserve",
		Interval: time.Second, Schedule: "1s",
		Destinations: []config.Destination{{Backend: "drive", Path: path, AccountID: "test-account", RootID: "test-root", CredentialRef: "rclone:" + name, Required: true, MinCompleteRestoreSets: 2, DeletePolicy: "none"}},
	}
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
	cfg := &config.Config{Jobs: []config.Job{testJob("C:/pair0", "gdrive:Backups")}}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountList(cmd, e, cfg, output.FormatTable, false); err != nil {
		t.Fatalf("runAccountList error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"gdrive", "work", "ready", "not set up", "1 job(s)", "0 job(s)"} {
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
	cfg := &config.Config{Jobs: []config.Job{testJob("C:/pair0", "gdrive:Backups")}}
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

// TestJobsUsingRemote_MatchesOnRemoteNameOnly verifies the job lookup splits
// on the first ":" rather than matching a prefix.
func TestJobsUsingRemote_MatchesOnRemoteNameOnly(t *testing.T) {
	cfg := &config.Config{Jobs: []config.Job{
		testJob("C:/a", "gdrive:Backups/x"),
		testJob("C:/b", "gdrive-work:/"),
		testJob("C:/c", "gdrive:/"),
	}}

	got := jobsUsingRemote(cfg, "gdrive")
	want := []string{"C:/a", "C:/c"}
	if len(got) != len(want) {
		t.Fatalf("jobsUsingRemote = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("jobsUsingRemote = %v, want %v", got, want)
		}
	}
}

// TestPairsUsingRemote_NilConfigIsNoPairs verifies a missing config.toml is
// treated as "no pair uses this remote". An account can exist before any pair
// does, so `account list` must survive a config that is not there yet.
// TestJobsUsingRemote_NilConfigIsNoJobs verifies a missing config.toml is
// treated as "no job uses this remote".
func TestJobsUsingRemote_NilConfigIsNoJobs(t *testing.T) {
	if got := jobsUsingRemote(nil, "gdrive"); got != nil {
		t.Errorf("jobsUsingRemote(nil, ...) = %v, want nil", got)
	}
}

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

// fakeSetupEngine is a setupEngine test double for the add/setup body, so
// those tests never open a browser or write to a real rclone config.
// configured and exists drive the idempotency branches; created records the
// name and params each CreateDriveRemote call received, which is what the
// headless-credential tests assert on.
type fakeSetupEngine struct {
	configured  bool
	exists      bool
	deleted     []string
	created     []string
	createdWith []map[string]string
	createErr   error
}

func (f *fakeSetupEngine) RemoteConfigured(string) (bool, error) { return f.configured, nil }
func (f *fakeSetupEngine) RemoteExists(string) (bool, error)     { return f.exists, nil }

func (f *fakeSetupEngine) DeleteRemote(name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeSetupEngine) CreateDriveRemote(name string, params map[string]string) error {
	f.created = append(f.created, name)
	f.createdWith = append(f.createdWith, params)
	return f.createErr
}

// TestSetupAndAccountAdd_ShareFlags verifies `setup` and `account add` expose
// exactly the same flags. They are one command under two names, so a flag that
// landed on only one of them would make the documented equivalence false for
// whichever name a user happened to reach for.
func TestSetupAndAccountAdd_ShareFlags(t *testing.T) {
	flagNames := func(c *cobra.Command) map[string]bool {
		names := map[string]bool{}
		c.Flags().VisitAll(func(f *pflag.Flag) { names[f.Name] = true })
		return names
	}

	setupFlags := flagNames(setupCmd())
	addFlags := flagNames(accountAddCmd())

	for name := range setupFlags {
		if !addFlags[name] {
			t.Errorf("`setup` has --%s but `account add` does not", name)
		}
	}
	for name := range addFlags {
		if !setupFlags[name] {
			t.Errorf("`account add` has --%s but `setup` does not", name)
		}
	}
	if len(setupFlags) == 0 {
		t.Error("setup exposes no flags at all, so this comparison proves nothing")
	}
}

// TestSetupOutput_Unchanged pins the two lines `setup` prints at runtime, byte
// for byte. Moving the body behind a shared constructor and adding flags to it
// must leave what an existing user or script sees completely untouched.
func TestSetupOutput_Unchanged(t *testing.T) {
	t.Run("already set up", func(t *testing.T) {
		e := &fakeSetupEngine{configured: true}
		cmd, out, _ := newAccountTestCmd()

		if err := runAccountAdd(cmd, e, "gdrive", driveCredentials{}); err != nil {
			t.Fatalf("runAccountAdd error = %v", err)
		}

		if got, want := out.String(), "remote \"gdrive\" already set up\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
		if len(e.created) != 0 {
			t.Errorf("CreateDriveRemote called %v, want no call for an already-configured remote", e.created)
		}
	})

	t.Run("created", func(t *testing.T) {
		e := &fakeSetupEngine{}
		cmd, out, _ := newAccountTestCmd()

		if err := runAccountAdd(cmd, e, "gdrive", driveCredentials{}); err != nil {
			t.Fatalf("runAccountAdd error = %v", err)
		}

		if got, want := out.String(), "remote \"gdrive\" created\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})
}

// TestAccountAdd_RecreatesTokenlessRemote verifies the self-healing branch
// survives the extraction: a remote that exists by name but has no token (an
// interrupted setup) is deleted before being created again, rather than being
// left broken.
func TestAccountAdd_RecreatesTokenlessRemote(t *testing.T) {
	e := &fakeSetupEngine{exists: true}
	cmd, _, _ := newAccountTestCmd()

	if err := runAccountAdd(cmd, e, "gdrive", driveCredentials{}); err != nil {
		t.Fatalf("runAccountAdd error = %v", err)
	}

	if len(e.deleted) != 1 || e.deleted[0] != "gdrive" {
		t.Errorf("deleted = %v, want the token-less remote cleared first", e.deleted)
	}
	if len(e.created) != 1 || e.created[0] != "gdrive" {
		t.Errorf("created = %v, want the remote recreated", e.created)
	}
}

// TestAccountAdd_IsRegisteredUnderTheAccountGroup verifies `add` is reachable
// as `better-drive account add`, which is what the account group's own
// documentation tells users to run.
func TestAccountAdd_IsRegisteredUnderTheAccountGroup(t *testing.T) {
	var found bool
	for _, sub := range accountCmd().Commands() {
		if sub.Name() == "add" {
			found = true
		}
	}
	if !found {
		t.Error("account command has no add subcommand")
	}
}

// TestSetupHeadless_BuildsRcloneParams verifies the credential flags map onto
// the rclone backend keys `rclone config create` expects, and that only the
// flags actually set become params.
func TestSetupHeadless_BuildsRcloneParams(t *testing.T) {
	e := &fakeSetupEngine{}
	cmd, _, _ := newAccountTestCmd()
	creds := driveCredentials{token: "X", clientID: "Y"}

	if err := runAccountAdd(cmd, e, "gdrive", creds); err != nil {
		t.Fatalf("runAccountAdd error = %v", err)
	}

	if len(e.createdWith) != 1 {
		t.Fatalf("CreateDriveRemote called %d times, want 1", len(e.createdWith))
	}
	got := e.createdWith[0]
	want := map[string]string{"token": "X", "client_id": "Y"}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("params[%q] = %q, want %q", key, got[key], value)
		}
	}
}

// TestSetupHeadless_MapsEveryCredentialFlag verifies each remaining flag
// reaches its documented rclone key, so a service-account or custom-OAuth
// setup is not silently dropped on the floor.
func TestSetupHeadless_MapsEveryCredentialFlag(t *testing.T) {
	creds := driveCredentials{
		token:              "T",
		clientID:           "CI",
		clientSecret:       "CS",
		serviceAccountFile: "C:/keys/sa.json",
	}

	got := creds.params()
	want := map[string]string{
		"token":                "T",
		"client_id":            "CI",
		"client_secret":        "CS",
		"service_account_file": "C:/keys/sa.json",
	}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("params[%q] = %q, want %q", key, got[key], value)
		}
	}
}

// TestSetupNonInteractive_RequiresCredential verifies --non-interactive with
// nothing to authenticate with fails before any rclone call. Reaching rclone
// would leave it waiting for an OAuth answer nobody is there to give, and a
// job that hangs is far worse than one that exits.
func TestSetupNonInteractive_RequiresCredential(t *testing.T) {
	e := &fakeSetupEngine{}
	cmd, _, _ := newAccountTestCmd()
	creds := driveCredentials{nonInteractive: true}

	err := runAccountAdd(cmd, e, "gdrive", creds)
	if err == nil {
		t.Fatal("runAccountAdd error = nil, want --non-interactive rejected without a credential")
	}
	if got := exitcode.Code(err); got != exitcode.ConfigErrorCode {
		t.Errorf("Code = %d, want %d", got, exitcode.ConfigErrorCode)
	}
	if len(e.created) != 0 || len(e.deleted) != 0 {
		t.Errorf("engine was called (created=%v deleted=%v), want the guard to fire first", e.created, e.deleted)
	}
	hint := exitcode.RemediationOf(err)
	for _, want := range []string{"rclone authorize", "--token", "gdrive"} {
		if !strings.Contains(hint, want) {
			t.Errorf("remediation = %q, want it to mention %q", hint, want)
		}
	}
	// The message has to carry the way out too: RenderError prints the
	// remediation only for a --format json caller, and this command has no
	// --format flag, so a hint kept solely in the remediation is invisible to
	// the person running the command.
	if !strings.Contains(err.Error(), "rclone authorize") {
		t.Errorf("Error() = %q, want the message itself to name how to get a token", err.Error())
	}
}

// TestSetupNonInteractive_AcceptsServiceAccountFile verifies a service account
// key satisfies the same guard: it is a credential that needs no browser, so
// requiring a token on top of it would reject a perfectly valid CI setup.
func TestSetupNonInteractive_AcceptsServiceAccountFile(t *testing.T) {
	e := &fakeSetupEngine{}
	cmd, _, _ := newAccountTestCmd()
	creds := driveCredentials{nonInteractive: true, serviceAccountFile: "C:/keys/sa.json"}

	if err := runAccountAdd(cmd, e, "gdrive", creds); err != nil {
		t.Fatalf("runAccountAdd error = %v, want a service account to satisfy the guard", err)
	}
	if len(e.created) != 1 {
		t.Errorf("created = %v, want the remote created", e.created)
	}
}

// TestSetupNonInteractive_AddsConfigIsLocalFalse verifies --non-interactive
// reaches rclone as config_is_local=false, which is what stops the backend
// from trying to open a browser.
func TestSetupNonInteractive_AddsConfigIsLocalFalse(t *testing.T) {
	e := &fakeSetupEngine{}
	cmd, _, _ := newAccountTestCmd()
	creds := driveCredentials{nonInteractive: true, token: "X"}

	if err := runAccountAdd(cmd, e, "gdrive", creds); err != nil {
		t.Fatalf("runAccountAdd error = %v", err)
	}

	if len(e.createdWith) != 1 {
		t.Fatalf("CreateDriveRemote called %d times, want 1", len(e.createdWith))
	}
	if got := e.createdWith[0]["config_is_local"]; got != "false" {
		t.Errorf("params[config_is_local] = %q, want %q", got, "false")
	}
}

// TestSetup_TokenNeverEchoed verifies the token value never reaches stdout,
// stderr or an error string. A token is a live credential, and stderr is
// exactly what a CI job archives, so echoing it once would leak it into a log
// that outlives the run.
func TestSetup_TokenNeverEchoed(t *testing.T) {
	const secret = "SECRET-TOKEN-DO-NOT-PRINT"

	t.Run("create fails", func(t *testing.T) {
		e := &fakeSetupEngine{createErr: errors.New("rclone config create: exit status 1")}
		cmd, out, errOut := newAccountTestCmd()

		err := runAccountAdd(cmd, e, "gdrive", driveCredentials{token: secret, nonInteractive: true})
		if err == nil {
			t.Fatal("runAccountAdd error = nil, want the create failure surfaced")
		}
		for name, got := range map[string]string{"stdout": out.String(), "stderr": errOut.String(), "error": err.Error()} {
			if strings.Contains(got, secret) {
				t.Errorf("%s leaked the token value: %q", name, got)
			}
		}
	})

	t.Run("create succeeds", func(t *testing.T) {
		e := &fakeSetupEngine{}
		cmd, out, errOut := newAccountTestCmd()

		if err := runAccountAdd(cmd, e, "gdrive", driveCredentials{token: secret}); err != nil {
			t.Fatalf("runAccountAdd error = %v", err)
		}
		if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
			t.Errorf("the token value was echoed; stdout=%q stderr=%q", out.String(), errOut.String())
		}
	})
}

// TestSetup_NoFlagsKeepsParamsNil is the regression guard for the pre-existing
// path: with no credential flag set, `rclone config create` must receive the
// same nil params it always has, so the browser-driven setup an existing user
// runs is bit-for-bit the command it was before.
func TestSetup_NoFlagsKeepsParamsNil(t *testing.T) {
	e := &fakeSetupEngine{}
	cmd, _, _ := newAccountTestCmd()

	if err := runAccountAdd(cmd, e, "gdrive", driveCredentials{}); err != nil {
		t.Fatalf("runAccountAdd error = %v", err)
	}

	if len(e.createdWith) != 1 {
		t.Fatalf("CreateDriveRemote called %d times, want 1", len(e.createdWith))
	}
	if e.createdWith[0] != nil {
		t.Errorf("params = %v, want nil with no credential flag set", e.createdWith[0])
	}
}

// TestSetupAndAccountAdd_HaveEveryCredentialFlag verifies the flags are
// actually registered on the commands (not merely supported by the struct
// behind them), under both names.
func TestSetupAndAccountAdd_HaveEveryCredentialFlag(t *testing.T) {
	for _, c := range []*cobra.Command{setupCmd(), accountAddCmd()} {
		for _, name := range []string{"remote", "token", "client-id", "client-secret", "service-account-file", "non-interactive"} {
			if c.Flags().Lookup(name) == nil {
				t.Errorf("command %q has no --%s flag", c.Name(), name)
			}
		}
	}
}

// removeFixture builds the shared starting point for the removal tests: one
// configured account with a single pair pointing at it.
func removeFixture() (*fakeAccountEngine, *config.Config) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive"},
		configured: map[string]bool{"gdrive": true},
	}
	cfg := &config.Config{Jobs: []config.Job{testJob("C:/pair0", "gdrive:Backups")}}
	return e, cfg
}

// TestAccountRemove_RefusesWhenPairUsesIt verifies deleting an account a pair
// still syncs against is refused rather than performed. Deleting the remote
// would leave that pair permanently failing with an error naming a remote the
// user can no longer see, so the guard names the pair instead.
func TestAccountRemove_RefusesWhenPairUsesIt(t *testing.T) {
	e, cfg := removeFixture()
	cmd, _, _ := newAccountTestCmd()

	err := runAccountRemove(cmd, e, cfg, "gdrive", false)
	if err == nil {
		t.Fatal("runAccountRemove error = nil, want a refusal")
	}
	if got := exitcode.Code(err); got != exitcode.ConfigErrorCode {
		t.Errorf("Code = %d, want %d", got, exitcode.ConfigErrorCode)
	}
	if len(e.deleted) != 0 {
		t.Errorf("DeleteRemote called %v, want no call when the removal is refused", e.deleted)
	}
	hint := exitcode.RemediationOf(err)
	if !strings.Contains(hint, "C:/pair0") {
		t.Errorf("remediation = %q, want it to name the pair holding the account", hint)
	}
	if !strings.Contains(hint, "--force") {
		t.Errorf("remediation = %q, want it to mention the --force override", hint)
	}
	// See the matching assertion in TestSetupNonInteractive_RequiresCredential:
	// the remediation is json-only, so the override has to be in the message.
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("Error() = %q, want the message itself to name the --force override", err.Error())
	}
}

// TestAccountRemove_ForceDeletesAnyway verifies --force is a real override:
// the same situation that is refused above goes through when the user has
// said they mean it.
func TestAccountRemove_ForceDeletesAnyway(t *testing.T) {
	e, cfg := removeFixture()
	cmd, _, _ := newAccountTestCmd()

	if err := runAccountRemove(cmd, e, cfg, "gdrive", true); err != nil {
		t.Fatalf("runAccountRemove error = %v, want nil under --force", err)
	}
	if len(e.deleted) != 1 || e.deleted[0] != "gdrive" {
		t.Errorf("deleted = %v, want exactly [gdrive]", e.deleted)
	}
}

// TestAccountRemove_UnknownRemoteErrors verifies naming an account that does
// not exist fails instead of reporting a removal that never happened - even
// under --force, which overrides the pair guard and not reality.
func TestAccountRemove_UnknownRemoteErrors(t *testing.T) {
	for _, force := range []bool{false, true} {
		e := &fakeAccountEngine{}
		cmd, _, _ := newAccountTestCmd()

		err := runAccountRemove(cmd, e, nil, "gdrive", force)
		if err == nil {
			t.Fatalf("force=%v: runAccountRemove error = nil, want an error for an unknown account", force)
		}
		if got := exitcode.Code(err); got != exitcode.ConfigErrorCode {
			t.Errorf("force=%v: Code = %d, want %d", force, got, exitcode.ConfigErrorCode)
		}
		if len(e.deleted) != 0 {
			t.Errorf("force=%v: DeleteRemote called %v, want no call", force, e.deleted)
		}
	}
}

// TestAccountRemove_SuccessMessageOnStdout verifies the success path reports
// on stdout (it is a result, not a diagnostic) and calls through to rclone.
func TestAccountRemove_SuccessMessageOnStdout(t *testing.T) {
	e := &fakeAccountEngine{remotes: []string{"gdrive"}, configured: map[string]bool{"gdrive": true}}
	cmd, out, errOut := newAccountTestCmd()

	if err := runAccountRemove(cmd, e, nil, "gdrive", false); err != nil {
		t.Fatalf("runAccountRemove error = %v", err)
	}

	if got, want := out.String(), "account \"gdrive\" removed\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if errOut.String() != "" {
		t.Errorf("stderr = %q, want empty on success", errOut.String())
	}
	if len(e.deleted) != 1 || e.deleted[0] != "gdrive" {
		t.Errorf("deleted = %v, want exactly [gdrive]", e.deleted)
	}
}

// TestAccountRemove_ReportsDeleteFailure verifies a failing `rclone config
// delete` surfaces instead of printing the success line anyway.
func TestAccountRemove_ReportsDeleteFailure(t *testing.T) {
	e := &fakeAccountEngine{
		remotes:    []string{"gdrive"},
		configured: map[string]bool{"gdrive": true},
		deleteErr:  errors.New("config file is read-only"),
	}
	cmd, out, _ := newAccountTestCmd()

	if err := runAccountRemove(cmd, e, nil, "gdrive", false); err == nil {
		t.Fatal("runAccountRemove error = nil, want the delete failure surfaced")
	}
	if strings.Contains(out.String(), "removed") {
		t.Errorf("stdout claims a removal that failed; got:\n%s", out.String())
	}
}

// TestAccountRemoveCmd_TakesExactlyOneNameAndHasForce verifies the command is
// wired to require the account name as a positional argument and to expose
// --force, which the guard's remediation tells users to reach for.
func TestAccountRemoveCmd_TakesExactlyOneNameAndHasForce(t *testing.T) {
	var remove *cobra.Command
	for _, sub := range accountCmd().Commands() {
		if sub.Name() == "remove" {
			remove = sub
		}
	}
	if remove == nil {
		t.Fatal("account command has no remove subcommand")
	}
	if remove.Flags().Lookup("force") == nil {
		t.Error("account remove has no --force flag")
	}
	if remove.Args == nil {
		t.Fatal("account remove accepts any number of args, want exactly one account name")
	}
	if err := remove.Args(remove, []string{}); err == nil {
		t.Error("account remove accepted zero args, want exactly one account name")
	}
	if err := remove.Args(remove, []string{"a", "b"}); err == nil {
		t.Error("account remove accepted two args, want exactly one account name")
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
