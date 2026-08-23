package cleanup

import (
	"strings"
	"testing"
)

func inventoryObject(id string) Object {
	return Object{ID: id, Name: id + ".bin", ContentHash: strings.Repeat("a", 64), Size: 4, Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", ETag: "etag-" + id, Class: ClassUnknown}
}

func validRootSet() RootSet {
	return RootSet{
		SchemaVersion: CurrentInventorySchemaVersion,
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
