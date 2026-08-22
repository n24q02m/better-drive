package engine

import (
	"path/filepath"
	"testing"
)

func TestRestoreSetJournalRoundTripPreservesComponentAndReplicaAcks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore-sets.jsonl")
	want := RestoreSetRecord{
		RestoreSetID: "set-1",
		Components:   []RestoreComponent{{Name: "state", Digest: "sha256:state"}, {Name: "history", Digest: "sha256:history"}},
		ReplicaAcks:  []RestoreReplicaAck{{ReplicaID: "drive-root", Required: true, Complete: true, CiphertextDigest: "sha256:cipher"}},
	}
	if err := AppendRestoreSetRecord(path, want); err != nil {
		t.Fatalf("AppendRestoreSetRecord: %v", err)
	}
	got, err := ReadRestoreSetRecords(path)
	if err != nil {
		t.Fatalf("ReadRestoreSetRecords: %v", err)
	}
	if len(got) != 1 || got[0].RestoreSetID != want.RestoreSetID || len(got[0].Components) != 2 || len(got[0].ReplicaAcks) != 1 {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestRestoreSetRecordRejectsIncompleteRequiredAck(t *testing.T) {
	record := RestoreSetRecord{
		RestoreSetID: "set-1",
		Components:   []RestoreComponent{{Name: "state", Digest: "sha256:state"}},
		ReplicaAcks:  []RestoreReplicaAck{{ReplicaID: "drive-root", Required: true, Complete: false}},
	}
	if err := record.Validate(); err == nil {
		t.Fatal("Validate returned nil, want incomplete required ack rejection")
	}
}

func TestRestoreSetRecordRejectsDuplicateComponentAndReplicaIDs(t *testing.T) {
	record := RestoreSetRecord{
		RestoreSetID: "set-1",
		Components:   []RestoreComponent{{Name: "state", Digest: "sha256:a"}, {Name: "state", Digest: "sha256:b"}},
		ReplicaAcks:  []RestoreReplicaAck{{ReplicaID: "drive", Required: true, Complete: true}, {ReplicaID: "drive", Required: true, Complete: true}},
	}
	if err := record.Validate(); err == nil {
		t.Fatal("Validate returned nil, want duplicate identity rejection")
	}
}
