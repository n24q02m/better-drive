package cleanup

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const candidateControlTestTrustBundleDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestVerifyCandidateControlExerciseBuildsExactMarker(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
	capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	readback["capability_digest"] = candidateControlDigest(t, capability)
	readbackData := signCandidateControlJSON(t, readback, readbackKey)
	if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, strings.Repeat("c", 63), now); err == nil || !strings.Contains(err.Error(), "trust_bundle_digest") {
		t.Fatalf("invalid candidate-control trust bundle digest error = %v", err)
	}

	marker, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	if marker.SchemaVersion != CurrentCandidateControlSchemaVersion || marker.RecordType != CandidateControlExercisedRecordType {
		t.Fatalf("candidate control marker schema = %+v", marker)
	}
	if marker.TransactionID != capability["transaction_id"] || marker.CapabilityDigest != readback["capability_digest"] {
		t.Fatalf("candidate control marker identity = %+v", marker)
	}
	if marker.Remote != capability["remote"] || marker.RemoteURLDigest != capability["remote_url_digest"] || marker.Transport != "github" {
		t.Fatalf("candidate control marker remote binding = %+v", marker)
	}
	if marker.ClaimOID != readback["claim"].(map[string]any)["oid"] || marker.CompletionOID != marker.ClaimOID || marker.OperationCount != 2 {
		t.Fatalf("candidate control marker atomic binding = %+v", marker)
	}
	if !marker.Atomic || !marker.ReplayDenied || !marker.RefEscapeDenied || !marker.ProductionRemoteDenied || !marker.ProductionRefDenied {
		t.Fatalf("candidate control marker proofs = %+v", marker)
	}
	if marker.ControlRootFingerprint != capabilityRoot.Fingerprint || marker.ReadbackRootFingerprint != readbackRoot.Fingerprint || marker.TrustBundleDigest != candidateControlTestTrustBundleDigest {
		t.Fatalf("candidate control marker roots = %+v", marker)
	}
	if marker.TeardownIntentDigest != capability["teardown_intent_digest"] || marker.ReadbackDigest == "" {
		t.Fatalf("candidate control marker evidence binding = %+v", marker)
	}
	if !marker.ObservedAt.Equal(now) {
		t.Fatalf("candidate control marker observed_at = %s", marker.ObservedAt)
	}
}

func TestCandidateControlClaimCommitOIDMatchesSkretFixture(t *testing.T) {
	payload := CandidateControlClaimPayload{
		SchemaVersion:     CurrentCandidateControlSchemaVersion,
		Purpose:           CandidateControlClaimPurpose,
		TransactionID:     "bd-control-cross-language-1",
		CapabilityDigest:  strings.Repeat("a", 64),
		ExecutorID:        "skret-candidate-executor-1",
		ExecutorPublicKey: strings.Repeat("b", 64),
		InvocationID:      "invocation-cross-language-1",
		ClaimedAt:         "2027-01-15T08:00:00Z",
	}

	oid, err := CandidateControlClaimCommitOID(payload)
	if err != nil {
		t.Fatal(err)
	}
	if oid != "c506b2df3ba91152039353d6be886f8b60ce9e86" {
		t.Fatalf("claim commit OID = %s", oid)
	}
}

func TestVerifyCandidateControlExerciseRejectsResignedFalseEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	tests := map[string]func(map[string]any){
		"fixture transport": func(readback map[string]any) {
			readback["transport"] = "file-fixture"
		},
		"nonzero atomic push": func(readback map[string]any) {
			atomic := readback["atomic_push"].(map[string]any)
			atomic["exit_code"] = 1
			atomic["reconciled"] = true
		},
		"same replay claim": func(readback map[string]any) {
			replay := readback["replay_probe"].(map[string]any)
			replay["attempted_claim_oid"] = replay["winning_claim_oid"]
		},
		"successful replay push": func(readback map[string]any) {
			readback["replay_probe"].(map[string]any)["exit_code"] = 0
		},
		"denial called provider": func(readback map[string]any) {
			probe := readback["production_remote_probe"].(map[string]any)
			probe["provider_invoked"] = true
			probe["provider_calls_after"] = 20
		},
		"operation readback drift": func(readback map[string]any) {
			operation := readback["operations"].([]any)[0].(map[string]any)
			operation["after_oid"] = strings.Repeat("f", 40)
		},
		"claim completion mismatch": func(readback map[string]any) {
			readback["completion"].(map[string]any)["oid"] = strings.Repeat("6", 40)
		},
		"atomic update drift": func(readback map[string]any) {
			updates := readback["atomic_push"].(map[string]any)["updates"].([]any)
			updates[1].(map[string]any)["after_oid"] = strings.Repeat("f", 40)
		},
		"replay request drift": func(readback map[string]any) {
			replay := readback["replay_probe"].(map[string]any)
			updates := replay["updates"].([]any)
			updates[0].(map[string]any)["desired_oid"] = replay["winning_claim_oid"]
		},
		"claim payload drift": func(readback map[string]any) {
			readback["claim_payload"].(map[string]any)["invocation_id"] = "invocation-substituted"
		},
		"future observation": func(readback map[string]any) {
			readback["observed_at"] = candidateControlTimestamp(now.Add(5 * time.Minute))
		},
		"negative provider counters": func(readback map[string]any) {
			replay := readback["replay_probe"].(map[string]any)
			replay["provider_calls_before"] = -2
			replay["provider_calls_after"] = -1
			for _, name := range []string{
				"ref_escape_probe",
				"production_remote_probe",
				"production_ref_probe",
			} {
				probe := readback[name].(map[string]any)
				probe["provider_calls_before"] = -1
				probe["provider_calls_after"] = -1
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
			capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
			readback["capability_digest"] = candidateControlDigest(t, capability)
			mutate(readback)
			readbackData := signCandidateControlJSON(t, readback, readbackKey)

			if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil {
				t.Fatal("resigned false candidate-control evidence was accepted")
			}
		})
	}
}

func TestVerifyCandidateControlExerciseRejectsSignatureAndExecutorSubstitution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
	capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	readback["capability_digest"] = candidateControlDigest(t, capability)
	readbackData := signCandidateControlJSON(t, readback, readbackKey)

	t.Run("tampered signature", func(t *testing.T) {
		var tampered map[string]any
		if err := json.Unmarshal(readbackData, &tampered); err != nil {
			t.Fatal(err)
		}
		tampered["operation_count"] = float64(3)
		tamperedData, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, tamperedData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "signature") {
			t.Fatalf("tampered readback error = %v", err)
		}
	})

	t.Run("substituted executor root", func(t *testing.T) {
		otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		substituted, err := NewTrustRoot(
			"substituted-readback-root",
			readbackRoot.Issuer,
			CandidateControlReadbackPurpose,
			otherPublic,
			now.Add(-time.Hour),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, substituted, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "executor") {
			t.Fatalf("substituted executor root error = %v", err)
		}
	})
}

func TestVerifyCandidateControlExerciseRejectsRetroactiveTrustEnrollment(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()

	t.Run("capability issuer enrolled after issuance", func(t *testing.T) {
		capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
		capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
		readback["capability_digest"] = candidateControlDigest(t, capability)
		readbackData := signCandidateControlJSON(t, readback, readbackKey)
		capabilityRoot.EnrolledAt = now.Add(-30 * time.Second)

		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "enrolled before") {
			t.Fatalf("retroactive capability root error = %v", err)
		}
	})

	t.Run("executor enrolled after observation", func(t *testing.T) {
		capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
		capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
		readback["capability_digest"] = candidateControlDigest(t, capability)
		readbackData := signCandidateControlJSON(t, readback, readbackKey)
		readbackRoot.EnrolledAt = now.Add(30 * time.Second)

		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "enrolled before") {
			t.Fatalf("retroactive readback root error = %v", err)
		}
	})

	t.Run("capability enrollment equal to issuance", func(t *testing.T) {
		capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
		capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
		readbackData := signCandidateControlJSON(t, readback, readbackKey)
		capabilityRoot.EnrolledAt = now.Add(-time.Minute)

		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "enrolled before") {
			t.Fatalf("equal capability enrollment error = %v", err)
		}
	})

	t.Run("readback enrollment equal to observation", func(t *testing.T) {
		capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
		capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
		readbackData := signCandidateControlJSON(t, readback, readbackKey)
		readbackRoot.EnrolledAt = now

		if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "enrolled before") {
			t.Fatalf("equal readback enrollment error = %v", err)
		}
	})
}

func TestVerifyCandidateControlExerciseRequiresCanonicalExactSchema(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	capability, readback, capabilityRoot, readbackRoot, capabilityKey, readbackKey := candidateControlFixture(t, now)
	capabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	readback["capability_digest"] = candidateControlDigest(t, capability)
	readbackData := signCandidateControlJSON(t, readback, readbackKey)
	validNamespace := capability["ref_namespace"].(string)

	nonCanonical := append([]byte(" "), capabilityData...)
	if _, err := VerifyCandidateControlExercise(nonCanonical, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("noncanonical capability error = %v", err)
	}
	var upperSignature map[string]any
	if err := json.Unmarshal(capabilityData, &upperSignature); err != nil {
		t.Fatal(err)
	}
	upperSignature["signature"] = strings.ToUpper(upperSignature["signature"].(string))
	upperSignatureData, err := json.Marshal(upperSignature)
	if err != nil {
		t.Fatal(err)
	}
	upperSignatureData = append(upperSignatureData, '\n')
	if _, err := VerifyCandidateControlExercise(upperSignatureData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "signature encoding") {
		t.Fatalf("uppercase signature error = %v", err)
	}
	var extra map[string]any
	if err := json.Unmarshal(readbackData, &extra); err != nil {
		t.Fatal(err)
	}
	extra["unbound"] = true
	extraData := signCandidateControlJSON(t, extra, readbackKey)
	if _, err := VerifyCandidateControlExercise(capabilityData, capabilityRoot, extraData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "shape") {
		t.Fatalf("extra readback field error = %v", err)
	}

	caseAlias := make(map[string]any, len(capability))
	for key, value := range capability {
		caseAlias[key] = value
	}
	caseAlias["remote"] = "N24Q02M/production-control"
	caseAliasData := signCandidateControlJSON(t, caseAlias, capabilityKey)
	if _, err := VerifyCandidateControlExercise(caseAliasData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "remote binding") {
		t.Fatalf("case-alias remote error = %v", err)
	}

	capability["ref_namespace"] = "refs/tags/bdrive-candidate/bd-control-20260831/"
	tagCapabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	if _, err := VerifyCandidateControlExercise(tagCapabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "ref namespace") {
		t.Fatalf("tag namespace capability error = %v", err)
	}
	capability["ref_namespace"] = validNamespace + "café/"
	unicodeCapabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	if _, err := VerifyCandidateControlExercise(unicodeCapabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "ref namespace") {
		t.Fatalf("unicode ref namespace capability error = %v", err)
	}

	capability["ref_namespace"] = validNamespace
	operation := capability["operations"].([]any)[0].(map[string]any)
	validExpectedOID := operation["expected_oid"]
	operation["expected_oid"] = strings.Repeat("1", 64)
	longOIDCapabilityData := signCandidateControlJSON(t, capability, capabilityKey)
	if _, err := VerifyCandidateControlExercise(longOIDCapabilityData, capabilityRoot, readbackData, readbackRoot, candidateControlTestTrustBundleDigest, now); err == nil || !strings.Contains(err.Error(), "OID binding") {
		t.Fatalf("64-character object id capability error = %v", err)
	}
	operation["expected_oid"] = validExpectedOID
}

func candidateControlFixture(t *testing.T, now time.Time) (map[string]any, map[string]any, TrustRoot, TrustRoot, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	capabilityPublic, capabilityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	readbackPublic, readbackPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	capabilityRoot, err := NewTrustRoot(
		"candidate-capability-root",
		"skret-candidate-issuer",
		CandidateControlIssuerPurpose,
		capabilityPublic,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	readbackRoot, err := NewTrustRoot(
		"candidate-readback-root",
		"skret-candidate-executor-1",
		CandidateControlReadbackPurpose,
		readbackPublic,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	transaction := "bd-control-20260831"
	namespace := "refs/heads/bdrive-candidate/" + transaction + "/"
	remoteURL := "https://github.com/n24q02m/synthetic-control.git"
	operations := []any{
		map[string]any{"ref": namespace + "lease", "expected_oid": strings.Repeat("1", 40), "desired_oid": strings.Repeat("2", 40)},
		map[string]any{"ref": namespace + "retention", "expected_oid": strings.Repeat("3", 40), "desired_oid": strings.Repeat("4", 40)},
	}
	capability := map[string]any{
		"schema_version":         1,
		"purpose":                CandidateControlIssuerPurpose,
		"transaction_id":         transaction,
		"issuer":                 capabilityRoot.Issuer,
		"executor_id":            readbackRoot.Issuer,
		"executor_public_key":    hex.EncodeToString(readbackPublic),
		"client_id":              "better-drive-candidate-client-1",
		"remote":                 "n24q02m/synthetic-control",
		"remote_url_digest":      sha256Hex([]byte(remoteURL)),
		"ref_namespace":          namespace,
		"claim_ref":              namespace + "claim",
		"completion_ref":         namespace + "completion",
		"operations":             operations,
		"operation_budget":       2,
		"ref_escape_probe":       "refs/heads/main",
		"production_remote":      "n24q02m/production-control",
		"production_ref":         "refs/heads/main",
		"teardown_intent_digest": strings.Repeat("a", 64),
		"issued_at":              candidateControlTimestamp(now.Add(-time.Minute)),
		"expires_at":             candidateControlTimestamp(now.Add(10 * time.Minute)),
		"nonce":                  "bd-control-nonce-1",
	}
	capabilityDigest := candidateControlDigest(t, capability)
	claimPayload := map[string]any{
		"schema_version":      1,
		"purpose":             CandidateControlClaimPurpose,
		"transaction_id":      transaction,
		"capability_digest":   capabilityDigest,
		"executor_id":         readbackRoot.Issuer,
		"executor_public_key": capability["executor_public_key"],
		"invocation_id":       "invocation-winner-1",
		"claimed_at":          candidateControlTimestamp(now.Add(-30 * time.Second)),
	}
	claimOID := candidateControlClaimOIDFromMap(t, claimPayload)
	replayClaimPayload := map[string]any{
		"schema_version":      1,
		"purpose":             CandidateControlClaimPurpose,
		"transaction_id":      transaction,
		"capability_digest":   capabilityDigest,
		"executor_id":         readbackRoot.Issuer,
		"executor_public_key": capability["executor_public_key"],
		"invocation_id":       "invocation-replay-1",
		"claimed_at":          candidateControlTimestamp(now),
	}
	replayOID := candidateControlClaimOIDFromMap(t, replayClaimPayload)
	operationReadbacks := []any{
		map[string]any{"ref": namespace + "lease", "expected_oid": strings.Repeat("1", 40), "desired_oid": strings.Repeat("2", 40), "before_oid": strings.Repeat("1", 40), "after_oid": strings.Repeat("2", 40)},
		map[string]any{"ref": namespace + "retention", "expected_oid": strings.Repeat("3", 40), "desired_oid": strings.Repeat("4", 40), "before_oid": strings.Repeat("3", 40), "after_oid": strings.Repeat("4", 40)},
	}
	finalOperations := []any{
		map[string]any{"ref": namespace + "lease", "oid": strings.Repeat("2", 40)},
		map[string]any{"ref": namespace + "retention", "oid": strings.Repeat("4", 40)},
	}
	atomicUpdates := []any{
		map[string]any{"kind": "claim", "ref": namespace + "claim", "expected_oid": nil, "desired_oid": claimOID, "before_oid": nil, "after_oid": claimOID},
		map[string]any{"kind": "operation", "ref": namespace + "lease", "expected_oid": strings.Repeat("1", 40), "desired_oid": strings.Repeat("2", 40), "before_oid": strings.Repeat("1", 40), "after_oid": strings.Repeat("2", 40)},
		map[string]any{"kind": "operation", "ref": namespace + "retention", "expected_oid": strings.Repeat("3", 40), "desired_oid": strings.Repeat("4", 40), "before_oid": strings.Repeat("3", 40), "after_oid": strings.Repeat("4", 40)},
		map[string]any{"kind": "completion", "ref": namespace + "completion", "expected_oid": nil, "desired_oid": claimOID, "before_oid": nil, "after_oid": claimOID},
	}
	replayUpdates := []any{
		map[string]any{"kind": "claim", "ref": namespace + "claim", "expected_oid": nil, "desired_oid": replayOID, "before_oid": claimOID, "after_oid": claimOID},
		map[string]any{"kind": "operation", "ref": namespace + "lease", "expected_oid": strings.Repeat("1", 40), "desired_oid": strings.Repeat("2", 40), "before_oid": strings.Repeat("2", 40), "after_oid": strings.Repeat("2", 40)},
		map[string]any{"kind": "operation", "ref": namespace + "retention", "expected_oid": strings.Repeat("3", 40), "desired_oid": strings.Repeat("4", 40), "before_oid": strings.Repeat("4", 40), "after_oid": strings.Repeat("4", 40)},
		map[string]any{"kind": "completion", "ref": namespace + "completion", "expected_oid": nil, "desired_oid": replayOID, "before_oid": claimOID, "after_oid": claimOID},
	}
	readback := map[string]any{
		"schema_version":    1,
		"purpose":           CandidateControlReadbackPurpose,
		"transaction_id":    transaction,
		"capability_digest": capabilityDigest,
		"issuer":            readbackRoot.Issuer,
		"executor_id":       readbackRoot.Issuer,
		"client_id":         "better-drive-candidate-client-1",
		"remote":            "n24q02m/synthetic-control",
		"remote_url_digest": capability["remote_url_digest"],
		"transport":         "github",
		"claim_payload":     claimPayload,
		"claim": map[string]any{
			"ref": namespace + "claim", "oid": claimOID, "invocation_id": "invocation-winner-1", "created": true,
		},
		"completion": map[string]any{
			"ref": namespace + "completion", "oid": claimOID, "invocation_id": "invocation-winner-1", "created": true,
		},
		"operations":      operationReadbacks,
		"operation_count": 2,
		"atomic_push": map[string]any{
			"attempted": true, "exit_code": 0, "reconciled": false, "updates": atomicUpdates,
		},
		"replay_probe": map[string]any{
			"attempted":               true,
			"provider_invoked":        true,
			"provider_calls_before":   10,
			"provider_calls_after":    19,
			"exit_code":               1,
			"outcome":                 "ALREADY_COMPLETED",
			"winning_claim_oid":       claimOID,
			"attempted_claim_oid":     replayOID,
			"attempted_invocation_id": "invocation-replay-1",
			"attempted_claim_payload": replayClaimPayload,
			"claim_oid_before":        claimOID,
			"claim_oid_after":         claimOID,
			"completion_oid_before":   claimOID,
			"completion_oid_after":    claimOID,
			"operations_before":       finalOperations,
			"operations_after":        finalOperations,
			"updates":                 replayUpdates,
		},
		"ref_escape_probe": map[string]any{
			"remote": capability["remote"], "ref": capability["ref_escape_probe"], "provider_invoked": false, "provider_calls_before": 19, "provider_calls_after": 19, "outcome": "REF_OUTSIDE_NAMESPACE",
		},
		"production_remote_probe": map[string]any{
			"remote": capability["production_remote"], "ref": capability["production_ref"], "provider_invoked": false, "provider_calls_before": 19, "provider_calls_after": 19, "outcome": "REMOTE_OUTSIDE_ALLOWLIST",
		},
		"production_ref_probe": map[string]any{
			"remote": capability["remote"], "ref": capability["production_ref"], "provider_invoked": false, "provider_calls_before": 19, "provider_calls_after": 19, "outcome": "REF_OUTSIDE_NAMESPACE",
		},
		"observed_at": candidateControlTimestamp(now),
	}
	return capability, readback, capabilityRoot, readbackRoot, capabilityPrivate, readbackPrivate
}

func candidateControlClaimOIDFromMap(t *testing.T, value map[string]any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var payload CandidateControlClaimPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	oid, err := CandidateControlClaimCommitOID(payload)
	if err != nil {
		t.Fatal(err)
	}
	return oid
}

func signCandidateControlJSON(t *testing.T, unsigned map[string]any, privateKey ed25519.PrivateKey) []byte {
	t.Helper()
	copy := make(map[string]any, len(unsigned)+1)
	for key, value := range unsigned {
		if key != "signature" {
			copy[key] = value
		}
	}
	canonical, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	copy["signature"] = hex.EncodeToString(ed25519.Sign(privateKey, canonical))
	signed, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	return append(signed, '\n')
}

func candidateControlDigest(t *testing.T, unsigned map[string]any) string {
	t.Helper()
	copy := make(map[string]any, len(unsigned))
	for key, value := range unsigned {
		if key != "signature" {
			copy[key] = value
		}
	}
	canonical, err := json.Marshal(copy)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(canonical)
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func candidateControlTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
