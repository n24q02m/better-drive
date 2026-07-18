package engine

import (
	"encoding/json"
	"errors"
	"os"
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

// TestCopyBuildsParams verifies Copy calls rc "sync/copy" with srcFs=local,
// dstFs carrying the runtime skip_gdocs connection string, createEmptySrcDirs
// set, and filters passed through the "_filter.FilterRule" mechanism (the
// same "+ "/"- " prefixed rule syntax as bisync's filters file - verified
// empirically against rclone v1.74.4's fs/sync/rc.go + fs/rc/jobs/job.go
// getFilter, which Reshapes "_filter" via encoding/json using the Go field
// name FilterRule, not the RulesOpt "filter" config-tag name).
func TestCopyBuildsParams(t *testing.T) {
	path1 := t.TempDir()
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{}`, 200
	})
	err := e.Copy(CopyParams{
		Local: path1, Remote: "gdrive:Backup", Workdir: t.TempDir(),
		Filters: []string{"- **/*.tmp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "sync/copy" {
		t.Fatalf("method = %q, want sync/copy", gotMethod)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	if m["srcFs"] != path1 {
		t.Errorf("srcFs = %v, want %v", m["srcFs"], path1)
	}
	if m["dstFs"] != "gdrive,skip_gdocs=true:Backup" {
		t.Errorf("dstFs = %v, want gdrive,skip_gdocs=true:Backup", m["dstFs"])
	}
	if m["createEmptySrcDirs"] != true {
		t.Errorf("createEmptySrcDirs = %v, want true", m["createEmptySrcDirs"])
	}
	filter, ok := m["_filter"].(map[string]any)
	if !ok {
		t.Fatalf("_filter = %v (%T), want map", m["_filter"], m["_filter"])
	}
	rules, ok := filter["FilterRule"].([]any)
	if !ok || len(rules) != 1 || rules[0] != "- **/*.tmp" {
		t.Errorf("_filter.FilterRule = %v, want [\"- **/*.tmp\"]", filter["FilterRule"])
	}
}

// TestCopyOmitsFilterWhenEmpty verifies Copy does not send an empty "_filter"
// object when there are no filters (harmless either way, but keeps the rc
// payload minimal and matches what a .driveignore-less pair sends today).
func TestCopyOmitsFilterWhenEmpty(t *testing.T) {
	var gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotInput = input
		return `{}`, 200
	})
	if err := e.Copy(CopyParams{Local: t.TempDir(), Remote: "gdrive:Backup"}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["_filter"]; ok {
		t.Errorf("_filter present = %v, want omitted for empty Filters", m["_filter"])
	}
}

// TestSyncBuildsParams verifies Sync calls rc "sync/sync" (mirror - deletes on
// dst to match src) with the same srcFs/dstFs/filter shape as Copy.
func TestSyncBuildsParams(t *testing.T) {
	path1 := t.TempDir()
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{}`, 200
	})
	err := e.Sync(CopyParams{Local: path1, Remote: "gdrive:Mirror"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "sync/sync" {
		t.Fatalf("method = %q, want sync/sync", gotMethod)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(gotInput), &m); err != nil {
		t.Fatal(err)
	}
	if m["srcFs"] != path1 {
		t.Errorf("srcFs = %v, want %v", m["srcFs"], path1)
	}
	if m["dstFs"] != "gdrive,skip_gdocs=true:Mirror" {
		t.Errorf("dstFs = %v, want gdrive,skip_gdocs=true:Mirror", m["dstFs"])
	}
}

// TestCopyPropagatesRcError verifies a non-200 rc response surfaces as an
// error from Copy (no ErrNeedsResync classification applies to 1-way modes).
func TestCopyPropagatesRcError(t *testing.T) {
	e := newTestEngine(func(method, input string) (string, int) {
		return `{"error":"permission denied"}`, 500
	})
	err := e.Copy(CopyParams{Local: "a", Remote: "gdrive:b"})
	if err == nil {
		t.Fatal("want non-nil error")
	}
	if errors.Is(err, ErrNeedsResync) {
		t.Fatalf("Copy error must NOT be classified as ErrNeedsResync, got %v", err)
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

// TestSyncOpsSerialize verifies the engine mutex serializes Copy/Sync/Bisync.
// rclone applies the rc _filter to process-global state during a sync, so two
// syncs must never overlap (verified E2E that concurrency silently crosses
// filters and empties a dest with err=nil).
func TestSyncOpsSerialize(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	e := newTestEngine(func(method, input string) (string, int) {
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
		return `{}`, 200
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
