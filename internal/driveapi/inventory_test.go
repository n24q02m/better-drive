package driveapi

import (
	"context"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type paginatedProvider struct {
	pages map[string]Page
}

func (p *paginatedProvider) List(_ context.Context, _, _, cursor string) (Page, error) {
	return p.pages[cursor], nil
}

func (p *paginatedProvider) Mutate(context.Context, MutationRequest) (MutationResult, error) {
	return MutationResult{}, nil
}

func TestCollectRootRequiresCursorProgressAndCompletesPages(t *testing.T) {
	provider := &paginatedProvider{pages: map[string]Page{
		"":         {Cursor: "cursor-1", Next: "cursor-2", Objects: []cleanup.Object{{ID: "object-1"}}},
		"cursor-2": {Cursor: "cursor-2", Complete: true, Objects: []cleanup.Object{{ID: "object-2"}}},
	}}
	client := NewClient(provider)
	root, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, 2)
	if err != nil {
		t.Fatalf("CollectRoot() error = %v", err)
	}
	if root.ExpectedPages != 2 || len(root.Pages) != 2 || root.Pages[1].Cursor != "cursor-2" {
		t.Fatalf("unexpected root: %+v", root)
	}
}

func TestCollectRootRejectsCursorStallAndPageOverflow(t *testing.T) {
	provider := &paginatedProvider{pages: map[string]Page{
		"": {Cursor: "cursor-1", Next: "cursor-1", Objects: []cleanup.Object{{ID: "object-1"}}},
	}}
	client := NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, 3); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("expected cursor stall rejection, got %v", err)
	}

	provider = &paginatedProvider{pages: map[string]Page{
		"":         {Cursor: "cursor-1", Next: "cursor-2", Objects: []cleanup.Object{{ID: "object-1"}}},
		"cursor-2": {Cursor: "cursor-2", Next: "cursor-3", Objects: []cleanup.Object{{ID: "object-2"}}},
	}}
	client = NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, 1); err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("expected page limit rejection, got %v", err)
	}
}
