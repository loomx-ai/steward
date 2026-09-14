package gcp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRouterMutationSettlementCurrentAndPriorReceipts(t *testing.T) {
	for _, prior := range []bool{false, true} {
		for _, mode := range []string{"done", "running", "failed", "expired", "missing", "lost", "foreign-target", "foreign-request", "missing-echo", "wrong-type", "wrong-name", "wrong-region", "denied", "partial", "receipt-changed", "review-changed"} {
			t.Run(map[bool]string{false: "current/", true: "prior/"}[prior]+mode, func(t *testing.T) {
				r, values, f := routerCascadeRuntime(t)
				request := prepareRouterCascadeDeletion(t, r, values, f)
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				var result contracts.ActionResult
				if prior {
					// Reproduce the former generic driver's native DELETE and UUID, then retain
					// only its historical operation-only receipt and numeric Router inventory.
					a := driver.(*action)
					data, err := a.client.request(t.Context(), "DELETE", a.endpoint+"?requestId="+googleRequestID(request.IdempotencyKey), nil)
					if err != nil {
						t.Fatal(err)
					}
					operation, err := a.routerComponentOperationURL(text(data["name"]))
					if err != nil {
						t.Fatal(err)
					}
					result = contracts.ActionResult{ProviderOperationID: operation, Data: map[string]any{"operation": operation}}
					delete(request.Asset.Normalized, routerReview)
					delete(request.Asset.Normalized, routerBaseReview)
				} else {
					result, err = driver.Execute(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
				}
				expected := result.ProviderOperationID
				operation := f.operations["router-delete"]
				uuid := text(operation["clientOperationId"])
				switch mode {
				case "running":
					operation["status"] = "RUNNING"
				case "failed":
					operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
				case "lost":
					result = contracts.ActionResult{}
				case "foreign-target":
					operation["targetId"] = "9999"
				case "foreign-request":
					operation["clientOperationId"] = "foreign"
				case "missing-echo":
					delete(operation, "clientOperationId")
				case "wrong-type":
					operation["operationType"] = "patch"
				case "wrong-name":
					operation["name"] = "other"
				case "wrong-region":
					operation["region"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/europe-west1"
				case "receipt-changed":
					result.Data["operation"] = "https://example.com/operations/router-delete"
				case "review-changed":
					request.Asset.Normalized["id"] = "9999"
				}
				reads := 0
				r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != "GET" || !strings.Contains(req.URL.Path, "/regions/us-central1/operations") {
						t.Fatal("recovery made resource read/mutation", req.Method, req.URL)
					}
					reads++
					if mode == "denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if strings.HasSuffix(req.URL.Path, "/operations") {
						if req.URL.Query().Get("filter") != "clientOperationId = \""+uuid+"\"" {
							t.Fatal("wrong original request UUID", req.URL)
						}
						if mode == "missing" {
							return apiResponse(req, 200, `{}`), nil
						}
						body := map[string]any{"items": []any{operation}}
						if mode == "partial" {
							body["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
						}
						return dataformResponse(req, 200, body), nil
					}
					if mode == "expired" || mode == "missing" || mode == "partial" {
						return apiResponse(req, 404, `{}`), nil
					}
					return dataformResponse(req, 200, operation), nil
				})
				driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, result)
				good := mode == "done" || mode == "failed" || mode == "expired" || !prior && (mode == "lost" || mode == "missing-echo")
				uncertain := mode == "running" || mode == "missing"
				if good && (err != nil || !settled.Settled || settled.Operation != expected) || uncertain && (err != nil || settled.Settled) || !good && !uncertain && err == nil {
					t.Fatal(settled, err)
				}
				if good && reads == 0 || f.routerDeletes != 1 {
					t.Fatal("missing native proof/repeated delete")
				}
			})
		}
	}
}
