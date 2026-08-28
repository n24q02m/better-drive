package cleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const CurrentSchemaVersion = 2

const (
	ModeQuarantine Mode = "quarantine"
)

type Mode string

type ObjectType string

const (
	ObjectTypeFile   ObjectType = "file"
	ObjectTypeFolder ObjectType = "folder"
)


type ObjectClass string

const (
	ClassActive            ObjectClass = "active"
	ClassDuplicateSameHash ObjectClass = "duplicate_same_hash"
	ClassOrphan            ObjectClass = "orphan"
	ClassLegacyUnmarked    ObjectClass = "legacy_unmarked"
	ClassQuarantined       ObjectClass = "quarantined"
	ClassExpectedFixture   ObjectClass = "expected_fixture"
	ClassLegacyRetained    ObjectClass = "legacy_retained"
	ClassConflict          ObjectClass = "conflict"
	ClassUnknown           ObjectClass = "unknown"
)

type Budget struct {
	MaxObjects int   `json:"max_objects"`
	MaxBytes   int64 `json:"max_bytes"`
}

type Object struct {
	ID                  string      `json:"id"`
	ParentID            string      `json:"parent_id"`
	Name                string      `json:"name"`
	Path                string      `json:"path"`
	ObjectType          ObjectType  `json:"object_type"`
	ContentHash         string      `json:"content_hash"`
	Size                int64       `json:"size"`
	Provider            string      `json:"provider"`
	AccountID           string      `json:"account_id"`
	RootID              string      `json:"root_id"`
	Namespace           string      `json:"namespace"`
	Version             string      `json:"version"`
	Generation          string      `json:"generation"`
	ETag                string      `json:"etag"`
	ModifiedAt          time.Time   `json:"modified_at"`
	Trashed             bool        `json:"trashed"`
	Depth               int         `json:"depth"`
	ChildrenComplete    bool        `json:"children_complete"`
	ChildCount          int         `json:"child_count"`
	SubtreeComplete     bool        `json:"subtree_complete"`
	SubtreeObjectCount  int         `json:"subtree_object_count"`
	Class               ObjectClass `json:"class"`
	SubtreeWriterFence  string      `json:"subtree_writer_fence,omitempty"`
	EmptyCheckIDs       []string    `json:"empty_check_ids,omitempty"`
	RetainedPeerID      string      `json:"retained_peer_id,omitempty"`
	OwnershipMarker     string      `json:"ownership_marker,omitempty"`
	RestoreEvidence     string      `json:"restore_evidence,omitempty"`
}

type Manifest struct {
	SchemaVersion       int       `json:"schema_version"`
	ManifestID          string    `json:"manifest_id"`
	AccountID           string    `json:"account_id"`
	RootID              string    `json:"root_id"`
	Namespace           string    `json:"namespace"`
	Mode                Mode      `json:"mode"`
	CreatedAt           time.Time `json:"created_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	Nonce               string    `json:"nonce"`
	Budget              Budget    `json:"budget"`
	SourceInventoryHash string    `json:"source_inventory_hash"`
	FixtureDigest       string    `json:"fixture_digest,omitempty"`
	Objects             []Object  `json:"objects"`
}

type Validation struct {
	ManifestDigest string `json:"manifest_digest"`
	ObjectCount    int    `json:"object_count"`
	ByteCount      int64  `json:"byte_count"`
	Mode           Mode   `json:"mode"`
	ExpiresAt      string `json:"expires_at"`
}

func CanonicalManifest(manifest Manifest) ([]byte, error) {
	copyManifest := manifest
	copyManifest.Objects = append([]Object(nil), manifest.Objects...)
	sort.Slice(copyManifest.Objects, func(i, j int) bool {
		return objectKey(copyManifest.Objects[i]) < objectKey(copyManifest.Objects[j])
	})
	return json.Marshal(copyManifest)
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ValidateManifest(manifest Manifest, now time.Time) (Validation, error) {
	if manifest.SchemaVersion != CurrentSchemaVersion {
		return Validation{}, fmt.Errorf("unsupported manifest schema_version %d", manifest.SchemaVersion)
	}
	for name, value := range map[string]string{
		"manifest_id":           manifest.ManifestID,
		"account_id":            manifest.AccountID,
		"root_id":               manifest.RootID,
		"namespace":             manifest.Namespace,
		"nonce":                 manifest.Nonce,
		"source_inventory_hash": manifest.SourceInventoryHash,
	} {
		if strings.TrimSpace(value) == "" {
			return Validation{}, fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "*?[]") {
			return Validation{}, fmt.Errorf("%s must not contain wildcard characters", name)
		}
	}
	if manifest.Mode != ModeQuarantine {
		return Validation{}, fmt.Errorf("unsupported cleanup mode %q; only quarantine is supported", manifest.Mode)
	}
	if manifest.CreatedAt.IsZero() || manifest.ExpiresAt.IsZero() || !manifest.ExpiresAt.After(manifest.CreatedAt) {
		return Validation{}, errors.New("manifest created_at and expires_at must form a positive interval")
	}
	if !now.IsZero() && !now.Before(manifest.ExpiresAt) {
		return Validation{}, fmt.Errorf("manifest expired at %s", manifest.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if manifest.Budget.MaxObjects <= 0 || manifest.Budget.MaxBytes <= 0 {
		return Validation{}, errors.New("manifest budgets must be positive")
	}
	if len(manifest.Objects) == 0 {
		return Validation{}, errors.New("manifest must contain at least one object")
	}
	seen := make(map[string]Object, len(manifest.Objects))
	var totalBytes int64
	for i, object := range manifest.Objects {
		if err := validateObject(manifest, object); err != nil {
			return Validation{}, fmt.Errorf("object %d: %w", i, err)
		}
		key := objectKey(object)
		if _, exists := seen[key]; exists {
			return Validation{}, fmt.Errorf("duplicate object ID %q in canonical provider context", object.ID)
		}
		seen[key] = object
		if totalBytes > maxInt64-object.Size {
			return Validation{}, fmt.Errorf("object byte count overflow")
		}
		totalBytes += object.Size
	}
	for _, object := range manifest.Objects {
		if object.Class != ClassDuplicateSameHash {
			continue
		}
		peerKey := strings.Join([]string{object.Provider, object.AccountID, object.RetainedPeerID}, "\x00")
		if _, selected := seen[peerKey]; selected {
			return Validation{}, fmt.Errorf("retained peer %q must be absent from selected objects", object.RetainedPeerID)
		}
	}
	if len(manifest.Objects) > manifest.Budget.MaxObjects {
		return Validation{}, fmt.Errorf("object budget exceeded: %d > %d", len(manifest.Objects), manifest.Budget.MaxObjects)
	}
	if totalBytes > manifest.Budget.MaxBytes {
		return Validation{}, fmt.Errorf("byte budget exceeded: %d > %d", totalBytes, manifest.Budget.MaxBytes)
	}
	canonical, err := CanonicalManifest(manifest)
	if err != nil {
		return Validation{}, fmt.Errorf("canonicalize manifest: %w", err)
	}
	return Validation{
		ManifestDigest: Digest(canonical),
		ObjectCount:    len(manifest.Objects),
		ByteCount:      totalBytes,
		Mode:           manifest.Mode,
		ExpiresAt:      manifest.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func ValidateManifestAgainstInventory(manifest Manifest, rootSet RootSet, inventory InventoryAggregate, now time.Time) (Validation, error) {
	validation, err := ValidateManifest(manifest, now)
	if err != nil {
		return Validation{}, err
	}
	verified, err := validateAggregateCapture(rootSet, inventory, manifest.AccountID)
	if err != nil {
		return Validation{}, err
	}
	if verified.AccountID != manifest.AccountID {
		return Validation{}, errors.New("inventory account does not match manifest account")
	}
	if verified.InventoryHash != manifest.SourceInventoryHash {
		return Validation{}, errors.New("inventory hash does not match manifest source_inventory_hash")
	}
	objects := make(map[string]Object, len(verified.Objects))
	for _, object := range verified.Objects {
		key := objectKey(object)
		if _, exists := objects[key]; exists {
			return Validation{}, fmt.Errorf("duplicate inventory object %q", key)
		}
		objects[key] = object
	}
	selected := make(map[string]struct{}, len(manifest.Objects))
	for _, object := range manifest.Objects {
		selected[objectKey(object)] = struct{}{}
	}
	for _, selectedObject := range manifest.Objects {
		observed, exists := objects[objectKey(selectedObject)]
		if !exists {
			return Validation{}, fmt.Errorf("manifest object %q is absent from inventory", selectedObject.ID)
		}
		if observed.Provider != selectedObject.Provider ||
			observed.AccountID != selectedObject.AccountID ||
			observed.RootID != selectedObject.RootID ||
			observed.Namespace != selectedObject.Namespace ||
			observed.Name != selectedObject.Name ||
			observed.Path != selectedObject.Path ||
			observed.ObjectType != selectedObject.ObjectType ||
			observed.ContentHash != selectedObject.ContentHash ||
			observed.Size != selectedObject.Size ||
			observed.Version != selectedObject.Version ||
			observed.Generation != selectedObject.Generation ||
			observed.ETag != selectedObject.ETag ||
			!observed.ModifiedAt.Equal(selectedObject.ModifiedAt) ||
			observed.Trashed != selectedObject.Trashed ||
			observed.Depth != selectedObject.Depth ||
			observed.ParentID != selectedObject.ParentID ||
			observed.ChildrenComplete != selectedObject.ChildrenComplete ||
			observed.ChildCount != selectedObject.ChildCount ||
			observed.SubtreeComplete != selectedObject.SubtreeComplete ||
			observed.SubtreeObjectCount != selectedObject.SubtreeObjectCount {
			return Validation{}, fmt.Errorf("object %q metadata drifted from inventory", selectedObject.ID)
		}
	}
	for _, selectedObject := range manifest.Objects {
		if selectedObject.Class != ClassDuplicateSameHash {
			continue
		}
		peerKey := strings.Join([]string{selectedObject.Provider, selectedObject.AccountID, selectedObject.RetainedPeerID}, "\x00")
		if _, selectedPeer := selected[peerKey]; selectedPeer {
			return Validation{}, fmt.Errorf("retained peer %q must be absent from selected objects", selectedObject.RetainedPeerID)
		}
		peer, exists := objects[peerKey]
		if !exists {
			return Validation{}, fmt.Errorf("retained peer %q is absent from verified inventory", selectedObject.RetainedPeerID)
		}
		if peer.Trashed || peer.Class == ClassQuarantined {
			return Validation{}, fmt.Errorf("retained peer %q is trashed or quarantined", peer.ID)
		}
		if peer.Class != ClassActive && peer.Class != ClassExpectedFixture && peer.Class != ClassLegacyRetained {
			return Validation{}, fmt.Errorf("retained peer %q has unsafe class %q", peer.ID, peer.Class)
		}
		if peer.ContentHash != selectedObject.ContentHash || peer.Size != selectedObject.Size ||
			peer.Provider != selectedObject.Provider || peer.AccountID != selectedObject.AccountID ||
			peer.RootID != selectedObject.RootID || peer.Namespace != selectedObject.Namespace {
			return Validation{}, fmt.Errorf("retained peer %q metadata does not match duplicate %q", peer.ID, selectedObject.ID)
		}
		if strings.TrimSpace(peer.OwnershipMarker) == "" || strings.TrimSpace(peer.RestoreEvidence) == "" {
			return Validation{}, fmt.Errorf("retained peer %q lacks ownership and restore evidence", peer.ID)
		}
	}
	return validation, nil
}

func validateObject(manifest Manifest, object Object) error {
	for name, value := range map[string]string{
		"id": object.ID, "parent_id": object.ParentID, "name": object.Name, "path": object.Path,
		"provider": object.Provider, "account_id": object.AccountID, "root_id": object.RootID,
		"namespace": object.Namespace, "version": object.Version, "generation": object.Generation,
		"etag": object.ETag,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "*?[]") {
			return fmt.Errorf("%s must not contain wildcard characters", name)
		}
	}
	if object.ModifiedAt.IsZero() {
		return errors.New("modified_at is required")
	}
	if object.Size < 0 {
		return errors.New("size must not be negative")
	}
	if object.Depth < 0 {
		return errors.New("depth must not be negative")
	}
	if object.ChildCount < 0 || object.SubtreeObjectCount < 0 {
		return errors.New("folder child counts must not be negative")
	}
	if object.AccountID != manifest.AccountID || object.RootID != manifest.RootID || object.Namespace != manifest.Namespace {
		return errors.New("provider/account/root/namespace does not match manifest scope")
	}
	if object.Trashed {
		return errors.New("trashed objects are not mutation-eligible")
	}
	if object.Class != ClassDuplicateSameHash && object.Class != ClassOrphan && object.Class != ClassLegacyUnmarked {
		return fmt.Errorf("class %q is not mutation-eligible", object.Class)
	}
	if object.Class == ClassDuplicateSameHash && object.RetainedPeerID == "" {
		return errors.New("duplicate_same_hash requires retained_peer_id")
	}
	if object.OwnershipMarker == "" {
		return errors.New("ownership marker is required")
	}
	if object.RestoreEvidence == "" {
		return errors.New("restore evidence is required")
	}
	switch object.ObjectType {
	case ObjectTypeFile:
		if strings.TrimSpace(object.ContentHash) == "" {
			return errors.New("content_hash is required for files")
		}
		if object.ChildrenComplete || object.ChildCount != 0 || object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return errors.New("file contains folder subtree metadata")
		}
	case ObjectTypeFolder:
		if object.Size != 0 {
			return errors.New("folder size must be zero")
		}
		if !object.ChildrenComplete || object.ChildCount != 0 || !object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return errors.New("folder is not proven empty by complete traversal")
		}
		if strings.TrimSpace(object.SubtreeWriterFence) == "" {
			return errors.New("empty folder requires subtree writer fence")
		}
		if len(object.EmptyCheckIDs) != 2 ||
			strings.TrimSpace(object.EmptyCheckIDs[0]) == "" ||
			strings.TrimSpace(object.EmptyCheckIDs[1]) == "" ||
			object.EmptyCheckIDs[0] >= object.EmptyCheckIDs[1] {
			return errors.New("empty folder requires two sorted unique empty checks")
		}
	default:
		return fmt.Errorf("object_type %q is unsupported", object.ObjectType)
	}
	return nil
}

func objectKey(object Object) string {
	return physicalObjectKey(object)
}

func DecodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode cleanup manifest: %w", err)
	}
	return manifest, nil
}
