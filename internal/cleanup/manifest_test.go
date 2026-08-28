package cleanup

import (
	"strings"
	"testing"
	"time"
)

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:       CurrentSchemaVersion,
		ManifestID:          "manifest-1",
		AccountID:           "account-1",
		RootID:              "root-1",
		Namespace:           "backup/home",
		Mode:                ModeQuarantine,
		CreatedAt:           time.Unix(100, 0).UTC(),
		ExpiresAt:           time.Unix(200, 0).UTC(),
		Nonce:               "nonce-1",
		Budget:              Budget{MaxObjects: 2, MaxBytes: 20},
		SourceInventoryHash: strings.Repeat("i", 64),
		Objects: []Object{
			{
				ID:              "object-b",
				ParentID:        "root-1",
				Name:            "b.bin",
				Path:            "b.bin",
				ObjectType:      ObjectTypeFile,
				ContentHash:     strings.Repeat("b", 64),
				Size:            10,
				Provider:        "drive",
				AccountID:       "account-1",
				RootID:          "root-1",
				Namespace:       "backup/home",
				Version:         "v2",
				Generation:      "generation-b",
				ETag:            "etag-b",
				ModifiedAt:      time.Unix(90, 0).UTC(),
				Depth:           1,
				Class:           ClassDuplicateSameHash,
				RetainedPeerID:  "retained-a",
				OwnershipMarker: "marker-1",
				RestoreEvidence: "restore-1",
			},
			{
				ID:              "object-a",
				ParentID:        "root-1",
				Name:            "a.bin",
				Path:            "a.bin",
				ObjectType:      ObjectTypeFile,
				ContentHash:     strings.Repeat("b", 64),
				Size:            10,
				Provider:        "drive",
				AccountID:       "account-1",
				RootID:          "root-1",
				Namespace:       "backup/home",
				Version:         "v1",
				Generation:      "generation-a",
				ETag:            "etag-a",
				ModifiedAt:      time.Unix(91, 0).UTC(),
				Depth:           1,
				Class:           ClassDuplicateSameHash,
				RetainedPeerID:  "retained-b",
				OwnershipMarker: "marker-1",
				RestoreEvidence: "restore-1",
			},
		},
	}
}

func TestValidateManifestAcceptsBoundSafeManifest(t *testing.T) {
	m := validManifest()
	got, err := ValidateManifest(m, time.Unix(150, 0).UTC())
	if err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}
	if got.ObjectCount != 2 || got.ByteCount != 20 {
		t.Fatalf("unexpected totals: %+v", got)
	}
	if got.ManifestDigest == "" {
		t.Fatal("expected manifest digest")
	}
}
func TestValidateManifestRequiresCompleteEmptyFolderProof(t *testing.T) {
	m := validManifest()
	m.Budget = Budget{MaxObjects: 1, MaxBytes: 1}
	folder := m.Objects[0]
	folder.ObjectType = ObjectTypeFolder
	folder.Class = ClassOrphan
	folder.ContentHash = ""
	folder.Size = 0
	m.Objects = []Object{folder}
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("unproven empty-folder error = %v, want fail-closed rejection", err)
	}

	folder.ChildrenComplete = true
	folder.ChildCount = 0
	folder.SubtreeComplete = true
	folder.SubtreeObjectCount = 0
	folder.SubtreeWriterFence = "fence-1"
	folder.EmptyCheckIDs = []string{"check-1", "check-2"}
	m.Objects[0] = folder
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err != nil {
		t.Fatalf("complete empty-folder proof rejected: %v", err)
	}
}

func TestValidateManifestRejectsTrashMode(t *testing.T) {
	m := validManifest()
	m.Mode = Mode("trash")
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("expected quarantine-only mode rejection, got %v", err)
	}
}


func TestValidateManifestRejectsUnsafeClassesAndDuplicateIDs(t *testing.T) {
	m := validManifest()
	m.Objects[0].Class = ClassActive
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "class") {
		t.Fatalf("expected unsafe class rejection, got %v", err)
	}

	m = validManifest()
	m.Objects[1].ID = m.Objects[0].ID
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate ID rejection, got %v", err)
	}
}

func TestValidateManifestRejectsBudgetAndExpiry(t *testing.T) {
	m := validManifest()
	m.Budget.MaxObjects = 1
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "object budget") {
		t.Fatalf("expected object budget rejection, got %v", err)
	}

	m = validManifest()
	if _, err := ValidateManifest(m, time.Unix(250, 0).UTC()); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry rejection, got %v", err)
	}
}

func TestValidateManifestRejectsByteCountOverflow(t *testing.T) {
	m := validManifest()
	m.Objects[0].Size = maxInt64
	m.Objects[1].Size = 1
	if _, err := ValidateManifest(m, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("ValidateManifest overflow error = %v, want overflow rejection", err)
	}
}

func TestCanonicalManifestSortsObjectsAndDigestsStableBytes(t *testing.T) {
	m := validManifest()
	first, err := CanonicalManifest(m)
	if err != nil {
		t.Fatalf("CanonicalManifest() error = %v", err)
	}
	m.Objects[0], m.Objects[1] = m.Objects[1], m.Objects[0]
	second, err := CanonicalManifest(m)
	if err != nil {
		t.Fatalf("CanonicalManifest() reordered error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical bytes changed after object reorder:\n%s\n%s", first, second)
	}
	if Digest(first) != Digest(second) {
		t.Fatal("canonical digest changed after object reorder")
	}
}
