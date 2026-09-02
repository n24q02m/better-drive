package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validFenceRequest(t *testing.T) OwnerRiskFenceRequest {
	t.Helper()
	claimRequest := validOwnerRiskClaimRequest(t)
	claimRequest.ExpiresAt = time.Now().UTC().Add(time.Minute)
	canonical, err := CanonicalOwnerRiskClaimRequest(claimRequest)
	if err != nil {
		t.Fatal(err)
	}
	return OwnerRiskFenceRequest{
		SchemaVersion: CurrentOwnerRiskSchemaVersion,
		ClaimID:       "claim-http-01",
		ClaimDigest:   Digest(canonical),
		ApprovalID:    claimRequest.ApprovalID,
		ExecutionID:   claimRequest.ExecutionID,
		RequestID:     claimRequest.RequestID,
		StateOID:      strings.Repeat("3", 40),
		JournalOID:    strings.Repeat("4", 40),
		LeaseOID:      strings.Repeat("5", 40),
		Generation:    1,
		Fence:         1,
	}
}

func validSettlementRequest(t *testing.T) OwnerRiskSettlementRequest {
	t.Helper()
	fence := validFenceRequest(t)
	return OwnerRiskSettlementRequest{
		SchemaVersion:      CurrentOwnerRiskSchemaVersion,
		ClaimID:            fence.ClaimID,
		ClaimDigest:        fence.ClaimDigest,
		ApprovalID:         fence.ApprovalID,
		ExecutionID:        fence.ExecutionID,
		RequestID:          fence.RequestID,
		StateExpectedOID:   fence.StateOID,
		JournalExpectedOID: fence.JournalOID,
		LeaseExpectedOID:   fence.LeaseOID,
		Settlement:         OwnerRiskConsumed,
		OutcomeDigest:      strings.Repeat("6", 64),
		ProviderRequests:   []string{"drive-attempt-01"},
	}
}

func TestOwnerRiskHTTPAuthorityUsesBoundedExactRoutesWithoutBearer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "" {
			t.Errorf("method=%q authorization=%q", request.Method, request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/broker/v1/owner-risk/snapshot":
			_ = json.NewEncoder(writer).Encode(OwnerRiskSnapshot{SchemaVersion: CurrentOwnerRiskSchemaVersion})
		case "/broker/v1/owner-risk/claim":
			var body OwnerRiskClaimRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(writer).Encode(OwnerRiskClaim{SchemaVersion: CurrentOwnerRiskSchemaVersion, ClaimID: "claim-http-01"})
		case "/broker/v1/owner-risk/recheck":
			_ = json.NewEncoder(writer).Encode(OwnerRiskFenceReadback{SchemaVersion: CurrentOwnerRiskSchemaVersion, State: OwnerRiskClaimed})
		case "/broker/v1/owner-risk/settle":
			_ = json.NewEncoder(writer).Encode(OwnerRiskSettlement{SchemaVersion: CurrentOwnerRiskSchemaVersion, State: OwnerRiskConsumed})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	authority, err := NewOwnerRiskHTTPAuthority(server.Client(), server.URL+"/broker/")
	if err != nil {
		t.Fatal(err)
	}
	claimRequest := validOwnerRiskClaimRequest(t)
	claimRequest.ExpiresAt = time.Now().UTC().Add(time.Minute)
	snapshotRequest := OwnerRiskSnapshotRequest{
		SchemaVersion:    CurrentOwnerRiskSchemaVersion,
		Repository:       claimRequest.Repository,
		ApprovalID:       claimRequest.ApprovalID,
		ManifestDigest:   claimRequest.ManifestDigest,
		QuarantineTarget: claimRequest.QuarantineTarget,
		RequestID:        claimRequest.RequestID,
	}
	if _, err := authority.SnapshotOwnerRisk(context.Background(), snapshotRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ClaimOwnerRisk(context.Background(), claimRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RecheckOwnerRisk(context.Background(), validFenceRequest(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.SettleOwnerRisk(context.Background(), validSettlementRequest(t)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("broker calls = %d, want four", calls.Load())
	}
}

func TestOwnerRiskHTTPAuthorityTreatsAmbiguousWriteAsUnknownAndRedactsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("private-token-and-provider-secret"))
	}))
	defer server.Close()
	authority, err := NewOwnerRiskHTTPAuthority(server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	request := validOwnerRiskClaimRequest(t)
	request.ExpiresAt = time.Now().UTC().Add(time.Minute)
	_, err = authority.ClaimOwnerRisk(context.Background(), request)
	if !errors.Is(err, ErrOwnerRiskAuthorityUnknown) {
		t.Fatalf("ClaimOwnerRisk() error = %v, want unknown", err)
	}
	if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("broker error leaked response body: %v", err)
	}
}

func TestOwnerRiskHTTPAuthorityDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	authority, err := NewOwnerRiskHTTPAuthority(server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	request := validOwnerRiskClaimRequest(t)
	request.ExpiresAt = time.Now().UTC().Add(time.Minute)
	_, err = authority.ClaimOwnerRisk(context.Background(), request)
	if !errors.Is(err, ErrOwnerRiskAuthorityUnknown) {
		t.Fatalf("redirect error = %v, want unknown", err)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target calls = %d, want zero", redirected.Load())
	}
}

func TestOwnerRiskHTTPAuthorityRejectsJSONPrefixContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json-malformed")
		_, _ = writer.Write([]byte("{}"))
	}))
	defer server.Close()
	authority, err := NewOwnerRiskHTTPAuthority(server.Client(), server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RecheckOwnerRisk(t.Context(), validFenceRequest(t)); err == nil ||
		!strings.Contains(err.Error(), "non-JSON") {
		t.Fatalf("content type error = %v", err)
	}
}
