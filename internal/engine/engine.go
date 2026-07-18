package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/rclone/rclone/backend/drive"    // register the Drive backend (path2)
	_ "github.com/rclone/rclone/backend/local"     // register the local filesystem backend (path1)
	_ "github.com/rclone/rclone/cmd/bisync"        // register the sync/bisync rc method (its init calls addRC)
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

// CreateDriveRemote creates a Drive remote via rc config/create. skip_gdocs is
// NOT set here: it is applied at runtime through a connection string on the
// bisync path (see withSkipGdocs). config/create cannot persist a stored
// skip_gdocs anyway - the drive backend's OAuth state-machine rebuilds the
// stored config from its interactive answers and drops extra "parameters"
// (verified: after setup, `rclone config dump` showed only scope/team_drive/
// token/type), and rc config/update pauses on the token-refresh question
// without saving. The runtime connection string sidesteps both.
func (e *Engine) CreateDriveRemote(name string, params map[string]string) error {
	if params == nil {
		params = map[string]string{}
	}
	p := map[string]any{
		"name":       name,
		"type":       "drive",
		"parameters": params,
	}
	_, err := e.call("config/create", p)
	return err
}

// withSkipGdocs adds the skip_gdocs backend option to a remote path via an
// rclone connection string, e.g. "gdrive:Backup" -> "gdrive,skip_gdocs=true:Backup".
// Google Docs cannot be downloaded as files, so bisync must skip them or it
// aborts. Applied at runtime (not stored in config) because config/create
// drops the param during OAuth and config/update pauses without saving on an
// already-token'd remote. Verified working: `rclone lsf "gdrive,skip_gdocs=true:..."`.
// A local path (no remote before the ":") or a bare path is returned unchanged.
func withSkipGdocs(remotePath string) string {
	name, path, found := strings.Cut(remotePath, ":")
	if !found {
		return remotePath // local path / no remote to annotate
	}
	return name + ",skip_gdocs=true:" + path
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

// ensureRemoteDir creates a remote path (e.g. "gdrive:Backup") if it does not
// exist yet. rclone bisync --resync aborts when path2's root is missing, so the
// first run must create it. mkdir is idempotent, so an existing dir is a no-op.
func (e *Engine) ensureRemoteDir(path string) error {
	fsName, remote, found := strings.Cut(path, ":")
	if !found || remote == "" {
		return nil // not a remote path, or the remote root (always exists)
	}
	_, err := e.call("operations/mkdir", map[string]any{"fs": fsName + ":", "remote": remote})
	return err
}

func (e *Engine) Bisync(p BisyncParams) (BisyncResult, error) {
	filtersFile := filepath.Join(p.Workdir, "filters.txt")
	if err := os.MkdirAll(p.Workdir, 0o755); err != nil {
		return BisyncResult{}, err
	}
	// First run (resync): ensure both sides exist. path1 is always a local folder
	// for better-drive; path2 is the Drive remote.
	if p.Resync {
		if err := os.MkdirAll(p.Path1, 0o755); err != nil {
			return BisyncResult{}, err
		}
		if err := e.ensureRemoteDir(p.Path2); err != nil {
			return BisyncResult{}, err
		}
	}
	if err := os.WriteFile(filtersFile, []byte(strings.Join(p.Filters, "\n")+"\n"), 0o600); err != nil {
		return BisyncResult{}, err
	}
	params := map[string]any{
		"path1":              p.Path1,
		"path2":              withSkipGdocs(p.Path2), // skip Google Docs on the Drive side (can't be downloaded)
		"workdir":            p.Workdir,
		"filtersFile":        filtersFile,
		"resync":             p.Resync,
		"resilient":          true,
		"recover":            true,
		"maxDelete":          50, // percent; rc omits the CLI's 50% default, and 0 aborts on ANY delete (breaks 2-way delete propagation)
		"createEmptySrcDirs": true,
		"conflictResolve":    "newer",
		"conflictLoser":      "num",
		"compare":            "size,modtime,checksum",
	}
	res, err := e.call("sync/bisync", params)
	out, _ := json.Marshal(res)
	return BisyncResult{Output: string(out)}, err
}

// CopyParams configures a 1-way sync/copy or sync/sync call. Unlike
// BisyncParams there is no Resync/baseline concept: sync/copy and sync/sync
// are stateless per rc call (rc.Call registration in rclone's fs/sync/rc.go
// takes only srcFs/dstFs/createEmptySrcDirs - no filtersFile, no listings) and
// sync/copy auto-creates the destination directory, so no ensureRemoteDir
// call is needed either (verified empirically - see engine's package doc /
// the throwaway check run during implementation).
type CopyParams struct {
	Local, Remote, Workdir string
	Filters                []string
}

// filterRC builds the rc "_filter" object using the RulesOpt.FilterRule field
// (JSON field name, not the "filter" config-tag name - rc.Params.GetStruct /
// job.go's getFilter Reshape the object via encoding/json, which uses Go
// struct field names since filter.Options/RulesOpt carry no `json:` tags).
// FilterRule expects the SAME "+ glob" / "- glob" prefixed rule syntax as a
// bisync filters file (first-match-wins across the list), which is exactly
// what config.TranslateDriveIgnore already produces - so Filters here reuses
// those strings unchanged. Returns nil (omit "_filter" entirely) when there
// are no filters, since an empty FilterRule list is harmless but unnecessary.
func filterRC(filters []string) map[string]any {
	if len(filters) == 0 {
		return nil
	}
	return map[string]any{"FilterRule": filters}
}

// Copy performs a 1-way backup copy: files are copied from Local to Remote,
// but nothing already on Remote is ever deleted (rc sync/copy - verified
// empirically that a pre-existing extra file on dst survives a copy run).
// Workdir is accepted for interface parity with Bisync/Sync but unused: copy
// keeps no baseline/listings on disk.
func (e *Engine) Copy(p CopyParams) error {
	params := map[string]any{
		"srcFs":              p.Local,
		"dstFs":              withSkipGdocs(p.Remote),
		"createEmptySrcDirs": true,
	}
	if f := filterRC(p.Filters); f != nil {
		params["_filter"] = f
	}
	_, err := e.call("sync/copy", params)
	return err
}

// Sync performs a 1-way mirror: Remote is made to exactly match Local,
// including deleting anything on Remote that is not present on Local (rc
// sync/sync - verified empirically that a dst-only file is removed).
func (e *Engine) Sync(p CopyParams) error {
	params := map[string]any{
		"srcFs":              p.Local,
		"dstFs":              withSkipGdocs(p.Remote),
		"createEmptySrcDirs": true,
	}
	if f := filterRC(p.Filters); f != nil {
		params["_filter"] = f
	}
	_, err := e.call("sync/sync", params)
	return err
}
