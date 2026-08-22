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
	HealthHealthy            = "healthy"
	HealthStale              = "stale"
	HealthMissingOwner       = "missing-owner"
	HealthOwnerMismatch      = "owner-mismatch"
	HealthDisabled           = "disabled"
	HealthActiveOverlap      = "active-overlap"
	HealthMultipleActive     = "multiple-active"
	HealthUnknownOverlap     = "unknown-overlap"
	HealthCatchUpWithinGrace = "catch-up-within-grace"
	HealthCatchUpBeyondGrace = "catch-up-beyond-grace"
	HealthReadbackMismatch   = "readback-mismatch"
)

var validHealth = map[string]struct{}{
	HealthHealthy: {}, HealthStale: {}, HealthMissingOwner: {}, HealthOwnerMismatch: {},
	HealthDisabled: {}, HealthActiveOverlap: {}, HealthMultipleActive: {}, HealthUnknownOverlap: {},
	HealthCatchUpWithinGrace: {}, HealthCatchUpBeyondGrace: {}, HealthReadbackMismatch: {},
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
	return nil
}

func EvaluateSchedulerHealth(s SchedulerState, now time.Time) string {
	if !s.Enabled {
		return HealthDisabled
	}
	if strings.TrimSpace(s.Owner) == "" || strings.TrimSpace(s.OwnerJobID) == "" {
		return HealthMissingOwner
	}
	if s.OverlapHealth == "readback-mismatch" {
		return HealthReadbackMismatch
	}
	switch s.OverlapState {
	case "active":
		return HealthActiveOverlap
	case "multiple":
		return HealthMultipleActive
	case "unknown":
		return HealthUnknownOverlap
	}
	if s.ObservedAt.IsZero() || now.Sub(s.ObservedAt) > s.FreshnessWindow {
		return HealthStale
	}
	if !s.NextTrigger.IsZero() && now.After(s.NextTrigger) {
		if now.Sub(s.NextTrigger) > s.CatchUpGrace {
			return HealthCatchUpBeyondGrace
		}
		return HealthCatchUpWithinGrace
	}
	return HealthHealthy
}

func Save(path string, state State) error {
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
	if err := state.Validate(); err != nil {
		return nil, err
	}
	return &state, nil
}
