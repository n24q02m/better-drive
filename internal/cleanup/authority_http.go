package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxOwnerRiskBrokerBodyBytes = 256 << 10

var ErrOwnerRiskAuthorityUnknown = errors.New("owner-risk authority settlement unknown")

// OwnerRiskHTTPAuthority calls a protected, peer-authenticated broker. The
// supplied HTTP client owns transport authentication (for example mTLS or a
// local peer-authenticated socket); this client never accepts or emits bearer
// credentials and never follows redirects.
type OwnerRiskHTTPAuthority struct {
	client  *http.Client
	baseURL *url.URL
}

func NewOwnerRiskHTTPAuthority(client *http.Client, endpoint string) (*OwnerRiskHTTPAuthority, error) {
	if client == nil {
		return nil, errors.New("owner-risk broker HTTP client is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || !strings.HasSuffix(parsed.Path, "/") {
		return nil, errors.New("owner-risk broker endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && ownerRiskLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("owner-risk broker endpoint must use HTTPS")
	}
	boundedClient := *client
	if boundedClient.Timeout <= 0 || boundedClient.Timeout > 30*time.Second {
		boundedClient.Timeout = 30 * time.Second
	}
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &OwnerRiskHTTPAuthority{client: &boundedClient, baseURL: parsed}, nil
}

func (authority *OwnerRiskHTTPAuthority) SnapshotOwnerRisk(ctx context.Context, request OwnerRiskSnapshotRequest) (OwnerRiskSnapshot, error) {
	if _, err := CanonicalOwnerRiskSnapshotRequest(request); err != nil {
		return OwnerRiskSnapshot{}, err
	}
	var response OwnerRiskSnapshot
	if err := authority.post(ctx, "v1/owner-risk/snapshot", request, &response, false); err != nil {
		return OwnerRiskSnapshot{}, err
	}
	return response, nil
}

func (authority *OwnerRiskHTTPAuthority) ClaimOwnerRisk(ctx context.Context, request OwnerRiskClaimRequest) (OwnerRiskClaim, error) {
	if _, err := CanonicalOwnerRiskClaimRequest(request); err != nil {
		return OwnerRiskClaim{}, err
	}
	var response OwnerRiskClaim
	if err := authority.post(ctx, "v1/owner-risk/claim", request, &response, true); err != nil {
		return OwnerRiskClaim{}, err
	}
	return response, nil
}

func (authority *OwnerRiskHTTPAuthority) RecheckOwnerRisk(ctx context.Context, request OwnerRiskFenceRequest) (OwnerRiskFenceReadback, error) {
	if _, err := CanonicalOwnerRiskFenceRequest(request); err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	var response OwnerRiskFenceReadback
	if err := authority.post(ctx, "v1/owner-risk/recheck", request, &response, false); err != nil {
		return OwnerRiskFenceReadback{}, err
	}
	return response, nil
}

func (authority *OwnerRiskHTTPAuthority) SettleOwnerRisk(ctx context.Context, request OwnerRiskSettlementRequest) (OwnerRiskSettlement, error) {
	if _, err := CanonicalOwnerRiskSettlementRequest(request); err != nil {
		return OwnerRiskSettlement{}, err
	}
	var response OwnerRiskSettlement
	if err := authority.post(ctx, "v1/owner-risk/settle", request, &response, true); err != nil {
		return OwnerRiskSettlement{}, err
	}
	return response, nil
}

func (authority *OwnerRiskHTTPAuthority) post(ctx context.Context, route string, input, output any, ambiguousOnFailure bool) error {
	if authority == nil || authority.client == nil || authority.baseURL == nil {
		return errors.New("owner-risk broker is not configured")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return errors.New("encode owner-risk broker request")
	}
	if len(body) > maxOwnerRiskBrokerBodyBytes {
		return errors.New("owner-risk broker request exceeds size bound")
	}
	requestURL := *authority.baseURL
	requestURL.Path += route
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("build owner-risk broker request")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := authority.client.Do(httpRequest)
	if err != nil {
		return ownerRiskBrokerError(ambiguousOnFailure, "transport failed")
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxOwnerRiskBrokerBodyBytes+1))
	if readErr != nil || len(responseBody) > maxOwnerRiskBrokerBodyBytes {
		return ownerRiskBrokerError(ambiguousOnFailure, "response was unreadable")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ownerRiskBrokerError(ambiguousOnFailure, fmt.Sprintf("returned status %d", response.StatusCode))
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || !strings.EqualFold(mediaType, "application/json") ||
		response.Header.Get("Content-Encoding") != "" {
		return ownerRiskBrokerError(ambiguousOnFailure, "returned a non-JSON response")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return ownerRiskBrokerError(ambiguousOnFailure, "returned invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ownerRiskBrokerError(ambiguousOnFailure, "returned trailing JSON")
	}
	return nil
}

func ownerRiskBrokerError(ambiguous bool, detail string) error {
	if ambiguous {
		return fmt.Errorf("%w: broker %s", ErrOwnerRiskAuthorityUnknown, detail)
	}
	return fmt.Errorf("owner-risk broker %s", detail)
}

func ownerRiskLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
