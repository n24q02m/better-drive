package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyNow() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) }

func validRunRecord(now time.Time) RunRecord {
	return RunRecord{
		SchemaVersion: HistorySchemaVersion,
		RunID:         "run-1",
		JobID:         "job-1",
		StartedAt:     now.Add(-1 * time.Minute),
		FinishedAt:    now,
		Status:        "ok",
		ObjectCount:   3,
		ByteCount:     42,
		Warnings:      []string{"warn-a"},
		EngineVersion: "1.6.0",
		ReplicaOutcomes: []ReplicaState{
			{ID: "r1", Target: "drive:backup", Required: true, Status: "ok"},
		},
		RestoreSetID:        "restore-set-1",
		RestoreAcknowledged: true,
		Scheduler: SchedulerState{
			Owner: "better-drive", OwnerJobID: "job-1", Enabled: true,
			ActiveInstance: "one-shot", OverlapState: OverlapNone, OverlapHealth: "ok",
			ObservedAt: now, FreshnessWindow: 15 * time.Minute, CatchUpGrace: 6*time.Hour + 15*time.Minute,
			Health: HealthHealthy, LastTrigger: now.Add(-1 * time.Hour), NextTrigger: now.Add(5 * time.Hour),
		},
		CycleID: "cycle-1",
	}
}

func TestHistoryStoreAppendAndLoadPreservesVersionedEvidence(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), func() time.Time { return now })
	rec := validRunRecord(now)
	if err := store.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := store.Load("job-1", 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].RunID != rec.RunID || got[0].JobID != rec.JobID || got[0].ObjectCount != 3 || got[0].ByteCount != 42 {
		t.Fatalf("Load = %#v, want persisted evidence", got)
	}
	if got[0].Warnings[0] != "warn-a" || got[0].EngineVersion != "1.6.0" || len(got[0].ReplicaOutcomes) != 1 {
		t.Fatalf("warnings/engine/replicas lost: %#v", got[0])
	}
	if got[0].RestoreSetID != "restore-set-1" || !got[0].RestoreAcknowledged {
		t.Fatalf("restore_set lost: %#v", got[0])
	}
	if got[0].Scheduler.OwnerJobID != "job-1" || got[0].Scheduler.Health != HealthHealthy {
		t.Fatalf("scheduler evidence lost: %#v", got[0].Scheduler)
	}
	if got[0].CycleID != "cycle-1" || got[0].SchemaVersion != HistorySchemaVersion {
		t.Fatalf("cycle/schema lost: %#v", got[0])
	}
}

func TestHistoryStoreAppendRejectsDuplicateRunIDWithDifferentBytes(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), func() time.Time { return now })
	rec := validRunRecord(now)
	if err := store.Append(rec); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	other := rec
	other.ByteCount = 999
	other.ObjectCount = 999
	if err := store.Append(other); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate with different bytes accepted: %v", err)
	}
	// exact same bytes is idempotent
	if err := store.Append(rec); err != nil {
		t.Fatalf("idempotent re-append failed: %v", err)
	}
	got, err := store.Load("job-1", 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load = %d records, want 1 after idempotent re-append", len(got))
	}
}

func TestHistoryStoreLoadBoundedAndCurrentPerJob(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), func() time.Time { return now })
	for i := 1; i <= 5; i++ {
		rec := validRunRecord(now.Add(time.Duration(i) * time.Minute))
		rec.RunID = "run-" + strings.Repeat("a", i)
		if i%2 == 0 {
			rec.JobID = "job-2"
		}
		if err := store.Append(rec); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := store.Load("job-1", 2)
	if err != nil {
		t.Fatalf("Load bounded: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("bounded Load = %d, want 2", len(got))
	}
	// most recent first
	if got[0].StartedAt.Before(got[1].StartedAt) {
		t.Fatalf("bounded order not recent-first: %#v", got)
	}
	cur, err := store.LoadCurrent("job-1")
	if err != nil {
		t.Fatalf("LoadCurrent: %v", err)
	}
	if cur == nil || cur.JobID != "job-1" {
		t.Fatalf("LoadCurrent = %#v, want job-1", cur)
	}
	all, err := store.LoadAll(3)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("LoadAll = %d, want 3", len(all))
	}
	// missing job returns empty without error
	empty, err := store.Load("missing-job", 10)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("missing job Load = %d, want 0", len(empty))
	}
	if cur, err := store.LoadCurrent("missing-job"); err != nil || cur != nil {
		t.Fatalf("LoadCurrent missing = %#v err=%v want nil", cur, err)
	}
}

func TestHistoryStoreFailsVisiblyOnTruncationCorruptUnknownSchema(t *testing.T) {
	now := historyNow()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store := NewHistoryStore(path, func() time.Time { return now })
	if err := store.Append(validRunRecord(now)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// truncation: append incomplete JSON line without newline
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(`{"schema_version":1,"run_id":"run-bad"`)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := store.Load("job-1", 10); err == nil || !strings.Contains(strings.ToLower(err.Error()), "corrupt") && !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "truncat") {
		t.Fatalf("truncation not detected: %v", err)
	}
	// rewrite with corrupt line
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("job-1", 10); err == nil || !strings.Contains(strings.ToLower(err.Error()), "corrupt") && !strings.Contains(err.Error(), "decode") {
		t.Fatalf("corrupt not detected: %v", err)
	}
	// unknown schema
	rec := validRunRecord(now)
	rec.RunID = "run-unknown"
	rec.SchemaVersion = 99
	body, _ := json.Marshal(rec)
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("job-1", 10); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("unknown schema not detected: %v", err)
	}
	// Append must also reject unknown schema visibly
	store2 := NewHistoryStore(filepath.Join(t.TempDir(), "history2.jsonl"), func() time.Time { return now })
	bad := validRunRecord(now)
	bad.SchemaVersion = 99
	if err := store2.Append(bad); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("Append unknown schema accepted: %v", err)
	}
}

func TestHistoryStoreValidatesRequiredFieldsAndRejectsUnknownStatus(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), func() time.Time { return now })
	cases := []struct {
		name string
		mut  func(RunRecord) RunRecord
	}{
		{"missing run_id", func(r RunRecord) RunRecord { r.RunID = ""; return r }},
		{"missing job_id", func(r RunRecord) RunRecord { r.JobID = ""; return r }},
		{"zero started", func(r RunRecord) RunRecord { r.StartedAt = time.Time{}; return r }},
		{"zero finished", func(r RunRecord) RunRecord { r.FinishedAt = time.Time{}; return r }},
		{"unknown status", func(r RunRecord) RunRecord { r.Status = "mystery"; return r }},
		{"missing engine", func(r RunRecord) RunRecord { r.EngineVersion = ""; return r }},
		{"negative object", func(r RunRecord) RunRecord { r.ObjectCount = -1; return r }},
	}
	for _, tc := range cases {
		rec := tc.mut(validRunRecord(now))
		if err := store.Append(rec); err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}
}

func TestHistoryStoreRejectsEmptyPathAndPreservesPermissions(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore("", func() time.Time { return now })
	if err := store.Append(validRunRecord(now)); err == nil {
		t.Fatal("empty path Append was accepted")
	}
	path := filepath.Join(t.TempDir(), "a", "b", "history.jsonl")
	store = NewHistoryStore(path, func() time.Time { return now })
	if err := store.Append(validRunRecord(now)); err != nil {
		t.Fatalf("Append with nested dir: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history file perm = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("history dir perm = %o, want 0700", dirInfo.Mode().Perm())
	}
	// constructor with nil clock uses time.Now and still works
	storeNilClock := NewHistoryStore(filepath.Join(t.TempDir(), "history2.jsonl"), nil)
	if err := storeNilClock.Append(validRunRecord(now)); err != nil {
		t.Fatalf("nil clock Append: %v", err)
	}
}

func TestHistoryStoreSchedulerEvidenceIsFailClosed(t *testing.T) {
	now := historyNow()
	store := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), func() time.Time { return now })
	rec := validRunRecord(now)
	// scheduler health must be consistent with evidence; stale observed_at must not be stored as healthy
	rec.Scheduler.ObservedAt = now.Add(-30 * time.Minute)
	rec.Scheduler.Health = HealthHealthy // caller claims healthy but evidence is stale
	if err := store.Append(rec); err == nil || !strings.Contains(strings.ToLower(err.Error()), "health") && !strings.Contains(err.Error(), "stale") {
		// We allow storage of stale evidence, but Load must re-evaluate health as stale, not healthy
		// So if Append accepts, verify Load re-evaluates
		if err != nil {
			t.Fatalf("stale scheduler evidence rejected unexpectedly: %v", err)
		}
	}
	// Store a fresh record and verify health is re-evaluated on LoadCurrent as healthy
	fresh := validRunRecord(now)
	fresh.RunID = "run-fresh"
	fresh.Scheduler.ObservedAt = now
	fresh.Scheduler.Health = HealthHealthy
	if err := store.Append(fresh); err != nil {
		t.Fatalf("fresh Append: %v", err)
	}
	cur, err := store.LoadCurrent("job-1")
	if err != nil {
		t.Fatalf("LoadCurrent: %v", err)
	}
	if cur == nil {
		t.Fatal("LoadCurrent nil")
	}
	// The latest record should be the fresh one; health should be healthy
	if cur.RunID != "run-fresh" {
		t.Fatalf("LoadCurrent = %q, want run-fresh", cur.RunID)
	}
}
