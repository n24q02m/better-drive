package driveapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

const (
	googleDriveAPIBaseURL = "https://www.googleapis.com/drive/v3/"
	maxProviderBodyBytes  = 1 << 20
	maxAccessTokenBytes   = 16 << 10
)

var (
	ErrSettlementUnknown = errors.New("provider settlement unknown")
	exactDriveIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

type moveRequest struct {
	Expected            cleanup.Object `json:"expected"`
	DestinationParentID string         `json:"destination_parent_id"`
	AttemptID           string         `json:"attempt_id"`
}

type FileState struct {
	ID             string    `json:"id"`
	ParentID       string    `json:"parent_id"`
	ContentHash    string    `json:"content_hash"`
	Size           int64     `json:"size"`
	ModifiedAt     time.Time `json:"modified_at"`
	Version        string    `json:"version"`
	Generation     string    `json:"generation"`
	MetadataDigest string    `json:"metadata_digest"`
	Trashed        bool      `json:"trashed"`
}

type MoveResult struct {
	Before            FileState `json:"before"`
	After             FileState `json:"after"`
	AttemptID         string    `json:"attempt_id"`
	MutationAttempted bool      `json:"mutation_attempted"`
}

type quarantineHTTPClient struct {
	client      *http.Client
	baseURL     *url.URL
	tokenSource AccessTokenSource
}

func newQuarantineHTTPClient(client *http.Client, endpoint, accessToken string) (*quarantineHTTPClient, error) {
	tokenSource, err := NewStaticAccessTokenSource(accessToken)
	if err != nil {
		return nil, err
	}
	return newQuarantineHTTPClientWithTokenSource(client, endpoint, tokenSource)
}

func newQuarantineHTTPClientWithTokenSource(
	client *http.Client,
	endpoint string,
	tokenSource AccessTokenSource,
) (*quarantineHTTPClient, error) {
	if client == nil {
		return nil, errors.New("Drive HTTP client is required")
	}
	if tokenSource == nil {
		return nil, errors.New("Drive access token source is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || !strings.HasSuffix(parsed.Path, "/") {
		return nil, errors.New("Drive API endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("Drive API endpoint must use HTTPS")
	}
	if client.Transport != nil {
		if _, ok := client.Transport.(*http.Transport); !ok {
			return nil, errors.New("Drive HTTP client must use the standard non-retrying transport")
		}
	}
	boundedClient := *client
	if boundedClient.Timeout <= 0 || boundedClient.Timeout > 30*time.Second {
		boundedClient.Timeout = 30 * time.Second
	}
	boundedClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &quarantineHTTPClient{client: &boundedClient, baseURL: parsed, tokenSource: tokenSource}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validateDriveID(value, name string) error {
	if exactDriveIDPattern.MatchString(value) {
		return nil
	}
	return fmt.Errorf("%s must be an exact Drive ID", name)
}

func (client *quarantineHTTPClient) fileURL(fileID string, query url.Values) string {
	resolved := *client.baseURL
	resolved.Path += "files/" + url.PathEscape(fileID)
	resolved.RawQuery = query.Encode()
	return resolved.String()
}

func (client *quarantineHTTPClient) request(ctx context.Context, method, requestURL string, body []byte, mutation bool) ([]byte, http.Header, int, error) {
	if ctx == nil {
		return nil, nil, 0, errors.New("context is nil")
	}
	accessToken, err := client.tokenSource.AccessToken(ctx)
	if err != nil {
		return nil, nil, 0, errors.New("Drive access token source failed")
	}
	if err := validateSecretField(accessToken, "Drive access token", maxAccessTokenBytes); err != nil {
		return nil, nil, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, errors.New("build Drive request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		if mutation {
			return nil, nil, 0, fmt.Errorf("%w: Drive mutation transport failed: %w", ErrSettlementUnknown, err)
		}
		return nil, nil, 0, fmt.Errorf("Drive metadata read failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxProviderBodyBytes+1))
	if err != nil {
		if mutation {
			return nil, nil, response.StatusCode, fmt.Errorf("%w: Drive mutation response was unreadable: %w", ErrSettlementUnknown, err)
		}
		return nil, nil, response.StatusCode, fmt.Errorf("Drive metadata response was unreadable: %w", err)
	}
	if len(data) > maxProviderBodyBytes {
		if mutation {
			return nil, nil, response.StatusCode, fmt.Errorf("%w: Drive mutation response exceeded the size bound", ErrSettlementUnknown)
		}
		return nil, nil, response.StatusCode, errors.New("Drive metadata response exceeded the size bound")
	}
	return data, response.Header.Clone(), response.StatusCode, nil
}

type driveFile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name,omitempty"`
	MIMEType       string   `json:"mimeType,omitempty"`
	Parents        []string `json:"parents"`
	Trashed        bool     `json:"trashed"`
	MD5Checksum    string   `json:"md5Checksum"`
	Size           string   `json:"size"`
	ModifiedTime   string   `json:"modifiedTime"`
	Version        string   `json:"version"`
	HeadRevisionID string   `json:"headRevisionId"`
}

func driveMetadataDigest(file driveFile) (string, error) {
	if file.ID == "" || file.Name == "" || file.MIMEType == "" || len(file.Parents) != 1 ||
		file.ModifiedTime == "" || file.Version == "" {
		return "", errors.New("Drive metadata identity is incomplete")
	}
	data, err := json.Marshal(struct {
		ID             string   `json:"id"`
		Name           string   `json:"name"`
		MIMEType       string   `json:"mime_type"`
		Parents        []string `json:"parents"`
		Trashed        bool     `json:"trashed"`
		MD5Checksum    string   `json:"md5_checksum"`
		Size           string   `json:"size"`
		ModifiedTime   string   `json:"modified_time"`
		Version        string   `json:"version"`
		HeadRevisionID string   `json:"head_revision_id"`
	}{
		ID: file.ID, Name: file.Name, MIMEType: file.MIMEType, Parents: file.Parents,
		Trashed: file.Trashed, MD5Checksum: file.MD5Checksum, Size: file.Size,
		ModifiedTime: file.ModifiedTime, Version: file.Version, HeadRevisionID: file.HeadRevisionID,
	})
	if err != nil {
		return "", fmt.Errorf("canonicalize Drive metadata: %w", err)
	}
	return cleanup.Digest(data), nil
}

func decodeFileState(data []byte, expectedID string) (FileState, error) {
	var file driveFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return FileState{}, errors.New("Drive metadata response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FileState{}, errors.New("Drive metadata response has trailing JSON")
	}
	if file.ID != expectedID || len(file.Parents) != 1 || file.Trashed {
		return FileState{}, errors.New("Drive metadata identity, parent, or trash state is unsafe")
	}
	size, err := strconv.ParseInt(file.Size, 10, 64)
	if err != nil || size < 0 {
		return FileState{}, errors.New("Drive metadata size is invalid")
	}
	modifiedAt, err := time.Parse(time.RFC3339Nano, file.ModifiedTime)
	if err != nil {
		return FileState{}, errors.New("Drive metadata modified time is invalid")
	}
	if file.Version == "" || file.HeadRevisionID == "" {
		return FileState{}, errors.New("Drive metadata version readback is incomplete")
	}
	metadataDigest, err := driveMetadataDigest(file)
	if err != nil {
		return FileState{}, err
	}
	return FileState{
		ID:             file.ID,
		ParentID:       file.Parents[0],
		ContentHash:    file.MD5Checksum,
		Size:           size,
		ModifiedAt:     modifiedAt.UTC(),
		Version:        file.Version,
		Generation:     file.HeadRevisionID,
		MetadataDigest: metadataDigest,
		Trashed:        file.Trashed,
	}, nil
}

func (client *quarantineHTTPClient) read(ctx context.Context, fileID string) (FileState, error) {
	if err := validateDriveID(fileID, "file ID"); err != nil {
		return FileState{}, err
	}
	query := url.Values{
		"alt":               {"json"},
		"fields":            {"id,name,mimeType,parents,trashed,md5Checksum,size,modifiedTime,version,headRevisionId"},
		"supportsAllDrives": {"true"},
	}
	data, _, status, err := client.request(ctx, http.MethodGet, client.fileURL(fileID, query), nil, false)
	if err != nil {
		return FileState{}, err
	}
	if status != http.StatusOK {
		return FileState{}, fmt.Errorf("Drive metadata read rejected with status %d", status)
	}
	return decodeFileState(data, fileID)
}

func validateExpectedState(expected cleanup.Object, observed FileState) error {
	if observed.ID != expected.ID ||
		observed.ParentID != expected.ParentID ||
		observed.ContentHash != expected.ContentHash ||
		observed.Size != expected.Size ||
		!observed.ModifiedAt.Equal(expected.ModifiedAt) ||
		observed.Version != expected.Version ||
		observed.Generation != expected.Generation ||
		observed.MetadataDigest != expected.MetadataDigest ||
		observed.Trashed != expected.Trashed {
		return errors.New("Drive object metadata drifted before mutation")
	}
	return nil
}

func validatePostMove(before, after FileState, destinationParentID string) error {
	if after.ID != before.ID || after.ParentID != destinationParentID || after.Trashed ||
		after.ContentHash != before.ContentHash || after.Size != before.Size ||
		!after.ModifiedAt.Equal(before.ModifiedAt) || after.Generation != before.Generation {
		return errors.New("Drive move readback did not preserve the exact object")
	}
	return nil
}

func (client *quarantineHTTPClient) move(ctx context.Context, request moveRequest) (MoveResult, error) {
	if client == nil || client.client == nil || client.baseURL == nil {
		return MoveResult{}, errors.New("Drive quarantine client is not configured")
	}
	if err := validateDriveID(request.Expected.ID, "file ID"); err != nil {
		return MoveResult{}, err
	}
	if err := validateDriveID(request.Expected.ParentID, "source parent ID"); err != nil {
		return MoveResult{}, err
	}
	if err := validateDriveID(request.DestinationParentID, "destination parent ID"); err != nil {
		return MoveResult{}, err
	}
	if request.DestinationParentID == request.Expected.ParentID || request.DestinationParentID == request.Expected.ID {
		return MoveResult{}, errors.New("Drive destination parent is not isolated from the source object")
	}
	before, err := client.read(ctx, request.Expected.ID)
	if err != nil {
		return MoveResult{}, err
	}
	if err := validateExpectedState(request.Expected, before); err != nil {
		return MoveResult{}, err
	}
	// Drive v3 documents no conditional update precondition. Do not send
	// If-Match and imply CAS. The signed cleanup claim explicitly accepts the
	// owner-risk race, authorizes exactly one PATCH, and retains its lease when
	// the settlement is unknown.
	query := url.Values{
		"addParents":        {request.DestinationParentID},
		"fields":            {"id"},
		"removeParents":     {request.Expected.ParentID},
		"supportsAllDrives": {"true"},
	}
	result := MoveResult{Before: before, AttemptID: request.AttemptID, MutationAttempted: true}
	_, _, status, err := client.request(ctx, http.MethodPatch, client.fileURL(request.Expected.ID, query), []byte("{}"), true)
	if err != nil {
		return result, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if status >= http.StatusInternalServerError {
			return result, fmt.Errorf("%w: Drive mutation returned status %d", ErrSettlementUnknown, status)
		}
		return result, fmt.Errorf("Drive mutation rejected with status %d", status)
	}
	after, err := client.read(ctx, request.Expected.ID)
	if err != nil {
		return result, fmt.Errorf("%w: Drive post-mutation readback failed", ErrSettlementUnknown)
	}
	result.After = after
	if err := validatePostMove(before, after, request.DestinationParentID); err != nil {
		return result, fmt.Errorf("%w: Drive post-mutation state is ambiguous", ErrSettlementUnknown)
	}
	return result, nil
}
