package driveapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type Page struct {
	Cursor   string           `json:"cursor"`
	Next     string           `json:"next_cursor,omitempty"`
	Complete bool             `json:"complete"`
	Objects  []cleanup.Object `json:"objects"`
}

const NoCASAccepted = "accepted"

// ErrUnknownSettlement marks a mutation whose provider settlement cannot be
// proven after the provider call. Callers must retain the request fence and
// must not retry it automatically.
var ErrUnknownSettlement = errors.New("unknown Drive mutation settlement")

// MutationRequest is a complete, immutable description of one Drive object
// mutation. Provider generations and versions are opaque strings and must be
// echoed exactly by the provider readback.
type MutationRequest struct {
	ObjectID            string             `json:"object_id"`
	AccountID           string             `json:"account_id"`
	RootID              string             `json:"root_id"`
	Namespace           string             `json:"namespace"`
	ParentID            string             `json:"parent_id"`
	Mode                cleanup.Mode       `json:"mode"`
	ExpectedETag        string             `json:"expected_etag"`
	Version             string             `json:"version"`
	Generation          string             `json:"generation"`
	Size                int64              `json:"size"`
	Hash                string             `json:"hash"`
	RequestID           string             `json:"request_id"`
	Capability          MutationCapability `json:"capability"`
	NoCASRiskAccepted   bool               `json:"no_cas_risk_accepted"`
	NoCASClassification string             `json:"no_cas_classification"`
	OwnerRiskApproved   bool               `json:"owner_risk_approved"`
}

type MutationCapability struct {
	ObjectID     string       `json:"object_id"`
	AccountID    string       `json:"account_id"`
	RootID       string       `json:"root_id"`
	Namespace    string       `json:"namespace"`
	ParentID     string       `json:"parent_id"`
	Mode         cleanup.Mode `json:"mode"`
	ExpectedETag string       `json:"expected_etag"`
	Version      string       `json:"version"`
	Generation   string       `json:"generation"`
	Size         int64        `json:"size"`
	Hash         string       `json:"hash"`
	RequestID    string       `json:"request_id"`
	ExpiresAt    time.Time    `json:"expires_at"`
	Signature    string       `json:"-"`
}

// SignedMutationCapability is an explicit name for the exact capability
// value accepted by Mutate.
type SignedMutationCapability = MutationCapability

type MutationResult struct {
	ProviderID string `json:"provider_id"`
	State      string `json:"state"`
	ObjectID   string `json:"object_id"`
	AccountID  string `json:"account_id"`
	RootID     string `json:"root_id"`
	Namespace  string `json:"namespace"`
	ParentID   string `json:"parent_id"`
	ETag       string `json:"etag"`
	Version    string `json:"version"`
	Generation string `json:"generation"`
	Size       int64  `json:"size"`
	Hash       string `json:"hash"`
	RequestID  string `json:"request_id"`
}

type Provider interface {
	List(ctx context.Context, accountID, rootID, cursor string) (Page, error)
	Mutate(ctx context.Context, request MutationRequest) (MutationResult, error)
}

type Client struct {
	provider Provider
	mu       sync.Mutex
	used     map[string]struct{}
	Now      func() time.Time
}

func NewClient(provider Provider) *Client {
	return &Client{provider: provider, used: make(map[string]struct{}), Now: time.Now}
}

func NewMutationCapability(request MutationRequest, expiresAt time.Time, signature string) MutationCapability {
	return MutationCapability{
		ObjectID: request.ObjectID, AccountID: request.AccountID, RootID: request.RootID,
		Namespace: request.Namespace, ParentID: request.ParentID, Mode: request.Mode,
		ExpectedETag: request.ExpectedETag, Version: request.Version, Generation: request.Generation,
		Size: request.Size, Hash: request.Hash, RequestID: request.RequestID, ExpiresAt: expiresAt,
		Signature: signature,
	}
}

func (capability MutationCapability) String() string {
	return strings.Join([]string{capability.AccountID, capability.RootID, capability.Namespace, capability.ObjectID, capability.Version, capability.Generation, capability.ExpectedETag}, "\x00")
}

func (client *Client) List(ctx context.Context, accountID, rootID, cursor string) (Page, error) {
	if client == nil || client.provider == nil {
		return Page{}, errors.New("Drive provider is not configured")
	}
	if accountID == "" || rootID == "" {
		return Page{}, errors.New("account and root IDs are required")
	}
	if err := contextErr(ctx); err != nil {
		return Page{}, err
	}
	return client.provider.List(ctx, accountID, rootID, cursor)
}

func (client *Client) Mutate(ctx context.Context, request MutationRequest) (MutationResult, error) {
	if client == nil || client.provider == nil {
		return MutationResult{}, errors.New("Drive provider is not configured")
	}
	if err := contextErr(ctx); err != nil {
		return MutationResult{}, err
	}
	if err := validateMutationRequest(request); err != nil {
		return MutationResult{}, err
	}
	if err := request.Capability.validate(request, client.now()); err != nil {
		return MutationResult{}, err
	}
	client.mu.Lock()
	if _, exists := client.used[request.RequestID]; exists {
		client.mu.Unlock()
		return MutationResult{}, errors.New("mutation request replay rejected")
	}
	client.used[request.RequestID] = struct{}{}
	client.mu.Unlock()
	result, err := client.provider.Mutate(ctx, request)
	if err != nil {
		return MutationResult{}, fmt.Errorf("Drive mutation outcome unknown: %w", errors.Join(ErrUnknownSettlement, err))
	}
	if err := contextErr(ctx); err != nil {
		return MutationResult{}, fmt.Errorf("Drive mutation outcome unknown: %w", errors.Join(ErrUnknownSettlement, err))
	}
	if !mutationReadbackMatches(request, result) {
		readbackErr := errors.New("provider mutation readback does not match exact object identity, version, generation, ETag, size, hash, parent, or request identity")
		return MutationResult{}, fmt.Errorf("Drive mutation outcome unknown: %w", errors.Join(ErrUnknownSettlement, readbackErr))
	}
	return result, nil
}

func (capability MutationCapability) validate(request MutationRequest, now time.Time) error {
	if capability.Signature == "" || capability.RequestID == "" {
		return errors.New("Drive mutation requires exact typed capability")
	}
	if !capability.ExpiresAt.After(now) {
		return errors.New("Drive mutation capability is expired")
	}
	if capability != NewMutationCapability(request, capability.ExpiresAt, capability.Signature) {
		return errors.New("Drive mutation capability scope drifted")
	}
	return nil
}

func validateMutationRequest(request MutationRequest) error {
	for name, value := range map[string]string{
		"object ID": request.ObjectID, "account": request.AccountID, "root": request.RootID,
		"namespace": request.Namespace, "parent ID": request.ParentID, "expected ETag": request.ExpectedETag,
		"version": request.Version, "generation": request.Generation, "hash": request.Hash, "request ID": request.RequestID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Drive %s is required", name)
		}
	}
	if request.Size < 0 {
		return errors.New("Drive object size must not be negative")
	}
	if request.Mode != cleanup.ModeQuarantine && request.Mode != cleanup.ModeTrash {
		return fmt.Errorf("unsupported mutation mode %q", request.Mode)
	}
	if !request.NoCASRiskAccepted || request.NoCASClassification != NoCASAccepted || !request.OwnerRiskApproved {
		return errors.New("Drive mutation requires owner-risk capability, no-CAS classification, and approval")
	}
	return nil
}

func mutationReadbackMatches(request MutationRequest, result MutationResult) bool {
	return result.ProviderID == request.ObjectID && result.ObjectID == request.ObjectID && result.AccountID == request.AccountID && result.RootID == request.RootID && result.Namespace == request.Namespace && result.ParentID == request.ParentID && result.ETag == request.ExpectedETag && result.Version == request.Version && result.Generation == request.Generation && result.Size == request.Size && result.Hash == request.Hash && result.RequestID == request.RequestID && result.State != ""
}

func (client *Client) now() time.Time {
	if client != nil && client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
