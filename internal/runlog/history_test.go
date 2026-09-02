package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validTestCycle(now time.Time) CycleRecord {
	return CycleRecord{
		SchemaVersion:       HistorySchemaVersion,
		CycleID:             "cycle-1",
		RunID:               "run-1",
		JobID:               "job-1",
		StartedAt:           now.Add(-1 * time.Minute),
		FinishedAt:          now,
		Status:              "ok",
		ObjectCount:         5,
		ByteCount:           1024,
		Warnings:            []string{"w1"},
		EngineVersion:       "1.6.0",
		SchedulerOwner:      "better-drive",
		SchedulerJobID:      "job-1",
		SchedulerHealth:     "healthy",
		SchedulerObservedAt: now,
		FreshnessWindow:     15 * time.Minute,
		CatchUpGrace:        6*time.Hour + 15*time.Minute,
	}
}

func TestCycleStoreAppendAndLoadPreservesVersionedCycleEvidence(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cycles.jsonl")
	store := NewCycleStore(path, func() time.Time { return now })
	rec := validTestCycle(now)
	if err := store.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.Load("job-1", 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].CycleID != "cycle-1" || got[0].RunID != "run-1" || got[0].ObjectCount != 5 || got[0].ByteCount != 1024 {
		t.Fatalf("Load = %#v, want persisted cycle", got)
	}
	if got[0].SchedulerHealth != "healthy" || got[0].SchedulerJobID != "job-1" {
		t.Fatalf("scheduler evidence = %#v", got[0])
	}
	cur, err := store.LoadCurrent("job-1")
	if err != nil || cur == nil || cur.CycleID != "cycle-1" {
		t.Fatalf("LoadCurrent = %#v, err=%v", cur, err)
	}
}

func TestCycleStoreRejectsDuplicateCycleIDWithDifferentBytes(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cycles.jsonl")
	store := NewCycleStore(path, func() time.Time { return now })
	rec := validTestCycle(now)
	if err := store.Append(rec); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	other := rec
	other.ByteCount = 9999
	if err := store.Append(other); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate with different bytes accepted: %v", err)
	}
	// exact same is idempotent
	if err := store.Append(rec); err != nil {
		t.Fatalf("idempotent append failed: %v", err)
	}
	all, err := store.LoadAll(0)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAll count = %d, want 1", len(all))
	}
}

func TestCycleStoreFailsVisiblyOnTruncationAndUnknownSchema(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cycles.jsonl")
	store := NewCycleStore(path, func() time.Time { return now })
	if err := store.Append(validTestCycle(now)); err != nil {
		t.Fatal(err)
	}
	// append truncated line
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write([]byte(`{"cycle_id":"truncated"`))
	f.Close()
	if _, err := store.Load("job-1", 10); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("truncation error = %v, want corrupt rejection", err)
	}
	// rewrite unknown schema
	bad := validTestCycle(now)
	bad.SchemaVersion = 99
	body, _ := json.Marshal(bad)
	os.WriteFile(path, append(body, '\n'), 0o600)
	if _, err := store.Load("job-1", 10); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("unknown schema error = %v, want schema rejection", err)
	}
}

func TestCycleStoreValidatesFailClosedSchedulerHealth(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cycles.jsonl")
	store := NewCycleStore(path, func() time.Time { return now })
	rec := validTestCycle(now)
	// Stale observed_at with healthy claim fails validation
	rec.SchedulerObservedAt = now.Add(-30 * time.Minute)
	rec.SchedulerHealth = "healthy"
	if err := store.Append(rec); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale scheduler healthy claim was accepted: %v", err)
	}
	// Stale observed_at with stale health is accepted
	rec.SchedulerHealth = "stale"
	if err := store.Append(rec); err != nil {
		t.Fatalf("stale scheduler with stale health rejected: %v", err)
	}
}
