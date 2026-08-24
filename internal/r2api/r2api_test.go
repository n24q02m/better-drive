package r2api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func r2Identity(key, version, etag string) ObjectIdentity {
	return ObjectIdentity{AccountID: "acct-1", Bucket: "source", Key: key, VersionID: version, ETag: etag}
}

func r2Object(key, version, etag string, size int64, hash string) Object {
	return Object{Identity: r2Identity(key, version, etag), Size: size, SHA256: hash}
}

func r2Limits() Limits {
	return Limits{MaxPages: 2, MaxObjects: 3, MaxBytes: 10}
}

func TestClientListEnforcesPageObjectAndByteBoundsAndCancellation(t *testing.T) {
	provider := &fakeProvider{pages: []Page{
		{Cursor: "p1", Next: "p2", Objects: []Object{r2Object("one", "v1", "e1", 4, "h1")}},
		{Cursor: "p2", Complete: true, Objects: []Object{r2Object("two", "v1", "e2", 4, "h2")}},
	}}
	client := NewClient(provider)
	inventory, err := client.List(context.Background(), ListRequest{AccountID: "acct-1", Bucket: "source"}, r2Limits())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if inventory.ObjectCount != 2 || inventory.ByteCount != 8 || len(inventory.Pages) != 2 {
		t.Fatalf("inventory = %+v", inventory)
	}
	if provider.listCalls != 2 {
		t.Fatalf("list calls = %d, want 2", provider.listCalls)
	}

	provider = &fakeProvider{pages: []Page{{Cursor: "p1", Complete: true, Objects: []Object{r2Object("one", "v1", "e1", 11, "h1")}}}}
	client = NewClient(provider)
	if _, err := client.List(context.Background(), ListRequest{AccountID: "acct-1", Bucket: "source"}, r2Limits()); err == nil || !strings.Contains(err.Error(), "byte") {
		t.Fatalf("byte limit error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.List(ctx, ListRequest{AccountID: "acct-1", Bucket: "source"}, r2Limits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled List() error = %v", err)
	}
}

func TestCopyRequiresExactCapabilityAndVerifiedChecksumReadback(t *testing.T) {
	source := r2Identity("one", "v1", "e1")
	destination := ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "", ETag: ""}
	provider := &fakeProvider{head: map[string]Object{
		identityKey(source): r2Object("one", "v1", "e1", 4, "h1"),
		identityKey(ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"}): {Identity: ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"}, Size: 4, SHA256: "h1"},
	}}
	client := NewClient(provider)
	request := CopyRequest{Source: source, Destination: destination, ExpectedSize: 4, ExpectedSHA256: "h1", RequestID: "copy-1"}
	if _, err := client.Copy(context.Background(), request, CopyCapability{}); err == nil || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("missing capability error = %v", err)
	}
	capability := NewCopyCapability(source, destination, "copy-1", time.Unix(200, 0).UTC(), "signed-copy")
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	receipt, err := client.Copy(context.Background(), request, capability)
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if !receipt.ReadbackVerified || receipt.Destination.VersionID != "v2" || receipt.Destination.ETag != "e2" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if provider.copyCalls != 1 || provider.headCalls != 2 {
		t.Fatalf("calls: copy=%d head=%d", provider.copyCalls, provider.headCalls)
	}

	provider.head[identityKey(source)] = r2Object("one", "v1", "changed", 4, "h1")
	if _, err := client.Copy(context.Background(), request, capability); err == nil || !strings.Contains(err.Error(), "ETag") {
		t.Fatalf("source drift error = %v", err)
	}
}

func TestCopyRequiresExactProviderRequestIDAndUnknownPostProviderCancellation(t *testing.T) {
	source := r2Identity("one", "v1", "e1")
	destination := ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one"}
	head := map[string]Object{
		identityKey(source): r2Object("one", "v1", "e1", 4, "h1"),
		identityKey(ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"}): {
			Identity: ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"},
			Size:     4, SHA256: "h1",
		},
	}
	provider := &fakeProvider{head: head, copyRequestID: "wrong-copy"}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	request := CopyRequest{Source: source, Destination: destination, ExpectedSize: 4, ExpectedSHA256: "h1", RequestID: "copy-1"}
	capability := NewCopyCapability(source, destination, request.RequestID, time.Unix(200, 0).UTC(), "signed-copy")
	if _, err := client.Copy(context.Background(), request, capability); err == nil || !errors.Is(err, ErrUnknownSettlement) || !strings.Contains(err.Error(), "request") {
		t.Fatalf("wrong receipt request ID error = %v, want unknown settlement", err)
	}

	provider.copyRequestID = ""
	ctx, cancel := context.WithCancel(context.Background())
	provider.cancelAfterCopy = cancel
	if _, err := client.Copy(ctx, request, capability); err == nil || !errors.Is(err, ErrUnknownSettlement) || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-provider cancellation error = %v, want unknown cancellation", err)
	}
}

func TestDeleteAndPurgeUseDistinctCapabilitiesAndPurgeRequiresEmptyLifecycle(t *testing.T) {
	source := r2Identity("one", "v1", "e1")
	quarantine := ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "", ETag: ""}
	provider := &fakeProvider{head: map[string]Object{}}
	provider.head[identityKey(source)] = r2Object("one", "v1", "e1", 4, "h1")
	provider.head[identityKey(ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"})] = Object{Identity: ObjectIdentity{AccountID: "acct-1", Bucket: "quarantine", Key: "one", VersionID: "v2", ETag: "e2"}, Size: 4, SHA256: "h1"}
	client := NewClient(provider)
	client.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	copyRequest := CopyRequest{Source: source, Destination: quarantine, ExpectedSize: 4, ExpectedSHA256: "h1", RequestID: "copy-1"}
	copyCapability := NewCopyCapability(source, quarantine, "copy-1", time.Unix(200, 0).UTC(), "signed-copy")
	copyReceipt, err := client.Copy(context.Background(), copyRequest, copyCapability)
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	deleteRequest := DeleteRequest{Source: source, Quarantine: copyReceipt.Destination, Copy: copyReceipt, CopyRequestID: copyReceipt.RequestID, RequestID: "delete-1"}
	deleteCap := NewDeleteCapability(source, copyReceipt.Destination, "delete-1", time.Unix(200, 0).UTC(), "signed-delete")
	if _, err := client.Delete(context.Background(), deleteRequest, DeleteCapability{}); err == nil || !strings.Contains(err.Error(), "delete capability") {
		t.Fatalf("missing capability error = %v", err)
	}
	forged := copyReceipt
	forged.verifiedProof = nil
	forgedRequest := deleteRequest
	forgedRequest.Copy = forged
	if _, err := client.Delete(context.Background(), forgedRequest, deleteCap); err == nil || !strings.Contains(err.Error(), "verified quarantine copy") {
		t.Fatalf("forged copy proof error = %v", err)
	}
	wrongCopyRequest := deleteRequest
	wrongCopyRequest.CopyRequestID = "another-copy"
	if _, err := client.Delete(context.Background(), wrongCopyRequest, deleteCap); err == nil || !strings.Contains(err.Error(), "verified quarantine copy") {
		t.Fatalf("cross-request copy proof error = %v", err)
	}
	if _, err := client.Delete(context.Background(), deleteRequest, deleteCap); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if provider.deleteCalls != 1 {
		t.Fatalf("delete calls = %d", provider.deleteCalls)
	}
	purgeRequest := PurgeRequest{Object: copyReceipt.Destination, RequestID: "purge-1", Lifecycle: "retain-7d"}
	purgeCap := NewPurgeCapability(copyReceipt.Destination, "purge-1", time.Unix(200, 0).UTC(), "signed-purge")
	if _, err := client.Purge(context.Background(), purgeRequest, purgeCap); err == nil || !strings.Contains(err.Error(), "lifecycle") {
		t.Fatalf("lifecycle error = %v", err)
	}
	purgeRequest.Lifecycle = ""
	if _, err := client.Purge(context.Background(), purgeRequest, PurgeCapability{}); err == nil || !strings.Contains(err.Error(), "purge capability") {
		t.Fatalf("distinct capability error = %v", err)
	}
	if _, err := client.Purge(context.Background(), purgeRequest, purgeCap); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if provider.purgeCalls != 1 {
		t.Fatalf("purge calls = %d", provider.purgeCalls)
	}
}

type fakeProvider struct {
	pages           []Page
	pageIndex       int
	head            map[string]Object
	listCalls       int
	headCalls       int
	copyCalls       int
	deleteCalls     int
	purgeCalls      int
	copyRequestID   string
	cancelAfterCopy context.CancelFunc
}

func (provider *fakeProvider) List(_ context.Context, _ ListRequest) (Page, error) {
	provider.listCalls++
	if provider.pageIndex >= len(provider.pages) {
		return Page{}, errors.New("unexpected page")
	}
	page := provider.pages[provider.pageIndex]
	provider.pageIndex++
	return page, nil
}

func (provider *fakeProvider) Head(_ context.Context, identity ObjectIdentity) (Object, error) {
	provider.headCalls++
	if provider.head == nil {
		return Object{Identity: identity}, nil
	}
	object, ok := provider.head[identityKey(identity)]
	if !ok {
		return Object{}, errors.New("head not found")
	}
	return object, nil
}

func (provider *fakeProvider) Copy(_ context.Context, request CopyRequest) (CopyReceipt, error) {
	provider.copyCalls++
	requestID := request.RequestID
	if provider.copyRequestID != "" {
		requestID = provider.copyRequestID
	}
	if provider.cancelAfterCopy != nil {
		provider.cancelAfterCopy()
		provider.cancelAfterCopy = nil
	}
	return CopyReceipt{Source: request.Source, Destination: ObjectIdentity{AccountID: request.Destination.AccountID, Bucket: request.Destination.Bucket, Key: request.Destination.Key, VersionID: "v2", ETag: "e2"}, Size: request.ExpectedSize, SHA256: request.ExpectedSHA256, RequestID: requestID}, nil
}

func (provider *fakeProvider) Delete(_ context.Context, request DeleteRequest) (MutationReceipt, error) {
	provider.deleteCalls++
	return MutationReceipt{Identity: request.Source, State: "deleted", RequestID: request.RequestID}, nil
}

func (provider *fakeProvider) Purge(_ context.Context, request PurgeRequest) (MutationReceipt, error) {
	provider.purgeCalls++
	return MutationReceipt{Identity: request.Object, State: "purged", RequestID: request.RequestID}, nil
}
