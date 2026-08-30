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

func quarantineObject() cleanup.Object {
	return cleanup.Object{
		ID:          "object-1",
		ParentID:    "source-parent",
		ObjectType:  cleanup.ObjectTypeFile,
		ContentHash: "abc123",
		Size:        5,
		Version:     "7",
		Generation:  "revision-1",
		ETag:        `"etag-1"`,
		ModifiedAt:  time.Unix(100, 0).UTC(),
	}
}

func writeDriveFile(t *testing.T, writer http.ResponseWriter, parent, version, etag string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("ETag", etag)
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"id":             "object-1",
		"parents":        []string{parent},
		"trashed":        false,
		"md5Checksum":    "abc123",
		"size":           "5",
		"modifiedTime":   time.Unix(100, 0).UTC().Format(time.RFC3339Nano),
		"version":        version,
		"headRevisionId": "revision-1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineHTTPClientMovesExactIDOnceAndReadsBack(t *testing.T) {
	var patchCalls atomic.Int32
	parent := "source-parent"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.URL.Path != "/drive/v3/files/object-1" {
			t.Errorf("path = %q", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		switch request.Method {
		case http.MethodGet:
			version, etag := "7", `"etag-1"`
			if parent == "quarantine-parent" {
				version, etag = "8", `"etag-2"`
			}
			writeDriveFile(t, writer, parent, version, etag)
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
			writeDriveFile(t, writer, parent, "8", `"etag-2"`)
		default:
			http.Error(writer, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client, err := newQuarantineHTTPClient(server.Client(), server.URL+"/drive/v3/", "secret-token")
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
	if result.Before.ParentID != "source-parent" || result.After.ParentID != "quarantine-parent" {
		t.Fatalf("unexpected move readback: %+v", result)
	}
	if result.After.Version != "8" || result.After.ETag != `"etag-2"` {
		t.Fatalf("post-move version readback = %+v", result.After)
	}
}

func TestQuarantineHTTPClientMetadataDriftPerformsZeroMutation(t *testing.T) {
	var patchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch {
			patchCalls.Add(1)
		}
		writeDriveFile(t, writer, "source-parent", "7", `"drifted-etag"`)
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
			writeDriveFile(t, writer, "source-parent", "7", `"etag-1"`)
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
	data := []byte(`{"id":"object-1","parents":["source-parent"],"trashed":false,"md5Checksum":"abc123","size":"5","modifiedTime":"1970-01-01T00:01:40Z","version":"7","headRevisionId":"revision-1"} {}`)
	headers := http.Header{"ETag": {`"etag-1"`}}
	if _, err := decodeFileState(data, headers, "object-1"); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing metadata error = %v", err)
	}
}
