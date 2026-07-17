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
