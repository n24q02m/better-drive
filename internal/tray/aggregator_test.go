package tray

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/syncloop"
)

// TestDeriveAllIdleIsIdle verifies that when every loop reports Idle, the
// combined state is Idle.
func TestDeriveAllIdleIsIdle(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StateIdle, 1: syncloop.StateIdle, 2: syncloop.StateIdle}
	if got := derive(states); got != syncloop.StateIdle {
		t.Fatalf("derive(all idle) = %v, want StateIdle", got)
	}
}

// TestDeriveAnySyncingIsSyncing verifies Syncing has the highest precedence:
// even with one loop Error and another Idle, one loop Syncing wins.
func TestDeriveAnySyncingIsSyncing(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StateIdle, 1: syncloop.StateSyncing, 2: syncloop.StateError}
	if got := derive(states); got != syncloop.StateSyncing {
		t.Fatalf("derive(any syncing) = %v, want StateSyncing", got)
	}
}

// TestDeriveAnyErrorIsError verifies Error beats NeedsResync/Idle/Paused
// (but not Syncing, covered separately).
func TestDeriveAnyErrorIsError(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StateIdle, 1: syncloop.StateError, 2: syncloop.StateNeedsResync}
	if got := derive(states); got != syncloop.StateError {
		t.Fatalf("derive(any error) = %v, want StateError", got)
	}
}

func TestDeriveAggregatePreservesNeedsResyncBehindError(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StateError, 1: syncloop.StateNeedsResync}
	got := deriveAggregate(states)
	if got.State != syncloop.StateError {
		t.Fatalf("aggregate state = %v, want StateError", got.State)
	}
	if !got.NeedsResync {
		t.Fatal("aggregate lost NeedsResync while StateError had display precedence")
	}
}

// TestDeriveAnyNeedsResyncBeatsIdle verifies NeedsResync outranks Idle/Paused
// when no loop is Syncing or Error.
func TestDeriveAnyNeedsResyncBeatsIdle(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StateIdle, 1: syncloop.StateNeedsResync}
	if got := derive(states); got != syncloop.StateNeedsResync {
		t.Fatalf("derive(any needs-resync) = %v, want StateNeedsResync", got)
	}
}

// TestDeriveAllPausedIsPaused verifies the combined state is Paused only
// when EVERY loop reports Paused (mirrors the tray's Pause/Resume menu item
// acting on all loops together).
func TestDeriveAllPausedIsPaused(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StatePaused, 1: syncloop.StatePaused}
	if got := derive(states); got != syncloop.StatePaused {
		t.Fatalf("derive(all paused) = %v, want StatePaused", got)
	}
}

// TestDeriveMixedPausedAndIdleIsIdle verifies a partial-pause snapshot (some
// loops paused, others idle, none syncing/error/needs-resync) falls back to
// Idle rather than Paused, since Paused is reserved for "all paused".
func TestDeriveMixedPausedAndIdleIsIdle(t *testing.T) {
	states := map[int]syncloop.State{0: syncloop.StatePaused, 1: syncloop.StateIdle}
	if got := derive(states); got != syncloop.StateIdle {
		t.Fatalf("derive(mixed paused/idle) = %v, want StateIdle", got)
	}
}

// TestDeriveEmptyIsIdle verifies an Aggregator with no registered loops
// derives Idle rather than panicking or zero-valuing to something else.
func TestDeriveEmptyIsIdle(t *testing.T) {
	if got := derive(map[int]syncloop.State{}); got != syncloop.StateIdle {
		t.Fatalf("derive(empty) = %v, want StateIdle", got)
	}
}

// noopSyncer implements syncloop.Syncer without ever being invoked; the
// tests below only drive loop state via Pause/Resume (synchronous, no
// goroutines), so Bisync/Copy/Sync never actually run.
type noopSyncer struct{}

func (noopSyncer) CountSourceObjects(context.Context, string, []string, io.Writer) (int64, error) {
	return 0, nil
}

func (noopSyncer) Bisync(engine.BisyncParams) (engine.BisyncResult, error) {
	return engine.BisyncResult{}, nil
}
func (noopSyncer) Copy(engine.CopyParams) error { return nil }
func (noopSyncer) Sync(engine.CopyParams) error { return nil }

func noFilters() ([]string, error) { return nil, nil }

// TestAggregatorRegisterFeedsCombinedState exercises the real Register/
// OnChange wiring (not just the pure derive function): registering two
// Loops and driving their state via Pause/Resume (synchronous) must update
// the Aggregator's combined state exactly as derive's precedence dictates.
func TestAggregatorRegisterFeedsCombinedState(t *testing.T) {
	loopA := syncloop.New(noopSyncer{}, "a", "gdrive:a", t.TempDir(), "copy", noFilters)
	loopB := syncloop.New(noopSyncer{}, "b", "gdrive:b", t.TempDir(), "copy", noFilters)

	agg := NewAggregator()
	var callbackFired int
	agg.OnChange(func(AggregateState) { callbackFired++ })
	agg.Register(0, loopA)
	agg.Register(1, loopB)

	loopA.Pause()
	loopB.Pause()
	if got := agg.State(); got != syncloop.StatePaused {
		t.Fatalf("agg.State() after both paused = %v, want StatePaused", got)
	}

	loopA.Resume()
	if got := agg.State(); got != syncloop.StateIdle {
		t.Fatalf("agg.State() after only loopA resumed = %v, want StateIdle (mixed paused/idle)", got)
	}

	loopB.Resume()
	if got := agg.State(); got != syncloop.StateIdle {
		t.Fatalf("agg.State() after both resumed = %v, want StateIdle", got)
	}

	if callbackFired == 0 {
		t.Fatal("OnChange callback was never invoked despite loop state changes")
	}
}

func TestAggregatorOnChangePreservesNeedsResyncBehindError(t *testing.T) {
	agg := NewAggregator()
	var got AggregateState
	agg.OnChange(func(snapshot AggregateState) { got = snapshot })

	agg.update(0, syncloop.StateError)
	agg.update(1, syncloop.StateNeedsResync)

	if got.State != syncloop.StateError || !got.NeedsResync {
		t.Fatalf("OnChange snapshot = %+v, want StateError with NeedsResync", got)
	}
}

func TestAggregatorCallbacksNeverDeliverOlderSnapshotAfterNewer(t *testing.T) {
	agg := NewAggregator()
	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	var seenMu sync.Mutex
	var seen []syncloop.State
	agg.OnChange(func(snapshot AggregateState) {
		if snapshot.State == syncloop.StateError {
			close(oldEntered)
			<-releaseOld
		}
		seenMu.Lock()
		seen = append(seen, snapshot.State)
		seenMu.Unlock()
	})

	oldDone := make(chan struct{})
	go func() {
		agg.update(0, syncloop.StateError)
		close(oldDone)
	}()
	<-oldEntered

	// The old implementation invoked this newer callback immediately while
	// the older one was blocked, deterministically producing [idle error]. A
	// serialized dispatcher queues it and returns without scheduler timing.
	agg.update(0, syncloop.StateIdle)
	if agg.State() != syncloop.StateIdle {
		t.Fatal("new aggregate state was not committed before callback dispatch")
	}
	close(releaseOld)
	<-oldDone

	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != 2 || seen[0] != syncloop.StateError || seen[1] != syncloop.StateIdle {
		t.Fatalf("callback states = %v, want generation order [error idle]", seen)
	}
}
