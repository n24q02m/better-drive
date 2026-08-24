// Package credentials keeps provider bindings separate from secret material.
//
// A Reference and Binding are safe to put in configuration, logs, and evidence.
// Secret bytes are owned only by Source and are never part of a Binding.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Reference identifies a credential without carrying its secret.
type Reference struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Root     string `json:"root"`
	Bucket   string `json:"bucket"`
	Endpoint string `json:"endpoint"`

	// AccountID and RootID are compatibility spellings for callers that use
	// the Drive object vocabulary. They are not serialized and must agree with
	// Account and Root when both are supplied.
	AccountID string `json:"-"`
	RootID    string `json:"-"`
}

// CredentialReference is the descriptive name used by callers that prefer
// the fully-qualified type name.
type CredentialReference = Reference

// Ref is a concise alias for Reference.
type Ref = Reference

// Binding is the non-secret result of resolving a Reference.
type Binding struct {
	Provider         string    `json:"provider"`
	Account          string    `json:"account"`
	Root             string    `json:"root"`
	Bucket           string    `json:"bucket"`
	Endpoint         string    `json:"endpoint"`
	SessionExpiresAt time.Time `json:"session_expires_at"`
	AccountID        string    `json:"-"`
	RootID           string    `json:"-"`
}

// Resolved is an alias that makes it explicit that a Binding is resolved
// metadata only; it never contains a token or secret.
type Resolved = Binding

// Source resolves public binding metadata and is the only boundary through
// which a caller can retrieve secret bytes. Implementations MUST keep secrets
// in memory or an equivalent protected store and MUST NOT return them from
// Resolve.
type Source interface {
	Resolve(context.Context, Reference) (Binding, error)
	Secret(context.Context, Reference) ([]byte, error)
}

// SecretSource is an alternate name for Source used by integration code.
type SecretSource = Source

// Resolver validates source output and applies an explicit clock for expiry
// checks. The clock is injectable solely for deterministic tests.
type Resolver struct {
	Source Source
	Now    func() time.Time
}

func NewResolver(source Source) *Resolver {
	return &Resolver{Source: source, Now: time.Now}
}

func (resolver *Resolver) Resolve(ctx context.Context, ref Reference) (Binding, error) {
	if resolver == nil || resolver.Source == nil {
		return Binding{}, errors.New("credential source is not configured")
	}
	if err := ref.Validate(); err != nil {
		return Binding{}, fmt.Errorf("invalid credential reference: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return Binding{}, err
	}
	binding, err := resolver.Source.Resolve(ctx, normalizeReference(ref))
	if err != nil {
		return Binding{}, sanitizeError("credential resolution failed", err)
	}
	now := time.Now().UTC()
	if resolver.Now != nil {
		now = resolver.Now().UTC()
	}
	if err := binding.Validate(now); err != nil {
		return Binding{}, fmt.Errorf("credential resolution failed: %w", err)
	}
	if !sameBinding(ref, binding) {
		return Binding{}, errors.New("credential resolution failed: source binding does not match reference")
	}
	return normalizeBinding(binding), nil
}

// ResolveSecret is deliberately separate from Resolve. Its return value is
// transient secret material and must not be embedded in evidence or metadata.
func (resolver *Resolver) ResolveSecret(ctx context.Context, ref Reference) ([]byte, error) {
	if resolver == nil || resolver.Source == nil {
		return nil, errors.New("credential source is not configured")
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("invalid credential reference: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	binding, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	secret, err := resolver.Source.Secret(ctx, normalizeReference(ref))
	if err != nil {
		return nil, sanitizeError("credential secret retrieval failed", err)
	}
	if len(secret) == 0 {
		return nil, errors.New("credential secret retrieval failed: secret is empty")
	}
	// Keep the binding variable used as a deliberate proof that expiry and
	// scope are checked before secret retrieval.
	_ = binding
	return append([]byte(nil), secret...), nil
}

func (ref Reference) Validate() error {
	if err := validateCompatibilityAliases(ref.Account, ref.AccountID, ref.Root, ref.RootID); err != nil {
		return err
	}
	ref = normalizeReference(ref)
	for name, value := range map[string]string{
		"provider": ref.Provider,
		"account":  ref.Account,
		"root":     ref.Root,
		"bucket":   ref.Bucket,
		"endpoint": ref.Endpoint,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if !identifier(ref.Provider) || !identifier(ref.Account) || !identifier(ref.Root) || !identifier(ref.Bucket) {
		return errors.New("credential binding identifiers are invalid")
	}
	parsed, err := url.Parse(ref.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("endpoint must be an HTTPS URL without userinfo, query, or fragment")
	}
	return nil
}

func (binding Binding) Validate(now time.Time) error {
	ref := Reference{Provider: binding.Provider, Account: binding.Account, Root: binding.Root, Bucket: binding.Bucket, Endpoint: binding.Endpoint, AccountID: binding.AccountID, RootID: binding.RootID}
	if err := ref.Validate(); err != nil {
		return err
	}
	if binding.SessionExpiresAt.IsZero() {
		return errors.New("session expiry is required")
	}
	if !binding.SessionExpiresAt.After(now.UTC()) {
		return errors.New("credential session is expired")
	}
	return nil
}

func (ref Reference) String() string {
	ref = normalizeReference(ref)
	return strings.Join([]string{ref.Provider, ref.Account, ref.Root, ref.Bucket}, "/")
}

func (binding Binding) String() string {
	return Reference{Provider: binding.Provider, Account: binding.Account, Root: binding.Root, Bucket: binding.Bucket}.String()
}

// MemorySource is a deterministic in-memory Source for local execution and
// tests. The record map and secret bytes are intentionally unexported.
type MemorySource struct {
	mu      sync.RWMutex
	records map[string]memoryRecord
}

type memoryRecord struct {
	binding Binding
	secret  []byte
}

// InMemorySource is an alias for MemorySource.
type InMemorySource = MemorySource

func NewMemorySource() *MemorySource {
	return &MemorySource{records: make(map[string]memoryRecord)}
}

func (source *MemorySource) Put(ref Reference, binding Binding, secret []byte) error {
	if source == nil {
		return errors.New("credential source is not configured")
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("invalid credential reference: %w", err)
	}
	if err := binding.Validate(time.Unix(0, 0).UTC()); err != nil {
		return fmt.Errorf("invalid credential binding: %w", err)
	}
	if !sameBinding(ref, binding) {
		return errors.New("credential binding does not match reference")
	}
	if len(secret) == 0 {
		return errors.New("credential secret is empty")
	}
	if source.records == nil {
		source.records = make(map[string]memoryRecord)
	}
	key := normalizeReference(ref).String()
	source.mu.Lock()
	source.records[key] = memoryRecord{binding: normalizeBinding(binding), secret: append([]byte(nil), secret...)}
	source.mu.Unlock()
	return nil
}

func (source *MemorySource) Resolve(ctx context.Context, ref Reference) (Binding, error) {
	if source == nil {
		return Binding{}, errors.New("credential source is not configured")
	}
	if err := contextErr(ctx); err != nil {
		return Binding{}, err
	}
	if err := ref.Validate(); err != nil {
		return Binding{}, fmt.Errorf("invalid credential reference: %w", err)
	}
	key := normalizeReference(ref).String()
	source.mu.RLock()
	record, ok := source.records[key]
	source.mu.RUnlock()
	if !ok {
		return Binding{}, errors.New("credential binding not found")
	}
	return record.binding, nil
}

// Binding is provided as a convenience for callers that use a metadata-only
// source vocabulary; secret retrieval remains available only through Secret.
func (source *MemorySource) Binding(ctx context.Context, ref Reference) (Binding, error) {
	return source.Resolve(ctx, ref)
}

func (source *MemorySource) Secret(ctx context.Context, ref Reference) ([]byte, error) {
	if source == nil {
		return nil, errors.New("credential source is not configured")
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("invalid credential reference: %w", err)
	}
	key := normalizeReference(ref).String()
	source.mu.RLock()
	record, ok := source.records[key]
	secret := append([]byte(nil), record.secret...)
	source.mu.RUnlock()
	if !ok {
		return nil, errors.New("credential secret not found")
	}
	return secret, nil
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
func validateCompatibilityAliases(account, accountID, root, rootID string) error {
	if account != "" && accountID != "" && account != accountID {
		return errors.New("account and account_id must match")
	}
	if root != "" && rootID != "" && root != rootID {
		return errors.New("root and root_id must match")
	}
	return nil
}

func normalizeReference(ref Reference) Reference {
	if ref.Account == "" {
		ref.Account = ref.AccountID
	}
	if ref.Root == "" {
		ref.Root = ref.RootID
	}
	return ref
}

func normalizeBinding(binding Binding) Binding {
	if binding.Account == "" {
		binding.Account = binding.AccountID
	}
	if binding.Root == "" {
		binding.Root = binding.RootID
	}
	return binding
}

func sameBinding(ref Reference, binding Binding) bool {
	ref = normalizeReference(ref)
	binding = normalizeBinding(binding)
	return ref.Provider == binding.Provider && ref.Account == binding.Account && ref.Root == binding.Root && ref.Bucket == binding.Bucket && ref.Endpoint == binding.Endpoint
}

func identifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func sanitizeError(prefix string, err error) error {
	if err == nil {
		return errors.New(prefix)
	}
	// Source errors are not trusted to be safe. Do not include their text,
	// because an implementation may accidentally include a token value.
	return errors.New(prefix)
}
