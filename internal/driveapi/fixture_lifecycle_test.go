package driveapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/protectedfs"
)

func TestFixtureLifecycleExecutorRunsExactThreePhaseSequenceAndRejectsReplay(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var parent atomic.Value
	parent.Store("fixture-source")
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/files/fixture-file" {
			http.NotFound(writer, request)
			return
		}
		if request.Method == http.MethodPatch {
			query := request.URL.Query()
			if query.Get("removeParents") != parent.Load().(string) {
				http.Error(writer, "wrong source parent", http.StatusConflict)
				return
			}
			parent.Store(query.Get("addParents"))
			patches.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"fixture-file"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id":             "fixture-file",
			"name":           "fixture.bin",
			"mimeType":       "application/octet-stream",
			"parents":        []string{parent.Load().(string)},
			"trashed":        false,
			"md5Checksum":    strings.Repeat("a", 32),
			"size":           "10",
			"modifiedTime":   now.Format(time.RFC3339Nano),
			"version":        "1",
			"headRevisionId": "generation-1",
		})
	}))
	defer server.Close()

	capability := fixtureLifecycleCapability(t, now, privateKey)
	repoPath := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(repoPath); err != nil {
		t.Fatal(err)
	}
	repo, err := cleanup.NewGitRepo(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newFixtureLifecycleExecutor(server.Client(), server.URL+"/", "fixture-token", publicKey, receiptStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(t.Context(), capability)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalParentID != "fixture-quarantine" || len(result.Moves) != 3 || patches.Load() != 3 {
		t.Fatalf("fixture lifecycle result = %+v patches=%d", result, patches.Load())
	}
	wantParents := []string{"fixture-quarantine", "fixture-source", "fixture-quarantine"}
	for index, move := range result.Moves {
		if move.After.ParentID != wantParents[index] {
			t.Fatalf("phase %d parent = %q, want %q", index, move.After.ParentID, wantParents[index])
		}
	}
	replayStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	replayExecutor, err := newFixtureLifecycleExecutor(server.Client(), server.URL+"/", "fixture-token", publicKey, replayStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayExecutor.Execute(t.Context(), capability); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("fixture capability replay error = %v", err)
	}
	if patches.Load() != 3 {
		t.Fatalf("replay issued provider mutation: patches=%d", patches.Load())
	}
}

func TestFixtureLifecycleExecutorPersistsAttemptBeforeProviderFailure(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var patches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch {
			patches.Add(1)
			writer.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id":             "fixture-file",
			"name":           "fixture.bin",
			"mimeType":       "application/octet-stream",
			"parents":        []string{"fixture-source"},
			"trashed":        false,
			"md5Checksum":    strings.Repeat("a", 32),
			"size":           "10",
			"modifiedTime":   now.Format(time.RFC3339Nano),
			"version":        "1",
			"headRevisionId": "generation-1",
		})
	}))
	defer server.Close()
	repoPath := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(repoPath); err != nil {
		t.Fatal(err)
	}
	repo, err := cleanup.NewGitRepo(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newFixtureLifecycleExecutor(server.Client(), server.URL+"/", "fixture-token", publicKey, receiptStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := fixtureLifecycleCapability(t, now, privateKey)
	if _, err := executor.Execute(t.Context(), request); !errors.Is(err, ErrSettlementUnknown) {
		t.Fatalf("provider rejection error = %v, want settlement unknown", err)
	}
	replayStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	replayExecutor, err := newFixtureLifecycleExecutor(server.Client(), server.URL+"/", "fixture-token", publicKey, replayStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayExecutor.Execute(t.Context(), request); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("replay error = %v", err)
	}
	if patches.Load() != 1 {
		t.Fatalf("provider PATCH calls = %d, want one", patches.Load())
	}
}

func TestFreezeFixtureLifecycleRejectsMalformedMetadataDigest(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, metadataDigest := range []string{"not-a-sha256", strings.Repeat("A", 64)} {
		capability := fixtureLifecycleCapability(t, now, privateKey).Capability
		capability.Initial.MetadataDigest = metadataDigest
		if _, err := FreezeFixtureLifecycleCapability(capability); err == nil || !strings.Contains(err.Error(), "active fixture") {
			t.Fatalf("metadata digest %q error = %v, want active-fixture rejection", metadataDigest, err)
		}
	}
}

func TestFixtureLifecycleExecutorDeniesProductionRootBeforeProviderCall(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	request := fixtureLifecycleCapability(t, now, privateKey)
	mutated := request.Capability
	mutated.RootID = "production-root"
	if _, err := FreezeFixtureLifecycleCapability(mutated); err == nil {
		t.Fatal("fixture capability overlapping production root was frozen")
	}
	request.Capability = mutated
	repoPath := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(repoPath); err != nil {
		t.Fatal(err)
	}
	repo, err := cleanup.NewGitRepo(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptStore, err := cleanup.NewGitFixtureLifecycleReceiptStore(repo, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	executor, err := newFixtureLifecycleExecutor(server.Client(), server.URL+"/", "fixture-token", publicKey, receiptStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(t.Context(), request); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("production denial error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("production denial reached provider: requests=%d", requests.Load())
	}
}

func fixtureLifecycleCapability(t *testing.T, now time.Time, privateKey ed25519.PrivateKey) FixtureLifecycleRequest {
	t.Helper()
	metadataDigest, err := driveMetadataDigest(driveFile{
		ID: "fixture-file", Name: "fixture.bin", MIMEType: "application/octet-stream",
		Parents: []string{"fixture-source"}, MD5Checksum: strings.Repeat("a", 32), Size: "10",
		ModifiedTime: now.Format(time.RFC3339Nano), Version: "1", HeadRevisionID: "generation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := FreezeFixtureLifecycleCapability(FixtureLifecycleCapability{
		SchemaVersion:      CurrentFixtureLifecycleSchemaVersion,
		FixtureID:          "candidate-fixture-1",
		ArtifactDigest:     strings.Repeat("b", 64),
		Issuer:             "cleanup-signer",
		Provider:           "drive",
		AccountID:          "candidate-account",
		RootID:             "candidate-root",
		Namespace:          "candidate-fixture",
		ObjectID:           "fixture-file",
		OriginalParentID:   "fixture-source",
		QuarantineParentID: "fixture-quarantine",
		ProductionRootIDs:  []string{"production-root"},
		Sequence:           []string{FixturePhaseQuarantine, FixturePhaseRestore, FixturePhaseRequarantine},
		CreatedAt:          now.Add(-time.Minute),
		ExpiresAt:          now.Add(time.Hour),
		Nonce:              "fixture-nonce-1",
		MutationSemantics:  FixtureMutationSemantics,
		ProductionDenied:   true,
		Initial: cleanup.Object{
			ID: "fixture-file", ParentID: "fixture-source", Name: "fixture.bin", Path: "/candidate-fixture/fixture.bin", ObjectType: cleanup.ObjectTypeFile,
			ContentHash: strings.Repeat("a", 32), Size: 10, Provider: "drive", AccountID: "candidate-account", RootID: "candidate-root", Namespace: "candidate-fixture",
			Version: "1", Generation: "generation-1", MetadataDigest: metadataDigest, ModifiedAt: now, Class: cleanup.ClassExpectedFixture,
			OwnershipMarker: "fixture:candidate-fixture-1", RestoreEvidence: "fixture:candidate-fixture-1:" + strings.Repeat("b", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalFixtureLifecycleCapability(capability)
	if err != nil {
		t.Fatal(err)
	}
	return FixtureLifecycleRequest{Capability: capability, SignatureHex: hex.EncodeToString(ed25519.Sign(privateKey, canonical))}
}

func parseFixtureMoveQuery(values url.Values) (string, string) {
	return values.Get("removeParents"), values.Get("addParents")
}
