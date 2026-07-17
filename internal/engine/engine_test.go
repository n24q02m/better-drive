package engine

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// newTestEngine inject fake rpc, không gọi librclone thật.
func newTestEngine(fn func(method, input string) (string, int)) *Engine {
	return &Engine{rpc: fn}
}

func TestBisyncBuildsParams(t *testing.T) {
	var gotMethod, gotInput string
	e := newTestEngine(func(method, input string) (string, int) {
		gotMethod, gotInput = method, input
		return `{}`, 200
	})
	_, err := e.Bisync(BisyncParams{
		Path1: "C:/x", Path2: "gdrive:x", Workdir: t.TempDir(),
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
		"path1": "C:/x", "path2": "gdrive:x", "resync": true,
		"conflictResolve": "newer", "conflictLoser": "num", "resilient": true,
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
