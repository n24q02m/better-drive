package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type replicaCall struct {
	kind    string
	local   string
	remote  string
	workdir string
}

type replicaFakeTransferer struct {
	calls       []replicaCall
	errByTarget map[string]error
	lastBisync  BisyncParams
	lastCopy    CopyParams
	lastSync    CopyParams
}

func (f *replicaFakeTransferer) CountSourceObjects(context.Context, string, []string, io.Writer) (int64, error) {
	return 0, nil
}

func (f *replicaFakeTransferer) Bisync(p BisyncParams) (BisyncResult, error) {
	f.lastBisync = p
	f.calls = append(f.calls, replicaCall{kind: "bisync", local: p.Path1, remote: p.Path2, workdir: p.Workdir})
	return BisyncResult{}, f.errByTarget[p.Path2]
}
func (f *replicaFakeTransferer) Copy(p CopyParams) error {
	f.lastCopy = p
	f.calls = append(f.calls, replicaCall{kind: "copy", local: p.Local, remote: p.Remote, workdir: p.Workdir})
	return f.errByTarget[p.Remote]
}
func (f *replicaFakeTransferer) Sync(p CopyParams) error {
	f.lastSync = p
	f.calls = append(f.calls, replicaCall{kind: "sync", local: p.Local, remote: p.Remote, workdir: p.Workdir})
	return f.errByTarget[p.Remote]
}

func TestValidateTransferMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, mode, direction string
		wantErr               bool
	}{
		{name: "copy push", mode: "copy", direction: "push"},
		{name: "copy pull", mode: "copy", direction: "pull"},
		{name: "copy bidirectional rejected", mode: "copy", direction: "bidirectional", wantErr: true},
		{name: "sync push", mode: "sync", direction: "push"},
		{name: "sync pull", mode: "sync", direction: "pull"},
		{name: "sync bidirectional rejected", mode: "sync", direction: "bidirectional", wantErr: true},
		{name: "bisync bidirectional", mode: "bisync", direction: "bidirectional"},
		{name: "bisync push rejected", mode: "bisync", direction: "push", wantErr: true},
		{name: "unknown mode rejected", mode: "mirror", direction: "push", wantErr: true},
		{name: "unknown direction rejected", mode: "copy", direction: "merge", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTransfer(tc.mode, tc.direction)
			if tc.wantErr && err == nil {
				t.Fatal("ValidateTransfer returned nil, want rejection")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTransfer = %v, want nil", err)
			}
		})
	}
}

func TestExecuteReplicasDispatchesPullAndBidirectionalBeforeAnySpawn(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	summary, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "pull",
		Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, Workdir: "C:/work/r1"}},
	})
	if err != nil {
		t.Fatalf("ExecuteReplicas pull: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0].kind != "copy" || f.calls[0].local != "gdrive:backup" || f.calls[0].remote != "C:/source" {
		t.Fatalf("pull calls = %#v, want reversed copy paths", f.calls)
	}
	if summary.Status != "ok" || summary.Outcomes[0].Status != "ok" {
		t.Fatalf("pull summary = %#v, want ok", summary)
	}

	f = &replicaFakeTransferer{errByTarget: map[string]error{}}
	wasNonEmpty := true
	objectCount := int64(1)
	_, err = ExecuteReplicas(f, TransferSpec{Local: "C:/source", Mode: "bisync", Direction: "bidirectional", SourceWasNonEmpty: &wasNonEmpty, SourceObjectCount: &objectCount, Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, Workdir: "C:/work/r1"}}})
	if err != nil || len(f.calls) != 1 || f.calls[0].kind != "bisync" {
		t.Fatalf("bidirectional result calls=%#v err=%v, want one bisync call", f.calls, err)
	}
}

func TestExecuteReplicasForwardsSafetyEvidence(t *testing.T) {
	wasNonEmpty := true
	objectCount := int64(3)
	budget := &DeleteBudget{MaxObjects: 10, MaxBytes: 1000}
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	_, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "sync", Direction: "push",
		SourceWasNonEmpty: &wasNonEmpty, SourceObjectCount: &objectCount, DeleteBudget: budget,
		Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, Workdir: "C:/work/r1"}},
	})
	if err != nil {
		t.Fatalf("ExecuteReplicas: %v", err)
	}
	if f.lastSync.SourceWasNonEmpty == nil || *f.lastSync.SourceWasNonEmpty != wasNonEmpty ||
		f.lastSync.SourceObjectCount == nil || *f.lastSync.SourceObjectCount != objectCount ||
		f.lastSync.DeleteBudget != budget {
		t.Fatalf("sync safety evidence = %#v, want forwarded pointers", f.lastSync)
	}
}

func TestExecuteReplicasRejectsDestructiveTransferWithoutSourceSafetyEvidence(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	_, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "sync", Direction: "push",
		Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, Workdir: "C:/work/r1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "safety evidence") || len(f.calls) != 0 {
		t.Fatalf("result err=%v calls=%#v, want missing-evidence rejection before transfer", err, f.calls)
	}
}

func TestExecuteReplicasAttemptsAllAndRequiredFailureBlocks(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{"gdrive:required": errors.New("required failed")}}
	summary, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{
			{ID: "required", Target: "gdrive:required", Required: true, Workdir: "C:/work/required"},
			{ID: "optional", Target: "r2:optional", Required: false, Workdir: "C:/work/optional"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("ExecuteReplicas error = %v, want required failure", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %#v, want both replicas attempted", f.calls)
	}
	if summary.Status != "failed" || summary.Outcomes[0].Status != "failed" || summary.Outcomes[1].Status != "ok" {
		t.Fatalf("summary = %#v, want required failure with successful optional replica", summary)
	}
	if f.calls[0].workdir == f.calls[1].workdir {
		t.Fatalf("replica calls share workdir: %#v", f.calls)
	}
}

func TestExecuteReplicasOptionalFailureIsDegraded(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{"r2:optional": errors.New("optional failed")}}
	summary, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{
			{ID: "required", Target: "gdrive:required", Required: true, Workdir: "C:/work/required"},
			{ID: "optional", Target: "r2:optional", Required: false, Workdir: "C:/work/optional"},
		},
	})
	if err != nil {
		t.Fatalf("optional failure error = %v, want nil", err)
	}
	if summary.Status != "degraded" || summary.Outcomes[1].Status != "failed" {
		t.Fatalf("summary = %#v, want degraded optional result", summary)
	}
}

func TestExecuteReplicasRejectsUnsupportedTransferBeforeSyncerCall(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	_, err := ExecuteReplicas(f, TransferSpec{Local: "C:/source", Mode: "bisync", Direction: "push", Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true}}})
	if err == nil || len(f.calls) != 0 {
		t.Fatalf("result err=%v calls=%#v, want pre-spawn rejection and zero calls", err, f.calls)
	}
}

func TestExecuteReplicasRejectsReplicaBelowRestoreFloorBeforeTransfer(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	_, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, MinCompleteRestoreSets: 1}},
	})
	if err == nil || len(f.calls) != 0 || !strings.Contains(err.Error(), "min_complete_restore_sets") {
		t.Fatalf("result err=%v calls=%#v, want floor rejection before transfer", err, f.calls)
	}
}

func TestExecuteReplicasPreservesDriveR2AndCryptTargets(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	targets := []string{"gdrive:backup", "r2:backup", "crypt:backup"}
	replicas := make([]ReplicaSpec, 0, len(targets))
	for i, target := range targets {
		replicas = append(replicas, ReplicaSpec{ID: target, Target: target, Workdir: "wd", Required: true, MinCompleteRestoreSets: 2})
		_ = i
	}
	if _, err := ExecuteReplicas(f, TransferSpec{Local: "C:/source", Mode: "copy", Direction: "push", Replicas: replicas}); err != nil {
		t.Fatalf("ExecuteReplicas: %v", err)
	}
	if len(f.calls) != len(targets) {
		t.Fatalf("calls = %#v, want %d endpoint calls", f.calls, len(targets))
	}
	for i, target := range targets {
		if f.calls[i].remote != target {
			t.Errorf("call %d remote = %q, want %q", i, f.calls[i].remote, target)
		}
	}
}

func TestRestoreFloorCountsOnlyCompleteRequiredVerifiedAcknowledgements(t *testing.T) {
	acks := []RestoreSetAck{
		{RestoreSetID: "set-1", ReplicaID: "required", Required: true, Complete: true, VerifiedReadback: true},
		{RestoreSetID: "set-1", ReplicaID: "required", Required: true, Complete: true, VerifiedReadback: true},
		{RestoreSetID: "set-2", ReplicaID: "required", Required: true, Complete: false, VerifiedReadback: true},
		{RestoreSetID: "set-3", ReplicaID: "optional", Required: false, Complete: true, VerifiedReadback: true},
		{RestoreSetID: "set-4", ReplicaID: "required", Required: true, Complete: true, VerifiedReadback: false},
		{RestoreSetID: "set-5", ReplicaID: "required", Required: true, Complete: true, VerifiedReadback: true},
	}
	if err := ValidateRestoreFloor(2, acks); err != nil {
		t.Fatalf("ValidateRestoreFloor: %v", err)
	}
	if err := ValidateRestoreFloor(3, acks); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("ValidateRestoreFloor(3) = %v, want floor rejection", err)
	}
}

func TestExecuteReplicasAllowsInitialUploadWithInsufficientHistoricalRestoreAcks(t *testing.T) {
	f := &replicaFakeTransferer{errByTarget: map[string]error{}}
	summary, err := ExecuteReplicas(f, TransferSpec{
		Local: "C:/source", Mode: "copy", Direction: "push",
		Replicas: []ReplicaSpec{{ID: "r1", Target: "gdrive:backup", Required: true, MinCompleteRestoreSets: 2, RestoreAcks: []RestoreSetAck{{RestoreSetID: "set-1", ReplicaID: "r1", Required: true, Complete: true, VerifiedReadback: true}}}},
	})
	if err != nil || summary.Status != "ok" || len(f.calls) != 1 {
		t.Fatalf("result summary=%#v err=%v calls=%#v, want upload despite historical floor not met", summary, err, f.calls)
	}
}
