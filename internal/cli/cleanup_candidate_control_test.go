package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

func TestCleanupCandidateControlVerifyRendersDerivedJSONMarker(t *testing.T) {
	setTestConfigHome(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	issuerPublic, issuerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	executorPublic, executorPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuerRoot, err := cleanup.NewTrustRoot("candidate-issuer-root", "skret-candidate-issuer", cleanup.CandidateControlIssuerPurpose, issuerPublic, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	executorRoot, err := cleanup.NewTrustRoot("candidate-executor-root", "skret-candidate-executor-1", cleanup.CandidateControlReadbackPurpose, executorPublic, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	transaction := "bd-control-cli-1"
	namespace := "refs/heads/bdrive-candidate/" + transaction + "/"
	operations := []cleanup.CandidateControlOperation{
		{Ref: namespace + "lease", ExpectedOID: strings.Repeat("1", 40), DesiredOID: strings.Repeat("2", 40)},
		{Ref: namespace + "retention", ExpectedOID: strings.Repeat("3", 40), DesiredOID: strings.Repeat("4", 40)},
	}
	capability := cleanup.CandidateControlCapability{
		SchemaVersion:        cleanup.CurrentCandidateControlSchemaVersion,
		Purpose:              cleanup.CandidateControlIssuerPurpose,
		TransactionID:        transaction,
		Issuer:               issuerRoot.Issuer,
		ExecutorID:           executorRoot.Issuer,
		ExecutorPublicKey:    hex.EncodeToString(executorPublic),
		ClientID:             "better-drive-candidate-client-1",
		Remote:               "n24q02m/synthetic-control",
		RemoteURLDigest:      cleanupCandidateSHA256([]byte("https://github.com/n24q02m/synthetic-control.git")),
		RefNamespace:         namespace,
		ClaimRef:             namespace + "claim",
		CompletionRef:        namespace + "completion",
		Operations:           operations,
		OperationBudget:      len(operations),
		RefEscapeProbe:       "refs/heads/main",
		ProductionRemote:     "n24q02m/production-control",
		ProductionRef:        "refs/heads/main",
		TeardownIntentDigest: strings.Repeat("a", 64),
		IssuedAt:             now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt:            now.Add(10 * time.Minute).Format(time.RFC3339),
		Nonce:                "bd-control-cli-nonce-1",
	}
	capabilityData, capabilityUnsigned := signCleanupCandidateJSON(t, capability, issuerPrivate)
	capabilityDigest := cleanupCandidateSHA256(capabilityUnsigned)
	claimPayload := cleanup.CandidateControlClaimPayload{
		SchemaVersion:     cleanup.CurrentCandidateControlSchemaVersion,
		Purpose:           cleanup.CandidateControlClaimPurpose,
		TransactionID:     transaction,
		CapabilityDigest:  capabilityDigest,
		ExecutorID:        executorRoot.Issuer,
		ExecutorPublicKey: capability.ExecutorPublicKey,
		InvocationID:      "invocation-cli-1",
		ClaimedAt:         now.Add(-30 * time.Second).Format(time.RFC3339),
	}
	claimOID, err := cleanup.CandidateControlClaimCommitOID(claimPayload)
	if err != nil {
		t.Fatal(err)
	}
	replayClaimPayload := claimPayload
	replayClaimPayload.InvocationID = "invocation-cli-replay-1"
	replayClaimPayload.ClaimedAt = now.Format(time.RFC3339)
	replayOID, err := cleanup.CandidateControlClaimCommitOID(replayClaimPayload)
	if err != nil {
		t.Fatal(err)
	}
	finalOperations := []cleanup.CandidateControlRefOID{
		{Ref: operations[0].Ref, OID: operations[0].DesiredOID},
		{Ref: operations[1].Ref, OID: operations[1].DesiredOID},
	}
	atomicUpdates := []cleanup.CandidateControlAtomicUpdate{
		{Kind: "claim", Ref: capability.ClaimRef, DesiredOID: claimOID, AfterOID: claimOID},
		{Kind: "operation", Ref: operations[0].Ref, ExpectedOID: new(operations[0].ExpectedOID), DesiredOID: operations[0].DesiredOID, BeforeOID: new(operations[0].ExpectedOID), AfterOID: operations[0].DesiredOID},
		{Kind: "operation", Ref: operations[1].Ref, ExpectedOID: new(operations[1].ExpectedOID), DesiredOID: operations[1].DesiredOID, BeforeOID: new(operations[1].ExpectedOID), AfterOID: operations[1].DesiredOID},
		{Kind: "completion", Ref: capability.CompletionRef, DesiredOID: claimOID, AfterOID: claimOID},
	}
	replayUpdates := []cleanup.CandidateControlAtomicUpdate{
		{Kind: "claim", Ref: capability.ClaimRef, DesiredOID: replayOID, BeforeOID: new(claimOID), AfterOID: claimOID},
		{Kind: "operation", Ref: operations[0].Ref, ExpectedOID: new(operations[0].ExpectedOID), DesiredOID: operations[0].DesiredOID, BeforeOID: new(operations[0].DesiredOID), AfterOID: operations[0].DesiredOID},
		{Kind: "operation", Ref: operations[1].Ref, ExpectedOID: new(operations[1].ExpectedOID), DesiredOID: operations[1].DesiredOID, BeforeOID: new(operations[1].DesiredOID), AfterOID: operations[1].DesiredOID},
		{Kind: "completion", Ref: capability.CompletionRef, DesiredOID: replayOID, BeforeOID: new(claimOID), AfterOID: claimOID},
	}
	readback := cleanup.CandidateControlReadback{
		SchemaVersion:    cleanup.CurrentCandidateControlSchemaVersion,
		Purpose:          cleanup.CandidateControlReadbackPurpose,
		TransactionID:    transaction,
		CapabilityDigest: capabilityDigest,
		Issuer:           executorRoot.Issuer,
		ExecutorID:       executorRoot.Issuer,
		ClientID:         capability.ClientID,
		Remote:           capability.Remote,
		RemoteURLDigest:  capability.RemoteURLDigest,
		Transport:        "github",
		ClaimPayload:     claimPayload,
		Claim: cleanup.CandidateControlRefReadback{
			Ref: capability.ClaimRef, OID: claimOID, InvocationID: claimPayload.InvocationID, Created: true,
		},
		Completion: cleanup.CandidateControlRefReadback{
			Ref: capability.CompletionRef, OID: claimOID, InvocationID: claimPayload.InvocationID, Created: true,
		},
		Operations: []cleanup.CandidateControlOperationReadback{
			{Ref: operations[0].Ref, ExpectedOID: operations[0].ExpectedOID, DesiredOID: operations[0].DesiredOID, BeforeOID: operations[0].ExpectedOID, AfterOID: operations[0].DesiredOID},
			{Ref: operations[1].Ref, ExpectedOID: operations[1].ExpectedOID, DesiredOID: operations[1].DesiredOID, BeforeOID: operations[1].ExpectedOID, AfterOID: operations[1].DesiredOID},
		},
		OperationCount: len(operations),
		AtomicPush: cleanup.CandidateControlAtomicPush{
			Attempted: true, ExitCode: 0, Reconciled: false, Updates: atomicUpdates,
		},
		ReplayProbe: cleanup.CandidateControlReplayProbe{
			Attempted: true, ProviderInvoked: true, ProviderCallsBefore: 10, ProviderCallsAfter: 19,
			ExitCode: 1, Outcome: "ALREADY_COMPLETED", WinningClaimOID: claimOID, AttemptedClaimOID: replayOID,
			AttemptedInvocationID: replayClaimPayload.InvocationID, AttemptedClaimPayload: replayClaimPayload,
			ClaimOIDBefore: claimOID, ClaimOIDAfter: claimOID, CompletionOIDBefore: claimOID, CompletionOIDAfter: claimOID,
			OperationsBefore: finalOperations, OperationsAfter: finalOperations, Updates: replayUpdates,
		},
		RefEscapeProbe: cleanup.CandidateControlDenialProbe{
			Remote: capability.Remote, Ref: capability.RefEscapeProbe, ProviderCallsBefore: 19, ProviderCallsAfter: 19, Outcome: "REF_OUTSIDE_NAMESPACE",
		},
		ProductionRemoteProbe: cleanup.CandidateControlDenialProbe{
			Remote: capability.ProductionRemote, Ref: capability.ProductionRef, ProviderCallsBefore: 19, ProviderCallsAfter: 19, Outcome: "REMOTE_OUTSIDE_ALLOWLIST",
		},
		ProductionRefProbe: cleanup.CandidateControlDenialProbe{
			Remote: capability.Remote, Ref: capability.ProductionRef, ProviderCallsBefore: 19, ProviderCallsAfter: 19, Outcome: "REF_OUTSIDE_NAMESPACE",
		},
		ObservedAt: now.Format(time.RFC3339),
	}
	readbackData, _ := signCleanupCandidateJSON(t, readback, executorPrivate)

	dir := t.TempDir()
	capabilityPath := writeCleanupCandidateFile(t, dir, "capability.json", capabilityData)
	readbackPath := writeCleanupCandidateFile(t, dir, "readback.json", readbackData)
	issuerRootPath := writeCleanupCandidateJSONFile(t, dir, "issuer-root.json", issuerRoot)
	executorRootPath := writeCleanupCandidateJSONFile(t, dir, "executor-root.json", executorRoot)
	args := []string{
		"cleanup", "candidate-control", "verify",
		"--capability", capabilityPath,
		"--capability-root", issuerRootPath,
		"--readback", readbackPath,
		"--readback-root", executorRootPath,
		"--format", "json",
	}
	root := newRootCmd()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(args)
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "protected candidate-control trust bundle") {
		t.Fatalf("missing protected candidate-control trust bundle error = %v", err)
	}

	bundleData, err := json.Marshal(candidateControlTrustBundle{
		SchemaVersion:  1,
		CapabilityRoot: issuerRoot,
		ReadbackRoot:   executorRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	trustBundleDigest, err := writeCandidateControlTrustBundle(bundleData, nil)
	if err != nil {
		t.Fatal(err)
	}
	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(args)
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("candidate-control verify error = %v", err)
	}
	if !strings.Contains(output.String(), `"record_type": "BD-CANDIDATE-CONTROL-EXERCISED"`) ||
		!strings.Contains(output.String(), `"transport": "github"`) ||
		!strings.Contains(output.String(), `"trust_bundle_digest": "`+trustBundleDigest+`"`) {
		t.Fatalf("unexpected candidate-control marker: %s", output.String())
	}

	substitutedPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	substitutedRoot, err := cleanup.NewTrustRoot(
		"candidate-issuer-substituted",
		issuerRoot.Issuer,
		cleanup.CandidateControlIssuerPurpose,
		substitutedPublic,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	substitutedRootPath := writeCleanupCandidateJSONFile(t, dir, "issuer-root-substituted.json", substitutedRoot)
	substitutedArgs := append([]string(nil), args...)
	substitutedArgs[6] = substitutedRootPath
	root = newRootCmd()
	output.Reset()
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(substitutedArgs)
	if _, err := root.ExecuteC(); err == nil || !strings.Contains(err.Error(), "do not match the protected") {
		t.Fatalf("substituted candidate-control trust root error = %v", err)
	}
}

func TestReadCandidateControlFileRejectsOversizeInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversize.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(candidateControlInputLimit + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readCandidateControlFile(path, "--capability"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversize candidate-control input error = %v", err)
	}
}

func signCleanupCandidateJSON(t *testing.T, value any, privateKey ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	delete(object, "signature")
	unsigned, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	object["signature"] = hex.EncodeToString(ed25519.Sign(privateKey, unsigned))
	signed, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return append(signed, '\n'), unsigned
}

func writeCleanupCandidateFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCleanupCandidateJSONFile(t *testing.T, dir, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return writeCleanupCandidateFile(t, dir, name, data)
}

func cleanupCandidateSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
