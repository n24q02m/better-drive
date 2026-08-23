package driveapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type RootRequest struct {
	Provider  string
	AccountID string
	RootID    string
	Namespace string
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
