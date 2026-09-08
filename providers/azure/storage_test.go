package azure

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestBlobEmptyCheckIncludesVersionsAndRejectsAmbiguousResponses(t *testing.T) {
	id := strings.ToLower(resourceID(storageType, "teststorage")) + "/blobservices/default/containers/testcontainer"
	for _, test := range []struct {
		name, xml   string
		status      int
		empty, fail bool
	}{
		{"empty", `<EnumerationResults><Blobs/><NextMarker/></EnumerationResults>`, 200, true, false},
		{"snapshot", `<EnumerationResults><Blobs><Blob><Name>file</Name><Snapshot>2026-01-01</Snapshot></Blob></Blobs><NextMarker/></EnumerationResults>`, 200, false, false},
		{"version", `<EnumerationResults><Blobs><Blob><Name>file</Name><VersionId>old</VersionId></Blob></Blobs><NextMarker/></EnumerationResults>`, 200, false, false},
		{"more pages", `<EnumerationResults><Blobs/><NextMarker>two</NextMarker></EnumerationResults>`, 200, false, false},
		{"missing marker", `<EnumerationResults><Blobs/></EnumerationResults>`, 200, false, true},
		{"malformed", `<EnumerationResults>`, 200, false, true},
		{"denied", `<Error><Code>AuthorizationFailure</Code></Error>`, 403, false, true},
		{"data plane absent", `<Error><Code>ContainerNotFound</Code></Error>`, 404, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := directClient(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "management.azure.com" {
					return jsonResponse(200, map[string]any{"properties": map[string]any{"primaryEndpoints": map[string]any{"blob": "https://teststorage.blob.core.windows.net/"}}}, nil), nil
				}
				if req.URL.Host != "teststorage.blob.core.windows.net" || !strings.Contains(req.URL.Query().Get("include"), "versions,snapshots,deleted") || req.Header.Get("x-ms-version") == "" {
					t.Fatalf("incorrect data plane request %s", req.URL)
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"X-Ms-Request-Id": {"blob-request"}}, Body: io.NopCloser(strings.NewReader(test.xml))}, nil
			})
			empty, err := c.blobContainerEmpty(context.Background(), id)
			if empty != test.empty || (err != nil) != test.fail {
				t.Fatalf("empty=%v error=%v", empty, err)
			}
			if test.status == 404 {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorConflict || call.Provider.RequestID != "blob-request" {
					t.Fatalf("ambiguous data plane absence=%v", err)
				}
			}
		})
	}
}

func TestStorageAccountChecksAllDataServicesAndSoftDeletedChildren(t *testing.T) {
	for _, child := range []string{"", "containers", "shares", "queues", "tables"} {
		t.Run("nonempty_"+child, func(t *testing.T) {
			calls := map[string]bool{}
			c := directClient(func(req *http.Request) (*http.Response, error) {
				leaf := last(req.URL.Path)
				calls[leaf] = true
				if (leaf == "containers" || leaf == "shares") && req.URL.Query().Get("$include") != "deleted" {
					t.Error("soft-deleted child omitted")
				}
				values := []any{}
				if child == leaf {
					values = append(values, map[string]any{"name": "retained"})
				}
				return jsonResponse(200, map[string]any{"value": values}, nil), nil
			})
			empty, err := c.storageAccountEmpty(context.Background(), strings.ToLower(resourceID(storageType, "teststorage")), map[string]any{"kind": "StorageV2"})
			if err != nil || empty != (child == "") {
				t.Fatalf("empty=%v err=%v", empty, err)
			}
			if child == "" && len(calls) != 4 {
				t.Fatalf("services checked=%v", calls)
			}
		})
	}
}
