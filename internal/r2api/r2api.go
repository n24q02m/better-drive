// Package r2api defines a bounded, provider-neutral object API. It does not
// know how credentials are acquired and it never carries bearer material.
package r2api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrUnknownSettlement marks an R2 mutation whose provider settlement cannot
// be proven after the provider call. Callers must retain the request fence and
// must not retry it automatically.
var ErrUnknownSettlement = errors.New("unknown R2 mutation settlement")

// ObjectIdentity is the exact provider identity used for readback and
// mutation. VersionID and ETag are part of the identity, not display hints.
type ObjectIdentity struct {
	AccountID string `json:"account_id"`
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	ETag      string `json:"etag"`
}

// Object is metadata returned by list/head. It intentionally contains no
// object body and no credential material.
type Object struct {
	Identity ObjectIdentity `json:"identity"`
	Size     int64          `json:"size"`
	SHA256   string         `json:"sha256"`
}

type ListRequest struct {
	AccountID string `json:"account_id"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type Page struct {
	Cursor   string   `json:"cursor"`
	Next     string   `json:"next_cursor,omitempty"`
	Complete bool     `json:"complete"`
	Objects  []Object `json:"objects"`
}

type Limits struct {
	MaxPages   int   `json:"max_pages"`
	MaxObjects int   `json:"max_objects"`
	MaxBytes   int64 `json:"max_bytes"`
}

func (limits Limits) Validate() error {
	if limits.MaxPages <= 0 {
		return errors.New("R2 page limit must be positive")
	}
	if limits.MaxObjects <= 0 {
		return errors.New("R2 object limit must be positive")
	}
	if limits.MaxBytes <= 0 {
		return errors.New("R2 byte limit must be positive")
	}
	return nil
}

type Inventory struct {
	Pages       []Page   `json:"pages"`
	Objects     []Object `json:"objects"`
	PageCount   int      `json:"page_count"`
	ObjectCount int      `json:"object_count"`
	ByteCount   int64    `json:"byte_count"`
}

// Provider is deliberately provider-neutral. Implementations perform exactly
// one bounded page, one metadata head, or one requested mutation.
type Provider interface {
	List(context.Context, ListRequest) (Page, error)
	Head(context.Context, ObjectIdentity) (Object, error)
	Copy(context.Context, CopyRequest) (CopyReceipt, error)
	Delete(context.Context, DeleteRequest) (MutationReceipt, error)
	Purge(context.Context, PurgeRequest) (MutationReceipt, error)
}

type Client struct {
	provider Provider
	Now      func() time.Time
	mu       sync.Mutex
	used     map[string]struct{}
}

func NewClient(provider Provider, defaults ...Limits) *Client {
	return &Client{provider: provider, Now: time.Now, used: make(map[string]struct{})}
}

func (client *Client) List(ctx context.Context, request ListRequest, limits ...Limits) (Inventory, error) {
	if err := client.ready(ctx); err != nil {
		return Inventory{}, err
	}
	if err := validateListRequest(request); err != nil {
		return Inventory{}, err
	}
	bound := Limits{MaxPages: 100, MaxObjects: 10000, MaxBytes: 1 << 30}
	if len(limits) > 0 {
		bound = limits[0]
	}
	if err := bound.Validate(); err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{}
	cursor := request.Cursor
	for pageNumber := 1; pageNumber <= bound.MaxPages; pageNumber++ {
		if err := contextErr(ctx); err != nil {
			return Inventory{}, err
		}
		pageRequest := request
		pageRequest.Cursor = cursor
		page, err := client.provider.List(ctx, pageRequest)
		if err != nil {
			return Inventory{}, fmt.Errorf("R2 list page %d: %w", pageNumber, err)
		}
		if err := contextErr(ctx); err != nil {
			return Inventory{}, err
		}
		if strings.TrimSpace(page.Cursor) == "" {
			return Inventory{}, fmt.Errorf("R2 list page %d has no cursor", pageNumber)
		}
		if pageNumber > 1 && page.Cursor == inventory.Pages[len(inventory.Pages)-1].Cursor {
			return Inventory{}, fmt.Errorf("R2 list page %d cursor did not advance", pageNumber)
		}
		for _, object := range page.Objects {
			if err := object.Validate(true); err != nil {
				return Inventory{}, fmt.Errorf("R2 list page %d object: %w", pageNumber, err)
			}
			if inventory.ObjectCount >= bound.MaxObjects {
				return Inventory{}, fmt.Errorf("R2 object limit %d exceeded", bound.MaxObjects)
			}
			if object.Size > bound.MaxBytes-inventory.ByteCount {
				return Inventory{}, fmt.Errorf("R2 byte limit %d exceeded", bound.MaxBytes)
			}
			inventory.Objects = append(inventory.Objects, object)
			inventory.ObjectCount++
			inventory.ByteCount += object.Size
		}
		inventory.Pages = append(inventory.Pages, page)
		inventory.PageCount++
		if page.Complete {
			if page.Next != "" {
				return Inventory{}, fmt.Errorf("R2 complete page %d has a next cursor", pageNumber)
			}
			return inventory, nil
		}
		if strings.TrimSpace(page.Next) == "" {
			return Inventory{}, fmt.Errorf("R2 list page %d is incomplete without a next cursor", pageNumber)
		}
		if page.Next == page.Cursor {
			return Inventory{}, fmt.Errorf("R2 list page %d next cursor did not advance", pageNumber)
		}
		cursor = page.Next
	}
	return Inventory{}, fmt.Errorf("R2 page limit %d reached before completion", bound.MaxPages)
}

func (client *Client) Head(ctx context.Context, identity ObjectIdentity) (Object, error) {
	if err := client.ready(ctx); err != nil {
		return Object{}, err
	}
	if err := identity.Validate(); err != nil {
		return Object{}, err
	}
	object, err := client.provider.Head(ctx, identity)
	if err != nil {
		return Object{}, fmt.Errorf("R2 head failed: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return Object{}, err
	}
	if err := object.Validate(true); err != nil {
		return Object{}, fmt.Errorf("R2 head readback: %w", err)
	}
	if !sameIdentity(identity, object.Identity) {
		return Object{}, errors.New("R2 head readback identity, version, or ETag drifted")
	}
	return object, nil
}

type CopyRequest struct {
	Source         ObjectIdentity `json:"source"`
	Destination    ObjectIdentity `json:"destination"`
	ExpectedSize   int64          `json:"expected_size"`
	ExpectedSHA256 string         `json:"expected_sha256"`
	RequestID      string         `json:"request_id"`
}

type copyProof struct{}

type CopyReceipt struct {
	Source           ObjectIdentity `json:"source"`
	Destination      ObjectIdentity `json:"destination"`
	Size             int64          `json:"size"`
	SHA256           string         `json:"sha256"`
	ReadbackVerified bool           `json:"readback_verified"`
	RequestID        string         `json:"request_id,omitempty"`
	verifiedProof    *copyProof
}

func (client *Client) Copy(ctx context.Context, request CopyRequest, capability CopyCapability) (CopyReceipt, error) {
	if err := client.ready(ctx); err != nil {
		return CopyReceipt{}, err
	}
	if err := validateCopyRequest(request); err != nil {
		return CopyReceipt{}, err
	}
	if err := capability.validate(request, client.now()); err != nil {
		return CopyReceipt{}, err
	}
	if _, err := client.Head(ctx, request.Source); err != nil {
		return CopyReceipt{}, err
	}
	receipt, err := client.provider.Copy(ctx, request)
	if err != nil {
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", err)
	}
	if err := contextErr(ctx); err != nil {
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", err)
	}
	if receipt.RequestID != request.RequestID || receipt.Source != request.Source || !sameCreateScope(request.Destination, receipt.Destination) || receipt.Size != request.ExpectedSize || receipt.SHA256 != request.ExpectedSHA256 {
		readbackErr := errors.New("R2 quarantine copy readback does not match exact request ID, source, destination, size, or checksum")
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", readbackErr)
	}
	if receipt.Destination.VersionID == "" || receipt.Destination.ETag == "" {
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", errors.New("R2 quarantine copy readback lacks exact version or ETag"))
	}
	readback, err := client.Head(ctx, receipt.Destination)
	if err != nil {
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", err)
	}
	if readback.Size != request.ExpectedSize || readback.SHA256 != request.ExpectedSHA256 {
		return CopyReceipt{}, unknownSettlementError("R2 quarantine copy", errors.New("R2 quarantine copy checksum readback mismatch"))
	}
	receipt.Destination = readback.Identity
	receipt.Size = readback.Size
	receipt.SHA256 = readback.SHA256
	receipt.ReadbackVerified = true
	receipt.verifiedProof = &copyProof{}
	return receipt, nil
}

type DeleteRequest struct {
	Source        ObjectIdentity `json:"source"`
	Quarantine    ObjectIdentity `json:"quarantine"`
	Copy          CopyReceipt    `json:"copy"`
	CopyRequestID string         `json:"copy_request_id"`
	RequestID     string         `json:"request_id"`
}

type PurgeRequest struct {
	Object    ObjectIdentity `json:"object"`
	RequestID string         `json:"request_id"`
	Lifecycle string         `json:"lifecycle"`
}

type MutationReceipt struct {
	Identity  ObjectIdentity `json:"identity"`
	State     string         `json:"state"`
	RequestID string         `json:"request_id"`
}

func (client *Client) Delete(ctx context.Context, request DeleteRequest, capability DeleteCapability) (MutationReceipt, error) {
	if err := client.ready(ctx); err != nil {
		return MutationReceipt{}, err
	}
	if err := validateDeleteRequest(request); err != nil {
		return MutationReceipt{}, err
	}
	if err := capability.validate(request, client.now()); err != nil {
		return MutationReceipt{}, err
	}
	receipt, err := client.provider.Delete(ctx, request)
	if err != nil {
		return MutationReceipt{}, unknownSettlementError("R2 source delete", err)
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, unknownSettlementError("R2 source delete", err)
	}
	if receipt.Identity != request.Source || receipt.RequestID != request.RequestID || receipt.State != "deleted" {
		return MutationReceipt{}, unknownSettlementError("R2 source delete", errors.New("R2 source delete readback does not match exact request"))
	}
	return receipt, nil
}

func (client *Client) Purge(ctx context.Context, request PurgeRequest, capability PurgeCapability) (MutationReceipt, error) {
	if err := client.ready(ctx); err != nil {
		return MutationReceipt{}, err
	}
	if err := validatePurgeRequest(request); err != nil {
		return MutationReceipt{}, err
	}
	if err := capability.validate(request, client.now()); err != nil {
		return MutationReceipt{}, err
	}
	receipt, err := client.provider.Purge(ctx, request)
	if err != nil {
		return MutationReceipt{}, unknownSettlementError("R2 purge", err)
	}
	if err := contextErr(ctx); err != nil {
		return MutationReceipt{}, unknownSettlementError("R2 purge", err)
	}
	if receipt.Identity != request.Object || receipt.RequestID != request.RequestID || receipt.State != "purged" {
		return MutationReceipt{}, unknownSettlementError("R2 purge", errors.New("R2 purge readback does not match exact request"))
	}
	return receipt, nil
}

type CopyCapability struct {
	Source      ObjectIdentity `json:"source"`
	Destination ObjectIdentity `json:"destination"`
	RequestID   string         `json:"request_id"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Signature   string         `json:"-"`
}

func NewCopyCapability(source, destination ObjectIdentity, requestID string, expiresAt time.Time, signature string) CopyCapability {
	return CopyCapability{Source: source, Destination: destination, RequestID: requestID, ExpiresAt: expiresAt, Signature: signature}
}

func (capability CopyCapability) validate(request CopyRequest, now time.Time) error {
	if capability.Signature == "" || capability.RequestID == "" || capability.RequestID != request.RequestID {
		return errors.New("R2 copy capability is required")
	}
	if !capability.ExpiresAt.After(now) {
		return errors.New("R2 copy capability is expired")
	}
	if capability.Source != request.Source || !sameCreateScope(capability.Destination, request.Destination) {
		return errors.New("R2 copy capability scope drifted")
	}
	return nil
}

// Validate checks an exact copy capability without executing a provider call.
func (capability CopyCapability) Validate(request CopyRequest, now time.Time) error {
	return capability.validate(request, now.UTC())
}

type DeleteCapability struct {
	Source     ObjectIdentity `json:"source"`
	Quarantine ObjectIdentity `json:"quarantine"`
	RequestID  string         `json:"request_id"`
	ExpiresAt  time.Time      `json:"expires_at"`
	Signature  string         `json:"-"`
}

func NewDeleteCapability(source, quarantine ObjectIdentity, requestID string, expiresAt time.Time, signature string) DeleteCapability {
	return DeleteCapability{Source: source, Quarantine: quarantine, RequestID: requestID, ExpiresAt: expiresAt, Signature: signature}
}

func (capability DeleteCapability) validate(request DeleteRequest, now time.Time) error {
	if capability.Signature == "" || capability.RequestID == "" || capability.RequestID != request.RequestID {
		return errors.New("R2 delete capability is required")
	}
	if !capability.ExpiresAt.After(now) {
		return errors.New("R2 delete capability is expired")
	}
	if capability.Source != request.Source || capability.Quarantine != request.Quarantine {
		return errors.New("R2 delete capability scope drifted")
	}
	return nil
}

// Validate checks an exact source-delete capability without executing a provider call.
func (capability DeleteCapability) Validate(request DeleteRequest, now time.Time) error {
	return capability.validate(request, now.UTC())
}

type PurgeCapability struct {
	Object    ObjectIdentity `json:"object"`
	RequestID string         `json:"request_id"`
	ExpiresAt time.Time      `json:"expires_at"`
	Signature string         `json:"-"`
}

func NewPurgeCapability(object ObjectIdentity, requestID string, expiresAt time.Time, signature string) PurgeCapability {
	return PurgeCapability{Object: object, RequestID: requestID, ExpiresAt: expiresAt, Signature: signature}
}

func (capability PurgeCapability) validate(request PurgeRequest, now time.Time) error {
	if capability.Signature == "" || capability.RequestID == "" || capability.RequestID != request.RequestID {
		return errors.New("R2 purge capability is required")
	}
	if !capability.ExpiresAt.After(now) {
		return errors.New("R2 purge capability is expired")
	}
	if capability.Object != request.Object {
		return errors.New("R2 purge capability scope drifted")
	}
	return nil
}

// Validate checks an exact purge capability without executing a provider call.
func (capability PurgeCapability) Validate(request PurgeRequest, now time.Time) error {
	return capability.validate(request, now.UTC())
}

func (client *Client) ready(ctx context.Context) error {
	if client == nil || client.provider == nil {
		return errors.New("R2 provider is not configured")
	}
	return contextErr(ctx)
}

func (client *Client) now() time.Time {
	if client != nil && client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}

func validateListRequest(request ListRequest) error {
	for name, value := range map[string]string{"account": request.AccountID, "bucket": request.Bucket} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("R2 %s is required", name)
		}
	}
	return nil
}

func validateCopyRequest(request CopyRequest) error {
	if err := request.Source.Validate(); err != nil {
		return fmt.Errorf("R2 copy source: %w", err)
	}
	if err := request.Destination.ValidateCreate(); err != nil {
		return fmt.Errorf("R2 copy destination: %w", err)
	}
	if request.Source.AccountID != request.Destination.AccountID || (request.Source.Bucket == request.Destination.Bucket && request.Source.Key == request.Destination.Key) {
		return errors.New("R2 copy source and destination scope is invalid")
	}
	if request.RequestID == "" || request.ExpectedSHA256 == "" || request.ExpectedSize < 0 {
		return errors.New("R2 copy request requires request ID, checksum, and non-negative size")
	}
	return nil
}

func validateDeleteRequest(request DeleteRequest) error {
	if err := request.Source.Validate(); err != nil {
		return fmt.Errorf("R2 delete source: %w", err)
	}
	if err := request.Quarantine.Validate(); err != nil {
		return fmt.Errorf("R2 delete quarantine: %w", err)
	}
	copyReceipt := request.Copy
	if request.RequestID == "" || request.CopyRequestID == "" || copyReceipt.RequestID != request.CopyRequestID ||
		!copyReceipt.ReadbackVerified || copyReceipt.verifiedProof == nil ||
		copyReceipt.Source != request.Source || copyReceipt.Destination != request.Quarantine ||
		copyReceipt.Size < 0 || copyReceipt.SHA256 == "" {
		return errors.New("R2 delete requires exact verified quarantine copy")
	}
	return nil
}

func validatePurgeRequest(request PurgeRequest) error {
	if err := request.Object.Validate(); err != nil {
		return fmt.Errorf("R2 purge object: %w", err)
	}
	if request.RequestID == "" {
		return errors.New("R2 purge request ID is required")
	}
	if request.Lifecycle != "" {
		return errors.New("R2 lifecycle must be empty before explicit purge")
	}
	return nil
}

func (object Object) Validate(requireVersion bool) error {
	if err := object.Identity.Validate(); err != nil {
		return err
	}
	if !requireVersion && object.Identity.VersionID == "" && object.Identity.ETag == "" {
		return nil
	}
	if object.Size < 0 {
		return errors.New("R2 object size must not be negative")
	}
	if object.SHA256 == "" {
		return errors.New("R2 object checksum is required")
	}
	return nil
}

func (identity ObjectIdentity) Validate() error {
	for name, value := range map[string]string{"account": identity.AccountID, "bucket": identity.Bucket, "key": identity.Key, "version": identity.VersionID, "ETag": identity.ETag} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("R2 object %s is required", name)
		}
		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("R2 object %s is invalid", name)
		}
	}
	return nil
}

func (identity ObjectIdentity) ValidateCreate() error {
	for name, value := range map[string]string{"account": identity.AccountID, "bucket": identity.Bucket, "key": identity.Key} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("R2 object %s is required", name)
		}
	}
	return nil
}

func sameIdentity(left, right ObjectIdentity) bool { return left == right }

func sameCreateScope(expected, actual ObjectIdentity) bool {
	if expected.AccountID != actual.AccountID || expected.Bucket != actual.Bucket || expected.Key != actual.Key {
		return false
	}
	if expected.VersionID != "" && expected.VersionID != actual.VersionID {
		return false
	}
	if expected.ETag != "" && expected.ETag != actual.ETag {
		return false
	}
	return true
}

func identityKey(identity ObjectIdentity) string {
	return strings.Join([]string{identity.AccountID, identity.Bucket, identity.Key, identity.VersionID, identity.ETag}, "\x00")
}

func (identity ObjectIdentity) String() string { return identityKey(identity) }

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
