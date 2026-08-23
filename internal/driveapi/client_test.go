package driveapi

import (
	"context"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type fakeProvider struct {
	listCalls     int
	mutationCalls int
}

func (f *fakeProvider) List(_ context.Context, _, _, _ string) (Page, error) {
	f.listCalls++
	return Page{Cursor: "cursor-1", Complete: true, Objects: []cleanup.Object{{ID: "object-1"}}}, nil
}

func (f *fakeProvider) Mutate(_ context.Context, _ MutationRequest) (MutationResult, error) {
	f.mutationCalls++
	return MutationResult{ProviderID: "object-1", State: "quarantined"}, nil
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
	request := MutationRequest{ObjectID: "object-1", Mode: cleanup.ModeQuarantine, AccountID: "account-1", RootID: "root-1", ExpectedETag: "etag-1", NoCASRiskAccepted: true, Capability: "BD-DRIVE-MUTATION-RW"}
	if _, err := client.Mutate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "owner-risk") {
		t.Fatalf("expected owner-risk rejection, got %v", err)
	}
	request.OwnerRiskApproved = true
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
