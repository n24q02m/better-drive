package cleanup

import (
	"strings"
	"testing"
	"time"
)

func manifestCapture(t *testing.T) (Manifest, RootSet, InventoryAggregate) {
	t.Helper()
	manifest := validManifest()
	objects := make([]Object, 0, len(manifest.Objects)*2)
	for _, selected := range manifest.Objects {
		observed := selected
		observed.Class = ClassUnknown
		observed.OwnershipMarker = ""
		observed.RestoreEvidence = ""
		observed.RetainedPeerID = ""
		objects = append(objects, observed)
	}
	for _, selected := range manifest.Objects {
		peer := selected
		peer.ID = selected.RetainedPeerID
		peer.Name = "retained-" + selected.RetainedPeerID + ".bin"
		peer.Path = peer.Name
		peer.Class = ClassActive
		peer.RetainedPeerID = ""
		peer.OwnershipMarker = "peer-marker"
		peer.RestoreEvidence = "peer-restore"
		objects = append(objects, peer)
	}
	rootSet := RootSet{
		SchemaVersion: CurrentRootSetSchemaVersion,
		Roots: []Root{{
			Provider: "drive", AccountID: manifest.AccountID, RootID: manifest.RootID, Namespace: manifest.Namespace,
			ExpectedPages: 1,
			Pages: []Page{{Number: 1, ParentID: manifest.RootID, Cursor: "root-page-1", Status: PageComplete, Objects: objects}},
		}},
	}
	var err error
	rootSet, err = FreezeRootSet(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := BuildAggregate(rootSet, manifest.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceInventoryHash = aggregate.InventoryHash
	return manifest, rootSet, aggregate
}

func TestValidateManifestAgainstInventoryBindsExactMetadata(t *testing.T) {
	manifest, rootSet, inventory := manifestCapture(t)
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err != nil {
		t.Fatalf("ValidateManifestAgainstInventory() error = %v", err)
	}
	inventory.Objects[0].ETag = "foreign-etag"
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("expected exact capture rejection, got %v", err)
	}
}

func TestValidateManifestAgainstInventoryBindsEveryObservedField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Object)
	}{
		{name: "name", mutate: func(object *Object) { object.Name = "drifted.bin" }},
		{name: "path", mutate: func(object *Object) { object.Path = "drifted/path" }},
		{name: "content_hash", mutate: func(object *Object) { object.ContentHash = "drifted-hash" }},
		{name: "size", mutate: func(object *Object) { object.Size++ }},
		{name: "version", mutate: func(object *Object) { object.Version = "drifted-version" }},
		{name: "generation", mutate: func(object *Object) { object.Generation = "drifted-generation" }},
		{name: "etag", mutate: func(object *Object) { object.ETag = "drifted-etag" }},
		{name: "modified_at", mutate: func(object *Object) { object.ModifiedAt = object.ModifiedAt.Add(time.Second) }},
		{name: "trashed", mutate: func(object *Object) { object.Trashed = true }},
		{name: "depth", mutate: func(object *Object) { object.Depth++ }},
		{name: "parent_id", mutate: func(object *Object) { object.ParentID = "drifted-parent" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, rootSet, inventory := manifestCapture(t)
			if test.name == "parent_id" || test.name == "depth" {
				test.mutate(&manifest.Objects[0])
			} else {
				test.mutate(&rootSet.Roots[0].Pages[0].Objects[0])
			}
			var err error
			rootSet, err = FreezeRootSet(rootSet)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err = BuildAggregate(rootSet, manifest.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			manifest.SourceInventoryHash = inventory.InventoryHash
			if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "metadata") {
				t.Fatalf("metadata drift error = %v, want rejection", err)
			}
		})
	}
}

func TestValidateManifestAgainstInventoryRejectsRetainedPeerSelectionAndUnsafePeer(t *testing.T) {
	manifest, rootSet, inventory := manifestCapture(t)
	manifest.Objects[0].RetainedPeerID = manifest.Objects[1].ID
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("selected retained peer error = %v", err)
	}

	manifest, rootSet, inventory = manifestCapture(t)
	inventory.Objects[len(inventory.Objects)-1].Class = ClassQuarantined
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("tampered peer aggregate error = %v", err)
	}
}

func TestValidateManifestAgainstInventoryRejectsHashAndScopeDrift(t *testing.T) {
	manifest, rootSet, inventory := manifestCapture(t)
	inventory.InventoryHash = strings.Repeat("f", 64)
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("expected inventory hash rejection, got %v", err)
	}
	manifest, rootSet, inventory = manifestCapture(t)
	manifest.AccountID = "foreign-account"
	if _, err := ValidateManifestAgainstInventory(manifest, rootSet, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account scope rejection, got %v", err)
	}
}
