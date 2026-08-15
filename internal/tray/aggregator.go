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

type aggregateChange struct {
	snapshot AggregateState
	callback func(AggregateState)
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
	mu          sync.Mutex
	states      map[int]syncloop.State
	onChange    func(AggregateState)
	pending     []aggregateChange
	dispatching bool
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
	if fn == nil {
		a.mu.Unlock()
		return
	}
	// Enqueue under the same lock that establishes state order. Exactly one
	// updater drains the queue, so callbacks cannot overtake one another even
	// when an older callback blocks while a newer state is committed. Keeping
	// the callback outside a.mu lets callbacks safely query Aggregate/State.
	a.pending = append(a.pending, aggregateChange{snapshot: combined, callback: fn})
	if a.dispatching {
		a.mu.Unlock()
		return
	}
	a.dispatching = true
	a.mu.Unlock()

	a.dispatchChanges()
}

func (a *Aggregator) dispatchChanges() {
	for {
		a.mu.Lock()
		if len(a.pending) == 0 {
			a.dispatching = false
			a.mu.Unlock()
			return
		}
		change := a.pending[0]
		a.pending[0] = aggregateChange{}
		a.pending = a.pending[1:]
		a.mu.Unlock()

		change.callback(change.snapshot)
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
