package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func prerequisiteScenario(t *testing.T, kind string) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	version := "2024-05-01"
	if strings.HasPrefix(kind, "Microsoft.Compute/") {
		version = "2024-07-01"
	}
	parent := nativeResource(kind, "parent", "eastus", map[string]any{"provisioningState": "Succeeded", "resourceGuid": "parent-uid"})
	parent["etag"] = "parent-etag"
	parent["systemData"] = map[string]any{"createdAt": "2026-01-01T00:00:00Z", "lastModifiedAt": "2026-01-02T00:00:00Z"}
	s.add(parent, version)
	raw := []map[string]any{parent}
	for _, childKind := range servicePrerequisiteRules[kind] {
		collection := strings.ToLower(text(parent["id"]) + "/" + last(childKind))
		s.lists[collection] = []any{}
		s.version[collection] = version
		for _, name := range []string{"one", "two"} {
			child := map[string]any{"id": text(parent["id"]) + "/" + last(childKind) + "/" + name, "type": childKind, "name": name, "etag": name + "-etag", "properties": map[string]any{"provisioningState": "Succeeded"}}
			s.add(child, version)
			s.lists[collection] = append(s.lists[collection], child)
			raw = append(raw, child)
			if childKind == vpnConnectionType {
				link := map[string]any{"id": text(child["id"]) + "/vpnLinkConnections/link", "name": "link", "etag": "link-etag", "properties": map[string]any{"provisioningState": "Succeeded", "sharedKey": "sensitive-link-key"}}
				s.add(link, version)
				raw = append(raw, link)
				linkList := strings.ToLower(text(child["id"]) + "/vpnLinkConnections")
				s.lists[linkList], s.version[linkList] = []any{link}, version
			}
		}
	}
	field := map[string]string{hostGroupType: "hosts", capacityGroupType: "capacityReservations", vpnGatewayType: "connections", expressGatewayType: "expressRouteConnections"}[kind]
	object(parent["properties"])[field] = []any{map[string]any{"id": raw[1]["id"]}}
	root := "/subscriptions/" + testSubscription
	s.lists[strings.ToLower(root+"/providers/"+kind)] = []any{parent}
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test", "type": groupType, "location": "eastus"}}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, record := range raw {
		assets = append(assets, dnsAsset(t, r, record))
	}
	return s, r, assets
}

func TestNativePrerequisitesPlanExecutionAndRestart(t *testing.T) {
	for _, kind := range []string{hostGroupType, capacityGroupType, vpnGatewayType, expressGatewayType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := prerequisiteScenario(t, kind)
			root := assets[0]
			request, input := dnsRequest(t, r, assets, root)
			result, err := plan.Solve(input)
			if err != nil || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != len(servicePrerequisiteRules[kind])*2 {
				t.Fatalf("missing native prerequisite steps: %+v %+v %v", request, result, err)
			}
			var parentStep plan.CleanupTaskStep
			for _, step := range result.Steps {
				if step.AssetID == root.ID {
					parentStep = step
				}
			}
			for _, step := range result.Steps {
				if step.AssetID != root.ID && !slices.Contains(parentStep.DependsOn, step.ID) {
					t.Fatalf("parent does not wait for child %+v", step)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("parent deleted before native child absence")
			}
			for _, prerequisite := range request.PrerequisiteDeletions {
				childRequest := servicePlanRequest(result, assets, prerequisite.Asset)
				childDriver, err := r.ResolveAction(context.Background(), "connection", prerequisite.Asset)
				if err != nil {
					t.Fatal(err)
				}
				operation, err := childDriver.Execute(context.Background(), childRequest)
				if err != nil {
					t.Fatal(err)
				}
				s.gone[prerequisite.Asset.Identity.NativeID] = true
				if len(childRequest.LifecycleImpacts) > 0 {
					wait, err := childDriver.Wait(context.Background(), childRequest, operation)
					if err != nil || wait.Done {
						t.Fatalf("child cascade completed before link absence: %+v %v", wait, err)
					}
					for _, impact := range childRequest.LifecycleImpacts {
						s.gone[impact.Asset.Identity.NativeID] = true
					}
				}
				wait, err := childDriver.Wait(context.Background(), childRequest, operation)
				if err != nil || !wait.Done {
					t.Fatalf("child did not complete: %+v %v", wait, err)
				}
			}
			live := s.records[root.Identity.NativeID]
			live["etag"] = "changed-by-child-deletions"
			object(live["systemData"])["lastModifiedAt"] = "2026-01-03T00:00:00Z"
			field := map[string]string{hostGroupType: "hosts", capacityGroupType: "capacityReservations", vpnGatewayType: "connections", expressGatewayType: "expressRouteConnections"}[kind]
			object(live["properties"])[field] = []any{}
			encoded, _ := json.Marshal(request)
			var restored contracts.ActionRequest
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", restored.Asset)
			operation, err := driver.Execute(context.Background(), restored)
			if err != nil || len(s.deletes) != len(request.PrerequisiteDeletions)+1 {
				t.Fatalf("parent resume failed: %+v %v", operation, err)
			}
			s.gone[root.Identity.NativeID] = true
			// A native child reappearing after the root disappears cannot be
			// reported as successful cleanup, including after serialized resume.
			childID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
			s.gone[childID] = false
			if wait, err := driver.Wait(context.Background(), restored, operation); err == nil || wait.Done {
				t.Fatalf("recreated prerequisite accepted: %+v %v", wait, err)
			}
			s.gone[childID] = true
			if wait, err := driver.Wait(context.Background(), restored, operation); err != nil || !wait.Done {
				t.Fatalf("parent completion=%+v %v", wait, err)
			}
			before := len(s.deletes)
			if _, err := driver.Execute(context.Background(), restored); err != nil || len(s.deletes) != before {
				t.Fatalf("resumed parent repeated DELETE: %v", err)
			}
		})
	}
}

func TestNativePrerequisitesRejectRetentionAndDrift(t *testing.T) {
	for _, mode := range []string{"retention", "missing-inventory", "wrong-incarnation", "new-child", "list-403", "list-404", "list-206", "detail-403", "detail-206", "foreign-detail", "duplicate", "foreign-prerequisite", "wrong-kind", "wrong-connection", "wrong-partition", "wrong-controller", "duplicate-prerequisite", "retain-prerequisite", "missing-prerequisite-proof", "parent-configuration", "parent-createdAt", "parent-uid", "protected-parent", "locked-parent", "absent-prerequisite-read-403"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := prerequisiteScenario(t, vpnGatewayType)
			root := assets[0]
			if mode == "missing-inventory" || mode == "wrong-incarnation" {
				if mode == "missing-inventory" {
					assets = assets[:len(assets)-1]
				} else {
					assets[1].Normalized["_arm_generation"] = "recreated"
				}
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				result, err := contributor.Contribute(context.Background(), "scope", assets)
				if err == nil && len(result.Unresolved) == 0 {
					t.Fatal("incomplete inventory accepted")
				}
				return
			}
			request, input := dnsRequest(t, r, assets, root)
			if mode == "retention" {
				input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{assets[1].Identity.NativeID}}}
				result, err := plan.Solve(input)
				if err != nil || len(result.Blockers) == 0 {
					t.Fatalf("native required child retention accepted: %+v %v", result, err)
				}
				return
			}
			child := request.PrerequisiteDeletions[0].Asset
			collection := child.Identity.NativeID[:strings.LastIndex(child.Identity.NativeID, "/")]
			// The remaining failures run after all originally reviewed child steps.
			for _, value := range assets[1:] {
				s.gone[value.Identity.NativeID] = true
			}
			live := s.records[root.Identity.NativeID]
			live["etag"] = "after-prerequisites"
			switch mode {
			case "new-child", "duplicate", "detail-403", "detail-206", "foreign-detail":
				s.gone[child.Identity.NativeID] = false
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
				if mode == "duplicate" {
					s.lists[collection] = append(s.lists[collection], s.records[child.Identity.NativeID])
				}
				if mode == "detail-403" {
					s.status[child.Identity.NativeID] = 403
				}
				if mode == "detail-206" {
					s.status[child.Identity.NativeID] = 206
				}
				if mode == "foreign-detail" {
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, child.Identity.NativeID) {
							return jsonResponse(200, map[string]any{"id": child.Identity.NativeID + "-other"}, nil), true
						}
						return nil, false
					}
				}
			case "list-403":
				s.status[collection] = 403
			case "list-404":
				s.status[collection] = 404
			case "list-206":
				s.status[collection] = 206
			case "foreign-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeID = strings.Replace(child.Identity.NativeID, "/parent/", "/foreign/", 1)
			case "wrong-kind":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeType = hostType
			case "wrong-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "wrong-partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "other"
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = child.ID
			case "duplicate-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "retain-prerequisite":
				request.PrerequisiteDeletions[0].Delete = false
			case "missing-prerequisite-proof":
				request.PrerequisiteDeletions = nil
			case "parent-configuration":
				object(live["properties"])["vpnGatewayScaleUnit"] = 5
			case "parent-createdAt":
				object(live["systemData"])["createdAt"] = "2026-09-01T00:00:00Z"
			case "parent-uid":
				object(live["properties"])["resourceGuid"] = "recreated"
			case "protected-parent":
				live["tags"] = map[string]any{"steward/protected": "true"}
			case "locked-parent":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": root.Identity.NativeID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "absent-prerequisite-read-403":
				s.status[child.Identity.NativeID] = 403
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("changed prerequisite or parent authorized deletion")
			}
		})
	}
}

func TestVPNLinkNativeInventoryAndNATDeletionOrder(t *testing.T) {
	s, r, assets := prerequisiteScenario(t, vpnGatewayType)
	link, nat := assets[2], assets[len(assets)-1]
	object(s.records[link.Identity.NativeID]["properties"])["ingressNatRules"] = []any{map[string]any{"id": nat.Identity.NativeID}}
	link = dnsAsset(t, r, s.records[link.Identity.NativeID])
	assets[2] = link
	if link.Normalized["cleanup_controller_only"] != true || link.Normalized["sharedKey"] != nil {
		t.Fatal("managed link protection or secret redaction missing")
	}
	if _, err := r.ResolveAction(context.Background(), "connection", link); err == nil {
		t.Fatal("invented native link DELETE")
	}
	request := productRequest(r, vpnLinkConnectionType)
	count := 0
	for {
		batch, err := r.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range batch.Items {
			if value.NativeType != vpnLinkConnectionType || value.Location != "eastus" || value.Normalized["sharedKey"] != nil || value.Normalized["cleanup_controller_only"] != true {
				t.Fatalf("invalid native link: %+v", value)
			}
			count++
		}
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if count != 2 {
		t.Fatalf("lost nested parent inventory: %d", count)
	}
	refs := references(vpnLinkConnectionType, link.Identity.NativeID, s.records[link.Identity.NativeID])
	if !slices.Contains(refs[vpnNATRuleType], nat.Identity.NativeID) {
		t.Fatal("link NAT dependency missing")
	}
	_, input := dnsRequest(t, r, assets, assets[0])
	input.Relationships = append(input.Relationships, graph.Relationship{SourceAssetID: link.ID, TargetAssetID: nat.ID, Type: graph.RelationshipDependsOn, Source: "native-nat-reference", Confidence: 1})
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("NAT dependency plan=%+v %v", result, err)
	}
	var natStep, connectionStep plan.CleanupTaskStep
	for _, step := range result.Steps {
		if step.AssetID == nat.ID {
			natStep = step
		}
		if step.AssetID == assets[1].ID {
			connectionStep = step
		}
	}
	if !slices.Contains(natStep.DependsOn, connectionStep.ID) {
		t.Fatalf("NAT rule does not wait for owning connection: %+v", natStep)
	}
}

func TestNativeAssociationPreflightProtectsActiveComputeAndVPN(t *testing.T) {
	for _, tc := range []struct{ kind, field string }{{hostType, "virtualMachines"}, {capacityType, "virtualMachinesAssociated"}, {capacityGroupType, "virtualMachinesAssociated"}, {vpnNATRuleType, "ingressVpnSiteLinkConnections"}, {vpnNATRuleType, "egressVpnSiteLinkConnections"}} {
		for _, value := range []any{[]any{map[string]any{"id": "associated-resource"}}, "invalid-native-array"} {
			t.Run(tc.kind+"/"+tc.field, func(t *testing.T) {
				parent := strings.Join(strings.Split(tc.kind, "/")[:2], "/")
				s, r, assets := prerequisiteScenario(t, parent)
				var target asset.Asset
				for _, candidate := range assets {
					if candidate.Identity.NativeType == tc.kind {
						target = candidate
						break
					}
				}
				object(s.records[target.Identity.NativeID]["properties"])[tc.field] = value
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
					t.Fatal("associated resource deletion accepted")
				}
			})
		}
	}
}

func TestOfficialVPNLinkResponseIDAliasUsesBoundNativeRoute(t *testing.T) {
	data, err := os.ReadFile("fixtures/VpnSiteLinkConnectionGet.json")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "subid", testSubscription))
	var example map[string]any
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	raw := object(object(object(example["responses"])["200"])["body"])
	canonical := strings.ToLower(responseID(vpnLinkConnectionType, text(raw["id"])))
	connectionID := canonical[:strings.LastIndex(canonical, "/vpnlinkconnections/")]
	gatewayID := connectionID[:strings.LastIndex(connectionID, "/vpnconnections/")]
	connection := map[string]any{"id": connectionID, "type": vpnConnectionType, "name": "vpnConnection1", "properties": map[string]any{"provisioningState": "Succeeded"}}
	gateway := map[string]any{"id": gatewayID, "type": vpnGatewayType, "name": "gateway1", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	s := newDNSScenario()
	s.add(gateway, "2024-05-01")
	s.add(connection, "2024-05-01")
	// Native response ID differs only in the final collection. The transport
	// must still call /vpnLinkConnections, never /VpnSiteLinkConnections.
	s.records[canonical], s.version[canonical] = raw, "2024-05-01"
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/rg1", "type": groupType, "location": "eastus"}}
	s.lists[root+"/providers/microsoft.network/vpngateways"] = []any{gateway}
	s.lists[gatewayID+"/vpnconnections"] = []any{connection}
	s.lists[connectionID+"/vpnlinkconnections"] = []any{raw}
	s.version[connectionID+"/vpnlinkconnections"] = "2024-05-01"
	r := s.runtime(t)
	batch, err := r.List(context.Background(), productRequest(r, vpnLinkConnectionType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != canonical {
		t.Fatalf("official aliased link inventory=%+v %v", batch, err)
	}
	item := batch.Items[0]
	link := asset.Asset{ID: "link", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: vpnLinkConnectionType, NativeID: canonical}, Location: item.Location, Normalized: item.Normalized}
	parent := dnsAsset(t, r, connection)
	request, _ := dnsRequest(t, r, []asset.Asset{parent, link}, parent)
	if len(request.LifecycleImpacts) != 1 {
		t.Fatalf("aliased cascade link missing: %+v", request)
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", parent)
	operation, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(context.Background(), request, operation); err != nil || wait.Done {
		t.Fatalf("official link prematurely absent: %+v %v", wait, err)
	}
	s.gone[canonical] = true
	if wait, err := driver.Wait(context.Background(), request, operation); err != nil || !wait.Done {
		t.Fatalf("official link did not complete: %+v %v", wait, err)
	}
	for _, id := range []string{
		strings.Replace(text(raw["id"]), "/rg1/", "/other/", 1),
		strings.Replace(text(raw["id"]), "/vpnConnection1/", "/other/", 1),
		text(raw["id"]) + "-foreign",
		strings.Replace(text(raw["id"]), "/VpnSiteLinkConnections/", "/otherLinks/", 1),
	} {
		if validResourceResponse(response{status: 200, data: map[string]any{"id": id, "type": raw["type"]}}, canonical, vpnLinkConnectionType) {
			t.Fatalf("alias accepted foreign native identity: %s", id)
		}
	}
	// Canonical and aliased forms are the same resource, not two impacts.
	s.gone[canonical] = false
	s.gone[connectionID] = false
	s.lists[connectionID+"/vpnlinkconnections"] = []any{raw, map[string]any{"id": canonical, "type": vpnLinkConnectionType}}
	if _, err := r.List(context.Background(), productRequest(r, vpnLinkConnectionType)); err == nil {
		t.Fatal("duplicate canonical/alias identity accepted")
	}
}
