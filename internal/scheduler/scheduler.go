package scheduler

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/state"
)

type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
	PlatformDarwin  Platform = "darwin"
)

type Definition struct {
	JobID                 string   `json:"job_id"`
	Executable            string   `json:"executable"`
	Config                string   `json:"config"`
	Arguments             []string `json:"arguments,omitempty"`
	IntervalSeconds       int      `json:"interval_seconds"`
	CatchUp               bool     `json:"catch_up"`
	ExecutionLimitSeconds int      `json:"execution_limit_seconds"`
	Owner                 string   `json:"owner"`
	Generation            uint64   `json:"generation,omitempty"`
}

type OwnerRecord struct {
	Owner      string `json:"owner"`
	JobID      string `json:"job_id"`
	Generation uint64 `json:"generation,omitempty"`
}

type Readback struct {
	Platform       Platform    `json:"platform"`
	Owner          OwnerRecord `json:"owner"`
	Installed      bool        `json:"installed"`
	Enabled        bool        `json:"enabled"`
	ActiveInstance string      `json:"active_instance"`
	OverlapState   string      `json:"overlap_state"`
	OverlapHealth  string      `json:"overlap_health"`
	LastTrigger    time.Time   `json:"last_trigger,omitempty"`
	NextTrigger    time.Time   `json:"next_trigger,omitempty"`
	ObservedAt     time.Time   `json:"observed_at"`
	Health         string      `json:"health"`
	Definition     *Definition `json:"definition,omitempty"`
	RawOutput      string      `json:"raw_output,omitempty"`
	Warnings       []string    `json:"warnings,omitempty"`
}

var (
	ErrOwnerMismatch      = errors.New("scheduler owner mismatch")
	ErrOverlapConflict    = errors.New("scheduler overlap detected")
	ErrMutationDisabled   = errors.New("live scheduler mutation is disabled")
	ErrReconciliationGate = errors.New("scheduler requires reconciliation before mutation")
)

type Adapter interface {
	Platform() Platform
	Install(ctx context.Context, def Definition, replace bool) error
	Readback(ctx context.Context, jobID string) (Readback, error)
	Remove(ctx context.Context, jobID string, force bool) error
}

// MemoryAdapter is a test/fake in-memory adapter that enforces exact owner,
// job identity, generation sequencing, and single-active overlap constraints.
type MemoryAdapter struct {
	mu           sync.Mutex
	platform     Platform
	now          func() time.Time
	definitions  map[string]Definition
	generations  map[string]uint64
	activeJobs   map[string]string
	overlapState map[string]string
	enabled      map[string]bool
	lastTrigger  map[string]time.Time
	nextTrigger  map[string]time.Time
	reconcile    map[string]bool
}

func NewMemoryAdapter(platform Platform, now func() time.Time) *MemoryAdapter {
	if now == nil {
		now = time.Now
	}
	return &MemoryAdapter{
		platform:     platform,
		now:          now,
		definitions:  make(map[string]Definition),
		generations:  make(map[string]uint64),
		activeJobs:   make(map[string]string),
		overlapState: make(map[string]string),
		enabled:      make(map[string]bool),
		lastTrigger:  make(map[string]time.Time),
		nextTrigger:  make(map[string]time.Time),
		reconcile:    make(map[string]bool),
	}
}

func (m *MemoryAdapter) Platform() Platform { return m.platform }

func (m *MemoryAdapter) Install(ctx context.Context, def Definition, replace bool) error {
	if err := def.Validate(); err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	jobKey := def.JobID
	if m.reconcile[jobKey] && !replace {
		return fmt.Errorf("%w: job %q is locked in reconciliation", ErrReconciliationGate, jobKey)
	}

	existing, exists := m.definitions[jobKey]
	currentRecord := OwnerRecord{}
	if exists {
		currentRecord = OwnerRecord{Owner: existing.Owner, JobID: existing.JobID, Generation: m.generations[jobKey]}
	}
	if err := ValidateOwner(currentRecord, def, replace); err != nil {
		return err
	}
	if m.activeJobs[jobKey] != "" && !replace {
		return fmt.Errorf("%w: job %q currently has active instance %q", ErrOverlapConflict, jobKey, m.activeJobs[jobKey])
	}

	nextGen := m.generations[jobKey] + 1
	if def.Generation > 0 {
		if def.Generation <= m.generations[jobKey] {
			return fmt.Errorf("scheduler generation %d must be > current %d", def.Generation, m.generations[jobKey])
		}
		nextGen = def.Generation
	}
	def.Generation = nextGen
	m.definitions[jobKey] = def
	m.generations[jobKey] = nextGen
	m.enabled[jobKey] = true
	m.overlapState[jobKey] = state.OverlapNone
	m.reconcile[jobKey] = false
	now := m.now().UTC()
	m.lastTrigger[jobKey] = now
	m.nextTrigger[jobKey] = now.Add(time.Duration(def.IntervalSeconds) * time.Second)
	return nil
}

func (m *MemoryAdapter) Readback(ctx context.Context, jobID string) (Readback, error) {
	if ctx != nil && ctx.Err() != nil {
		return Readback{}, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	def, exists := m.definitions[jobID]
	if !exists {
		return Readback{
			Platform:   m.platform,
			Installed:  false,
			Enabled:    false,
			ObservedAt: now,
			Health:     state.HealthMissing,
		}, nil
	}

	owner := OwnerRecord{Owner: def.Owner, JobID: def.JobID, Generation: m.generations[jobID]}
	active := m.activeJobs[jobID]
	overlap := m.overlapState[jobID]
	if overlap == "" {
		overlap = state.OverlapNone
		if active != "" {
			overlap = state.OverlapSingleActive
		}
	}
	overlapHealth := "ok"
	if m.reconcile[jobID] {
		overlapHealth = "needs_reconciliation"
	} else if overlap == state.OverlapMultipleActive {
		overlapHealth = "overlap"
	}

	schedState := state.SchedulerState{
		Owner:           owner.Owner,
		OwnerJobID:      owner.JobID,
		Enabled:         m.enabled[jobID],
		LastTrigger:     m.lastTrigger[jobID],
		NextTrigger:     m.nextTrigger[jobID],
		ActiveInstance:  active,
		OverlapState:    overlap,
		OverlapHealth:   overlapHealth,
		ObservedAt:      now,
		FreshnessWindow: 15 * time.Minute,
		CatchUpGrace:    6*time.Hour + 15*time.Minute,
	}
	health := state.EvaluateSchedulerHealth(schedState, now)
	schedState.Health = health

	defCopy := def
	return Readback{
		Platform:       m.platform,
		Owner:          owner,
		Installed:      true,
		Enabled:        schedState.Enabled,
		ActiveInstance: active,
		OverlapState:   overlap,
		OverlapHealth:  overlapHealth,
		LastTrigger:    schedState.LastTrigger,
		NextTrigger:    schedState.NextTrigger,
		ObservedAt:     now,
		Health:         health,
		Definition:     &defCopy,
	}, nil
}

func (m *MemoryAdapter) Remove(ctx context.Context, jobID string, force bool) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.definitions[jobID]; !exists {
		return nil
	}
	if m.activeJobs[jobID] != "" && !force {
		return fmt.Errorf("%w: cannot remove job %q with active instance %q without force", ErrOverlapConflict, jobID, m.activeJobs[jobID])
	}
	delete(m.definitions, jobID)
	delete(m.generations, jobID)
	delete(m.activeJobs, jobID)
	delete(m.overlapState, jobID)
	delete(m.enabled, jobID)
	delete(m.lastTrigger, jobID)
	delete(m.nextTrigger, jobID)
	delete(m.reconcile, jobID)
	return nil
}

func (m *MemoryAdapter) SetActiveInstance(jobID, instance string, overlapState string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeJobs[jobID] = instance
	if overlapState != "" {
		m.overlapState[jobID] = overlapState
	} else if instance != "" {
		m.overlapState[jobID] = state.OverlapSingleActive
	} else {
		m.overlapState[jobID] = state.OverlapNone
	}
}

func (m *MemoryAdapter) SetReconciliation(jobID string, flag bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcile[jobID] = flag
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.JobID) == "" || strings.TrimSpace(d.Executable) == "" || strings.TrimSpace(d.Config) == "" {
		return fmt.Errorf("scheduler definition requires job_id, executable, and config")
	}
	if !isAbsolutePath(d.Executable) {
		return fmt.Errorf("scheduler definition requires absolute executable")
	}
	if !isAbsolutePath(d.Config) {
		return fmt.Errorf("scheduler definition requires absolute config")
	}
	if d.IntervalSeconds <= 0 || d.ExecutionLimitSeconds <= 0 {
		return fmt.Errorf("scheduler interval and execution limit must be > 0")
	}
	if strings.TrimSpace(d.Owner) == "" {
		return fmt.Errorf("scheduler owner is required")
	}
	return nil
}

func Render(platform Platform, definition Definition) ([]byte, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	switch platform {
	case PlatformWindows:
		return renderWindows(definition), nil
	case PlatformLinux:
		return renderLinux(definition), nil
	case PlatformDarwin:
		return renderDarwin(definition), nil
	default:
		return nil, fmt.Errorf("unsupported scheduler platform %q", platform)
	}
}

func ValidateOwner(current OwnerRecord, desired Definition, replace bool) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if current.Owner == "" && current.JobID == "" {
		return nil
	}
	if current.Owner == desired.Owner && (current.JobID == "" || current.JobID == desired.JobID) {
		return nil
	}
	if !replace {
		return fmt.Errorf("%w: scheduler owner %q/%q differs from desired %q/%q; use --replace", ErrOwnerMismatch, current.Owner, current.JobID, desired.Owner, desired.JobID)
	}
	return nil
}

func isAbsolutePath(value string) bool {
	if filepath.IsAbs(value) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func commandLine(d Definition) string {
	parts := []string{quote(d.Executable)}
	for _, arg := range schedulerArguments(d) {
		parts = append(parts, quote(arg))
	}
	return strings.Join(parts, " ")
}

func schedulerArguments(d Definition) []string {
	args := append([]string(nil), d.Arguments...)
	if len(args) == 0 {
		return []string{"sync", "--format", "json", "--config", d.Config}
	}
	if !containsArgument(args, "sync") {
		args = append([]string{"sync"}, args...)
	}
	if !containsFlagValue(args, "--format") {
		args = append(args, "--format", "json")
	}
	if !containsFlagValue(args, "--config") {
		args = append(args, "--config", d.Config)
	}
	return args
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsFlagValue(args []string, flag string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" && !strings.HasPrefix(args[i+1], "-") {
			return true
		}
		if strings.HasPrefix(arg, flag+"=") && strings.TrimSpace(strings.TrimPrefix(arg, flag+"=")) != "" {
			return true
		}
	}
	return false
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func quoteArgs(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quote(value))
	}
	return quoted
}

func formatInterval(seconds int) string {
	if seconds%(60*60) == 0 {
		return fmt.Sprintf("%dh", seconds/(60*60))
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
