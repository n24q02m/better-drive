package cleanup

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func inventoryObject(id string) Object {
	return Object{
		ID: id, ParentID: "root-1", Name: id + ".bin", Path: id + ".bin", ObjectType: ObjectTypeFile,
		ContentHash: strings.Repeat("a", 64), Size: 4, Provider: "drive", AccountID: "account-1",
		RootID: "root-1", Namespace: "backup/home", Version: "v1", Generation: "generation-" + id,
		MetadataDigest: Digest([]byte("metadata-" + id)), ModifiedAt: time.Unix(100, 0).UTC(), Depth: 1, Class: ClassUnknown,
	}
}

func validRootSet() RootSet {
	rootSet := RootSet{
		SchemaVersion: CurrentRootSetSchemaVersion,
		Roots: []Root{
			{
				Provider:      "drive",
				AccountID:     "account-1",
				RootID:        "root-1",
				Namespace:     "backup/home",
				ExpectedPages: 2,
				Pages: []Page{
					{Number: 1, ParentID: "root-1", Cursor: "cursor-1", Status: PageComplete, Objects: []Object{inventoryObject("object-1")}},
					{Number: 2, ParentID: "root-1", Cursor: "cursor-2", Status: PageComplete, Objects: []Object{inventoryObject("object-2")}},
				},
			},
		},
	}
	rootSet, _ = FreezeRootSet(rootSet)
	return rootSet
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

func TestBuildAggregateRejectsMalformedMetadataDigest(t *testing.T) {
	rootSet := validRootSet()
	rootSet.ExpectedHash = ""
	rootSet.Roots[0].Pages[0].Objects[0].MetadataDigest = "not-a-sha256"
	rootSet, err := FreezeRootSet(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAggregate(rootSet, "account-1"); err == nil || !strings.Contains(err.Error(), "metadata_digest") {
		t.Fatalf("metadata digest error = %v, want lowercase SHA-256 rejection", err)
	}
}
func TestDecodeAggregateRequiresExactRootCapture(t *testing.T) {
	rootSet := validRootSet()
	aggregate, err := BuildAggregate(rootSet, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAggregate(data, rootSet, "account-1"); err != nil {
		t.Fatalf("DecodeAggregate exact capture: %v", err)
	}
	aggregate.Status = InventoryIncomplete
	data, _ = json.Marshal(aggregate)
	if _, err := DecodeAggregate(data, rootSet, "account-1"); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("DecodeAggregate forged status = %v, want exact-capture rejection", err)
	}
	aggregate, _ = BuildAggregate(rootSet, "account-1")
	aggregate.Objects = append(aggregate.Objects, aggregate.Objects[0])
	data, _ = json.Marshal(aggregate)
	if _, err := DecodeAggregate(data, rootSet, "account-1"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("DecodeAggregate duplicate object = %v, want duplicate rejection", err)
	}
}

func TestBuildAggregateRejectsDuplicatePhysicalRootsAndObjects(t *testing.T) {
	roots := validRootSet()
	alias := roots.Roots[0]
	alias.Namespace = "backup/alias"
	roots.Roots = append(roots.Roots, alias)
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "physical root") {
		t.Fatalf("BuildAggregate namespace alias = %v, want physical-root rejection", err)
	}

	roots = validRootSet()
	duplicate := roots.Roots[0]
	duplicate.Pages = append([]Page(nil), duplicate.Pages...)
	for pageIndex := range duplicate.Pages {
		duplicate.Pages[pageIndex].Objects = append([]Object(nil), duplicate.Pages[pageIndex].Objects...)
	}
	duplicate.RootID = "root-2"
	for pageIndex := range duplicate.Pages {
		duplicate.Pages[pageIndex].ParentID = "root-2"
		for objectIndex := range duplicate.Pages[pageIndex].Objects {
			duplicate.Pages[pageIndex].Objects[objectIndex].RootID = "root-2"
			duplicate.Pages[pageIndex].Objects[objectIndex].ParentID = "root-2"
		}
	}
	roots.Roots = append(roots.Roots, duplicate)
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "physical object") {
		t.Fatalf("BuildAggregate duplicate physical object = %v, want duplicate rejection", err)
	}
}
func TestBuildStateRejectsSelfAssertedCompleteAggregate(t *testing.T) {
	rootSet := validRootSet()
	aggregate, err := BuildAggregate(rootSet, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.InventoryHash = strings.Repeat("f", 64)
	state := BuildState(rootSet, aggregate, nil)
	if state.Status == InventoryComplete {
		t.Fatalf("BuildState accepted self-asserted aggregate: %+v", state)
	}
	if len(state.Errors) == 0 {
		t.Fatalf("BuildState did not retain aggregate verification failure: %+v", state)
	}
}

func TestBuildAggregateRejectsByteCountOverflow(t *testing.T) {
	roots := validRootSet()
	roots.Roots[0].Pages[0].Objects[0].Size = maxInt64
	roots.Roots[0].Pages[1].Objects[0].Size = 1
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("BuildAggregate overflow error = %v, want overflow rejection", err)
	}
}

func TestDecodeRootSetRequiresFrozenExpectedHash(t *testing.T) {
	rootSet := validRootSet()
	rootSet.ExpectedHash = ""
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
func TestDecodeRootSetRejectsPreviousCurrentSchema(t *testing.T) {
	rootSet := validRootSet()
	rootSet.SchemaVersion = CurrentRootSetSchemaVersion - 1
	rootSet.ExpectedHash = ""
	data, err := json.Marshal(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRootSet(data); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("DecodeRootSet previous current schema = %v, want explicit rejection", err)
	}
}

func TestDecodeRootSetRejectsLegacySchema(t *testing.T) {
	rootSet := validRootSet()
	rootSet.SchemaVersion = 1
	rootSet.ExpectedHash = ""
	data, err := json.Marshal(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRootSet(data); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("DecodeRootSet legacy schema = %v, want explicit rejection", err)
	}
}

func TestBuildAggregateRejectsMissingAndDuplicatePages(t *testing.T) {
	roots := validRootSet()
	roots.Roots[0].Pages = roots.Roots[0].Pages[:1]
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "page") {
		t.Fatalf("expected missing page rejection, got %v", err)
	}

	roots = validRootSet()
	roots.Roots[0].Pages[1].Number = 1
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "duplicate page") {
		t.Fatalf("expected duplicate page rejection, got %v", err)
	}

	roots = validRootSet()
	roots.Roots[0].Pages[1].Cursor = roots.Roots[0].Pages[0].Cursor
	roots, _ = FreezeRootSet(roots)
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
	roots, _ = FreezeRootSet(roots)
	if _, err := BuildAggregate(roots, "account-1"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete page rejection, got %v", err)
	}
}
