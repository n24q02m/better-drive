package driveapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type fakeDriveProvider struct {
	listCalls       int
	quarantineCalls int
	restoreCalls    int
	page            Page
	quarantineErr   error
	restoreErr      error
	cancelAfter     context.CancelFunc
}

func (f *fakeDriveProvider) List(_ context.Context, _, parentID, _ string) (Page, error) {
	f.listCalls++
	if f.page.Cursor == "" {
		f.page = Page{Cursor: "cursor-1", Complete: true, ParentID: parentID}
	}
	return f.page, nil
}

func (f *fakeDriveProvider) Quarantine(_ context.Context, req QuarantineRequest) (MutationReceipt, error) {
	f.quarantineCalls++
	if f.cancelAfter != nil {
		f.cancelAfter()
		f.cancelAfter = nil
	}
	if f.quarantineErr != nil {
		return MutationReceipt{}, f.quarantineErr
	}
	return MutationReceipt{
		ObjectID:         req.ObjectID,
		ParentID:         req.QuarantineParentID,
		State:            "quarantined",
		ReadbackVerified: true,
		RequestID:        req.RequestID,
	}, nil
}

func (f *fakeDriveProvider) Restore(_ context.Context, req RestoreRequest) (MutationReceipt, error) {
	f.restoreCalls++
	if f.cancelAfter != nil {
		f.cancelAfter()
		f.cancelAfter = nil
	}
	if f.restoreErr != nil {
		return MutationReceipt{}, f.restoreErr
	}
	return MutationReceipt{
		ObjectID:         req.ObjectID,
		ParentID:         req.OriginalParentID,
		State:            "restored",
		ReadbackVerified: true,
		RequestID:        req.RequestID,
	}, nil
}

func validQuarantineCapability(objectID, accountID, rootID string) MutationCapability {
	return MutationCapability{
		ClaimID:          "claim-drive-1",
		Role:             "workstation",
		Intent:           "BD-DRIVE-MUTATION-RW",
		ObjectID:         objectID,
		AccountID:        accountID,
		RootID:           rootID,
		Mode:             cleanup.ModeQuarantine,
		Budget:           cleanup.Budget{MaxObjects: 10, MaxBytes: 1000},
		ExpiresAt:        time.Unix(200, 0).UTC(),
		Nonce:            "nonce-drive-1",
		Issuer:           "issuer-security",
		Signature:        "sig-valid-hex",
		AcceptsNoCASRisk: true,
	}
}

func validQuarantineReq() QuarantineRequest {
	return QuarantineRequest{
		AccountID:          "account-1",
		RootID:             "root-1",
		Namespace:          "backup/home",
		ObjectID:           "obj-123",
		ParentID:           "folder-active",
		QuarantineParentID: "folder-quarantine",
		ExpectedETag:       "etag-1",
		Version:            "v1",
		Generation:         "gen-1",
		Size:               100,
		Hash:               "sha-123",
		RequestID:          "req-q-1",
	}
}

func TestClientListsReadOnlyPages(t *testing.T) {
	provider := &fakeDriveProvider{}
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
	provider := &fakeDriveProvider{page: Page{Cursor: "cursor-1", Complete: true}}
	client := NewClient(provider)
	if _, err := client.List(context.Background(), "account-1", "root-1", ""); err == nil || !strings.Contains(err.Error(), "parent ID") {
		t.Fatalf("missing parent readback error = %v", err)
	}
}

func TestClientRejectsMismatchedProviderParentReadback(t *testing.T) {
	provider := &fakeDriveProvider{page: Page{Cursor: "cursor-1", Complete: true, ParentID: "other-root"}}
	client := NewClient(provider)
	if _, err := client.List(context.Background(), "account-1", "root-1", ""); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched parent readback error = %v", err)
	}
}

func TestDriveQuarantineRequiresCapabilityAndAcceptsNoCASRisk(t *testing.T) {
	provider := &fakeDriveProvider{}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	req := validQuarantineReq()

	// Missing capability
	if _, err := client.Quarantine(context.Background(), req, MutationCapability{}); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("missing capability error = %v", err)
	}

	// AcceptsNoCASRisk = false must fail
	capNoCAS := validQuarantineCapability(req.ObjectID, req.AccountID, req.RootID)
	capNoCAS.AcceptsNoCASRisk = false
	if _, err := client.Quarantine(context.Background(), req, capNoCAS); err == nil || !strings.Contains(err.Error(), "no-CAS") {
		t.Fatalf("missing no-CAS acceptance error = %v", err)
	}

	// Valid capability succeeds
	validCap := validQuarantineCapability(req.ObjectID, req.AccountID, req.RootID)
	receipt, err := client.Quarantine(context.Background(), req, validCap)
	if err != nil {
		t.Fatalf("Quarantine() error = %v", err)
	}
	if !receipt.ReadbackVerified || receipt.State != "quarantined" || receipt.ParentID != req.QuarantineParentID {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if provider.quarantineCalls != 1 {
		t.Fatalf("quarantine calls = %d, want 1", provider.quarantineCalls)
	}
}

func TestDriveQuarantineRejectsReplay(t *testing.T) {
	provider := &fakeDriveProvider{}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	req := validQuarantineReq()
	validCap := validQuarantineCapability(req.ObjectID, req.AccountID, req.RootID)

	if _, err := client.Quarantine(context.Background(), req, validCap); err != nil {
		t.Fatalf("first Quarantine error = %v", err)
	}
	if _, err := client.Quarantine(context.Background(), req, validCap); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replayed Quarantine error = %v, want replay rejection", err)
	}
	if provider.quarantineCalls != 1 {
		t.Fatalf("replayed calls = %d, want 1", provider.quarantineCalls)
	}
}

func TestDriveQuarantineUnknownSettlementOnFailureOrCancel(t *testing.T) {
	provider := &fakeDriveProvider{quarantineErr: errors.New("timeout")}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	req := validQuarantineReq()
	validCap := validQuarantineCapability(req.ObjectID, req.AccountID, req.RootID)

	if _, err := client.Quarantine(context.Background(), req, validCap); err == nil || !errors.Is(err, ErrUnknownSettlement) {
		t.Fatalf("provider failure error = %v, want ErrUnknownSettlement", err)
	}

	// Replay rejected even after unknown settlement
	if _, err := client.Quarantine(context.Background(), req, validCap); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay after failure error = %v, want replay rejection", err)
	}

	// Cancel after provider call
	ctx, cancel := context.WithCancel(context.Background())
	provider2 := &fakeDriveProvider{cancelAfter: cancel}
	client2 := NewClient(provider2)
	client2.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	req2 := validQuarantineReq()
	req2.RequestID = "req-q-2"
	if _, err := client2.Quarantine(ctx, req2, validCap); err == nil || !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled post-mutation error = %v, want unknown settlement and canceled", err)
	}
}

func TestDriveRestoreFixtureWorkflow(t *testing.T) {
	provider := &fakeDriveProvider{}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	req := RestoreRequest{
		AccountID:        "account-1",
		RootID:           "root-1",
		Namespace:        "backup/home",
		ObjectID:         "obj-123",
		CurrentParentID:  "folder-quarantine",
		OriginalParentID: "folder-active",
		ExpectedETag:     "etag-1",
		RequestID:        "req-r-1",
	}
	validCap := validQuarantineCapability(req.ObjectID, req.AccountID, req.RootID)
	receipt, err := client.Restore(context.Background(), req, validCap)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !receipt.ReadbackVerified || receipt.State != "restored" || receipt.ParentID != req.OriginalParentID {
		t.Fatalf("unexpected restore receipt: %+v", receipt)
	}
}
