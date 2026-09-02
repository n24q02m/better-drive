package runlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const HistorySchemaVersion = 1

var validCycleStatus = map[string]struct{}{
	"ok":               {},
	"degraded":         {},
	"failed":           {},
	"skipped_optional": {},
	"unknown":          {},
}

// CycleRecord is the versioned persisted evidence for one daemon cycle/run.
// It mirrors state.RunRecord generically so a later sync adapter can conform
// without importing engine/syncloop. Keep it boring and explicit.
type CycleRecord struct {
	SchemaVersion int       `json:"schema_version"`
	CycleID       string    `json:"cycle_id"`
	RunID         string    `json:"run_id"`
	JobID         string    `json:"job_id"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Status        string    `json:"status"`
	ObjectCount   int64     `json:"object_count"`
	ByteCount     int64     `json:"byte_count"`
	Warnings      []string  `json:"warnings,omitempty"`
	EngineVersion string    `json:"engine_version"`
	// Scheduler evidence for fail-closed health.
	SchedulerOwner      string        `json:"scheduler_owner"`
	SchedulerJobID      string        `json:"scheduler_job_id"`
	SchedulerHealth     string        `json:"scheduler_health"`
	SchedulerObservedAt time.Time     `json:"scheduler_observed_at"`
	FreshnessWindow     time.Duration `json:"freshness_window"`
	CatchUpGrace        time.Duration `json:"catch_up_grace"`
}

// CycleStore appends one immutable versioned cycle record atomically and
// loads bounded records/current per job. No global singleton; path/clock are injected.
type CycleStore struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewCycleStore returns a store for path. now may be nil.
func NewCycleStore(path string, now func() time.Time) *CycleStore {
	if now == nil {
		now = time.Now
	}
	return &CycleStore{path: path, now: now}
}

func validateCycleRecord(r CycleRecord) error {
	if r.SchemaVersion != HistorySchemaVersion {
		return fmt.Errorf("cycle history schema_version must be %d, got %d", HistorySchemaVersion, r.SchemaVersion)
	}
	if strings.TrimSpace(r.CycleID) == "" {
		return fmt.Errorf("cycle history cycle_id is required")
	}
	if strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("cycle history run_id is required")
	}
	if strings.TrimSpace(r.JobID) == "" {
		return fmt.Errorf("cycle history job_id is required")
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("cycle history started_at is required")
	}
	if r.FinishedAt.IsZero() {
		return fmt.Errorf("cycle history finished_at is required")
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("cycle history finished_at must not be before started_at")
	}
	if _, ok := validCycleStatus[r.Status]; !ok {
		return fmt.Errorf("cycle history status %q is unknown", r.Status)
	}
	if r.ObjectCount < 0 || r.ByteCount < 0 {
		return fmt.Errorf("cycle history counts must be >= 0")
	}
	if strings.TrimSpace(r.EngineVersion) == "" {
		return fmt.Errorf("cycle history engine_version is required")
	}
	if r.FreshnessWindow <= 0 {
		return fmt.Errorf("cycle history freshness_window must be > 0")
	}
	if r.CatchUpGrace <= 0 {
		return fmt.Errorf("cycle history catch_up_grace must be > 0")
	}
	if strings.TrimSpace(r.SchedulerHealth) == "" {
		return fmt.Errorf("cycle history scheduler_health is required")
	}
	// fail-closed: if observed_at is stale, health cannot be healthy
	if !r.SchedulerObservedAt.IsZero() && r.FreshnessWindow > 0 {
		if r.FinishedAt.Sub(r.SchedulerObservedAt) > r.FreshnessWindow && r.SchedulerHealth == "healthy" {
			return fmt.Errorf("cycle history scheduler health %q is stale", r.SchedulerHealth)
		}
	}
	return nil
}

func cycleRecordsEqual(a, b CycleRecord) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func (s *CycleStore) readAll() ([]CycleRecord, [][]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("open cycle history: %w", err)
	}
	if len(data) == 0 {
		return nil, nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, nil, fmt.Errorf("corrupt cycle history: truncated last line")
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var records []CycleRecord
	var raws [][]byte
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var rec CycleRecord
		if err := json.Unmarshal(trimmed, &rec); err != nil {
			return nil, nil, fmt.Errorf("corrupt cycle history: decode: %w", err)
		}
		if rec.SchemaVersion != HistorySchemaVersion {
			return nil, nil, fmt.Errorf("unsupported cycle history schema_version %d", rec.SchemaVersion)
		}
		if err := validateCycleRecord(rec); err != nil {
			return nil, nil, fmt.Errorf("corrupt cycle history: %w", err)
		}
		records = append(records, rec)
		raw := make([]byte, len(trimmed)+1)
		copy(raw, trimmed)
		raw[len(trimmed)] = '\n'
		raws = append(raws, raw)
	}
	return records, raws, nil
}

// Append adds one record atomically. Duplicate cycle_id with different bytes is rejected.
func (s *CycleStore) Append(rec CycleRecord) error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("cycle store path is required")
	}
	if err := validateCycleRecord(rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create cycle history directory: %w", err)
	}
	records, raws, err := s.readAll()
	if err != nil {
		return err
	}
	for _, existing := range records {
		if existing.CycleID == rec.CycleID {
			if cycleRecordsEqual(existing, rec) {
				return nil
			}
			return fmt.Errorf("duplicate cycle_id %q with different bytes", rec.CycleID)
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode cycle record: %w", err)
	}
	line = append(line, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".cycle-*.tmp")
	if err != nil {
		return fmt.Errorf("create cycle temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod cycle temp: %w", err)
	}
	for _, raw := range raws {
		if _, err := tmp.Write(raw); err != nil {
			return fmt.Errorf("write cycle temp: %w", err)
		}
	}
	if _, err := tmp.Write(line); err != nil {
		return fmt.Errorf("write cycle temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync cycle temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cycle temp: %w", err)
	}
	cleanup = func() { _ = os.Remove(tmpPath) }
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace cycle history: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("chmod cycle history: %w", err)
	}
	return nil
}

// Load returns up to limit records for jobID, most recent first.
func (s *CycleStore) Load(jobID string, limit int) ([]CycleRecord, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("cycle job_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var filtered []CycleRecord
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].JobID == jobID {
			filtered = append(filtered, records[i])
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}
	return filtered, nil
}

// LoadCurrent returns the most recent record for jobID.
func (s *CycleStore) LoadCurrent(jobID string) (*CycleRecord, error) {
	got, err := s.Load(jobID, 1)
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, nil
	}
	rec := got[0]
	return &rec, nil
}

// LoadAll returns up to limit records across all jobs, most recent first.
func (s *CycleStore) LoadAll(limit int) ([]CycleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit >= len(records) {
		out := make([]CycleRecord, len(records))
		for i := range records {
			out[i] = records[len(records)-1-i]
		}
		return out, nil
	}
	out := make([]CycleRecord, 0, limit)
	for i := len(records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, records[i])
	}
	return out, nil
}
