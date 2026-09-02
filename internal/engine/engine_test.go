package engine

import (
	"context"
	"errors"
	"io"
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

// newFakeStreamingRunnerEngine builds an Engine with injectable captured and
// streaming runner seams, so tests can exercise long-lived mount behavior
// without a real rclone binary.
func newFakeStreamingRunnerEngine(cfg string, runFn runner, streamFn streamRunner) *Engine {
	if runFn == nil {
		runFn = func(args ...string) (string, string, error) { return "", "", nil }
	}
	if streamFn == nil {
		streamFn = func(ctx context.Context, stdout, stderr io.Writer, args ...string) error { return nil }
	}
	return &Engine{cfg: cfg, run: runFn, stream: streamFn}
}

type triggeredContext struct {
	context.Context
	err    error
	cancel context.CancelFunc
	once   sync.Once
}

const helperProcessCleanupGrace = time.Second

func newTriggeredContext(t *testing.T, err error) *triggeredContext {
	t.Helper()
	testDeadline, ok := t.Deadline()
	if !ok {
		t.Fatal("helper-process test requires go test's bounded timeout")
	}
	// Bound child cleanup by the test binary's deadline instead of guessing
	// how quickly Windows can schedule a new process under suite-wide load.
	guardDeadline := testDeadline.Add(-helperProcessCleanupGrace)
	if !time.Now().Before(guardDeadline) {
		t.Fatal("insufficient time remains to start helper process and clean it up")
	}

	guard, cancel := context.WithDeadline(context.Background(), guardDeadline)
	ctx := &triggeredContext{Context: guard, err: err, cancel: cancel}
	t.Cleanup(ctx.trigger)
	return ctx
}

func (c *triggeredContext) Err() error {
	if c.Context.Err() == context.Canceled {
		return c.err
	}
	return c.Context.Err()
}
func (c *triggeredContext) trigger() {
	c.once.Do(c.cancel)
}

type signalingWriter struct {
	dst      io.Writer
	signaled chan struct{}
	onSignal func()
	once     sync.Once
}

func (w *signalingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.once.Do(func() {
			close(w.signaled)
			if w.onSignal != nil {
				w.onSignal()
			}
		})
	}
	return n, err
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func waitForSignal(t *testing.T, ch <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}
}

// TestNewForegroundResolvesRunner verifies the explicit foreground mount
// constructor wires up working runner seams without requiring a real rclone
// binary on PATH for construction.
func TestNewForegroundResolvesRunner(t *testing.T) {
	e := NewForeground("")
	if e == nil {
		t.Fatal("NewForeground(\"\") returned nil")
	}
	if e.run == nil {
		t.Fatal("NewForeground(\"\").run is nil, want a resolved runner")
	}
	if e.bin == "" {
		t.Fatal("NewForeground(\"\").bin is empty, want a resolved rclone binary name/path")
	}
	if e.stream == nil {
		t.Fatal("NewForeground(\"\").stream is nil, want a resolved streaming runner")
	}
}

func TestExecStreamRunnerNormalizesContextCancellation(t *testing.T) {
	const helperEnv = "GO_WANT_EXEC_STREAM_RUNNER_HELPER"
	if os.Getenv(helperEnv) == "1" {
		// An empty select is reported as a runtime deadlock, so the helper can
		// exit with status 2 before its parent observes readiness. A blocking OS
		// pipe read remains alive until CommandContext terminates the process.
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			_, _ = io.WriteString(os.Stderr, "create helper pipe: "+err.Error()+"\n")
			os.Exit(2)
		}
		defer readEnd.Close()
		defer writeEnd.Close()

		if _, err := io.WriteString(os.Stdout, "helper ready\n"); err != nil {
			_, _ = io.WriteString(os.Stderr, "write helper readiness: "+err.Error()+"\n")
			os.Exit(2)
		}
		var blocked [1]byte
		if _, err := readEnd.Read(blocked[:]); err != nil {
			_, _ = io.WriteString(os.Stderr, "helper pipe read returned: "+err.Error()+"\n")
		} else {
			_, _ = io.WriteString(os.Stderr, "helper pipe became readable unexpectedly\n")
		}
		os.Exit(2)
	}

	t.Setenv(helperEnv, "1")
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			ctx := newTriggeredContext(t, want)
			ready := make(chan struct{})
			stdout := &signalingWriter{dst: io.Discard, signaled: ready, onSignal: ctx.trigger}
			var helperStderr strings.Builder

			err := execStreamRunner(os.Args[0])(
				ctx,
				stdout,
				&helperStderr,
				"-test.run=^TestExecStreamRunnerNormalizesContextCancellation$",
			)
			select {
			case <-ready:
			default:
				t.Fatalf("helper process exited before readiness; runner error = %v, stderr = %q", err, helperStderr.String())
			}
			if !errors.Is(err, want) {
				t.Fatalf("execStreamRunner() error = %v, want errors.Is(..., %v); helper stderr = %q", err, want, helperStderr.String())
			}
		})
	}
}

func TestMountBuildsStreamingArgs(t *testing.T) {
	t.Run("default cache mode and config prefix", func(t *testing.T) {
		var gotArgv []string
		e := newFakeStreamingRunnerEngine("X:/portable/rclone.conf", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
			gotArgv = append([]string(nil), args...)
			return nil
		})

		err := e.Mount(context.Background(), MountParams{
			Remote:     "gdrive:Projects",
			Mountpoint: "G:",
			Stdout:     io.Discard,
			Stderr:     io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}

		want := []string{
			"--config", "X:/portable/rclone.conf",
			"mount", "gdrive:Projects", "G:",
			"--vfs-cache-mode", "full",
		}
		if !reflect.DeepEqual(gotArgv, want) {
			t.Fatalf("argv = %#v, want %#v", gotArgv, want)
		}
	})

	t.Run("read only appends flag", func(t *testing.T) {
		var gotArgv []string
		e := newFakeStreamingRunnerEngine("", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
			gotArgv = append([]string(nil), args...)
			return nil
		})

		err := e.Mount(context.Background(), MountParams{
			Remote:     "gdrive:",
			Mountpoint: "M:",
			ReadOnly:   true,
			Stdout:     io.Discard,
			Stderr:     io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}

		want := []string{
			"mount", "gdrive:", "M:",
			"--vfs-cache-mode", "full",
			"--read-only",
		}
		if !reflect.DeepEqual(gotArgv, want) {
			t.Fatalf("argv = %#v, want %#v", gotArgv, want)
		}
	})
}

func TestMountStreamsOutputAndRetainsStderrInError(t *testing.T) {
	var stdoutBuf, stderrBuf strings.Builder
	stdoutSeen := make(chan struct{})
	stderrSeen := make(chan struct{})
	releaseRunner := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRunner) }) }
	t.Cleanup(release)

	boom := errors.New("exit status 1")
	e := newFakeStreamingRunnerEngine("", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		if _, err := io.WriteString(stdout, "mounted\n"); err != nil {
			return err
		}
		if _, err := io.WriteString(stderr, "driver missing\n"); err != nil {
			return err
		}
		<-releaseRunner
		return boom
	})

	done := make(chan error, 1)
	go func() {
		done <- e.Mount(context.Background(), MountParams{
			Remote:     "gdrive:",
			Mountpoint: "G:",
			Stdout:     &signalingWriter{dst: &stdoutBuf, signaled: stdoutSeen},
			Stderr:     &signalingWriter{dst: &stderrBuf, signaled: stderrSeen},
		})
	}()

	waitForSignal(t, stdoutSeen, "stdout did not reach its writer while the mount runner was active")
	waitForSignal(t, stderrSeen, "stderr did not reach its writer while the mount runner was active")
	select {
	case err := <-done:
		t.Fatalf("Mount() returned before the blocked runner was released: %v", err)
	default:
	}

	if got := stdoutBuf.String(); got != "mounted\n" {
		t.Fatalf("live stdout = %q, want %q", got, "mounted\n")
	}
	if got := stderrBuf.String(); got != "driver missing\n" {
		t.Fatalf("live stderr = %q, want %q", got, "driver missing\n")
	}

	release()
	err := <-done
	if err == nil {
		t.Fatal("Mount() error = nil, want streamed stderr folded into the returned error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Mount() error = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), "driver missing") {
		t.Fatalf("error = %v, want streamed stderr preserved for remediation", err)
	}
}

func TestMountRetainsBoundedStderrTail(t *testing.T) {
	const wantTailLimit = 64 * 1024
	const head = "HEAD-MUST-BE-TRUNCATED\n"
	const tail = "TAIL-MUST-BE-RETAINED"
	payload := head + strings.Repeat("x", wantTailLimit) + tail
	boom := errors.New("exit status 1")
	var liveStderr strings.Builder
	e := newFakeStreamingRunnerEngine("", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		for start := 0; start < len(payload); {
			end := start + 7919
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := io.WriteString(stderr, payload[start:end]); err != nil {
				return err
			}
			start = end
		}
		return boom
	})

	err := e.Mount(context.Background(), MountParams{
		Remote:     "gdrive:",
		Mountpoint: "G:",
		Stdout:     io.Discard,
		Stderr:     &liveStderr,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Mount() error = %v, want wrapped %v", err, boom)
	}
	if got := liveStderr.String(); got != payload {
		t.Fatalf("live stderr length = %d, want full %d-byte stream", len(got), len(payload))
	}

	errorPrefix := "rclone mount: " + boom.Error() + ": "
	evidence, ok := strings.CutPrefix(err.Error(), errorPrefix)
	if !ok {
		t.Fatalf("Mount() error = %q, want prefix %q", err, errorPrefix)
	}
	if len(evidence) > wantTailLimit {
		t.Fatalf("retained stderr length = %d, want at most %d", len(evidence), wantTailLimit)
	}
	wantEvidence := payload[len(payload)-wantTailLimit:]
	if evidence != wantEvidence {
		t.Fatalf("retained stderr is not the exact %d-byte tail", wantTailLimit)
	}
	if strings.Contains(evidence, head) {
		t.Fatalf("retained stderr still contains truncated head marker")
	}
	if !strings.Contains(evidence, tail) {
		t.Fatalf("retained stderr = %q, want tail marker %q", evidence, tail)
	}
}

func TestMountRetainsStderrWhenLiveWriterFails(t *testing.T) {
	writerErr := errors.New("terminal writer failed")
	const evidence = "WinFsp driver missing"
	e := newFakeStreamingRunnerEngine("", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		_, err := io.WriteString(stderr, evidence)
		return err
	})

	err := e.Mount(context.Background(), MountParams{
		Remote:     "gdrive:",
		Mountpoint: "G:",
		Stdout:     io.Discard,
		Stderr:     failingWriter{err: writerErr},
	})
	if !errors.Is(err, writerErr) {
		t.Fatalf("Mount() error = %v, want wrapped terminal writer error %v", err, writerErr)
	}
	if !strings.Contains(err.Error(), evidence) {
		t.Fatalf("Mount() error = %v, want retained stderr evidence %q", err, evidence)
	}
}

func TestMountPropagatesContextToStreamingRunner(t *testing.T) {
	started := make(chan context.Context, 1)
	e := newFakeStreamingRunnerEngine("", nil, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- e.Mount(ctx, MountParams{
			Remote:     "gdrive:",
			Mountpoint: "Z:",
			Stdout:     io.Discard,
			Stderr:     io.Discard,
		})
	}()

	gotCtx := <-started
	if gotCtx != ctx {
		t.Fatalf("runner context = %p, want %p", gotCtx, ctx)
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Mount() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Mount() did not return after context cancellation")
	}
}

func TestMountDoesNotSerializeWithSyncOps(t *testing.T) {
	mountStarted := make(chan struct{})
	copyEntered := make(chan struct{})
	releaseMount := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseMount) }) }
	t.Cleanup(release)
	e := newFakeStreamingRunnerEngine("", func(args ...string) (string, string, error) {
		close(copyEntered)
		return "", "", nil
	}, func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		close(mountStarted)
		<-releaseMount
		return nil
	})

	mountDone := make(chan error, 1)
	go func() {
		mountDone <- e.Mount(context.Background(), MountParams{
			Remote:     "gdrive:",
			Mountpoint: "X:",
			Stdout:     io.Discard,
			Stderr:     io.Discard,
		})
	}()

	waitForSignal(t, mountStarted, "mount runner did not enter")

	copyDone := make(chan error, 1)
	copyLocal := t.TempDir()
	go func() {
		copyDone <- e.Copy(CopyParams{Local: copyLocal, Remote: "gdrive:backup"})
	}()

	waitForSignal(t, copyEntered, "Copy runner did not enter while Mount was blocked; Mount may hold syncMu")
	select {
	case err := <-copyDone:
		if err != nil {
			t.Fatalf("Copy() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Copy() did not return after its runner entered")
	}

	release()

	select {
	case err := <-mountDone:
		if err != nil {
			t.Fatalf("Mount() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Mount() did not return after release")
	}
}

// TestSyncCommandsStreamProgressAndCancel protects the one-shot CLI path from
// looking hung while rclone scans a large remote. Every sync mode must use the
// context-aware streaming runner when the caller supplies a progress writer,
// expose rclone stats before the process exits, and stop on cancellation.
func TestSyncCommandsStreamProgressAndCancel(t *testing.T) {
	tests := []struct {
		name       string
		subcommand string
		fileLocal  bool
	}{
		{name: "copy directory", subcommand: "copy"},
		{name: "sync directory", subcommand: "sync"},
		{name: "copy single file", subcommand: "copyto", fileLocal: true},
		{name: "bisync", subcommand: "bisync"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			started := make(chan struct{})
			var gotContext context.Context
			var gotArgs []string
			var progress strings.Builder
			e := newFakeStreamingRunnerEngine("", nil,
				func(streamContext context.Context, _ io.Writer, stderr io.Writer, args ...string) error {
					gotContext = streamContext
					gotArgs = append([]string(nil), args...)
					if _, err := stderr.Write([]byte("Transferred: 1 / 2\n")); err != nil {
						return err
					}
					close(started)
					<-streamContext.Done()
					return streamContext.Err()
				})

			local := t.TempDir()
			workdir := t.TempDir()
			if tt.fileLocal {
				local = filepath.Join(local, ".claude.json")
				if err := os.WriteFile(local, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			done := make(chan error, 1)
			go func() {
				switch tt.subcommand {
				case "bisync":
					_, err := e.Bisync(BisyncParams{
						Path1: local, Path2: "gdrive:test", Workdir: workdir,
						DryRun: true, Context: ctx, Stderr: &progress,
					})
					done <- err
				default:
					operation := tt.subcommand
					if tt.fileLocal {
						operation = "copy"
					}
					done <- e.copyOrSync(operation, CopyParams{
						Local: local, Remote: "gdrive:test", DryRun: true,
						Context: ctx, Stderr: &progress,
					})
				}
			}()

			select {
			case <-started:
				if !strings.Contains(progress.String(), "Transferred: 1 / 2") {
					t.Fatalf("progress was not visible before process exit: %q", progress.String())
				}
			case err := <-done:
				t.Fatalf("sync returned before streaming runner started: %v", err)
			}

			cancel()
			err := <-done
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("sync error = %v, want context.Canceled", err)
			}

			if gotContext != ctx {
				t.Fatal("sync did not propagate the caller context to the streaming runner")
			}
			if len(gotArgs) == 0 || gotArgs[0] != tt.subcommand {
				t.Fatalf("args = %v, want subcommand %q", gotArgs, tt.subcommand)
			}
			for _, want := range []string{"--dry-run", "--stats", "5s", "--stats-one-line"} {
				if !containsArg(gotArgs, want) {
					t.Errorf("args = %v, missing progress arg %q", gotArgs, want)
				}
			}
		})
	}
}

// TestBisyncBuildsRcloneArgs verifies Bisync builds `rclone bisync <path1>
// <path2> --workdir <workdir> ...` with the shared perf/retry/skip_gdocs
// flags plus the bisync-specific ones (--resilient, --recover, --max-delete
// 50, --conflict-resolve newer, --conflict-loser num, --compare
// size,modtime,checksum), and a stable --filters-file whose content is the
// joined filter lines.
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
			// Read the --filters-file during the fake rclone call.
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
	if _, err := os.Stat(filterPath); err != nil {
		t.Errorf("--filters-file %q must survive for the next bisync cycle: %v", filterPath, err)
	}
	if filepath.Dir(filterPath) != workdir {
		t.Errorf("--filters-file directory = %q, want workdir %q", filepath.Dir(filterPath), workdir)
	}
}

func TestBisyncReusesStableFiltersFileAcrossCycles(t *testing.T) {
	workdir := t.TempDir()
	var filterPaths []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		if idx := indexOf(args, "--filters-file"); idx != -1 && idx+1 < len(args) {
			filterPaths = append(filterPaths, args[idx+1])
		}
		return "", "", nil
	})
	params := BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:x", Workdir: workdir,
		Resync: true, Filters: []string{"- **/*.tmp"},
	}
	if _, err := e.Bisync(params); err != nil {
		t.Fatal(err)
	}
	params.Resync = false
	if _, err := e.Bisync(params); err != nil {
		t.Fatal(err)
	}
	if len(filterPaths) != 2 || filterPaths[0] != filterPaths[1] {
		t.Fatalf("bisync filter paths = %v, want one stable path", filterPaths)
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

// TestBisyncNeedsResyncUnderResilientMode verifies the lost-baseline abort is
// recognised even though --resilient suppresses rclone's "Must run --resync to
// recover." instruction. The stderr below is the real output of rclone v1.74.2
// for a pair whose listings are gone, reproduced end to end; because Bisync
// always passes --resilient, this - not the instruction wording - is what
// better-drive actually sees. rclone calls the error "retryable", but for this
// particular error it is not: the prior listings do not exist, so every retry
// fails identically until a --resync rebuilds them.
func TestBisyncNeedsResyncUnderResilientMode(t *testing.T) {
	const stderr = `ERROR : Bisync critical error: cannot find prior Path1 or Path2 listings, likely due to critical error on prior run
ERROR : Bisync aborted. Error is retryable without --resync due to --resilient mode.
NOTICE: Failed to bisync: bisync aborted`
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", stderr, errors.New("exit status 7")
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

func TestRemoteConfiguredWithServiceAccountFile(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "[gdrive-sa]\ntype = drive\nservice_account_file = C:/secrets/drive-service-account.json\n", "", nil
	})

	ok, err := e.RemoteConfigured("gdrive-sa")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want true, nil for Drive service_account_file auth", ok, err)
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

// TestListDriveRemotesFiltersByType verifies ListDriveRemotes parses `rclone
// listremotes --json` and returns only the Drive remotes: a config can hold
// remotes of any backend (s3, dropbox, ...), and better-drive only ever syncs
// against Drive ones, so offering the rest as accounts would be misleading.
func TestListDriveRemotesFiltersByType(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return `[
{"name":"gdrive","type":"drive","source":"file","description":""},
{"name":"work","type":"drive","source":"file","description":""},
{"name":"backup","type":"s3","source":"file","description":""}
]`, "", nil
	})
	got, err := e.ListDriveRemotes()
	if err != nil {
		t.Fatalf("ListDriveRemotes() error = %v", err)
	}
	want := []string{"gdrive", "work"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListDriveRemotes() = %v, want %v (non-drive remotes must be filtered out)", got, want)
	}
	if !reflect.DeepEqual(gotArgv, []string{"listremotes", "--json"}) {
		t.Errorf("argv = %v, want [listremotes --json]", gotArgv)
	}
}

// TestListDriveRemotesEmptyConfig verifies an rclone config with no remotes at
// all (the very first run, before any setup) is an empty result rather than an
// error - `account list` has to work precisely then, to print its hint.
func TestListDriveRemotesEmptyConfig(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "[]\n", "", nil
	})
	got, err := e.ListDriveRemotes()
	if err != nil {
		t.Fatalf("ListDriveRemotes() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListDriveRemotes() = %v, want empty", got)
	}
}

// TestListDriveRemotesReportsUnparseableJSON verifies a body rclone did not
// produce (a truncated pipe, a future format change) fails loudly instead of
// being read as "this config has no Drive remote" - that silent reading would
// tell a user their configured account had vanished.
func TestListDriveRemotesReportsUnparseableJSON(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "not json at all", "", nil
	})
	if _, err := e.ListDriveRemotes(); err == nil {
		t.Fatal("ListDriveRemotes() error = nil, want the parse failure surfaced")
	}
}

// TestAboutParsesQuota verifies About calls `rclone about <name>: --json` and
// fills Quota from the fields rclone reports (the trashed/other fields it also
// returns are deliberately dropped - see Quota's doc).
func TestAboutParsesQuota(t *testing.T) {
	var gotArgv []string
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		gotArgv = args
		return `{"total":5497558138880,"used":79762245855,"trashed":16055214801,"other":253151494371,"free":5164644398654}`, "", nil
	})
	got, err := e.About("gdrive")
	if err != nil {
		t.Fatalf("About() error = %v", err)
	}
	if got.Total != 5497558138880 || got.Used != 79762245855 || got.Free != 5164644398654 {
		t.Errorf("About() = %+v, want total/used/free filled from the rclone json", got)
	}
	if !reflect.DeepEqual(gotArgv, []string{"about", "gdrive:", "--json"}) {
		t.Errorf("argv = %v, want [about gdrive: --json]", gotArgv)
	}
}

// TestAboutReportsRcloneFailure verifies a failing `rclone about` (an expired
// token, no network) surfaces as an error rather than a zero-valued Quota,
// which a caller would render as a real "0 B of 0 B" reading.
func TestAboutReportsRcloneFailure(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "couldn't connect", errors.New("exit status 1")
	})
	if _, err := e.About("gdrive"); err == nil {
		t.Fatal("About() error = nil, want the rclone failure surfaced")
	}
}

// TestAboutReportsUnparseableJSON is About's counterpart to the
// ListDriveRemotes parse-failure guard, for the same reason: an unreadable
// body must not become a plausible-looking zero quota.
func TestAboutReportsUnparseableJSON(t *testing.T) {
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "<html>proxy error</html>", "", nil
	})
	if _, err := e.About("gdrive"); err == nil {
		t.Fatal("About() error = nil, want the parse failure surfaced")
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

func TestBisyncDryRunDoesNotCreateWorkdir(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "not-created")
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		return "", "", nil
	})

	if _, err := e.Bisync(BisyncParams{
		Path1: t.TempDir(), Path2: "gdrive:x", Workdir: workdir, DryRun: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("workdir %q exists after Bisync dry-run, want no filesystem write", workdir)
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
// The two flags must also both survive onto the bisync argv: this is what a
// user previewing the `sync --resync` recovery path actually runs, and a
// resync that quietly dropped --dry-run would rebuild the baseline for real.
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
	var bisyncArgv []string
	for _, argv := range calls {
		if len(argv) > 0 && argv[0] == "mkdir" {
			t.Fatalf("unexpected real `rclone mkdir` call under DryRun: %v", argv)
		}
		if len(argv) > 0 && argv[0] == "bisync" {
			bisyncArgv = argv
		}
	}
	if !containsArg(bisyncArgv, "--resync") || !containsArg(bisyncArgv, "--dry-run") {
		t.Errorf("argv %v, want both --resync and --dry-run", bisyncArgv)
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

func TestCountSourceObjectsUsesRcloneSizeWithTransferFilters(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "excluded.txt"), []byte("not transferable"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotArgs []string
	var gotRules string
	e := newFakeRunnerEngine("C:/cfg/rclone.conf", func(args ...string) (string, string, error) {
		gotArgs = append([]string(nil), args...)
		for i, arg := range args {
			if arg == "--filter-from" && i+1 < len(args) {
				data, err := os.ReadFile(args[i+1])
				if err != nil {
					t.Fatalf("read filter file during runner call: %v", err)
				}
				gotRules = string(data)
			}
		}
		return `{"count":0,"bytes":0}`, "", nil
	})

	count, err := e.CountSourceObjects(nil, source, []string{"- excluded.txt"}, nil)
	if err != nil {
		t.Fatalf("CountSourceObjects: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want effective filtered count 0", count)
	}
	if !reflect.DeepEqual(gotArgs[:4], []string{"--config", "C:/cfg/rclone.conf", "size", source}) {
		t.Fatalf("argv prefix = %#v", gotArgs)
	}
	if !strings.Contains(gotRules, "- excluded.txt") {
		t.Fatalf("filter rules = %q, want exclusion", gotRules)
	}
}

func TestExecWithContextDiscardsTransferStdout(t *testing.T) {
	e := newFakeStreamingRunnerEngine("", nil, func(_ context.Context, stdout, _ io.Writer, _ ...string) error {
		if stdout != io.Discard {
			t.Fatalf("transfer stdout writer = %T, want io.Discard", stdout)
		}
		return nil
	})

	if _, err := e.execWithContext(context.Background(), io.Discard, "sync", "local", "remote:"); err != nil {
		t.Fatalf("execWithContext: %v", err)
	}
}

func TestCountSourceObjectsCapturesBoundedSizeJSONWithContext(t *testing.T) {
	source := t.TempDir()
	e := newFakeStreamingRunnerEngine("", nil, func(_ context.Context, stdout, _ io.Writer, _ ...string) error {
		_, err := io.WriteString(stdout, `{"count":7,"bytes":21}`)
		return err
	})

	count, err := e.CountSourceObjects(context.Background(), source, nil, io.Discard)
	if err != nil {
		t.Fatalf("CountSourceObjects: %v", err)
	}
	if count != 7 {
		t.Fatalf("count = %d, want 7", count)
	}
}
