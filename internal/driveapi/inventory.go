package driveapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

const (
	maxInventoryPages   = 1_000_000
	maxInventoryObjects = 1_000_000
	maxInventoryDepth   = 1024
)

type RootRequest struct {
	Provider  string
	AccountID string
	RootID    string
	Namespace string
}

type InventoryLimits struct {
	MaxPages   int
	MaxObjects int
	MaxDepth   int
}

func (limits InventoryLimits) validate() error {
	if limits.MaxPages <= 0 || limits.MaxPages > maxInventoryPages {
		return fmt.Errorf("max page limit must be between 1 and %d", maxInventoryPages)
	}
	if limits.MaxObjects <= 0 || limits.MaxObjects > maxInventoryObjects {
		return fmt.Errorf("max object limit must be between 1 and %d", maxInventoryObjects)
	}
	if limits.MaxDepth <= 0 || limits.MaxDepth > maxInventoryDepth {
		return fmt.Errorf("max depth limit must be between 1 and %d", maxInventoryDepth)
	}
	return nil
}

func (client *Client) CollectAllRoots(ctx context.Context, accountID string, requests []RootRequest, limits InventoryLimits) (cleanup.RootSet, error) {
	if client == nil || client.provider == nil {
		return cleanup.RootSet{}, errors.New("Drive provider is not configured")
	}
	if strings.TrimSpace(accountID) == "" {
		return cleanup.RootSet{}, errors.New("account is required")
	}
	if len(requests) == 0 {
		return cleanup.RootSet{}, errors.New("all-roots request set must not be empty")
	}
	if err := limits.validate(); err != nil {
		return cleanup.RootSet{}, err
	}
	requestedRootIDs := make(map[string]struct{}, len(requests))
	for index, request := range requests {
		if strings.TrimSpace(request.Provider) == "" || strings.TrimSpace(request.RootID) == "" || strings.TrimSpace(request.Namespace) == "" {
			return cleanup.RootSet{}, fmt.Errorf("root request %d provider, root, and namespace are required", index)
		}
		if request.AccountID != accountID {
			return cleanup.RootSet{}, fmt.Errorf("root request %d account %q does not match requested account %q", index, request.AccountID, accountID)
		}
		rootKey := strings.Join([]string{request.Provider, request.AccountID, request.RootID}, "\x00")
		if _, exists := requestedRootIDs[rootKey]; exists {
			return cleanup.RootSet{}, fmt.Errorf("overlapping physical root %q across namespaces", rootKey)
		}
		requestedRootIDs[rootKey] = struct{}{}
	}
	seenRoots := make(map[string]struct{}, len(requests))
	seenObjects := make(map[string]struct{})
	roots := make([]cleanup.Root, 0, len(requests))
	remaining := limits
	for index, request := range requests {
		if request.AccountID != accountID {
			return cleanup.RootSet{}, fmt.Errorf("root request %d account %q does not match requested account %q", index, request.AccountID, accountID)
		}
		rootKey := strings.Join([]string{request.Provider, request.AccountID, request.RootID}, "\x00")
		if _, exists := seenRoots[rootKey]; exists {
			return cleanup.RootSet{}, fmt.Errorf("overlapping physical root %q across namespaces", rootKey)
		}
		seenRoots[rootKey] = struct{}{}
		if remaining.MaxPages <= 0 {
			return cleanup.RootSet{}, fmt.Errorf("all-roots page limit %d exceeded", limits.MaxPages)
		}
		if remaining.MaxObjects <= 0 {
			return cleanup.RootSet{}, fmt.Errorf("all-roots object limit %d exceeded", limits.MaxObjects)
		}
		root, err := client.CollectRoot(ctx, request, remaining)
		if err != nil {
			return cleanup.RootSet{}, fmt.Errorf("collect root %q: %w", rootKey, err)
		}
		remaining.MaxPages -= len(root.Pages)
		objectCount := 0
		for _, page := range root.Pages {
			objectCount += len(page.Objects)
			for _, object := range page.Objects {
				physicalKey := strings.Join([]string{object.Provider, object.AccountID, object.ID}, "\x00")
				if _, requestedRoot := requestedRootIDs[physicalKey]; requestedRoot {
					return cleanup.RootSet{}, fmt.Errorf("overlapping nested root object %q", physicalKey)
				}
				if _, exists := seenObjects[physicalKey]; exists {
					return cleanup.RootSet{}, fmt.Errorf("overlapping physical object %q across roots", physicalKey)
				}
				seenObjects[physicalKey] = struct{}{}
			}
		}
		remaining.MaxObjects -= objectCount
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		left := strings.Join([]string{roots[i].Provider, roots[i].AccountID, roots[i].RootID, roots[i].Namespace}, "\x00")
		right := strings.Join([]string{roots[j].Provider, roots[j].AccountID, roots[j].RootID, roots[j].Namespace}, "\x00")
		return left < right
	})
	rootSet, err := cleanup.FreezeRootSet(cleanup.RootSet{SchemaVersion: cleanup.CurrentRootSetSchemaVersion, Roots: roots})
	if err != nil {
		return cleanup.RootSet{}, fmt.Errorf("freeze all-roots set: %w", err)
	}
	return rootSet, nil
}

type folderVisit struct {
	id    string
	path  string
	depth int
}

type objectLocation struct {
	pageIndex   int
	objectIndex int
}

func (client *Client) CollectRoot(ctx context.Context, request RootRequest, limits InventoryLimits) (cleanup.Root, error) {
	if client == nil || client.provider == nil {
		return cleanup.Root{}, errors.New("Drive provider is not configured")
	}
	for name, value := range map[string]string{"provider": request.Provider, "account": request.AccountID, "root": request.RootID, "namespace": request.Namespace} {
		if strings.TrimSpace(value) == "" {
			return cleanup.Root{}, fmt.Errorf("%s is required", name)
		}
	}
	if err := limits.validate(); err != nil {
		return cleanup.Root{}, err
	}
	root := cleanup.Root{
		Provider: request.Provider, AccountID: request.AccountID, RootID: request.RootID,
		Namespace: request.Namespace, ExpectedPages: limits.MaxPages,
	}
	queue := []folderVisit{{id: request.RootID, depth: 0}}
	folderParents := map[string]string{request.RootID: ""}
	nextPageNumber := 0
	objectCount := 0
	locations := make(map[string]objectLocation)
	seenObjects := make(map[string]struct{})
	seenPages := make(map[string]struct{})

	for len(queue) > 0 {
		visit := queue[0]
		queue = queue[1:]
		cursor := ""
		discoveredFolders := make([]folderVisit, 0)
		for {
			if nextPageNumber >= limits.MaxPages {
				return cleanup.Root{}, fmt.Errorf("root page limit %d reached before provider completion", limits.MaxPages)
			}
			page, err := client.List(ctx, request.AccountID, visit.id, cursor)
			if err != nil {
				return cleanup.Root{}, fmt.Errorf("list parent %q page %d: %w", visit.id, nextPageNumber+1, err)
			}
			nextPageNumber++
			if strings.TrimSpace(page.Cursor) == "" {
				return cleanup.Root{}, fmt.Errorf("parent %q page %d has no provider cursor", visit.id, nextPageNumber)
			}
			if page.ParentID != "" && page.ParentID != visit.id {
				return cleanup.Root{}, fmt.Errorf("parent %q page %d parent readback %q is inconsistent", visit.id, nextPageNumber, page.ParentID)
			}
			pageKey := visit.id + "\x00" + page.Cursor
			if _, exists := seenPages[pageKey]; exists {
				return cleanup.Root{}, fmt.Errorf("duplicate page cursor %q for parent %q", page.Cursor, visit.id)
			}
			seenPages[pageKey] = struct{}{}
			if page.Complete && page.Next != "" {
				return cleanup.Root{}, fmt.Errorf("parent %q page %d is complete but has next cursor", visit.id, nextPageNumber)
			}
			if !page.Complete && strings.TrimSpace(page.Next) == "" {
				return cleanup.Root{}, fmt.Errorf("parent %q page %d is incomplete and has no next cursor", visit.id, nextPageNumber)
			}
			if page.Next == page.Cursor {
				return cleanup.Root{}, fmt.Errorf("parent %q page %d next cursor did not advance", visit.id, nextPageNumber)
			}
			objects := append([]cleanup.Object(nil), page.Objects...)
			sort.Slice(objects, func(i, j int) bool {
				return inventoryObjectSortKey(objects[i]) < inventoryObjectSortKey(objects[j])
			})
			for objectIndex := range objects {
				object, err := normalizeInventoryObject(request, visit, objects[objectIndex])
				if err != nil {
					return cleanup.Root{}, fmt.Errorf("parent %q page %d object %q: %w", visit.id, nextPageNumber, objects[objectIndex].ID, err)
				}
				if object.Depth > limits.MaxDepth {
					return cleanup.Root{}, fmt.Errorf("inventory depth limit %d exceeded by object %q", limits.MaxDepth, object.ID)
				}
				if _, exists := seenObjects[object.ID]; exists {
					if object.ObjectType == cleanup.ObjectTypeFolder && folderIsAncestor(folderParents, visit.id, object.ID) {
						return cleanup.Root{}, fmt.Errorf("folder ancestry cycle through %q", object.ID)
					}
					return cleanup.Root{}, fmt.Errorf("duplicate object ID %q", object.ID)
				}
				if objectCount >= limits.MaxObjects {
					return cleanup.Root{}, fmt.Errorf("inventory object limit %d exceeded", limits.MaxObjects)
				}
				objectCount++
				if object.ObjectType == cleanup.ObjectTypeFolder {
					if object.ID == request.RootID || folderIsAncestor(folderParents, visit.id, object.ID) {
						return cleanup.Root{}, fmt.Errorf("folder ancestry cycle through %q", object.ID)
					}
					folderParents[object.ID] = visit.id
					discoveredFolders = append(discoveredFolders, folderVisit{id: object.ID, path: object.Path, depth: object.Depth})
				}
				seenObjects[object.ID] = struct{}{}
				objects[objectIndex] = object
			}
			pageIndex := len(root.Pages)
			root.Pages = append(root.Pages, cleanup.Page{
				Number: nextPageNumber, ParentID: visit.id, Cursor: page.Cursor,
				Status: cleanup.PageComplete, Objects: objects,
			})
			for objectIndex, object := range objects {
				locations[object.ID] = objectLocation{pageIndex: pageIndex, objectIndex: objectIndex}
			}
			if page.Complete {
				break
			}
			cursor = page.Next
		}
		sort.Slice(discoveredFolders, func(i, j int) bool {
			if discoveredFolders[i].id != discoveredFolders[j].id {
				return discoveredFolders[i].id < discoveredFolders[j].id
			}
			return discoveredFolders[i].path < discoveredFolders[j].path
		})
		queue = append(queue, discoveredFolders...)
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].id != queue[j].id {
				return queue[i].id < queue[j].id
			}
			return queue[i].depth < queue[j].depth
		})
	}
	root.ExpectedPages = nextPageNumber
	if err := finalizeInventoryRoot(&root, locations, limits.MaxDepth, limits.MaxObjects); err != nil {
		return cleanup.Root{}, err
	}
	return root, nil
}

func normalizeInventoryObject(request RootRequest, visit folderVisit, object cleanup.Object) (cleanup.Object, error) {
	if object.Provider != request.Provider || object.AccountID != request.AccountID ||
		object.RootID != request.RootID || object.Namespace != request.Namespace {
		return cleanup.Object{}, errors.New("object is outside root scope")
	}
	for name, value := range map[string]string{
		"id": object.ID, "parent_id": object.ParentID, "name": object.Name,
		"version": object.Version, "generation": object.Generation, "etag": object.ETag,
	} {
		if strings.TrimSpace(value) == "" {
			return cleanup.Object{}, fmt.Errorf("%s is required", name)
		}
	}
	if object.ParentID != visit.id {
		return cleanup.Object{}, fmt.Errorf("parent ID %q does not match listed parent %q", object.ParentID, visit.id)
	}
	if object.ModifiedAt.IsZero() {
		return cleanup.Object{}, errors.New("modified_at is required")
	}
	if object.Depth != visit.depth+1 {
		return cleanup.Object{}, fmt.Errorf("depth %d does not match traversal depth %d", object.Depth, visit.depth+1)
	}
	if object.Depth <= 0 {
		return cleanup.Object{}, errors.New("depth must be positive")
	}
	if object.Size < 0 {
		return cleanup.Object{}, errors.New("size must not be negative")
	}
	expectedPath := object.Name
	if visit.path != "" {
		expectedPath = strings.TrimSuffix(visit.path, "/") + "/" + object.Name
	}
	if strings.TrimSpace(object.Path) == "" {
		object.Path = expectedPath
	} else if object.Path != expectedPath {
		return cleanup.Object{}, fmt.Errorf("path %q does not match traversal path %q", object.Path, expectedPath)
	}
	switch object.ObjectType {
	case cleanup.ObjectTypeFile:
		if strings.TrimSpace(object.ContentHash) == "" {
			return cleanup.Object{}, errors.New("content_hash is required for files")
		}
		if object.ChildrenComplete || object.ChildCount != 0 || object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return cleanup.Object{}, errors.New("file contains folder subtree metadata")
		}
	case cleanup.ObjectTypeFolder:
		if object.Size != 0 {
			return cleanup.Object{}, errors.New("folder size must be zero")
		}
		if object.ChildrenComplete || object.ChildCount != 0 || object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return cleanup.Object{}, errors.New("folder subtree proof must be derived by complete traversal")
		}
	default:
		return cleanup.Object{}, fmt.Errorf("object_type %q is unsupported", object.ObjectType)
	}
	object.ChildrenComplete = false
	object.ChildCount = 0
	object.SubtreeComplete = false
	object.SubtreeWriterFence = ""
	object.EmptyCheckIDs = nil
	object.SubtreeObjectCount = 0
	return object, nil
}

func folderIsAncestor(parents map[string]string, start, target string) bool {
	for current := start; current != ""; current = parents[current] {
		if current == target {
			return true
		}
	}
	return false
}


func finalizeInventoryRoot(root *cleanup.Root, locations map[string]objectLocation, maxDepth, maxObjects int) error {
	objects := make(map[string]cleanup.Object)
	children := make(map[string][]string)
	folders := make(map[string]struct{})
	folderPages := make(map[string]struct{})
	for _, page := range root.Pages {
		if page.ParentID != root.RootID {
			folderPages[page.ParentID] = struct{}{}
		}
		for _, object := range page.Objects {
			objects[object.ID] = object
			children[object.ParentID] = append(children[object.ParentID], object.ID)
			if object.ObjectType == cleanup.ObjectTypeFolder {
				folders[object.ID] = struct{}{}
			}
		}
	}
	if len(objects) > maxObjects {
		return fmt.Errorf("root has too many objects for bounded tree finalization")
	}
	for folderID := range folders {
		if _, ok := folderPages[folderID]; !ok {
			return fmt.Errorf("folder %q has no complete child enumeration", folderID)
		}
	}
	visiting := make(map[string]bool)
	subtreeMemo := make(map[string]int)
	var subtreeCount func(string) (int, error)
	subtreeCount = func(folderID string) (int, error) {
		if count, ok := subtreeMemo[folderID]; ok {
			return count, nil
		}
		if visiting[folderID] {
			return 0, fmt.Errorf("folder subtree cycle through %q", folderID)
		}
		if len(visiting) >= maxDepth {
			return 0, fmt.Errorf("inventory subtree depth exceeds bounded maximum %d", maxDepth)
		}
		visiting[folderID] = true
		total := 0
		for _, childID := range children[folderID] {
			if total >= maxObjects {
				return 0, fmt.Errorf("inventory subtree exceeds bounded object maximum %d", maxObjects)
			}
			total++
			if objects[childID].ObjectType == cleanup.ObjectTypeFolder {
				nested, err := subtreeCount(childID)
				if err != nil {
					return 0, err
				}
				if nested > maxObjects-total {
					return 0, fmt.Errorf("inventory subtree exceeds bounded object maximum %d", maxObjects)
				}
				total += nested
			}
		}
		delete(visiting, folderID)
		subtreeMemo[folderID] = total
		return total, nil
	}
	for folderID := range folders {
		object := objects[folderID]
		count, err := subtreeCount(folderID)
		if err != nil {
			return err
		}
		location, ok := locations[folderID]
		if !ok {
			return fmt.Errorf("folder %q has no descriptor location", folderID)
		}
		stored := &root.Pages[location.pageIndex].Objects[location.objectIndex]
		stored.ChildrenComplete = true
		stored.ChildCount = len(children[folderID])
		stored.SubtreeComplete = true
		stored.SubtreeObjectCount = count
		object = *stored
		objects[folderID] = object
	}
	return nil
}

func inventoryObjectSortKey(object cleanup.Object) string {
	return strings.Join([]string{object.Provider, object.AccountID, object.RootID, object.Namespace, object.ID}, "\x00")
}
