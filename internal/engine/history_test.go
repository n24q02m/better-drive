package engine

import (
	"strings"
	"testing"
	"time"
)

func validCycleRecord() CycleRecord {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return CycleRecord{
		RunID:         "run-20260829T120000Z-1",
		JobID:         "home-claude",
		Mode:          "copy",
		Direction:     "push",
		EngineVersion: "v1.7.0",
		StartedAt:     now,
		EndedAt:       now.Add(time.Second),
		Status:        CycleOK,
		Replicas:      []ReplicaRecord{{ID: "r1", Target: "gdrive:backups/claude", Required: true, Status: "ok"}},
		RestoreAcks: []RestoreSetAck{
			{RestoreSetID: "set-1", ReplicaID: "r1", Required: true, Complete: true, VerifiedReadback: true},
			{RestoreSetID: "set-2", ReplicaID: "r1", Required: true, Complete: true, VerifiedReadback: true},
		},
	}
}

func TestCycleRecordValidateAcceptsCompleteHistory(t *testing.T) {
	if err := validCycleRecord().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestHistoryStoreRejectsDuplicateRunID(t *testing.T) {
	store := &MemoryHistoryStore{}
	record := validCycleRecord()
	if err := store.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Append(record); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate Append error = %v, want duplicate rejection", err)
	}
	if got := store.Records(); len(got) != 1 || got[0].RunID != record.RunID {
		t.Fatalf("Records = %#v, want one immutable record", got)
	}
}

func TestHistoryStoreCopiesOnAppendSoCallerCannotMutatePersistedEvidence(t *testing.T) {
	store := &MemoryHistoryStore{}
	record := validCycleRecord()
	if err := store.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	record.Replicas[0].Status = "failed"
	record.RestoreAcks[0].RestoreSetID = "tampered"
	got := store.Records()
	if got[0].Replicas[0].Status != "ok" || got[0].RestoreAcks[0].RestoreSetID != "set-1" {
		t.Fatalf("persisted record was mutated via caller slice: %#v", got[0])
	}
}

func TestCycleRecordValidateFailsClosedOnUnknownStatus(t *testing.T) {
	record := validCycleRecord()
	record.Status = "unknown"
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("Validate error = %v, want status rejection", err)
	}
	record = validCycleRecord()
	record.Replicas[0].Status = "unknown"
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("replica status Validate error = %v, want rejection", err)
	}
}

func TestCycleRecordValidateFailsClosedOnBadTimestamps(t *testing.T) {
	record := validCycleRecord()
	record.EndedAt = record.StartedAt.Add(-time.Second)
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "ended before") {
		t.Fatalf("Validate error = %v, want timestamp ordering rejection", err)
	}
	record = validCycleRecord()
	record.RunID = ""
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("Validate error = %v, want run_id rejection", err)
	}
}

func TestMemoryHistoryStorePreservesAppendOrder(t *testing.T) {
	store := &MemoryHistoryStore{}
	first := validCycleRecord()
	second := validCycleRecord()
	second.RunID = "run-20260829T120001Z-1"
	second.Replicas[0].ID = "r2"
	if err := store.Append(first); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if err := store.Append(second); err != nil {
		t.Fatalf("Append second: %v", err)
	}
	records := store.Records()
	if len(records) != 2 || records[0].RunID != first.RunID || records[1].RunID != second.RunID {
		t.Fatalf("Records order = %#v, want append order", records)
	}
}
