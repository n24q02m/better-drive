package engine

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// newFakeRunnerEngine builds an Engine whose runner is fn, bypassing
// exec.Command entirely - used by tests that assert the constructed rclone
// argv without a real rclone binary.
func newFakeRunnerEngine(cfg string, fn runner) *Engine {
	return &Engine{cfg: cfg, run: fn}
}

// TestNewResolvesRunner verifies New wires up a working runner seam (the
// rclone shell-out replacement for librclone.Initialize/RPC) without
// requiring a real rclone binary on PATH for the construction itself.
func TestNewResolvesRunner(t *testing.T) {
	e := New("")
	if e == nil {
		t.Fatal("New(\"\") returned nil")
	}
	if e.run == nil {
		t.Fatal("New(\"\").run is nil, want a resolved runner")
	}
	if e.bin == "" {
		t.Fatal("New(\"\").bin is empty, want a resolved rclone binary name/path")
	}
}

// TestBisyncBuildsRcloneArgs verifies Bisync builds `rclone bisync <path1>
// <path2> --workdir <workdir> ...` with the shared perf/retry/skip_gdocs
// flags plus the bisync-specific ones (--resilient, --recover, --max-delete
// 50, --conflict-resolve newer, --conflict-loser num, --compare
// size,modtime,checksum), and a --filters-file whose content is the joined
// filter lines - the temp file removed again once Bisync returns.
func TestBisyncBuildsRcloneArgs(t *testing.T) {
	// path1 must be a real (but disposable) directory: Resync:true makes
	// Bisync os.MkdirAll(p.Path1), and a hard-coded "C:/x" would leak that
	// dir onto the real disk every time the unit suite runs.
	path1 := t.TempDir()
	workdir := t.TempDir()
	var gotArgv []string
	var filterFileContent string
	var filterFileReadErr error
	var filterPath string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		if len(args) > 0 && args[0] == "bisync" {
			gotArgv = args
			// Read the --filters-file HERE, while Bisync's defer cleanup()
			// has not yet run - the one point the temp file is guaranteed to
			// still exist.
			if idx := indexOf(args, "--filters-file"); idx != -1 && idx+1 < len(args) {
				filterPath = args[idx+1]
				data, err := os.ReadFile(filterPath)
				filterFileContent, filterFileReadErr = string(data), err
			}
		}
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{
		Path1: path1, Path2: "gdrive:x", Workdir: workdir,
		Resync: true, Filters: []string{"- **/*.tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 3 || gotArgv[0] != "bisync" || gotArgv[1] != path1 || gotArgv[2] != "gdrive:x" {
		t.Fatalf("argv = %v, want [bisync %s gdrive:x ...]", gotArgv, path1)
	}
	for _, want := range []string{
		"--resync", "--resilient", "--recover", "--create-empty-src-dirs",
		"--drive-skip-gdocs", "--local-no-check-updated", "--retries",
	} {
		if !containsArg(gotArgv, want) {
			t.Errorf("argv %v missing %q", gotArgv, want)
		}
	}
	for flag, want := range map[string]string{
		"--workdir": workdir, "--max-delete": "50",
		"--conflict-resolve": "newer", "--conflict-loser": "num",
		"--compare": "size,modtime,checksum",
	} {
		idx := indexOf(gotArgv, flag)
		if idx == -1 || idx+1 >= len(gotArgv) {
			t.Errorf("argv %v missing %s <value>", gotArgv, flag)
			continue
		}
		if gotArgv[idx+1] != want {
			t.Errorf("%s = %v, want %q", flag, gotArgv[idx+1], want)
		}
	}
	if filterPath == "" {
		t.Fatalf("argv %v missing --filters-file <path>", gotArgv)
	}
	if filterFileReadErr != nil {
		t.Fatalf("read --filters-file during the fake call: %v", filterFileReadErr)
	}
	if filterFileContent != "- **/*.tmp\n" {
		t.Errorf("filters file content = %q, want %q", filterFileContent, "- **/*.tmp\n")
	}
	if _, err := os.Stat(filterPath); !os.IsNotExist(err) {
		t.Errorf("--filters-file temp file %q still exists after Bisync returns, want removed", filterPath)
	}
}

// TestBisyncResyncFlag verifies Resync:true/false controls whether --resync
// is present in the bisync argv.
func TestBisyncResyncFlag(t *testing.T) {
	for _, resync := range []bool{true, false} {
		var gotArgv []string
		e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
			gotArgv = args
			return "", "", nil
		})
		_, err := e.Bisync(BisyncParams{Path1: t.TempDir(), Path2: "gdrive:x", Workdir: t.TempDir(), Resync: resync})
		if err != nil {
			t.Fatal(err)
		}
		if got := containsArg(gotArgv, "--resync"); got != resync {
			t.Errorf("Resync=%v: --resync present = %v, want %v (argv=%v)", resync, got, resync, gotArgv)
		}
	}
}

// TestBisyncEnsuresRemoteDirOnResync verifies ensureRemoteDir's rclone call:
// on a --resync run, Bisync must run `rclone mkdir gdrive:sub` before
// `rclone bisync ...` - rclone bisync --resync aborts when path2's root
// doesn't exist yet.
func TestBisyncEnsuresRemoteDirOnResync(t *testing.T) {
	var calls [][]string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:sub", Workdir: t.TempDir(), Resync: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mkdirIdx, bisyncIdx := -1, -1
	for i, argv := range calls {
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "mkdir":
			if mkdirIdx == -1 {
				mkdirIdx = i
				if len(argv) < 2 || argv[1] != "gdrive:sub" {
					t.Errorf("mkdir argv = %v, want [mkdir gdrive:sub]", argv)
				}
			}
		case "bisync":
			if bisyncIdx == -1 {
				bisyncIdx = i
			}
		}
	}
	if mkdirIdx == -1 {
		t.Fatal("no mkdir call recorded")
	}
	if bisyncIdx == -1 {
		t.Fatal("no bisync call recorded")
	}
	if mkdirIdx >= bisyncIdx {
		t.Fatalf("mkdir (call %d) must happen before bisync (call %d)", mkdirIdx, bisyncIdx)
	}
}

// TestBisyncSkipsMkdirWhenNotResync verifies ensureRemoteDir is only invoked
// on --resync runs: a normal (non-resync) bisync must not touch path2's root,
// since it may legitimately not exist as a subfolder yet on later runs.
func TestBisyncSkipsMkdirWhenNotResync(t *testing.T) {
	var calls [][]string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:sub", Workdir: t.TempDir(), Resync: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range calls {
		if len(argv) > 0 && argv[0] == "mkdir" {
			t.Fatalf("unexpected mkdir call on non-resync run: %v", argv)
		}
	}
}

// TestBisyncNeedsResyncMappedFromStderr verifies a stderr message telling the
// caller to (re-)run --resync (case-insensitive) is mapped to ErrNeedsResync.
func TestBisyncNeedsResyncMappedFromStderr(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "cannot find prior Path1 or Path2 listings, likely due to critical error. Must run --resync", errors.New("exit status 7")
	})
	_, err := e.Bisync(BisyncParams{Path1: "a", Path2: "b", Workdir: t.TempDir()})
	if !errors.Is(err, ErrNeedsResync) {
		t.Fatalf("want ErrNeedsResync, got %v", err)
	}
}

// TestRemoteExists verifies RemoteExists parses `rclone listremotes` output
// (one "name:" per line) and matches by name with the trailing colon stripped.
func TestRemoteExists(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "gdrive:\nother:\n", "", nil
	})
	ok, err := e.RemoteExists("gdrive")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, err = e.RemoteExists("missing")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
	}
}

// TestRemoteConfiguredWithToken verifies RemoteConfigured parses `rclone
// config show <name>` output and reports true when a non-empty "token" line
// is present.
func TestRemoteConfiguredWithToken(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "[gdrive]\ntype = drive\nskip_gdocs = true\ntoken = {\"access_token\":\"x\"}\n", "", nil
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want true, nil", ok, err)
	}
	if len(gotArgv) < 3 || gotArgv[0] != "config" || gotArgv[1] != "show" || gotArgv[2] != "gdrive" {
		t.Fatalf("argv = %v, want [config show gdrive]", gotArgv)
	}
}

// TestRemoteConfiguredTokenless verifies a remote whose config/create hasn't
// finished OAuth yet (no "token" line at all) is reported as not configured.
func TestRemoteConfiguredTokenless(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "[gdrive]\ntype = drive\nskip_gdocs = true\n", "", nil
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
	}
}

// TestRemoteConfiguredErrorTreatedAsMissing verifies a `rclone config show`
// failure (e.g. no such remote) is treated the same as "not configured".
func TestRemoteConfiguredErrorTreatedAsMissing(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "didn't find section in config file", errors.New("exit status 1")
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
	}
}

// TestListRemote verifies ListRemote calls `rclone lsf <remotePath>` and
// strips each entry's trailing "/" (rclone lsf's default directory marker).
func TestListRemote(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "keep.txt\nsub/\n", "", nil
	})
	names, err := e.ListRemote("gdrive:better-drive-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 2 || gotArgv[0] != "lsf" || gotArgv[1] != "gdrive:better-drive-e2e" {
		t.Fatalf("argv = %v, want [lsf gdrive:better-drive-e2e]", gotArgv)
	}
	want := []string{"keep.txt", "sub"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestListRemoteEmpty(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "", nil
	})
	names, err := e.ListRemote("gdrive:better-drive-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %#v, want empty", names)
	}
}

// TestCreateDriveRemote verifies CreateDriveRemote issues a single `rclone
// config create <name> drive` call. skip_gdocs is NOT passed here - it is
// applied per-invocation via the global --drive-skip-gdocs flag (see
// CreateDriveRemote's doc).
func TestCreateDriveRemote(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.CreateDriveRemote("gdrive", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"config", "create", "gdrive", "drive"}
	if !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("argv = %#v, want %#v", gotArgv, want)
	}
}

// TestCreateDriveRemoteWithParams verifies extra backend params are appended
// as sorted "key=value" args (sorted for a deterministic, reviewable argv).
func TestCreateDriveRemoteWithParams(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.CreateDriveRemote("gdrive", map[string]string{"scope": "drive", "team_drive": "abc"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"config", "create", "gdrive", "drive", "scope=drive", "team_drive=abc"}
	if !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("argv = %#v, want %#v", gotArgv, want)
	}
}

// TestBisyncDryRunPassesFlagToRclone verifies BisyncParams.DryRun appends
// --dry-run to the rclone bisync argv, so a caller can preview a bisync cycle
// (including its delete propagation) without applying any change.
func TestBisyncDryRunPassesFlagToRclone(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{Path1: t.TempDir(), Path2: "gdrive:x", Workdir: t.TempDir(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !containsArg(gotArgv, "--dry-run") {
		t.Errorf("argv %v missing --dry-run", gotArgv)
	}
}

// TestBisyncOmitsDryRunWhenFalse verifies the zero value of DryRun does not
// add --dry-run - a normal bisync run must apply its changes.
func TestBisyncOmitsDryRunWhenFalse(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{Path1: t.TempDir(), Path2: "gdrive:x", Workdir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(gotArgv, "--dry-run") {
		t.Errorf("argv %v has --dry-run, want omitted for DryRun=false", gotArgv)
	}
}

// TestSyncDryRunPassesFlagToRclone verifies CopyParams.DryRun appends
// --dry-run to `rclone sync`'s argv - this is the mode the dry-run feature
// exists for: mode="sync" deletes remote files absent locally, and --dry-run
// is the only way to preview that deletion before it happens.
func TestSyncDryRunPassesFlagToRclone(t *testing.T) {
	path1 := t.TempDir()
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Sync(CopyParams{Local: path1, Remote: "gdrive:Mirror", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if gotArgv[0] != "sync" {
		t.Fatalf("subcommand = %q, want sync", gotArgv[0])
	}
	if !containsArg(gotArgv, "--dry-run") {
		t.Errorf("argv %v missing --dry-run", gotArgv)
	}
}

// TestBisyncResyncDryRunSkipsRealMkdir verifies Resync+DryRun together never
// run a REAL `rclone mkdir` against the remote (nor os.MkdirAll on the local
// side) - the --resync setup step is a genuine write, so "no changes will be
// made" must skip it, not just append --dry-run to the bisync argv itself.
func TestBisyncResyncDryRunSkipsRealMkdir(t *testing.T) {
	path1 := filepath.Join(t.TempDir(), "not-yet-created")
	var calls [][]string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		calls = append(calls, args)
		return "", "", nil
	})
	_, err := e.Bisync(BisyncParams{Path1: path1, Path2: "gdrive:sub", Workdir: t.TempDir(), Resync: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range calls {
		if len(argv) > 0 && argv[0] == "mkdir" {
			t.Fatalf("unexpected real `rclone mkdir` call under DryRun: %v", argv)
		}
	}
	if _, statErr := os.Stat(path1); !os.IsNotExist(statErr) {
		t.Errorf("Path1 %q was created on disk under DryRun, want left absent", path1)
	}
}

// TestCopyDryRunPassesFlagToRclone mirrors the sync case for Copy, so preview
// works uniformly regardless of which pair mode is configured.
func TestCopyDryRunPassesFlagToRclone(t *testing.T) {
	path1 := t.TempDir()
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: path1, Remote: "gdrive:Backup", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !containsArg(gotArgv, "--dry-run") {
		t.Errorf("argv %v missing --dry-run", gotArgv)
	}
}

// TestBisyncGenericErrorNotResync verifies a generic rclone failure surfaces
// as a plain error from Bisync, not classified as ErrNeedsResync.
func TestBisyncGenericErrorNotResync(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "permission denied", errors.New("exit status 1")
	})
	_, err := e.Bisync(BisyncParams{Path1: "a", Path2: "b", Workdir: t.TempDir()})
	if err == nil {
		t.Fatal("want non-nil error")
	}
	if errors.Is(err, ErrNeedsResync) {
		t.Fatalf("generic error must NOT be classified as ErrNeedsResync, got %v", err)
	}
}

// indexOf returns the index of want in argv, or -1 if not present.
func indexOf(argv []string, want string) int {
	for i, a := range argv {
		if a == want {
			return i
		}
	}
	return -1
}

// containsArg reports whether want is present anywhere in argv.
func containsArg(argv []string, want string) bool { return indexOf(argv, want) != -1 }

// TestCopyBuildsRcloneArgs verifies Copy builds `rclone copy <local> <remote>`
// with the perf/retry/skip_gdocs/create-empty-src-dirs flags, and a
// "--filter-from <tmpfile>" whose content is the joined filter lines (one per
// line) - the temp file written via os.CreateTemp and removed again once Copy
// returns (the deferred os.Remove).
func TestCopyBuildsRcloneArgs(t *testing.T) {
	path1 := t.TempDir()
	var gotArgv []string
	var filterFileContent string
	var filterFileReadErr error
	var filterPath string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		// Read the --filter-from file HERE, while Copy's defer cleanup() has
		// not yet run (it fires only after this fake call returns) - this is
		// the one point in the test where the temp file is guaranteed to
		// still exist.
		if idx := indexOf(args, "--filter-from"); idx != -1 && idx+1 < len(args) {
			filterPath = args[idx+1]
			data, err := os.ReadFile(filterPath)
			filterFileContent, filterFileReadErr = string(data), err
		}
		return "", "", nil
	})
	err := e.Copy(CopyParams{
		Local: path1, Remote: "gdrive:Backup", Workdir: t.TempDir(),
		Filters: []string{"- **/*.tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 3 || gotArgv[0] != "copy" || gotArgv[1] != path1 || gotArgv[2] != "gdrive:Backup" {
		t.Fatalf("argv = %v, want [copy %s gdrive:Backup ...]", gotArgv, path1)
	}
	for _, want := range []string{"--drive-skip-gdocs", "--local-no-check-updated", "--retries", "--transfers", "--create-empty-src-dirs"} {
		if !containsArg(gotArgv, want) {
			t.Errorf("argv %v missing %q", gotArgv, want)
		}
	}
	if filterPath == "" {
		t.Fatalf("argv %v missing --filter-from <path>", gotArgv)
	}
	if filterFileReadErr != nil {
		t.Fatalf("read --filter-from file during the fake call: %v", filterFileReadErr)
	}
	if filterFileContent != "- **/*.tmp\n" {
		t.Errorf("filter file content = %q, want %q", filterFileContent, "- **/*.tmp\n")
	}
	if _, err := os.Stat(filterPath); !os.IsNotExist(err) {
		t.Errorf("--filter-from temp file %q still exists after Copy returns, want removed", filterPath)
	}
}

// TestCopyOmitsFilterFlagWhenEmpty verifies Copy does not pass --filter-from
// at all when there are no filters (no temp file created either).
func TestCopyOmitsFilterFlagWhenEmpty(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: t.TempDir(), Remote: "gdrive:Backup"}); err != nil {
		t.Fatal(err)
	}
	if containsArg(gotArgv, "--filter-from") {
		t.Errorf("argv %v has --filter-from, want omitted for empty Filters", gotArgv)
	}
}

// TestSyncUsesSyncSubcommand verifies Sync builds `rclone sync <local> <remote>`
// (mirror - deletes on dst to match src), the same argv shape as Copy otherwise.
func TestSyncUsesSyncSubcommand(t *testing.T) {
	path1 := t.TempDir()
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Sync(CopyParams{Local: path1, Remote: "gdrive:Mirror"}); err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 3 || gotArgv[0] != "sync" || gotArgv[1] != path1 || gotArgv[2] != "gdrive:Mirror" {
		t.Fatalf("argv = %v, want [sync %s gdrive:Mirror ...]", gotArgv, path1)
	}
}

// TestCopyPropagatesRunnerError verifies a runner error surfaces from Copy
// with rclone's stderr folded in for diagnostics (no ErrNeedsResync
// classification applies to 1-way modes).
func TestCopyPropagatesRunnerError(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "permission denied", errors.New("exit status 1")
	})
	err := e.Copy(CopyParams{Local: t.TempDir(), Remote: "gdrive:b"})
	if err == nil {
		t.Fatal("want non-nil error")
	}
	if errors.Is(err, ErrNeedsResync) {
		t.Fatalf("Copy error must NOT be classified as ErrNeedsResync, got %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want it to include rclone's stderr", err)
	}
}

// TestCopyPrependsConfigFlagWhenSet verifies the engine's cfg path is
// forwarded as a leading "--config <path>" on every invocation.
func TestCopyPrependsConfigFlagWhenSet(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("X:/portable/rclone.conf", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: t.TempDir(), Remote: "gdrive:Backup"}); err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 2 || gotArgv[0] != "--config" || gotArgv[1] != "X:/portable/rclone.conf" {
		t.Fatalf("argv = %v, want [--config X:/portable/rclone.conf ...]", gotArgv)
	}
}

// TestDeleteRemote verifies DeleteRemote issues `rclone config delete <name>`.
func TestDeleteRemote(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.DeleteRemote("bdfixtest"); err != nil {
		t.Fatal(err)
	}
	want := []string{"config", "delete", "bdfixtest"}
	if !reflect.DeepEqual(gotArgv, want) {
		t.Fatalf("argv = %#v, want %#v", gotArgv, want)
	}
}

// TestCopyFileLocalUsesCopyto verifies a pair whose Local is a single file
// (e.g. ~/.claude.json) dispatches to `rclone copyto <file> <remoteDir>/<base>`
// (skip_gdocs and retries via global flags, not a connection string) and
// omits --filter-from (filters do not apply to a single-file copy).
func TestCopyFileLocalUsesCopyto(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(filePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	err := e.Copy(CopyParams{Local: filePath, Remote: "gdrive:Backups/claude", Filters: []string{"- **/*.tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 3 || gotArgv[0] != "copyto" {
		t.Fatalf("argv = %v, want [copyto ...]", gotArgv)
	}
	if gotArgv[1] != filePath {
		t.Errorf("argv[1] (source) = %v, want %v", gotArgv[1], filePath)
	}
	if gotArgv[2] != "gdrive:Backups/claude/claude.json" {
		t.Errorf("argv[2] (dest) = %v, want gdrive:Backups/claude/claude.json", gotArgv[2])
	}
	if containsArg(gotArgv, "--filter-from") {
		t.Errorf("argv %v has --filter-from, want omitted for single-file copy", gotArgv)
	}
}

// TestCopyFileLocalDryRunPassesFlagToRclone verifies DryRun also reaches the
// single-file (`rclone copyto`) dispatch path, not just the directory
// `rclone copy`/`sync` path - copyLocalFile takes DryRun as an explicit
// parameter precisely so this case is not silently skipped.
func TestCopyFileLocalDryRunPassesFlagToRclone(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(filePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: filePath, Remote: "gdrive:Backups/claude", DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if gotArgv[0] != "copyto" {
		t.Fatalf("argv = %v, want [copyto ...]", gotArgv)
	}
	if !containsArg(gotArgv, "--dry-run") {
		t.Errorf("argv %v missing --dry-run", gotArgv)
	}
}

// TestSyncFileLocalUsesCopyto mirrors the Copy file-local test for Sync: a
// single-file Local has no "extra content" on the dst side to mirror away, so
// Sync collapses to the same copyto call.
func TestSyncFileLocalUsesCopyto(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(filePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return "", "", nil
	})
	if err := e.Sync(CopyParams{Local: filePath, Remote: "gdrive:Backups/claude"}); err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) < 2 || gotArgv[0] != "copyto" || gotArgv[1] != filePath {
		t.Fatalf("argv = %v, want [copyto %s ...]", gotArgv, filePath)
	}
}

// TestCopyWithDirLocalStillUsesCopySubcommand is a regression guard: a
// directory Local (the pre-existing, common case) must keep using `rclone
// copy`, not the single-file copyto path. TestCopyBuildsRcloneArgs already
// covers this (its path1 is a t.TempDir() directory) - this test names the
// guarantee explicitly for the file-local feature's benefit.
func TestCopyWithDirLocalStillUsesCopySubcommand(t *testing.T) {
	dir := t.TempDir()
	var gotSubcommand string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		if len(args) > 0 {
			gotSubcommand = args[0]
		}
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: dir, Remote: "gdrive:Backup"}); err != nil {
		t.Fatal(err)
	}
	if gotSubcommand != "copy" {
		t.Fatalf("subcommand = %q, want copy for a directory Local", gotSubcommand)
	}
}

// TestCopyWithNonexistentLocalFallsBackToCopySubcommand locks in the fallback
// decision for isFileLocal: a Local that does not exist (os.Stat fails) is
// NOT treated as a single file - it keeps the pre-existing directory `rclone
// copy` behavior and lets rclone report its own error, rather than silently
// changing dispatch based on a stat failure.
func TestCopyWithNonexistentLocalFallsBackToCopySubcommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	var gotSubcommand string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		if len(args) > 0 {
			gotSubcommand = args[0]
		}
		return "", "", nil
	})
	if err := e.Copy(CopyParams{Local: missing, Remote: "gdrive:Backup"}); err != nil {
		t.Fatal(err)
	}
	if gotSubcommand != "copy" {
		t.Fatalf("subcommand = %q, want copy for a nonexistent Local", gotSubcommand)
	}
}

// TestSyncOpsSerialize verifies the engine mutex serializes Copy/Sync/Bisync
// subprocess invocations - kept as cheap insurance against overlapping runs
// (see the syncMu doc comment on Engine).
func TestSyncOpsSerialize(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(3 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return "", "", nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.Copy(CopyParams{Local: "a", Remote: "gdrive:b", Workdir: t.TempDir()})
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("concurrent sync ops overlapped: maxActive=%d, want 1 (engine mutex must serialize)", maxActive)
	}
}
