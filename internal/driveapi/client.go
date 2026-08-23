package driveapi

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type Page struct {
	Cursor   string           `json:"cursor"`
	Next     string           `json:"next_cursor,omitempty"`
	Complete bool             `json:"complete"`
	Objects  []cleanup.Object `json:"objects"`
}

type MutationRequest struct {
	ObjectID          string       `json:"object_id"`
	AccountID         string       `json:"account_id"`
	RootID            string       `json:"root_id"`
	Mode              cleanup.Mode `json:"mode"`
	ExpectedETag      string       `json:"expected_etag"`
	Capability        string       `json:"capability"`
	NoCASRiskAccepted bool         `json:"no_cas_risk_accepted"`
	OwnerRiskApproved bool         `json:"owner_risk_approved"`
}

type MutationResult struct {
	ProviderID string `json:"provider_id"`
	State      string `json:"state"`
}

type Provider interface {
	List(ctx context.Context, accountID, rootID, cursor string) (Page, error)
	Mutate(ctx context.Context, request MutationRequest) (MutationResult, error)
}

type Client struct {
	provider Provider
	mu       sync.Mutex
	used     map[string]struct{}
}

func NewClient(provider Provider) *Client {
	return &Client{provider: provider, used: make(map[string]struct{})}
}

func (client *Client) List(ctx context.Context, accountID, rootID, cursor string) (Page, error) {
	if client == nil || client.provider == nil {
		return Page{}, errors.New("Drive provider is not configured")
	}
	if accountID == "" || rootID == "" {
		return Page{}, errors.New("account and root IDs are required")
	}
	return client.provider.List(ctx, accountID, rootID, cursor)
}

func (client *Client) Mutate(ctx context.Context, request MutationRequest) (MutationResult, error) {
	if client == nil || client.provider == nil {
		return MutationResult{}, errors.New("Drive provider is not configured")
	}
	if request.ObjectID == "" || request.AccountID == "" || request.RootID == "" || request.ExpectedETag == "" {
		return MutationResult{}, errors.New("object ID, account, root, and expected ETag are required")
	}
	if request.Mode != cleanup.ModeQuarantine && request.Mode != cleanup.ModeTrash {
		return MutationResult{}, fmt.Errorf("unsupported mutation mode %q", request.Mode)
	}
	if request.Capability != "BD-DRIVE-MUTATION-RW" || !request.NoCASRiskAccepted || !request.OwnerRiskApproved {
		return MutationResult{}, errors.New("Drive mutation requires owner-risk capability, no-CAS acceptance, and approval")
	}
	key := request.ObjectID + "\x00" + request.ExpectedETag + "\x00" + string(request.Mode)
	client.mu.Lock()
	if _, exists := client.used[key]; exists {
		client.mu.Unlock()
		return MutationResult{}, errors.New("mutation request replay rejected")
	}
	client.used[key] = struct{}{}
	client.mu.Unlock()
	result, err := client.provider.Mutate(ctx, request)
	if err != nil {
		return MutationResult{}, err
	}
	if result.ProviderID != request.ObjectID || result.State == "" {
		return MutationResult{}, errors.New("provider mutation readback does not match exact object")
	}
	return result, nil
}
