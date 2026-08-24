package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func validRestoreSetRecord() RestoreSetRecord {
	return RestoreSetRecord{
		RestoreSetID: "set-1",
		Components: []RestoreComponent{
			{Name: "state", Digest: "sha256:state"},
			{Name: "history", Digest: "sha256:history"},
		},
		ExpectedReplicas: []RestoreReplica{
			{ReplicaID: "drive-root", Required: true},
			{ReplicaID: "r2-root", Required: false},
		},
		ReplicaAcks: []RestoreReplicaAck{
			{
				ReplicaID:        "drive-root",
				Required:         true,
				Complete:         true,
				ProviderVersion:  "v42",
				ETag:             "etag-drive",
				CiphertextDigest: "sha256:cipher",
				VerifiedReadback: true,
				Components:       []RestoreComponent{{Name: "state", Digest: "sha256:state"}, {Name: "history", Digest: "sha256:history"}},
			},
			{
				ReplicaID:  "r2-root",
				Required:   false,
				Complete:   false,
				Components: []RestoreComponent{{Name: "state", Digest: "sha256:state"}, {Name: "history", Digest: "sha256:history"}},
			},
		},
	}
}

func TestRestoreSetJournalRoundTripPreservesComponentReplicaReadbackAndIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore-sets.jsonl")
	want := validRestoreSetRecord()
	if err := AppendRestoreSetRecord(path, want); err != nil {
		t.Fatalf("AppendRestoreSetRecord: %v", err)
	}
	got, err := ReadRestoreSetRecords(path)
	if err != nil {
		t.Fatalf("ReadRestoreSetRecords: %v", err)
	}
	if len(got) != 1 || got[0].RestoreSetID != want.RestoreSetID || len(got[0].Components) != 2 || len(got[0].ReplicaAcks) != 2 {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	ack := got[0].ReplicaAcks[0]
	if ack.ProviderVersion != "v42" || ack.ETag != "etag-drive" || !ack.VerifiedReadback || len(ack.Components) != 2 {
		t.Fatalf("ack readback = %#v, want provider/version/verified components", ack)
	}
}

func TestRestoreSetRecordRejectsIncompleteRequiredAck(t *testing.T) {
	record := validRestoreSetRecord()
	record.ReplicaAcks[0].Complete = false
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("Validate error = %v, want incomplete required ack rejection", err)
	}
}

func TestRestoreSetRecordRejectsDuplicateUnknownAndMissingComponents(t *testing.T) {
	record := validRestoreSetRecord()
	record.Components = append(record.Components, RestoreComponent{Name: "state", Digest: "sha256:other"})
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate component") {
		t.Fatalf("duplicate component error = %v", err)
	}

	record = validRestoreSetRecord()
	record.ReplicaAcks[0].Components = []RestoreComponent{{Name: "unknown", Digest: "sha256:unknown"}, {Name: "history", Digest: "sha256:history"}}
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "component") {
		t.Fatalf("unknown component error = %v", err)
	}

	record = validRestoreSetRecord()
	record.ReplicaAcks[0].Components = []RestoreComponent{{Name: "state", Digest: "sha256:state"}}
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "component") {
		t.Fatalf("missing component error = %v", err)
	}
}

func TestRestoreSetRecordRejectsUnknownReplicaRequiredDriftAndReplay(t *testing.T) {
	record := validRestoreSetRecord()
	record.ReplicaAcks[0].ReplicaID = "foreign"
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "unknown replica") {
		t.Fatalf("unknown replica error = %v", err)
	}

	record = validRestoreSetRecord()
	record.ReplicaAcks[1].Required = true
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("required-bit drift error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "restore-sets.jsonl")
	record = validRestoreSetRecord()
	if err := AppendRestoreSetRecord(path, record); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := AppendRestoreSetRecord(path, record); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay append error = %v, want replay rejection", err)
	}
}

func TestRestoreSetRecordRejectsMissingVerifiedReadbackProviderFields(t *testing.T) {
	record := validRestoreSetRecord()
	record.ReplicaAcks[0].VerifiedReadback = false
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "readback") {
		t.Fatalf("readback error = %v, want verified readback rejection", err)
	}
	record = validRestoreSetRecord()
	record.ReplicaAcks[0].ProviderVersion = ""
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("provider error = %v, want provider version rejection", err)
	}
	record = validRestoreSetRecord()
	record.ReplicaAcks[0].ETag = ""
	if err := record.Validate(); err == nil || !strings.Contains(err.Error(), "ETag") {
		t.Fatalf("ETag error = %v, want ETag rejection", err)
	}
}
