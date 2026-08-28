package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// CycleStatus is the immutable terminal status of one transfer cycle.
const (
	CycleOK        = "ok"
	CycleDegraded  = "degraded"
	CycleFailed    = "failed"
	CycleCancelled = "cancelled"
)

// ReplicaRecord is the immutable per-replica evidence persisted for one cycle.
// It mirrors ReplicaOutcome but is intentionally a separate wire type so a
// caller cannot mutate live outcome state after it is recorded.
type ReplicaRecord struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Required bool   `json:"required"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// CycleRecord is the immutable persisted evidence for one sync cycle.
// All fields are caller-injected and never minted inside the engine.
// RunID and timestamps are required so a scheduler-owned adapter can order
// and deduplicate cycles without relying on mutable state.
type CycleRecord struct {
	RunID         string          `json:"run_id"`
	JobID         string          `json:"job_id"`
	Mode          string          `json:"mode"`
	Direction     string          `json:"direction"`
	EngineVersion string          `json:"engine_version"`
	StartedAt     time.Time       `json:"started_at"`
	EndedAt       time.Time       `json:"ended_at"`
	Status        string          `json:"status"`
	Replicas      []ReplicaRecord `json:"replicas"`
	RestoreAcks   []RestoreSetAck `json:"restore_acks,omitempty"`
}

// Validate reports whether r is a well-formed immutable record.
func (r CycleRecord) Validate() error {
	if strings.TrimSpace(r.RunID) == "" {
		return errors.New("history cycle run_id is required")
	}
	if strings.TrimSpace(r.JobID) == "" {
		return errors.New("history cycle job_id is required")
	}
	if r.Mode != "copy" && r.Mode != "sync" && r.Mode != "bisync" {
		return fmt.Errorf("history cycle mode %q is invalid", r.Mode)
	}
	if r.Direction != "push" && r.Direction != "pull" && r.Direction != "bidirectional" {
		return fmt.Errorf("history cycle direction %q is invalid", r.Direction)
	}
	if r.StartedAt.IsZero() || r.EndedAt.IsZero() {
		return errors.New("history cycle timestamps are required")
	}
	if r.EndedAt.Before(r.StartedAt) {
		return errors.New("history cycle ended before it started")
	}
	switch r.Status {
	case CycleOK, CycleDegraded, CycleFailed, CycleCancelled:
	default:
		return fmt.Errorf("history cycle status %q is invalid", r.Status)
	}
	if len(r.Replicas) == 0 {
		return errors.New("history cycle requires at least one replica record")
	}
	for i, replica := range r.Replicas {
		if strings.TrimSpace(replica.ID) == "" || strings.TrimSpace(replica.Target) == "" {
			return fmt.Errorf("history cycle replica %d: id and target are required", i)
		}
		switch replica.Status {
		case "ok", "failed", "degraded":
		default:
			// History stores the exact engine status vocabulary; unknown status
			// is never persisted as success.
			if replica.Status != "ok" && replica.Status != "failed" {
				return fmt.Errorf("history cycle replica %d: status %q is invalid", i, replica.Status)
			}
		}
	}
	return nil
}

// HistoryStore is the caller-injected persistence boundary for per-cycle
// history. The engine never creates its own store, never synthesizes a
// RunID, and never prunes or mutates a previously appended record. A
// scheduler-owned adapter implements this interface against state/runlog
// persistence; tests use MemoryHistoryStore.
type HistoryStore interface {
	Append(CycleRecord) error
}

// MemoryHistoryStore is a deterministic in-memory HistoryStore for tests
// and contract verification. It copies records on append so a caller cannot
// mutate persisted evidence through the original slice.
type MemoryHistoryStore struct {
	records []CycleRecord
}

// Append validates and appends r. It fails closed on invalid records and on
// duplicate RunID so a retry cannot silently overwrite immutable evidence.
func (m *MemoryHistoryStore) Append(r CycleRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, existing := range m.records {
		if existing.RunID == r.RunID {
			return fmt.Errorf("history duplicate run_id %q", r.RunID)
		}
	}
	clone := r
	clone.Replicas = append([]ReplicaRecord(nil), r.Replicas...)
	clone.RestoreAcks = append([]RestoreSetAck(nil), r.RestoreAcks...)
	m.records = append(m.records, clone)
	return nil
}

// Records returns a copy of all appended cycles in append order.
func (m *MemoryHistoryStore) Records() []CycleRecord {
	out := make([]CycleRecord, len(m.records))
	for i, r := range m.records {
		out[i] = r
		out[i].Replicas = append([]ReplicaRecord(nil), r.Replicas...)
		out[i].RestoreAcks = append([]RestoreSetAck(nil), r.RestoreAcks...)
	}
	return out
}

// NopHistoryStore discards cycles and is used only by tests that explicitly
// opt out of history verification. Production wiring must inject a real store.
type NopHistoryStore struct{}

func (NopHistoryStore) Append(CycleRecord) error { return nil }
