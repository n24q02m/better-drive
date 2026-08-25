package driveapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

func driveMutationFixture() (MutationRequest, MutationCapability) {
	request := MutationRequest{
		ObjectID: "object-1", AccountID: "account-1", RootID: "root-1", Namespace: "backup",
		Mode: cleanup.ModeQuarantine, ExpectedETag: "etag-1", Version: "version-1", Generation: "generation-1",
		Size: 12, Hash: "hash-1", ParentID: "parent-1", RequestID: "request-1",
		NoCASRiskAccepted: true, NoCASClassification: NoCASAccepted, OwnerRiskApproved: true,
	}
	capability := NewMutationCapability(request, time.Unix(200, 0).UTC(), "signed-drive-capability")
	request.Capability = capability
	return request, capability
}

func TestMutationRequiresExactTypedCapabilityAndIdentityReadback(t *testing.T) {
	provider := &identityProvider{}
	client := NewClient(provider)
	request, capability := driveMutationFixture()
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	request.Capability = MutationCapability{}
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("missing typed capability error = %v", err)
	}
	request.Capability = capability
	result, err := client.Mutate(context.Background(), request)
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if result.RequestID != request.RequestID || result.Generation != request.Generation || provider.calls != 1 {
		t.Fatalf("result = %+v calls=%d", result, provider.calls)
	}
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay error = %v", err)
	}
}
func TestMutationRejectsScopeDriftAndExactReadbackMismatch(t *testing.T) {
	provider := &identityProvider{drift: true}
	client := NewClient(provider)
	request, capability := driveMutationFixture()
	request.Capability = capability
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "readback") || !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("readback mismatch error = %v, want unknown settlement", err)
	}
	provider.drift = false
	request.Namespace = "other"
	request.RequestID = "request-2"
	request.Capability = capability
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("scope mismatch error = %v", err)
	}
}

func TestMutationProviderErrorAndPostCallCancellationAreUnknown(t *testing.T) {
	provider := &identityProvider{err: errors.New("provider timeout")}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	request, capability := driveMutationFixture()
	request.Capability = capability
	if _, err := client.Mutate(context.Background(), request); err == nil || !errors.Is(err, ErrUnknownSettlement) || !strings.Contains(err.Error(), "provider timeout") {
		t.Fatalf("provider mutation error = %v, want preserved unknown settlement", err)
	}

	request, capability = driveMutationFixture()
	request.RequestID = "request-cancel"
	capability = NewMutationCapability(request, time.Unix(200, 0).UTC(), "signed-drive-capability")
	request.Capability = capability
	ctx, cancel := context.WithCancel(context.Background())
	provider.err = nil
	provider.cancel = cancel
	if _, err := client.Mutate(ctx, request); err == nil || !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-provider cancellation = %v, want unknown cancellation", err)
	}
}

func TestMutationCapabilityJSONOmitsSignature(t *testing.T) {
	request, capability := driveMutationFixture()
	request.Capability = capability
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), capability.Signature) {
		t.Fatalf("serialized mutation leaked signature: %s", data)
	}
}

type identityProvider struct {
	calls  int
	drift  bool
	err    error
	cancel context.CancelFunc
}

func (provider *identityProvider) List(context.Context, string, string, string) (Page, error) {
	return Page{}, nil
}

func (provider *identityProvider) Mutate(_ context.Context, request MutationRequest) (MutationResult, error) {
	provider.calls++
	if provider.cancel != nil {
		provider.cancel()
		provider.cancel = nil
	}
	if provider.err != nil {
		return MutationResult{}, provider.err
	}
	result := MutationResult{
		ProviderID: request.ObjectID, State: "quarantined", AccountID: request.AccountID, RootID: request.RootID,
		Namespace: request.Namespace, ObjectID: request.ObjectID, Version: request.Version, Generation: request.Generation,
		Size: request.Size, Hash: request.Hash, ParentID: request.ParentID, ETag: request.ExpectedETag, RequestID: request.RequestID,
	}
	if provider.drift {
		result.Generation = "other-generation"
	}
	return result, nil
}
