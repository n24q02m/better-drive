package driveapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

type nonStandardRoundTripper struct{}

func (nonStandardRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("must not be called")
}

type rotatingTokenSource struct {
	calls atomic.Int32
}

func (source *rotatingTokenSource) AccessToken(context.Context) (string, error) {
	return fmt.Sprintf("token-%d", source.calls.Add(1)), nil
}

func quarantineDriveFile(parent, version string) driveFile {
	return driveFile{
		ID: "object-1", Name: "object.bin", MIMEType: "application/octet-stream",
		Parents: []string{parent}, MD5Checksum: "abc123", Size: "5",
		ModifiedTime: time.Unix(100, 0).UTC().Format(time.RFC3339Nano),
		Version:      version, HeadRevisionID: "revision-1",
	}
}

func quarantineObject() cleanup.Object {
	file := quarantineDriveFile("source-parent", "7")
	metadataDigest, err := driveMetadataDigest(file)
	if err != nil {
		panic(err)
	}
	return cleanup.Object{
		ID:             "object-1",
		ParentID:       "source-parent",
		ObjectType:     cleanup.ObjectTypeFile,
		ContentHash:    "abc123",
		Size:           5,
		Version:        "7",
		Generation:     "revision-1",
		MetadataDigest: metadataDigest,
		ModifiedAt:     time.Unix(100, 0).UTC(),
	}
}

func writeDriveFile(t *testing.T, writer http.ResponseWriter, parent, version string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(quarantineDriveFile(parent, version)); err != nil {
		t.Fatal(err)
	}
}

func TestDriveMetadataDigestBindsEveryMutationRelevantField(t *testing.T) {
	base := quarantineDriveFile("source-parent", "7")
	baseDigest, err := driveMetadataDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*driveFile)
	}{
		{name: "name", mutate: func(file *driveFile) { file.Name = "renamed.bin" }},
		{name: "mime", mutate: func(file *driveFile) { file.MIMEType = "application/zip" }},
		{name: "parent", mutate: func(file *driveFile) { file.Parents = []string{"other-parent"} }},
		{name: "trash", mutate: func(file *driveFile) { file.Trashed = true }},
		{name: "hash", mutate: func(file *driveFile) { file.MD5Checksum = "changed" }},
		{name: "size", mutate: func(file *driveFile) { file.Size = "6" }},
		{name: "modified", mutate: func(file *driveFile) { file.ModifiedTime = time.Unix(101, 0).UTC().Format(time.RFC3339Nano) }},
		{name: "version", mutate: func(file *driveFile) { file.Version = "8" }},
		{name: "revision", mutate: func(file *driveFile) { file.HeadRevisionID = "revision-2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Parents = append([]string(nil), base.Parents...)
			test.mutate(&changed)
			digest, err := driveMetadataDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseDigest {
				t.Fatalf("%s mutation did not change metadata digest", test.name)
			}
		})
	}
}

func TestQuarantineHTTPClientUsesFreshTokenPerRequestAndMovesExactIDOnce(t *testing.T) {
	var patchCalls atomic.Int32
	var requestCalls atomic.Int32
	parent := "source-parent"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestNumber := requestCalls.Add(1)
		expectedAuthorization := fmt.Sprintf("Bearer token-%d", requestNumber)
		if authorization := request.Header.Get("Authorization"); authorization != expectedAuthorization {
			t.Errorf("Authorization = %q, want %q", authorization, expectedAuthorization)
		}
		if request.URL.Path != "/drive/v3/files/object-1" {
			t.Errorf("path = %q", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		switch request.Method {
		case http.MethodGet:
			version := "7"
			if parent == "quarantine-parent" {
				version = "8"
			}
			writeDriveFile(t, writer, parent, version)
		case http.MethodPatch:
			patchCalls.Add(1)
			if request.Header.Get("If-Match") != "" {
				t.Error("undocumented If-Match precondition must not imply Drive CAS")
			}
			if got := request.URL.Query().Get("addParents"); got != "quarantine-parent" {
				t.Errorf("addParents = %q", got)
			}
			if got := request.URL.Query().Get("removeParents"); got != "source-parent" {
				t.Errorf("removeParents = %q", got)
			}
			if request.URL.Query().Get("supportsAllDrives") != "true" {
				t.Error("supportsAllDrives was not true")
			}
			parent = "quarantine-parent"
			writeDriveFile(t, writer, parent, "8")
		default:
			http.Error(writer, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	tokenSource := &rotatingTokenSource{}
	client, err := newQuarantineHTTPClientWithTokenSource(server.Client(), server.URL+"/drive/v3/", tokenSource)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.move(context.Background(), moveRequest{
		Expected:            quarantineObject(),
		DestinationParentID: "quarantine-parent",
	})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Fatalf("PATCH calls = %d, want exactly one", patchCalls.Load())
	}
	if requestCalls.Load() != 3 || tokenSource.calls.Load() != 3 {
		t.Fatalf("requests = %d token calls = %d, want three each", requestCalls.Load(), tokenSource.calls.Load())
	}
	if result.Before.ParentID != "source-parent" || result.After.ParentID != "quarantine-parent" {
		t.Fatalf("unexpected move readback: %+v", result)
	}
	if result.After.Version != "8" || len(result.After.MetadataDigest) != 64 || result.After.MetadataDigest == result.Before.MetadataDigest {
		t.Fatalf("post-move version readback = %+v", result.After)
	}
}

func TestQuarantineHTTPClientMetadataDriftPerformsZeroMutation(t *testing.T) {
	var patchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch {
			patchCalls.Add(1)
		}
		writeDriveFile(t, writer, "source-parent", "8")
	}))
	defer server.Close()
	client, err := newQuarantineHTTPClient(server.Client(), server.URL+"/drive/v3/", "secret-token")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.move(context.Background(), moveRequest{
		Expected:            quarantineObject(),
		DestinationParentID: "quarantine-parent",
	})
	if err == nil || !strings.Contains(err.Error(), "metadata drift") {
		t.Fatalf("Move() error = %v, want metadata drift", err)
	}
	if patchCalls.Load() != 0 {
		t.Fatalf("PATCH calls = %d, want zero", patchCalls.Load())
	}
}

func TestQuarantineHTTPClientAmbiguousMutationNeverRetriesOrLeaksToken(t *testing.T) {
	var patchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeDriveFile(t, writer, "source-parent", "7")
			return
		}
		patchCalls.Add(1)
		http.Error(writer, "provider secret-token internal failure", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := newQuarantineHTTPClient(server.Client(), server.URL+"/drive/v3/", "secret-token")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.move(context.Background(), moveRequest{
		Expected:            quarantineObject(),
		DestinationParentID: "quarantine-parent",
	})
	if !errors.Is(err, ErrSettlementUnknown) {
		t.Fatalf("Move() error = %v, want ErrSettlementUnknown", err)
	}
	if patchCalls.Load() != 1 {
		t.Fatalf("PATCH calls = %d, want exactly one", patchCalls.Load())
	}
	if strings.Contains(fmt.Sprint(err), "secret-token") {
		t.Fatalf("error leaked bearer material: %v", err)
	}
}

func TestQuarantineHTTPClientBoundsTimeoutAndRejectsCustomTransport(t *testing.T) {
	client, err := newQuarantineHTTPClient(
		&http.Client{Timeout: time.Hour},
		"https://www.googleapis.com/drive/v3/",
		"secret-token",
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.client.Timeout != 30*time.Second {
		t.Fatalf("bounded timeout = %s", client.client.Timeout)
	}
	if _, err := newQuarantineHTTPClient(
		&http.Client{Transport: nonStandardRoundTripper{}},
		"https://www.googleapis.com/drive/v3/",
		"secret-token",
	); err == nil || !strings.Contains(err.Error(), "standard non-retrying transport") {
		t.Fatalf("custom transport error = %v", err)
	}
}

func TestDecodeFileStateRejectsTrailingJSON(t *testing.T) {
	data := []byte(`{"id":"object-1","name":"object.bin","mimeType":"application/octet-stream","parents":["source-parent"],"trashed":false,"md5Checksum":"abc123","size":"5","modifiedTime":"1970-01-01T00:01:40Z","version":"7","headRevisionId":"revision-1"} {}`)
	if _, err := decodeFileState(data, "object-1"); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing metadata error = %v", err)
	}
}
