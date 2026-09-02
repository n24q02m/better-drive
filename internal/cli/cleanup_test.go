package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/driveapi"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/protectedfs"
)

func writeCleanupTestManifest(t *testing.T, dir string) string {
	t.Helper()
	manifest := cleanup.Manifest{
		SchemaVersion:     cleanup.CurrentSchemaVersion,
		ManifestID:        "manifest-1",
		AccountID:         "account-1",
		RootID:            "root-1",
		Namespace:         "backup/home",
		Mode:              cleanup.ModeQuarantine,
		MutationSemantics: cleanup.MutationSemanticsDriveOwnerRisk,
		QuarantineTarget: cleanup.QuarantineTarget{
			Provider:         "drive",
			AccountID:        "account-1",
			ParentID:         "quarantine-1",
			EnrollmentDigest: strings.Repeat("c", 64),
		},
		CreatedAt:           time.Now().UTC().Add(-time.Minute),
		ExpiresAt:           time.Now().UTC().Add(time.Hour),
		Nonce:               "nonce-1",
		Budget:              cleanup.Budget{MaxObjects: 1, MaxBytes: 5},
		SourceInventoryHash: strings.Repeat("a", 64),
		FixtureDigest:       strings.Repeat("f", 64),
		Objects: []cleanup.Object{{
			ID: "object-1", ParentID: "root-1", Name: "object.bin", Path: "object.bin", ObjectType: cleanup.ObjectTypeFile,
			ContentHash: strings.Repeat("b", 64), Size: 5, Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
			Version: "v1", Generation: "generation-1", MetadataDigest: strings.Repeat("d", 64), ModifiedAt: time.Now().UTC(), Depth: 1,
			Class: cleanup.ClassOrphan, OwnershipMarker: "marker-1", RestoreEvidence: "restore-1",
		}},
	}
	path := filepath.Join(dir, "manifest.json")
	data, err := cleanup.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCleanupTestRootSet(t *testing.T, dir string) string {
	t.Helper()
	rootSet := cleanup.RootSet{
		SchemaVersion: cleanup.CurrentRootSetSchemaVersion,
		Roots: []cleanup.Root{{
			Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", ExpectedPages: 1,
			Pages: []cleanup.Page{{Number: 1, ParentID: "root-1", Cursor: "cursor-1", Status: cleanup.PageComplete, Objects: []cleanup.Object{{
				ID: "object-1", ParentID: "root-1", Name: "object.bin", Path: "object.bin", ObjectType: cleanup.ObjectTypeFile,
				ContentHash: strings.Repeat("a", 64), Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home", Version: "v1", Generation: "generation-1", MetadataDigest: strings.Repeat("d", 64), ModifiedAt: time.Now().UTC(), Depth: 1, Size: 5,
			}}},
			}},
		}}
	rootSet, err := cleanup.FreezeRootSet(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "all-roots.json")
	data, err := json.Marshal(rootSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCleanupTestApproval(t *testing.T, dir string) (string, cleanup.Approval) {
	t.Helper()
	approval := cleanup.Approval{
		SchemaVersion:     cleanup.CurrentApprovalSchemaVersion,
		ApprovalID:        "approval-test-1",
		ManifestDigest:    strings.Repeat("a", 64),
		AccountID:         "account-1",
		RootID:            "root-1",
		Mode:              cleanup.ModeQuarantine,
		MutationSemantics: cleanup.MutationSemanticsDriveOwnerRisk,
		QuarantineTarget: cleanup.QuarantineTarget{
			Provider:         "drive",
			AccountID:        "account-1",
			ParentID:         "quarantine-1",
			EnrollmentDigest: strings.Repeat("c", 64),
		},
		MaxObjects:    10,
		MaxBytes:      1000,
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
		Nonce:         "nonce-123",
		Issuer:        "sec-team",
		FixtureDigest: strings.Repeat("f", 64),
	}
	path := filepath.Join(dir, "approval.json")
	data, err := cleanup.CanonicalApproval(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, approval
}

func TestCleanupApprovalWorkflowCommands(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "approval-store")
	approvalPath, approval := writeCleanupTestApproval(t, dir)

	// 1. Canonicalize command
	root := newRootCmd()
	var canonOut bytes.Buffer
	root.SetOut(&canonOut)
	root.SetErr(&canonOut)
	root.SetArgs([]string{"cleanup", "approval", "canonicalize", "--approval", approvalPath})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("canonicalize error = %v", err)
	}
	if !strings.Contains(canonOut.String(), `"approval_id":"approval-test-1"`) {
		t.Fatalf("unexpected canonicalize output: %s", canonOut.String())
	}

	// 2. Prepare command
	root = newRootCmd()
	var prepOut bytes.Buffer
	root.SetOut(&prepOut)
	root.SetErr(&prepOut)
	root.SetArgs([]string{"cleanup", "approval", "prepare", "--approval", approvalPath, "--store", storePath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	if !strings.Contains(prepOut.String(), `"draft_digest"`) {
		t.Fatalf("unexpected prepare output: %s", prepOut.String())
	}

	// 3. Sign offline and Activate command
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := cleanup.SignApproval(approval, priv)
	if err != nil {
		t.Fatal(err)
	}
	trustRoot, err := cleanup.NewTrustRoot("root-1", approval.Issuer, cleanup.CleanupTrustPurpose, pub, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := json.MarshalIndent(trustRoot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(dir, "trust-root.json")
	if err := os.WriteFile(rootPath, rootData, 0o600); err != nil {
		t.Fatal(err)
	}

	root = newRootCmd()
	var actOut bytes.Buffer
	root.SetOut(&actOut)
	root.SetErr(&actOut)
	root.SetArgs([]string{
		"cleanup", "approval", "activate",
		"--approval", approvalPath,
		"--signature", hex.EncodeToString(sig),
		"--root", rootPath,
		"--store", storePath,
		"--format", "json",
	})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("activate error = %v", err)
	}
	if !strings.Contains(actOut.String(), `"state": "approved"`) {
		t.Fatalf("unexpected activate output: %s", actOut.String())
	}
}

func TestReadDriveAccessTokenSourceAcceptsLegacyDescriptor(t *testing.T) {
	setInheritedFileDescriptor(t, driveTokenFDEnv, []byte("legacy-access-token"))
	tokenSource, err := readDriveAccessTokenSource(&http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokenSource.AccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "legacy-access-token" {
		t.Fatalf("legacy access token = %q", token)
	}
}

func TestReadDriveAccessTokenSourceRejectsAmbiguousDescriptors(t *testing.T) {
	t.Setenv(driveTokenFDEnv, "100")
	t.Setenv(driveOAuthCredentialFDEnv, "101")
	if _, err := readDriveAccessTokenSource(&http.Client{}); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("ambiguous descriptor error = %v", err)
	}
}

func TestCleanupInventoryCapturesProviderAggregateAndState(t *testing.T) {
	dir := t.TempDir()
	rootSetPath := writeCleanupTestRootSet(t, dir)
	rootSetData, err := os.ReadFile(rootSetPath)
	if err != nil {
		t.Fatal(err)
	}
	rootSet, err := cleanup.DecodeRootSet(rootSetData)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := cleanup.BuildAggregate(rootSet, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := driveapi.FreezeInventoryPlan(driveapi.InventoryPlan{
		SchemaVersion: driveapi.CurrentInventoryPlanSchemaVersion,
		AccountID:     "account-1",
		Roots: []driveapi.InventoryRoot{{
			Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "inventory-plan.json")
	planData, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialData, err := json.Marshal(driveapi.OAuthCredential{
		SchemaVersion: driveapi.CurrentOAuthCredentialSchemaVersion,
		AccessToken:   "inventory-token", RefreshToken: "refresh-token", TokenType: "Bearer",
		Expiry: time.Now().UTC().Add(time.Hour), ClientID: "client-id", ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	setInheritedFileDescriptor(t, driveOAuthCredentialFDEnv, credentialData)
	statePath := filepath.Join(dir, "inventory-state.json")
	capturePath := filepath.Join(dir, "all-roots.json")
	aggregatePath := filepath.Join(dir, "inventory-aggregate.json")
	command := cleanupInventoryCommand(func(_ *http.Client, tokenSource driveapi.AccessTokenSource) (cleanupInventoryCapturer, error) {
		token, err := tokenSource.AccessToken(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if token != "inventory-token" {
			t.Fatalf("inventory token = %q", token)
		}
		return cleanupInventoryStub{rootSet: rootSet, aggregate: aggregate}, nil
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--plan", planPath, "--capture", capturePath, "--state", statePath, "--output", aggregatePath, "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatalf("cleanup inventory error = %v", err)
	}
	if !strings.Contains(output.String(), `"status": "COMPLETE"`) {
		t.Fatalf("unexpected inventory output: %s", output.String())
	}
	for _, path := range []string{statePath, capturePath, aggregatePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing inventory output %s: %v", path, err)
		}
	}
}

type cleanupInventoryStub struct {
	rootSet   cleanup.RootSet
	aggregate cleanup.InventoryAggregate
}

func (stub cleanupInventoryStub) Capture(context.Context, driveapi.InventoryPlan) (cleanup.RootSet, cleanup.InventoryAggregate, error) {
	return stub.rootSet, stub.aggregate, nil
}

func TestCleanupFixtureLifecyclePreviewVerifiesSignedProductionDenial(t *testing.T) {
	setTestConfigHome(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	approvalPublicKey, approvalPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoot, err := cleanup.NewTrustRoot(
		"fixture-preview-approval",
		"fixture-preview-signer",
		cleanup.CleanupTrustPurpose,
		approvalPublicKey,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	bundle := testCleanupTrustBundle(t, now.Add(-time.Hour), "fixture-preview")
	bundle.ApprovalRoot = approvalRoot
	bundleData, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeCleanupTrustBundle(bundleData, nil); err != nil {
		t.Fatal(err)
	}
	capability, err := driveapi.FreezeFixtureLifecycleCapability(driveapi.FixtureLifecycleCapability{
		SchemaVersion:      driveapi.CurrentFixtureLifecycleSchemaVersion,
		FixtureID:          "preview-fixture",
		ArtifactDigest:     strings.Repeat("b", 64),
		Issuer:             approvalRoot.Issuer,
		Provider:           "drive",
		AccountID:          "candidate-account",
		RootID:             "candidate-root",
		Namespace:          "candidate-fixture",
		ObjectID:           "fixture-file",
		OriginalParentID:   "fixture-source",
		QuarantineParentID: "fixture-quarantine",
		ProductionRootIDs:  []string{"production-root"},
		Sequence:           []string{driveapi.FixturePhaseQuarantine, driveapi.FixturePhaseRestore, driveapi.FixturePhaseRequarantine},
		CreatedAt:          now.Add(-time.Minute),
		ExpiresAt:          now.Add(time.Hour),
		Nonce:              "preview-fixture-nonce",
		MutationSemantics:  driveapi.FixtureMutationSemantics,
		ProductionDenied:   true,
		Initial: cleanup.Object{
			ID: "fixture-file", ParentID: "fixture-source", Name: "fixture.bin", Path: "/candidate-fixture/fixture.bin", ObjectType: cleanup.ObjectTypeFile,
			ContentHash: strings.Repeat("a", 32), Size: 10, Provider: "drive", AccountID: "candidate-account", RootID: "candidate-root", Namespace: "candidate-fixture",
			Version: "1", Generation: "generation-1", MetadataDigest: strings.Repeat("e", 64), ModifiedAt: now, Class: cleanup.ClassExpectedFixture,
			OwnershipMarker: "fixture:preview-fixture", RestoreEvidence: "fixture:preview-fixture:" + strings.Repeat("b", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := driveapi.CanonicalFixtureLifecycleCapability(capability)
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := json.Marshal(driveapi.FixtureLifecycleRequest{
		Capability: capability, SignatureHex: hex.EncodeToString(ed25519.Sign(approvalPrivateKey, canonical)),
	})
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "fixture-request.json")
	if err := os.WriteFile(requestPath, requestData, 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	var rendered bytes.Buffer
	root.SetOut(&rendered)
	root.SetErr(&rendered)
	root.SetArgs([]string{"cleanup", "fixture-cycle", "--request", requestPath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), `"status": "preview"`) ||
		!strings.Contains(rendered.String(), `"production_denied": true`) ||
		!strings.Contains(rendered.String(), `"capability_digest"`) {
		t.Fatalf("fixture preview output = %s", rendered.String())
	}
}
func TestCleanupValidateCommandRendersJSON(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"cleanup", "validate", "--manifest", manifestPath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("cleanup validate error = %v", err)
	}
	if !strings.Contains(output.String(), `"manifest_digest"`) || !strings.Contains(output.String(), `"object_count": 1`) {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestCleanupApplyExecuteFailsClosed(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"cleanup", "apply", "--manifest", manifestPath, "--execute"})
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "protected provider broker") {
		t.Fatalf("expected fail-closed mutation gate, got %v", err)
	}
}

func TestCleanupApplyPreviewWritesJournal(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeCleanupTestManifest(t, dir)
	journalPath := filepath.Join(dir, "cleanup.jsonl")
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"cleanup", "apply", "--manifest", manifestPath, "--journal", journalPath, "--format", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("cleanup preview error = %v", err)
	}
	journal, err := cleanup.OpenFileJournal(journalPath)
	if err != nil {
		t.Fatalf("open preview journal error = %v", err)
	}
	if len(journal.Records) != 1 || journal.Records[0].Action != "preview" {
		t.Fatalf("unexpected preview journal: %+v", journal.Records)
	}
}

func TestClassifyCleanupExecutionErrorBlocksUnknownSettlementRetry(t *testing.T) {
	unknown := classifyCleanupExecutionError(driveapi.ErrSettlementUnknown)
	if exitcode.Code(unknown) != exitcode.SyncFailedCode {
		t.Fatalf("unknown settlement exit code = %d", exitcode.Code(unknown))
	}
	if !strings.Contains(exitcode.RemediationOf(unknown), "do not retry") {
		t.Fatalf("unknown settlement remediation = %q", exitcode.RemediationOf(unknown))
	}
	configuration := classifyCleanupExecutionError(context.Canceled)
	if exitcode.Code(configuration) != exitcode.ConfigErrorCode {
		t.Fatalf("configuration exit code = %d", exitcode.Code(configuration))
	}
}

func TestWriteJSONAtomicallyReplacesExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	if err := writeJSONAtomically(path, map[string]int{"generation": 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomically(path, map[string]int{"generation": 2}); err != nil {
		t.Fatal(err)
	}
	file, err := protectedfs.OpenPrivateFile(path)
	if err != nil {
		t.Fatalf("verify protected output: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]int
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output["generation"] != 2 {
		t.Fatalf("output generation = %d", output["generation"])
	}
}
