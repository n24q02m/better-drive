package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const CurrentSchemaVersion = 1

const (
	HealthHealthy             = "healthy"
	HealthStale               = "stale"
	HealthMissing             = "missing"
	HealthDisabled            = "disabled"
	HealthOwnerMismatch       = "owner_mismatch"
	HealthOverlap             = "overlap"
	HealthNeedsReconciliation = "needs_reconciliation"
	HealthUnknown             = "unknown"

	OverlapNone           = "none"
	OverlapSingleActive   = "single_active"
	OverlapMultipleActive = "multiple_active"
	OverlapUnknown        = "unknown"
)

var validHealth = map[string]struct{}{
	HealthHealthy:             {},
	HealthStale:               {},
	HealthMissing:             {},
	HealthDisabled:            {},
	HealthOwnerMismatch:       {},
	HealthOverlap:             {},
	HealthNeedsReconciliation: {},
	HealthUnknown:             {},
}

var validOverlap = map[string]struct{}{
	OverlapNone:           {},
	OverlapSingleActive:   {},
	OverlapMultipleActive: {},
	OverlapUnknown:        {},
}

type State struct {
	SchemaVersion int            `json:"schema_version"`
	EngineVersion string         `json:"engine_version"`
	Jobs          []JobState     `json:"jobs"`
	Scheduler     SchedulerState `json:"scheduler"`
}

type JobState struct {
	JobID           string         `json:"job_id"`
	Status          string         `json:"status"`
	LastSuccess     time.Time      `json:"last_success,omitempty"`
	NextDue         time.Time      `json:"next_due,omitempty"`
	ObjectCount     int64          `json:"object_count"`
	ByteCount       int64          `json:"byte_count"`
	Warnings        []string       `json:"warnings,omitempty"`
	ReplicaOutcomes []ReplicaState `json:"replica_outcomes,omitempty"`
}

type ReplicaState struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Required bool   `json:"required"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type SchedulerState struct {
	Owner           string        `json:"owner"`
	OwnerJobID      string        `json:"owner_job_id"`
	Enabled         bool          `json:"enabled"`
	LastTrigger     time.Time     `json:"last_trigger,omitempty"`
	NextTrigger     time.Time     `json:"next_trigger,omitempty"`
	ActiveInstance  string        `json:"active_instance"`
	OverlapState    string        `json:"overlap_state"`
	OverlapHealth   string        `json:"overlap_health"`
	ObservedAt      time.Time     `json:"observed_at"`
	FreshnessWindow time.Duration `json:"freshness_window"`
	CatchUpGrace    time.Duration `json:"catch_up_grace"`
	Health          string        `json:"health"`
}

func (s State) Validate() error {
	if s.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("state schema_version must be %d, got %d", CurrentSchemaVersion, s.SchemaVersion)
	}
	if strings.TrimSpace(s.EngineVersion) == "" {
		return fmt.Errorf("state engine_version is required")
	}
	for i, job := range s.Jobs {
		if strings.TrimSpace(job.JobID) == "" {
			return fmt.Errorf("job state %d: job_id is required", i)
		}
		if job.Status != "ok" && job.Status != "degraded" && job.Status != "failed" && job.Status != "skipped_optional" {
			return fmt.Errorf("job state %q: unknown status %q", job.JobID, job.Status)
		}
	}
	if s.Scheduler.FreshnessWindow <= 0 {
		return fmt.Errorf("scheduler freshness_window must be > 0")
	}
	if s.Scheduler.CatchUpGrace <= 0 {
		return fmt.Errorf("scheduler catch_up_grace must be > 0")
	}
	if _, ok := validHealth[s.Scheduler.Health]; !ok {
		return fmt.Errorf("scheduler health %q is unknown", s.Scheduler.Health)
	}
	if s.Scheduler.Health == HealthHealthy {
		ownerJobID := s.Scheduler.OwnerJobID
		matched := false
		for _, job := range s.Jobs {
			if job.JobID == ownerJobID {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("scheduler healthy owner_job_id %q does not match any job state", ownerJobID)
		}
	}
	if s.Scheduler.OverlapState != "" {
		if _, ok := validOverlap[s.Scheduler.OverlapState]; !ok {
			return fmt.Errorf("scheduler overlap_state %q is invalid", s.Scheduler.OverlapState)
		}
	}
	return nil
}

// migrateLegacyTokens translates schema-v1 scheduler values into the
// canonical wire enums while leaving all timestamps, ownership, and overlap
// evidence untouched. Schema version 1 remains the on-disk schema; only the
// token vocabulary changed.
func migrateLegacyTokens(s *State) {
	if s == nil {
		return
	}
	s.Scheduler.Health = canonicalHealthToken(s.Scheduler.Health)
	s.Scheduler.OverlapState = canonicalOverlapToken(s.Scheduler.OverlapState)
	s.Scheduler.OverlapHealth = canonicalHealthEvidence(s.Scheduler.OverlapHealth)
}

func canonicalHealthToken(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", HealthUnknown:
		if value == "" {
			return value
		}
		return HealthUnknown
	case HealthHealthy:
		return HealthHealthy
	case HealthStale, "catch-up-beyond-grace":
		return HealthStale
	case HealthMissing, "missing-owner":
		return HealthMissing
	case HealthOwnerMismatch, "owner-mismatch":
		return HealthOwnerMismatch
	case HealthDisabled:
		return HealthDisabled
	case HealthOverlap, "active-overlap", "multiple-active":
		return HealthOverlap
	case HealthNeedsReconciliation, "readback-mismatch":
		return HealthNeedsReconciliation
	case "unknown-overlap":
		return HealthUnknown
	case "catch-up-within-grace":
		return HealthHealthy
	default:
		return value
	}
}

func canonicalHealthEvidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "owner-mismatch", "mismatch":
		return HealthOwnerMismatch
	case "readback-mismatch", "needs-reconciliation":
		return HealthNeedsReconciliation
	case "active-overlap", "multiple-active":
		return HealthOverlap
	case "unknown-overlap":
		return HealthUnknown
	default:
		return value
	}
}

func canonicalOverlapToken(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return OverlapNone
	case OverlapNone:
		return OverlapNone
	case OverlapSingleActive, "active", "single":
		return OverlapSingleActive
	case OverlapMultipleActive, "multiple", "overlap":
		return OverlapMultipleActive
	case OverlapUnknown, "unknown-overlap":
		return OverlapUnknown
	default:
		return value
	}
}

// NormalizeOverlapState maps legacy or alternate overlap labels to canonical
// none|single_active|multiple_active|unknown tokens.
func NormalizeOverlapState(value string) string {
	return canonicalOverlapToken(value)
}

// EvaluateSchedulerHealth returns the exact canonical enum value based on
// live evidence, timestamps, ownership, and catch-up bounds.
func EvaluateSchedulerHealth(s SchedulerState, now time.Time) string {
	now = now.UTC()
	if !s.Enabled {
		return HealthDisabled
	}
	if strings.TrimSpace(s.Owner) == "" || strings.TrimSpace(s.OwnerJobID) == "" {
		return HealthMissing
	}
	switch strings.ToLower(strings.TrimSpace(s.OverlapHealth)) {
	case "owner_mismatch", "owner-mismatch", "mismatch":
		return HealthOwnerMismatch
	case "needs_reconciliation", "needs-reconciliation", "readback_mismatch", "readback-mismatch":
		return HealthNeedsReconciliation
	case "unknown", "invalid":
		return HealthUnknown
	}
	overlap := NormalizeOverlapState(s.OverlapState)
	if _, ok := validOverlap[overlap]; !ok {
		return HealthUnknown
	}
	if overlap == OverlapMultipleActive || strings.EqualFold(s.OverlapHealth, "overlap") {
		return HealthOverlap
	}
	if overlap == OverlapUnknown {
		return HealthUnknown
	}
	if s.ObservedAt.IsZero() {
		return HealthMissing
	}
	observed := s.ObservedAt.UTC()
	if observed.After(now.Add(time.Minute)) {
		return HealthNeedsReconciliation
	}
	if s.FreshnessWindow <= 0 || now.Sub(observed) > s.FreshnessWindow {
		return HealthStale
	}
	if !s.LastTrigger.IsZero() && s.LastTrigger.UTC().After(now.Add(time.Minute)) {
		return HealthNeedsReconciliation
	}
	if !s.NextTrigger.IsZero() {
		next := s.NextTrigger.UTC()
		if now.After(next) {
			if s.CatchUpGrace <= 0 || now.Sub(next) > s.CatchUpGrace {
				return HealthStale
			}
		}
	}
	return HealthHealthy
}
func Save(path string, state State) error {
	migrateLegacyTokens(&state)
	if err := state.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create state temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod state temp: %w", err)
	}
	if err := json.NewEncoder(tmp).Encode(state); err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync state temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func Load(path string) (*State, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	defer file.Close()
	var state State
	if err := json.NewDecoder(file).Decode(&state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	migrateLegacyTokens(&state)
	if err := state.Validate(); err != nil {
		return nil, err
	}
	return &state, nil
}
