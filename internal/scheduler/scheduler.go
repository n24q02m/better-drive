package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
	RunCount       uint64      `json:"run_count,omitempty"`
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

const definitionMetadataPrefix = "better-drive-definition-v1:"

type Adapter interface {
	Platform() Platform
	Install(ctx context.Context, def Definition, replace bool) error
	Readback(ctx context.Context, jobID string) (Readback, error)
	SetEnabled(ctx context.Context, jobID string, enabled bool) error
	Run(ctx context.Context, jobID string) error
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
	m.enabled[jobKey] = false
	m.overlapState[jobKey] = state.OverlapNone
	m.reconcile[jobKey] = false
	m.lastTrigger[jobKey] = time.Time{}
	m.nextTrigger[jobKey] = time.Time{}
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

func (m *MemoryAdapter) SetEnabled(ctx context.Context, jobID string, enabled bool) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	definition, exists := m.definitions[jobID]
	if !exists {
		return fmt.Errorf("scheduler job %q is not installed", jobID)
	}
	m.enabled[jobID] = enabled
	if enabled {
		m.nextTrigger[jobID] = m.now().UTC().Add(time.Duration(definition.IntervalSeconds) * time.Second)
	} else {
		m.nextTrigger[jobID] = time.Time{}
		m.activeJobs[jobID] = ""
		m.overlapState[jobID] = state.OverlapNone
	}
	return nil
}

func (m *MemoryAdapter) Run(ctx context.Context, jobID string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	definition, exists := m.definitions[jobID]
	if !exists {
		return fmt.Errorf("scheduler job %q is not installed", jobID)
	}
	if !m.enabled[jobID] {
		return fmt.Errorf("scheduler job %q is disabled", jobID)
	}
	if m.activeJobs[jobID] != "" {
		return fmt.Errorf("%w: job %q currently has active instance %q", ErrOverlapConflict, jobID, m.activeJobs[jobID])
	}
	now := m.now().UTC()
	m.lastTrigger[jobID] = now
	m.nextTrigger[jobID] = now.Add(time.Duration(definition.IntervalSeconds) * time.Second)
	return nil
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
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "job_id", value: d.JobID},
		{name: "executable", value: d.Executable},
		{name: "config", value: d.Config},
		{name: "owner", value: d.Owner},
	} {
		if strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("scheduler %s contains control characters", field.name)
		}
	}
	for _, argument := range d.Arguments {
		if strings.IndexFunc(argument, unicode.IsControl) >= 0 {
			return fmt.Errorf("scheduler argument contains control characters")
		}
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
	parts := []string{quoteSystemd(d.Executable)}
	for _, arg := range schedulerArguments(d) {
		parts = append(parts, quoteSystemd(arg))
	}
	return strings.Join(parts, " ")
}

func schedulerArguments(d Definition) []string {
	extras := make([]string, 0, len(d.Arguments))
	for index := 0; index < len(d.Arguments); index++ {
		arg := d.Arguments[index]
		if arg == "sync" {
			continue
		}
		managed := arg == "--job" || arg == "--format" || arg == "--config"
		if managed {
			if index+1 < len(d.Arguments) && !strings.HasPrefix(d.Arguments[index+1], "-") {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "--job=") ||
			strings.HasPrefix(arg, "--format=") ||
			strings.HasPrefix(arg, "--config=") {
			continue
		}
		extras = append(extras, arg)
	}

	args := make([]string, 0, 7+len(extras))
	args = append(args, "sync", "--job", d.JobID, "--format", "json", "--config", d.Config)
	return append(args, extras...)
}

// ManagedName maps an arbitrary job ID to the fixed native scheduler identity
// used on every supported platform. Job IDs never enter native selectors.
func ManagedName(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	return "better-drive-" + hex.EncodeToString(sum[:12])
}

func darwinLabel(jobID string) string {
	return "com.better-drive.LaunchAgent." + ManagedName(jobID)
}
func definitionMetadata(d Definition) string {
	payload, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("marshal scheduler definition: %v", err))
	}
	return definitionMetadataPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func definitionFromMetadata(value string) (Definition, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(value), definitionMetadataPrefix)
	if !ok {
		return Definition{}, errors.New("native scheduler definition is not owned by better-drive")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Definition{}, fmt.Errorf("decode scheduler definition metadata: %w", err)
	}
	var definition Definition
	if err := json.Unmarshal(payload, &definition); err != nil {
		return Definition{}, fmt.Errorf("parse scheduler definition metadata: %w", err)
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, fmt.Errorf("validate scheduler definition metadata: %w", err)
	}
	return definition, nil
}

func nextDefinition(current Readback, desired Definition, replace bool) (Definition, error) {
	if err := desired.Validate(); err != nil {
		return Definition{}, err
	}
	currentGeneration := uint64(0)
	if current.Installed {
		if strings.TrimSpace(current.Owner.Owner) == "" || strings.TrimSpace(current.Owner.JobID) == "" {
			return Definition{}, fmt.Errorf("%w: native scheduler identity has no recoverable better-drive owner metadata", ErrOwnerMismatch)
		} else if err := ValidateOwner(current.Owner, desired, replace); err != nil {
			return Definition{}, err
		}
		currentGeneration = current.Owner.Generation
	}
	if desired.Generation == 0 {
		desired.Generation = currentGeneration + 1
	} else if desired.Generation <= currentGeneration {
		return Definition{}, fmt.Errorf("scheduler generation %d must be > current %d", desired.Generation, currentGeneration)
	}
	return desired, nil
}

// MatchesDefinition verifies the semantic native readback after a mutation.
// Generation is assigned by the adapter and therefore checked for presence,
// not equality with a zero-valued desired generation.
func MatchesDefinition(readback Readback, desired Definition) bool {
	if !readback.Installed || readback.Definition == nil {
		return false
	}
	actual := *readback.Definition
	if actual.Generation == 0 || readback.Owner.Generation != actual.Generation {
		return false
	}
	desired.Generation = actual.Generation
	return actual.JobID == desired.JobID &&
		actual.Executable == desired.Executable &&
		actual.Config == desired.Config &&
		slices.Equal(actual.Arguments, desired.Arguments) &&
		actual.IntervalSeconds == desired.IntervalSeconds &&
		actual.CatchUp == desired.CatchUp &&
		actual.ExecutionLimitSeconds == desired.ExecutionLimitSeconds &&
		actual.Owner == desired.Owner &&
		readback.Owner.Owner == desired.Owner &&
		readback.Owner.JobID == desired.JobID
}

func quoteSystemd(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + replacer.Replace(value) + `"`
}

func quoteWindows(value string) string {
	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')
	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			quoted.WriteString(strings.Repeat(`\`, backslashes*2+1))
			quoted.WriteRune(character)
			backslashes = 0
			continue
		}
		quoted.WriteString(strings.Repeat(`\`, backslashes))
		backslashes = 0
		quoted.WriteRune(character)
	}
	quoted.WriteString(strings.Repeat(`\`, backslashes*2))
	quoted.WriteByte('"')
	return quoted.String()
}

func quoteArgs(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteWindows(value))
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
