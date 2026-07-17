package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/rclone/rclone/backend/drive"
	"github.com/rclone/rclone/librclone/librclone"
)

var ErrNeedsResync = errors.New("bisync needs --resync (baseline lost)")

type Engine struct {
	rpc func(method, input string) (string, int)
}

func New() *Engine {
	librclone.Initialize()
	return &Engine{rpc: librclone.RPC}
}

func (e *Engine) Close() { librclone.Finalize() }

func (e *Engine) call(method string, params map[string]any) (map[string]any, error) {
	in, _ := json.Marshal(params)
	out, status := e.rpc(method, string(in))
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if status != 200 {
		msg, _ := res["error"].(string)
		if strings.Contains(strings.ToLower(msg+out), "must run --resync") {
			return res, ErrNeedsResync
		}
		return res, fmt.Errorf("rc %s status=%d: %s", method, status, out)
	}
	return res, nil
}

func (e *Engine) RemoteExists(name string) (bool, error) {
	res, err := e.call("config/listremotes", map[string]any{})
	if err != nil {
		return false, err
	}
	list, _ := res["remotes"].([]any)
	for _, r := range list {
		if s, _ := r.(string); s == name {
			return true, nil
		}
	}
	return false, nil
}

// RemoteConfigured reports whether name is a remote with a valid OAuth token,
// as opposed to a broken, token-less stanza left behind by an interrupted
// config/create (see CreateDriveRemote doc). Verified empirically: rc
// config/get returns status=200 for a missing remote too (empty {} body,
// no "error" field) - so a non-200/err response is treated the same as
// "not configured" rather than being distinguished as a separate case.
func (e *Engine) RemoteConfigured(name string) (bool, error) {
	res, err := e.call("config/get", map[string]any{"name": name})
	if err != nil {
		return false, nil
	}
	token, _ := res["token"].(string)
	return token != "", nil
}

// DeleteRemote removes a remote's config stanza (used to clear a broken,
// token-less remote before recreating it).
func (e *Engine) DeleteRemote(name string) error {
	_, err := e.call("config/delete", map[string]any{"name": name})
	return err
}

func (e *Engine) CreateDriveRemote(name string, params map[string]string) error {
	p := map[string]any{
		"name":       name,
		"type":       "drive",
		"parameters": mergeDefaults(params),
	}
	_, err := e.call("config/create", p)
	return err
}

func mergeDefaults(in map[string]string) map[string]string {
	out := map[string]string{"skip_gdocs": "true"} // Google Docs không tải dạng file
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ListRemote lists the top-level entries under remotePath (e.g.
// "gdrive:better-drive-e2e") via rc operations/list and returns their names.
// remotePath is passed as-is for the "fs" param (rclone builds the fs.Fs from
// the "remote:path" string directly), with "remote" left empty so the
// listing is rooted at remotePath itself.
func (e *Engine) ListRemote(remotePath string) ([]string, error) {
	res, err := e.call("operations/list", map[string]any{
		"fs":     remotePath,
		"remote": "",
	})
	if err != nil {
		return nil, err
	}
	items, _ := res["list"].([]any)
	names := make([]string, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["Name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

type BisyncParams struct {
	Path1, Path2, Workdir string
	Resync                bool
	Filters               []string
}

type BisyncResult struct{ Output string }

func (e *Engine) Bisync(p BisyncParams) (BisyncResult, error) {
	filtersFile := filepath.Join(p.Workdir, "filters.txt")
	if err := os.MkdirAll(p.Workdir, 0o755); err != nil {
		return BisyncResult{}, err
	}
	if err := os.WriteFile(filtersFile, []byte(strings.Join(p.Filters, "\n")+"\n"), 0o600); err != nil {
		return BisyncResult{}, err
	}
	params := map[string]any{
		"path1":              p.Path1,
		"path2":              p.Path2,
		"workdir":            p.Workdir,
		"filtersFile":        filtersFile,
		"resync":             p.Resync,
		"resilient":          true,
		"createEmptySrcDirs": true,
		"conflictResolve":    "newer",
		"conflictLoser":      "num",
	}
	res, err := e.call("sync/bisync", params)
	out, _ := json.Marshal(res)
	return BisyncResult{Output: string(out)}, err
}
