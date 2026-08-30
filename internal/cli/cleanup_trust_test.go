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

func TestWriteCleanupTrustBundleCreatesAndCASRotatesFixedBundle(t *testing.T) {
	setTestConfigHome(t, t.TempDir())
	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	initial := testCleanupTrustBundle(t, now, "initial")
	initialData, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	initialDigest, err := writeCleanupTrustBundle(initialData, nil)
	if err != nil {
		t.Fatalf("create trust bundle: %v", err)
	}
	if len(initialDigest) != 64 {
		t.Fatalf("initial digest = %q", initialDigest)
	}
	if _, err := os.Stat(paths.CleanupTrustBundleFile()); err != nil {
		t.Fatalf("fixed trust bundle path: %v", err)
	}
	if _, err := writeCleanupTrustBundle(initialData, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate enrollment error = %v", err)
	}

	rotated := testCleanupTrustBundle(t, now.Add(time.Hour), "rotated")
	rotatedData, err := json.Marshal(rotated)
	if err != nil {
		t.Fatal(err)
	}
	wrongDigest := strings.Repeat("0", 64)
	if _, err := writeCleanupTrustBundle(rotatedData, &wrongDigest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale rotation error = %v", err)
	}
	current, err := readProtectedTrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	if current.ApprovalRoot.Fingerprint != initial.ApprovalRoot.Fingerprint {
		t.Fatal("stale rotation changed the active trust bundle")
	}

	rotatedDigest, err := writeCleanupTrustBundle(rotatedData, &initialDigest)
	if err != nil {
		t.Fatalf("rotate trust bundle: %v", err)
	}
	if rotatedDigest == initialDigest {
		t.Fatal("rotation did not change the trust bundle digest")
	}
	current, err = readProtectedTrustBundle()
	if err != nil {
		t.Fatal(err)
	}
	if current.ApprovalRoot.Fingerprint != rotated.ApprovalRoot.Fingerprint ||
		current.AuthorityRoot.Fingerprint != rotated.AuthorityRoot.Fingerprint ||
		current.Broker.Authority != rotated.Broker.Authority {
		t.Fatalf("rotated trust bundle = %+v", current)
	}
}

func TestCleanupTrustBundleRequiresSeparatedApprovalAndAuthorityKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoot, err := cleanup.NewTrustRoot("approval-root", "cleanup-signer", cleanup.CleanupTrustPurpose, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	authorityRoot, err := cleanup.NewTrustRoot("authority-root", "cleanup-broker", cleanup.OwnerRiskAuthorityPurpose, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	pki := brokerTestCertificates(t)
	bundle := cleanupTrustBundle{
		SchemaVersion: cleanupTrustBundleSchema,
		ApprovalRoot:  approvalRoot,
		AuthorityRoot: authorityRoot,
		Broker: cleanupBrokerConfig{
			SchemaVersion: cleanupBrokerConfigSchema,
			Endpoint:      "https://127.0.0.1:9443/",
			Repository:    "n24q02m/private-control",
			Authority:     authorityRoot.Issuer,
			Owner:         "executor-home",
		},
		BrokerServerCAPEM: string(pki.serverCAPEM),
	}
	if err := validateCleanupTrustBundle(bundle, now); err == nil || !strings.Contains(err.Error(), "separate") {
		t.Fatalf("shared-key bundle error = %v", err)
	}
}

func TestCleanupTrustRotationAllowsPinnedServerCAOnly(t *testing.T) {
	current := testCleanupTrustBundle(t, time.Now().UTC().Add(-time.Hour), "current")
	next := current
	next.BrokerServerCAPEM = string(brokerTestCertificates(t).serverCAPEM)
	if err := validateCleanupTrustBundle(next, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := validateCleanupTrustRotation(current, next); err != nil {
		t.Fatalf("server CA-only rotation: %v", err)
	}
}

func TestCleanupTrustCommandsAreRegistered(t *testing.T) {
	for _, arguments := range [][]string{{"trust", "enroll"}, {"trust", "rotate"}} {
		command, _, err := cleanupCmd().Find(arguments)
		if err != nil {
			t.Fatal(err)
		}
		if command == nil || command.Name() != arguments[1] {
			t.Fatalf("cleanup %v command not found: %v", arguments, command)
		}
	}
}

func testCleanupTrustBundle(t *testing.T, enrolledAt time.Time, suffix string) cleanupTrustBundle {
	t.Helper()
	pki := brokerTestCertificates(t)
	approvalPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoot, err := cleanup.NewTrustRoot(
		"approval-root-"+suffix,
		"cleanup-signer-"+suffix,
		cleanup.CleanupTrustPurpose,
		approvalPublicKey,
		enrolledAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	authorityRoot, err := cleanup.NewTrustRoot(
		"authority-root-"+suffix,
		"cleanup-broker-"+suffix,
		cleanup.OwnerRiskAuthorityPurpose,
		authorityPublicKey,
		enrolledAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return cleanupTrustBundle{
		SchemaVersion: cleanupTrustBundleSchema,
		ApprovalRoot:  approvalRoot,
		AuthorityRoot: authorityRoot,
		Broker: cleanupBrokerConfig{
			SchemaVersion: cleanupBrokerConfigSchema,
			Endpoint:      "https://127.0.0.1:9443/",
			Repository:    "n24q02m/private-control",
			Authority:     authorityRoot.Issuer,
			Owner:         "executor-home",
		},
		BrokerServerCAPEM: string(pki.serverCAPEM),
	}
}
