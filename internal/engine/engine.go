package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/rclone/rclone/backend/drive" // register the Drive backend (path2)
	_ "github.com/rclone/rclone/backend/local"  // register the local filesystem backend (path1)
	_ "github.com/rclone/rclone/cmd/bisync"     // register the sync/bisync rc method (its init calls addRC)
)

var ErrNeedsResync = errors.New("bisync needs --resync (baseline lost)")

// retryBackoffUnit is the base backoff between high-level retries in
// callWithRetry (attempt N sleeps N*unit). A package var so tests can zero it.
var retryBackoffUnit = 10 * time.Second

type Engine struct {
	rpc func(method, input string) (string, int)
	// bin is the resolved rclone binary path (from exec.LookPath, or the bare
	// "rclone" name when not found on PATH - the error surfaces on first use
	// instead of at New()). cfg is the rclone config file path to pass via
	// --config; empty means let rclone fall back to its own default discovery
	// (including honoring the RCLONE_CONFIG env var itself). run is the seam
	// tests inject a fake into; New wires it to the real execRunner(bin).
	bin string
	cfg string
	run runner
	// syncMu serializes the sync operations (Bisync/Copy/Sync). The previous
	// librclone rc engine applied its _filter (and other run options) to
	// PROCESS-GLOBAL state for the duration of a sync, so two concurrent syncs
	// with different filters raced: verified E2E that concurrent Copy calls
	// silently crossed their filters and one dest ended up empty with err=nil.
	// Each rclone subprocess is now independent, but the lock is kept as cheap
	// insurance and to guard the temp filter-file's lifetime (a second sync
	// must not remove the first one's still-in-use file).
	syncMu sync.Mutex
}

// New builds an Engine that shells out to the system rclone binary.
// rcloneConfigPath, when non-empty, is passed to every rclone invocation via
// --config (e.g. a scoop portable install's rclone.conf); an empty value is
// passed through as-is (no --config flag), letting rclone fall back to its
// own default config discovery, including the RCLONE_CONFIG env var, which
// the rclone CLI honors natively.
func New(rcloneConfigPath string) *Engine {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		bin = "rclone" // not found on PATH; the error surfaces on first use
	}
	return &Engine{bin: bin, cfg: rcloneConfigPath, run: execRunner(bin)}
}

// Close is a no-op: each rclone invocation is an independent subprocess with
// nothing to finalize (unlike the previous in-process librclone engine).
func (e *Engine) Close() {}

// args prepends --config <cfg> to base when the engine has a non-empty
// config path, so every rclone invocation goes through it.
func (e *Engine) args(base ...string) []string {
	if e.cfg == "" {
		return base
	}
	return append([]string{"--config", e.cfg}, base...)
}

// exec runs an rclone subcommand (argv, without --config) through the
// runner seam, applying args' --config prefixing.
func (e *Engine) exec(argv ...string) (stdout, stderr string, err error) {
	return e.run(e.args(argv...)...)
}

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

// callWithRetry wraps call with rclone-style high-level retries for a heavy
// transfer op (sync/copy, sync/sync, operations/copyfile). The rc methods run
// only rclone's low-level (per-request) retries, not the high-level retry loop
// the `rclone` CLI applies in cmd.Run - so an intermittent Drive
// "rateLimitExceeded" (the shared client_id's per-minute quota) that low-level
// retries can't outlast fails the whole pair, where `rclone copy` would re-run
// and succeed. Re-running is cheap: the copy is incremental, so a retry
// re-attempts only the files that failed, hitting the API far less and usually
// clearing the quota window. Only transient errors are retried; a config/auth
// error fails fast.
func (e *Engine) callWithRetry(method string, params map[string]any) (map[string]any, error) {
	const maxAttempts = 3
	var res map[string]any
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err = e.call(method, params)
		if err == nil || attempt == maxAttempts || !isRetryable(err) {
			return res, err
		}
		time.Sleep(time.Duration(attempt) * retryBackoffUnit) // 10s, then 20s
	}
	return res, err
}

// isRetryable reports whether err looks like a transient Drive/network failure
// worth re-running the whole operation for (rate limit, quota, Google 5xx,
// connection reset/timeout) rather than a fatal config/auth/permission error a
// retry cannot fix.
func isRetryable(err error) bool {
	m := strings.ToLower(err.Error())
	for _, marker := range []string{
		"ratelimitexceeded", "userratelimitexceeded", "quota exceeded",
		"too many requests", "backenderror", "internalerror", "serviceunavailable",
		"connection reset", "connection refused", "i/o timeout", "timeout",
		"unexpected eof", "tls handshake", "no such host",
	} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// RemoteExists reports whether name is a configured remote (any type), by
// parsing `rclone listremotes` output (one "name:" per line).
func (e *Engine) RemoteExists(name string) (bool, error) {
	stdout, stderr, err := e.exec("listremotes")
	if err != nil {
		return false, fmt.Errorf("rclone listremotes: %w: %s", err, strings.TrimSpace(stderr))
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.TrimSuffix(line, ":") == name {
			return true, nil
		}
	}
	return false, nil
}

// RemoteConfigured reports whether name is a remote with a valid OAuth token,
// as opposed to a broken, token-less stanza left behind by an interrupted
// config/create (see CreateDriveRemote doc). `rclone config show <name>` on a
// remote whose config/create hasn't finished OAuth yet returns the stanza
// without a "token" line at all; on a missing remote (or any other failure)
// it errors - both are treated the same as "not configured" rather than
// distinguished as a separate case.
func (e *Engine) RemoteConfigured(name string) (bool, error) {
	stdout, _, err := e.exec("config", "show", name)
	if err != nil {
		return false, nil
	}
	for _, line := range strings.Split(stdout, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if strings.TrimSpace(key) == "token" && strings.TrimSpace(value) != "" {
			return true, nil
		}
	}
	return false, nil
}

// DeleteRemote removes a remote's config stanza via `rclone config delete
// <name>` (used to clear a broken, token-less remote before recreating it).
func (e *Engine) DeleteRemote(name string) error {
	_, stderr, err := e.exec("config", "delete", name)
	if err != nil {
		return fmt.Errorf("rclone config delete: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// CreateDriveRemote creates a Drive remote via `rclone config create <name>
// drive [key=value ...]` (params sorted by key for a deterministic argv).
// skip_gdocs is NOT passed here: it is applied per-invocation through the
// global --drive-skip-gdocs flag (see commonSyncFlags) instead of a stored
// config value - the drive backend's OAuth state-machine rebuilds the stored
// config from its interactive answers and drops extra backend options
// (verified: after setup, `rclone config dump` showed only scope/team_drive/
// token/type), so a stored skip_gdocs would not survive OAuth anyway.
func (e *Engine) CreateDriveRemote(name string, params map[string]string) error {
	argv := []string{"config", "create", name, "drive"}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, k+"="+params[k])
	}
	_, stderr, err := e.exec(argv...)
	if err != nil {
		return fmt.Errorf("rclone config create: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
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

// perfConfig returns rc _config overrides that speed up large syncs (matching
// the tuning of the backup script better-drive replaces): fast-list (UseListR)
// lists a whole tree in far fewer API calls, and more transfers/checkers with a
// TPS cap keep large folders (e.g. ~/.claude) from taking many minutes. rc
// _config keys are the fs.ConfigInfo field names (case-insensitive).
func perfConfig() map[string]any {
	return map[string]any{
		"UseListR":  true, // --fast-list
		"Transfers": 8,
		"Checkers":  16,
		"TPSLimit":  10.0,
	}
}

// ListRemote lists the top-level entries under remotePath (e.g.
// "gdrive:better-drive-e2e") via `rclone lsf` and returns their names.
// `lsf`'s default format marks directories with a trailing "/", which is
// stripped so a directory entry's name matches a file entry's shape.
func (e *Engine) ListRemote(remotePath string) ([]string, error) {
	stdout, stderr, err := e.exec("lsf", remotePath)
	if err != nil {
		return nil, fmt.Errorf("rclone lsf: %w: %s", err, strings.TrimSpace(stderr))
	}
	lines := strings.Split(stdout, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, strings.TrimSuffix(line, "/"))
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
// exist yet via `rclone mkdir`. rclone bisync --resync aborts when path2's
// root is missing, so the first run must create it. mkdir is idempotent, so
// an existing dir is a no-op.
func (e *Engine) ensureRemoteDir(path string) error {
	_, remote, found := strings.Cut(path, ":")
	if !found || remote == "" {
		return nil // not a remote path, or the remote root (always exists)
	}
	_, stderr, err := e.exec("mkdir", path)
	if err != nil {
		return fmt.Errorf("rclone mkdir: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Bisync runs a 2-way sync via `rclone bisync path1 path2 --workdir workdir
// [--resync] ...`, keeping rclone's own baseline (*.lst listing files) under
// Workdir - the same location syncloop.baselineExists checks to decide
// whether a pair still needs its first --resync. On error, a stderr message
// telling the caller to (re-)run --resync is mapped to the ErrNeedsResync
// sentinel; any other error is wrapped with rclone's stderr for diagnostics.
func (e *Engine) Bisync(p BisyncParams) (BisyncResult, error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
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
	filterArgv, cleanup, err := writeFilters("--filters-file", p.Filters)
	if err != nil {
		return BisyncResult{}, err
	}
	defer cleanup()

	argv := []string{"bisync", p.Path1, p.Path2, "--workdir", p.Workdir}
	if p.Resync {
		argv = append(argv, "--resync")
	}
	argv = append(argv, commonSyncFlags()...)
	argv = append(argv,
		"--resilient", "--recover",
		"--max-delete", "50", // percent; 0 aborts on ANY delete (breaks 2-way delete propagation)
		"--conflict-resolve", "newer", "--conflict-loser", "num",
		"--compare", "size,modtime,checksum",
	)
	argv = append(argv, filterArgv...)

	_, stderr, err := e.exec(argv...)
	if err != nil {
		if strings.Contains(strings.ToLower(stderr), "must run --resync") {
			return BisyncResult{}, ErrNeedsResync
		}
		return BisyncResult{}, fmt.Errorf("rclone bisync: %w: %s", err, strings.TrimSpace(stderr))
	}
	return BisyncResult{}, nil
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

// isFileLocal reports whether local is an existing regular file (not a
// directory). A pair whose Local is a single file (e.g. ~/.claude.json,
// alongside the usual directory pairs) needs file-to-file copy semantics
// instead of directory `rclone copy`/`rclone sync`. A local that does not
// exist, or that stat fails on, is treated as a directory path (the
// pre-existing behavior) and left to rclone's own error reporting.
func isFileLocal(local string) bool {
	info, err := os.Stat(local)
	return err == nil && !info.IsDir()
}

// commonSyncFlags are the flags shared by copy/sync/bisync invocations:
// --fast-list plus --transfers/--checkers/--tpslimit (the old rc _config
// UseListR/Transfers/Checkers/TPSLimit tuning), --retries (rclone's own
// high-level retry loop, replacing the old callWithRetry wrapper),
// --local-no-check-updated (RCLONE_LOCAL_NO_CHECK_UPDATED env - a file still
// being appended to, e.g. ~/.claude/**/instinct.log, transfers at the size
// first seen instead of aborting), --drive-skip-gdocs (Google Docs cannot be
// downloaded as files, so any Drive side must skip them - replacing the old
// withSkipGdocs connection-string trick), and --create-empty-src-dirs.
func commonSyncFlags() []string {
	return []string{
		"--fast-list",
		"--transfers", "8",
		"--checkers", "16",
		"--tpslimit", "10",
		"--retries", "3",
		"--local-no-check-updated",
		"--drive-skip-gdocs",
		"--create-empty-src-dirs",
	}
}

// writeFilters writes filters (if any) to a temp file and returns the argv
// flag+path to append (e.g. ["--filter-from", path] for copy/sync, or
// ["--filters-file", path] for bisync) plus a cleanup func that removes the
// temp file - always safe to call, even when no file was created (len(filters)
// == 0 returns a nil argv and a no-op cleanup).
func writeFilters(flag string, filters []string) (argv []string, cleanup func(), err error) {
	if len(filters) == 0 {
		return nil, func() {}, nil
	}
	f, err := os.CreateTemp("", "better-drive-filter-*.txt")
	if err != nil {
		return nil, func() {}, err
	}
	path := f.Name()
	cleanup = func() { os.Remove(path) }
	if _, err := f.WriteString(strings.Join(filters, "\n") + "\n"); err != nil {
		f.Close()
		cleanup()
		return nil, func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return []string{flag, path}, cleanup, nil
}

// joinRemotePath joins a remote directory (e.g. "gdrive:Backups/claude") and a
// file's base name into the single path `rclone copyto` expects as its
// destination, e.g. "gdrive:Backups/claude/claude.json". Always uses "/" -
// remote paths (including Drive) use forward slashes regardless of host OS.
func joinRemotePath(dir, name string) string {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// copyLocalFile copies a single local file to a remote directory via `rclone
// copyto <local> <remoteDir>/<base>`. Filters are not applied: there is
// nothing else under a single source file to include/exclude.
func (e *Engine) copyLocalFile(local, remoteDir string) error {
	dst := joinRemotePath(remoteDir, filepath.Base(local))
	_, stderr, err := e.exec("copyto", local, dst,
		"--retries", "3", "--local-no-check-updated", "--drive-skip-gdocs")
	if err != nil {
		return fmt.Errorf("rclone copyto: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Copy performs a 1-way backup copy: files are copied from Local to Remote,
// but nothing already on Remote is ever deleted (`rclone copy`). Workdir is
// accepted for interface parity with Bisync/Sync but unused: copy keeps no
// baseline/listings on disk. When Local is a single file (not a directory),
// it is copied file-to-file via `rclone copyto` instead (see copyLocalFile) -
// e.g. for a pair backing up ~/.claude.json.
func (e *Engine) Copy(p CopyParams) error { return e.copyOrSync("copy", p) }

// Sync performs a 1-way mirror: Remote is made to exactly match Local,
// including deleting anything on Remote that is not present on Local (`rclone
// sync`). When Local is a single file, it is copied file-to-file via `rclone
// copyto` instead (see Copy's file-local handling) - there is no "extra
// content" on a single destination file to mirror away, so the copy/sync
// distinction collapses to the same operation for a file-local pair.
func (e *Engine) Sync(p CopyParams) error { return e.copyOrSync("sync", p) }

// copyOrSync implements Copy and Sync: both differ only in the rclone
// subcommand (copy vs sync), otherwise sharing the same argv shape, filter
// handling, and file-local dispatch.
func (e *Engine) copyOrSync(subcommand string, p CopyParams) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	if isFileLocal(p.Local) {
		return e.copyLocalFile(p.Local, p.Remote)
	}
	filterArgv, cleanup, err := writeFilters("--filter-from", p.Filters)
	if err != nil {
		return err
	}
	defer cleanup()
	argv := append([]string{subcommand, p.Local, p.Remote}, commonSyncFlags()...)
	argv = append(argv, filterArgv...)
	_, stderr, err := e.exec(argv...)
	if err != nil {
		return fmt.Errorf("rclone %s: %w: %s", subcommand, err, strings.TrimSpace(stderr))
	}
	return nil
}
