package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const CurrentInventorySchemaVersion = 1

const (
	PageComplete        = "COMPLETE"
	PageIncomplete      = "INCOMPLETE"
	InventoryComplete   = "COMPLETE"
	InventoryIncomplete = "INCOMPLETE"
)

type Page struct {
	Number  int      `json:"number"`
	Cursor  string   `json:"cursor"`
	Status  string   `json:"status"`
	Objects []Object `json:"objects"`
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
	if err := json.Unmarshal(data, &rootSet); err != nil {
		return RootSet{}, fmt.Errorf("decode all-roots set: %w", err)
	}
	if rootSet.SchemaVersion != CurrentInventorySchemaVersion {
		return RootSet{}, fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	return rootSet, nil
}

func BuildAggregate(rootSet RootSet, accountID string) (InventoryAggregate, error) {
	if rootSet.SchemaVersion != CurrentInventorySchemaVersion {
		return InventoryAggregate{}, fmt.Errorf("unsupported all-roots schema_version %d", rootSet.SchemaVersion)
	}
	if strings.TrimSpace(accountID) == "" {
		return InventoryAggregate{}, errors.New("account is required")
	}
	if len(rootSet.Roots) == 0 {
		return InventoryAggregate{}, errors.New("all-roots set must not be empty")
	}
	rootSetHash, err := rootSetDigest(rootSet)
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
		key := rootKey(root)
		if _, exists := seenRoots[key]; exists {
			return InventoryAggregate{}, fmt.Errorf("duplicate root %q", key)
		}
		seenRoots[key] = struct{}{}
		seenPages := make(map[int]struct{}, len(root.Pages))
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
			if strings.TrimSpace(page.Cursor) == "" {
				return InventoryAggregate{}, fmt.Errorf("root %q page %d has no cursor readback", key, page.Number)
			}
			pageCount++
			for _, object := range page.Objects {
				if object.AccountID != root.AccountID || object.RootID != root.RootID || object.Namespace != root.Namespace || object.Provider != root.Provider {
					return InventoryAggregate{}, fmt.Errorf("object %q is outside root %q", object.ID, key)
				}
				if object.ID == "" {
					return InventoryAggregate{}, errors.New("inventory object ID is required")
				}
				objectID := objectKey(object)
				if _, exists := seenObjects[objectID]; exists {
					return InventoryAggregate{}, fmt.Errorf("duplicate object %q", objectID)
				}
				seenObjects[objectID] = struct{}{}
				objects = append(objects, object)
				byteCount += object.Size
			}
		}
		if len(seenPages) != root.ExpectedPages {
			return InventoryAggregate{}, fmt.Errorf("root %q missing page: expected %d, got %d", key, root.ExpectedPages, len(seenPages))
		}
	}
	if len(seenRoots) != len(rootSet.Roots) {
		return InventoryAggregate{}, errors.New("root set contains duplicate roots")
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

func BuildState(rootSet RootSet, aggregate InventoryAggregate, err error) InventoryState {
	state := InventoryState{SchemaVersion: CurrentInventorySchemaVersion, Status: InventoryIncomplete}
	if rootSetHash, hashErr := rootSetDigest(rootSet); hashErr == nil {
		state.RootSetHash = rootSetHash
	}
	if err == nil && aggregate.Status == InventoryComplete {
		state.Status = InventoryComplete
		for _, root := range rootSet.Roots {
			for _, page := range root.Pages {
				state.CompletedPages = append(state.CompletedPages, fmt.Sprintf("%s/%d", rootKey(root), page.Number))
			}
		}
		sort.Strings(state.CompletedPages)
		return state
	}
	if err != nil {
		state.Errors = []string{err.Error()}
	}
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

func rootSetDigest(rootSet RootSet) (string, error) {
	copySet := rootSet
	copySet.Roots = append([]Root(nil), rootSet.Roots...)
	sort.Slice(copySet.Roots, func(i, j int) bool { return rootKey(copySet.Roots[i]) < rootKey(copySet.Roots[j]) })
	for i := range copySet.Roots {
		copySet.Roots[i].Pages = append([]Page(nil), copySet.Roots[i].Pages...)
		sort.Slice(copySet.Roots[i].Pages, func(a, b int) bool { return copySet.Roots[i].Pages[a].Number < copySet.Roots[i].Pages[b].Number })
	}
	canonical, err := json.Marshal(copySet)
	if err != nil {
		return "", fmt.Errorf("canonicalize root set: %w", err)
	}
	return Digest(canonical), nil
}
