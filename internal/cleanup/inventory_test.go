package cleanup

import (
	"encoding/json"
	"strings"
	"testing"
)

func inventoryObject(id string) Object {
	return Object{ID: id, Name: id + ".bin", ContentHash: strings.Repeat("a", 64), Size: 4, Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", ETag: "etag-" + id, Class: ClassUnknown}
}

func validRootSet() RootSet {
	return RootSet{
		SchemaVersion: CurrentRootSetSchemaVersion,
		Roots: []Root{
			{
				Provider:      "drive",
				AccountID:     "account-1",
				RootID:        "root-1",
				Namespace:     "backup/home",
				ExpectedPages: 2,
				Pages: []Page{
					{Number: 1, Cursor: "cursor-1", Status: PageComplete, Objects: []Object{inventoryObject("object-1")}},
					{Number: 2, Cursor: "cursor-2", Status: PageComplete, Objects: []Object{inventoryObject("object-2")}},
				},
			},
		},
	}
}

func TestValidateRootSetAndAggregate(t *testing.T) {
	roots := validRootSet()
	aggregate, err := BuildAggregate(roots, "account-1")
	if err != nil {
		t.Fatalf("BuildAggregate() error = %v", err)
	}
	if aggregate.Status != InventoryComplete || aggregate.ObjectCount != 2 || aggregate.ByteCount != 8 {
		t.Fatalf("unexpected aggregate: %+v", aggregate)
	}
	if aggregate.RootSetHash == "" || aggregate.InventoryHash == "" {
		t.Fatal("expected root-set and inventory hashes")
	}
}

func TestBuildAggregateRejectsByteCountOverflow(t *testing.T) {
	roots := validRootSet()
	roots.Roots[0].Pages[0].Objects[0].Size = maxInt64
	roots.Roots[0].Pages[1].Objects[0].Size = 1
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("BuildAggregate overflow error = %v, want overflow rejection", err)
	}
}

func TestDecodeRootSetRequiresFrozenExpectedHash(t *testing.T) {
	rootSet := validRootSet()
	data, err := json.Marshal(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRootSet(data); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("DecodeRootSet without expected hash = %v, want fail-closed rejection", err)
	}

	frozen, err := FreezeRootSet(rootSet)
	if err != nil {
		t.Fatalf("FreezeRootSet: %v", err)
	}
	data, err = json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRootSet(data); err != nil {
		t.Fatalf("DecodeRootSet frozen set: %v", err)
	}
	frozen.ExpectedHash = strings.Repeat("f", 64)
	data, err = json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRootSet(data); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("DecodeRootSet mismatched expected hash = %v, want hash rejection", err)
	}
}

func TestDecodeRootSetMigratesLegacyV1CaptureWithoutChangingIdentity(t *testing.T) {
	legacy := validRootSet()
	legacy.SchemaVersion = legacyRootSetSchemaVersion
	legacy.ExpectedHash = ""
	legacyIdentity, err := legacyRootSetDigest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}

	migrated, err := DecodeRootSet(data)
	if err != nil {
		t.Fatalf("DecodeRootSet legacy v1: %v", err)
	}
	if migrated.SchemaVersion != CurrentRootSetSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", migrated.SchemaVersion, CurrentRootSetSchemaVersion)
	}
	if migrated.ExpectedHash == "" || migrated.LegacyHash != legacyIdentity {
		t.Fatalf("migrated hashes = expected:%q legacy:%q, want authenticated current hash and legacy identity %q", migrated.ExpectedHash, migrated.LegacyHash, legacyIdentity)
	}
	aggregate, err := BuildAggregate(migrated, "account-1")
	if err != nil {
		t.Fatalf("BuildAggregate migrated v1: %v", err)
	}
	if aggregate.RootSetHash != legacyIdentity {
		t.Fatalf("aggregate root_set_hash = %q, want preserved legacy identity %q", aggregate.RootSetHash, legacyIdentity)
	}
	state := BuildState(migrated, aggregate, nil)
	if state.RootSetHash != aggregate.RootSetHash {
		t.Fatalf("state root_set_hash = %q, aggregate = %q", state.RootSetHash, aggregate.RootSetHash)
	}
}

func TestBuildAggregateRejectsMissingAndDuplicatePages(t *testing.T) {
	roots := validRootSet()
	roots.Roots[0].Pages = roots.Roots[0].Pages[:1]
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "page") {
		t.Fatalf("expected missing page rejection, got %v", err)
	}

	roots = validRootSet()
	roots.Roots[0].Pages[1].Number = 1
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "duplicate page") {
		t.Fatalf("expected duplicate page rejection, got %v", err)
	}

	roots = validRootSet()
	roots.Roots[0].Pages[1].Cursor = roots.Roots[0].Pages[0].Cursor
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "duplicate cursor") {
		t.Fatalf("expected duplicate cursor rejection, got %v", err)
	}
}

func TestBuildAggregateRejectsAccountMismatchAndIncompletePage(t *testing.T) {
	roots := validRootSet()
	if _, err := BuildAggregate(roots, "another-account"); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account mismatch rejection, got %v", err)
	}

	roots = validRootSet()
	roots.Roots[0].Pages[1].Status = PageIncomplete
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete page rejection, got %v", err)
	}
}
