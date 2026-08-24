package driveapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type RootRequest struct {
	Provider  string
	AccountID string
	RootID    string
	Namespace string
}

func (client *Client) CollectAllRoots(ctx context.Context, accountID string, requests []RootRequest, maxPages int) (cleanup.RootSet, error) {
	if client == nil || client.provider == nil {
		return cleanup.RootSet{}, errors.New("Drive provider is not configured")
	}
	if strings.TrimSpace(accountID) == "" {
		return cleanup.RootSet{}, errors.New("account is required")
	}
	if len(requests) == 0 {
		return cleanup.RootSet{}, errors.New("all-roots request set must not be empty")
	}
	if maxPages <= 0 {
		return cleanup.RootSet{}, errors.New("max page limit must be positive")
	}
	seen := make(map[string]struct{}, len(requests))
	roots := make([]cleanup.Root, 0, len(requests))
	for index, request := range requests {
		if request.AccountID != accountID {
			return cleanup.RootSet{}, fmt.Errorf("root request %d account %q does not match requested account %q", index, request.AccountID, accountID)
		}
		key := strings.Join([]string{request.Provider, request.AccountID, request.RootID, request.Namespace}, "\x00")
		if _, exists := seen[key]; exists {
			return cleanup.RootSet{}, fmt.Errorf("duplicate root %q", key)
		}
		seen[key] = struct{}{}
		root, err := client.CollectRoot(ctx, request, maxPages)
		if err != nil {
			return cleanup.RootSet{}, fmt.Errorf("collect root %q: %w", key, err)
		}
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

func (client *Client) CollectRoot(ctx context.Context, request RootRequest, maxPages int) (cleanup.Root, error) {
	if client == nil || client.provider == nil {
		return cleanup.Root{}, errors.New("Drive provider is not configured")
	}
	for name, value := range map[string]string{"provider": request.Provider, "account": request.AccountID, "root": request.RootID, "namespace": request.Namespace} {
		if strings.TrimSpace(value) == "" {
			return cleanup.Root{}, fmt.Errorf("%s is required", name)
		}
	}
	if maxPages <= 0 {
		return cleanup.Root{}, errors.New("max page limit must be positive")
	}
	root := cleanup.Root{Provider: request.Provider, AccountID: request.AccountID, RootID: request.RootID, Namespace: request.Namespace, ExpectedPages: maxPages}
	cursor := ""
	for pageNumber := 1; pageNumber <= maxPages; pageNumber++ {
		page, err := client.List(ctx, request.AccountID, request.RootID, cursor)
		if err != nil {
			return cleanup.Root{}, fmt.Errorf("list root page %d: %w", pageNumber, err)
		}
		if strings.TrimSpace(page.Cursor) == "" {
			return cleanup.Root{}, fmt.Errorf("root page %d has no provider cursor", pageNumber)
		}
		root.Pages = append(root.Pages, cleanup.Page{Number: pageNumber, Cursor: page.Cursor, Status: cleanup.PageComplete, Objects: page.Objects})
		if page.Complete && page.Next == "" {
			if pageNumber != maxPages {
				root.ExpectedPages = pageNumber
			}
			return root, nil
		}
		if strings.TrimSpace(page.Next) == "" {
			return cleanup.Root{}, fmt.Errorf("root page %d is not complete and has no next cursor", pageNumber)
		}
		if page.Next == page.Cursor {
			return cleanup.Root{}, fmt.Errorf("root page %d next cursor did not advance", pageNumber)
		}
		cursor = page.Next
	}
	return cleanup.Root{}, fmt.Errorf("root page limit %d reached before provider completion", maxPages)
}
