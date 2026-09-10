package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func apimRevisionScenario(t *testing.T, kind string) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	current := cdnAsset(t, assets, kind)
	raw := apimExample(t, "ApiManagementGetApiRevision.json")
	id := current.Identity.NativeID + ";rev=3"
	raw["id"], raw["name"], raw["type"] = id, last(id), kind
	raw["_apim_header_etag"] = `"revision-three"`
	s.add(raw, apimVersion)
	for _, childKind := range apimOwnedKinds(kind) {
		collection := id + "/" + strings.ToLower(last(childKind))
		s.lists[collection], s.version[collection] = []any{}, apimVersion
	}
	s.lists[current.Identity.NativeID+"/revisions"] = append(s.lists[current.Identity.NativeID+"/revisions"], map[string]any{"apiId": "/apis/" + last(id), "apiRevision": "3", "isCurrent": false, "isOnline": false})
	revision := dnsAsset(t, r, raw)
	assets = append(assets, revision)
	return s, r, assets, current, revision
}

func TestAPIMRevisionsUseNativeMetadataAndDistinctAPIIdentities(t *testing.T) {
	for _, kind := range []string{apimAPIType, apimWorkspaceType + "/apis"} {
		for _, mode := range []string{"logical-list", "explicit-current", "paginated", "orphan-revision", "revision-read-denied", "index-read-denied", "ambiguous-current", "mismatched-revision", "changed-index", "foreign-next-page"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, _, current, revision := apimRevisionScenario(t, kind)
				collection := redisParentID(current.Identity.NativeID) + "/apis"
				index := current.Identity.NativeID + "/revisions"
				base := s.handle
				reads := 0
				switch mode {
				case "explicit-current":
					listed := maps.Clone(s.records[current.Identity.NativeID])
					listed["id"], listed["name"] = current.Identity.NativeID+";rev=1", last(current.Identity.NativeID)+";rev=1"
					s.lists[collection] = []any{listed, s.records[revision.Identity.NativeID]}
				case "orphan-revision":
					s.gone[current.Identity.NativeID] = true
					s.lists[collection] = []any{s.records[revision.Identity.NativeID]}
				case "revision-read-denied":
					s.status[revision.Identity.NativeID] = 403
				case "index-read-denied":
					s.status[index] = 403
				case "ambiguous-current":
					object(s.lists[index][1])["isCurrent"] = true
				case "mismatched-revision":
					object(s.records[revision.Identity.NativeID]["properties"])["apiRevision"] = "4"
				case "paginated", "changed-index", "foreign-next-page":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, index) {
							reads++
							if mode == "paginated" {
								if req.URL.Query().Get("$skip") == "1" {
									return jsonResponse(200, map[string]any{"value": s.lists[index][1:]}, nil), true
								}
								return jsonResponse(200, map[string]any{"value": s.lists[index][:1], "nextLink": "https://management.azure.com" + index + "?api-version=" + apimVersion + "&$skip=1"}, nil), true
							}
							if mode == "foreign-next-page" {
								return jsonResponse(200, map[string]any{"value": s.lists[index], "nextLink": "https://management.azure.com" + redisParentID(current.Identity.NativeID) + "/apis/unreviewed/revisions?api-version=" + apimVersion}, nil), true
							}
							if reads == 2 {
								object(s.lists[index][1])["isOnline"] = true
							}
						}
						return base(req)
					}
				}
				batch, err := r.List(context.Background(), productRequest(r, kind))
				if !slices.Contains([]string{"logical-list", "explicit-current", "paginated", "orphan-revision"}, mode) {
					if err == nil {
						t.Fatal("incomplete or changed revision inventory accepted", batch)
					}
					return
				}
				want := 2
				if mode == "orphan-revision" {
					want = 1
				}
				if err != nil || !batch.Complete || len(batch.Items) != want {
					t.Fatal("revision inventory", len(batch.Items), err)
				}
				found := false
				for _, item := range batch.Items {
					if item.NativeID == revision.Identity.NativeID {
						found = item.Normalized["apiRevision"] == "3" && slices.Contains(stringValues(item.Normalized["_apim_references"]), current.Identity.NativeID)
					}
				}
				if !found {
					t.Fatal("non-current revision was hidden or merged with alias")
				}
				if mode == "orphan-revision" {
					driver, _ := r.ResolveAction(context.Background(), "connection", revision)
					result, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: revision, Action: "delete"})
					if err != nil {
						t.Fatal("orphan revision cannot be cleaned up", err)
					}
					waited, err := driver.Wait(context.Background(), contracts.ActionRequest{Asset: revision, Action: "delete"}, result)
					if err != nil || !waited.Done || len(s.deletes) != 1 || s.deletes[0] != revision.Identity.NativeID {
						t.Fatal("orphan revision cleanup", waited, err, s.deletes)
					}
				}
			})
		}
	}
}

func TestAPIMRevisionInventoryPreservesNativeListRequestIDs(t *testing.T) {
	for _, mode := range []string{"single", "empty", "paged"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			api := cdnAsset(t, assets, apimAPIType)
			path := redisParentID(api.Identity.NativeID) + "/apis"
			original := s.handle
			if mode == "empty" {
				s.lists[path] = []any{}
				s.gone[api.Identity.NativeID] = true
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, path) {
					if mode == "paged" && req.URL.Query().Get("$skip") == "" {
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://management.azure.com" + path + "?api-version=" + apimVersion + "&$skip=1"}, http.Header{"X-Ms-Request-Id": {"apim-first-page"}}), true
					}
					return jsonResponse(200, map[string]any{"value": s.lists[path]}, http.Header{"X-Ms-Request-Id": {"apim-list-request"}}), true
				}
				return original(req)
			}
			batch, err := r.List(context.Background(), productRequest(r, apimAPIType))
			if err != nil || !batch.Complete || batch.RequestID != "apim-list-request" {
				t.Fatal("APIM aggregate inventory lost native list provenance", batch.RequestID, err)
			}
		})
	}
}

func TestAPIMRevisionAndSubscriptionPrerequisitesAreReviewed(t *testing.T) {
	for _, targetKind := range []string{apimAPIType, apimWorkspaceType + "/apis", apimServiceType + "/products", apimWorkspaceType + "/products", apimServiceType + "/users", apimServiceType + "/apiVersionSets", apimWorkspaceType + "/apiVersionSets"} {
		t.Run(targetKind, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			if strings.EqualFold(last(targetKind), "apis") {
				s, r, assets, _, _ = apimRevisionScenario(t, targetKind)
			}
			target := cdnAsset(t, assets, targetKind)
			namespaceKind := apimServiceType
			if strings.Contains(targetKind, "/workspaces/") {
				namespaceKind = apimWorkspaceType
			}
			referrerKind := namespaceKind + "/subscriptions"
			field := "scope"
			if last(targetKind) == "users" {
				field = "ownerId"
			}
			if last(targetKind) == "apiVersionSets" {
				referrerKind, field = namespaceKind+"/apis", "apiVersionSetId"
			}
			referrer := cdnAsset(t, assets, referrerKind)
			object(s.records[referrer.Identity.NativeID]["properties"])[field] = target.Identity.NativeID
			for i := range assets {
				if assets[i].ID == referrer.ID {
					assets[i] = dnsAsset(t, r, s.records[referrer.Identity.NativeID])
				}
			}
			// Changing an API also changes the ancestor snapshot of its children.
			for i := range assets {
				if strings.HasPrefix(assets[i].Identity.NativeID, referrer.Identity.NativeID+"/") {
					assets[i] = dnsAsset(t, r, s.records[assets[i].Identity.NativeID])
				}
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(request.PrerequisiteDeletions) == 0 {
				t.Fatal("missing APIM prerequisites", err)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("referenced APIM resource deleted")
			}
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						selected = value
					}
				}
				stepRequest := servicePlanRequest(solved, assets, selected)
				encoded, _ := json.Marshal(stepRequest)
				json.Unmarshal(encoded, &stepRequest)
				native, err := r.ResolveAction(context.Background(), "connection", selected)
				if err != nil {
					t.Fatal(err)
				}
				result, err := native.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatal("native prerequisite", selected.Identity.NativeID, err)
				}
				streamAnalyticsAfterDelete(s)
				waited, err := native.Wait(context.Background(), stepRequest, result)
				if err != nil || !waited.Done {
					t.Fatal("prerequisite absence", selected.Identity.NativeID, waited, err)
				}
			}
			if len(s.deletes) != len(solved.Steps) || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("unreviewed deletion or wrong dependency order", s.deletes)
			}
		})
	}
}

func TestAPIMRevisionRetentionAndNewReferencePreventDeletion(t *testing.T) {
	for _, mode := range []string{"retained", "unobserved", "new-revision", "index-permission", "forged-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, target, revision := apimRevisionScenario(t, apimAPIType)
			request, input := dnsRequest(t, r, assets, target)
			switch mode {
			case "retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{revision.Identity.NativeID}}}
				solved, err := plan.Solve(input)
				if err != nil || len(solved.Blockers) == 0 {
					t.Fatal("retained revision failed to block current API", err)
				}
				return
			case "unobserved":
				assets = slices.DeleteFunc(assets, func(value asset.Asset) bool { return value.ID == revision.ID })
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil || len(contribution.Unresolved) == 0 {
					t.Fatal("missing revision observation was ignored", err)
				}
				return
			case "new-revision":
				s.gone[revision.Identity.NativeID] = true
				raw := apimExample(t, "ApiManagementGetApiRevision.json")
				id := target.Identity.NativeID + ";rev=4"
				raw["id"], raw["name"], raw["_apim_header_etag"] = id, last(id), `"revision-four"`
				object(raw["properties"])["apiRevision"] = "4"
				s.add(raw, apimVersion)
				s.lists[target.Identity.NativeID+"/revisions"] = append(s.lists[target.Identity.NativeID+"/revisions"], map[string]any{"apiId": id, "apiRevision": "4", "isCurrent": false})
			case "index-permission":
				s.status[target.Identity.NativeID+"/revisions"] = 403
				request.PrerequisiteDeletions = nil
			case "forged-prerequisite":
				s.gone[revision.Identity.NativeID] = true
				request.PrerequisiteDeletions[0].Asset.Normalized = map[string]any{}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed or forged revision dependency permitted a write")
			}
		})
	}
}
