package cleanup

import (
	"strings"
	"testing"
	"time"
)

func TestValidateManifestAgainstInventoryBindsExactMetadata(t *testing.T) {
	manifest := validManifest()
	inventory := InventoryAggregate{
		SchemaVersion: CurrentInventorySchemaVersion,
		Status:        InventoryComplete,
		AccountID:     manifest.AccountID,
		InventoryHash: manifest.SourceInventoryHash,
		Objects: []Object{
			manifest.Objects[0],
			manifest.Objects[1],
		},
	}
	if _, err := ValidateManifestAgainstInventory(manifest, inventory, time.Unix(150, 0).UTC()); err != nil {
		t.Fatalf("ValidateManifestAgainstInventory() error = %v", err)
	}
	inventory.Objects[0].ETag = "foreign-etag"
	if _, err := ValidateManifestAgainstInventory(manifest, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("expected metadata drift rejection, got %v", err)
	}
}

func TestValidateManifestAgainstInventoryRejectsHashAndScopeDrift(t *testing.T) {
	manifest := validManifest()
	inventory := InventoryAggregate{SchemaVersion: CurrentInventorySchemaVersion, Status: InventoryComplete, AccountID: manifest.AccountID, InventoryHash: strings.Repeat("f", 64), Objects: manifest.Objects}
	if _, err := ValidateManifestAgainstInventory(manifest, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "inventory hash") {
		t.Fatalf("expected inventory hash rejection, got %v", err)
	}
	inventory.InventoryHash = manifest.SourceInventoryHash
	inventory.AccountID = "foreign-account"
	if _, err := ValidateManifestAgainstInventory(manifest, inventory, time.Unix(150, 0).UTC()); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account scope rejection, got %v", err)
	}
}
