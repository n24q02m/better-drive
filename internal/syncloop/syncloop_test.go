package syncloop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/paths"
)

type fakeSyncer struct {
	mu        sync.Mutex
	calls     []engine.BisyncParams
	copyCalls []engine.CopyParams
	syncCalls []engine.CopyParams
	err       error
	inFlight  func()
}

func (f *fakeSyncer) Bisync(p engine.BisyncParams) (engine.BisyncResult, error) {
	if f.inFlight != nil {
		f.inFlight()
	}
	f.mu.Lock()
	f.calls = append(f.calls, p)
	f.mu.Unlock()
	return engine.BisyncResult{}, f.err
}

func (f *fakeSyncer) Copy(p engine.CopyParams) error {
	if f.inFlight != nil {
		f.inFlight()
	}
	f.mu.Lock()
	f.copyCalls = append(f.copyCalls, p)
	f.mu.Unlock()
	return f.err
}

func (f *fakeSyncer) Sync(p engine.CopyParams) error {
	if f.inFlight != nil {
		f.inFlight()
	}
	f.mu.Lock()
	f.syncCalls = append(f.syncCalls, p)
	f.mu.Unlock()
	return f.err
}

type panicSyncer struct{}

func (panicSyncer) Bisync(engine.BisyncParams) (engine.BisyncResult, error) {
	panic("simulated syncer panic")
}

func (panicSyncer) Copy(engine.CopyParams) error { panic("simulated syncer panic") }
func (panicSyncer) Sync(engine.CopyParams) error { panic("simulated syncer panic") }

func newLoop(s Syncer) *Loop {
	return New(s, "C:/x", "gdrive:x", "wd", "bisync", func() ([]string, error) { return nil, nil })
}

func newLoopMode(s Syncer, mode string) *Loop {
	return New(s, "C:/x", "gdrive:x", "wd", mode, func() ([]string, error) { return nil, nil })
}

func TestFirstRunResyncsThenNot(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.runOnce()
	l.runOnce()
	if len(f.calls) != 2 {
		t.Fatalf("calls=%d", len(f.calls))
	}
	if !f.calls[0].Resync {
		t.Error("first call must resync")
	}
	if f.calls[1].Resync {
		t.Error("second call must NOT resync")
	}
	if l.State() != StateIdle {
		t.Errorf("state=%v", l.State())
	}
}

func TestNeedsResyncErrorSetsState(t *testing.T) {
	f := &fakeSyncer{err: engine.ErrNeedsResync}
	l := newLoop(f)
	l.hasBaseline = true // avoid the first-run auto-resync branch, not under test here
	l.runOnce()
	if l.State() != StateNeedsResync {
		t.Fatalf("state=%v, want NeedsResync", l.State())
	}
}

func TestGenericErrorSetsError(t *testing.T) {
	f := &fakeSyncer{err: errors.New("boom")}
	l := newLoop(f)
	l.hasBaseline = true
	l.runOnce()
	if l.State() != StateError {
		t.Fatalf("state=%v", l.State())
	}
}

func TestPauseSkipsRun(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.Pause()
	l.runOnce()
	if len(f.calls) != 0 {
		t.Fatalf("paused but ran %d times", len(f.calls))
	}
	if l.State() != StatePaused {
		t.Fatalf("state=%v", l.State())
	}
}

func TestExistingBaselineSkipsResync(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "foo.lst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSyncer{}
	l := New(f, "C:/x", "gdrive:x", workdir, "bisync", func() ([]string, error) { return nil, nil })
	l.runOnce()
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d", len(f.calls))
	}
	if f.calls[0].Resync {
		t.Error("existing baseline (*.lst present) must NOT trigger resync on first run")
	}
}

// TestForeignJobListingsDoNotCountAsBaseline is the regression test for a job
// that syncs once and is then stuck forever: a workdir from another stable job
// ID must never satisfy the current job's baseline check.
func TestForeignJobListingsDoNotCountAsBaseline(t *testing.T) {
	root := t.TempDir()
	dirFor := func(jobID string) string {
		return filepath.Join(root, filepath.Base(paths.JobWorkdir(jobID)))
	}
	foreign := dirFor("foreign-job")
	mine := dirFor("mine-job")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	// rclone derives its listing file names from the two paths of the session
	// that wrote them, which is why these belong to the other pair alone.
	if err := os.WriteFile(filepath.Join(foreign, "C__other..gdrive_other..path1.lst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &fakeSyncer{}
	l := New(f, "C:/mine", "gdrive:mine", mine, "bisync", func() ([]string, error) { return nil, nil })
	l.runOnce()
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(f.calls))
	}
	if !f.calls[0].Resync {
		t.Error("a pair with no listings of its own must resync, even when another pair's listings exist")
	}

	// Control: the pair those listings actually belong to must still NOT
	// resync, or the fix would have traded one bug (a stuck pair) for another
	// (a resync on every run, which does not propagate deletions).
	owner := &fakeSyncer{}
	ownerLoop := New(owner, "C:/other", "gdrive:other", foreign, "bisync", func() ([]string, error) { return nil, nil })
	ownerLoop.runOnce()
	if len(owner.calls) != 1 {
		t.Fatalf("owner calls=%d, want 1", len(owner.calls))
	}
	if owner.calls[0].Resync {
		t.Error("the pair owning the listings must keep its baseline, not resync")
	}
}

func TestStartCancels(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Start(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

func TestRunOnceThreadsExecutionContextAndStderr(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "one-shot")

	for _, mode := range []string{"copy", "sync", "bisync"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeSyncer{}
			var stderr bytes.Buffer
			l := newLoopMode(f, mode)
			l.SetExecution(ctx, &stderr)
			if err := l.RunOnce(); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}

			switch mode {
			case "copy":
				if len(f.copyCalls) != 1 || f.copyCalls[0].Context != ctx || f.copyCalls[0].Stderr != &stderr {
					t.Fatalf("copy params = %+v, want caller context and stderr", f.copyCalls)
				}
			case "sync":
				if len(f.syncCalls) != 1 || f.syncCalls[0].Context != ctx || f.syncCalls[0].Stderr != &stderr {
					t.Fatalf("sync params = %+v, want caller context and stderr", f.syncCalls)
				}
			default:
				if len(f.calls) != 1 || f.calls[0].Context != ctx || f.calls[0].Stderr != &stderr {
					t.Fatalf("bisync params = %+v, want caller context and stderr", f.calls)
				}
			}
		})
	}
}

func TestResumeReturnsToIdle(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.Pause()
	if l.State() != StatePaused {
		t.Fatalf("state after Pause=%v, want StatePaused", l.State())
	}
	l.Resume()
	if l.State() != StateIdle {
		t.Fatalf("state after Resume=%v, want StateIdle", l.State())
	}
}

func TestResumeRestoresNeedsResyncLatch(t *testing.T) {
	f := &fakeSyncer{err: engine.ErrNeedsResync}
	l := newLoop(f)
	l.hasBaseline = true
	if err := l.runOnce(); !errors.Is(err, engine.ErrNeedsResync) {
		t.Fatalf("runOnce error = %v, want ErrNeedsResync", err)
	}

	l.Pause()
	if l.State() != StatePaused {
		t.Fatalf("state after Pause=%v, want StatePaused", l.State())
	}
	l.Resume()
	if l.State() != StateNeedsResync {
		t.Fatalf("state after Resume=%v, want StateNeedsResync while latch is open", l.State())
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{
		StateIdle:        "idle",
		StateSyncing:     "syncing",
		StateError:       "error",
		StatePaused:      "paused",
		StateNeedsResync: "needs-resync",
		State(99):        "unknown",
	}
	for st, want := range cases {
		if got := st.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", st, got, want)
		}
	}
}

func TestOnChangeInvokedOnStateChange(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	var mu sync.Mutex
	var seen []State
	l.OnChange(func(st State) {
		mu.Lock()
		seen = append(seen, st)
		mu.Unlock()
	})
	l.runOnce()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("OnChange callback was never invoked")
	}
	if seen[len(seen)-1] != StateIdle {
		t.Errorf("last observed state = %v, want StateIdle", seen[len(seen)-1])
	}
}

func TestSyncNowRunsAsync(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	if accepted := l.SyncNow(); !accepted {
		t.Fatal("SyncNow rejected before shutdown")
	}
	deadline := time.After(2 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.calls)
		f.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("SyncNow did not trigger a Bisync call in time")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestShutdownWaitsForAcceptedSyncNow(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := &fakeSyncer{inFlight: func() {
		close(entered)
		<-release
	}}
	l := newLoop(f)
	l.hasBaseline = true
	if accepted := l.SyncNow(); !accepted {
		t.Fatal("SyncNow rejected before shutdown")
	}
	<-entered

	shutdownDone := make(chan struct{})
	go func() {
		l.Shutdown()
		close(shutdownDone)
	}()

	for {
		l.mu.Lock()
		closed := l.manualClosed
		l.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while an accepted SyncNow was still blocked")
	default:
	}

	close(release)
	<-shutdownDone
}

func TestSyncNowRejectedAfterShutdown(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.Shutdown()

	if accepted := l.SyncNow(); accepted {
		t.Fatal("SyncNow accepted work after shutdown")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatalf("SyncNow after shutdown ran %d sync cycles, want 0", len(f.calls))
	}
}

func TestShutdownAllClosesEveryGateBeforeWaiting(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	blocked := newLoop(&fakeSyncer{inFlight: func() {
		close(entered)
		<-release
	}})
	blocked.hasBaseline = true
	later := newLoop(&fakeSyncer{})
	if !blocked.SyncNow() {
		t.Fatal("blocked loop rejected SyncNow before shutdown")
	}
	<-entered

	done := make(chan struct{})
	go func() {
		ShutdownAll([]*Loop{blocked, later})
		close(done)
	}()
	for {
		later.mu.Lock()
		closed := later.manualClosed
		later.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if later.SyncNow() {
		t.Fatal("later loop accepted SyncNow while ShutdownAll waited on an earlier loop")
	}
	select {
	case <-done:
		t.Fatal("ShutdownAll returned while accepted work was blocked")
	default:
	}
	close(release)
	<-done
}

func TestRunOncePanicRecovers(t *testing.T) {
	f := &panicSyncer{}
	l := newLoop(f)
	l.hasBaseline = true
	l.runOnce() // must not panic out of the test
	if l.State() != StateError {
		t.Fatalf("state after panicking Syncer = %v, want StateError", l.State())
	}
	if l.running {
		t.Fatal("running flag left true after panic recovery; no-overlap guard would wedge forever")
	}
}

func TestPauseDuringInFlightSync(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	f := &fakeSyncer{inFlight: func() {
		close(entered)
		<-release
	}}
	l := newLoop(f)
	l.hasBaseline = true
	go func() { l.runOnce(); close(done) }()
	<-entered      // sync is in-flight
	l.Pause()      // pause requested mid-flight
	close(release) // allow the in-flight sync to finish
	<-done         // runOnce returned
	if l.State() != StatePaused {
		t.Fatalf("state after pause-during-sync = %v, want StatePaused", l.State())
	}
}

// TestModeCopyCallsCopyNotBisync verifies mode="copy" dispatches to the
// Syncer's Copy method (1-way backup) and never touches Bisync - no
// resync/baseline concept applies to copy mode.
func TestModeCopyCallsCopyNotBisync(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoopMode(f, "copy")
	l.runOnce()
	if len(f.copyCalls) != 1 {
		t.Fatalf("copyCalls=%d, want 1", len(f.copyCalls))
	}
	if len(f.calls) != 0 {
		t.Fatalf("bisync calls=%d, want 0 (mode=copy must not call Bisync)", len(f.calls))
	}
	if len(f.syncCalls) != 0 {
		t.Fatalf("syncCalls=%d, want 0", len(f.syncCalls))
	}
	if f.copyCalls[0].Local != l.path1 || f.copyCalls[0].Remote != l.path2 {
		t.Errorf("copy params = %+v, want Local=%q Remote=%q", f.copyCalls[0], l.path1, l.path2)
	}
	if l.State() != StateIdle {
		t.Errorf("state=%v, want StateIdle", l.State())
	}
}

// TestModeSyncCallsSyncNotBisync verifies mode="sync" dispatches to the
// Syncer's Sync method (mirror).
func TestModeSyncCallsSyncNotBisync(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoopMode(f, "sync")
	l.runOnce()
	if len(f.syncCalls) != 1 {
		t.Fatalf("syncCalls=%d, want 1", len(f.syncCalls))
	}
	if len(f.calls) != 0 {
		t.Fatalf("bisync calls=%d, want 0 (mode=sync must not call Bisync)", len(f.calls))
	}
	if len(f.copyCalls) != 0 {
		t.Fatalf("copyCalls=%d, want 0", len(f.copyCalls))
	}
	if l.State() != StateIdle {
		t.Errorf("state=%v, want StateIdle", l.State())
	}
}

// TestModeBisyncUnaffectedByModeSupport is a regression guard: mode="bisync"
// (the default/existing behaviour) must still call Bisync with the resync
// flag driven by hasBaseline, exactly as before mode support existed.
func TestModeBisyncUnaffectedByModeSupport(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoopMode(f, "bisync")
	l.runOnce()
	if len(f.calls) != 1 {
		t.Fatalf("bisync calls=%d, want 1", len(f.calls))
	}
	if !f.calls[0].Resync {
		t.Error("first bisync run must resync")
	}
	if len(f.copyCalls) != 0 || len(f.syncCalls) != 0 {
		t.Fatalf("mode=bisync must not call Copy/Sync: copyCalls=%d syncCalls=%d", len(f.copyCalls), len(f.syncCalls))
	}
}

// TestModeCopyGenericErrorSetsStateError verifies a plain error from Copy
// (no ErrNeedsResync concept in 1-way modes) is classified as StateError.
func TestModeCopyGenericErrorSetsStateError(t *testing.T) {
	f := &fakeSyncer{err: errors.New("copy failed")}
	l := newLoopMode(f, "copy")
	l.runOnce()
	if l.State() != StateError {
		t.Fatalf("state=%v, want StateError", l.State())
	}
}

// TestModeSyncGenericErrorSetsStateError mirrors the copy-mode error test for
// sync mode.
func TestModeSyncGenericErrorSetsStateError(t *testing.T) {
	f := &fakeSyncer{err: errors.New("sync failed")}
	l := newLoopMode(f, "sync")
	l.runOnce()
	if l.State() != StateError {
		t.Fatalf("state=%v, want StateError", l.State())
	}
}

// TestRunOnceReturnsSyncerError verifies the exported RunOnce (the one-shot
// entry point used by the `sync` CLI command) runs exactly one cycle and
// surfaces the Syncer's error, in addition to the pre-existing State()
// transition.
func TestRunOnceReturnsSyncerError(t *testing.T) {
	f := &fakeSyncer{err: errors.New("boom")}
	l := newLoop(f)
	l.hasBaseline = true // avoid the first-run auto-resync branch, not under test here
	err := l.RunOnce()
	if err == nil || err.Error() != "boom" {
		t.Fatalf("RunOnce() err = %v, want \"boom\"", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d, want exactly 1 (one-shot)", len(f.calls))
	}
	if l.State() != StateError {
		t.Fatalf("state=%v, want StateError", l.State())
	}
}

// TestRunOnceSuccessReturnsNil verifies RunOnce returns nil and leaves the
// loop idle after a successful one-shot cycle.
func TestRunOnceSuccessReturnsNil(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	if err := l.RunOnce(); err != nil {
		t.Fatalf("RunOnce() err = %v, want nil", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d, want exactly 1", len(f.calls))
	}
	if l.State() != StateIdle {
		t.Fatalf("state=%v, want StateIdle", l.State())
	}
}

// TestOnResultInvokedWithSuccess verifies OnResult fires with a nil error
// after a successful cycle - the callback backing the daemon's per-cycle log
// line ("OK" vs "FAILED: <err>").
func TestOnResultInvokedWithSuccess(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	var mu sync.Mutex
	var called bool
	var got error
	l.OnResult(func(err error) {
		mu.Lock()
		called = true
		got = err
		mu.Unlock()
	})
	if err := l.RunOnce(); err != nil {
		t.Fatalf("RunOnce() err = %v, want nil", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("OnResult callback was never invoked")
	}
	if got != nil {
		t.Errorf("OnResult err = %v, want nil", got)
	}
}

// TestOnResultInvokedWithSyncerError verifies OnResult receives the exact
// Syncer error for a failing cycle (not just a "something failed" signal),
// since the daemon log line embeds it verbatim.
func TestOnResultInvokedWithSyncerError(t *testing.T) {
	f := &fakeSyncer{err: errors.New("boom")}
	l := newLoop(f)
	l.hasBaseline = true // avoid the first-run auto-resync branch, not under test here
	var mu sync.Mutex
	var got error
	l.OnResult(func(err error) {
		mu.Lock()
		got = err
		mu.Unlock()
	})
	if err := l.RunOnce(); err == nil {
		t.Fatal("RunOnce() err = nil, want \"boom\"")
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.Error() != "boom" {
		t.Errorf("OnResult err = %v, want \"boom\"", got)
	}
}

// TestSetDryRunThreadsIntoBisyncParams verifies SetDryRun(true) is read at
// the start of the next runOnce cycle and forwarded as BisyncParams.DryRun.
func TestSetDryRunThreadsIntoBisyncParams(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.hasBaseline = true
	l.SetDryRun(true)
	l.runOnce()
	if len(f.calls) != 1 || !f.calls[0].DryRun {
		t.Fatalf("calls=%+v, want exactly 1 call with DryRun=true", f.calls)
	}
}

// TestSetDryRunThreadsIntoSyncParams mirrors the bisync case for mode="sync"
// (CopyParams), the mode dry-run exists to preview (remote deletion).
func TestSetDryRunThreadsIntoSyncParams(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoopMode(f, "sync")
	l.SetDryRun(true)
	l.runOnce()
	if len(f.syncCalls) != 1 || !f.syncCalls[0].DryRun {
		t.Fatalf("syncCalls=%+v, want exactly 1 call with DryRun=true", f.syncCalls)
	}
}

// TestDryRunFalseByDefault verifies a Loop that never called SetDryRun keeps
// applying real changes - dry-run must be opt-in.
func TestDryRunFalseByDefault(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.runOnce()
	if len(f.calls) != 1 || f.calls[0].DryRun {
		t.Fatalf("calls=%+v, want exactly 1 call with DryRun=false", f.calls)
	}
}

// TestSetForceResyncRebuildsAnExistingBaseline verifies SetForceResync(true)
// asks for --resync even when the workdir already holds listing files, which
// is the whole point of the `sync --resync` escape hatch: the listings can be
// present and still be unusable (they belong to another path1/path2 session,
// or rclone abandoned them mid-run), and that state is otherwise unrecoverable
// without deleting the workdir by hand.
func TestSetForceResyncRebuildsAnExistingBaseline(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "foo.lst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSyncer{}
	l := New(f, "C:/x", "gdrive:x", workdir, "bisync", func() ([]string, error) { return nil, nil })
	if !l.hasBaseline {
		t.Fatal("fixture is wrong: the loop must start with a baseline for this test to mean anything")
	}
	l.SetForceResync(true)
	l.runOnce()
	if len(f.calls) != 1 || !f.calls[0].Resync {
		t.Fatalf("calls=%+v, want exactly 1 call with Resync=true", f.calls)
	}
}

// TestForceResyncKeepsDryRunNonDestructive verifies the two flags compose:
// forcing a baseline rebuild under --dry-run must still preview only. Both
// reach the Syncer, where engine.Bisync's own guard skips the real mkdir /
// remote-mkdir that a resync would otherwise perform.
func TestForceResyncKeepsDryRunNonDestructive(t *testing.T) {
	f := &fakeSyncer{}
	l := newLoop(f)
	l.SetForceResync(true)
	l.SetDryRun(true)
	l.runOnce()
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(f.calls))
	}
	if !f.calls[0].Resync || !f.calls[0].DryRun {
		t.Errorf("params = %+v, want both Resync and DryRun true", f.calls[0])
	}
}

// TestForceResyncIsOptIn verifies a Loop that never called SetForceResync
// keeps its baseline behaviour: no resync once listings exist. An unconditional
// resync would silently stop propagating deletions.
func TestForceResyncIsOptIn(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "foo.lst"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSyncer{}
	l := New(f, "C:/x", "gdrive:x", workdir, "bisync", func() ([]string, error) { return nil, nil })
	l.runOnce()
	if len(f.calls) != 1 || f.calls[0].Resync {
		t.Fatalf("calls=%+v, want exactly 1 call with Resync=false", f.calls)
	}
}

// TestNeedsResyncErrorDoesNotAutoResyncNextRun is the guard against "fixing"
// a lost baseline by retrying with --resync automatically: rclone bisync
// --resync does not propagate deletions, so an automatic rebuild would
// resurrect files the user deleted while the daemon was off. Recovery stays
// explicit (`better-drive sync --resync`), so a second cycle after
// ErrNeedsResync must still ask for a plain, non-resync bisync.
func TestNeedsResyncErrorDoesNotAutoResyncNextRun(t *testing.T) {
	f := &fakeSyncer{err: engine.ErrNeedsResync}
	l := newLoop(f)
	l.hasBaseline = true // a baseline exists on disk; rclone just rejected it
	l.runOnce()
	l.runOnce()
	if len(f.calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(f.calls))
	}
	for i, c := range f.calls {
		if c.Resync {
			t.Errorf("call %d resynced on its own; recovery must stay explicit", i)
		}
	}
}

// TestModeDefaultsToBisyncWhenEmpty verifies New("") behaves like
// New("bisync") for backward compatibility (config.Load already defaults an
// empty toml mode to "bisync", but Loop itself must be defensive too).
func TestModeDefaultsToBisyncWhenEmpty(t *testing.T) {
	f := &fakeSyncer{}
	l := New(f, "C:/x", "gdrive:x", "wd", "", func() ([]string, error) { return nil, nil })
	l.runOnce()
	if len(f.calls) != 1 {
		t.Fatalf("bisync calls=%d, want 1 (empty mode must default to bisync)", len(f.calls))
	}
}

type directionReplicaSyncer struct {
	copies      []engine.CopyParams
	errByRemote map[string]error
}

func (f *directionReplicaSyncer) Bisync(p engine.BisyncParams) (engine.BisyncResult, error) {
	return engine.BisyncResult{}, f.errByRemote[p.Path2]
}
func (f *directionReplicaSyncer) Copy(p engine.CopyParams) error {
	f.copies = append(f.copies, p)
	return f.errByRemote[p.Remote]
}
func (f *directionReplicaSyncer) Sync(engine.CopyParams) error { return nil }

func TestNewWithReplicasSupportsPullDirection(t *testing.T) {
	f := &directionReplicaSyncer{errByRemote: map[string]error{}}
	l := NewWithReplicas(f, "C:/source", []engine.ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Workdir: "wd/r1", Required: true}}, "copy", "pull", func() ([]string, error) { return nil, nil })
	if err := l.RunOnce(); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(f.copies) != 1 || f.copies[0].Local != "gdrive:backup" || f.copies[0].Remote != "C:/source" {
		t.Fatalf("copy calls = %#v, want reversed pull direction", f.copies)
	}
}

func TestNewWithReplicasPreservesOptionalFailureAsDegraded(t *testing.T) {
	f := &directionReplicaSyncer{errByRemote: map[string]error{"r2:optional": errors.New("optional failed")}}
	l := NewWithReplicas(f, "C:/source", []engine.ReplicaSpec{
		{ID: "r1", Target: "gdrive:required", Workdir: "wd/r1", Required: true},
		{ID: "r2", Target: "r2:optional", Workdir: "wd/r2", Required: false},
	}, "copy", "push", func() ([]string, error) { return nil, nil })
	if err := l.RunOnce(); err != nil {
		t.Fatalf("optional failure: %v, want nil", err)
	}
	if l.State() != StateIdle {
		t.Fatalf("state = %v, want idle for optional failure", l.State())
	}
	if len(f.copies) != 2 {
		t.Fatalf("copy calls = %#v, want both replicas attempted", f.copies)
	}
}
