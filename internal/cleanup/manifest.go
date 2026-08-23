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

const CurrentSchemaVersion = 1

const (
	ModeQuarantine Mode = "quarantine"
	ModeTrash      Mode = "trash"
)

type Mode string

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
	ID              string      `json:"id"`
	ParentID        string      `json:"parent_id"`
	Name            string      `json:"name"`
	ContentHash     string      `json:"content_hash"`
	Size            int64       `json:"size"`
	Provider        string      `json:"provider"`
	AccountID       string      `json:"account_id"`
	RootID          string      `json:"root_id"`
	Namespace       string      `json:"namespace"`
	Version         string      `json:"version"`
	ETag            string      `json:"etag"`
	Class           ObjectClass `json:"class"`
	RetainedPeerID  string      `json:"retained_peer_id,omitempty"`
	OwnershipMarker string      `json:"ownership_marker,omitempty"`
	RestoreEvidence string      `json:"restore_evidence,omitempty"`
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
	if manifest.Mode != ModeQuarantine && manifest.Mode != ModeTrash {
		return Validation{}, fmt.Errorf("unsupported cleanup mode %q", manifest.Mode)
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
		totalBytes += object.Size
	}
	for _, object := range manifest.Objects {
		if object.Class != ClassDuplicateSameHash {
			continue
		}
		peerKey := strings.Join([]string{object.Provider, object.AccountID, object.RootID, object.Namespace, object.RetainedPeerID}, "\x00")
		peer, exists := seen[peerKey]
		if !exists {
			return Validation{}, fmt.Errorf("retained peer %q is not present in manifest", object.RetainedPeerID)
		}
		if peer.ContentHash != object.ContentHash {
			return Validation{}, fmt.Errorf("retained peer %q content hash does not match duplicate %q", object.RetainedPeerID, object.ID)
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

func validateObject(manifest Manifest, object Object) error {
	for name, value := range map[string]string{
		"id": object.ID, "name": object.Name, "provider": object.Provider,
		"account_id": object.AccountID, "root_id": object.RootID, "namespace": object.Namespace,
		"content_hash": object.ContentHash, "version": object.Version, "etag": object.ETag,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.ContainsAny(value, "*?[]") {
			return fmt.Errorf("%s must not contain wildcard characters", name)
		}
	}
	if object.Size < 0 {
		return errors.New("size must not be negative")
	}
	if object.AccountID != manifest.AccountID || object.RootID != manifest.RootID || object.Namespace != manifest.Namespace {
		return errors.New("provider/account/root/namespace does not match manifest scope")
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
	return nil
}

func objectKey(object Object) string {
	return strings.Join([]string{object.Provider, object.AccountID, object.RootID, object.Namespace, object.ID}, "\x00")
}

func DecodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode cleanup manifest: %w", err)
	}
	return manifest, nil
}
