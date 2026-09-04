package syncloop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/engine"
)

type State int

const (
	StateIdle State = iota
	StateSyncing
	StateError
	StatePaused
	StateNeedsResync
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateSyncing:
		return "syncing"
	case StateError:
		return "error"
	case StatePaused:
		return "paused"
	case StateNeedsResync:
		return "needs-resync"
	}
	return "unknown"
}

type Syncer = engine.Transferer

type IgnoreFunc func() ([]string, error)

type Loop struct {
	s            Syncer
	path1        string
	path2        string
	workdir      string
	mode         string
	direction    string
	replicas     []engine.ReplicaSpec
	baselines    map[string]bool
	ignore       IgnoreFunc
	history      engine.HistoryStore
	concurrency  engine.ConcurrencyConfig
	mu           sync.Mutex
	state        State
	paused       bool
	needsResync  bool
	hasBaseline  bool
	running      bool
	dryRun       bool
	forceResync  bool
	execContext  context.Context
	stderr       io.Writer
	onChange     func(State)
	onResult     func(error)
	lastSummary  engine.ReplicaSummary
	manualWG     sync.WaitGroup
	manualClosed bool
}

// New creates a single-replica Loop for the legacy internal call shape.
// Bisync defaults to bidirectional; copy and sync default to push.
func New(s Syncer, path1, path2, workdir, mode string, ignore IgnoreFunc) *Loop {
	direction := "push"
	if mode == "" || mode == "bisync" {
		direction = "bidirectional"
	}
	return NewWithReplicas(s, path1, []engine.ReplicaSpec{{ID: "default", Target: path2, Workdir: workdir, Required: true}}, mode, direction, ignore)
}

// NewWithReplicas creates a Loop whose one cycle attempts every destination.
// Required and optional outcomes are kept independently by engine.ExecuteReplicas.
func NewWithReplicas(s Syncer, path1 string, replicas []engine.ReplicaSpec, mode, direction string, ignore IgnoreFunc) *Loop {
	if mode == "" {
		mode = "bisync"
	}
	if direction == "" {
		direction = "push"
		if mode == "bisync" {
			direction = "bidirectional"
		}
	}
	path2, workdir := "", ""
	if len(replicas) > 0 {
		path2, workdir = replicas[0].Target, replicas[0].Workdir
	}
	baselines := make(map[string]bool, len(replicas))
	for _, replica := range replicas {
		baselines[replica.ID] = baselineExists(replica.Workdir)
	}
	hasBaseline := false
	if len(replicas) > 0 {
		hasBaseline = baselines[replicas[0].ID]
	}
	return &Loop{s: s, path1: path1, path2: path2, workdir: workdir, mode: mode, direction: direction,
		replicas: append([]engine.ReplicaSpec(nil), replicas...), baselines: baselines, ignore: ignore,
		state: StateIdle, hasBaseline: hasBaseline}
}

// baselineExists reports whether a prior bisync run already left listing
// files (*.lst) in workdir. Without this, every process restart would leave
// hasBaseline false, forcing a --resync on the next run; rclone bisync
// --resync does not propagate deletions, so a file deleted locally while the
// daemon was off would get resurrected from Drive.
func baselineExists(workdir string) bool {
	matches, _ := filepath.Glob(filepath.Join(workdir, "*.lst"))
	return len(matches) > 0
}

// inspectSourceSafety records conservative source evidence for destructive
// push-side transfers. The transferer computes the effective object count with
// the same filters it will pass to rclone, so excluded-only inputs cannot make
// an empty destructive source look non-empty.
func inspectSourceSafety(ctx context.Context, syncer Syncer, path string, filters []string, stderr io.Writer) (wasNonEmpty *bool, objectCount *int64, err error) {
	count, err := syncer.CountSourceObjects(ctx, path, filters, stderr)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect source %q: %w", path, err)
	}
	history := true
	return &history, &count, nil
}

func (l *Loop) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *Loop) OnChange(fn func(State)) {
	l.mu.Lock()
	l.onChange = fn
	l.mu.Unlock()
}

// OnResult registers fn to be called once per completed sync cycle with that
// cycle's outcome (nil on success, the Syncer's error on failure, or the
// recovered error on panic).
func (l *Loop) OnResult(fn func(error)) {
	l.mu.Lock()
	l.onResult = fn
	l.mu.Unlock()
}

// SetDryRun controls whether the next (and subsequent) sync cycles preview
// changes via rclone's own --dry-run instead of applying them. It exists for
// the `sync` CLI command's --dry-run flag; the continuous daemon (`run`)
// never sets it.
func (l *Loop) SetDryRun(v bool) {
	l.mu.Lock()
	l.dryRun = v
	l.mu.Unlock()
}

// SetForceResync makes the next (and subsequent) bisync cycles rebuild the
// baseline via rclone's --resync even when listing files already exist in the
// workdir. It backs the `sync` CLI command's --resync flag, which is the
// escape hatch for listings that are present but unusable - they belong to a
// different path1/path2 session, or rclone abandoned them mid-run - a state
// rclone reports as ErrNeedsResync and that no amount of re-running fixes.
//
// It is deliberately a user-driven flag rather than an automatic reaction to
// ErrNeedsResync: rclone bisync --resync does not propagate deletions, so an
// automatic rebuild would resurrect files deleted while the daemon was off,
// which is the same hazard baselineExists guards against. The continuous
// daemon (`run`) therefore never sets it.
func (l *Loop) SetForceResync(v bool) {
	l.mu.Lock()
	l.forceResync = v
	l.mu.Unlock()
}

func (l *Loop) LastReplicaSummary() engine.ReplicaSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	summary := l.lastSummary
	summary.Outcomes = append([]engine.ReplicaOutcome(nil), summary.Outcomes...)
	return summary
}

// SetHistoryStore injects a per-cycle history persistence sink. If not set,
// history is discarded through NopHistoryStore. The engine never mints its own
// store.
func (l *Loop) SetHistoryStore(h engine.HistoryStore) {
	l.mu.Lock()
	l.history = h
	l.mu.Unlock()
}

// SetConcurrency configures bounded parallelism across replicas for this loop.
func (l *Loop) SetConcurrency(c engine.ConcurrencyConfig) {
	l.mu.Lock()
	l.concurrency = c
	l.mu.Unlock()
}

// SetExecution opts subsequent cycles into context-aware execution with live
// rclone stderr. The one-shot CLI uses it for cancellation and progress; the
// continuous daemon leaves both values unset and retains captured execution.
func (l *Loop) SetExecution(ctx context.Context, stderr io.Writer) {
	l.mu.Lock()
	l.execContext = ctx
	l.stderr = stderr
	l.mu.Unlock()
}

func (l *Loop) setState(st State) {
	l.mu.Lock()
	l.state = st
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

// runOnce executes exactly one sync cycle (mode dispatch + bisync
// resync-if-no-baseline) and returns the Syncer's error for that cycle (nil
// on success). State()/OnChange observers still see the same transitions as
// before RunOnce existed; the return value is additive, for one-shot callers
// (RunOnce, and in turn the `sync` CLI command) that need the outcome
// directly instead of polling State().
func (l *Loop) runOnce() (err error) {
	l.mu.Lock()
	if l.paused {
		l.mu.Unlock()
		l.setState(StatePaused)
		return nil
	}
	if l.running { // no-overlap guard
		l.mu.Unlock()
		return nil
	}
	l.running = true
	replicas := append([]engine.ReplicaSpec(nil), l.replicas...)
	for i := range replicas {
		if l.mode == "bisync" {
			hasBaseline := l.baselines[replicas[i].ID]
			if i == 0 {
				hasBaseline = hasBaseline || l.hasBaseline
			}
			replicas[i].Resync = !hasBaseline || l.forceResync
		}
	}
	dryRun := l.dryRun
	execContext := l.execContext
	stderr := l.stderr
	history := l.history
	concurrency := l.concurrency
	l.mu.Unlock()

	var resultOnce sync.Once
	fireResult := func(cycleErr error) {
		resultOnce.Do(func() {
			l.mu.Lock()
			onResult := l.onResult
			l.mu.Unlock()
			if onResult != nil {
				onResult(cycleErr)
			}
		})
	}

	// A panicking Syncer must not leave l.running stuck at true (which would
	// wedge the no-overlap guard for the rest of the process's life), and must
	// invoke OnResult exactly once with the recovered failure.
	defer func() {
		if r := recover(); r != nil {
			l.mu.Lock()
			l.running = false
			l.state = StateError
			fn := l.onChange
			l.mu.Unlock()
			if fn != nil {
				fn(StateError)
			}
			err = fmt.Errorf("syncloop: recovered panic: %v", r)
			fireResult(err)
		}
	}()

	startedAt := time.Now().UTC()
	l.setState(StateSyncing)
	var filters []string
	filters, err = l.ignore()
	var sourceWasNonEmpty *bool
	var sourceObjectCount *int64
	if err == nil && l.mode != "copy" && l.direction != "pull" {
		sourceWasNonEmpty, sourceObjectCount, err = inspectSourceSafety(execContext, l.s, l.path1, filters, stderr)
	}
	var summary engine.ReplicaSummary
	if err == nil {
		transferSpec := engine.TransferSpec{
			Local: l.path1, Mode: l.mode, Direction: l.direction, DryRun: dryRun,
			Filters: filters, Context: execContext, Stderr: stderr,
			SourceWasNonEmpty: sourceWasNonEmpty, SourceObjectCount: sourceObjectCount,
			Replicas: replicas,
		}
		if concurrency.MaxConcurrent > 0 {
			summary, err = engine.ExecuteReplicasConcurrent(l.s, transferSpec, concurrency)
		} else {
			summary, err = engine.ExecuteReplicas(l.s, transferSpec)
		}
	}
	endedAt := time.Now().UTC()
	if history != nil {
		status := engine.CycleOK
		if err != nil {
			if execContext != nil && execContext.Err() != nil {
				status = engine.CycleCancelled
			} else {
				status = engine.CycleFailed
			}
		} else if summary.Status == engine.CycleDegraded {
			status = engine.CycleDegraded
		}
		records := make([]engine.ReplicaRecord, 0, len(summary.Outcomes))
		for _, outcome := range summary.Outcomes {
			rec := engine.ReplicaRecord{ID: outcome.ID, Target: outcome.Target, Required: outcome.Required, Status: outcome.Status}
			if outcome.Err != nil {
				rec.Error = outcome.Err.Error()
			}
			records = append(records, rec)
		}
		var acks []engine.RestoreSetAck
		for _, replica := range replicas {
			acks = append(acks, replica.RestoreAcks...)
		}
		_ = history.Append(engine.CycleRecord{
			RunID:       fmt.Sprintf("run-%s-%s", l.path1, startedAt.Format("20060102T150405.000000000Z")),
			JobID:       l.path1,
			Mode:        l.mode,
			Direction:   l.direction,
			StartedAt:   startedAt,
			EndedAt:     endedAt,
			Status:      status,
			Replicas:    records,
			RestoreAcks: acks,
		})
	}
	l.mu.Lock()
	l.running = false
	l.lastSummary = summary
	switch {
	case err == nil:
		for _, outcome := range summary.Outcomes {
			if outcome.Status == "ok" && l.mode == "bisync" {
				l.baselines[outcome.ID] = true
			}
		}
		if len(l.replicas) > 0 {
			l.hasBaseline = l.baselines[l.replicas[0].ID]
		}
		l.needsResync = false
		l.state = StateIdle
	case errors.Is(err, engine.ErrNeedsResync):
		l.needsResync = true
		l.state = StateNeedsResync
	default:
		l.state = StateError
	}
	if l.paused {
		l.state = StatePaused
	}
	st := l.state
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn(st)
	}
	fireResult(err)
	return err
}

// RunOnce runs exactly one sync cycle - the same mode dispatch and bisync
// resync-if-no-baseline logic as the internal ticker path (Start) - and
// returns its error. It is for one-shot callers (the `sync` CLI command,
// invoked e.g. by a Windows Scheduled Task) that need a single pass with no
// tray and no ticker.
func (l *Loop) RunOnce() error { return l.runOnce() }

// SyncNow schedules a manual cycle without blocking the tray event loop. The
// wait-group increment and the shutdown gate share l.mu so Shutdown cannot
// begin waiting while a newly accepted cycle is still being registered.
// It returns false once shutdown has started and no new manual work is
// accepted.
func (l *Loop) SyncNow() bool {
	l.mu.Lock()
	if l.manualClosed {
		l.mu.Unlock()
		return false
	}
	l.manualWG.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.manualWG.Done()
		_ = l.runOnce()
	}()
	return true
}

func (l *Loop) beginShutdown() {
	l.mu.Lock()
	l.manualClosed = true
	l.mu.Unlock()
}

// Shutdown stops accepting SyncNow requests and waits for every manual cycle
// accepted before the gate closed. The scheduled Start goroutine remains
// owned by its caller's context and wait group.
func (l *Loop) Shutdown() {
	l.beginShutdown()
	l.manualWG.Wait()
}

// ShutdownAll closes every loop's acceptance gate before waiting on any one
// loop. This prevents a blocked manual cycle in an early loop from leaving
// later loops able to accept tray work during daemon teardown.
func ShutdownAll(loops []*Loop) {
	for _, loop := range loops {
		loop.beginShutdown()
	}
	for _, loop := range loops {
		loop.manualWG.Wait()
	}
}

func (l *Loop) Pause() {
	l.mu.Lock()
	l.paused = true
	l.state = StatePaused
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn(StatePaused)
	}
}

func (l *Loop) Resume() {
	l.mu.Lock()
	l.paused = false
	st := StateIdle
	if l.needsResync {
		st = StateNeedsResync
	}
	l.state = st
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

func (l *Loop) Start(ctx context.Context, interval time.Duration) {
	_ = l.runOnce() // #nosec G104 -- runOnce tự chuyển sang StateError và gọi callback khi thất bại.
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = l.runOnce() // #nosec G104 -- runOnce tự chuyển sang StateError và gọi callback khi thất bại.
		}
	}
}
