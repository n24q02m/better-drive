package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/paths"
)

func TestWriteCandidateControlTrustBundleCreatesAndCASRotatesFixedBundle(t *testing.T) {
	setTestConfigHome(t, t.TempDir())
	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	initial := testCandidateControlTrustBundle(t, now)
	initialData, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	initialDigest, err := writeCandidateControlTrustBundle(initialData, nil)
	if err != nil {
		t.Fatalf("create candidate-control trust bundle: %v", err)
	}
	if _, err := os.Stat(paths.CleanupCandidateControlTrustBundleFile()); err != nil {
		t.Fatalf("fixed candidate-control trust bundle path: %v", err)
	}
	if _, err := writeCandidateControlTrustBundle(initialData, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate candidate-control enrollment error = %v", err)
	}

	rotated := initial
	rotatedPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotated.CapabilityRoot, err = cleanup.NewTrustRoot(
		"candidate-capability-root-rotated",
		"skret-candidate-issuer-rotated",
		cleanup.CandidateControlIssuerPurpose,
		rotatedPublic,
		now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	rotatedData, err := json.Marshal(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeCandidateControlTrustBundle(rotatedData, new(strings.Repeat("0", 64))); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale candidate-control rotation error = %v", err)
	}
	current, currentDigest, err := readProtectedCandidateControlTrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	if current != initial || currentDigest != initialDigest {
		t.Fatalf("candidate-control bundle changed after stale rotation: bundle=%+v digest=%s", current, currentDigest)
	}

	rotatedDigest, err := writeCandidateControlTrustBundle(rotatedData, &initialDigest)
	if err != nil {
		t.Fatalf("rotate candidate-control trust bundle: %v", err)
	}
	installed, installedDigest, err := readProtectedCandidateControlTrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	if installed != rotated || installedDigest != rotatedDigest || rotatedDigest == initialDigest {
		t.Fatalf("candidate-control rotated bundle mismatch: bundle=%+v digest=%s", installed, installedDigest)
	}
}

func TestCandidateControlTrustBundleRequiresSeparatedRoots(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	capabilityRoot, err := cleanup.NewTrustRoot(
		"candidate-capability-root",
		"skret-candidate-issuer",
		cleanup.CandidateControlIssuerPurpose,
		publicKey,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	readbackRoot, err := cleanup.NewTrustRoot(
		"candidate-readback-root",
		"skret-candidate-executor",
		cleanup.CandidateControlReadbackPurpose,
		publicKey,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	bundle := candidateControlTrustBundle{
		SchemaVersion:  candidateControlTrustBundleSchema,
		CapabilityRoot: capabilityRoot,
		ReadbackRoot:   readbackRoot,
	}
	if err := validateCandidateControlTrustBundle(bundle, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "separate") {
		t.Fatalf("shared candidate-control key error = %v", err)
	}
}

func testCandidateControlTrustBundle(t *testing.T, enrolledAt time.Time) candidateControlTrustBundle {
	t.Helper()
	capabilityPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	readbackPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	capabilityRoot, err := cleanup.NewTrustRoot(
		"candidate-capability-root",
		"skret-candidate-issuer",
		cleanup.CandidateControlIssuerPurpose,
		capabilityPublic,
		enrolledAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	readbackRoot, err := cleanup.NewTrustRoot(
		"candidate-readback-root",
		"skret-candidate-executor",
		cleanup.CandidateControlReadbackPurpose,
		readbackPublic,
		enrolledAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return candidateControlTrustBundle{
		SchemaVersion:  candidateControlTrustBundleSchema,
		CapabilityRoot: capabilityRoot,
		ReadbackRoot:   readbackRoot,
	}
}
