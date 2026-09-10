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

func apimGatewayScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	source := cdnAsset(t, assets, apimWorkspaceType)
	gatewayID := "/subscriptions/" + testSubscription + "/resourcegroups/gateway-rg/providers/microsoft.apimanagement/gateways/shared-gateway"
	gateway := apimExample(t, "ApiManagementGatewayGetGateway.json")
	gateway["id"], gateway["name"], gateway["type"], gateway["location"] = gatewayID, last(gatewayID), apimGatewayType, "West US"
	object(object(object(gateway["properties"])["backend"])["subnet"])["id"] = "/subscriptions/" + testSubscription + "/resourcegroups/network-rg/providers/microsoft.network/virtualnetworks/apim-network/subnets/gateway"
	s.add(gateway, apimVersion)
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.apimanagement/gateways"] = []any{gateway}
	connection := apimExample(t, "ApiManagementGetGatewayConfigConnection.json")
	connectionID := gatewayID + "/configconnections/workspace-connection"
	connection["id"], connection["name"], connection["type"] = connectionID, last(connectionID), apimGatewayConnectionType
	object(connection["properties"])["sourceId"] = source.Identity.NativeID
	s.add(connection, apimVersion)
	s.lists[gatewayID+"/configconnections"], s.version[gatewayID+"/configconnections"] = []any{connection}, apimVersion
	apimGatewayLinkScenario(t, s, source.Identity.NativeID, gatewayID, connectionID)
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path == gatewayID || path == connectionID {
			if req.Method == "GET" {
				if s.status[path] != 0 {
					return jsonResponse(s.status[path], nil, nil), true
				}
				if s.gone[path] {
					return jsonResponse(404, nil, nil), true
				}
				body := maps.Clone(s.records[path])
				if path == gatewayID {
					body["id"] = strings.Replace(gatewayID, "/gateways/", "/gateway/", 1)
					body["type"] = apimGatewayAlias
				}
				return jsonResponse(200, body, nil), true
			}
			if req.Method == "DELETE" {
				if path == connectionID && req.Header.Get("If-Match") != text(connection["etag"]) {
					t.Fatal("native body ETag not used exactly", req.Header.Get("If-Match"))
				}
				if path == gatewayID && req.Header.Get("If-Match") != "" {
					t.Fatal("gateway DELETE has no If-Match contract")
				}
				s.deletes = append(s.deletes, path)
				s.gone[path] = true
				return jsonResponse(204, nil, http.Header{"X-Ms-Request-Id": {"gateway-delete"}}), true
			}
		}
		return base(req)
	}
	gatewayAsset := dnsAsset(t, r, gateway)
	connectionAsset := dnsAsset(t, r, connection)
	assets = append(assets, gatewayAsset, connectionAsset)
	return s, r, assets, gatewayAsset, connectionAsset
}

func apimGatewayLinkScenario(t *testing.T, s *dnsScenario, workspace, gateway, connection string) {
	t.Helper()
	root := apimRootID(workspace)
	collection := root + "/workspacelinks"
	id := collection + "/" + last(workspace)
	link := apimExample(t, "ApiManagementGetWorkspaceLink.json")
	link["id"], link["name"] = id, last(id)
	object(link["properties"])["workspaceId"] = workspace
	object(link["properties"])["gateways"] = []any{map[string]any{"id": gateway}}
	s.add(link, apimVersion)
	s.lists[collection] = append(s.lists[collection], link)
	s.version[collection] = apimVersion
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		// This read-only reverse view loses the association after unlinking.
		if s.gone[connection] {
			object(link["properties"])["gateways"] = []any{}
		}
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) && s.status[id] == 0 && !s.gone[id] {
			return jsonResponse(200, s.records[id], nil), true
		}
		return base(req)
	}
}

func TestAPIMGatewayNativeInventoryBodyETagsAndAlias(t *testing.T) {
	s, r, _, gateway, connection := apimGatewayScenario(t)
	for _, value := range []asset.Asset{gateway, connection} {
		batch, err := r.List(context.Background(), productRequest(r, value.Identity.NativeType))
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal("native gateway inventory", value.Identity.NativeType, len(batch.Items), err)
		}
		item := batch.Items[0]
		if item.NativeID != value.Identity.NativeID || item.Location != "westus" || text(item.Normalized["arm_etag"]) != text(s.records[value.Identity.NativeID]["etag"]) {
			t.Fatal("gateway ID, inherited region, or body ETag lost")
		}
	}
	if !slices.Contains(stringValues(gateway.Normalized["_apim_references"]), "/subscriptions/"+testSubscription+"/resourcegroups/network-rg/providers/microsoft.network/virtualnetworks/apim-network/subnets/gateway") {
		t.Fatal("gateway subnet missing")
	}
	if text(connection.Normalized["_apim_gateway_source_configuration"]) == "" {
		t.Fatal("workspace source not frozen")
	}
}

func TestAPIMGatewayAndConnectionCleanupPreserveWorkspace(t *testing.T) {
	for _, kind := range []string{apimGatewayType, apimGatewayConnectionType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets, gateway, connection := apimGatewayScenario(t)
			target := gateway
			if kind == apimGatewayConnectionType {
				target = connection
			}
			source := cdnAsset(t, assets, apimWorkspaceType)
			request, _ := dnsRequest(t, r, assets, target)
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != target.Identity.NativeID {
				t.Fatal("gateway cleanup", err, s.deletes)
			}
			if kind == apimGatewayType {
				waited, err := driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done {
					t.Fatal("gateway absence hid surviving connection", waited, err)
				}
			}
			streamAnalyticsAfterDelete(s)
			encoded, _ := json.Marshal(request)
			json.Unmarshal(encoded, &request)
			encoded, _ = json.Marshal(result)
			json.Unmarshal(encoded, &result)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			waited, err := driver.Wait(context.Background(), request, result)
			if err != nil || !waited.Done || s.gone[source.Identity.NativeID] {
				t.Fatal("gateway readback touched workspace", waited, err)
			}
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
				t.Fatal("gateway deletion not idempotent", err)
			}
		})
	}
}

func TestAPIMWorkspaceAndServiceDeletionUnlinkSharedGatewayFirst(t *testing.T) {
	for _, kind := range []string{apimWorkspaceType, apimServiceType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets, gateway, connection := apimGatewayScenario(t)
			target := cdnAsset(t, assets, kind)
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(request.PrerequisiteDeletions, func(p contracts.ActionImpact) bool { return p.Asset.ID == connection.ID }) {
				t.Fatal("workspace gateway connection not reviewed")
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("workspace deleted while gateway still subscribed")
			}
			input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{connection.Identity.NativeID}}}
			retained, err := plan.Solve(input)
			if err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained connection ignored", err)
			}
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, a := range assets {
					if a.ID == step.AssetID {
						selected = a
					}
				}
				req := servicePlanRequest(solved, assets, selected)
				encoded, _ := json.Marshal(req)
				json.Unmarshal(encoded, &req)
				driver, err := r.ResolveAction(context.Background(), "connection", selected)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(context.Background(), req)
				if err != nil {
					t.Fatal("gateway prerequisite cleanup", selected.Identity.NativeType, err)
				}
				streamAnalyticsAfterDelete(s)
				waited, err := driver.Wait(context.Background(), req, result)
				if err != nil || !waited.Done {
					t.Fatal("gateway prerequisite readback", waited, err)
				}
			}
			if s.gone[gateway.Identity.NativeID] || len(s.deletes) < 2 || s.deletes[0] != connection.Identity.NativeID || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("shared gateway deleted or wrong order", s.deletes)
			}
		})
	}
}

func TestAPIMGatewaySourceAndNativeIndexBoundaries(t *testing.T) {
	reviews := apimReviewCache{}
	for _, mode := range []string{"source-moved", "source-wrong-type", "source-recreated", "source-read-denied", "source-read-404", "foreign-subscription", "wrong-region", "gateway-recreated", "gateway-protected", "gateway-list-denied", "gateway-list-partial", "gateway-list-foreign-next", "gateway-list-duplicate", "connection-retargeted", "connection-etag", "missing-source-proof"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, gateway, connection := apimGatewayScenario(t)
			source := cdnAsset(t, assets, apimWorkspaceType)
			target := connection
			if strings.HasPrefix(mode, "gateway-list") {
				target = source
			}
			request, _ := reviews.request(t, r, assets, target)
			root := "/subscriptions/" + testSubscription + "/providers/microsoft.apimanagement/gateways"
			base := s.handle
			switch mode {
			case "source-moved":
				s.records[source.Identity.NativeID]["id"] = source.Identity.NativeID + "moved"
			case "source-wrong-type":
				object(s.records[connection.Identity.NativeID]["properties"])["sourceId"] = apimRootID(source.Identity.NativeID)
			case "source-recreated":
				s.records[source.Identity.NativeID]["_apim_header_etag"] = `"recreated-workspace"`
			case "source-read-denied":
				s.status[source.Identity.NativeID] = 403
			case "source-read-404":
				s.status[source.Identity.NativeID] = 404
			case "foreign-subscription":
				object(s.records[connection.Identity.NativeID]["properties"])["sourceId"] = strings.Replace(source.Identity.NativeID, testSubscription, testTenant, 1)
			case "wrong-region":
				s.records[gateway.Identity.NativeID]["location"] = "East US"
			case "gateway-recreated":
				object(s.records[gateway.Identity.NativeID]["properties"])["createdAtUtc"] = "2026-09-10T12:00:00Z"
			case "gateway-protected":
				s.records[gateway.Identity.NativeID]["tags"] = map[string]any{"steward:protect": "true"}
			case "gateway-list-denied":
				s.status[root] = 403
			case "gateway-list-partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, root) {
						return jsonResponse(206, map[string]any{"value": s.lists[root]}, nil), true
					}
					return base(req)
				}
			case "gateway-list-foreign-next":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, root) {
						return jsonResponse(200, map[string]any{"value": s.lists[root], "nextLink": "https://management.azure.com/subscriptions/" + testTenant + "/providers/Microsoft.ApiManagement/gateways?api-version=" + apimVersion}, nil), true
					}
					return base(req)
				}
			case "gateway-list-duplicate":
				s.lists[root] = append(s.lists[root], s.lists[root][0])
			case "connection-retargeted":
				object(s.records[connection.Identity.NativeID]["properties"])["sourceId"] = source.Identity.NativeID + "new"
			case "connection-etag":
				s.records[connection.Identity.NativeID]["etag"] = "new-connection-etag"
			case "missing-source-proof":
				request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
				delete(request.Asset.Normalized, "_apim_gateway_source_configuration")
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed gateway mutation", mode, err, s.deletes)
			}
		})
	}
}

func TestAPIMGatewayNativeAsynchronousDeletionAndResumption(t *testing.T) {
	for _, kind := range []string{apimGatewayType, apimGatewayConnectionType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets, gateway, connection := apimGatewayScenario(t)
			target := gateway
			if kind == apimGatewayConnectionType {
				target = connection
			}
			request, _ := dnsRequest(t, r, assets, target)
			base := s.handle
			operation := "https://management.azure.com" + target.Identity.NativeID + "?api-version=" + apimVersion
			if kind == apimGatewayType {
				operation = "https://management.azure.com" + strings.Replace(gateway.Identity.NativeID, "/gateways/", "/gateway/", 1) + "/operationresults/TGV2eTExMDZtMDJfVGVybV9jMmZlY2QwMA==?api-version=" + apimVersion
			}
			stage := "Deleting"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
					if kind == apimGatewayConnectionType && req.Header.Get("If-Match") != text(s.records[target.Identity.NativeID]["etag"]) {
						t.Fatal("native config If-Match missing")
					}
					s.deletes = append(s.deletes, target.Identity.NativeID)
					headers := http.Header{"Location": {operation}, "X-Ms-Request-Id": {"gateway-accepted"}}
					if kind == apimGatewayConnectionType {
						headers.Set("Azure-AsyncOperation", operation)
						return jsonResponse(202, nil, headers), true
					}
					body := maps.Clone(s.records[target.Identity.NativeID])
					body["id"], body["type"] = strings.Replace(target.Identity.NativeID, "/gateways/", "/gateway/", 1), apimGatewayAlias
					return jsonResponse(202, body, headers), true
				}
				if req.Method == "GET" && req.URL.String() == operation {
					if stage == "Absent" {
						return jsonResponse(404, nil, nil), true
					}
					body := maps.Clone(s.records[target.Identity.NativeID])
					body["properties"] = maps.Clone(object(body["properties"]))
					object(body["properties"])["targetProvisioningState"] = stage
					if kind == apimGatewayType {
						body["id"], body["type"] = strings.Replace(target.Identity.NativeID, "/gateways/", "/gateway/", 1), apimGatewayAlias
					}
					return jsonResponse(200, body, nil), true
				}
				return base(req)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.ProviderOperationID != operation || result.ProviderRequestID != "gateway-accepted" {
				t.Fatal("native gateway async receipt", result, err)
			}
			encoded, _ := json.Marshal(result)
			json.Unmarshal(encoded, &result)
			encoded, _ = json.Marshal(request)
			json.Unmarshal(encoded, &request)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			waited, err := driver.Wait(context.Background(), request, result)
			if err != nil || waited.Done {
				t.Fatal("in-progress gateway treated as absent", waited, err)
			}
			stage = "Succeeded"
			waited, err = driver.Wait(context.Background(), request, result)
			if err != nil || waited.Done {
				t.Fatal("successful LRO hid live gateway", waited, err)
			}
			stage = "Absent"
			s.gone[target.Identity.NativeID] = true
			if kind == apimGatewayType {
				waited, err = driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done {
					t.Fatal("root gone but connection survived", waited, err)
				}
			}
			streamAnalyticsAfterDelete(s)
			waited, err = driver.Wait(context.Background(), request, result)
			if err != nil || !waited.Done {
				t.Fatal("resumed gateway absence", waited, err)
			}
		})
	}
}

func TestAPIMGatewayPollingURLsStayBoundToExactResource(t *testing.T) {
	_, r, _, gateway, connection := apimGatewayScenario(t)
	for _, value := range []asset.Asset{gateway, connection} {
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		a := monitorTargetInner(driver).(*action)
		native := "https://management.azure.com" + value.Identity.NativeID + "?api-version=" + apimVersion
		if value.Identity.NativeType == apimGatewayType {
			native = "https://management.azure.com" + strings.Replace(value.Identity.NativeID, "/gateways/", "/gateway/", 1) + "/operationResults/operation-1?api-version=" + apimVersion
		}
		if err := a.validateOperationURL(native); err != nil {
			t.Fatal("native gateway URL rejected", err)
		}
		for _, changed := range []string{strings.Replace(native, "shared-gateway", "foreign-gateway", 1), strings.Replace(native, testSubscription, testTenant, 1), strings.Replace(native, "gateway-rg", "foreign-rg", 1), strings.Replace(native, apimVersion, "2022-08-01", 1), native + "&unselected=true", native + "#fragment", strings.Replace(native, "management.azure.com", "attacker.invalid", 1)} {
			if err := a.validateOperationURL(changed); err == nil {
				t.Fatal("gateway polling owner changed", changed)
			}
		}
	}
	if validateAPIMOperationURL(testSubscription, "not-a-resource", "westus", apimVersion, "https://management.azure.com/subscriptions/"+testSubscription+"/resourceGroups/test/providers/Microsoft.Other/resources/owner/operationResults/operation?api-version="+apimVersion) == nil {
		t.Fatal("empty gateway owner accepted")
	}
}

func TestAPIMGatewayBodyETagAndPublishedIdentityDiscrepancies(t *testing.T) {
	reviews := apimReviewCache{}
	for _, mode := range []string{"valid", "missing", "wildcard", "whitespace", "newline", "wrong-source-plural"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, _, connection := apimGatewayScenario(t)
			request, _ := reviews.request(t, r, assets, connection)
			switch mode {
			case "missing":
				delete(s.records[connection.Identity.NativeID], "etag")
			case "wildcard":
				s.records[connection.Identity.NativeID]["etag"] = "*"
			case "whitespace":
				s.records[connection.Identity.NativeID]["etag"] = "  tag  "
			case "newline":
				s.records[connection.Identity.NativeID]["etag"] = "tag\nforged"
			case "wrong-source-plural":
				props := object(s.records[connection.Identity.NativeID]["properties"])
				props["sourceId"] = strings.Replace(text(props["sourceId"]), "/service/", "/services/", 1)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", connection)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if mode == "valid" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatal("native body ETag", err)
				}
				return
			}
			if err == nil || len(s.deletes) != 0 {
				t.Fatal("invalid native gateway identity/ETag accepted", mode, err)
			}
		})
	}
	native := apimExample(t, "ApiManagementGetGatewayConfigConnection.json")
	if _, err := apimGatewaySourceID(text(native["id"]), native); err == nil {
		t.Fatal("published source services/workspaces typo silently rebound")
	}
}
