package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDeleteBudgetRejectsObjectAndByteOverflow(t *testing.T) {
	if err := (DeleteBudget{MaxObjects: 2, MaxBytes: 100, Objects: 3, Bytes: 10}).Validate(); err == nil || !strings.Contains(err.Error(), "objects") {
		t.Fatalf("object overflow error = %v, want object budget rejection", err)
	}
	if err := (DeleteBudget{MaxObjects: 2, MaxBytes: 100, Objects: 1, Bytes: 101}).Validate(); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("byte overflow error = %v, want byte budget rejection", err)
	}
}

func TestValidateOwnershipMarkerRejectsIdentityDrift(t *testing.T) {
	expected := OwnershipMarker{JobID: "job-1", SourceIdentity: "source-1"}
	if err := ValidateOwnershipMarker(expected, OwnershipMarker{JobID: "job-2", SourceIdentity: "source-1"}); err == nil || !strings.Contains(err.Error(), "job") {
		t.Fatal("job identity drift was accepted")
	}
	if err := ValidateOwnershipMarker(expected, expected); err != nil {
		t.Fatalf("matching ownership marker rejected: %v", err)
	}
}

func TestValidateDestinationCollisionsRejectsExactAndAncestorOverlap(t *testing.T) {
	base := DestinationIdentity{Provider: "drive", AccountID: "acct", RootID: "root", Namespace: "Backups/Home"}
	if err := ValidateDestinationCollisions([]DestinationIdentity{base, base}); err == nil {
		t.Fatal("exact destination collision was accepted")
	}
	child := base
	child.Namespace = "backups/home/claude"
	if err := ValidateDestinationCollisions([]DestinationIdentity{base, child}); err == nil || !strings.Contains(err.Error(), "ancestor") {
		t.Fatal("ancestor destination collision was accepted")
	}
}

func TestValidateQuarantineIdentityRejectsTransferOverlap(t *testing.T) {
	transfer := DestinationIdentity{Provider: "drive", AccountID: "acct", RootID: "root", Namespace: "Backups"}
	quarantine := DestinationIdentity{Provider: "drive", AccountID: "acct", RootID: "root", Namespace: "Backups/quarantine"}
	if err := ValidateQuarantineIdentity(transfer, quarantine); err == nil {
		t.Fatal("quarantine inside transfer namespace was accepted")
	}
}

func TestValidateSourceForDestructiveModeBlocksEmptyHistory(t *testing.T) {
	if err := ValidateSourceForDestructiveMode("sync", true, 0); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatal("empty previously-nonempty source was accepted for sync")
	}
	if err := ValidateSourceForDestructiveMode("copy", true, 0); err != nil {
		t.Fatalf("copy should remain allowed for empty source: %v", err)
	}
}

func TestValidateSymlinkPolicyBlocksScheduledFollow(t *testing.T) {
	if err := ValidateSymlinkPolicy("follow", true); err == nil || !strings.Contains(err.Error(), "scheduled") {
		t.Fatal("scheduled follow policy was accepted")
	}
	if err := ValidateSymlinkPolicy("preserve", true); err != nil {
		t.Fatalf("scheduled preserve rejected: %v", err)
	}
}

func TestDestinationLockRejectsOverlapUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "destination.lock")
	first, err := AcquireDestinationLock(path)
	if err != nil {
		t.Fatalf("AcquireDestinationLock first: %v", err)
	}
	defer first.Release()
	if _, err := AcquireDestinationLock(path); err == nil {
		t.Fatal("second lock acquisition succeeded")
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := AcquireDestinationLock(path)
	if err != nil {
		t.Fatalf("AcquireDestinationLock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestCopyRejectsEmptyDestructiveSourceBeforeRunner(t *testing.T) {
	wasNonEmpty := true
	objectCount := int64(0)
	called := false
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		called = true
		return "", "", nil
	})
	err := e.Sync(CopyParams{Local: t.TempDir(), Remote: "gdrive:target", SourceWasNonEmpty: &wasNonEmpty, SourceObjectCount: &objectCount})
	if err == nil || called || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Sync error=%v called=%v, want empty-source rejection before runner", err, called)
	}
}

func TestCopyRejectsDeleteBudgetBeforeRunner(t *testing.T) {
	called := false
	e := newFakeRunnerEngine("", func(args ...string) (string, string, error) {
		called = true
		return "", "", nil
	})
	budget := &DeleteBudget{MaxObjects: 1, MaxBytes: 10, Objects: 2, Bytes: 1}
	err := e.Sync(CopyParams{Local: t.TempDir(), Remote: "gdrive:target", DeleteBudget: budget})
	if err == nil || called || !strings.Contains(err.Error(), "objects") {
		t.Fatalf("Sync error=%v called=%v, want delete-budget rejection before runner", err, called)
	}
}
