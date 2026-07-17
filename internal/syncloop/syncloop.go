package syncloop

import (
	"context"
	"errors"
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
}

type IgnoreFunc func() ([]string, error)

type Loop struct {
	s           Syncer
	path1       string
	path2       string
	workdir     string
	ignore      IgnoreFunc
	mu          sync.Mutex
	state       State
	paused      bool
	hasBaseline bool
	running     bool
	onChange    func(State)
}

func New(s Syncer, path1, path2, workdir string, ignore IgnoreFunc) *Loop {
	return &Loop{s: s, path1: path1, path2: path2, workdir: workdir, ignore: ignore, state: StateIdle}
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

func (l *Loop) runOnce() {
	l.mu.Lock()
	if l.paused {
		l.mu.Unlock()
		l.setState(StatePaused)
		return
	}
	if l.running { // no-overlap guard
		l.mu.Unlock()
		return
	}
	l.running = true
	resync := !l.hasBaseline
	l.mu.Unlock()

	l.setState(StateSyncing)
	filters, err := l.ignore()
	if err == nil {
		_, err = l.s.Bisync(engine.BisyncParams{
			Path1: l.path1, Path2: l.path2, Workdir: l.workdir,
			Resync: resync, Filters: filters,
		})
	}

	l.mu.Lock()
	l.running = false
	switch {
	case err == nil:
		l.hasBaseline = true
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
}

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
