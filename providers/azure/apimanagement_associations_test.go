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

var apimAssociationKinds = []string{apimServiceType + "/products/apis", apimServiceType + "/products/groups", apimServiceType + "/gateways/apis", apimServiceType + "/groups/users", apimWorkspaceType + "/groups/users", apimAPIType + "/tags", apimAPIType + "/operations/tags", apimServiceType + "/products/tags"}

func apimAssociationScenario(t *testing.T, kind string) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	parent := cdnAsset(t, assets, kind[:strings.LastIndex(kind, "/")])
	target := cdnAsset(t, assets, apimServiceType+"/"+last(kind))
	raw := maps.Clone(s.records[target.Identity.NativeID])
	id := parent.Identity.NativeID + "/" + strings.ToLower(last(kind)) + "/" + last(target.Identity.NativeID)
	raw["id"], raw["name"], raw["type"] = id, last(id), kind
	delete(raw, "_apim_header_etag")
	s.add(raw, apimVersion)
	collection := strings.TrimSuffix(id, "/"+last(id))
	s.lists[collection] = []any{raw}
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path == id && req.Method == "GET" {
			if resourceReadMethod(kind) == "GET" {
				if s.gone[id] {
					return jsonResponse(404, nil, nil), true
				}
				if s.status[id] != 0 {
					return jsonResponse(s.status[id], nil, nil), true
				}
				body := maps.Clone(s.records[target.Identity.NativeID])
				delete(body, "_apim_header_etag")
				return jsonResponse(200, body, nil), true
			}
			t.Fatal("association has no native GET endpoint", req.URL)
		}
		if path == id && req.Method == "HEAD" {
			status := 204
			if kind == apimServiceType+"/gateways/apis" {
				status = 200
			}
			if s.gone[id] {
				status = 404
			}
			if s.status[id] != 0 {
				status = s.status[id]
			}
			return jsonResponse(status, nil, nil), true
		}
		if path == id && req.Method == "DELETE" && req.Header.Get("If-Match") != "" {
			t.Fatal("unsupported association If-Match")
		}
		// A member-form LIST row survives the member itself but disappears when
		// the parent association is detached.
		if path == collection && req.Method == "GET" && s.status[path] == 0 && s.gone[id] {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return base(req)
	}
	c, _ := r.resolve(context.Background(), "connection")
	live, err := c.apimResource(context.Background(), id)
	if err != nil {
		t.Fatal("native association read", err)
	}
	value := dnsAsset(t, r, live)
	assets = append(assets, value)
	return s, r, assets, value, target
}

func TestAPIMAssociationNativeInventoryAndUnlink(t *testing.T) {
	for _, kind := range apimAssociationKinds {
		t.Run(kind, func(t *testing.T) {
			s, r, assets, value, target := apimAssociationScenario(t, kind)
			batch, err := r.List(context.Background(), productRequest(r, kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("native link inventory", len(batch.Items), err)
			}
			item := batch.Items[0]
			if item.NativeID != value.Identity.NativeID || item.Location != "westus" || !slices.Contains(stringValues(item.Normalized["_apim_references"]), target.Identity.NativeID) {
				t.Fatal("link identity, region, or target lost", item.NativeID)
			}
			request, _ := dnsRequest(t, r, assets, value)
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != value.Identity.NativeID || s.gone[target.Identity.NativeID] {
				t.Fatal("link deletion touched member or failed", err, s.deletes)
			}
			encoded, _ := json.Marshal(request)
			json.Unmarshal(encoded, &request)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			waited, err := driver.Wait(context.Background(), request, result)
			if err != nil || !waited.Done {
				t.Fatal("HEAD absence", waited, err)
			}
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
				t.Fatal("unlink idempotency", err)
			}
		})
	}
}

func TestAPIMAssociationMemberFormsAndNativeDiscrepancies(t *testing.T) {
	for _, kind := range apimAssociationKinds {
		for _, mode := range []string{"member-id", "member-type", "workspace-user-id", "wrong-name", "foreign-parent", "foreign-member", "wrong-type", "duplicate", "partial-page", "foreign-next-page", "second-page", "head-403", "head-206", "list-404", "member-404", "member-changed", "missing-from-list", "head-changed"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, _, value, target := apimAssociationScenario(t, kind)
				id := value.Identity.NativeID
				collection := strings.TrimSuffix(id, "/"+last(id))
				row := maps.Clone(s.records[id])
				s.lists[collection] = []any{row}
				base := s.handle
				heads := 0
				switch mode {
				case "member-id", "member-type":
					row["id"] = target.Identity.NativeID
					if mode == "member-type" {
						row["type"] = target.Identity.NativeType
					}
				case "workspace-user-id":
					if kind != apimWorkspaceType+"/groups/users" {
						return
					}
					row["id"] = apimNamespaceID(id) + "/users/" + last(id)
				case "wrong-name":
					row["name"] = "ambiguous"
				case "foreign-parent":
					row["id"] = strings.Replace(id, "/stewardtest/", "/foreign/", 1)
				case "foreign-member":
					row["id"] = strings.Replace(target.Identity.NativeID, "/stewardtest/", "/foreign/", 1)
				case "wrong-type":
					row["type"] = apimServiceType + "/namedValues"
				case "duplicate":
					s.lists[collection] = append(s.lists[collection], row)
				case "head-403":
					s.status[id] = 403
				case "head-206":
					s.status[id] = 206
				case "list-404":
					s.status[collection] = 404
				case "member-404":
					s.status[target.Identity.NativeID] = 404
				case "member-changed":
					row["properties"] = maps.Clone(object(row["properties"]))
					object(row["properties"])["displayName"] = "old"
				case "missing-from-list":
					s.lists[collection] = []any{}
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == resourceReadMethod(kind) && strings.EqualFold(req.URL.Path, id) {
						heads++
						if mode == "head-changed" && heads == 2 {
							return jsonResponse(404, nil, nil), true
						}
					}
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, collection) {
						switch mode {
						case "partial-page":
							return jsonResponse(206, map[string]any{"value": s.lists[collection]}, nil), true
						case "foreign-next-page":
							return jsonResponse(200, map[string]any{"value": s.lists[collection], "nextLink": "https://management.azure.com" + collection + "?api-version=2022-08-01&$skip=1"}, nil), true
						case "second-page":
							if req.URL.Query().Get("$skip") == "" {
								return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://management.azure.com" + collection + "?api-version=" + apimVersion + "&$skip=1"}, nil), true
							}
						}
					}
					return base(req)
				}
				c, _ := r.resolve(context.Background(), "connection")
				raw, err := c.apimResource(context.Background(), id)
				if slices.Contains([]string{"member-id", "member-type", "workspace-user-id", "second-page"}, mode) {
					if err != nil || text(raw["id"]) != id {
						t.Fatal("supported native member form", err)
					}
					return
				}
				if err == nil || isNotFound(err) || len(s.deletes) != 0 {
					t.Fatal("invalid association accepted or dependency absence mistaken for target absence", mode, err)
				}
			})
		}
	}
	for _, file := range []string{"ApiManagementListGroupUsers.json", "ApiManagementListWorkspaceGroupUsers.json"} {
		raw := object(array(apimExample(t, file)["value"])[0])
		kind := text(raw["type"])
		parent := apimNamespaceID(text(raw["id"])) + "/groups/example"
		if _, err := apimAssociationRow(parent, kind, raw); err == nil {
			t.Fatal("published name/ID mismatch was silently repaired", file)
		}
	}
}

func TestAPIMAssociationPrerequisiteAndParentCascade(t *testing.T) {
	for _, kind := range apimAssociationKinds {
		for _, mode := range []string{"member", "parent"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets, value, target := apimAssociationScenario(t, kind)
				if mode == "parent" {
					target = cdnAsset(t, assets, kind[:strings.LastIndex(kind, "/")])
				}
				request, input := dnsRequest(t, r, assets, target)
				solved, err := plan.Solve(input)
				if err != nil || len(solved.Blockers) != 0 {
					t.Fatal("native association plan", err, solved.Blockers)
				}
				if mode == "member" {
					if !slices.ContainsFunc(request.PrerequisiteDeletions, func(p contracts.ActionImpact) bool { return p.Asset.ID == value.ID }) {
						t.Fatal("member omitted association prerequisite")
					}
					driver, _ := r.ResolveAction(context.Background(), "connection", target)
					if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
						t.Fatal("member deleted with retained association")
					}
				} else if !slices.ContainsFunc(request.LifecycleImpacts, func(p contracts.ActionImpact) bool { return p.Asset.ID == value.ID }) {
					t.Fatal("parent omitted owned association")
				}
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{value.Identity.NativeID}}}
				retained, err := plan.Solve(input)
				if err != nil || len(retained.Blockers) == 0 {
					t.Fatal("retained association did not block owner/member", err)
				}
				for _, step := range solved.Steps {
					var selected asset.Asset
					for _, candidate := range assets {
						if candidate.ID == step.AssetID {
							selected = candidate
						}
					}
					request := servicePlanRequest(solved, assets, selected)
					encoded, _ := json.Marshal(request)
					json.Unmarshal(encoded, &request)
					driver, err := r.ResolveAction(context.Background(), "connection", selected)
					if err != nil {
						t.Fatal(err)
					}
					result, err := driver.Execute(context.Background(), request)
					if err != nil {
						t.Fatal("reviewed association cleanup", selected.Identity.NativeType, err)
					}
					streamAnalyticsAfterDelete(s)
					waited, err := driver.Wait(context.Background(), request, result)
					if err != nil || !waited.Done {
						t.Fatal("association/cascade absence", waited, err)
					}
				}
				if len(s.deletes) != len(solved.Steps) || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
					t.Fatal("unexpected cleanup order", s.deletes)
				}
			})
		}
	}
}

func TestAPIMAssociationPreflightRejectsDriftAndProtection(t *testing.T) {
	reviews := apimReviewCache{}
	for _, kind := range apimAssociationKinds {
		for _, mode := range []string{"target-recreated", "target-changed", "ancestor-changed", "ancestor-protected", "locked", "read-denied", "list-denied", "member-missing", "listed-missing", "forged-snapshot", "late-recreation", "wait-surviving", "wait-denied"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets, value, target := apimAssociationScenario(t, kind)
				request, _ := reviews.request(t, r, assets, value)
				id := value.Identity.NativeID
				parent := redisParentID(id)
				collection := strings.TrimSuffix(id, "/"+last(id))
				c, _ := r.resolve(context.Background(), "connection")
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				base := s.handle
				reads := 0
				switch mode {
				case "target-recreated":
					s.records[target.Identity.NativeID]["_apim_header_etag"] = `"recreated"`
				case "target-changed":
					s.records[target.Identity.NativeID]["properties"] = maps.Clone(object(s.records[target.Identity.NativeID]["properties"]))
					object(s.records[target.Identity.NativeID]["properties"])["description"] = "changed"
				case "ancestor-changed":
					object(s.records[parent]["properties"])["description"] = "changed"
				case "ancestor-protected":
					s.records[parent]["tags"] = map[string]any{"steward:protect": "true"}
				case "read-denied":
					s.status[id] = 403
				case "list-denied":
					s.status[collection] = 403
				case "member-missing":
					s.status[target.Identity.NativeID] = 404
				case "listed-missing":
					s.lists[collection] = []any{}
				case "forged-snapshot":
					request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
					delete(request.Asset.Normalized, "_apim_private_configuration")
				case "locked":
					s.lists[c.root()+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": parent + "/providers/Microsoft.Authorization/locks/link", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "late-recreation":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
							reads++
							if reads == 2 {
								s.records[target.Identity.NativeID]["_apim_header_etag"] = `"changed-before-unlink"`
							}
						}
						return base(req)
					}
				}
				if strings.HasPrefix(mode, "wait-") {
					result, err := driver.Execute(context.Background(), request)
					if err != nil {
						t.Fatal(err)
					}
					s.gone[id] = false
					if mode == "wait-denied" {
						s.status[id] = 403
					}
					waited, err := driver.Wait(context.Background(), request, result)
					if waited.Done || mode == "wait-surviving" && err != nil || mode == "wait-denied" && err == nil {
						t.Fatal("association absence not proved", waited, err)
					}
					return
				}
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("unreviewed association mutation", mode, err, s.deletes)
				}
			})
		}
	}
}

func TestAPIMBuiltinProductGroupCanBeDetachedWithoutDeletingGroup(t *testing.T) {
	s, r, assets, value, target := apimAssociationScenario(t, apimServiceType+"/products/groups")
	object(s.records[target.Identity.NativeID]["properties"])["builtIn"] = true
	c, _ := r.resolve(context.Background(), "connection")
	raw, err := c.apimResource(context.Background(), value.Identity.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	value = dnsAsset(t, r, raw)
	for i := range assets {
		if assets[i].ID == value.ID {
			assets[i] = value
		}
		if assets[i].ID == target.ID {
			assets[i] = dnsAsset(t, r, s.records[target.Identity.NativeID])
		}
	}
	request, _ := dnsRequest(t, r, assets, value)
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 || s.deletes[0] != value.Identity.NativeID || s.gone[target.Identity.NativeID] {
		t.Fatal("builtin group confused with product association", err, s.deletes)
	}
}
