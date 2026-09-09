package azure

import (
	"context"
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestSearchDeleteFailuresAndRestartedOperationIdentity(t *testing.T) {
	for _, kind := range []string{searchLinkType} {
		for _, mode := range []string{"async", "forbidden", "conflict", "partial-delete", "failed", "canceled", "partial-poll", "wrong-receipt", "wrong-operation", "wrong-id", "wrong-name", "wrong-resource", "wrong-region", "wrong-subscription", "wrong-provider"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := searchScenario(t)
				target := cdnAsset(t, assets, kind)
				// Native product discovery inherits the proxy's root location.
				target.Location = resourceRegion(s.records[redisParentID(target.Identity.NativeID)])
				request := contracts.ActionRequest{Action: "delete", Asset: target}
				version := "2025-05-01"
				operation := "/subscriptions/" + testSubscription + "/providers/Microsoft.Search/locations/" + target.Location + "/operationStatuses/delete-link"
				endpoint := apiURL(operation, version)
				state := "InProgress"
				polls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
						if mode == "forbidden" || mode == "conflict" || mode == "partial-delete" {
							status := 403
							if mode == "conflict" {
								status = 409
							} else if mode == "partial-delete" {
								status = 206
							}
							return jsonResponse(status, map[string]any{}, nil), true
						}
						returned := endpoint
						switch mode {
						case "wrong-region":
							returned = apiURL(strings.Replace(operation, "/locations/"+target.Location+"/", "/locations/unrelated/", 1), version)
						case "wrong-subscription":
							returned = strings.Replace(endpoint, testSubscription, testTenant, 1)
						case "wrong-provider":
							returned = strings.Replace(endpoint, "Microsoft.Search", "Microsoft.Other", 1)
						}
						return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {returned}}), true
					}
					if strings.EqualFold(req.URL.Path, operation) {
						polls++
						body := map[string]any{"id": operation, "name": last(operation), "resourceId": target.Identity.NativeID, "status": state}
						switch mode {
						case "partial-poll":
							return jsonResponse(206, body, nil), true
						case "wrong-id":
							body["id"] = operation + "other"
						case "wrong-name":
							body["name"] = "another-operation"
						case "wrong-resource":
							body["resourceId"] = target.Identity.NativeID + "other"
						}
						return jsonResponse(200, body, nil), true
					}
					return nil, false
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				result, err := driver.Execute(context.Background(), request)
				if slices.Contains([]string{"forbidden", "conflict", "partial-delete", "wrong-region", "wrong-subscription", "wrong-provider"}, mode) {
					if err == nil || len(s.deletes) != 1 || polls != 0 {
						t.Fatal("invalid native delete response accepted", err)
					}
					return
				}
				if err != nil {
					t.Fatal("native asynchronous delete", err)
				}
				payload, _ := json.Marshal(request)
				json.Unmarshal(payload, &request)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
				switch mode {
				case "wrong-receipt":
					result.Data["search_operation_binding"] = "another-resource"
				case "wrong-operation":
					u, _ := url.Parse(result.ProviderOperationID)
					u.Path += "other"
					result.ProviderOperationID = u.String()
				case "failed":
					state = "Failed"
				case "canceled":
					state = "Canceled"
				}
				wait, err := driver.Wait(context.Background(), request, result)
				if mode != "async" {
					if err == nil || wait.Done || ((mode == "wrong-receipt" || mode == "wrong-operation") && polls != 0) {
						t.Fatal("invalid resumed Search operation accepted", mode, err)
					}
					return
				}
				if err != nil || wait.Done || polls != 1 {
					t.Fatal("pending Search operation", err)
				}
				state = "Succeeded"
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatal("successful Search operation hid a live target", err)
				}
				s.gone[target.Identity.NativeID] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("completed resumed Search operation", err)
				}
			})
		}
	}
}

func TestSearchRetainedAndUnreviewedChildrenBlockCleanup(t *testing.T) {
	s, r, assets := searchScenario(t)
	target := assets[0]
	request, input := dnsRequest(t, r, assets, target)
	children := append(slices.Clone(request.LifecycleImpacts), request.PrerequisiteDeletions...)
	if len(children) != 3 {
		t.Fatal("Search parent lost its reviewed children")
	}
	for _, child := range children {
		input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{child.Asset.Identity.NativeID}}}
		retained, err := plan.Solve(input)
		if err != nil || len(retained.Blockers) == 0 {
			t.Fatal("retained Search child allowed parent deletion", child.Asset.Identity.NativeType, err)
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatal("unreviewed Search children allowed parent deletion")
	}
	// Parent absence does not prove its read-only managed configuration is gone.
	for _, child := range request.PrerequisiteDeletions {
		s.gone[child.Asset.Identity.NativeID] = true
	}
	s.gone[target.Identity.NativeID] = true
	checked, err := driver.Preflight(context.Background(), request)
	if err != nil || checked.Absent {
		t.Fatal("Search absence bypassed managed child readback", checked, err)
	}
	config := cdnAsset(t, assets, searchPerimeterType)
	s.status[config.Identity.NativeID] = 403
	if _, err := driver.Preflight(context.Background(), request); err == nil {
		t.Fatal("Search hid unreadable managed child")
	}
	delete(s.status, config.Identity.NativeID)
	s.gone[config.Identity.NativeID] = true
	checked, err = driver.Preflight(context.Background(), request)
	if err != nil || !checked.Absent || !checked.Allowed {
		t.Fatal("Search final absence failed", checked, err)
	}
}

func TestSearchPagedChildrenBindParentAndRejectIncompleteCollections(t *testing.T) {
	for _, mode := range []string{"complete", "cycle", "version", "host", "subscription", "collection", "parent-change", "partial", "denied", "missing-array", "duplicate", "foreign-child", "wrong-region"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := searchScenario(t)
			target := cdnAsset(t, assets, searchConnectionType)
			parentID := redisParentID(target.Identity.NativeID)
			raw := s.records[target.Identity.NativeID]
			payload, _ := json.Marshal(raw)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = target.Identity.NativeID+"2", last(target.Identity.NativeID)+"2"
			s.add(second, "2025-05-01")
			collection := parentID + "/privateendpointconnections"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				if mode == "partial" || mode == "denied" {
					status := 206
					if mode == "denied" {
						status = 403
					}
					return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
				}
				if mode == "missing-array" {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if mode == "duplicate" {
					return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
				}
				if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
				}
				next := apiURL(collection, "2025-05-01") + "&%24skiptoken=second"
				switch mode {
				case "version":
					next = strings.Replace(next, "2025-05-01", "1900-01-01", 1)
				case "host":
					next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
				case "subscription":
					next = strings.Replace(next, testSubscription, testTenant, 1)
				case "collection":
					next = strings.Replace(next, "/privateendpointconnections", "/sharedprivatelinkresources", 1)
				case "foreign-child":
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": strings.Replace(target.Identity.NativeID, "/mysearchservice/", "/foreignservice/", 1), "type": searchConnectionType}}}, nil), true
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
			}
			request := productRequest(r, searchConnectionType)
			if mode == "wrong-region" {
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "unrelatedregion"}
			}
			first, err := r.List(context.Background(), request)
			if !slices.Contains([]string{"complete", "cycle", "parent-change", "wrong-region"}, mode) {
				if err == nil {
					t.Fatal("incomplete Search collection accepted", mode)
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatal("first native Search page", err)
			}
			if mode == "parent-change" {
				object(s.records[parentID]["properties"])["replicaCount"] = 7
			}
			request.Cursor = first.NextCursor
			final, err := r.List(context.Background(), request)
			if mode == "cycle" || mode == "parent-change" {
				if err == nil {
					t.Fatal("changed Search continuation accepted")
				}
				return
			}
			count := 2
			if mode == "wrong-region" {
				count = 0
			}
			if err != nil || !final.Complete || len(first.Items)+len(final.Items) != count {
				t.Fatal("native Search paging", err)
			}
		})
	}
}
