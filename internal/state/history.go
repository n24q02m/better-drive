package state

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

var validRunStatus = map[string]struct{}{
	"ok":               {},
	"degraded":         {},
	"failed":           {},
	"skipped_optional": {},
	"unknown":          {},
}

// RunRecord is the immutable versioned evidence for one job run/cycle.
// It captures run_id/job_id/started/finished/status/object+byte counts/warnings/engine_version
// plus replica outcomes, restore_set acknowledgements and scheduler evidence.
type RunRecord struct {
	SchemaVersion       int            `json:"schema_version"`
	RunID               string         `json:"run_id"`
	JobID               string         `json:"job_id"`
	StartedAt           time.Time      `json:"started_at"`
	FinishedAt          time.Time      `json:"finished_at"`
	Status              string         `json:"status"`
	ObjectCount         int64          `json:"object_count"`
	ByteCount           int64          `json:"byte_count"`
	Warnings            []string       `json:"warnings,omitempty"`
	EngineVersion       string         `json:"engine_version"`
	ReplicaOutcomes     []ReplicaState `json:"replica_outcomes,omitempty"`
	RestoreSetID        string         `json:"restore_set_id,omitempty"`
	RestoreAcknowledged bool           `json:"restore_acknowledged,omitempty"`
	Scheduler           SchedulerState `json:"scheduler"`
	CycleID             string         `json:"cycle_id,omitempty"`
}

// HistoryStore appends one immutable versioned run record atomically and
// loads bounded records/current per job. Constructor gets explicit path/clock;
// no global singleton.
type HistoryStore struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewHistoryStore returns a store for path. now may be nil to use time.Now.
func NewHistoryStore(path string, now func() time.Time) *HistoryStore {
	if now == nil {
		now = time.Now
	}
	return &HistoryStore{path: path, now: now}
}

func validateRunRecord(r RunRecord) error {
	if r.SchemaVersion != HistorySchemaVersion {
		return fmt.Errorf("history schema_version must be %d, got %d", HistorySchemaVersion, r.SchemaVersion)
	}
	if strings.TrimSpace(r.RunID) == "" {
		return fmt.Errorf("history run_id is required")
	}
	if strings.TrimSpace(r.JobID) == "" {
		return fmt.Errorf("history job_id is required")
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("history started_at is required")
	}
	if r.FinishedAt.IsZero() {
		return fmt.Errorf("history finished_at is required")
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("history finished_at must not be before started_at")
	}
	if _, ok := validRunStatus[r.Status]; !ok {
		return fmt.Errorf("history status %q is unknown", r.Status)
	}
	if r.ObjectCount < 0 {
		return fmt.Errorf("history object_count must be >= 0")
	}
	if r.ByteCount < 0 {
		return fmt.Errorf("history byte_count must be >= 0")
	}
	if strings.TrimSpace(r.EngineVersion) == "" {
		return fmt.Errorf("history engine_version is required")
	}
	// scheduler evidence: freshness window and catch-up grace must be >0,
	// health and overlap must be known tokens, and health must be consistent
	// with live evaluation at FinishedAt. This enforces fail-closed stale/unknown.
	if r.Scheduler.FreshnessWindow <= 0 {
		return fmt.Errorf("history scheduler freshness_window must be > 0")
	}
	if r.Scheduler.CatchUpGrace <= 0 {
		return fmt.Errorf("history scheduler catch_up_grace must be > 0")
	}
	if _, ok := validHealth[r.Scheduler.Health]; !ok {
		return fmt.Errorf("history scheduler health %q is unknown", r.Scheduler.Health)
	}
	if r.Scheduler.OverlapState != "" {
		if _, ok := validOverlap[r.Scheduler.OverlapState]; !ok {
			return fmt.Errorf("history scheduler overlap_state %q is invalid", r.Scheduler.OverlapState)
		}
	}
	// Fail closed: a caller may record non-healthy states, but a healthy claim
	// is strictly verified against fresh evaluation. Stale/missing/unknown cannot
	// be recorded as healthy.
	evaluated := EvaluateSchedulerHealth(r.Scheduler, r.FinishedAt)
	if r.Scheduler.Health == HealthHealthy && evaluated != HealthHealthy {
		return fmt.Errorf("history scheduler health %q is not healthy under evaluation: %q", r.Scheduler.Health, evaluated)
	}
	for i, rep := range r.ReplicaOutcomes {
		if strings.TrimSpace(rep.ID) == "" {
			return fmt.Errorf("history replica %d: id is required", i)
		}
		if rep.Status != "ok" && rep.Status != "failed" && rep.Status != "skipped" && rep.Status != "degraded" {
			// allow ok/failed for replicas; be permissive but fail on unknown
			if _, ok := validRunStatus[rep.Status]; !ok && rep.Status != "ok" && rep.Status != "failed" {
				return fmt.Errorf("history replica %q: unknown status %q", rep.ID, rep.Status)
			}
		}
	}
	return nil
}

func recordsEqual(a, b RunRecord) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// readAll returns all records and their raw lines (each ending with '\n').
// It fails visibly on truncation, corrupt JSON, or unknown schema.
func (s *HistoryStore) readAll() ([]RunRecord, [][]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("open history: %w", err)
	}
	if len(data) == 0 {
		return nil, nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, nil, fmt.Errorf("corrupt history: truncated last line")
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var records []RunRecord
	var raws [][]byte
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal(trimmed, &rec); err != nil {
			return nil, nil, fmt.Errorf("corrupt history: decode: %w", err)
		}
		if rec.SchemaVersion != HistorySchemaVersion {
			return nil, nil, fmt.Errorf("unsupported history schema_version %d", rec.SchemaVersion)
		}
		if err := validateRunRecord(rec); err != nil {
			return nil, nil, fmt.Errorf("corrupt history: %w", err)
		}
		records = append(records, rec)
		// preserve raw line with single '\n'
		raw := make([]byte, len(trimmed)+1)
		copy(raw, trimmed)
		raw[len(trimmed)] = '\n'
		raws = append(raws, raw)
	}
	return records, raws, nil
}

// Append atomically adds one immutable record. Duplicate run_id with different
// bytes is rejected; exact same bytes is idempotent.
func (s *HistoryStore) Append(rec RunRecord) error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("history store path is required")
	}
	if err := validateRunRecord(rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	records, raws, err := s.readAll()
	if err != nil {
		return err
	}
	for _, existing := range records {
		if existing.RunID == rec.RunID {
			if recordsEqual(existing, rec) {
				return nil
			}
			return fmt.Errorf("duplicate run_id %q with different bytes", rec.RunID)
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode history record: %w", err)
	}
	line = append(line, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("create history temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod history temp: %w", err)
	}
	for _, raw := range raws {
		if _, err := tmp.Write(raw); err != nil {
			return fmt.Errorf("write history temp: %w", err)
		}
	}
	if _, err := tmp.Write(line); err != nil {
		return fmt.Errorf("write history temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync history temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close history temp: %w", err)
	}
	// Remove cleanup's deferred close side-effect: file already closed
	cleanup = func() { _ = os.Remove(tmpPath) }
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace history: %w", err)
	}
	// Ensure final perm is 0600 (rename preserves tmp perm)
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("chmod history: %w", err)
	}
	return nil
}

// Load returns up to limit records for jobID, most recent first.
// limit <=0 means all.
func (s *HistoryStore) Load(jobID string, limit int) ([]RunRecord, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("history job_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var filtered []RunRecord
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

// LoadCurrent returns the most recent record for jobID, or nil if none.
func (s *HistoryStore) LoadCurrent(jobID string) (*RunRecord, error) {
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
func (s *HistoryStore) LoadAll(limit int) ([]RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, _, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit >= len(records) {
		// reverse to most recent first
		out := make([]RunRecord, len(records))
		for i := range records {
			out[i] = records[len(records)-1-i]
		}
		return out, nil
	}
	out := make([]RunRecord, 0, limit)
	for i := len(records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, records[i])
	}
	return out, nil
}

// HistoryStoreInterface is the borrowing interface a sync adapter can conform to
// without importing engine/syncloop. It mirrors the store's boring methods.
type HistoryStoreInterface interface {
	Append(RunRecord) error
	Load(jobID string, limit int) ([]RunRecord, error)
	LoadCurrent(jobID string) (*RunRecord, error)
	LoadAll(limit int) ([]RunRecord, error)
}

var _ HistoryStoreInterface = (*HistoryStore)(nil)
