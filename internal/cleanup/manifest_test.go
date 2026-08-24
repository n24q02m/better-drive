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
				ParentID:        "parent-1",
				Name:            "b.bin",
				ContentHash:     strings.Repeat("b", 64),
				Size:            10,
				Provider:        "drive",
				AccountID:       "account-1",
				RootID:          "root-1",
				Namespace:       "backup/home",
				Version:         "v2",
				ETag:            "etag-b",
				Class:           ClassDuplicateSameHash,
				RetainedPeerID:  "object-a",
				OwnershipMarker: "marker-1",
				RestoreEvidence: "restore-1",
			},
			{
				ID:              "object-a",
				ParentID:        "parent-1",
				Name:            "a.bin",
				ContentHash:     strings.Repeat("b", 64),
				Size:            10,
				Provider:        "drive",
				AccountID:       "account-1",
				RootID:          "root-1",
				Namespace:       "backup/home",
				Version:         "v1",
				ETag:            "etag-a",
				Class:           ClassDuplicateSameHash,
				RetainedPeerID:  "object-b",
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
