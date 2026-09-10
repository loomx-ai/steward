package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorPrivateLinkExample(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile("fixtures/applicationinsights/monitor/examples/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return object(object(object(value["responses"])["200"])["body"])
}

func TestMonitorPrivateLinkPublishedConnectionListIdentity(t *testing.T) {
	rows := array(monitorPrivateLinkExample(t, "PrivateEndpointConnectionList")["value"])
	if len(rows) != 2 || object(rows[0])["id"] != object(rows[1])["id"] || object(rows[0])["name"] == object(rows[1])["name"] {
		t.Fatal("published duplicate-ID discrepancy changed")
	}
	if err := monitorPrivateLinkListed(monitorPrivateConnectionType, object(rows[0]), object(rows[1])); err == nil {
		t.Fatal("native list name/identity discrepancy was accepted")
	}
}

func monitorPrivateLinkScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := resourceID(monitorPrivateLinkType, "scope")
	for _, row := range []struct{ kind, name, fixture string }{
		{monitorPrivateLinkType, "", "PrivateLinkScopesGet"},
		{monitorScopedResourceType, "/scopedResources/association", "PrivateLinkScopedResourceGet"},
		{monitorPrivateConnectionType, "/privateEndpointConnections/connection", "PrivateEndpointConnectionGet"},
	} {
		raw := monitorPrivateLinkExample(t, row.fixture)
		// Compose the three independently published examples under one scope.
		// Shared target IDs remain exactly as published, including cross-sub links.
		raw["id"], raw["type"], raw["name"] = root+row.name, row.kind, last(root+row.name)
		s.add(raw, monitorPrivateLinkVersion)
		collection := strings.ToLower(root + row.name[:max(0, strings.LastIndex(row.name, "/"))])
		if row.name == "" {
			collection = "/subscriptions/" + testSubscription + "/providers/microsoft.insights/privatelinkscopes"
		}
		s.lists[collection], s.version[collection] = []any{raw}, monitorPrivateLinkVersion
	}
	capability := monitorPrivateLinkExample(t, "PrivateLinkScopePrivateLinkResourceGet")
	capability["id"] = root + "/privateLinkResources/azuremonitor"
	s.add(capability, monitorPrivateLinkVersion)
	s.lists[strings.ToLower(root+"/privateLinkResources")] = []any{capability}
	s.version[strings.ToLower(root+"/privateLinkResources")] = monitorPrivateLinkVersion
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/resourceGroups/test", "type": groupType}}
	r := s.runtime(t)
	values := []asset.Asset{}
	for _, kind := range []string{monitorPrivateLinkType, monitorScopedResourceType, monitorPrivateConnectionType} {
		batch, err := r.List(context.Background(), productRequest(r, kind))
		if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "global" {
			t.Fatalf("native AMPLS inventory %s: %+v %v", kind, batch, err)
		}
		item := batch.Items[0]
		values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	return s, r, values
}

func TestMonitorPrivateLinkNativeLifecycle(t *testing.T) {
	s, r, values := monitorPrivateLinkScenario(t)
	request, input := dnsRequest(t, r, values, values[0])
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 || len(request.PrerequisiteDeletions) != 2 || len(request.LifecycleImpacts) != 0 {
		t.Fatalf("native AMPLS prerequisite plan: %+v %+v %v", result, request, err)
	}
	for _, binding := range input.LifecycleBindings {
		if binding.CleanupPolicy != graph.CleanupDirect || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] == true {
			t.Fatal("AMPLS children must have independent DELETE steps")
		}
	}
	retained := input
	retained.RequestOptions = map[asset.AssetID]map[string]any{values[0].ID: {"retain_resources": []string{string(values[1].ID)}}}
	if result, err := plan.Solve(retained); err != nil || len(result.Blockers) == 0 {
		t.Fatalf("accepted retention of a required association: %+v %v", result, err)
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", values[0])
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("scope deletion did not wait for its two prerequisites")
	}
	operation := "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.Insights/privateLinkScopeOperationStatuses/11111111-2222-4333-8444-555555555555"
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.Contains(req.URL.Path, "/privateLinkScopeOperationStatuses/") {
			return jsonResponse(200, map[string]any{"status": "Succeeded", "id": operation, "name": last(operation)}, nil), true
		}
		if req.Method == "DELETE" {
			if req.Header.Get("If-Match") != "" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") == "" {
				t.Fatal("AMPLS DELETE diverged from its native contract")
			}
			s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
			return jsonResponse(202, nil, http.Header{"Location": {operation}, "X-Ms-Request-Id": {"monitor-delete"}}), true
		}
		return nil, false
	}
	for _, i := range []int{1, 2, 0} {
		value := values[i]
		req := servicePlanRequest(result, values, value)
		req.IdempotencyKey = value.Identity.NativeID
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		op, err := driver.Execute(context.Background(), req)
		if err != nil || op.ProviderRequestID != "monitor-delete" || op.ProviderOperationID != apiURL(operation, monitorPrivateLinkVersion) {
			t.Fatalf("native relative LRO: %+v %v", op, err)
		}
		payload, _ := json.Marshal(op)
		json.Unmarshal(payload, &op)
		payload, _ = json.Marshal(req)
		json.Unmarshal(payload, &req)
		driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", req.Asset)
		if wait, err := driver.Wait(context.Background(), req, op); err != nil || wait.Done {
			t.Fatalf("operation success hid existing resource: %+v %v", wait, err)
		}
		for _, mutation := range []string{"operation", "protocol", "receipt", "empty-operation", "identity", "connection", "partition"} {
			var changed contracts.ActionResult
			json.Unmarshal(payloadOf(t, op), &changed)
			changedRequest := req
			switch mutation {
			case "operation":
				changed.ProviderOperationID = strings.ReplaceAll(op.ProviderOperationID, "11111111-2222-4333-8444-555555555555", "11111111-2222-4333-8444-555555555556")
			case "protocol":
				changed.Data["polling"] = "location"
			case "receipt":
				delete(changed.Data, "monitor_private_link_operation_binding")
			case "empty-operation":
				changed.ProviderOperationID = ""
			case "identity":
				changedRequest.Asset.Identity.NativeID += "-other"
			case "connection":
				changedRequest.Asset.Identity.ConnectionID = "another-connection"
			case "partition":
				changedRequest.Asset.Identity.Partition = "another-partition"
			}
			if wait, err := driver.Wait(context.Background(), changedRequest, changed); err == nil || wait.Done {
				t.Fatalf("accepted changed %s: %+v %v", mutation, wait, err)
			}
		}
		s.gone[value.Identity.NativeID] = true
		if i == 0 {
			s.gone[values[1].Identity.NativeID] = false
			if wait, err := driver.Wait(context.Background(), req, op); err == nil || wait.Done {
				t.Fatalf("scope absence hid surviving association: %+v %v", wait, err)
			}
			s.gone[values[1].Identity.NativeID] = true
		}
		if wait, err := driver.Wait(context.Background(), req, op); err != nil || !wait.Done {
			t.Fatalf("native AMPLS absence: %+v %v", wait, err)
		}
		before := len(s.deletes)
		op, err = driver.Execute(context.Background(), req)
		if err != nil || len(s.deletes) != before {
			t.Fatalf("absent AMPLS deletion was repeated: %v", err)
		}
		if wait, err := driver.Wait(context.Background(), req, op); err != nil || !wait.Done {
			t.Fatalf("absent AMPLS retry could not finish: %+v %v", wait, err)
		}
	}
	if len(s.deletes) != 3 || s.deletes[2] != values[0].Identity.NativeID {
		t.Fatalf("wrong AMPLS deletion order: %v", s.deletes)
	}
}

func TestMonitorPrivateLinkOperationStatesAndCredentialBinding(t *testing.T) {
	for _, mode := range []string{"pending", "failed", "canceled", "empty", "foreign-resource", "foreign-operation", "permission", "expired-existing", "expired-absent", "credential"} {
		t.Run(mode, func(t *testing.T) {
			s, r, values := monitorPrivateLinkScenario(t)
			target := values[1]
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			a := monitorTargetInner(driver).(*action)
			operation := "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.Insights/privateLinkScopeOperationStatuses/11111111-2222-4333-8444-555555555555"
			body := map[string]any{"status": "Succeeded", "id": operation, "resourceId": target.Identity.NativeID}
			status := 200
			switch mode {
			case "pending":
				body["status"] = "Running"
				s.gone[target.Identity.NativeID] = true
			case "failed":
				body["status"] = "Failed"
			case "canceled":
				body["status"] = "Canceled"
			case "empty":
				body = map[string]any{}
			case "foreign-resource":
				body["resourceId"] = target.Identity.NativeID + "-another"
			case "foreign-operation":
				body["id"] = operation + "1"
			case "permission":
				status = 403
			case "expired-existing", "expired-absent":
				status = 404
				s.gone[target.Identity.NativeID] = mode == "expired-absent"
			}
			op, err := a.operationResult(response{header: http.Header{"Azure-Asyncoperation": {apiURL(operation, monitorPrivateLinkVersion)}}})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "credential" {
				client := *a.client
				client.fingerprint[0] ^= 1
				a.client = &client
			}
			calls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(req.URL.Path, "/privateLinkScopeOperationStatuses/") {
					calls++
					return jsonResponse(status, body, nil), true
				}
				return nil, false
			}
			wait, err := a.Wait(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target}, op)
			switch mode {
			case "pending", "expired-existing":
				if err != nil || wait.Done {
					t.Fatalf("incomplete operation/absence accepted: %+v %v", wait, err)
				}
			case "expired-absent":
				if err != nil || !wait.Done {
					t.Fatalf("final native absence failed: %+v %v", wait, err)
				}
			default:
				if err == nil || wait.Done || (mode == "credential" && calls != 0) {
					t.Fatalf("invalid poll %s accepted: %+v %v", mode, wait, err)
				}
			}
		})
	}
}

func payloadOf(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestMonitorPrivateLinkLifecycleDrift(t *testing.T) {
	for _, mode := range []string{"access-mode", "exclusion", "opaque-config", "creation", "target", "endpoint", "parent", "capability", "missing-capability", "duplicate", "partial", "denied", "embedded", "embedded-duplicate", "proof", "parent-proof", "protected", "locked"} {
		t.Run(mode, func(t *testing.T) {
			s, r, values := monitorPrivateLinkScenario(t)
			request, _ := dnsRequest(t, r, values, values[0])
			target := values[0]
			root, child := s.records[values[0].Identity.NativeID], s.records[values[1].Identity.NativeID]
			collection := values[0].Identity.NativeID + "/scopedresources"
			membership := mode == "duplicate" || mode == "partial" || mode == "denied" || mode == "embedded" || mode == "embedded-duplicate"
			if !membership {
				// A ready scope is the baseline for configuration/proof checks;
				// existing prerequisites must not mask an ineffective drift guard.
				s.gone[values[1].Identity.NativeID], s.gone[values[2].Identity.NativeID] = true, true
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if check, err := driver.Preflight(context.Background(), request); err != nil || !check.Allowed || check.Absent {
					t.Fatalf("AMPLS baseline is not ready: %+v %v", check, err)
				}
			}
			switch mode {
			case "access-mode":
				object(object(root["properties"])["accessModeSettings"])["queryAccessMode"] = "PrivateOnly"
			case "exclusion":
				object(object(root["properties"])["accessModeSettings"])["exclusions"] = []any{map[string]any{"privateEndpointConnectionName": "connection", "queryAccessMode": "PrivateOnly"}}
			case "opaque-config":
				object(root["properties"])["configuration"] = "private-drift"
			case "creation":
				object(root["systemData"])["createdAt"] = "2026-09-10T00:00:00Z"
			case "target":
				target = values[1]
				object(child["properties"])["linkedResourceId"] = resourceID(dataCollectionEndpointType, "another")
			case "endpoint":
				target = values[2]
				object(object(s.records[target.Identity.NativeID]["properties"])["privateEndpoint"])["id"] = resourceID(privateEndpointType, "another")
			case "parent":
				target = values[1]
				object(object(root["properties"])["accessModeSettings"])["ingestionAccessMode"] = "PrivateOnly"
			case "capability":
				object(s.records[values[0].Identity.NativeID+"/privatelinkresources/azuremonitor"]["properties"])["requiredMembers"] = []any{"changed"}
			case "missing-capability":
				s.lists[values[0].Identity.NativeID+"/privatelinkresources"] = []any{}
			case "duplicate":
				s.lists[collection] = []any{child, child}
			case "partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) {
						return jsonResponse(200, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "denied":
				s.status[collection] = 403
			case "embedded", "embedded-duplicate":
				rows := []any{}
				if mode == "embedded-duplicate" {
					rows = []any{s.records[values[2].Identity.NativeID], s.records[values[2].Identity.NativeID]}
				}
				object(root["properties"])["privateEndpointConnections"] = rows
			case "proof":
				delete(target.Normalized, "_monitor_private_link_private_configuration")
			case "parent-proof":
				target = values[1]
				delete(target.Normalized, "_monitor_private_link_parent_configuration")
			case "protected":
				target = values[1]
				child["tags"] = map[string]any{"steward/protected": "true"}
			case "locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": values[0].Identity.NativeID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			if membership {
				c, _ := r.resolve(context.Background(), "connection")
				if children, err := c.monitorPrivateLinkChildren(context.Background(), values[0].Identity, root); err == nil {
					t.Fatalf("accepted changed native membership %s: %+v", mode, children)
				}
				return
			}
			if target.ID != values[0].ID {
				s.gone[target.Identity.NativeID] = false
				request = contracts.ActionRequest{Action: "delete", Asset: target}
			} else {
				request.Asset = target
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("AMPLS drift %s allowed a write: %v %v", mode, s.deletes, err)
			}
		})
	}
}
