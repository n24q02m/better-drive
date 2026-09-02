package driveapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validOAuthCredential(now time.Time) OAuthCredential {
	return OAuthCredential{
		SchemaVersion: CurrentOAuthCredentialSchemaVersion,
		AccessToken:   "old-access-token",
		RefreshToken:  "private-refresh-token",
		TokenType:     "Bearer",
		Expiry:        now.Add(time.Minute),
		ClientID:      "client-id",
		ClientSecret:  "private-client-secret",
	}
}

func TestOAuthTokenSourceRefreshesBeforeExpiryAndReusesFreshToken(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("refresh request = %s content-type=%q", request.Method, request.Header.Get("Content-Type"))
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "private-refresh-token" ||
			request.Form.Get("client_id") != "client-id" || request.Form.Get("client_secret") != "private-client-secret" {
			t.Fatalf("refresh form keys were not exact")
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "fresh-access-token",
			"expires_in":   3600,
			"token_type":   "Bearer",
			"scope":        "https://www.googleapis.com/auth/drive",
		})
	}))
	defer server.Close()

	source, err := newOAuthTokenSource(server.Client(), server.URL, validOAuthCredential(now), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		token, err := source.AccessToken(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if token != "fresh-access-token" {
			t.Fatalf("access token = %q", token)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want one", calls.Load())
	}
}

func TestOAuthTokenSourceUsesRotatedRefreshToken(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	currentTime := now
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		expectedRefreshToken := "private-refresh-token"
		if call == 2 {
			expectedRefreshToken = "rotated-refresh-token"
		}
		if request.Form.Get("refresh_token") != expectedRefreshToken {
			t.Fatalf("refresh token on call %d was not the current token", call)
		}
		response := map[string]any{
			"access_token": fmt.Sprintf("fresh-access-token-%d", call),
			"expires_in":   600,
			"token_type":   "Bearer",
		}
		if call == 1 {
			response["refresh_token"] = "rotated-refresh-token"
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	source, err := newOAuthTokenSource(server.Client(), server.URL, validOAuthCredential(now), func() time.Time { return currentTime })
	if err != nil {
		t.Fatal(err)
	}
	if token, err := source.AccessToken(t.Context()); err != nil || token != "fresh-access-token-1" {
		t.Fatalf("first access token = %q, error = %v", token, err)
	}
	currentTime = now.Add(6 * time.Minute)
	if token, err := source.AccessToken(t.Context()); err != nil || token != "fresh-access-token-2" {
		t.Fatalf("second access token = %q, error = %v", token, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls = %d, want two", calls.Load())
	}
}

func TestOAuthTokenSourceUsesUnexpiredAccessTokenWithoutRefresh(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	credential := validOAuthCredential(now)
	credential.Expiry = now.Add(time.Hour)
	source, err := newOAuthTokenSource(server.Client(), server.URL, credential, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.AccessToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token != credential.AccessToken || calls.Load() != 0 {
		t.Fatalf("token=%q refresh calls=%d", token, calls.Load())
	}
}

func TestDecodeOAuthCredentialIsStrict(t *testing.T) {
	credential := validOAuthCredential(time.Unix(100, 0).UTC())
	data, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOAuthCredential(data); err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := DecodeOAuthCredential(unknown); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := DecodeOAuthCredential(append(data, []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing data error = %v", err)
	}
}

func TestOAuthTokenSourceRedactsRefreshFailure(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "private-refresh-token private-client-secret provider-body", http.StatusBadGateway)
	}))
	defer server.Close()
	source, err := newOAuthTokenSource(server.Client(), server.URL, validOAuthCredential(now), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.AccessToken(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	for _, secret := range []string{"private-refresh-token", "private-client-secret", "provider-body"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("refresh error leaked %q: %v", secret, err)
		}
	}
}
