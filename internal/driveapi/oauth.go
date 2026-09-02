package driveapi

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
	"sync"
	"time"
	"unicode"
)

const (
	CurrentOAuthCredentialSchemaVersion = 1
	maxOAuthResponseBytes               = 64 << 10
	oauthRefreshSkew                    = 5 * time.Minute
)

// AccessTokenSource returns a current bearer token before one provider request.
// Implementations must never retry the provider operation itself.
type AccessTokenSource interface {
	AccessToken(context.Context) (string, error)
}

// OAuthCredential is the bounded refresh envelope delivered through a
// protected inherited descriptor. It must never be written to evidence.
type OAuthCredential struct {
	SchemaVersion int       `json:"schema_version"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	TokenType     string    `json:"token_type"`
	Expiry        time.Time `json:"expiry"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret"`
}

// OAuthTokenSource serializes proactive refreshes and returns one current
// bearer token for each provider request.
type OAuthTokenSource struct {
	client       *http.Client
	endpoint     string
	now          func() time.Time
	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiry       time.Time
	clientID     string
	clientSecret string
}

type staticAccessTokenSource struct {
	token string
}

func (source staticAccessTokenSource) AccessToken(context.Context) (string, error) {
	return source.token, nil
}

// NewStaticAccessTokenSource preserves the legacy inherited-token contract for
// callers that cannot yet provide a refresh envelope.
func NewStaticAccessTokenSource(accessToken string) (AccessTokenSource, error) {
	if err := validateSecretField(accessToken, "Drive access token", maxAccessTokenBytes); err != nil {
		return nil, err
	}
	return staticAccessTokenSource{token: accessToken}, nil
}

// DecodeOAuthCredential accepts exactly one current-schema JSON envelope.
func DecodeOAuthCredential(data []byte) (OAuthCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var credential OAuthCredential
	if err := decoder.Decode(&credential); err != nil {
		return OAuthCredential{}, fmt.Errorf("decode Drive OAuth credential: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OAuthCredential{}, errors.New("decode Drive OAuth credential: trailing JSON is not allowed")
	}
	if err := validateOAuthCredential(credential); err != nil {
		return OAuthCredential{}, err
	}
	return credential, nil
}

// NewGoogleOAuthTokenSource binds the refresh envelope to Google's fixed token
// endpoint and a standard non-retrying HTTP client.
func NewGoogleOAuthTokenSource(client *http.Client, credential OAuthCredential) (*OAuthTokenSource, error) {
	return newOAuthTokenSource(client, "https://oauth2.googleapis.com/token", credential, time.Now)
}

func newOAuthTokenSource(
	client *http.Client,
	endpoint string,
	credential OAuthCredential,
	now func() time.Time,
) (*OAuthTokenSource, error) {
	if err := validateOAuthCredential(credential); err != nil {
		return nil, err
	}
	if now == nil || now().UTC().IsZero() {
		return nil, errors.New("Drive OAuth clock is required")
	}
	boundedClient, err := boundedOAuthClient(client)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.Host == "" {
		return nil, errors.New("Drive OAuth token endpoint is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isOAuthLoopback(parsed.Hostname())) {
		return nil, errors.New("Drive OAuth token endpoint must use HTTPS")
	}
	return &OAuthTokenSource{
		client: boundedClient, endpoint: parsed.String(), now: now,
		accessToken: credential.AccessToken, refreshToken: credential.RefreshToken,
		expiry: credential.Expiry.UTC(), clientID: credential.ClientID, clientSecret: credential.ClientSecret,
	}, nil
}

func (source *OAuthTokenSource) AccessToken(ctx context.Context) (string, error) {
	if source == nil || source.client == nil || source.now == nil {
		return "", errors.New("Drive OAuth token source is not configured")
	}
	if ctx == nil {
		return "", errors.New("Drive OAuth token context is nil")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.expiry.After(source.now().UTC().Add(oauthRefreshSkew)) {
		return source.accessToken, nil
	}
	form := url.Values{
		"client_id":     {source.clientID},
		"client_secret": {source.clientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {source.refreshToken},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("build Drive OAuth refresh request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := source.client.Do(request)
	if err != nil {
		return "", errors.New("Drive OAuth refresh transport failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponseBytes+1))
	if err != nil {
		return "", errors.New("Drive OAuth refresh response was unreadable")
	}
	if len(data) > maxOAuthResponseBytes {
		return "", errors.New("Drive OAuth refresh response exceeded the size bound")
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Drive OAuth refresh returned status %d", response.StatusCode)
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/json") || response.Header.Get("Content-Encoding") != "" {
		return "", errors.New("Drive OAuth refresh returned a non-JSON response")
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token,omitempty"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&refreshed); err != nil {
		return "", errors.New("Drive OAuth refresh response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("Drive OAuth refresh response contains trailing data")
	}
	if err := validateSecretField(refreshed.AccessToken, "Drive refreshed access token", maxAccessTokenBytes); err != nil ||
		!strings.EqualFold(refreshed.TokenType, "Bearer") || refreshed.ExpiresIn < 60 || refreshed.ExpiresIn > 86400 {
		return "", errors.New("Drive OAuth refresh response is incomplete")
	}
	if refreshed.RefreshToken != "" {
		if err := validateSecretField(refreshed.RefreshToken, "Drive refreshed refresh token", maxAccessTokenBytes); err != nil {
			return "", errors.New("Drive OAuth refresh response is incomplete")
		}
		source.refreshToken = refreshed.RefreshToken
	}
	source.accessToken = refreshed.AccessToken
	source.expiry = source.now().UTC().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	return source.accessToken, nil
}

func validateOAuthCredential(credential OAuthCredential) error {
	if credential.SchemaVersion != CurrentOAuthCredentialSchemaVersion {
		return fmt.Errorf("unsupported Drive OAuth credential schema_version %d", credential.SchemaVersion)
	}
	for name, value := range map[string]string{
		"access token":  credential.AccessToken,
		"refresh token": credential.RefreshToken,
		"client ID":     credential.ClientID,
		"client secret": credential.ClientSecret,
	} {
		if err := validateSecretField(value, "Drive OAuth "+name, maxAccessTokenBytes); err != nil {
			return err
		}
	}
	if !strings.EqualFold(credential.TokenType, "Bearer") || credential.Expiry.IsZero() {
		return errors.New("Drive OAuth token type or expiry is invalid")
	}
	return nil
}

func validateSecretField(value, name string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is missing or malformed", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s is missing or malformed", name)
		}
	}
	return nil
}

func boundedOAuthClient(client *http.Client) (*http.Client, error) {
	if client == nil {
		return nil, errors.New("Drive OAuth HTTP client is required")
	}
	if client.Transport != nil {
		if _, ok := client.Transport.(*http.Transport); !ok {
			return nil, errors.New("Drive OAuth HTTP client must use the standard non-retrying transport")
		}
	}
	bounded := *client
	if bounded.Timeout <= 0 || bounded.Timeout > 30*time.Second {
		bounded.Timeout = 30 * time.Second
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &bounded, nil
}

func isOAuthLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
