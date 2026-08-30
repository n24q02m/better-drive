package driveapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
)

func TestInventoryClientCapturesEveryDeclaredRootPageAndNestedFolder(t *testing.T) {
	modified := time.Unix(100, 0).UTC().Format(time.RFC3339Nano)
	files := map[string]map[string]any{
		"folder-1": driveInventoryTestFile("folder-1", "root-1", "nested", driveFolderMIMEType, "", "0", modified, "1", "folder-generation"),
		"file-1":   driveInventoryTestFile("file-1", "root-1", "alpha.bin", "application/octet-stream", strings.Repeat("a", 32), "10", modified, "1", "generation-1"),
		"file-2":   driveInventoryTestFile("file-2", "root-1", "beta.bin", "application/octet-stream", strings.Repeat("b", 32), "20", modified, "1", "generation-2"),
		"file-3":   driveInventoryTestFile("file-3", "folder-1", "nested.bin", "application/octet-stream", strings.Repeat("c", 32), "30", modified, "1", "generation-3"),
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer inventory-token" {
			http.Error(writer, "missing token", http.StatusUnauthorized)
			return
		}
		if request.URL.Path == "/files" {
			parent := inventoryQueryParent(request.URL.Query().Get("q"))
			token := request.URL.Query().Get("pageToken")
			response := map[string]any{"files": []any{}}
			switch parent + ":" + token {
			case "root-1:":
				response["files"] = []any{files["folder-1"], files["file-1"]}
				response["nextPageToken"] = "root-page-2"
			case "root-1:root-page-2":
				response["files"] = []any{files["file-2"]}
			case "folder-1:":
				response["files"] = []any{files["file-3"]}
			default:
				http.Error(writer, "unexpected list", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(response)
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/files/")
		file, ok := files[id]
		if !ok || id == request.URL.Path {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(file)
	}))
	defer server.Close()

	plan := inventoryTestPlan(t, []string{"folder-1", "file-1", "file-2", "file-3"})
	client, err := newInventoryClient(server.Client(), server.URL+"/", "inventory-token")
	if err != nil {
		t.Fatal(err)
	}
	rootSet, aggregate, err := client.Capture(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootSet.Roots) != 1 || rootSet.Roots[0].ExpectedPages != 3 || len(rootSet.Roots[0].Pages) != 3 {
		t.Fatalf("captured roots/pages = %+v", rootSet.Roots)
	}
	if aggregate.Status != cleanup.InventoryComplete || aggregate.RootCount != 1 || aggregate.PageCount != 3 || aggregate.ObjectCount != 4 || aggregate.ByteCount != 60 {
		t.Fatalf("inventory aggregate = %+v", aggregate)
	}
	for _, object := range aggregate.Objects {
		if len(object.MetadataDigest) != 64 {
			t.Fatalf("object %q metadata digest = %q", object.ID, object.MetadataDigest)
		}
	}
	var folder cleanup.Object
	for _, object := range aggregate.Objects {
		if object.ID == "folder-1" {
			folder = object
			break
		}
	}
	if folder.ID != "folder-1" || !folder.ChildrenComplete || !folder.SubtreeComplete || folder.ChildCount != 1 || folder.SubtreeObjectCount != 1 {
		t.Fatalf("folder traversal metadata = %+v", folder)
	}
}

func TestInventoryClientCapturesUnboundProviderObjectAsUnknown(t *testing.T) {
	modified := time.Unix(100, 0).UTC().Format(time.RFC3339Nano)
	file := driveInventoryTestFile("file-extra", "root-1", "extra.bin", "application/octet-stream", strings.Repeat("d", 32), "1", modified, "1", "generation-extra")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/files" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []any{file}})
			return
		}
		_ = json.NewEncoder(writer).Encode(file)
	}))
	defer server.Close()

	plan := inventoryTestPlan(t, nil)
	client, err := newInventoryClient(server.Client(), server.URL+"/", "inventory-token")
	if err != nil {
		t.Fatal(err)
	}
	_, aggregate, err := client.Capture(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Objects) != 1 || aggregate.Objects[0].Class != cleanup.ClassUnknown ||
		aggregate.Objects[0].OwnershipMarker != "" || aggregate.Objects[0].RestoreEvidence != "" {
		t.Fatalf("unbound provider object = %+v", aggregate.Objects)
	}
}

func TestInventoryClientCapturesProviderNativeObjectAsUnknown(t *testing.T) {
	modified := time.Unix(100, 0).UTC().Format(time.RFC3339Nano)
	file := driveInventoryTestFile(
		"shortcut-1", "root-1", "native shortcut", "application/vnd.google-apps.shortcut",
		"", "", modified, "2", "",
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/files" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"files": []any{file}})
			return
		}
		_ = json.NewEncoder(writer).Encode(file)
	}))
	defer server.Close()

	plan := inventoryTestPlan(t, nil)
	client, err := newInventoryClient(server.Client(), server.URL+"/", "inventory-token")
	if err != nil {
		t.Fatal(err)
	}
	_, aggregate, err := client.Capture(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Objects) != 1 || aggregate.Objects[0].ObjectType != cleanup.ObjectTypeProviderNative ||
		aggregate.Objects[0].Class != cleanup.ClassUnknown || aggregate.Objects[0].ContentHash != "" ||
		aggregate.Objects[0].Size != 0 || len(aggregate.Objects[0].MetadataDigest) != 64 {
		t.Fatalf("provider-native inventory object = %+v", aggregate.Objects)
	}
}

func TestInventoryClientUsesDeclaredSharedDriveCorpora(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("corpora") != "drive" || query.Get("driveId") != "shared-drive-1" {
			http.Error(writer, "shared drive scope is missing", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"files": []any{}})
	}))
	defer server.Close()
	plan, err := FreezeInventoryPlan(InventoryPlan{
		SchemaVersion: CurrentInventoryPlanSchemaVersion,
		AccountID:     "account-1",
		Roots: []InventoryRoot{{
			Provider:  "drive",
			AccountID: "account-1",
			RootID:    "root-1",
			DriveID:   "shared-drive-1",
			Namespace: "backup",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newInventoryClient(server.Client(), server.URL+"/", "inventory-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Capture(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestInventoryClientRejectsBindingForMissingProviderObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"files": []any{}})
	}))
	defer server.Close()
	plan := inventoryTestPlan(t, []string{"missing-file"})
	client, err := newInventoryClient(server.Client(), server.URL+"/", "inventory-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Capture(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "did not resolve") {
		t.Fatalf("missing provider object binding error = %v", err)
	}
}

func inventoryTestPlan(t *testing.T, objectIDs []string) InventoryPlan {
	t.Helper()
	bindings := make([]InventoryBinding, 0, len(objectIDs))
	for _, id := range objectIDs {
		bindings = append(bindings, InventoryBinding{
			Provider:        "drive",
			AccountID:       "account-1",
			RootID:          "root-1",
			Namespace:       "backup",
			ObjectID:        id,
			Class:           cleanup.ClassExpectedFixture,
			OwnershipMarker: "fixture-owner",
			RestoreEvidence: "fixture-restore",
		})
	}
	plan, err := FreezeInventoryPlan(InventoryPlan{
		SchemaVersion: CurrentInventoryPlanSchemaVersion,
		AccountID:     "account-1",
		Roots: []InventoryRoot{{
			Provider:  "drive",
			AccountID: "account-1",
			RootID:    "root-1",
			Namespace: "backup",
		}},
		Bindings: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func driveInventoryTestFile(id, parent, name, mimeType, md5Checksum, size, modifiedTime, version, generation string) map[string]any {
	return map[string]any{
		"id":             id,
		"name":           name,
		"mimeType":       mimeType,
		"parents":        []string{parent},
		"trashed":        false,
		"md5Checksum":    md5Checksum,
		"size":           size,
		"modifiedTime":   modifiedTime,
		"version":        version,
		"headRevisionId": generation,
	}
}

func inventoryQueryParent(query string) string {
	var parent string
	_, _ = fmt.Sscanf(query, "'%s' in parents", &parent)
	return strings.TrimSuffix(parent, "'")
}
