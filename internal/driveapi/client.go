package driveapi

import (
	"context"
	"errors"
	"strings"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type Page struct {
	Cursor   string           `json:"cursor"`
	Next     string           `json:"next_cursor,omitempty"`
	Complete bool             `json:"complete"`
	ParentID string           `json:"parent_id"`
	Objects  []cleanup.Object `json:"objects"`
}

type Provider interface {
	List(ctx context.Context, accountID, parentID, cursor string) (Page, error)
}

type Client struct {
	provider Provider
}

func NewClient(provider Provider) *Client {
	return &Client{provider: provider}
}

func (client *Client) List(ctx context.Context, accountID, parentID, cursor string) (Page, error) {
	if client == nil || client.provider == nil {
		return Page{}, errors.New("Drive provider is not configured")
	}
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(parentID) == "" {
		return Page{}, errors.New("account and parent IDs are required")
	}
	if err := contextErr(ctx); err != nil {
		return Page{}, err
	}
	page, err := client.provider.List(ctx, accountID, parentID, cursor)
	if err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(page.ParentID) == "" {
		return Page{}, errors.New("Drive provider page parent ID is required")
	}
	if page.ParentID != parentID {
		return Page{}, errors.New("Drive provider page parent ID does not match requested parent")
	}
	return page, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
