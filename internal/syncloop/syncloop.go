package syncloop

import (
	"context"
	"errors"
	"fmt"
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

type Syncer interface {
	Bisync(engine.BisyncParams) (engine.BisyncResult, error)
	Copy(engine.CopyParams) error
	Sync(engine.CopyParams) error
}

type IgnoreFunc func() ([]string, error)

type Loop struct {
	s           Syncer
	path1       string
	path2       string
	workdir     string
	mode        string
	ignore      IgnoreFunc
	mu          sync.Mutex
	state       State
	paused      bool
	hasBaseline bool
	running     bool
	onChange    func(State)
}

// New creates a Loop for the given mode ("bisync", "copy", or "sync"); an
// empty mode defaults to "bisync" for callers that predate mode support (e.g.
// existing tests via newLoop).
func New(s Syncer, path1, path2, workdir, mode string, ignore IgnoreFunc) *Loop {
	if mode == "" {
		mode = "bisync"
	}
	return &Loop{
		s: s, path1: path1, path2: path2, workdir: workdir, mode: mode, ignore: ignore, state: StateIdle,
		hasBaseline: baselineExists(workdir),
	}
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
	resync := l.mode == "bisync" && !l.hasBaseline
	l.mu.Unlock()

	// A panicking Syncer must not leave l.running stuck at true (which would
	// wedge the no-overlap guard for the rest of the process's life).
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
		}
	}()

	l.setState(StateSyncing)
	var filters []string
	filters, err = l.ignore()
	if err == nil {
		switch l.mode {
		case "copy":
			err = l.s.Copy(engine.CopyParams{Local: l.path1, Remote: l.path2, Workdir: l.workdir, Filters: filters})
		case "sync":
			err = l.s.Sync(engine.CopyParams{Local: l.path1, Remote: l.path2, Workdir: l.workdir, Filters: filters})
		default: // "bisync"
			_, err = l.s.Bisync(engine.BisyncParams{
				Path1: l.path1, Path2: l.path2, Workdir: l.workdir,
				Resync: resync, Filters: filters,
			})
		}
	}

	l.mu.Lock()
	l.running = false
	switch {
	case err == nil:
		if l.mode == "bisync" {
			l.hasBaseline = true
		}
		l.state = StateIdle
	case errors.Is(err, engine.ErrNeedsResync):
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
	return err
}

// RunOnce runs exactly one sync cycle - the same mode dispatch and bisync
// resync-if-no-baseline logic as the internal ticker path (Start) - and
// returns its error. It is for one-shot callers (the `sync` CLI command,
// invoked e.g. by a Windows Scheduled Task) that need a single pass with no
// tray and no ticker.
func (l *Loop) RunOnce() error { return l.runOnce() }

func (l *Loop) SyncNow() { go l.runOnce() }

func (l *Loop) Pause() {
	l.mu.Lock()
	l.paused = true
	l.mu.Unlock()
	l.setState(StatePaused)
}

func (l *Loop) Resume() {
	l.mu.Lock()
	l.paused = false
	l.mu.Unlock()
	l.setState(StateIdle)
}

func (l *Loop) Start(ctx context.Context, interval time.Duration) {
	l.runOnce()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.runOnce()
		}
	}
}
