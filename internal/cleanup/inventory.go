package cleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	CurrentInventorySchemaVersion = 3
	CurrentRootSetSchemaVersion   = 4
)
const maxInt64 = int64(1<<63 - 1)

const (
	maxCurrentInventoryDepth   = 1024
	maxCurrentInventoryObjects = 1_000_000
)

const (
	PageComplete        = "COMPLETE"
	PageIncomplete      = "INCOMPLETE"
	InventoryComplete   = "COMPLETE"
	InventoryIncomplete = "INCOMPLETE"
)

type Page struct {
	Number   int      `json:"number"`
	ParentID string   `json:"parent_id"`
	Cursor   string   `json:"cursor"`
	Status   string   `json:"status"`
	Objects  []Object `json:"objects"`
}

type Root struct {
	Provider      string `json:"provider"`
	AccountID     string `json:"account_id"`
	RootID        string `json:"root_id"`
	Namespace     string `json:"namespace"`
	ExpectedPages int    `json:"expected_pages"`
	Pages         []Page `json:"pages"`
}

type RootSet struct {
	SchemaVersion int    `json:"schema_version"`
	ExpectedHash  string `json:"expected_hash"`
	Roots         []Root `json:"roots"`
}

type InventoryAggregate struct {
	SchemaVersion int      `json:"schema_version"`
	Status        string   `json:"status"`
	AccountID     string   `json:"account_id"`
	RootSetHash   string   `json:"root_set_hash"`
	InventoryHash string   `json:"inventory_hash"`
	RootCount     int      `json:"root_count"`
	PageCount     int      `json:"page_count"`
	ObjectCount   int      `json:"object_count"`
	ByteCount     int64    `json:"byte_count"`
	Objects       []Object `json:"objects"`
}

type InventoryState struct {
	SchemaVersion  int      `json:"schema_version"`
	Status         string   `json:"status"`
	RootSetHash    string   `json:"root_set_hash"`
	CompletedPages []string `json:"completed_pages"`
	Errors         []string `json:"errors,omitempty"`
}

func DecodeRootSet(data []byte) (RootSet, error) {
	var rootSet RootSet
	if err := decodeStrictJSONRecord(data, &rootSet); err != nil {
		return RootSet{}, fmt.Errorf("decode all-roots set: %w", err)
	}
	if rootSet.SchemaVersion != CurrentRootSetSchemaVersion {
		return RootSet{}, fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	if strings.TrimSpace(rootSet.ExpectedHash) == "" {
		return RootSet{}, errors.New("all-roots set expected hash is required")
	}
	actual, err := rootSetDigest(rootSet)
	if err != nil {
		return RootSet{}, err
	}
	if rootSet.ExpectedHash != actual {
		return RootSet{}, fmt.Errorf("all-roots set expected hash mismatch: got %q, want %q", rootSet.ExpectedHash, actual)
	}
	return rootSet, nil
}

func FreezeRootSet(rootSet RootSet) (RootSet, error) {
	if rootSet.SchemaVersion != CurrentRootSetSchemaVersion {
		return RootSet{}, fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	hash, err := rootSetDigest(rootSet)
	if err != nil {
		return RootSet{}, err
	}
	rootSet.ExpectedHash = hash
	return rootSet, nil
}

func DecodeAggregate(data []byte, rootSet RootSet, accountID string) (InventoryAggregate, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var aggregate InventoryAggregate
	if err := decoder.Decode(&aggregate); err != nil {
		return InventoryAggregate{}, fmt.Errorf("decode inventory aggregate: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return InventoryAggregate{}, errors.New("decode inventory aggregate: trailing JSON is not allowed")
		}
		return InventoryAggregate{}, fmt.Errorf("decode inventory aggregate: trailing data: %w", err)
	}
	verified, err := validateAggregateCapture(rootSet, aggregate, accountID)
	if err != nil {
		return InventoryAggregate{}, err
	}
	return verified, nil
}

func validateAggregateCapture(rootSet RootSet, aggregate InventoryAggregate, accountID string) (InventoryAggregate, error) {
	expected, err := BuildAggregate(rootSet, accountID)
	if err != nil {
		return InventoryAggregate{}, fmt.Errorf("rebuild inventory aggregate: %w", err)
	}
	if aggregate.SchemaVersion != CurrentInventorySchemaVersion {
		return InventoryAggregate{}, fmt.Errorf("unsupported inventory aggregate schema_version %d", aggregate.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(aggregate.Objects))
	for _, object := range aggregate.Objects {
		key := objectKey(object)
		if _, exists := seen[key]; exists {
			return InventoryAggregate{}, fmt.Errorf("duplicate inventory object %q", key)
		}
		seen[key] = struct{}{}
	}
	expectedBytes, err := canonicalAggregateBytes(expected)
	if err != nil {
		return InventoryAggregate{}, err
	}
	actualBytes, err := canonicalAggregateBytes(aggregate)
	if err != nil {
		return InventoryAggregate{}, err
	}
	if !bytes.Equal(expectedBytes, actualBytes) {
		return InventoryAggregate{}, errors.New("inventory aggregate does not exactly match rebuilt all-roots capture")
	}
	return expected, nil
}

func canonicalAggregateBytes(aggregate InventoryAggregate) ([]byte, error) {
	copyAggregate := aggregate
	copyAggregate.Objects = append([]Object(nil), aggregate.Objects...)
	sort.Slice(copyAggregate.Objects, func(i, j int) bool { return objectKey(copyAggregate.Objects[i]) < objectKey(copyAggregate.Objects[j]) })
	data, err := json.Marshal(copyAggregate)
	if err != nil {
		return nil, fmt.Errorf("canonicalize inventory aggregate: %w", err)
	}
	return data, nil
}

func rootSetIdentityHash(rootSet RootSet) (string, error) {
	if rootSet.SchemaVersion != CurrentRootSetSchemaVersion {
		return "", fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	if strings.TrimSpace(rootSet.ExpectedHash) == "" {
		return "", errors.New("all-roots set expected hash is required")
	}
	currentHash, err := rootSetDigest(rootSet)
	if err != nil {
		return "", err
	}
	if rootSet.ExpectedHash != currentHash {
		return "", fmt.Errorf("all-roots set expected hash mismatch: got %q, want %q", rootSet.ExpectedHash, currentHash)
	}
	return currentHash, nil
}

func BuildAggregate(rootSet RootSet, accountID string) (InventoryAggregate, error) {
	if rootSet.SchemaVersion != CurrentRootSetSchemaVersion {
		return InventoryAggregate{}, fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	if strings.TrimSpace(accountID) == "" {
		return InventoryAggregate{}, errors.New("account is required")
	}
	if len(rootSet.Roots) == 0 {
		return InventoryAggregate{}, errors.New("all-roots set must not be empty")
	}
	rootSetHash, err := rootSetIdentityHash(rootSet)
	if err != nil {
		return InventoryAggregate{}, err
	}
	seenRoots := make(map[string]struct{}, len(rootSet.Roots))
	seenObjects := make(map[string]struct{})
	objects := make([]Object, 0)
	pageCount := 0
	var byteCount int64
	for rootIndex, root := range rootSet.Roots {
		if root.AccountID != accountID {
			return InventoryAggregate{}, fmt.Errorf("root %d account %q does not match requested account %q", rootIndex, root.AccountID, accountID)
		}
		if err := validateRoot(root); err != nil {
			return InventoryAggregate{}, fmt.Errorf("root %d: %w", rootIndex, err)
		}
		key := rootPhysicalKey(root)
		if _, exists := seenRoots[key]; exists {
			return InventoryAggregate{}, fmt.Errorf("duplicate physical root %q across namespaces", key)
		}
		seenRoots[key] = struct{}{}
		if len(root.Pages) == 0 {
			return InventoryAggregate{}, fmt.Errorf("root %q has no pages", key)
		}
		seenPages := make(map[int]struct{}, len(root.Pages))
		seenCursors := make(map[string]struct{}, len(root.Pages))
		for _, page := range root.Pages {
			if page.Number < 1 || page.Number > root.ExpectedPages {
				return InventoryAggregate{}, fmt.Errorf("root %q page %d is outside expected range", key, page.Number)
			}
			if _, exists := seenPages[page.Number]; exists {
				return InventoryAggregate{}, fmt.Errorf("duplicate page %d for root %q", page.Number, key)
			}
			seenPages[page.Number] = struct{}{}
			if page.Status != PageComplete {
				return InventoryAggregate{}, fmt.Errorf("root %q page %d is incomplete", key, page.Number)
			}
			if strings.TrimSpace(page.ParentID) == "" {
				return InventoryAggregate{}, fmt.Errorf("root %q page %d has no parent readback", key, page.Number)
			}
			if strings.TrimSpace(page.Cursor) == "" {
				return InventoryAggregate{}, fmt.Errorf("root %q page %d has no cursor readback", key, page.Number)
			}
			cursorKey := page.ParentID + "\x00" + page.Cursor
			if _, exists := seenCursors[cursorKey]; exists {
				return InventoryAggregate{}, fmt.Errorf("duplicate cursor %q for root %q parent %q", page.Cursor, key, page.ParentID)
			}
			seenCursors[cursorKey] = struct{}{}
			pageCount++
			for _, object := range page.Objects {
				if err := validateInventoryObject(root, page, object); err != nil {
					return InventoryAggregate{}, fmt.Errorf("root %q page %d object %q: %w", key, page.Number, object.ID, err)
				}
				objectID := physicalObjectKey(object)
				if _, exists := seenObjects[objectID]; exists {
					return InventoryAggregate{}, fmt.Errorf("duplicate physical object %q", objectID)
				}
				if len(objects) >= maxCurrentInventoryObjects {
					return InventoryAggregate{}, fmt.Errorf("inventory has too many objects for bounded validation")
				}
				seenObjects[objectID] = struct{}{}
				objects = append(objects, object)
				if byteCount > maxInt64-object.Size {
					return InventoryAggregate{}, fmt.Errorf("inventory byte count overflow")
				}
				byteCount += object.Size
			}
		}
		if len(seenPages) != root.ExpectedPages {
			return InventoryAggregate{}, fmt.Errorf("root %q missing page: expected %d, got %d", key, root.ExpectedPages, len(seenPages))
		}
		if err := validateRootTree(root); err != nil {
			return InventoryAggregate{}, fmt.Errorf("root %q: %w", key, err)
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objectKey(objects[i]) < objectKey(objects[j]) })
	canonical, err := json.Marshal(struct {
		RootSetHash string   `json:"root_set_hash"`
		Objects     []Object `json:"objects"`
	}{RootSetHash: rootSetHash, Objects: objects})
	if err != nil {
		return InventoryAggregate{}, fmt.Errorf("canonicalize inventory: %w", err)
	}
	return InventoryAggregate{
		SchemaVersion: CurrentInventorySchemaVersion,
		Status:        InventoryComplete,
		AccountID:     accountID,
		RootSetHash:   rootSetHash,
		InventoryHash: Digest(canonical),
		RootCount:     len(seenRoots),
		PageCount:     pageCount,
		ObjectCount:   len(objects),
		ByteCount:     byteCount,
		Objects:       objects,
	}, nil
}

func validateInventoryObject(root Root, page Page, object Object) error {
	if object.Provider != root.Provider || object.AccountID != root.AccountID ||
		object.RootID != root.RootID || object.Namespace != root.Namespace {
		return fmt.Errorf("object is outside root scope")
	}
	if strings.TrimSpace(object.SubtreeWriterFence) != "" || len(object.EmptyCheckIDs) != 0 {
		return errors.New("inventory object must not carry mutation fence or empty-check evidence")
	}
	for name, value := range map[string]string{
		"id": object.ID, "parent_id": object.ParentID, "name": object.Name, "path": object.Path,
		"provider": object.Provider, "account_id": object.AccountID, "root_id": object.RootID,
		"namespace": object.Namespace, "version": object.Version, "generation": object.Generation,
		"metadata_digest": object.MetadataDigest,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if err := validateSHA256Hex(object.MetadataDigest, "metadata_digest"); err != nil {
		return err
	}
	if object.ParentID != page.ParentID {
		return fmt.Errorf("parent ID %q does not match page parent %q", object.ParentID, page.ParentID)
	}
	if object.ModifiedAt.IsZero() {
		return errors.New("modified_at is required")
	}
	if object.Size < 0 {
		return errors.New("size must not be negative")
	}
	if object.Depth < 1 {
		return errors.New("depth must be positive")
	}
	if object.ChildCount < 0 || object.SubtreeObjectCount < 0 {
		return errors.New("folder child counts must not be negative")
	}
	switch object.ObjectType {
	case ObjectTypeFile:
		if strings.TrimSpace(object.ContentHash) == "" {
			return errors.New("content_hash is required for files")
		}
		if object.ChildrenComplete || object.ChildCount != 0 || object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return errors.New("file contains folder subtree metadata")
		}
	case ObjectTypeFolder:
		if object.Size != 0 {
			return errors.New("folder size must be zero")
		}
		if !object.ChildrenComplete || !object.SubtreeComplete {
			return errors.New("folder children/subtree are not complete")
		}
	case ObjectTypeProviderNative:
		if object.Size != 0 || object.ContentHash != "" || object.Class != ClassUnknown {
			return errors.New("provider-native object must remain an unknown zero-byte inventory record")
		}
		if object.ChildrenComplete || object.ChildCount != 0 || object.SubtreeComplete || object.SubtreeObjectCount != 0 {
			return errors.New("provider-native object contains folder subtree metadata")
		}
	default:
		return fmt.Errorf("object_type %q is unsupported", object.ObjectType)
	}
	return nil
}

func validateRootTree(root Root) error {
	objects := make(map[string]Object)
	children := make(map[string][]string)
	folderPages := make(map[string]bool)
	if len(root.Pages) > maxCurrentInventoryObjects {
		return fmt.Errorf("root has too many pages for bounded tree validation")
	}
	for _, page := range root.Pages {
		if page.ParentID != root.RootID {
			parent, ok := objects[page.ParentID]
			if ok && parent.ObjectType == ObjectTypeFolder {
				folderPages[page.ParentID] = true
			}
		}
		for _, object := range page.Objects {
			objects[object.ID] = object
			children[object.ParentID] = append(children[object.ParentID], object.ID)
		}
	}
	if len(objects) > maxCurrentInventoryObjects {
		return fmt.Errorf("root has too many objects for bounded tree validation")
	}
	for _, page := range root.Pages {
		if page.ParentID == root.RootID {
			continue
		}
		parent, ok := objects[page.ParentID]
		if !ok || parent.ObjectType != ObjectTypeFolder {
			return fmt.Errorf("page %d parent %q is not an enumerated folder", page.Number, page.ParentID)
		}
		folderPages[page.ParentID] = true
	}
	for _, object := range objects {
		if object.ObjectType == ObjectTypeFolder && !folderPages[object.ID] {
			return fmt.Errorf("folder %q has no complete child enumeration", object.ID)
		}
	}
	visiting := make(map[string]bool)
	depthMemo := make(map[string]int)
	var depthOf func(string) (int, error)
	depthOf = func(id string) (int, error) {
		if id == root.RootID {
			return 0, nil
		}
		if depth, ok := depthMemo[id]; ok {
			return depth, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("folder ancestry cycle through %q", id)
		}
		object, ok := objects[id]
		if !ok {
			return 0, fmt.Errorf("parent %q is not present in inventory", id)
		}
		visiting[id] = true
		if object.ParentID != root.RootID {
			parent, ok := objects[object.ParentID]
			if !ok || parent.ObjectType != ObjectTypeFolder {
				return 0, fmt.Errorf("object %q parent %q is not an enumerated folder", id, object.ParentID)
			}
		}
		parentDepth, err := depthOf(object.ParentID)
		if err != nil {
			return 0, err
		}
		delete(visiting, id)
		depth := parentDepth + 1
		if depth > maxCurrentInventoryDepth {
			return 0, fmt.Errorf("inventory depth exceeds bounded maximum %d", maxCurrentInventoryDepth)
		}
		depthMemo[id] = depth
		return depth, nil
	}
	for id, object := range objects {
		depth, err := depthOf(id)
		if err != nil {
			return err
		}
		if object.Depth != depth {
			return fmt.Errorf("object %q depth %d does not match ancestry depth %d", id, object.Depth, depth)
		}
	}
	visiting = make(map[string]bool)
	subtreeMemo := make(map[string]int)
	var subtreeCount func(string) (int, error)
	subtreeCount = func(id string) (int, error) {
		if count, ok := subtreeMemo[id]; ok {
			return count, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("folder subtree cycle through %q", id)
		}
		if len(visiting) >= maxCurrentInventoryDepth {
			return 0, fmt.Errorf("inventory subtree depth exceeds bounded maximum %d", maxCurrentInventoryDepth)
		}
		visiting[id] = true
		total := 0
		for _, childID := range children[id] {
			if total >= maxCurrentInventoryObjects {
				return 0, fmt.Errorf("inventory subtree exceeds bounded object maximum %d", maxCurrentInventoryObjects)
			}
			total++
			child := objects[childID]
			if child.ObjectType == ObjectTypeFolder {
				nested, err := subtreeCount(childID)
				if err != nil {
					return 0, err
				}
				if nested > maxCurrentInventoryObjects-total {
					return 0, fmt.Errorf("inventory subtree exceeds bounded object maximum %d", maxCurrentInventoryObjects)
				}
				total += nested
			}
		}
		delete(visiting, id)
		subtreeMemo[id] = total
		return total, nil
	}
	for id, object := range objects {
		if object.ObjectType != ObjectTypeFolder {
			continue
		}
		if object.ChildCount != len(children[id]) {
			return fmt.Errorf("folder %q child count %d does not match complete traversal count %d", id, object.ChildCount, len(children[id]))
		}
		count, err := subtreeCount(id)

		if err != nil {
			return err
		}
		if object.SubtreeObjectCount != count {
			return fmt.Errorf("folder %q subtree object count %d does not match complete traversal count %d", id, object.SubtreeObjectCount, count)
		}
	}
	return nil
}

func BuildState(rootSet RootSet, aggregate InventoryAggregate, err error) InventoryState {
	state := InventoryState{SchemaVersion: CurrentInventorySchemaVersion, Status: InventoryIncomplete}
	if rootSetHash, hashErr := rootSetIdentityHash(rootSet); hashErr == nil {
		state.RootSetHash = rootSetHash
	}
	if err != nil {
		state.Errors = []string{err.Error()}
		return state
	}
	if aggregate.Status != InventoryComplete {
		return state
	}
	if _, verifyErr := validateAggregateCapture(rootSet, aggregate, aggregate.AccountID); verifyErr != nil {
		state.Errors = []string{verifyErr.Error()}
		return state
	}
	state.Status = InventoryComplete
	for _, root := range rootSet.Roots {
		for _, page := range root.Pages {
			state.CompletedPages = append(state.CompletedPages, fmt.Sprintf("%s/%d", rootKey(root), page.Number))
		}
	}
	sort.Strings(state.CompletedPages)
	return state
}

func validateRoot(root Root) error {
	for name, value := range map[string]string{"provider": root.Provider, "account_id": root.AccountID, "root_id": root.RootID, "namespace": root.Namespace} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if root.ExpectedPages <= 0 {
		return errors.New("expected_pages must be positive")
	}
	return nil
}

func rootKey(root Root) string {
	return strings.Join([]string{root.Provider, root.AccountID, root.RootID, root.Namespace}, "\x00")
}

func rootPhysicalKey(root Root) string {
	return strings.Join([]string{root.Provider, root.AccountID, root.RootID}, "\x00")
}

func physicalObjectKey(object Object) string {
	return strings.Join([]string{object.Provider, object.AccountID, object.ID}, "\x00")
}

func rootSetDigest(rootSet RootSet) (string, error) {
	copySet := rootSet
	copySet.ExpectedHash = ""
	copySet.Roots = append([]Root(nil), rootSet.Roots...)
	sort.Slice(copySet.Roots, func(i, j int) bool { return rootKey(copySet.Roots[i]) < rootKey(copySet.Roots[j]) })
	for i := range copySet.Roots {
		copySet.Roots[i].Pages = append([]Page(nil), copySet.Roots[i].Pages...)
		sort.Slice(copySet.Roots[i].Pages, func(a, b int) bool {
			if copySet.Roots[i].Pages[a].Number != copySet.Roots[i].Pages[b].Number {
				return copySet.Roots[i].Pages[a].Number < copySet.Roots[i].Pages[b].Number
			}
			return copySet.Roots[i].Pages[a].ParentID < copySet.Roots[i].Pages[b].ParentID
		})
		for pageIndex := range copySet.Roots[i].Pages {
			page := &copySet.Roots[i].Pages[pageIndex]
			page.Objects = append([]Object(nil), page.Objects...)
			sort.Slice(page.Objects, func(a, b int) bool { return objectKey(page.Objects[a]) < objectKey(page.Objects[b]) })
		}
	}
	canonical, err := json.Marshal(copySet)
	if err != nil {
		return "", fmt.Errorf("canonicalize root set: %w", err)
	}
	return Digest(canonical), nil
}
