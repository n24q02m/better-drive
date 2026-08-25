package restore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

func TestBuildPlanRejectsTraversalAbsoluteAndDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"C:/escape", "/absolute", "../escape", "a/../../escape", "secret:stream"} {
		if _, err := BuildPlan(root, []Entry{{RelativePath: relative}}); err == nil {
			t.Errorf("BuildPlan accepted unsafe path %q", relative)
		}
	}
	if _, err := BuildPlan(root, []Entry{{RelativePath: "a.txt"}, {RelativePath: "a.txt"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("BuildPlan accepted duplicate destination path")
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

func TestStageFileRejectsEnvelopeAndManifestFailuresWithoutCommit(t *testing.T) {
	metadata := artifactcrypto.Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	key := []byte("0123456789abcdef0123456789abcdef")
	resolver := testArtifactResolver{metadata.Reference(): key}
	cases := []struct {
		name   string
		mutate func(t *testing.T, entry *Entry, data []byte) ([]byte, artifactcrypto.Resolver)
	}{
		{
			name: "wrong key",
			mutate: func(_ *testing.T, _ *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				return data, testArtifactResolver{metadata.Reference(): []byte("abcdef0123456789abcdef0123456789")}
			},
		},
		{
			name: "resolver failure",
			mutate: func(_ *testing.T, _ *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				return data, testArtifactResolver{}
			},
		},
		{
			name: "wrong key reference",
			mutate: func(_ *testing.T, entry *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				entry.ArtifactMetadata.KeyRef = "other-key"
				return data, resolver
			},
		},
		{
			name: "tampered envelope",
			mutate: func(_ *testing.T, _ *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				data[len(data)-1] ^= 0x01
				return data, resolver
			},
		},
		{
			name: "truncated envelope",
			mutate: func(_ *testing.T, _ *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				return data[:len(data)-1], resolver
			},
		},
		{
			name: "ciphertext digest mismatch",
			mutate: func(_ *testing.T, entry *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				entry.CiphertextDigest = "sha256:" + strings.Repeat("0", sha256.Size*2)
				return data, resolver
			},
		},
		{
			name: "plaintext digest mismatch",
			mutate: func(_ *testing.T, entry *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				entry.PlaintextDigest = "sha256:" + strings.Repeat("0", sha256.Size*2)
				return data, resolver
			},
		},
		{
			name: "plaintext size mismatch",
			mutate: func(_ *testing.T, entry *Entry, data []byte) ([]byte, artifactcrypto.Resolver) {
				entry.PlaintextSize++
				return data, resolver
			},
		},
		{
			name: "plaintext source",
			mutate: func(_ *testing.T, entry *Entry, _ []byte) ([]byte, artifactcrypto.Resolver) {
				return []byte("restore-data"), resolver
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			entry := sealedEntry(t, "state.txt", []byte("restore-data"), metadata, resolver)
			data, err := os.ReadFile(entry.SourcePath)
			if err != nil {
				t.Fatal(err)
			}
			data, caseResolver := tc.mutate(t, &entry, data)
			if err := os.WriteFile(entry.SourcePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := BuildPlan(root, []Entry{entry})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if err := StageFile(plan, entry, caseResolver, &testStagingVerifier{}); err == nil {
				t.Fatal("StageFile accepted an invalid envelope or manifest")
			}
			if _, err := os.Lstat(filepath.Join(root, "state.txt")); !os.IsNotExist(err) {
				t.Fatalf("failed restore created destination: %v", err)
			}
		})
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
