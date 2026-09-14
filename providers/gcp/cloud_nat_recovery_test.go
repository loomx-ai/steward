package gcp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCloudNatMutationSettlementIsReadOnlyAndNotDeletionSuccess(t *testing.T) {
	for _, mode := range []string{"running", "done", "failed", "http-error", "expired", "expired-recovered", "expired-other", "denied", "wrong-target", "wrong-request", "wrong-name", "wrong-status", "bad-error", "bad-http-error", "receipt-tampered"} {
		t.Run(mode, func(t *testing.T) {
			r, request, f := cloudNatActionRuntime(t)
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			f.status = "DONE"
			switch mode {
			case "running":
				f.status = "RUNNING"
			case "failed":
				f.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "RESOURCE_IN_USE_BY_ANOTHER_RESOURCE"}}}
			case "http-error":
				f.operation["httpErrorStatusCode"] = 400
				f.operation["httpErrorMessage"] = "native update failed"
			case "denied":
				f.mode = "poll-denied"
			case "wrong-target":
				f.operation["targetId"] = "1002"
			case "wrong-request":
				f.operation["clientOperationId"] = "foreign"
			case "wrong-name":
				f.operation["name"] = "other"
			case "wrong-status":
				f.status = "UNKNOWN"
			case "bad-error":
				f.operation["error"] = nil
			case "bad-http-error":
				f.operation["httpErrorStatusCode"] = "400"
			case "receipt-tampered":
				result.Data["parent_id"] = "1002"
			}
			original := r.transport
			r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.Contains(req.URL.Path, "/operations") {
					t.Fatal("recovery performed more than operation reads", req.Method, req.URL)
				}
				if strings.HasPrefix(mode, "expired") {
					if strings.HasSuffix(req.URL.Path, "/operations") {
						if mode == "expired" {
							return apiResponse(req, 200, `{}`), nil
						}
						data := cloneParameters(f.operation)
						data["status"] = "DONE"
						if mode == "expired-other" {
							data["name"] = "other"
						}
						return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
					}
					return apiResponse(req, 404, `{}`), nil
				}
				return original.RoundTrip(req)
			})
			driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			reader := driver.(contracts.MutationSettlementReader)
			read, err := reader.MutationSettled(t.Context(), request, result)
			good := mode == "done" || mode == "failed" || mode == "http-error" || mode == "expired-recovered"
			uncertain := mode == "running" || mode == "expired"
			if good && (err != nil || !read.Settled || read.Operation != result.ProviderOperationID) || uncertain && (err != nil || read.Settled) || !good && !uncertain && err == nil {
				t.Fatal(read, err)
			}
			if !f.exists || f.deletes != 1 {
				t.Fatal("settlement changed NAT or repeated mutation")
			}
		})
	}
}

func TestCloudNatLostReceiptLookupRequiresCompleteMatchingOperation(t *testing.T) {
	for _, mode := range []string{"done", "running", "failed", "empty", "paged", "duplicate", "partial", "denied", "null-items", "scalar-item", "null-token", "cyclic-token", "foreign-request", "foreign-target", "missing-request", "wrong-region", "missing-name", "numeric-name", "numeric-type", "numeric-error-code", "malformed-error"} {
		t.Run(mode, func(t *testing.T) {
			r, request, f := cloudNatActionRuntime(t)
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			operation := cloneParameters(f.operation)
			operation["status"] = "DONE"
			switch mode {
			case "running":
				operation["status"] = "RUNNING"
			case "failed":
				operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "foreign-request":
				operation["clientOperationId"] = "other"
			case "foreign-target":
				operation["targetId"] = "1002"
			case "missing-request":
				delete(operation, "clientOperationId")
			case "wrong-region":
				operation["region"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/europe-west1"
			case "missing-name":
				delete(operation, "name")
			case "numeric-name":
				operation["name"] = 123
			case "numeric-type":
				operation["operationType"] = 123
			case "numeric-error-code":
				operation["error"] = map[string]any{"errors": []any{map[string]any{"code": 123}}}
			case "malformed-error":
				operation["error"] = map[string]any{"errors": []any{false}}
			}
			pages := 0
			r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Path != "/compute/v1/projects/sample-project/regions/us-central1/operations" {
					t.Fatal("lookup mutated/read another scope", req.Method, req.URL)
				}
				if req.URL.Query().Get("filter") != "clientOperationId = \""+f.requestIDs[0]+"\"" || req.URL.Query().Get("maxResults") != "500" {
					t.Fatal("wrong native lookup", req.URL)
				}
				pages++
				body := map[string]any{"items": []any{operation}}
				switch mode {
				case "empty":
					delete(body, "items")
				case "denied":
					return apiResponse(req, 403, `{}`), nil
				case "duplicate":
					body["items"] = []any{operation, operation}
				case "partial":
					body["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
				case "null-items":
					body["items"] = nil
				case "scalar-item":
					body["items"] = []any{42}
				case "null-token":
					body["nextPageToken"] = nil
				case "cyclic-token":
					body["items"] = []any{}
					body["nextPageToken"] = "next"
				case "paged":
					if pages == 1 {
						body["items"] = []any{}
						body["nextPageToken"] = "next"
					} else if req.URL.Query().Get("pageToken") != "next" {
						t.Fatal(req.URL)
					}
				}
				return dataformResponse(req, 200, body), nil
			})
			driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			read, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, contracts.ActionResult{})
			good := mode == "done" || mode == "failed" || mode == "paged"
			uncertain := mode == "running" || mode == "empty"
			if good && (err != nil || !read.Settled || read.Operation != result.ProviderOperationID) || uncertain && (err != nil || read.Settled) || !good && !uncertain && err == nil {
				t.Fatal(read, err)
			}
			if mode == "paged" && pages != 2 || f.deletes != 1 {
				t.Fatal(pages, f.deletes)
			}
		})
	}
}
