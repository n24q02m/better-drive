package driveapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/n24q02m/better-drive/internal/cleanup"
	"strings"
	"testing"
	"time"
)

type paginatedProvider struct {
	pages map[string]Page
}

func (p *paginatedProvider) List(_ context.Context, _, parentID, cursor string) (Page, error) {
	page := p.pages[cursor]
	if page.ParentID == "" {
		page.ParentID = parentID
	}
	return page, nil
}

func TestCollectRootRequiresCursorProgressAndCompletesPages(t *testing.T) {
	provider := &paginatedProvider{pages: map[string]Page{
		"":         {Cursor: "cursor-1", Next: "cursor-2", Objects: []cleanup.Object{nestedObject("object-1", "root-1", false)}},
		"cursor-2": {Cursor: "cursor-2", Complete: true, Objects: []cleanup.Object{nestedObject("object-2", "root-1", false)}},
	}}
	client := NewClient(provider)
	root, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, InventoryLimits{MaxPages: 2, MaxObjects: 8, MaxDepth: 8})
	if err != nil {
		t.Fatalf("CollectRoot() error = %v", err)
	}
	if root.ExpectedPages != 2 || len(root.Pages) != 2 || root.Pages[1].Cursor != "cursor-2" {
		t.Fatalf("unexpected root: %+v", root)
	}
}

func TestCollectRootRejectsCursorStallAndPageOverflow(t *testing.T) {
	provider := &paginatedProvider{pages: map[string]Page{
		"": {Cursor: "cursor-1", Next: "cursor-1", Objects: []cleanup.Object{nestedObject("object-1", "root-1", false)}},
	}}
	client := NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, InventoryLimits{MaxPages: 3, MaxObjects: 8, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("expected cursor stall rejection, got %v", err)
	}

	provider = &paginatedProvider{pages: map[string]Page{
		"":         {Cursor: "cursor-1", Next: "cursor-2", Objects: []cleanup.Object{nestedObject("object-1", "root-1", false)}},
		"cursor-2": {Cursor: "cursor-2", Next: "cursor-3", Objects: []cleanup.Object{nestedObject("object-2", "root-1", false)}},
	}}
	client = NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home"}, InventoryLimits{MaxPages: 1, MaxObjects: 8, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("expected page limit rejection, got %v", err)
	}
}
func TestCollectAllRootsSortsAndRejectsDuplicateScope(t *testing.T) {
	provider := &paginatedProvider{pages: map[string]Page{
		"": {Cursor: "cursor-1", Complete: true},
	}}
	client := NewClient(provider)
	requests := []RootRequest{
		{Provider: "drive", AccountID: "account-1", RootID: "root-2", Namespace: "backup/b"},
		{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/a"},
	}
	rootSet, err := client.CollectAllRoots(context.Background(), "account-1", requests, InventoryLimits{MaxPages: 2, MaxObjects: 8, MaxDepth: 8})
	if err != nil {
		t.Fatalf("CollectAllRoots() error = %v", err)
	}
	if rootSet.SchemaVersion != cleanup.CurrentRootSetSchemaVersion || len(rootSet.Roots) != 2 {
		t.Fatalf("unexpected root set: %+v", rootSet)
	}
	if rootSet.Roots[0].RootID != "root-1" || rootSet.Roots[1].RootID != "root-2" {
		t.Fatalf("root order is not deterministic: %+v", rootSet.Roots)
	}

	requests = append(requests, requests[0])
	if _, err := client.CollectAllRoots(context.Background(), "account-1", requests, InventoryLimits{MaxPages: 2, MaxObjects: 8, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("expected duplicate root rejection, got %v", err)
	}
}

func TestCollectAllRootsRejectsAccountScopeDrift(t *testing.T) {
	client := NewClient(&paginatedProvider{pages: map[string]Page{
		"": {Cursor: "cursor-1", Complete: true},
	}})
	_, err := client.CollectAllRoots(context.Background(), "account-1", []RootRequest{{
		Provider: "drive", AccountID: "account-2", RootID: "root-1", Namespace: "backup/home",
	}}, InventoryLimits{MaxPages: 1, MaxObjects: 8, MaxDepth: 8})
	if err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account scope rejection, got %v", err)
	}
}
func TestCollectAllRootsUsesAggregateBoundsAndPhysicalRootIdentity(t *testing.T) {
	object := nestedObject("file-root", "root-1", false)
	object.Namespace = "backup/one"
	provider := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-1-page", Complete: true,
			Objects: []cleanup.Object{object},
		},
		{parent: "root-2", cursor: ""}: {Cursor: "root-2-page", Complete: true},
	}}
	client := NewClient(provider)
	_, err := client.CollectAllRoots(context.Background(), "account-1", []RootRequest{
		{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/one"},
		{Provider: "drive", AccountID: "account-1", RootID: "root-2", Namespace: "backup/two"},
	}, InventoryLimits{MaxPages: 4, MaxObjects: 1, MaxDepth: 8})
	if err == nil || !strings.Contains(err.Error(), "object limit") {
		t.Fatalf("aggregate object bound error = %v, want rejection", err)
	}
	provider = &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {Cursor: "root-1-page", Complete: true},
		{parent: "root-2", cursor: ""}: {Cursor: "root-2-page", Complete: true},
	}}
	client = NewClient(provider)
	_, err = client.CollectAllRoots(context.Background(), "account-1", []RootRequest{
		{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/one"},
		{Provider: "drive", AccountID: "account-1", RootID: "root-2", Namespace: "backup/two"},
	}, InventoryLimits{MaxPages: 1, MaxObjects: 4, MaxDepth: 8})
	if err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("aggregate page bound error = %v, want rejection", err)
	}

	provider = &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {Cursor: "root-page", Complete: true},
	}}
	client = NewClient(provider)
	_, err = client.CollectAllRoots(context.Background(), "account-1", []RootRequest{
		{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/one"},
		{Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/alias"},
	}, InventoryLimits{MaxPages: 4, MaxObjects: 4, MaxDepth: 8})
	if err == nil || !strings.Contains(err.Error(), "overlapping physical root") {
		t.Fatalf("namespace alias error = %v, want physical-root rejection", err)
	}
}

func TestCollectRootRejectsDepthOverflowAndPathDrift(t *testing.T) {
	provider := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page", Complete: true,
			Objects: []cleanup.Object{nestedObject("folder-a", "root-1", true)},
		},
		{parent: "folder-a", cursor: ""}: {
			Cursor: "folder-page", Complete: true,
			Objects: []cleanup.Object{nestedObject("file-nested", "folder-a", false)},
		},
	}}
	client := NewClient(provider)
	_, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 4, MaxObjects: 4, MaxDepth: 1})
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("depth bound error = %v, want rejection", err)
	}

	provider = &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page", Complete: true,
			Objects: []cleanup.Object{nestedObject("file-root", "root-1", false)},
		},
	}}
	object := nestedObject("file-root", "root-1", false)
	object.Path = "wrong/path"
	provider.pages[nestedPageKey{parent: "root-1", cursor: ""}] = Page{
		Cursor: "root-page", Complete: true, Objects: []cleanup.Object{object},
	}
	client = NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 2, MaxObjects: 2, MaxDepth: 2}); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("path drift error = %v, want rejection", err)
	}
}

type nestedPageKey struct {
	parent string
	cursor string
}

type nestedPaginatedProvider struct {
	pages map[nestedPageKey]Page
	calls []nestedPageKey
}

func (p *nestedPaginatedProvider) List(_ context.Context, _, parentID, cursor string) (Page, error) {
	key := nestedPageKey{parent: parentID, cursor: cursor}
	p.calls = append(p.calls, key)
	page, ok := p.pages[key]
	if !ok {
		return Page{}, fmt.Errorf("missing page for %s/%s", parentID, cursor)
	}
	if page.ParentID == "" {
		page.ParentID = parentID
	}
	return page, nil
}

func nestedObject(id, parent string, folder bool) cleanup.Object {
	objectType := cleanup.ObjectTypeFile
	if folder {
		objectType = cleanup.ObjectTypeFolder
	}
	depth := 1
	if parent != "root-1" {
		depth = 2
	}
	path := id
	if parent != "root-1" {
		path = parent + "/" + id
	}
	object := cleanup.Object{
		ID: id, ParentID: parent, Name: id, Path: path, ObjectType: objectType,
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
		Version: "v1", Generation: "generation-" + id, ETag: "etag-" + id,
		ModifiedAt: time.Unix(100, 0).UTC(), Depth: depth, Class: cleanup.ClassUnknown,
	}
	if !folder {
		object.ContentHash = "hash-" + id
		object.Size = 4
	}
	return object
}

func TestCollectRootRecursivelyTraversesNestedFoldersAndEncodesIdentity(t *testing.T) {
	provider := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page-1", Next: "root-page-2",
			Objects: []cleanup.Object{
				nestedObject("folder-z", "root-1", true),
				nestedObject("file-root", "root-1", false),
			},
		},
		{parent: "root-1", cursor: "root-page-2"}: {
			Cursor: "root-page-2", Complete: true,
			Objects: []cleanup.Object{nestedObject("folder-a", "root-1", true)},
		},
		{parent: "folder-a", cursor: ""}: {
			Cursor: "folder-a-page-1", Complete: true,
		},
		{parent: "folder-z", cursor: ""}: {
			Cursor: "folder-z-page-1", Complete: true,
			Objects: []cleanup.Object{nestedObject("file-nested", "folder-z", false)},
		},
	}}
	client := NewClient(provider)
	root, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 8, MaxObjects: 16, MaxDepth: 8})
	if err != nil {
		t.Fatalf("CollectRoot() error = %v", err)
	}
	if len(root.Pages) != 4 {
		t.Fatalf("page count = %d, want 4: %+v", len(root.Pages), root.Pages)
	}
	if root.Pages[0].ParentID != "root-1" || root.Pages[0].Number != 1 ||
		root.Pages[1].ParentID != "root-1" || root.Pages[1].Number != 2 {
		t.Fatalf("root page identity/sequence = %+v", root.Pages[:2])
	}
	if len(root.Pages[0].Objects) != 2 || root.Pages[0].Objects[0].ID != "file-root" {
		t.Fatalf("page object order = %+v, want deterministic ID order", root.Pages[0].Objects)
	}
	if len(provider.calls) != 4 || provider.calls[2].parent != "folder-a" || provider.calls[3].parent != "folder-z" {
		t.Fatalf("recursive call order = %+v, want root pages then sorted folder-a/folder-z", provider.calls)
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	for _, want := range []string{`"parent_id":"folder-z"`, `"object_type":"folder"`, `"object_type":"file"`, `"children_complete":true`, `"subtree_complete":true`, `"child_count":0`, `"subtree_object_count":0`} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("recursive inventory missing %s: %s", want, encoded)
		}
	}
}

func TestCollectRootRejectsDuplicateObjectsAndFolderCycles(t *testing.T) {
	duplicate := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page-1", Next: "root-page-2",
			Objects: []cleanup.Object{nestedObject("same", "root-1", false)},
		},
		{parent: "root-1", cursor: "root-page-2"}: {
			Cursor: "root-page-2", Complete: true,
			Objects: []cleanup.Object{nestedObject("same", "root-1", false)},
		},
	}}
	client := NewClient(duplicate)
	if _, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 8, MaxObjects: 16, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate object error = %v, want duplicate rejection", err)
	}

	cycle := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page-1", Complete: true,
			Objects: []cleanup.Object{nestedObject("folder-a", "root-1", true)},
		},
		{parent: "folder-a", cursor: ""}: {
			Cursor: "folder-a-page-1", Complete: true,
			Objects: []cleanup.Object{nestedObject("folder-b", "folder-a", true)},
		},
		{parent: "folder-b", cursor: ""}: {
			Cursor: "folder-b-page-1", Complete: true,
			Objects: []cleanup.Object{func() cleanup.Object {
				object := nestedObject("folder-a", "folder-b", true)
				object.Path = "folder-a/folder-b/folder-a"
				object.Depth = 3
				return object
			}()},
		},
	}}
	client = NewClient(cycle)
	if _, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 8, MaxObjects: 16, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("folder cycle error = %v, want cycle rejection", err)
	}
}

func TestCollectRootWithBoundsRejectsObjectOverflow(t *testing.T) {
	provider := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page-1", Complete: true,
			Objects: []cleanup.Object{
				nestedObject("file-a", "root-1", false),
				nestedObject("file-b", "root-1", false),
			},
		},
	}}
	client := NewClient(provider)
	_, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 8, MaxObjects: 1, MaxDepth: 8})
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("object bound error = %v, want bounded rejection", err)
	}
}

func TestCollectRootRejectsUnprovenEmptyFolder(t *testing.T) {
	provider := &nestedPaginatedProvider{pages: map[nestedPageKey]Page{
		{parent: "root-1", cursor: ""}: {
			Cursor: "root-page-1", Complete: true,
			Objects: []cleanup.Object{nestedObject("folder-a", "root-1", true)},
		},
		{parent: "folder-a", cursor: ""}: {
			Cursor: "folder-a-page-1", Next: "folder-a-page-2",
		},
		{parent: "folder-a", cursor: "folder-a-page-2"}: {
			Cursor: "folder-a-page-2",
		},
	}}
	client := NewClient(provider)
	if _, err := client.CollectRoot(context.Background(), RootRequest{
		Provider: "drive", AccountID: "account-1", RootID: "root-1", Namespace: "backup/home",
	}, InventoryLimits{MaxPages: 8, MaxObjects: 16, MaxDepth: 8}); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("incomplete empty-folder error = %v, want fail-closed traversal", err)
	}
}
