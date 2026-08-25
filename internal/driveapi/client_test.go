package driveapi

import (
	"context"
	"github.com/n24q02m/better-drive/internal/cleanup"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	listCalls     int
	mutationCalls int
}

func (f *fakeProvider) List(_ context.Context, _, _, _ string) (Page, error) {
	f.listCalls++
	return Page{Cursor: "cursor-1", Complete: true, Objects: []cleanup.Object{{ID: "object-1"}}}, nil
}

func (f *fakeProvider) Mutate(_ context.Context, request MutationRequest) (MutationResult, error) {
	f.mutationCalls++
	return MutationResult{ProviderID: request.ObjectID, ObjectID: request.ObjectID, AccountID: request.AccountID, RootID: request.RootID, Namespace: request.Namespace, ParentID: request.ParentID, ETag: request.ExpectedETag, Version: request.Version, Generation: request.Generation, Size: request.Size, Hash: request.Hash, RequestID: request.RequestID, State: "quarantined"}, nil
}

func TestClientListsReadOnlyPages(t *testing.T) {
	provider := &fakeProvider{}
	client := NewClient(provider)
	page, err := client.List(context.Background(), "account-1", "root-1", "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !page.Complete || provider.listCalls != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	if provider.mutationCalls != 0 {
		t.Fatal("List unexpectedly mutated provider")
	}
}

func TestClientMutationRequiresCapabilityAndPerformsOneCall(t *testing.T) {
	provider := &fakeProvider{}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	request, capability := driveMutationFixture()
	request.Capability = MutationCapability{}
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("expected typed-capability rejection, got %v", err)
	}
	request.Capability = capability
	if _, err := client.Mutate(context.Background(), request); err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if provider.mutationCalls != 1 {
		t.Fatalf("mutation calls = %d, want 1", provider.mutationCalls)
	}
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("expected request replay rejection, got %v", err)
	}
}
