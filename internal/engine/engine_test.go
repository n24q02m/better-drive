package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestEngine inject fake rpc, không gọi librclone thật.
func newTestEngine(fn func(method, input string) (string, int)) *Engine {
	return &Engine{rpc: fn}
}

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

// recordedCall captures one fake-rpc invocation (method + raw JSON input),
// used by tests that need to assert call order/count instead of just the
// last call.
type recordedCall struct {
	method string
	input  string
}

func TestBisyncBuildsParams(t *testing.T) {
	// path1 must be a real (but disposable) directory: Resync:true makes
	// Bisync os.MkdirAll(p.Path1), and a hard-coded "C:/x" would leak that
	// dir onto the real disk every time the unit suite runs.
	path1 := t.TempDir()
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{}`, 200
	})
	_, err := e.Bisync(BisyncParams{
		Path1: path1, Path2: "gdrive:x", Workdir: t.TempDir(),
		Resync: true, Filters: []string{"- **/*.tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "sync/bisync" {
		t.Fatalf("method = %q", gotMethod)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		// path2 carries the runtime skip_gdocs connection string (see withSkipGdocs).
		"path1": path1, "path2": "gdrive,skip_gdocs=true:x", "resync": true,
		"conflictResolve": "newer", "conflictLoser": "num", "resilient": true,
		// JSON numbers unmarshal into map[string]any as float64.
		"maxDelete": float64(50),
	} {
		if m[k] != want {
			t.Errorf("param %s = %v, want %v", k, m[k], want)
		}
	}
	if !strings.HasSuffix(m["filtersFile"].(string), "filters.txt") {
		t.Errorf("filtersFile = %v", m["filtersFile"])
	}
	data, err := os.ReadFile(m["filtersFile"].(string))
	if err != nil {
		t.Fatalf("read filters file: %v", err)
	}
	if string(data) != "- **/*.tmp\n" {
		t.Errorf("filters file content = %q, want %q", string(data), "- **/*.tmp\n")
	}
}

// TestBisyncEnsuresRemoteDirOnResync verifies ensureRemoteDir's split: on a
// --resync run, Bisync must call operations/mkdir on path2's remote root
// (fs="gdrive:", remote="sub") before it calls sync/bisync itself - rclone
// bisync --resync aborts when path2's root doesn't exist yet.
func TestBisyncEnsuresRemoteDirOnResync(t *testing.T) {
	var calls []recordedCall
	e := newTestEngine(func(method, input string) (string, int) {
		calls = append(calls, recordedCall{method: method, input: input})
		return `{}`, 200
	})
	_, err := e.Bisync(BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:sub", Workdir: t.TempDir(), Resync: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mkdirIdx, bisyncIdx := -1, -1
	for i, c := range calls {
		if c.method == "operations/mkdir" && mkdirIdx == -1 {
			mkdirIdx = i
			var m map[string]any
			if err := json.Unmarshal([]byte(c.input), &m); err != nil {
				t.Fatal(err)
			}
			if m["fs"] != "gdrive:" {
				t.Errorf("mkdir fs = %v, want %q", m["fs"], "gdrive:")
			}
			if m["remote"] != "sub" {
				t.Errorf("mkdir remote = %v, want %q", m["remote"], "sub")
			}
		}
		if c.method == "sync/bisync" && bisyncIdx == -1 {
			bisyncIdx = i
		}
	}
	if mkdirIdx == -1 {
		t.Fatal("no operations/mkdir call recorded")
	}
	if bisyncIdx == -1 {
		t.Fatal("no sync/bisync call recorded")
	}
	if mkdirIdx >= bisyncIdx {
		t.Fatalf("operations/mkdir (call %d) must happen before sync/bisync (call %d)", mkdirIdx, bisyncIdx)
	}
}

// TestBisyncSkipsMkdirWhenNotResync verifies ensureRemoteDir is only invoked
// on --resync runs: a normal (non-resync) bisync must not touch path2's root,
// since it may legitimately not exist as a subfolder yet on later runs.
func TestBisyncSkipsMkdirWhenNotResync(t *testing.T) {
	var calls []recordedCall
	e := newTestEngine(func(method, input string) (string, int) {
		calls = append(calls, recordedCall{method: method, input: input})
		return `{}`, 200
	})
	_, err := e.Bisync(BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:sub", Workdir: t.TempDir(), Resync: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.method == "operations/mkdir" {
			t.Fatalf("unexpected operations/mkdir call on non-resync run: %s", c.input)
		}
	}
}

func TestBisyncNeedsResyncError(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"error":"cannot find prior Path1 or Path2 listings, likely due to critical error. must run --resync"}`, 500
	})
	_, err := e.Bisync(BisyncParams{Path1: "a", Path2: "b", Workdir: t.TempDir()})
	if !errors.Is(err, ErrNeedsResync) {
		t.Fatalf("want ErrNeedsResync, got %v", err)
	}
}

func TestRemoteExists(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"remotes":["gdrive","other"]}`, 200
	})
	ok, err := e.RemoteExists("gdrive")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestRemoteConfiguredWithToken(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		if method != "config/get" {
			t.Fatalf("method = %q, want config/get", method)
		}
		return `{"type":"drive","skip_gdocs":"true","token":"{\"access_token\":\"x\"}"}`, 200
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want true, nil", ok, err)
	}
}

func TestRemoteConfiguredTokenless(t *testing.T) {
	// Rclone rc config/get on a remote whose config/create hasn't finished OAuth
	// yet returns the stanza without a "token" key at all (verified empirically).
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"type":"drive","skip_gdocs":"true"}`, 200
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestRemoteConfiguredErrorTreatedAsMissing(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"error":"didn't find section in config file"}`, 500
	})
	ok, err := e.RemoteConfigured("gdrive")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestListRemote(t *testing.T) {
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{"list":[
			{"Path":"keep.txt","Name":"keep.txt","Size":2,"IsDir":false},
			{"Path":"sub","Name":"sub","Size":0,"IsDir":true}
		]}`, 200
	})
	names, err := e.ListRemote("gdrive:better-drive-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "operations/list" {
		t.Fatalf("method = %q, want operations/list", gotMethod)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	if m["fs"] != "gdrive:better-drive-e2e" {
		t.Errorf("fs = %v, want gdrive:better-drive-e2e", m["fs"])
	}
	if m["remote"] != "" {
		t.Errorf("remote = %v, want empty string", m["remote"])
	}
	want := []string{"keep.txt", "sub"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestListRemoteEmpty(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"list":[]}`, 200
	})
	names, err := e.ListRemote("gdrive:better-drive-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("names = %#v, want empty", names)
	}
}

// TestCreateDriveRemote verifies CreateDriveRemote issues a single config/create
// for a plain "drive" remote. skip_gdocs is NOT stored here - it is applied at
// runtime via withSkipGdocs on the bisync path (see CreateDriveRemote's doc).
func TestCreateDriveRemote(t *testing.T) {
	var calls []recordedCall
	e := newTestEngine(func(method, input string) (string, int) {
		calls = append(calls, recordedCall{method: method, input: input})
		return `{}`, 200
	})
	if err := e.CreateDriveRemote("gdrive", nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want 1 (config/create only)", calls)
	}
	if calls[0].method != "config/create" {
		t.Fatalf("method = %q, want config/create", calls[0].method)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(calls[0].input), &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "drive" {
		t.Errorf("type = %v, want %q", m["type"], "drive")
	}
	if m["name"] != "gdrive" {
		t.Errorf("name = %v, want %q", m["name"], "gdrive")
	}
}

// TestWithSkipGdocs verifies the runtime connection-string transform: a Drive
// remote path gains ",skip_gdocs=true" before the ":"; a local/plain path with
// no remote is returned unchanged.
func TestWithSkipGdocs(t *testing.T) {
	cases := map[string]string{
		"gdrive:Backup":     "gdrive,skip_gdocs=true:Backup",
		"gdrive:":           "gdrive,skip_gdocs=true:",
		"gdrive:a/b/c":      "gdrive,skip_gdocs=true:a/b/c",
		"/home/user/folder": "/home/user/folder",
	}
	for in, want := range cases {
		if got := withSkipGdocs(in); got != want {
			t.Errorf("withSkipGdocs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCallGenericErrorNotResync(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"error":"permission denied"}`, 500
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

func TestDeleteRemote(t *testing.T) {
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{}`, 200
	})
	if err := e.DeleteRemote("bdfixtest"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "config/delete" {
		t.Fatalf("method = %q, want config/delete", gotMethod)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "bdfixtest" {
		t.Errorf("name = %v, want bdfixtest", m["name"])
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

// swapBackoff sets retryBackoffUnit to d (0 in tests, to avoid real sleeps) and
// returns a restore func for defer.
func swapBackoff(d time.Duration) func() {
	old := retryBackoffUnit
	retryBackoffUnit = d
	return func() { retryBackoffUnit = old }
}

func TestIsRetryable(t *testing.T) {
	cases := map[string]bool{
		`rc sync/copy status=500: {"error":"googleapi: Error 403: Quota exceeded ... rateLimitExceeded"}`: true,
		`rc sync/copy status=500: {"error":"connection reset by peer"}`:                                    true,
		`rc sync/copy status=500: {"error":"i/o timeout"}`:                                                 true,
		`rc sync/copy status=500: {"error":"didn't find section in config file (\"gdrive\")"}`:             false,
		`rc sync/copy status=500: {"error":"directory not found"}`:                                         false,
	}
	for msg, want := range cases {
		if got := isRetryable(errors.New(msg)); got != want {
			t.Errorf("isRetryable(%q) = %v, want %v", msg, got, want)
		}
	}
}

// TestCallWithRetryRetriesTransientThenSucceeds verifies a transient rate-limit
// error is retried and a subsequent success is returned (the whole op re-runs,
// as rclone's cmd.Run does but the rc method does not).
func TestCallWithRetryRetriesTransientThenSucceeds(t *testing.T) {
	defer swapBackoff(0)()
	calls := 0
	e := newTestEngine(func(method, input string) (string, int) {
		calls++
		if calls < 2 {
			return `{"error":"googleapi: Error 403: rateLimitExceeded"}`, 500
		}
		return `{}`, 200
	})
	if _, err := e.callWithRetry("sync/copy", map[string]any{}); err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
	if calls != 2 {
		t.Errorf("want 2 calls (1 transient fail + 1 success), got %d", calls)
	}
}

// TestCallWithRetryFailsFastOnFatal verifies a fatal (config) error is NOT
// retried - a retry cannot fix a missing remote, so it returns after one call.
func TestCallWithRetryFailsFastOnFatal(t *testing.T) {
	defer swapBackoff(0)()
	calls := 0
	e := newTestEngine(func(method, input string) (string, int) {
		calls++
		return `{"error":"didn't find section in config file (\"gdrive\")"}`, 500
	})
	if _, err := e.callWithRetry("sync/copy", map[string]any{}); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Errorf("fatal error must not retry: want 1 call, got %d", calls)
	}
}

// TestCallWithRetryExhaustsOnPersistentTransient verifies retries are bounded:
// a persistently transient error stops after maxAttempts and returns the error.
func TestCallWithRetryExhaustsOnPersistentTransient(t *testing.T) {
	defer swapBackoff(0)()
	calls := 0
	e := newTestEngine(func(method, input string) (string, int) {
		calls++
		return `{"error":"rateLimitExceeded"}`, 500
	})
	if _, err := e.callWithRetry("sync/copy", map[string]any{}); err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("want 3 bounded attempts, got %d", calls)
	}
}
