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
	mu       sync.Mutex
	calls    []engine.BisyncParams
	err      error
	inFlight func()
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

type panicSyncer struct{}

func (panicSyncer) Bisync(engine.BisyncParams) (engine.BisyncResult, error) {
	panic("simulated syncer panic")
}

func newLoop(s Syncer) *Loop {
	return New(s, "C:/x", "gdrive:x", "wd", func() ([]string, error) { return nil, nil })
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
	l := New(f, "C:/x", "gdrive:x", workdir, func() ([]string, error) { return nil, nil })
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
