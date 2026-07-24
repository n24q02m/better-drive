package syncloop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/engine"
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
	l.hasBaseline = true // giả lập đã có baseline để không auto-resync
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
	l.SyncNow()
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
