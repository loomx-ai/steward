package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRoutePolicySettlementCoversUnrecordedDeleteAfterDetach(t *testing.T) {
	for _, mode := range []string{"detach-receipt", "delete-receipt", "lost-receipt", "expired-receipts", "failed-delete", "failed-detach", "running-detach", "running-delete", "missing-detach", "missing-delete", "foreign-delete", "foreign-detach", "missing-echo", "partial", "duplicate", "changed-review", "changed-receipt", "unreviewed-detach"} {
		t.Run(mode, func(t *testing.T) {
			r, requests, f := multiPolicyRuntime(t)
			request := requests[0]
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			// Wait actually issues the second mutation; deliberately discard its cursor.
			next, err := driver.Wait(t.Context(), request, result)
			if err != nil || next.Data["phase"] != "route_policy_delete" || len(f.patches) != 1 || len(f.deletes) != 1 {
				t.Fatal(next, err)
			}
			initial := text(f.operations["operation-1"]["clientOperationId"])
			final := text(f.operations["operation-2"]["clientOperationId"])
			if initial == final {
				t.Fatal("phase UUID collision")
			}
			switch mode {
			case "delete-receipt", "missing-echo", "expired-receipts":
				result.Data = next.Data
			case "lost-receipt":
				result = contracts.ActionResult{}
			case "failed-delete":
				f.operations["operation-2"]["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "failed-detach":
				f.operations["operation-1"]["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "running-detach":
				f.operations["operation-1"]["status"] = "RUNNING"
			case "running-delete":
				f.operations["operation-2"]["status"] = "RUNNING"
			case "missing-detach":
				delete(f.operations, "operation-1")
			case "missing-delete":
				delete(f.operations, "operation-2")
			case "foreign-delete":
				f.operations["operation-2"]["targetId"] = "9999"
			case "foreign-detach":
				f.operations["operation-1"]["clientOperationId"] = final
			case "changed-review":
				request.Asset.Normalized[routePolicyRouterID] = "9999"
			case "changed-receipt":
				result.Data["connection"] = "other"
			case "unreviewed-detach":
				a := driver.(*action)
				request.Asset.Normalized[routePolicyPeers] = []any{}
				result.Data = a.routerComponentStage(request, routePolicyDetach, result.ProviderOperationID, result.ProviderOperationID, "patch")
			}
			if mode == "missing-echo" {
				delete(f.operations["operation-1"], "clientOperationId")
			}
			reads := 0
			r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.Contains(req.URL.Path, "/regions/us-central1/operations") {
					t.Fatal("settlement made non-operation read/write", req.Method, req.URL)
				}
				reads++
				if !strings.HasSuffix(req.URL.Path, "/operations") {
					data, ok := f.operations[last(req.URL.Path)]
					if !ok || mode == "expired-receipts" {
						return apiResponse(req, 404, `{}`), nil
					}
					return dataformResponse(req, 200, data), nil
				}
				filter := req.URL.Query().Get("filter")
				if filter != "clientOperationId = \""+initial+"\"" && filter != "clientOperationId = \""+final+"\"" || req.URL.Query().Get("maxResults") != "500" || req.URL.Query().Get("policy") != "" {
					t.Fatal("wrong lookup", req.URL)
				}
				var items []any
				for _, name := range []string{"operation-1", "operation-2"} {
					data := f.operations[name]
					if data != nil && filter == "clientOperationId = \""+text(data["clientOperationId"])+"\"" {
						items = append(items, data)
					}
				}
				body := map[string]any{}
				if len(items) != 0 {
					body["items"] = items
				}
				if mode == "partial" {
					body["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
				}
				if mode == "duplicate" {
					body["items"] = append(items, items...)
				}
				return dataformResponse(req, 200, body), nil
			})
			// Restart from JSON only, with a transport that rejects all cloud mutations.
			raw, _ := json.Marshal(result)
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, result)
			good := mode == "detach-receipt" || mode == "delete-receipt" || mode == "lost-receipt" || mode == "expired-receipts" || mode == "failed-delete" || mode == "failed-detach"
			uncertain := strings.HasPrefix(mode, "running-") || mode == "missing-detach" || mode == "missing-delete"
			if good && (err != nil || !settled.Settled || len(strings.Split(settled.Operation, "\n")) != 2) || uncertain && (err != nil || settled.Settled) || !good && !uncertain && err == nil {
				t.Fatal(settled, err)
			}
			if good && reads < 2 || len(f.patches) != 1 || len(f.deletes) != 1 {
				t.Fatal("missing phase read/repeated mutation", reads)
			}
		})
	}
}

func TestRouterComponentSinglePhaseSettlement(t *testing.T) {
	for _, kind := range []string{routePolicyType, namedSetType} {
		for _, lost := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/receipt", true: "/lost"}[lost], func(t *testing.T) {
				r, request, f := routerComponentActionRuntime(t, kind)
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				expected := result.ProviderOperationID
				if lost {
					result = contracts.ActionResult{}
				}
				f.operation["status"] = "DONE"
				r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != "GET" || !strings.Contains(req.URL.Path, "/operations") {
						t.Fatal(req.Method, req.URL)
					}
					if strings.HasSuffix(req.URL.Path, "/operations") {
						if !lost || req.URL.Query().Get("filter") != "clientOperationId = \""+f.requestIDs[0]+"\"" || req.URL.Query().Get("policy") != "" || req.URL.Query().Get("namedSet") != "" {
							t.Fatal(req.URL)
						}
						return dataformResponse(req, 200, map[string]any{"items": []any{f.operation}}), nil
					}
					return dataformResponse(req, 200, f.operation), nil
				})
				driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, result)
				if err != nil || !settled.Settled || settled.Operation != expected || f.deletes != 1 || !f.exists {
					t.Fatal(settled, err)
				}
			})
		}
	}
}
