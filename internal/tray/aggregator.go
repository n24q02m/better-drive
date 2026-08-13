package tray

import (
	"sync"

	"github.com/n24q02m/better-drive/internal/syncloop"
)

// AggregateState keeps the scalar state used for tray display and independent
// metadata that must survive scalar precedence. NeedsResync remains true when
// another pair's StateError wins the displayed State, allowing the Sync now
// action to distinguish a retryable error from a pair that requires --resync.
type AggregateState struct {
	State       syncloop.State
	NeedsResync bool
}

// Aggregator combines the per-loop syncloop.State of N independent sync
// loops (one per config pair) into an AggregateState for the tray. Scalar
// State precedence (highest first):
//
//	Syncing > Error > NeedsResync > Paused (only when ALL loops are Paused) > Idle
//
// A mutex-guarded map of per-loop states plus a pure derive function keeps
// this simple: Register wires a Loop's OnChange callback to record its state
// under a stable key (the pair's index) and recompute the combined state.
type Aggregator struct {
	mu       sync.Mutex
	states   map[int]syncloop.State
	onChange func(AggregateState)
}

// NewAggregator returns an empty Aggregator; State() on an Aggregator with no
// registered loops reports StateIdle.
func NewAggregator() *Aggregator {
	return &Aggregator{states: make(map[int]syncloop.State)}
}

// OnChange registers fn to be called with the newly derived combined state
// whenever any registered loop's own state changes. Only one callback is
// kept (like syncloop.Loop.OnChange); a later call replaces the former.
func (a *Aggregator) OnChange(fn func(AggregateState)) {
	a.mu.Lock()
	a.onChange = fn
	a.mu.Unlock()
}

// Register wires loop's OnChange callback so its state updates feed this
// Aggregator under key idx (the pair's index in the config's [[pair]] list).
// idx must be unique per loop registered on the same Aggregator.
func (a *Aggregator) Register(idx int, loop *syncloop.Loop) {
	loop.OnChange(func(st syncloop.State) { a.update(idx, st) })
}

func (a *Aggregator) update(idx int, st syncloop.State) {
	a.mu.Lock()
	a.states[idx] = st
	combined := deriveAggregate(a.states)
	fn := a.onChange
	a.mu.Unlock()
	if fn != nil {
		fn(combined)
	}
}

// State returns the currently derived combined state.
func (a *Aggregator) State() syncloop.State {
	return a.Aggregate().State
}

// Aggregate returns a consistent snapshot of the displayed state and its
// independent metadata.
func (a *Aggregator) Aggregate() AggregateState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return deriveAggregate(a.states)
}

// derive computes the combined state from a snapshot of per-loop states. It
// is a pure function (no locking) so it can be unit tested directly with a
// plain map literal.
func derive(states map[int]syncloop.State) syncloop.State {
	return deriveAggregate(states).State
}

func deriveAggregate(states map[int]syncloop.State) AggregateState {
	if len(states) == 0 {
		return AggregateState{State: syncloop.StateIdle}
	}
	var anySyncing, anyError, anyNeedsResync, allPaused bool
	allPaused = true
	for _, st := range states {
		switch st {
		case syncloop.StateSyncing:
			anySyncing = true
		case syncloop.StateError:
			anyError = true
		case syncloop.StateNeedsResync:
			anyNeedsResync = true
		}
		if st != syncloop.StatePaused {
			allPaused = false
		}
	}
	switch {
	case anySyncing:
		return AggregateState{State: syncloop.StateSyncing, NeedsResync: anyNeedsResync}
	case anyError:
		return AggregateState{State: syncloop.StateError, NeedsResync: anyNeedsResync}
	case anyNeedsResync:
		return AggregateState{State: syncloop.StateNeedsResync, NeedsResync: true}
	case allPaused:
		return AggregateState{State: syncloop.StatePaused}
	default:
		return AggregateState{State: syncloop.StateIdle}
	}
}
