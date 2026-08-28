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

// ErrUnknownSettlement indicates that a Drive mutation was submitted but its
// post-mutation state could not be verified. Callers must retain destination
// leases and fencing and must not retry the mutation automatically.
var ErrUnknownSettlement = errors.New("unknown Drive mutation settlement")

type Page struct {
	Cursor   string           `json:"cursor"`
	Next     string           `json:"next_cursor,omitempty"`
	Complete bool             `json:"complete"`
	ParentID string           `json:"parent_id"`
	Objects  []cleanup.Object `json:"objects"`
}

type Provider interface {
	List(ctx context.Context, accountID, parentID, cursor string) (Page, error)
}

type MutationProvider interface {
	Provider
	Quarantine(ctx context.Context, req QuarantineRequest) (MutationReceipt, error)
	Restore(ctx context.Context, req RestoreRequest) (MutationReceipt, error)
}

type QuarantineRequest struct {
	AccountID          string `json:"account_id"`
	RootID             string `json:"root_id"`
	Namespace          string `json:"namespace"`
	ObjectID           string `json:"object_id"`
	ParentID           string `json:"parent_id"`
	QuarantineParentID string `json:"quarantine_parent_id"`
	ExpectedETag       string `json:"expected_etag"`
	Version            string `json:"version"`
	Generation         string `json:"generation"`
	Size               int64  `json:"size"`
	Hash               string `json:"hash"`
	RequestID          string `json:"request_id"`
}

type RestoreRequest struct {
	AccountID          string `json:"account_id"`
	RootID             string `json:"root_id"`
	Namespace          string `json:"namespace"`
	ObjectID           string `json:"object_id"`
	CurrentParentID    string `json:"current_parent_id"`
	OriginalParentID   string `json:"original_parent_id"`
	ExpectedETag       string `json:"expected_etag"`
	RequestID          string `json:"request_id"`
}

type MutationReceipt struct {
	ObjectID         string `json:"object_id"`
	ParentID         string `json:"parent_id"`
	State            string `json:"state"` // "quarantined" or "restored"
	ReadbackVerified bool   `json:"readback_verified"`
	RequestID        string `json:"request_id"`
}

// MutationCapability represents the signed machine claim (BD-DRIVE-MUTATION-RW)
// authorizing one-attempt owner-risk Drive quarantine or fixture restore.
// Drive offers no native CAS/ETag write preconditions, so caller must explicitly
// accept the no-CAS race risk. Native trash and permanent delete are never authorized.
type MutationCapability struct {
	ClaimID          string         `json:"claim_id"`
	Role             string         `json:"role"`
	Intent           string         `json:"intent"` // must be "BD-DRIVE-MUTATION-RW"
	ObjectID         string         `json:"object_id"`
	AccountID        string         `json:"account_id"`
	RootID           string         `json:"root_id"`
	Mode             cleanup.Mode   `json:"mode"`   // must be "quarantine"
	Budget           cleanup.Budget `json:"budget"`
	ExpiresAt        time.Time      `json:"expires_at"`
	Nonce            string         `json:"nonce"`
	Issuer           string         `json:"issuer"`
	Signature        string         `json:"signature"`
	AcceptsNoCASRisk bool           `json:"accepts_no_cas_risk"`
}

func (c MutationCapability) Validate(objectID, accountID, rootID, requestID string, now time.Time) error {
	if c.Signature == "" || c.ClaimID == "" {
		return errors.New("Drive mutation requires signed owner-risk capability")
	}
	if c.Intent != "BD-DRIVE-MUTATION-RW" {
		return fmt.Errorf("unsupported Drive mutation intent %q; requires BD-DRIVE-MUTATION-RW", c.Intent)
	}
	if c.Mode != cleanup.ModeQuarantine {
		return fmt.Errorf("unsupported Drive mutation mode %q; only quarantine is permitted", c.Mode)
	}
	if !c.AcceptsNoCASRisk {
		return errors.New("Drive mutation capability must explicitly acknowledge no-CAS race risk")
	}
	if c.ObjectID != objectID || c.AccountID != accountID || c.RootID != rootID {
		return errors.New("Drive mutation capability scope does not match target object")
	}
	if c.Budget.MaxObjects <= 0 || c.Budget.MaxBytes <= 0 {
		return errors.New("Drive mutation capability budget must be positive")
	}
	if c.ExpiresAt.IsZero() || !now.Before(c.ExpiresAt) {
		return errors.New("Drive mutation capability is expired")
	}
	if strings.TrimSpace(c.Nonce) == "" || strings.TrimSpace(c.Issuer) == "" {
		return errors.New("Drive mutation capability nonce and issuer are required")
	}
	return nil
}

type Client struct {
	provider Provider
	Now      func() time.Time
	mu       sync.Mutex
	used     map[string]struct{}
}

func NewClient(provider Provider) *Client {
	return &Client{
		provider: provider,
		Now:      time.Now,
		used:     make(map[string]struct{}),
	}
}

func (client *Client) now() time.Time {
	if client != nil && client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}

func (client *Client) reserveRequest(requestID string) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.used == nil {
		client.used = make(map[string]struct{})
	}
	if _, exists := client.used[requestID]; exists {
		return fmt.Errorf("Drive mutation request ID %q replay rejected", requestID)
	}
	client.used[requestID] = struct{}{}
	return nil
}

func (client *Client) List(ctx context.Context, accountID, parentID, cursor string) (Page, error) {
	if client == nil || client.provider == nil {
		return Page{}, errors.New("Drive provider is not configured")
	}
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(parentID) == "" {
		return Page{}, errors.New("account and parent IDs are required")
	}
	if err := contextErr(ctx); err != nil {
		return Page{}, err
	}
	page, err := client.provider.List(ctx, accountID, parentID, cursor)
	if err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(page.ParentID) == "" {
		return Page{}, errors.New("Drive provider page parent ID is required")
	}
	if page.ParentID != parentID {
		return Page{}, errors.New("Drive provider page parent ID does not match requested parent")
	}
	return page, nil
}

func (client *Client) Quarantine(ctx context.Context, req QuarantineRequest, capability MutationCapability) (MutationReceipt, error) {
	if client == nil || client.provider == nil {
		return MutationReceipt{}, errors.New("Drive provider is not configured")
	}
	mp, ok := client.provider.(MutationProvider)
	if !ok {
		return MutationReceipt{}, errors.New("Drive provider does not support mutation")
	}
	if err := validateQuarantineRequest(req); err != nil {
		return MutationReceipt{}, err
	}
	if err := capability.Validate(req.ObjectID, req.AccountID, req.RootID, req.RequestID, client.now()); err != nil {
		return MutationReceipt{}, err
	}
	if err := client.reserveRequest(req.RequestID); err != nil {
		return MutationReceipt{}, err
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, err
	}
	receipt, err := mp.Quarantine(ctx, req)
	if err != nil {
		return MutationReceipt{}, unknownSettlementError("Drive quarantine", err)
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, unknownSettlementError("Drive quarantine", err)
	}
	if !receipt.ReadbackVerified || receipt.RequestID != req.RequestID || receipt.ObjectID != req.ObjectID ||
		receipt.ParentID != req.QuarantineParentID || receipt.State != "quarantined" {
		return MutationReceipt{}, unknownSettlementError("Drive quarantine", errors.New("readback failed to prove quarantine destination"))
	}
	return receipt, nil
}

func (client *Client) Restore(ctx context.Context, req RestoreRequest, capability MutationCapability) (MutationReceipt, error) {
	if client == nil || client.provider == nil {
		return MutationReceipt{}, errors.New("Drive provider is not configured")
	}
	mp, ok := client.provider.(MutationProvider)
	if !ok {
		return MutationReceipt{}, errors.New("Drive provider does not support mutation")
	}
	if err := validateRestoreRequest(req); err != nil {
		return MutationReceipt{}, err
	}
	if err := capability.Validate(req.ObjectID, req.AccountID, req.RootID, req.RequestID, client.now()); err != nil {
		return MutationReceipt{}, err
	}
	if err := client.reserveRequest(req.RequestID); err != nil {
		return MutationReceipt{}, err
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, err
	}
	receipt, err := mp.Restore(ctx, req)
	if err != nil {
		return MutationReceipt{}, unknownSettlementError("Drive restore", err)
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, unknownSettlementError("Drive restore", err)
	}
	if !receipt.ReadbackVerified || receipt.RequestID != req.RequestID || receipt.ObjectID != req.ObjectID ||
		receipt.ParentID != req.OriginalParentID || receipt.State != "restored" {
		return MutationReceipt{}, unknownSettlementError("Drive restore", errors.New("readback failed to prove original parent restoration"))
	}
	return receipt, nil
}

func validateQuarantineRequest(req QuarantineRequest) error {
	for name, value := range map[string]string{
		"account": req.AccountID, "root": req.RootID, "namespace": req.Namespace,
		"object_id": req.ObjectID, "parent_id": req.ParentID,
		"quarantine_parent_id": req.QuarantineParentID, "etag": req.ExpectedETag,
		"version": req.Version, "generation": req.Generation, "hash": req.Hash,
		"request_id": req.RequestID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Drive quarantine %s is required", name)
		}
	}
	if req.ParentID == req.QuarantineParentID {
		return errors.New("Drive quarantine parent cannot match current parent")
	}
	if req.Size < 0 {
		return errors.New("Drive quarantine size must not be negative")
	}
	return nil
}

func validateRestoreRequest(req RestoreRequest) error {
	for name, value := range map[string]string{
		"account": req.AccountID, "root": req.RootID, "namespace": req.Namespace,
		"object_id": req.ObjectID, "current_parent_id": req.CurrentParentID,
		"original_parent_id": req.OriginalParentID, "etag": req.ExpectedETag,
		"request_id": req.RequestID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Drive restore %s is required", name)
		}
	}
	if req.CurrentParentID == req.OriginalParentID {
		return errors.New("Drive restore original parent cannot match current parent")
	}
	return nil
}

func unknownSettlementError(operation string, cause error) error {
	if cause == nil {
		cause = errors.New("mutation settlement could not be verified")
	}
	return fmt.Errorf("%s outcome unknown: %w", operation, errors.Join(ErrUnknownSettlement, cause))
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
