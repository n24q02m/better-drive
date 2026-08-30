package cleanup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func allowOwnerRiskPeer(*http.Request) error { return nil }

func TestOwnerRiskHTTPHandlerRunsGitAuthorityLifecycle(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	handler, err := NewOwnerRiskHTTPHandler(fixture.authority, allowOwnerRiskPeer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := NewOwnerRiskHTTPAuthority(server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}

	snapshotRequest := OwnerRiskSnapshotRequest{
		SchemaVersion:    CurrentOwnerRiskSchemaVersion,
		Repository:       fixture.request.Repository,
		ApprovalID:       fixture.request.ApprovalID,
		ManifestDigest:   fixture.request.ManifestDigest,
		QuarantineTarget: fixture.request.QuarantineTarget,
		RequestID:        "snapshot-http-01",
	}
	snapshot, err := client.SnapshotOwnerRisk(context.Background(), snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskSnapshot(snapshot, snapshotRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatal(err)
	}
	claim, err := client.ClaimOwnerRisk(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskClaim(claim, fixture.request, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatal(err)
	}
	fenceRequest := OwnerRiskFenceRequest{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       claim.ClaimID,
		ClaimDigest:   claimDigest(t, claim),
		ApprovalID:    claim.Request.ApprovalID,
		ExecutionID:   claim.Request.ExecutionID,
		RequestID:     claim.Request.RequestID,
		StateOID:      claim.StateOID,
		JournalOID:    claim.JournalOID,
		LeaseOID:      claim.LeaseOID,
		Generation:    claim.Generation,
		Fence:         claim.Fence,
	}
	readback, err := client.RecheckOwnerRisk(context.Background(), fenceRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskFenceReadback(readback, fenceRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatal(err)
	}
	settlementRequest := OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            claim.ClaimID,
		ClaimDigest:        fenceRequest.ClaimDigest,
		ApprovalID:         claim.Request.ApprovalID,
		ExecutionID:        claim.Request.ExecutionID,
		RequestID:          claim.Request.RequestID,
		StateExpectedOID:   claim.StateOID,
		JournalExpectedOID: claim.JournalOID,
		LeaseExpectedOID:   claim.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("e", 64),
		ProviderRequests:   []string{"drive-request-http-01"},
	}
	settlement, err := client.SettleOwnerRisk(context.Background(), settlementRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnerRiskSettlement(settlement, settlementRequest, fixture.authorityPublicKey, fixture.now); err != nil {
		t.Fatal(err)
	}
	finalSnapshot, err := fixture.store.ReadSnapshot(fixture.request.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if finalSnapshot.State.State != OwnerRiskConsumed {
		t.Fatalf("HTTP lifecycle state = %q, want consumed", finalSnapshot.State.State)
	}
}

func TestOwnerRiskHTTPHandlerRejectsUnauthenticatedAndMalformedRequests(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	handler, err := NewOwnerRiskHTTPHandler(fixture.authority, RequireMTLSOwnerRiskPeer)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		authorize   bool
		wantStatus  int
	}{
		{name: "method", method: http.MethodGet, contentType: "application/json", body: `{}`, wantStatus: http.StatusMethodNotAllowed},
		{name: "missing mTLS", method: http.MethodPost, contentType: "application/json", body: `{}`, wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/v1/owner-risk/snapshot", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.authorize {
				request.Header.Set("Authorization", "Bearer forbidden")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
		})
	}

	authenticatedHandler, err := NewOwnerRiskHTTPHandler(fixture.authority, allowOwnerRiskPeer)
	if err != nil {
		t.Fatal(err)
	}
	malformed := []struct {
		name        string
		contentType string
		body        string
		authorize   bool
		wantStatus  int
	}{
		{name: "authorization header", contentType: "application/json", body: `{}`, authorize: true, wantStatus: http.StatusUnauthorized},
		{name: "content type", contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown fields", contentType: "application/json", body: `{"unknown":true}`, wantStatus: http.StatusBadRequest},
		{name: "trailing document", contentType: "application/json", body: `{} {}`, wantStatus: http.StatusBadRequest},
		{name: "oversized", contentType: "application/json", body: strings.Repeat("x", maxOwnerRiskBrokerBodyBytes+1), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range malformed {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/owner-risk/snapshot", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.authorize {
				request.Header.Set("Authorization", "Bearer forbidden")
			}
			response := httptest.NewRecorder()
			authenticatedHandler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "forbidden") || strings.Contains(response.Body.String(), "unknown") {
				t.Fatalf("generic error leaked request detail: %s", response.Body.String())
			}
			var generic map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &generic); err != nil || generic["error"] == "" {
				t.Fatalf("generic JSON error = %q, decode err=%v", response.Body.String(), err)
			}
		})
	}
}

func TestRequireMTLSOwnerRiskPeerRequiresVerifiedLeaf(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := RequireMTLSOwnerRiskPeer(request); err == nil {
		t.Fatal("request without TLS unexpectedly authenticated")
	}
	leaf := &x509.Certificate{Raw: []byte("leaf")}
	request.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
	}
	if err := RequireMTLSOwnerRiskPeer(request); err != nil {
		t.Fatalf("verified peer rejected: %v", err)
	}
	request.TLS.VerifiedChains = [][]*x509.Certificate{{{Raw: []byte("other")}}}
	if err := RequireMTLSOwnerRiskPeer(request); err == nil {
		t.Fatal("mismatched verified leaf unexpectedly authenticated")
	}
}

func TestOwnerRiskHTTPHandlerConstructorRequiresAuthorityAndPeerVerifier(t *testing.T) {
	fixture := newGitAuthorityFixture(t)
	if _, err := NewOwnerRiskHTTPHandler(nil, allowOwnerRiskPeer); err == nil {
		t.Fatal("nil authority unexpectedly accepted")
	}
	if _, err := NewOwnerRiskHTTPHandler(fixture.authority, nil); err == nil {
		t.Fatal("nil peer verifier unexpectedly accepted")
	}
}
