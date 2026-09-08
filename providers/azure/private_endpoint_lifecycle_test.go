package azure

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func privateEndpointScenario() (*dnsScenario, []map[string]any) {
	s, raw := privateDNSGroupScenario()
	endpointID := resourceID(privateEndpointType, "endpoint")
	nicID := resourceID(nicType, "private-endpoint.nic")
	endpoint := map[string]any{"id": endpointID, "name": "endpoint", "type": privateEndpointType, "etag": "endpoint-etag", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "networkInterfaces": []any{map[string]any{"id": nicID}}}}
	nic := map[string]any{"id": nicID, "name": "private-endpoint.nic", "etag": "nic-etag", "location": "eastus", "properties": map[string]any{"resourceGuid": "nic-creation-guid", "privateEndpoint": map[string]any{"id": endpointID}, "ipConfigurations": []any{map[string]any{"name": "config", "properties": map[string]any{"privateIPAddress": "10.1.0.4"}}}}}
	s.add(endpoint, "2024-05-01")
	s.add(nic, "2024-05-01")
	s.lists[strings.ToLower(endpointID+"/privateDnsZoneGroups")] = []any{raw[0]}
	return s, []map[string]any{endpoint, nic, raw[0], raw[2], raw[3], raw[1]}
}

func TestPrivateEndpointReviewedNativeNICAndDNSCascade(t *testing.T) {
	s, raw := privateEndpointScenario()
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, value := range raw {
		assets = append(assets, dnsAsset(t, r, value))
	}
	if assets[1].Normalized["cleanup_controller_only"] != true {
		t.Fatal("managed NIC is directly actionable")
	}
	request, input := dnsRequest(t, r, assets, assets[0])
	if len(request.LifecycleImpacts) != 4 {
		t.Fatalf("endpoint lost descendants: %+v", request.LifecycleImpacts)
	}
	owners := map[asset.AssetID]asset.AssetID{}
	for _, impact := range request.LifecycleImpacts {
		owners[impact.Asset.ID] = impact.ControllerID
	}
	if owners[assets[1].ID] != assets[0].ID || owners[assets[2].ID] != assets[0].ID || owners[assets[3].ID] != assets[2].ID || owners[assets[4].ID] != assets[2].ID {
		t.Fatalf("wrong lifecycle tree: %v", owners)
	}
	for _, child := range []asset.Asset{assets[1], assets[2], assets[3]} {
		input.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{child.Identity.NativeID}}}
		solved, err := plan.Solve(input)
		if err != nil || len(solved.Blockers) == 0 {
			t.Fatalf("retention lost %s %+v %v", child.Identity.NativeID, solved, err)
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 || s.deletes[0] != strings.ToLower(text(raw[0]["id"])) {
		t.Fatalf("endpoint native delete: %v %v", s.deletes, err)
	}
	serialized, _ := json.Marshal(request)
	if err := json.Unmarshal(serialized, &request); err != nil {
		t.Fatal(err)
	}
	driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		wait, err := driver.Wait(context.Background(), request, result)
		if err != nil || wait.Done {
			t.Fatalf("completed before child %d disappeared: %+v %v", i, wait, err)
		}
		if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
			t.Fatalf("restart rewrote missing endpoint: %v %v", s.deletes, err)
		}
		s.gone[strings.ToLower(text(raw[i]["id"]))] = true
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("cascade readback %+v %v", wait, err)
	}
	if s.gone[strings.ToLower(text(raw[5]["id"]))] {
		t.Fatal("endpoint removed shared zone")
	}
}

func TestPrivateEndpointRejectsUnreviewedOrChangedNICOwnership(t *testing.T) {
	for _, mode := range []string{"missing NIC impact", "missing group impact", "missing record impact", "retain NIC", "foreign NIC controller", "foreign DNS controller", "planned NIC owner", "parent generation", "NIC generation", "NIC creation identity", "NIC owner", "NIC VM owner", "NIC type", "NIC identity", "missing NIC collection", "duplicate NIC", "foreign subscription", "unknown NIC", "NIC forbidden", "missing IP configs", "public IP delete", "public IP detach", "NIC protected", "NIC managed", "NIC group managed", "NIC lock", "new DNS group", "DNS records changed"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := privateEndpointScenario()
			r := s.runtime(t)
			assets := []asset.Asset{}
			for _, value := range raw[:5] {
				assets = append(assets, dnsAsset(t, r, value))
			}
			request, _ := dnsRequest(t, r, assets, assets[0])
			ni := -1
			gi := -1
			ri := -1
			for i, impact := range request.LifecycleImpacts {
				switch impact.Asset.ID {
				case assets[1].ID:
					ni = i
				case assets[2].ID:
					gi = i
				case assets[3].ID:
					ri = i
				}
			}
			parentProps, nicProps := object(raw[0]["properties"]), object(raw[1]["properties"])
			switch mode {
			case "missing NIC impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts[:ni], request.LifecycleImpacts[ni+1:]...)
			case "missing group impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts[:gi], request.LifecycleImpacts[gi+1:]...)
			case "missing record impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts[:ri], request.LifecycleImpacts[ri+1:]...)
			case "retain NIC":
				request.LifecycleImpacts[ni].Delete = false
			case "foreign NIC controller":
				request.LifecycleImpacts[ni].ControllerID = assets[2].ID
			case "foreign DNS controller":
				request.LifecycleImpacts[ri].ControllerID = assets[0].ID
			case "planned NIC owner":
				request.LifecycleImpacts[ni].Asset.Normalized["privateEndpoint"] = map[string]any{"id": resourceID(privateEndpointType, "foreign")}
			case "parent generation":
				raw[0]["etag"] = "new-endpoint"
			case "NIC generation":
				raw[1]["etag"] = "new-nic"
			case "NIC creation identity":
				nicProps["resourceGuid"] = "new-guid"
			case "NIC owner":
				nicProps["privateEndpoint"] = map[string]any{"id": resourceID(privateEndpointType, "foreign")}
			case "NIC VM owner":
				nicProps["virtualMachine"] = map[string]any{"id": resourceID(vmType, "foreign")}
			case "NIC type":
				raw[1]["type"] = vnetType
			case "NIC identity":
				raw[1]["id"] = resourceID(nicType, "foreign")
			case "missing NIC collection":
				delete(parentProps, "networkInterfaces")
			case "duplicate NIC":
				parentProps["networkInterfaces"] = append(parentProps["networkInterfaces"].([]any), parentProps["networkInterfaces"].([]any)[0])
			case "foreign subscription":
				parentProps["networkInterfaces"] = []any{map[string]any{"id": strings.Replace(text(raw[1]["id"]), testSubscription, "foreign", 1)}}
			case "unknown NIC":
				parentProps["networkInterfaces"] = []any{map[string]any{"id": resourceID(nicType, "new-nic")}}
				s.status[strings.ToLower(resourceID(nicType, "new-nic"))] = 404
			case "NIC forbidden":
				s.status[strings.ToLower(text(raw[1]["id"]))] = 403
			case "missing IP configs":
				delete(nicProps, "ipConfigurations")
			case "public IP delete", "public IP detach":
				policy := "Delete"
				if mode == "public IP detach" {
					policy = "Detach"
				}
				object(object(nicProps["ipConfigurations"].([]any)[0])["properties"])["publicIPAddress"] = map[string]any{"id": resourceID("Microsoft.Network/publicIPAddresses", "unexpected"), "properties": map[string]any{"deleteOption": policy}}
			case "NIC protected":
				raw[1]["tags"] = map[string]any{"steward:protected": "true"}
			case "NIC managed":
				raw[1]["managedBy"] = resourceID(aksType, "other-cluster")
			case "NIC group managed":
				s.add(map[string]any{"id": "/subscriptions/" + testSubscription + "/resourceGroups/test", "managedBy": resourceID(aksType, "other-cluster")}, resourcesVersion)
			case "NIC lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": text(raw[1]["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "new DNS group":
				extra := map[string]any{"id": text(raw[0]["id"]) + "/privateDnsZoneGroups/another", "properties": map[string]any{"privateDnsZoneConfigs": []any{}}}
				s.add(extra, "2024-05-01")
				s.lists[strings.ToLower(text(raw[0]["id"])+"/privateDnsZoneGroups")] = append(s.lists[strings.ToLower(text(raw[0]["id"])+"/privateDnsZoneGroups")], extra)
			case "DNS records changed":
				object(raw[3]["properties"])["ttl"] = 120
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("%s allowed writes %v %v", mode, s.deletes, err)
			}
		})
	}
}

func TestPrivateEndpointManagedNICCannotDeleteIndependently(t *testing.T) {
	s, raw := privateEndpointScenario()
	r := s.runtime(t)
	value := dnsAsset(t, r, raw[1])
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatalf("managed NIC delete allowed: %v", err)
	}
}
