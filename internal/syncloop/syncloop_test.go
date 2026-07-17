package syncloop

import (
	"errors"
	"sync"
	"testing"

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
