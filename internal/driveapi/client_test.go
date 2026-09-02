package driveapi

import (
	"context"
	"strings"
	"testing"
)

type fakeProvider struct {
	listCalls int
	page      Page
}

func (f *fakeProvider) List(_ context.Context, _, parentID, _ string) (Page, error) {
	f.listCalls++
	if f.page.Cursor == "" {
		f.page = Page{Cursor: "cursor-1", Complete: true, ParentID: parentID}
	}
	return f.page, nil
}

func TestClientListsReadOnlyPages(t *testing.T) {
	provider := &fakeProvider{}
	client := NewClient(provider)
	page, err := client.List(context.Background(), "account-1", "root-1", "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !page.Complete || page.ParentID != "root-1" || provider.listCalls != 1 {
		t.Fatalf("unexpected page: %+v calls=%d", page, provider.listCalls)
	}
}

func TestClientRejectsUnboundProviderParentReadback(t *testing.T) {
	provider := &fakeProvider{page: Page{Cursor: "cursor-1", Complete: true}}
	client := NewClient(provider)
	if _, err := client.List(context.Background(), "account-1", "root-1", ""); err == nil || !strings.Contains(err.Error(), "parent ID") {
		t.Fatalf("missing parent readback error = %v", err)
	}
}

func TestClientRejectsMismatchedProviderParentReadback(t *testing.T) {
	provider := &fakeProvider{page: Page{Cursor: "cursor-1", Complete: true, ParentID: "other-root"}}
	client := NewClient(provider)
	if _, err := client.List(context.Background(), "account-1", "root-1", ""); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched parent readback error = %v", err)
	}
}
