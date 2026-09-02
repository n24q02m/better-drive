package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var ErrNeedsResync = errors.New("bisync needs --resync (baseline lost)")

type Engine struct {
	// bin and cfg are only used by the explicit foreground compatibility
	// constructor below. Scheduled sync/account operations require an enrolled
	// absolute executable/config identity and an allowlisted child environment.
	bin    string
	cfg    string
	run    runner
	stream streamRunner
	// syncMu serializes sync operations. The lock protects invocation-scoped
	// filter lifetimes and content-addressed bisync workdir state from overlap.
	syncMu sync.Mutex

	workingConfigDir string
	closeOnce        sync.Once
	closeErr         error
}

// NewForeground builds an Engine for the separate foreground mount path.
// Unlike NewVerified, it may use rclone's foreground discovery semantics;
// scheduled transfer code must never call it.
func NewForeground(rcloneConfigPath string) *Engine {
	bin := "rclone"
	if resolved, err := exec.LookPath(bin); err == nil {
		bin = resolveRcloneExecutable(resolved)
	}
	return &Engine{
		bin:    bin,
		cfg:    rcloneConfigPath,
		run:    execRunner(bin),
		stream: execStreamRunner(bin),
	}
}

// Close removes any private working rclone config created for a transfer
// engine. Foreground and direct verified engines have nothing to finalize.
func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		if e.workingConfigDir != "" {
			e.closeErr = os.RemoveAll(e.workingConfigDir)
		}
	})
	return e.closeErr
}

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

// streamExec runs a long-lived rclone subcommand (argv, without --config)
// through the streaming runner seam, applying args' --config prefixing.
func (e *Engine) streamExec(ctx context.Context, stdout, stderr io.Writer, argv ...string) error {
	return e.stream(ctx, stdout, stderr, e.args(argv...)...)
}

// RemoteExists reports whether name is a configured remote (any type), by
// parsing `rclone listremotes` output (one "name:" per line).
func (e *Engine) RemoteExists(name string) (bool, error) {
	stdout, stderr, err := e.exec("listremotes")
	if err != nil {
		return false, fmt.Errorf("rclone listremotes: %w: %s", err, strings.TrimSpace(stderr))
	}
	// Cut walks the output one line at a time instead of splitting it into a
	// []string up front, so a single pass never materializes lines it won't
	// need beyond the first match.
	for stdout != "" {
		var line string
		line, stdout, _ = strings.Cut(stdout, "\n")
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

// RemoteConfigured reports whether name is a Drive remote with usable
// credentials: either a non-empty OAuth token or a service_account_file.
// A stanza left behind by interrupted OAuth has neither credential; a missing
// remote (or any other config-show failure) errors. Both are treated as "not
// configured" rather than distinguished as separate cases.
func (e *Engine) RemoteConfigured(name string) (bool, error) {
	stdout, _, err := e.exec("config", "show", name)
	if err != nil {
		return false, nil
	}
	isDrive := false
	hasCredential := false
	for stdout != "" {
		var line string
		line, stdout, _ = strings.Cut(stdout, "\n")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "type":
			isDrive = value == "drive"
		case "token", "service_account_file":
			hasCredential = hasCredential || value != ""
		}
	}
	return isDrive && hasCredential, nil
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

// ListRemote lists the top-level entries under remotePath (e.g.
// "gdrive:better-drive-e2e") via `rclone lsf` and returns their names.
// `lsf`'s default format marks directories with a trailing "/", which is
// stripped so a directory entry's name matches a file entry's shape.
func (e *Engine) ListRemote(remotePath string) ([]string, error) {
	stdout, stderr, err := e.exec("lsf", remotePath)
	if err != nil {
		return nil, fmt.Errorf("rclone lsf: %w: %s", err, strings.TrimSpace(stderr))
	}
	names := make([]string, 0, strings.Count(stdout, "\n"))
	for stdout != "" {
		var line string
		line, stdout, _ = strings.Cut(stdout, "\n")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, strings.TrimSuffix(line, "/"))
	}
	return names, nil
}

// ListDriveRemotes returns the names of the Drive remotes in the rclone
// config, in the order rclone reported them. It parses `rclone listremotes
// --json` rather than the bare listing RemoteExists reads, because the bare
// form carries only names: a user's config can hold s3, dropbox or sftp
// remotes too, and an account command that offered those as Google Drive
// accounts would be lying about what it can sync.
func (e *Engine) ListDriveRemotes() ([]string, error) {
	stdout, stderr, err := e.exec("listremotes", "--json")
	if err != nil {
		return nil, fmt.Errorf("rclone listremotes: %w: %s", err, strings.TrimSpace(stderr))
	}
	var remotes []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	// A body that does not parse is reported as a failure rather than as an
	// empty list: "no Drive remote is configured" is a claim a caller acts on
	// (it prints a first-run hint), and it must not be reachable by reading
	// output rclone never produced.
	if err := json.Unmarshal([]byte(stdout), &remotes); err != nil {
		return nil, fmt.Errorf("rclone listremotes: parse json: %w", err)
	}
	names := make([]string, 0, len(remotes))
	for _, r := range remotes {
		if r.Type == "drive" {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// Quota is a remote's storage usage in bytes, as reported by `rclone about
// --json`. rclone also breaks out "trashed" and "other"; both are dropped
// because the only question a sync tool needs answered is how much room is
// left before the next cycle starts failing.
type Quota struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

// MountParams configures a foreground `rclone mount` invocation. Stdout and
// Stderr receive rclone's live process output; nil writers are discarded.
type MountParams struct {
	Remote, Mountpoint string
	ReadOnly           bool
	Stdout, Stderr     io.Writer
}

const stderrTailLimit = 64 * 1024

// stderrTailWriter forwards stderr live while retaining only its bounded tail
// for the error returned by a streaming operation. Capture happens before
// forwarding so remediation evidence survives even when the caller's terminal
// writer fails.
type stderrTailWriter struct {
	live io.Writer
	tail []byte
}

func newStderrTailWriter(live io.Writer) *stderrTailWriter {
	return &stderrTailWriter{
		live: live,
		tail: make([]byte, 0, stderrTailLimit),
	}
}

func (w *stderrTailWriter) Write(p []byte) (int, error) {
	w.retain(p)
	return w.live.Write(p)
}

func (w *stderrTailWriter) retain(p []byte) {
	if len(p) >= stderrTailLimit {
		w.tail = w.tail[:stderrTailLimit]
		copy(w.tail, p[len(p)-stderrTailLimit:])
		return
	}
	if overflow := len(w.tail) + len(p) - stderrTailLimit; overflow > 0 {
		copy(w.tail, w.tail[overflow:])
		w.tail = w.tail[:len(w.tail)-overflow]
	}
	w.tail = append(w.tail, p...)
}

func (w *stderrTailWriter) String() string { return string(w.tail) }

// execCaptureWithContext runs a bounded command while preserving both stdout
// and the diagnostic tail of stderr. Callers that need machine-readable output
// use this instead of routing stdout to the live progress stream.
func (e *Engine) execCaptureWithContext(ctx context.Context, stderrOut io.Writer, argv ...string) (string, string, error) {
	if ctx == nil && stderrOut == nil {
		return e.exec(argv...)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stderrOut == nil {
		stderrOut = io.Discard
	}
	var stdout bytes.Buffer
	stderr := newStderrTailWriter(stderrOut)
	err := e.streamExec(ctx, &stdout, stderr, argv...)
	return stdout.String(), stderr.String(), err
}

// execWithContext preserves the captured runner for short-lived daemon calls,
// but switches to the context-aware runner when a one-shot caller supplies a
// context or live stderr writer. Streaming stderr remains bounded in memory so
// a returned error retains useful rclone diagnostics.
func (e *Engine) execWithContext(ctx context.Context, stderrOut io.Writer, argv ...string) (string, error) {
	if ctx == nil && stderrOut == nil {
		_, stderr, err := e.exec(argv...)
		return stderr, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stderrOut == nil {
		stderrOut = io.Discard
	}
	stderr := newStderrTailWriter(stderrOut)
	err := e.streamExec(ctx, io.Discard, stderr, argv...)
	return stderr.String(), err
}

func appendProgressFlags(argv []string, stderr io.Writer) []string {
	if stderr == nil {
		return argv
	}
	return append(argv, "--stats", "5s", "--stats-one-line")
}

// About reports a remote's storage quota via `rclone about <name>: --json`.
// Quota is the only account-level fact obtainable for a Drive remote: the
// Drive backend does not implement `config userinfo`: it reports that the
// Drive root with an empty path does not support UserInfo, so rclone cannot
// tell us which Google account a remote is signed in as; the numbers here are
// what a user has to tell two configured accounts apart by.
func (e *Engine) About(name string) (Quota, error) {
	stdout, stderr, err := e.exec("about", name+":", "--json")
	if err != nil {
		return Quota{}, fmt.Errorf("rclone about: %w: %s", err, strings.TrimSpace(stderr))
	}
	var q Quota
	// As in ListDriveRemotes, an unparseable body must not be mistaken for
	// success: a zero-valued Quota renders as a legitimate-looking "0 B of
	// 0 B", which reads as an empty account rather than as a broken read.
	if err := json.Unmarshal([]byte(stdout), &q); err != nil {
		return Quota{}, fmt.Errorf("rclone about: parse json: %w", err)
	}
	return q, nil
}

// Mount runs `rclone mount` in the foreground with the compatibility-safe
// `--vfs-cache-mode full` default, optionally adding `--read-only`. Unlike
// sync operations it intentionally does not hold syncMu: a long-lived mount
// must not block independent copy/sync/bisync cycles. stderr is streamed to
// the caller live and its last 64 KiB are retained in the returned error for
// remediation.
func (e *Engine) Mount(ctx context.Context, p MountParams) error {
	stdout := p.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderrOut := p.Stderr
	if stderrOut == nil {
		stderrOut = io.Discard
	}

	argv := []string{
		"mount",
		p.Remote,
		p.Mountpoint,
		"--vfs-cache-mode", "full",
	}
	if p.ReadOnly {
		argv = append(argv, "--read-only")
	}

	stderr := newStderrTailWriter(stderrOut)
	if err := e.streamExec(ctx, stdout, stderr, argv...); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("rclone mount: %w", err)
		}
		return fmt.Errorf("rclone mount: %w: %s", err, msg)
	}
	return nil
}

// CountSourceObjects returns the number of files rclone would actually
// transfer after applying the same filter rules as copy, sync, or bisync.
// Destructive-mode safety must use this effective count rather than a raw
// filesystem walk, because an excluded-only source is empty from rclone's
// perspective.
func (e *Engine) CountSourceObjects(ctx context.Context, local string, filters []string, stderrOut io.Writer) (int64, error) {
	info, err := os.Stat(local)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inspect source %q: %w", local, err)
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() {
			return 1, nil
		}
		return 0, nil
	}

	filterArgv, cleanup, err := writeFilters("--filter-from", filters)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	argv := []string{"size", local, "--json", "--fast-list"}
	argv = append(argv, filterArgv...)
	stdout, stderr, err := e.execCaptureWithContext(ctx, stderrOut, argv...)
	if err != nil {
		return 0, fmt.Errorf("rclone size: %w: %s", err, strings.TrimSpace(stderr))
	}
	var result struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		return 0, fmt.Errorf("rclone size: parse json: %w", err)
	}
	if result.Count < 0 {
		return 0, fmt.Errorf("rclone size: invalid negative object count %d", result.Count)
	}
	return result.Count, nil
}

type BisyncParams struct {
	Path1, Path2, Workdir string
	Resync                bool
	// Optional state evidence enables safety guards without making the legacy
	// call shape infer destructive history from an empty default.
	SourceWasNonEmpty *bool
	SourceObjectCount *int64
	DeleteBudget      *DeleteBudget
	// DryRun previews the cycle (including its delete propagation) via
	// rclone's own --dry-run, applying no change.
	DryRun  bool
	Filters []string
	// Context and Stderr opt a one-shot caller into cancellable execution with
	// live rclone progress. Their zero values retain the daemon's captured path.
	Context context.Context
	Stderr  io.Writer
}

type BisyncResult struct{ Output string }

// ensureRemoteDir creates a remote path (e.g. "gdrive:Backup") if it does not
// exist yet via `rclone mkdir`. rclone bisync --resync aborts when path2's
// root is missing, so the first run must create it. mkdir is idempotent, so
// an existing dir is a no-op.
func (e *Engine) ensureRemoteDir(ctx context.Context, stderrOut io.Writer, path string) error {
	_, remote, found := strings.Cut(path, ":")
	if !found || remote == "" {
		return nil // not a remote path, or the remote root (always exists)
	}
	stderr, err := e.execWithContext(ctx, stderrOut, "mkdir", path)
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
	if err := validateTransferSafety("bisync", p.SourceWasNonEmpty, p.SourceObjectCount, p.DeleteBudget); err != nil {
		return BisyncResult{}, err
	}
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	if !p.DryRun {
		if err := os.MkdirAll(p.Workdir, 0o700); err != nil {
			return BisyncResult{}, err
		}
	}
	// First run (resync): ensure both sides exist. path1 is always a local folder
	// for better-drive; path2 is the Drive remote. Both are real writes (a local
	// mkdir and a remote `rclone mkdir`), so DryRun skips them entirely - "no
	// changes will be made" must hold even on the very first (resync) cycle;
	// rclone's own --dry-run then previews (or reports) against whatever
	// already exists, rather than this step silently creating the destination
	// first.
	if p.Resync && !p.DryRun {
		if err := os.MkdirAll(p.Path1, 0o700); err != nil {
			return BisyncResult{}, err
		}
		if err := e.ensureRemoteDir(p.Context, p.Stderr, p.Path2); err != nil {
			return BisyncResult{}, err
		}
	}
	filterArgv, cleanup, err := writeBisyncFilters(p.Workdir, p.Filters, p.DryRun)
	if err != nil {
		return BisyncResult{}, err
	}
	defer cleanup()

	argv := []string{"bisync", p.Path1, p.Path2, "--workdir", p.Workdir}
	if p.Resync {
		argv = append(argv, "--resync")
	}
	if p.DryRun {
		argv = append(argv, "--dry-run")
	}
	argv = append(argv, commonSyncFlags()...)
	argv = append(argv,
		"--resilient", "--recover",
		"--max-delete", "50", // percent; 0 aborts on ANY delete (breaks 2-way delete propagation)
		"--conflict-resolve", "newer", "--conflict-loser", "num",
		"--compare", "size,modtime,checksum",
	)
	argv = append(argv, filterArgv...)
	argv = appendProgressFlags(argv, p.Stderr)

	stderr, err := e.execWithContext(p.Context, p.Stderr, argv...)
	if err != nil {
		if needsResync(stderr) {
			return BisyncResult{}, ErrNeedsResync
		}
		return BisyncResult{}, fmt.Errorf("rclone bisync: %w: %s", err, strings.TrimSpace(stderr))
	}
	return BisyncResult{}, nil
}

// needsResync reports whether rclone's stderr describes an abort that only a
// --resync can clear. Two wordings mean the same thing, and both are matched
// because which one appears depends on a flag Bisync itself always passes:
//
//   - "must run --resync" is rclone's own instruction, printed when it aborts
//     WITHOUT --resilient.
//   - "cannot find prior path1 or path2 listings" is the critical error raised
//     when the baseline for this path1/path2 session is missing, and it is
//     printed in both modes. Under --resilient - which every Bisync call here
//     sets - rclone replaces the instruction above with "Bisync aborted. Error
//     is retryable without --resync due to --resilient mode.", so matching the
//     instruction alone would miss the lost-baseline case entirely.
//
// rclone calling that error "retryable" is not true for this particular
// failure: the prior listings do not exist, so a plain retry reproduces it
// byte for byte (verified end to end) until a --resync rebuilds them. Reporting
// it as ErrNeedsResync is what lets the caller say so instead of printing a
// raw rclone dump and repeating the identical failure on the next run.
func needsResync(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "must run --resync") ||
		strings.Contains(s, "cannot find prior path1 or path2 listings")
}

type CopyParams struct {
	Local, Remote, Workdir string
	// Optional state evidence enables safety guards before a destructive sync.
	SourceWasNonEmpty *bool
	SourceObjectCount *int64
	DeleteBudget      *DeleteBudget
	// DryRun previews the operation via rclone's own --dry-run, applying no
	// change - the mode this matters most for is "sync" (Sync below), which
	// deletes remote files absent locally.
	DryRun  bool
	Filters []string
	// Context and Stderr opt a one-shot caller into cancellable execution with
	// live rclone progress. Their zero values retain the daemon's captured path.
	Context context.Context
	Stderr  io.Writer
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

// writeFilters writes filters (if any) to an invocation-scoped temporary file.
// Copy and sync do not persist filter identity between calls, so their files
// can be removed immediately after rclone returns.
func writeFilters(flag string, filters []string) (argv []string, cleanup func(), err error) {
	if len(filters) == 0 {
		return nil, func() {}, nil
	}
	f, err := os.CreateTemp("", "better-drive-filter-*.txt")
	if err != nil {
		return nil, func() {}, err
	}
	path := f.Name()
	cleanup = func() {
		_ = os.Remove(path) // #nosec G104 -- xóa tệp bộ lọc tạm theo best-effort; callback không thể trả lỗi dọn dẹp.
	}

	content := filterContent(filters)

	if _, err := f.WriteString(content); err != nil {
		if closeErr := f.Close(); closeErr != nil {
			err = fmt.Errorf("write error: %w, close error: %v", err, closeErr)
		}
		cleanup()
		return nil, func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return []string{flag, path}, cleanup, nil
}

// writeBisyncFilters keeps a content-addressed filter path under Workdir.
// rclone records the absolute --filters-file path in its bisync baseline;
// rotating a generic temporary filename on each cycle makes the next call
// abort with "filters file md5 hash not found". A dry-run reuses an existing
// stable file but does not create one.
func writeBisyncFilters(workdir string, filters []string, dryRun bool) ([]string, func(), error) {
	if len(filters) == 0 {
		return nil, func() {}, nil
	}
	content := filterContent(filters)
	contentBytes := []byte(content)
	sum := sha256.Sum256(contentBytes)
	path := filepath.Join(workdir, ".better-drive-bisync-filters-"+hex.EncodeToString(sum[:])+".txt")
	data, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(data, contentBytes) {
			return nil, func() {}, errors.New("bisync filter file content does not match its digest")
		}
		return []string{"--filters-file", path}, func() {}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, func() {}, fmt.Errorf("read bisync filter file: %w", err)
	}
	if dryRun {
		return writeFilters("--filters-file", filters)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			data, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(data, contentBytes) {
				return []string{"--filters-file", path}, func() {}, nil
			}
		}
		return nil, func() {}, fmt.Errorf("create bisync filter file: %w", err)
	}
	if _, err := file.Write(contentBytes); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, func() {}, fmt.Errorf("write bisync filter file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, func() {}, fmt.Errorf("close bisync filter file: %w", err)
	}
	return []string{"--filters-file", path}, func() {}, nil
}

func filterContent(filters []string) string {
	capacity := len(filters)
	for _, filter := range filters {
		capacity += len(filter)
	}
	var content strings.Builder
	content.Grow(capacity)
	for _, filter := range filters {
		content.WriteString(filter)
		content.WriteByte('\n')
	}
	return content.String()
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
func (e *Engine) copyLocalFile(p CopyParams) error {
	dst := joinRemotePath(p.Remote, filepath.Base(p.Local))
	argv := []string{"copyto", p.Local, dst, "--retries", "3", "--local-no-check-updated", "--drive-skip-gdocs"}
	if p.DryRun {
		argv = append(argv, "--dry-run")
	}
	argv = appendProgressFlags(argv, p.Stderr)
	stderr, err := e.execWithContext(p.Context, p.Stderr, argv...)
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
	if subcommand == "sync" {
		if err := validateTransferSafety("sync", p.SourceWasNonEmpty, p.SourceObjectCount, p.DeleteBudget); err != nil {
			return err
		}
	}
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	if isFileLocal(p.Local) {
		return e.copyLocalFile(p)
	}
	filterArgv, cleanup, err := writeFilters("--filter-from", p.Filters)
	if err != nil {
		return err
	}
	defer cleanup()
	argv := append([]string{subcommand, p.Local, p.Remote}, commonSyncFlags()...)
	if p.DryRun {
		argv = append(argv, "--dry-run")
	}
	argv = append(argv, filterArgv...)
	argv = appendProgressFlags(argv, p.Stderr)
	stderr, err := e.execWithContext(p.Context, p.Stderr, argv...)
	if err != nil {
		return fmt.Errorf("rclone %s: %w: %s", subcommand, err, strings.TrimSpace(stderr))
	}
	return nil
}
