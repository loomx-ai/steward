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
)

func apimIssueProjectionScenario(s *dnsScenario, canonical map[string]any) {
	canonicalID := text(canonical["id"])
	collection := apimRootID(canonicalID) + "/issues"
	id := collection + "/" + last(canonicalID)
	projection := maps.Clone(canonical)
	projection["id"], projection["type"] = id, apimServiceType+"/issues"
	s.add(projection, apimVersion)
	s.lists[collection] = append(s.lists[collection], projection)
	s.version[collection] = apimVersion
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		s.gone[id] = s.gone[canonicalID]
		return base(req)
	}
}

func TestAPIMWorkspaceLinksNativeProjectionBoundaries(t *testing.T) {
	for _, mode := range []string{"valid-second-page", "missing-link", "missing-connection", "list-denied", "partial-list", "foreign-page", "duplicate-page", "list-name-conflict", "list-type-conflict", "source-services-typo", "source-sibling", "missing-source", "foreign-gateway", "gateway-alias", "duplicate-gateway", "invalid-gateways", "read-denied", "read-missing", "partial-read", "read-retargeted", "read-etag-changed", "late-view-change"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, gateway, connection := apimGatewayScenario(t)
			workspace := cdnAsset(t, assets, apimWorkspaceType)
			root := apimRootID(workspace.Identity.NativeID)
			collection := root + "/workspacelinks"
			id := collection + "/" + last(workspace.Identity.NativeID)
			row := s.records[id]
			base := s.handle
			reads := 0
			switch mode {
			case "missing-link":
				s.lists[collection] = []any{}
			case "missing-connection":
				s.lists[gateway.Identity.NativeID+"/configconnections"] = []any{}
			case "list-denied":
				s.status[collection] = 403
			case "list-name-conflict":
				row["name"] = "different-name"
			case "list-type-conflict":
				row["type"] = apimWorkspaceType
			case "source-services-typo":
				object(row["properties"])["workspaceId"] = strings.Replace(workspace.Identity.NativeID, "/service/", "/services/", 1)
			case "source-sibling":
				object(row["properties"])["workspaceId"] = workspace.Identity.NativeID + "new"
			case "missing-source":
				s.status[workspace.Identity.NativeID] = 404
			case "foreign-gateway":
				object(array(object(row["properties"])["gateways"])[0])["id"] = strings.Replace(gateway.Identity.NativeID, testSubscription, testTenant, 1)
			case "gateway-alias":
				object(array(object(row["properties"])["gateways"])[0])["id"] = strings.Replace(gateway.Identity.NativeID, "/gateways/", "/gateway/", 1)
			case "duplicate-gateway":
				object(row["properties"])["gateways"] = []any{map[string]any{"id": gateway.Identity.NativeID}, map[string]any{"id": gateway.Identity.NativeID}}
			case "invalid-gateways":
				object(row["properties"])["gateways"] = nil
			case "read-denied":
				s.status[id] = 403
			case "read-missing":
				s.status[id] = 404
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "GET" && path == collection {
					switch mode {
					case "partial-list":
						return jsonResponse(206, map[string]any{"value": []any{row}}, nil), true
					case "foreign-page":
						return jsonResponse(200, map[string]any{"value": []any{row}, "nextLink": apiURL(strings.Replace(collection, testSubscription, testTenant, 1), apimVersion)}, nil), true
					case "valid-second-page", "duplicate-page":
						if req.URL.Query().Get("$skip") == "1" {
							return jsonResponse(200, map[string]any{"value": []any{row}}, nil), true
						}
						rows := []any{}
						if mode == "duplicate-page" {
							rows = append(rows, row)
						}
						return jsonResponse(200, map[string]any{"value": rows, "nextLink": apiURL(collection, apimVersion) + "&$skip=1"}, nil), true
					}
				}
				if req.Method == "GET" && path == id {
					reads++
					body := maps.Clone(row)
					switch mode {
					case "partial-read":
						return jsonResponse(206, body, nil), true
					case "read-retargeted":
						body["properties"] = maps.Clone(object(row["properties"]))
						object(body["properties"])["gateways"] = []any{}
						return jsonResponse(200, body, nil), true
					case "read-etag-changed":
						body["etag"] = "another-view"
						return jsonResponse(200, body, nil), true
					case "late-view-change":
						if reads == 2 {
							body["properties"] = maps.Clone(object(row["properties"]))
							object(body["properties"])["description"] = "changed-between-passes"
							return jsonResponse(200, body, nil), true
						}
					}
				}
				return base(req)
			}
			c, _ := r.resolve(context.Background(), "connection")
			index, err := c.apimIncomingIndex(context.Background(), workspace.Identity)
			if mode == "valid-second-page" {
				if err != nil || len(index[workspace.Identity.NativeID]) != 1 || index[workspace.Identity.NativeID][0].id != connection.Identity.NativeID || reads != 2 {
					t.Fatal("native paged projection lost actual connection", index, reads, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or changed reverse view accepted", mode)
			}
			if len(s.deletes) != 0 {
				t.Fatal("projection performed a write")
			}
		})
	}
	for _, name := range []string{"Microsoft.ApiManagement/service/workspaceLinks", "Microsoft.ApiManagement/service/issues"} {
		if _, exists := findType(name); exists {
			t.Fatal("derived view became duplicate asset", name)
		}
	}
	native := apimExample(t, "ApiManagementGetWorkspaceLink.json")
	if _, _, err := apimWorkspaceLink(apimRootID(text(native["id"])), native); err == nil {
		t.Fatal("published services/workspaces typo silently changed")
	}
}

func TestAPIMSharedGatewayKeepsOtherWorkspaceConnection(t *testing.T) {
	s, r, assets, gateway, connection := apimGatewayScenario(t)
	target := cdnAsset(t, assets, apimWorkspaceType)
	siblingID := redisParentID(target.Identity.NativeID) + "/workspaces/sibling"
	encoded, _ := json.Marshal(s.records[target.Identity.NativeID])
	var sibling map[string]any
	json.Unmarshal(encoded, &sibling)
	sibling["id"], sibling["name"], sibling["_apim_header_etag"] = siblingID, last(siblingID), `"sibling-workspace"`
	s.add(sibling, apimVersion)
	s.lists[redisParentID(siblingID)+"/workspaces"] = append(s.lists[redisParentID(siblingID)+"/workspaces"], sibling)
	for _, kind := range apimOwnedKinds(apimWorkspaceType) {
		path := siblingID + "/" + strings.ToLower(last(kind))
		s.lists[path], s.version[path] = []any{}, apimVersion
	}
	encoded, _ = json.Marshal(s.records[connection.Identity.NativeID])
	var siblingConnection map[string]any
	json.Unmarshal(encoded, &siblingConnection)
	connectionID := gateway.Identity.NativeID + "/configconnections/sibling"
	siblingConnection["id"], siblingConnection["name"] = connectionID, last(connectionID)
	object(siblingConnection["properties"])["sourceId"] = siblingID
	s.add(siblingConnection, apimVersion)
	s.lists[gateway.Identity.NativeID+"/configconnections"] = append(s.lists[gateway.Identity.NativeID+"/configconnections"], siblingConnection)
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, connectionID) {
			return jsonResponse(200, siblingConnection, nil), true
		}
		return base(req)
	}
	apimGatewayLinkScenario(t, s, siblingID, gateway.Identity.NativeID, connectionID)
	assets = append(assets, dnsAsset(t, r, sibling), dnsAsset(t, r, siblingConnection))
	_, input := dnsRequest(t, r, assets, target)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 {
		t.Fatal("shared gateway plan", solved.Blockers, err)
	}
	for _, step := range solved.Steps {
		i := slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })
		if i < 0 {
			t.Fatal("unknown plan step", step)
		}
		value := assets[i]
		request := servicePlanRequest(solved, assets, value)
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal("shared gateway cleanup", value.Identity.NativeType, err)
		}
		streamAnalyticsAfterDelete(s)
		waited, err := driver.Wait(context.Background(), request, result)
		if err != nil || !waited.Done {
			t.Fatal("shared gateway cleanup readback", waited, err)
		}
	}
	if s.gone[gateway.Identity.NativeID] || s.gone[siblingID] || s.gone[connectionID] || !slices.Equal(s.deletes, []string{connection.Identity.NativeID, target.Identity.NativeID}) {
		t.Fatal("sibling workspace or shared gateway removed", s.deletes)
	}
	// Even a syntactically valid source cannot move a second connection to
	// another APIM instance: one gateway serves workspaces from one service.
	other := strings.Replace(siblingID, "/service/stewardtest/", "/service/other/", 1)
	object(siblingConnection["properties"])["sourceId"] = other
	c, _ := r.resolve(context.Background(), "connection")
	s.gone[connection.Identity.NativeID] = false
	if _, err := c.nativeServiceChildren(context.Background(), gateway.Identity, s.records[gateway.Identity.NativeID], []string{apimGatewayConnectionType}); err == nil {
		t.Fatal("cross-service gateway membership accepted")
	}
}

func TestAPIMIssueProjectionNativeInventoryAndBoundaries(t *testing.T) {
	for _, mode := range []string{"valid-second-page", "missing-projection", "missing-api-issue", "projection-list-denied", "projection-read-denied", "projection-read-missing", "api-read-missing", "api-read-denied", "partial-list", "partial-read", "foreign-page", "duplicate-projection", "list-name-conflict", "list-type-conflict", "foreign-api", "api-wrong-kind", "projection-retargeted", "projection-etag", "api-content-changed", "late-api-etag"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			issue := cdnAsset(t, assets, apimIssueType)
			id := issue.Identity.NativeID
			root, api := apimRootID(id), redisParentID(id)
			collection, nativeCollection := root+"/issues", api+"/issues"
			projectionID := collection + "/" + last(id)
			row := s.records[projectionID]
			base := s.handle
			reads := 0
			switch mode {
			case "missing-projection":
				s.lists[collection] = []any{}
			case "missing-api-issue":
				s.lists[nativeCollection] = []any{}
			case "projection-list-denied":
				s.status[collection] = 403
			case "projection-read-denied":
				s.status[projectionID] = 403
			case "projection-read-missing":
				s.status[projectionID] = 404
			case "api-read-missing":
				s.status[id] = 404
			case "api-read-denied":
				s.status[id] = 403
			case "duplicate-projection":
				s.lists[collection] = append(s.lists[collection], row)
			case "list-name-conflict":
				row["name"] = "different-issue"
			case "list-type-conflict":
				row["type"] = apimIssueType
			case "foreign-api":
				row["properties"] = maps.Clone(object(row["properties"]))
				object(row["properties"])["apiId"] = strings.Replace(api, testSubscription, testTenant, 1)
			case "api-wrong-kind":
				row["properties"] = maps.Clone(object(row["properties"]))
				object(row["properties"])["apiId"] = root + "/products/product"
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "GET" && (path == collection || path == nativeCollection) {
					switch mode {
					case "valid-second-page":
						if req.URL.Query().Get("$skip") == "1" {
							return jsonResponse(200, map[string]any{"value": s.lists[path]}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, apimVersion) + "&$skip=1"}, nil), true
					case "partial-list":
						return jsonResponse(206, map[string]any{"value": s.lists[path]}, nil), true
					case "foreign-page":
						return jsonResponse(200, map[string]any{"value": s.lists[path], "nextLink": apiURL(path+"/foreign", apimVersion)}, nil), true
					}
				}
				if req.Method == "GET" && path == projectionID {
					body := maps.Clone(row)
					switch mode {
					case "partial-read":
						return jsonResponse(206, body, nil), true
					case "projection-retargeted":
						body["properties"] = maps.Clone(object(row["properties"]))
						object(body["properties"])["apiId"] = api + "another"
						return jsonResponse(200, body, nil), true
					case "projection-etag":
						return jsonResponse(200, body, http.Header{"Etag": {`"different-issue"`}}), true
					}
				}
				if req.Method == "GET" && path == id {
					reads++
					body := maps.Clone(s.records[id])
					switch mode {
					case "api-content-changed":
						body["properties"] = maps.Clone(object(body["properties"]))
						object(body["properties"])["description"] = "changed"
						return jsonResponse(200, body, http.Header{"Etag": {text(body["_apim_header_etag"])}}), true
					case "late-api-etag":
						if reads > 1 {
							return jsonResponse(200, body, http.Header{"Etag": {`"recreated-issue"`}}), true
						}
					}
				}
				return base(req)
			}
			c, _ := r.resolve(context.Background(), "connection")
			items, next, _, err := c.apimIssuePage(context.Background(), api)
			if mode == "valid-second-page" {
				if err != nil || next != "" || len(items) != 1 || object(items[0])["id"] != id || object(items[0])["type"] != apimIssueType || reads != 2 {
					t.Fatal("issue alias did not resolve to canonical resource", items, reads, err)
				}
			} else if err == nil {
				t.Fatal("issue projection conflict accepted", mode)
			}
			if len(s.deletes) != 0 {
				t.Fatal("issue inventory wrote to API")
			}
		})
	}
}

func TestAPIMIssueProjectionCanonicalDeletionAndRetention(t *testing.T) {
	s, r, assets := apimScenario(t)
	issue := cdnAsset(t, assets, apimIssueType)
	request, input := dnsRequest(t, r, assets, issue)
	attachment := cdnAsset(t, assets, apimIssueType+"/attachments")
	input.RequestOptions = map[asset.AssetID]map[string]any{issue.ID: {"retain_resources": []string{attachment.Identity.NativeID}}}
	retained, err := plan.Solve(input)
	if err != nil || len(retained.Blockers) == 0 {
		t.Fatal("retained issue attachment ignored", retained.Blockers, err)
	}
	// A changed service-level projection must prevent the canonical DELETE.
	projectionID := apimRootID(issue.Identity.NativeID) + "/issues/" + last(issue.Identity.NativeID)
	s.status[projectionID] = 404
	driver, err := r.ResolveAction(context.Background(), "connection", issue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("missing projection confused with absent target", err, s.deletes)
	}
	delete(s.status, projectionID)
	result, err := driver.Execute(context.Background(), request)
	if err != nil || !slices.Equal(s.deletes, []string{issue.Identity.NativeID}) {
		t.Fatal("issue deleted through wrong native route", err, s.deletes)
	}
	waited, err := driver.Wait(context.Background(), request, result)
	if err != nil || waited.Done {
		t.Fatal("issue absence hid surviving attachments/comments", waited, err)
	}
	streamAnalyticsAfterDelete(s)
	encoded, _ := json.Marshal(request)
	json.Unmarshal(encoded, &request)
	encoded, _ = json.Marshal(result)
	json.Unmarshal(encoded, &result)
	driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
	waited, err = driver.Wait(context.Background(), request, result)
	if err != nil || !waited.Done || s.gone[redisParentID(issue.Identity.NativeID)] {
		t.Fatal("canonical issue readback", waited, err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
		t.Fatal("issue deletion not idempotent", err)
	}
}

func TestAPIMServiceIssueProjectionDetectsMissingAPI(t *testing.T) {
	s, r, assets := apimScenario(t)
	service := cdnAsset(t, assets, apimServiceType)
	s.lists[service.Identity.NativeID+"/apis"] = []any{}
	c, _ := r.resolve(context.Background(), "connection")
	if _, err := c.apimChildren(context.Background(), service.Identity, s.records[service.Identity.NativeID]); err == nil {
		t.Fatal("service deletion could hide issue whose API was omitted")
	}
}
