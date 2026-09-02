package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
)

func digestBytes(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type testArtifactResolver map[artifactcrypto.KeyReference][]byte

func (r testArtifactResolver) Resolve(reference artifactcrypto.KeyReference) ([]byte, error) {
	key, ok := r[reference]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), key...), nil
}

type testStagingVerifier struct {
	calls  int
	mutate func(StagingEvidence, int) StagingEvidence
}

func (v *testStagingVerifier) Verify(_ string, identity RootIdentity) (StagingEvidence, error) {
	v.calls++
	evidence := StagingEvidence{
		RootIdentity:       identity,
		ProofDigest:        digestBytes(identity.Path + "|staging-proof"),
		ProofID:            "proof-1",
		VerifiedAt:         time.Unix(1700000000, 0).UTC(),
		EncryptedAtRest:    true,
		OwnerOnly:          true,
		ExcludedFromBackup: true,
		NonInheritedACL:    true,
	}
	if v.mutate != nil {
		evidence = v.mutate(evidence, v.calls)
	}
	return evidence, nil
}

type testSourceProvider struct {
	artifacts map[string][]byte
}

func (p *testSourceProvider) Open(_ context.Context, ref SourceReference) (io.ReadCloser, SourceReadback, error) {
	key := fmt.Sprintf("%s/%s/%s/%s", ref.Provider, ref.AccountID, ref.ObjectID, ref.Version)
	data, ok := p.artifacts[key]
	if !ok {
		return nil, SourceReadback{}, fmt.Errorf("source artifact %q not found in test provider", key)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	readback := SourceReadback{
		Reference:        ref,
		Size:             int64(len(data)),
		CiphertextDigest: digest,
		Version:          ref.Version,
	}
	return io.NopCloser(bytes.NewReader(data)), readback, nil
}

type testCheckpointVerifier struct {
	err error
}

func (v *testCheckpointVerifier) Verify(_ context.Context, checkpoint MachineCheckpoint, intent ApplyIntent) error {
	if v.err != nil {
		return v.err
	}
	if checkpoint.Signature != "valid-sig" {
		return fmt.Errorf("invalid checkpoint signature")
	}
	return nil
}

type testCleanupVerifier struct {
	err error
}

func (v *testCleanupVerifier) Verify(_ context.Context, claim CleanupClaim, intent CleanupIntent) error {
	if v.err != nil {
		return v.err
	}
	if claim.Signature != "valid-claim-sig" {
		return fmt.Errorf("invalid cleanup claim signature")
	}
	return nil
}

func sealedEntry(t *testing.T, relative string, payload []byte, metadata artifactcrypto.Metadata, resolver artifactcrypto.Resolver) Entry {
	t.Helper()
	source := filepath.Join(t.TempDir(), "artifact.bin")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	result, sealErr := artifactcrypto.Seal(file, bytes.NewReader(payload), resolver, metadata)
	closeErr := file.Close()
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return Entry{
		RelativePath:     relative,
		SourcePath:       source,
		ArtifactMetadata: metadata,
		PlaintextDigest:  result.PlaintextDigest,
		CiphertextDigest: result.CiphertextDigest,
		PlaintextSize:    int64(len(payload)),
	}
}

func sealedProviderEntry(t *testing.T, relative string, payload []byte, metadata artifactcrypto.Metadata, resolver artifactcrypto.Resolver, provider *testSourceProvider, ref SourceReference) Entry {
	t.Helper()
	var sealed bytes.Buffer
	result, err := artifactcrypto.Seal(&sealed, bytes.NewReader(payload), resolver, metadata)
	if err != nil {
		t.Fatal(err)
	}
	ref.Size = int64(sealed.Len())
	ref.CiphertextDigest = result.CiphertextDigest
	key := fmt.Sprintf("%s/%s/%s/%s", ref.Provider, ref.AccountID, ref.ObjectID, ref.Version)
	if provider.artifacts == nil {
		provider.artifacts = make(map[string][]byte)
	}
	provider.artifacts[key] = sealed.Bytes()
	return Entry{
		RelativePath:     relative,
		SourceReference:  &ref,
		ArtifactMetadata: metadata,
		PlaintextDigest:  result.PlaintextDigest,
		CiphertextDigest: result.CiphertextDigest,
		PlaintextSize:    int64(len(payload)),
	}
}

func TestBuildPlanRejectsTraversalAbsoluteAndDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"C:/escape", "/absolute", "../escape", "a/../../escape", "secret:stream",
		"archive.zip#inside.txt", "CON", "NUL", "COM1", "sub/AUX/file.txt",
	} {
		if _, err := BuildPlan(root, []Entry{{RelativePath: relative}}); err == nil {
			t.Errorf("BuildPlan accepted unsafe path %q", relative)
		}
	}
	if _, err := BuildPlan(root, []Entry{{RelativePath: "a.txt"}, {RelativePath: "a.txt"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("BuildPlan accepted duplicate destination path")
	}
}

func TestBuildPlanComputesCapacityAndConflictEvidence(t *testing.T) {
	root := t.TempDir()
	conflictPath := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(conflictPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{RelativePath: "existing.txt", PlaintextSize: 100},
		{RelativePath: "new.txt", PlaintextSize: 250},
	}
	plan, err := BuildPlan(root, entries)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.CapacityBytes != 350 {
		t.Fatalf("CapacityBytes = %d, want 350", plan.CapacityBytes)
	}
	if plan.TotalObjects != 2 {
		t.Fatalf("TotalObjects = %d, want 2", plan.TotalObjects)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0] != "existing.txt" {
		t.Fatalf("Conflicts = %#v, want [existing.txt]", plan.Conflicts)
	}
}

func TestCaptureRootIdentityRejectsDirectSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	parent := t.TempDir()
	supplied := filepath.Join(parent, "root-link")
	if err := os.Symlink(target, supplied); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := CaptureRootIdentity(supplied); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("CaptureRootIdentity accepted direct symlink root: %v", err)
	}
}

func TestCaptureRootIdentityRejectsIntermediateSymlinkRoot(t *testing.T) {
	realParent := t.TempDir()
	realRoot := filepath.Join(realParent, "restore")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(t.TempDir(), "parent-link")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	supplied := filepath.Join(linkParent, "restore")
	if _, err := CaptureRootIdentity(supplied); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("CaptureRootIdentity accepted intermediate symlink root: %v", err)
	}
}

func TestBuildPlanValidatesExecutableEnvelopeFields(t *testing.T) {
	root := t.TempDir()
	entry := Entry{RelativePath: "state.txt", SourcePath: filepath.Join(t.TempDir(), "artifact.bin")}
	if _, err := BuildPlan(root, []Entry{entry}); err == nil || !strings.Contains(err.Error(), "plaintext_digest") {
		t.Fatalf("BuildPlan accepted incomplete executable entry: %v", err)
	}
}

func TestStageFileWithTypedSourceProvider(t *testing.T) {
	root := t.TempDir()
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-prov", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	provider := &testSourceProvider{}
	ref := SourceReference{
		Provider:  "drive",
		AccountID: "acct-1",
		ObjectID:  "obj-state",
		Version:   "v1",
	}
	entry := sealedProviderEntry(t, "category/state.txt", []byte("provider-restore-data"), metadata, resolver, provider, ref)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := StageFileWithProvider(context.Background(), plan, entry, resolver, &testStagingVerifier{}, provider); err != nil {
		t.Fatalf("StageFileWithProvider: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "category", "state.txt"))
	if err != nil || string(got) != "provider-restore-data" {
		t.Fatalf("staged provider data = %q, err=%v", got, err)
	}
}

func TestStageFileWritesAuthenticatedEnvelopeCreateOnly(t *testing.T) {
	root := t.TempDir()
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := sealedEntry(t, "category/state.txt", []byte("restore-data"), metadata, resolver)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := StageFile(plan, entry, resolver, &testStagingVerifier{}); err != nil {
		t.Fatalf("StageFile: %v", err)
	}
	if err := StageFile(plan, entry, resolver, &testStagingVerifier{}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal("StageFile overwrote an existing destination")
	}
	got, err := os.ReadFile(filepath.Join(root, "category", "state.txt"))
	if err != nil || string(got) != "restore-data" {
		t.Fatalf("staged data = %q err=%v", got, err)
	}
}

func TestStageFileRejectsUnverifiedStagingWithoutResidue(t *testing.T) {
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	for _, tc := range []struct {
		name   string
		mutate func(StagingEvidence, int) StagingEvidence
	}{
		{name: "encrypted at rest", mutate: func(e StagingEvidence, _ int) StagingEvidence { e.EncryptedAtRest = false; return e }},
		{name: "owner only", mutate: func(e StagingEvidence, _ int) StagingEvidence { e.OwnerOnly = false; return e }},
		{name: "excluded from backup", mutate: func(e StagingEvidence, _ int) StagingEvidence { e.ExcludedFromBackup = false; return e }},
		{name: "non inherited acl", mutate: func(e StagingEvidence, _ int) StagingEvidence { e.NonInheritedACL = false; return e }},
		{name: "proof digest", mutate: func(e StagingEvidence, _ int) StagingEvidence { e.ProofDigest = "sha256:ABC"; return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "non inherited acl" && runtime.GOOS != "windows" {
				t.Skip("Windows-only ACL evidence")
			}
			root := t.TempDir()
			entry := sealedEntry(t, "state.txt", []byte("restore-data"), metadata, resolver)
			plan, err := BuildPlan(root, []Entry{entry})
			if err != nil {
				t.Fatal(err)
			}
			verifier := &testStagingVerifier{mutate: tc.mutate}
			if err := StageFile(plan, entry, resolver, verifier); err == nil {
				t.Fatal("StageFile accepted unverified staging evidence")
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("staging root residue after preflight failure: %#v", entries)
			}
		})
	}
}

func TestStageFileRejectsEvidenceDriftBeforeTempCreation(t *testing.T) {
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	root := t.TempDir()
	entry := sealedEntry(t, "state.txt", []byte("restore-data"), metadata, resolver)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &testStagingVerifier{mutate: func(e StagingEvidence, calls int) StagingEvidence {
		if calls >= 2 {
			e.RootIdentity.Token = "drifted"
		}
		return e
	}}
	if err := StageFile(plan, entry, resolver, verifier); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("StageFile evidence drift error = %v", err)
	}
	if verifier.calls != 2 {
		t.Fatalf("verifier calls = %d, want pre-open and pre-temp checks", verifier.calls)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging root residue after evidence drift: %#v", entries)
	}
}

func TestStageAndReplaceWithRollbackSnapshot(t *testing.T) {
	root := t.TempDir()
	destPath := filepath.Join(root, "replace.txt")
	if err := os.WriteFile(destPath, []byte("original-destination-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-rep", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := sealedEntry(t, "replace.txt", []byte("new-replaced-content"), metadata, resolver)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	rollbackDir := filepath.Join(root, ".restore-rollback", "tx-test")
	rollbackSnapshot, err := StageAndReplaceFile(context.Background(), plan, entry, resolver, &testStagingVerifier{}, nil, rollbackDir)
	if err != nil {
		t.Fatalf("StageAndReplaceFile: %v", err)
	}
	// Verify rollback snapshot has original content.
	orig, err := os.ReadFile(rollbackSnapshot)
	if err != nil || string(orig) != "original-destination-content" {
		t.Fatalf("rollback snapshot = %q err=%v", orig, err)
	}
	// Verify destination has new content.
	got, err := os.ReadFile(destPath)
	if err != nil || string(got) != "new-replaced-content" {
		t.Fatalf("replaced destination = %q err=%v", got, err)
	}
	// Now recover from rollback.
	records := []JournalRecord{
		{
			TransactionID:   "tx-test",
			Entry:           "replace.txt",
			Action:          "replace",
			Before:          digestBytes("original-destination-content"),
			After:           "replaced",
			PlaintextDigest: entry.PlaintextDigest,
			RollbackPath:    rollbackSnapshot,
		},
	}
	if err := RecoverWithIdentity(root, plan.RootIdentity, records); err != nil {
		t.Fatalf("RecoverWithIdentity replace: %v", err)
	}
	restored, err := os.ReadFile(destPath)
	if err != nil || string(restored) != "original-destination-content" {
		t.Fatalf("restored content = %q err=%v", restored, err)
	}
}

func TestMachineCheckpointVerification(t *testing.T) {
	root := t.TempDir()
	plan, err := BuildPlan(root, []Entry{{RelativePath: "a.txt", PlaintextSize: 100}})
	if err != nil {
		t.Fatal(err)
	}
	intent := ApplyIntent{
		Plan:          plan,
		CapacityBytes: 100,
		TotalObjects:  1,
	}
	cp := MachineCheckpoint{
		ID:            "cp-1",
		Kind:          CheckpointKindCreate,
		Root:          plan.Root,
		RootIdentity:  plan.RootIdentity,
		Entries:       []string{"a.txt"},
		CapacityBytes: 100,
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		Signature:     "valid-sig",
	}
	verifier := &testCheckpointVerifier{}
	if err := VerifyCheckpoint(context.Background(), cp, intent, verifier); err != nil {
		t.Fatalf("VerifyCheckpoint: %v", err)
	}

	// Tampered entry in checkpoint
	badCP := cp
	badCP.Entries = []string{"other.txt"}
	if err := VerifyCheckpoint(context.Background(), badCP, intent, verifier); err == nil {
		t.Fatal("VerifyCheckpoint accepted checkpoint with wrong entries")
	}

	// Insufficient capacity
	lowCapCP := cp
	lowCapCP.CapacityBytes = 50
	if err := VerifyCheckpoint(context.Background(), lowCapCP, intent, verifier); err == nil {
		t.Fatal("VerifyCheckpoint accepted checkpoint with insufficient capacity")
	}

	// Expired checkpoint
	expCP := cp
	expCP.ExpiresAt = time.Now().Add(-1 * time.Hour)
	if err := VerifyCheckpoint(context.Background(), expCP, intent, verifier); err == nil {
		t.Fatal("VerifyCheckpoint accepted expired checkpoint")
	}
}

func TestCleanupClaimAndPlaintextTTLAlerts(t *testing.T) {
	root := t.TempDir()
	stagingDir, rollbackDir := StagingRollbackDirs(root)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedFile := filepath.Join(stagingDir, "test.tmp")
	if err := os.WriteFile(stagedFile, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fresh file is within TTL.
	if err := CheckPlaintextTTL(root, 24*time.Hour); err != nil {
		t.Fatalf("CheckPlaintextTTL fresh error: %v", err)
	}

	// Simulated stale file older than TTL.
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stagedFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := CheckPlaintextTTL(root, 24*time.Hour); err == nil || !strings.Contains(err.Error(), "pending_cleanup") {
		t.Fatalf("CheckPlaintextTTL old file error = %v, want pending_cleanup", err)
	}

	// Cleanup with verified claim.
	identity, err := CaptureRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	claim := CleanupClaim{
		ID:           "claim-1",
		Root:         root,
		RootIdentity: identity,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		Signature:    "valid-claim-sig",
	}
	intent := CleanupIntent{
		Root:           root,
		RootIdentity:   identity,
		TransactionIDs: []string{"tx-1"},
		PlaintextPaths: []string{".restore-staging/test.tmp"},
		ExpiresAt:      time.Now().Add(1 * time.Hour),
	}
	cleanupVerifier := &testCleanupVerifier{}
	if err := CleanupPlaintextWithClaim(context.Background(), intent, claim, cleanupVerifier); err != nil {
		t.Fatalf("CleanupPlaintextWithClaim: %v", err)
	}
	if _, err := os.Lstat(stagedFile); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists after verified cleanup: %v", err)
	}
}

func TestWorkstationAndOCIArtifactFixtures(t *testing.T) {
	// Tests fixtures for Claude, Codex, OpenCode, VS Code Insiders, CurseForge, and OCI artifacts.
	categories := []struct {
		name         string
		relativePath string
		restoreSetID string
		component    string
		keyRef       string
		payload      []byte
	}{
		{
			name:         "Claude config and session state",
			relativePath: "claude/config.json",
			restoreSetID: "claude-set-1",
			component:    "config",
			keyRef:       "claude-key",
			payload:      []byte(`{"user":"n24q02m","auth":{"token":"redacted"}}`),
		},
		{
			name:         "Codex persistent session state",
			relativePath: "codex/sessions/2026-08-29.jsonl",
			restoreSetID: "codex-set-1",
			component:    "session",
			keyRef:       "codex-key",
			payload:      []byte(`{"event":"step","tool":"execute"}`),
		},
		{
			name:         "OpenCode worker state",
			relativePath: "opencode/db.sqlite",
			restoreSetID: "opencode-set-1",
			component:    "db",
			keyRef:       "opencode-key",
			payload:      []byte(`SQLite format 3...binary payload`),
		},
		{
			name:         "VS Code Insiders state",
			relativePath: "vscode-insiders/settings.json",
			restoreSetID: "vscode-set-1",
			component:    "settings",
			keyRef:       "vscode-key",
			payload:      []byte(`{"editor.tabSize": 4}`),
		},
		{
			name:         "CurseForge Minecraft modpack data",
			relativePath: "minecraft/saves/world1/level.dat",
			restoreSetID: "minecraft-set-1",
			component:    "save",
			keyRef:       "minecraft-key",
			payload:      []byte(`NBT-level-data-payload`),
		},
		{
			name:         "OCI combined stack database dump",
			relativePath: "oci/dumps/postgres.sql",
			restoreSetID: "oci-stack-set-1",
			component:    "database",
			keyRef:       "oci-db-key",
			payload:      []byte(`-- PostgreSQL database dump...`),
		},
	}

	root := t.TempDir()
	resolver := make(testArtifactResolver)
	provider := &testSourceProvider{artifacts: make(map[string][]byte)}
	entries := make([]Entry, 0, len(categories))

	for i, c := range categories {
		meta := artifactcrypto.Metadata{
			RestoreSetID: c.restoreSetID,
			Component:    c.component,
			KeyRef:       c.keyRef,
			KeyVersion:   uint64(i + 1),
		}
		key := []byte(fmt.Sprintf("%-32.32s", "key-"+c.component))
		resolver[meta.Reference()] = key
		ref := SourceReference{
			Provider:  "r2",
			AccountID: "oci-account",
			ObjectID:  fmt.Sprintf("obj-%s", c.component),
			Version:   "v1",
		}
		entry := sealedProviderEntry(t, c.relativePath, c.payload, meta, resolver, provider, ref)
		entries = append(entries, entry)
	}

	plan, err := BuildPlan(root, entries)
	if err != nil {
		t.Fatalf("BuildPlan fixtures: %v", err)
	}
	if plan.TotalObjects != len(categories) {
		t.Fatalf("TotalObjects = %d, want %d", plan.TotalObjects, len(categories))
	}

	for _, entry := range entries {
		if err := StageFileWithProvider(context.Background(), plan, entry, resolver, &testStagingVerifier{}, provider); err != nil {
			t.Fatalf("StageFileWithProvider fixture %q: %v", entry.RelativePath, err)
		}
	}

	// Verify all staged contents.
	for _, c := range categories {
		dest := filepath.Join(root, filepath.FromSlash(c.relativePath))
		got, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(got, c.payload) {
			t.Fatalf("fixture %q data mismatch: got %q, want %q, err=%v", c.name, got, c.payload, err)
		}
	}
}

func TestSwapRaceRefusalBeforeRename(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata := artifactcrypto.Metadata{RestoreSetID: "set-race", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := sealedEntry(t, "sub/state.txt", []byte("staged-data"), metadata, resolver)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}

	// If sub is replaced by a symlink before StageFile rename:
	subDir := filepath.Join(root, "sub")
	if err := os.Symlink(outside, subDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := StageFile(plan, entry, resolver, &testStagingVerifier{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("StageFile accepted symlink swap race: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "state.txt")); !os.IsNotExist(err) {
		t.Fatal("StageFile escaped root and wrote outside via symlink race")
	}
}

func TestApplyJournalRoundTripAndRecoveryMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apply.jsonl")
	journal := Journal{Path: path}
	record := JournalRecord{TransactionID: "tx-1", Entry: "category/state.txt", Action: "create", Before: "absent", After: "created", PlaintextDigest: digestBytes("restore-data")}
	if err := journal.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := journal.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].After != "created" {
		t.Fatalf("journal = %#v", got)
	}
	if err := journal.ValidateRecovery(got); err != nil {
		t.Fatalf("ValidateRecovery: %v", err)
	}
}

func TestRecoverCreateOnlyRemovesOnlyMatchingCreatedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "category", "state.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("restore-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := []JournalRecord{{TransactionID: "tx-1", Entry: "category/state.txt", Action: "create", Before: "absent", After: "created", PlaintextDigest: digestBytes("restore-data")}}
	if err := RecoverCreateOnly(root, records); err != nil {
		t.Fatalf("RecoverCreateOnly: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("recovered path error=%v, want removed", err)
	}

	if err := os.WriteFile(path, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverCreateOnly(root, records); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("RecoverCreateOnly mismatch error=%v, want digest refusal", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("mismatched destination disappeared: %v", err)
	}
}

func TestRecoverCreateOnlyRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "state.txt")
	if err := os.WriteFile(outsidePath, []byte("restore-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "category")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	records := []JournalRecord{{
		TransactionID: "tx-1",
		Entry:         "category/state.txt", Action: "create", Before: "absent", After: "created",
		PlaintextDigest: digestBytes("restore-data"),
	}}

	if err := RecoverCreateOnly(root, records); err == nil || !strings.Contains(err.Error(), "safe directory") {
		t.Fatalf("RecoverCreateOnly symlink ancestor error = %v, want refusal", err)
	}
	if _, err := os.Lstat(outsidePath); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
}

func TestTransactionRoundTripAndScopedRead(t *testing.T) {
	root := t.TempDir()
	tx, err := BeginTransaction(root, "tx-round-trip")
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	record := JournalRecord{
		TransactionID: tx.ID, Entry: "state.txt", Action: "create",
		Before: "absent", After: "staged", PlaintextDigest: digestBytes("restore-data"),
		CiphertextDigest: "sha256:" + strings.Repeat("0", 64),
	}
	if err := tx.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := tx.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].TransactionID != tx.ID || got[0].RootIdentity == "" ||
		got[0].CiphertextDigest != record.CiphertextDigest {
		t.Fatalf("transaction records = %#v", got)
	}
}

func TestRecoverTransactionOnlyRemovesRequestedTransaction(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old.txt")
	newPath := filepath.Join(root, "new.txt")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTx, err := BeginTransaction(root, "tx-old")
	if err != nil {
		t.Fatal(err)
	}
	newTx, err := BeginTransaction(root, "tx-new")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldTx.Append(JournalRecord{TransactionID: oldTx.ID, Entry: "old.txt", Action: "create", Before: "absent", After: "created", PlaintextDigest: digestBytes("old")}); err != nil {
		t.Fatal(err)
	}
	if err := newTx.Append(JournalRecord{TransactionID: newTx.ID, Entry: "new.txt", Action: "create", Before: "absent", After: "created", PlaintextDigest: digestBytes("new")}); err != nil {
		t.Fatal(err)
	}
	if err := RecoverTransaction(root, newTx.ID); err != nil {
		t.Fatalf("RecoverTransaction: %v", err)
	}
	if _, err := os.Lstat(newPath); !os.IsNotExist(err) {
		t.Fatalf("new transaction path = %v, want removed", err)
	}
	if got, err := os.ReadFile(oldPath); err != nil || string(got) != "old" {
		t.Fatalf("old transaction path = %q, err=%v; want preserved", got, err)
	}
}

func TestStageFileRejectsRootIdentityDrift(t *testing.T) {
	root := t.TempDir()
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testArtifactResolver{metadata.Reference(): []byte("0123456789abcdef0123456789abcdef")}
	entry := sealedEntry(t, "state.txt", []byte("restore-data"), metadata, resolver)
	plan, err := BuildPlan(root, []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	replaced := root + "-replaced"
	if err := os.Rename(root, replaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := StageFile(plan, entry, resolver, &testStagingVerifier{}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("StageFile root drift error = %v, want identity refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "state.txt")); !os.IsNotExist(err) {
		t.Fatalf("root-drift destination exists: %v", err)
	}
}

func TestTrustedSystemSymlinkPolicyIsNarrowAndDarwinOnly(t *testing.T) {
	tests := []struct {
		name string
		path string
		root string
		goos string
		want bool
	}{
		{name: "darwin var ancestor", path: "/var", root: "/var/folders/test", goos: "darwin", want: true},
		{name: "darwin root itself", path: "/var", root: "/var", goos: "darwin", want: false},
		{name: "darwin unrelated alias", path: "/usr", root: "/usr/local/test", goos: "darwin", want: false},
		{name: "linux var ancestor", path: "/var", root: "/var/tmp/test", goos: "linux", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := trustedSystemSymlink(test.path, test.root, test.goos); got != test.want {
				t.Fatalf("trustedSystemSymlink(%q, %q, %q) = %v, want %v", test.path, test.root, test.goos, got, test.want)
			}
		})
	}
}
