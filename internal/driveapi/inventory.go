package driveapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

const (
	CurrentInventoryPlanSchemaVersion = 1
	driveFolderMIMEType               = "application/vnd.google-apps.folder"
	maxDriveInventoryPages            = 10_000
	maxDriveInventoryObjects          = 1_000_000
)

type InventoryRoot struct {
	Provider  string `json:"provider"`
	AccountID string `json:"account_id"`
	RootID    string `json:"root_id"`
	DriveID   string `json:"drive_id,omitempty"`
	Namespace string `json:"namespace"`
}

type InventoryBinding struct {
	Provider        string              `json:"provider"`
	AccountID       string              `json:"account_id"`
	RootID          string              `json:"root_id"`
	Namespace       string              `json:"namespace"`
	ObjectID        string              `json:"object_id"`
	Class           cleanup.ObjectClass `json:"class"`
	RetainedPeerID  string              `json:"retained_peer_id,omitempty"`
	OwnershipMarker string              `json:"ownership_marker,omitempty"`
	RestoreEvidence string              `json:"restore_evidence,omitempty"`
}

type InventoryPlan struct {
	SchemaVersion int                `json:"schema_version"`
	ExpectedHash  string             `json:"expected_hash"`
	AccountID     string             `json:"account_id"`
	Roots         []InventoryRoot    `json:"roots"`
	Bindings      []InventoryBinding `json:"bindings"`
}

type InventoryClient struct {
	provider *quarantineHTTPClient
}

type inventoryParent struct {
	id    string
	path  string
	depth int
}

type driveListResponse struct {
	NextPageToken string      `json:"nextPageToken"`
	Files         []driveFile `json:"files"`
}

func NewInventoryClientWithTokenSource(client *http.Client, tokenSource AccessTokenSource) (*InventoryClient, error) {
	return newInventoryClientWithTokenSource(client, googleDriveAPIBaseURL, tokenSource)
}

func newInventoryClient(client *http.Client, endpoint, accessToken string) (*InventoryClient, error) {
	provider, err := newQuarantineHTTPClient(client, endpoint, accessToken)
	if err != nil {
		return nil, err
	}
	return &InventoryClient{provider: provider}, nil
}

func newInventoryClientWithTokenSource(
	client *http.Client,
	endpoint string,
	tokenSource AccessTokenSource,
) (*InventoryClient, error) {
	provider, err := newQuarantineHTTPClientWithTokenSource(client, endpoint, tokenSource)
	if err != nil {
		return nil, err
	}
	return &InventoryClient{provider: provider}, nil
}

func DecodeInventoryPlan(data []byte) (InventoryPlan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan InventoryPlan
	if err := decoder.Decode(&plan); err != nil {
		return InventoryPlan{}, fmt.Errorf("decode Drive inventory plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return InventoryPlan{}, errors.New("decode Drive inventory plan: trailing JSON is not allowed")
	}
	if err := validateInventoryPlan(plan, true); err != nil {
		return InventoryPlan{}, err
	}
	return plan, nil
}

func FreezeInventoryPlan(plan InventoryPlan) (InventoryPlan, error) {
	if err := validateInventoryPlan(plan, false); err != nil {
		return InventoryPlan{}, err
	}
	plan.Roots = append([]InventoryRoot(nil), plan.Roots...)
	plan.Bindings = append([]InventoryBinding(nil), plan.Bindings...)
	sortInventoryPlan(&plan)
	plan.ExpectedHash = inventoryPlanDigest(plan)
	return plan, nil
}

func (client *InventoryClient) Capture(ctx context.Context, plan InventoryPlan) (cleanup.RootSet, cleanup.InventoryAggregate, error) {
	if client == nil || client.provider == nil {
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, errors.New("Drive inventory client is not configured")
	}
	if ctx == nil {
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, errors.New("context is nil")
	}
	if err := validateInventoryPlan(plan, true); err != nil {
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, err
	}
	bindings := make(map[string]InventoryBinding, len(plan.Bindings))
	for _, binding := range plan.Bindings {
		bindings[inventoryBindingKey(binding.Provider, binding.AccountID, binding.RootID, binding.Namespace, binding.ObjectID)] = binding
	}
	usedBindings := make(map[string]struct{}, len(bindings))
	rootSet := cleanup.RootSet{SchemaVersion: cleanup.CurrentRootSetSchemaVersion, Roots: make([]cleanup.Root, 0, len(plan.Roots))}
	for _, declaration := range plan.Roots {
		root, err := client.captureRoot(ctx, declaration, bindings, usedBindings)
		if err != nil {
			return cleanup.RootSet{}, cleanup.InventoryAggregate{}, fmt.Errorf("capture Drive root %q: %w", declaration.RootID, err)
		}
		rootSet.Roots = append(rootSet.Roots, root)
	}
	if len(usedBindings) != len(bindings) {
		unused := make([]string, 0, len(bindings)-len(usedBindings))
		for key := range bindings {
			if _, used := usedBindings[key]; !used {
				unused = append(unused, key)
			}
		}
		sort.Strings(unused)
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, fmt.Errorf("Drive inventory binding did not resolve to a provider object: %s", strings.Join(unused, ", "))
	}
	frozen, err := cleanup.FreezeRootSet(rootSet)
	if err != nil {
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, err
	}
	aggregate, err := cleanup.BuildAggregate(frozen, plan.AccountID)
	if err != nil {
		return cleanup.RootSet{}, cleanup.InventoryAggregate{}, err
	}
	return frozen, aggregate, nil
}

func (client *InventoryClient) captureRoot(
	ctx context.Context,
	declaration InventoryRoot,
	bindings map[string]InventoryBinding,
	usedBindings map[string]struct{},
) (cleanup.Root, error) {
	root := cleanup.Root{
		Provider:  declaration.Provider,
		AccountID: declaration.AccountID,
		RootID:    declaration.RootID,
		Namespace: declaration.Namespace,
		Pages:     make([]cleanup.Page, 0),
	}
	queue := []inventoryParent{{id: declaration.RootID, path: "/" + url.PathEscape(declaration.Namespace), depth: 0}}
	seenObjects := make(map[string]struct{})
	seenFolders := map[string]struct{}{declaration.RootID: {}}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		pageToken := ""
		seenTokens := make(map[string]struct{})
		for {
			if len(root.Pages) >= maxDriveInventoryPages {
				return cleanup.Root{}, errors.New("Drive inventory exceeded the bounded page limit")
			}
			if _, duplicate := seenTokens[pageToken]; duplicate {
				return cleanup.Root{}, errors.New("Drive inventory pagination token repeated")
			}
			seenTokens[pageToken] = struct{}{}
			files, nextPageToken, err := client.listChildren(ctx, declaration, parent.id, pageToken)
			if err != nil {
				return cleanup.Root{}, err
			}
			page := cleanup.Page{
				Number:   len(root.Pages) + 1,
				ParentID: parent.id,
				Cursor:   inventoryCursor(pageToken),
				Status:   cleanup.PageComplete,
				Objects:  make([]cleanup.Object, 0, len(files)),
			}
			for _, listed := range files {
				if len(seenObjects) >= maxDriveInventoryObjects {
					return cleanup.Root{}, errors.New("Drive inventory exceeded the bounded object limit")
				}
				if _, duplicate := seenObjects[listed.ID]; duplicate {
					return cleanup.Root{}, fmt.Errorf("Drive object %q appeared more than once", listed.ID)
				}
				bindingKey := inventoryBindingKey(declaration.Provider, declaration.AccountID, declaration.RootID, declaration.Namespace, listed.ID)
				binding, bound := bindings[bindingKey]
				if !bound {
					binding = InventoryBinding{
						Provider:  declaration.Provider,
						AccountID: declaration.AccountID,
						RootID:    declaration.RootID,
						Namespace: declaration.Namespace,
						ObjectID:  listed.ID,
						Class:     cleanup.ClassUnknown,
					}
				}
				metadataDigest, err := driveMetadataDigest(listed)
				if err != nil {
					return cleanup.Root{}, err
				}
				object, err := inventoryObject(declaration, binding, listed, metadataDigest, parent)
				if err != nil {
					return cleanup.Root{}, err
				}
				page.Objects = append(page.Objects, object)
				seenObjects[object.ID] = struct{}{}
				if bound {
					usedBindings[bindingKey] = struct{}{}
				}
				if object.ObjectType == cleanup.ObjectTypeFolder {
					if _, duplicate := seenFolders[object.ID]; duplicate {
						return cleanup.Root{}, fmt.Errorf("Drive folder %q forms a duplicate or cycle", object.ID)
					}
					seenFolders[object.ID] = struct{}{}
					queue = append(queue, inventoryParent{id: object.ID, path: object.Path, depth: object.Depth})
				}
			}
			root.Pages = append(root.Pages, page)
			if nextPageToken == "" {
				break
			}
			pageToken = nextPageToken
		}
	}
	root.ExpectedPages = len(root.Pages)
	if err := completeFolderInventory(&root); err != nil {
		return cleanup.Root{}, err
	}
	return root, nil
}

func (client *InventoryClient) listChildren(
	ctx context.Context,
	root InventoryRoot,
	parentID string,
	pageToken string,
) ([]driveFile, string, error) {
	if err := validateDriveID(parentID, "Drive inventory parent ID"); err != nil {
		return nil, "", err
	}
	query := url.Values{
		"corpora":                   {"user"},
		"fields":                    {"nextPageToken,files(id,name,mimeType,parents,trashed,md5Checksum,size,modifiedTime,version,headRevisionId)"},
		"includeItemsFromAllDrives": {"true"},
		"pageSize":                  {"1000"},
		"q":                         {fmt.Sprintf("'%s' in parents", parentID)},
		"spaces":                    {"drive"},
		"supportsAllDrives":         {"true"},
	}
	if root.DriveID != "" {
		query.Set("corpora", "drive")
		query.Set("driveId", root.DriveID)
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	endpoint := *client.provider.baseURL
	endpoint.Path += "files"
	endpoint.RawQuery = query.Encode()
	data, _, status, err := client.provider.request(ctx, http.MethodGet, endpoint.String(), nil, false)
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK {
		return nil, "", fmt.Errorf("Drive inventory list rejected with status %d", status)
	}
	var response driveListResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, "", errors.New("Drive inventory list response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("Drive inventory list response has trailing data")
	}
	if len(response.Files) > 1000 {
		return nil, "", errors.New("Drive inventory list exceeded the requested page size")
	}
	return response.Files, response.NextPageToken, nil
}

func inventoryObject(
	root InventoryRoot,
	binding InventoryBinding,
	file driveFile,
	metadataDigest string,
	parent inventoryParent,
) (cleanup.Object, error) {
	if file.ID != binding.ObjectID || len(file.Parents) != 1 || file.Parents[0] != parent.id {
		return cleanup.Object{}, errors.New("Drive inventory object identity or parent is invalid")
	}
	if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.Version) == "" {
		return cleanup.Object{}, errors.New("Drive inventory object name or version is missing")
	}
	modifiedAt, err := time.Parse(time.RFC3339Nano, file.ModifiedTime)
	if err != nil {
		return cleanup.Object{}, errors.New("Drive inventory object modified time is invalid")
	}
	objectType := cleanup.ObjectTypeFile
	size := int64(0)
	generation := file.HeadRevisionID
	switch {
	case file.MIMEType == driveFolderMIMEType:
		objectType = cleanup.ObjectTypeFolder
		if generation == "" {
			generation = file.Version
		}
	case file.MD5Checksum == "" || file.Size == "" || file.HeadRevisionID == "":
		objectType = cleanup.ObjectTypeProviderNative
		generation = file.Version
		if binding.Class != cleanup.ClassUnknown {
			return cleanup.Object{}, errors.New("Drive provider-native object must remain class unknown")
		}
	default:
		size, err = strconv.ParseInt(file.Size, 10, 64)
		if err != nil || size < 0 {
			return cleanup.Object{}, errors.New("Drive inventory file size is invalid")
		}
	}
	return cleanup.Object{
		ID:              file.ID,
		ParentID:        parent.id,
		Name:            file.Name,
		Path:            parent.path + "/" + url.PathEscape(file.Name),
		ObjectType:      objectType,
		ContentHash:     file.MD5Checksum,
		Size:            size,
		Provider:        root.Provider,
		AccountID:       root.AccountID,
		RootID:          root.RootID,
		Namespace:       root.Namespace,
		Version:         file.Version,
		Generation:      generation,
		MetadataDigest:  metadataDigest,
		ModifiedAt:      modifiedAt.UTC(),
		Trashed:         file.Trashed,
		Depth:           parent.depth + 1,
		Class:           binding.Class,
		RetainedPeerID:  binding.RetainedPeerID,
		OwnershipMarker: binding.OwnershipMarker,
		RestoreEvidence: binding.RestoreEvidence,
	}, nil
}

func completeFolderInventory(root *cleanup.Root) error {
	objects := make(map[string]*cleanup.Object)
	children := make(map[string][]string)
	for pageIndex := range root.Pages {
		for objectIndex := range root.Pages[pageIndex].Objects {
			object := &root.Pages[pageIndex].Objects[objectIndex]
			objects[object.ID] = object
			children[object.ParentID] = append(children[object.ParentID], object.ID)
		}
	}
	visiting := make(map[string]bool)
	memo := make(map[string]int)
	var descendants func(string) (int, error)
	descendants = func(id string) (int, error) {
		if count, ok := memo[id]; ok {
			return count, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("Drive inventory ancestry cycle through %q", id)
		}
		visiting[id] = true
		total := 0
		for _, childID := range children[id] {
			total++
			child := objects[childID]
			if child != nil && child.ObjectType == cleanup.ObjectTypeFolder {
				count, err := descendants(childID)
				if err != nil {
					return 0, err
				}
				total += count
			}
		}
		delete(visiting, id)
		memo[id] = total
		return total, nil
	}
	for _, object := range objects {
		if object.ObjectType != cleanup.ObjectTypeFolder {
			continue
		}
		count, err := descendants(object.ID)
		if err != nil {
			return err
		}
		object.ChildrenComplete = true
		object.ChildCount = len(children[object.ID])
		object.SubtreeComplete = true
		object.SubtreeObjectCount = count
	}
	return nil
}

func validateInventoryPlan(plan InventoryPlan, requireHash bool) error {
	if plan.SchemaVersion != CurrentInventoryPlanSchemaVersion {
		return fmt.Errorf("unsupported Drive inventory plan schema_version %d", plan.SchemaVersion)
	}
	if strings.TrimSpace(plan.AccountID) == "" || strings.ContainsAny(plan.AccountID, "*?[]/\\\x00\r\n\t") {
		return errors.New("Drive inventory plan account ID is invalid")
	}
	if len(plan.Roots) == 0 {
		return errors.New("Drive inventory plan must declare at least one root")
	}
	rootKeys := make(map[string]struct{}, len(plan.Roots))
	for _, root := range plan.Roots {
		if root.Provider != "drive" || root.AccountID != plan.AccountID || strings.TrimSpace(root.Namespace) == "" || strings.ContainsAny(root.Namespace, "*?[]\\\x00\r\n\t") {
			return errors.New("Drive inventory root is invalid")
		}
		if err := validateDriveID(root.RootID, "Drive inventory root ID"); err != nil {
			return err
		}
		if root.DriveID != "" {
			if err := validateDriveID(root.DriveID, "Drive inventory shared drive ID"); err != nil {
				return err
			}
		}
		key := inventoryRootKey(root.Provider, root.AccountID, root.RootID, root.Namespace)
		if _, duplicate := rootKeys[key]; duplicate {
			return fmt.Errorf("duplicate Drive inventory root %q", key)
		}
		rootKeys[key] = struct{}{}
	}
	bindingKeys := make(map[string]struct{}, len(plan.Bindings))
	for _, binding := range plan.Bindings {
		rootKey := inventoryRootKey(binding.Provider, binding.AccountID, binding.RootID, binding.Namespace)
		if _, exists := rootKeys[rootKey]; !exists {
			return errors.New("Drive inventory binding is outside the declared roots")
		}
		if err := validateDriveID(binding.ObjectID, "Drive inventory binding object ID"); err != nil {
			return err
		}
		if !validInventoryClass(binding.Class) {
			return fmt.Errorf("Drive inventory binding class %q is invalid", binding.Class)
		}
		key := inventoryBindingKey(binding.Provider, binding.AccountID, binding.RootID, binding.Namespace, binding.ObjectID)
		if _, duplicate := bindingKeys[key]; duplicate {
			return fmt.Errorf("duplicate Drive inventory binding %q", key)
		}
		bindingKeys[key] = struct{}{}
	}
	if requireHash {
		if plan.ExpectedHash == "" || plan.ExpectedHash != inventoryPlanDigest(plan) {
			return errors.New("Drive inventory plan expected hash mismatch")
		}
	}
	return nil
}

func validInventoryClass(class cleanup.ObjectClass) bool {
	switch class {
	case cleanup.ClassActive, cleanup.ClassDuplicateSameHash, cleanup.ClassOrphan,
		cleanup.ClassLegacyUnmarked, cleanup.ClassQuarantined, cleanup.ClassExpectedFixture,
		cleanup.ClassLegacyRetained, cleanup.ClassConflict, cleanup.ClassUnknown:
		return true
	default:
		return false
	}
}

func inventoryPlanDigest(plan InventoryPlan) string {
	copyPlan := plan
	copyPlan.ExpectedHash = ""
	copyPlan.Roots = append([]InventoryRoot(nil), plan.Roots...)
	copyPlan.Bindings = append([]InventoryBinding(nil), plan.Bindings...)
	sortInventoryPlan(&copyPlan)
	data, _ := json.Marshal(copyPlan)
	return cleanup.Digest(data)
}

func sortInventoryPlan(plan *InventoryPlan) {
	sort.Slice(plan.Roots, func(i, j int) bool {
		return inventoryRootKey(plan.Roots[i].Provider, plan.Roots[i].AccountID, plan.Roots[i].RootID, plan.Roots[i].Namespace) <
			inventoryRootKey(plan.Roots[j].Provider, plan.Roots[j].AccountID, plan.Roots[j].RootID, plan.Roots[j].Namespace)
	})
	sort.Slice(plan.Bindings, func(i, j int) bool {
		left := plan.Bindings[i]
		right := plan.Bindings[j]
		return inventoryBindingKey(left.Provider, left.AccountID, left.RootID, left.Namespace, left.ObjectID) <
			inventoryBindingKey(right.Provider, right.AccountID, right.RootID, right.Namespace, right.ObjectID)
	})
}

func inventoryRootKey(provider, accountID, rootID, namespace string) string {
	return strings.Join([]string{provider, accountID, rootID, namespace}, "\x00")
}

func inventoryBindingKey(provider, accountID, rootID, namespace, objectID string) string {
	return inventoryRootKey(provider, accountID, rootID, namespace) + "\x00" + objectID
}

func inventoryCursor(pageToken string) string {
	if pageToken == "" {
		return "FIRST"
	}
	return pageToken
}
