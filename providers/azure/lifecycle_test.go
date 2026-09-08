package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type offlineSchemaLoader struct{}

func (offlineSchemaLoader) Load(uri string) (any, error) {
	return nil, fmt.Errorf("official schema was not included in the catalog snapshot: %s", uri)
}

func TestRetentionWritesValidateAgainstOfficialAzureSchemas(t *testing.T) {
	payload, err := os.ReadFile("catalog/source/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources catalog.RESTDocumentSet
	if err := json.Unmarshal(payload, &sources); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	locations := map[string]string{}
	for _, source := range sources.Documents {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(source.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(source.SourceURI, document); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(source.SourceURI, "/networkInterface.json") {
			locations[nicType] = source.SourceURI + "#/definitions/NetworkInterface"
		}
		if strings.HasSuffix(source.SourceURI, "/virtualMachine.json") {
			locations[vmType] = source.SourceURI + "#/definitions/VirtualMachineUpdate"
		}
	}
	resources := attachmentResources()
	assets := attachmentAssets(t, resources)
	for _, value := range assets {
		uri := locations[value.Identity.NativeType]
		if uri == "" {
			continue
		}
		attachments, err := resourceAttachments(testSubscription, value.Identity.NativeType, value.Normalized)
		if err != nil {
			t.Fatal(err)
		}
		body, err := (attachmentUpdate{asset: value, live: resources[string(value.ID)], retain: attachments}).body()
		if err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile(uri)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body)
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(document); err != nil {
			t.Fatalf("retention write violates official %s schema: %v", value.Identity.NativeType, err)
		}
	}
}

func attachmentResources() map[string]map[string]any {
	resources := map[string]map[string]any{
		"vm": nativeResource(vmType, "vm", "eastus", map[string]any{
			"provisioningState": "Succeeded", "hardwareProfile": map[string]any{"vmSize": "Standard_D2s_v3"},
			"storageProfile": map[string]any{
				"osDisk":    map[string]any{"name": "boot", "createOption": "FromImage", "managedDisk": map[string]any{"id": resourceID(diskType, "boot")}, "deleteOption": "Delete"},
				"dataDisks": []any{map[string]any{"name": "data", "lun": 0, "createOption": "Attach", "managedDisk": map[string]any{"id": resourceID(diskType, "data")}, "deleteOption": "Delete", "diskIOPSReadWrite": 500}},
			},
			"networkProfile": map[string]any{"networkInterfaces": []any{map[string]any{"id": resourceID(nicType, "nic"), "properties": map[string]any{"primary": true, "deleteOption": "Delete"}}}},
		}),
		"boot": nativeResource(diskType, "boot", "eastus", map[string]any{"provisioningState": "Succeeded"}),
		"data": nativeResource(diskType, "data", "eastus", map[string]any{"provisioningState": "Succeeded"}),
		"nic": nativeResource(nicType, "nic", "eastus", map[string]any{
			"provisioningState": "Succeeded", "virtualMachine": map[string]any{"id": resourceID(vmType, "vm")}, "macAddress": "00-11-22-33-44-55", "primary": true,
			"enableAcceleratedNetworking": true, "enableIPForwarding": true, "auxiliaryMode": "None", "auxiliarySku": "None",
			"dnsSettings":          map[string]any{"dnsServers": []any{"10.0.0.4"}, "internalDnsNameLabel": "app", "internalFqdn": "app.internal", "appliedDnsServers": []any{"10.0.0.4"}},
			"networkSecurityGroup": map[string]any{"id": resourceID("Microsoft.Network/networkSecurityGroups", "nsg"), "properties": map[string]any{"provisioningState": "Succeeded"}},
			"ipConfigurations": []any{map[string]any{"name": "primary", "id": resourceID(nicType, "nic") + "/ipConfigurations/primary", "etag": "ip-etag", "properties": map[string]any{
				"primary": true, "privateIPAddress": "10.0.0.5", "privateIPAllocationMethod": "Static", "privateIPAddressVersion": "IPv4", "provisioningState": "Succeeded",
				"subnet":                          map[string]any{"id": resourceID(vnetType, "vnet") + "/subnets/default", "properties": map[string]any{"provisioningState": "Succeeded"}},
				"loadBalancerBackendAddressPools": []any{map[string]any{"id": resourceID("Microsoft.Network/loadBalancers", "lb") + "/backendAddressPools/backend"}},
				"publicIPAddress":                 map[string]any{"id": resourceID("Microsoft.Network/publicIPAddresses", "ip"), "properties": map[string]any{"deleteOption": "Delete", "provisioningState": "Succeeded", "publicIPAllocationMethod": "Static"}},
			}}},
		}),
		"ip": nativeResource("Microsoft.Network/publicIPAddresses", "ip", "eastus", map[string]any{"provisioningState": "Succeeded"}),
	}
	resources["vm"]["etag"] = `"vm-etag"`
	resources["nic"]["etag"] = `W/"nic-etag"`
	resources["nic"]["tags"] = map[string]any{"owner": "fixture"}
	resources["boot"]["managedBy"] = resourceID(vmType, "vm")
	resources["data"]["managedBy"] = resourceID(vmType, "vm")
	return resources
}

func attachmentAssets(t *testing.T, resources map[string]map[string]any) []asset.Asset {
	t.Helper()
	var assets []asset.Asset
	for _, name := range []string{"vm", "boot", "data", "nic", "ip"} {
		raw := resources[name]
		payload, err := json.Marshal(raw["properties"])
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		if err := json.Unmarshal(payload, &normalized); err != nil {
			t.Fatal(err)
		}
		normalized["subscription_id"] = testSubscription
		id, kind, err := parseID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		rule, _ := findType(kind)
		assets = append(assets, asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure-public", ConnectionID: "connection", NativeType: rule.NativeType, NativeID: id, ScopeKey: "eastus"}, Normalized: normalized, Location: "eastus", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	return assets
}

func attachmentPlanRequest(t *testing.T, retained ...string) (contracts.ActionRequest, plan.Result) {
	t.Helper()
	assets := attachmentAssets(t, attachmentResources())
	contribution, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Solve(plan.Input{CleanupTaskID: "delete-vm", Assets: assets, ResolvedAssetIDs: []asset.AssetID{"vm", "boot", "data", "nic", "ip"}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings, RequestOptions: map[asset.AssetID]map[string]any{"vm": {"retain_resources": retained}}})
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("plan=%+v err=%v", result, err)
	}
	request := contracts.ActionRequest{Asset: assets[0], Action: "delete", IdempotencyKey: "vm-delete-job"}
	for _, impact := range result.ImpactItems {
		for _, value := range assets {
			if value.ID == impact.AssetID {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
			}
		}
	}
	return request, result
}

func TestAzureAttachmentsIncludeNestedImpactAndTransitiveRetention(t *testing.T) {
	for _, retained := range []string{"", "nic", "ip", "boot", strings.ToLower(resourceID(diskType, "boot"))} {
		t.Run(retained, func(t *testing.T) {
			request, result := attachmentPlanRequest(t, retained)
			if len(result.ImpactItems) != 4 {
				t.Fatalf("missing cascade impact: %+v", result)
			}
			for _, impact := range request.LifecycleImpacts {
				shouldRetain := string(impact.Asset.ID) == retained || strings.EqualFold(impact.Asset.Identity.NativeID, retained) || retained == "nic" && impact.Asset.ID == "ip"
				if impact.Delete == shouldRetain {
					t.Fatalf("wrong cascade retention: %+v", impact)
				}
			}
		})
	}
}

func TestAzureAttachmentDefaultsUnresolvedAndSubscriptionBoundaries(t *testing.T) {
	for _, mode := range []string{"detached", "missing", "foreign-connection", "foreign-subscription", "duplicate", "unmanaged-delete", "ephemeral", "invalid-policy"} {
		t.Run(mode, func(t *testing.T) {
			resources := attachmentResources()
			assets := attachmentAssets(t, resources)
			os := object(object(assets[0].Normalized["storageProfile"])["osDisk"])
			wantError := false
			switch mode {
			case "detached":
				delete(os, "deleteOption")
			case "missing":
				assets = append(assets[:1], assets[2:]...)
			case "foreign-connection":
				assets[1].Identity.ConnectionID = "other"
			case "foreign-subscription":
				object(os["managedDisk"])["id"] = strings.Replace(resourceID(diskType, "boot"), testSubscription, testTenant, 1)
				wantError = true
			case "duplicate":
				assets = append(assets, assets[1])
				wantError = true
			case "unmanaged-delete":
				delete(os, "managedDisk")
				os["vhd"] = map[string]any{"uri": "https://fixture.blob.core.windows.net/vhds/os.vhd"}
				wantError = true
			case "ephemeral":
				delete(os, "managedDisk")
				os["diffDiskSettings"] = map[string]any{"option": "Local"}
			case "invalid-policy":
				os["deleteOption"] = true
				wantError = true
			}
			result, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
			if (err != nil) != wantError {
				t.Fatalf("contribution=%+v err=%v", result, err)
			}
			if mode == "missing" || mode == "foreign-connection" {
				if len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != strings.ToLower(resourceID(diskType, "boot")) {
					t.Fatalf("unresolved attachment lost: %+v", result)
				}
			}
			if mode == "detached" || mode == "ephemeral" {
				if len(result.Bindings) != 3 {
					t.Fatalf("detached/ephemeral disk delegated: %+v", result)
				}
			}
		})
	}
}

func TestAzureRetentionSurvivesWorkerRestartAndPreservesNICSettings(t *testing.T) {
	request, _ := attachmentPlanRequest(t, "boot", "data", "ip")
	live := attachmentResources()
	type pending struct {
		target string
		polls  int
		apply  func()
	}
	operations := map[string]*pending{}
	mutations := []string{}
	deleted := false
	runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
		if operation := operations[r.URL.String()]; operation != nil {
			operation.polls++
			state := "Running"
			if operation.polls > 1 {
				state = "Succeeded"
				operation.apply()
			}
			return jsonResponse(200, map[string]any{"status": state}, nil), nil
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/locks") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resourcegroups/test") {
			return jsonResponse(200, map[string]any{}, nil), nil
		}
		for name, resource := range live {
			if !strings.EqualFold(r.URL.Path, text(resource["id"])) {
				continue
			}
			if r.Method == "GET" {
				if deleted && name == "vm" {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				return jsonResponse(200, resource, nil), nil
			}
			mutations = append(mutations, r.Method+" "+name)
			var apply func()
			switch r.Method + " " + name {
			case "PUT nic":
				if r.URL.Query().Get("api-version") != "2024-05-01" {
					t.Fatal("unreviewed network API version")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				props := object(body["properties"])
				ip := object(object(array(props["ipConfigurations"])[0])["properties"])
				if props["enableAcceleratedNetworking"] != true || props["enableIPForwarding"] != true || ip["privateIPAddress"] != "10.0.0.5" || ip["privateIPAllocationMethod"] != "Static" || object(body["tags"])["owner"] != "fixture" {
					t.Fatalf("NIC settings were lost: %v", body)
				}
				if !reflect.DeepEqual(object(props["dnsSettings"])["dnsServers"], []any{"10.0.0.4"}) || object(props["dnsSettings"])["internalDnsNameLabel"] != "app" || len(array(ip["loadBalancerBackendAddressPools"])) != 1 {
					t.Fatal("NIC DNS/backend settings lost")
				}
				if props["virtualMachine"] != nil || props["macAddress"] != nil || ip["provisioningState"] != nil || object(props["dnsSettings"])["internalFqdn"] != nil {
					t.Fatal("read-only NIC fields submitted")
				}
				if object(object(ip["publicIPAddress"])["properties"])["deleteOption"] != "Detach" || text(object(ip["subnet"])["id"]) == "" {
					t.Fatal("public IP retention/subnet lost")
				}
				apply = func() {
					object(object(object(array(object(live["nic"]["properties"])["ipConfigurations"])[0])["properties"])["publicIPAddress"])["properties"] = map[string]any{"deleteOption": "Detach"}
				}
			case "PATCH vm":
				if r.Header.Get("If-Match") != `"vm-etag"` {
					t.Fatal("VM conditional update missing")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				props := object(body["properties"])
				storage := object(props["storageProfile"])
				if object(storage["osDisk"])["deleteOption"] != "Detach" || object(array(storage["dataDisks"])[0])["deleteOption"] != "Detach" || object(array(storage["dataDisks"])[0])["diskIOPSReadWrite"] != nil || props["networkProfile"] != nil {
					t.Fatalf("incorrect VM retention patch: %+v", body)
				}
				apply = func() {
					storage := object(object(live["vm"]["properties"])["storageProfile"])
					object(storage["osDisk"])["deleteOption"] = "Detach"
					object(array(storage["dataDisks"])[0])["deleteOption"] = "Detach"
				}
			case "DELETE vm":
				if object(object(object(live["vm"]["properties"])["storageProfile"])["osDisk"])["deleteOption"] != "Detach" {
					t.Fatal("deleted VM before disk retention")
				}
				if fmt.Sprint(mutations) != "[PUT nic PATCH vm DELETE vm]" {
					t.Fatalf("wrong operation ordering: %v", mutations)
				}
				apply = func() { deleted = true }
			default:
				t.Fatalf("unexpected mutation %s %s", r.Method, name)
			}
			suffix := ":retain:" + strings.ToLower(text(resource["id"]))
			if r.Method == "DELETE" {
				suffix = ""
			}
			if r.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey+suffix) {
				t.Fatal("wrong correlation key")
			}
			namespace := "Microsoft.Compute"
			if name == "nic" {
				namespace = "Microsoft.Network"
			}
			endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/"+namespace+"/locations/eastus/operations/"+fmt.Sprint(len(mutations)), "2024-05-01")
			operations[endpoint] = &pending{target: name, apply: apply}
			return jsonResponse(202, map[string]any{}, http.Header{"Azure-Asyncoperation": {endpoint}}), nil
		}
		return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
	})
	newDriver := func() contracts.ActionDriver {
		driver, err := runtime.ResolveAction(context.Background(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	result, err := newDriver().Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	done := false
	for i := 0; i < 12; i++ {
		encoded, _ := json.Marshal(result)
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		wait, err := newDriver().Wait(context.Background(), request, result)
		if err != nil {
			t.Fatal(err)
		}
		if wait.Data != nil {
			result.Data = wait.Data
		}
		if wait.Done {
			done = true
			break
		}
	}
	if !done || !deleted || fmt.Sprint(mutations) != "[PUT nic PATCH vm DELETE vm]" {
		t.Fatalf("done=%v mutations=%v", done, mutations)
	}
}

func TestAzureCascadePreflightRejectsMissingImpactDriftAndLiveLocks(t *testing.T) {
	for _, mode := range []string{"missing-impact", "missing-nested-impact", "changed-nic-ip", "changed-delete-option", "nested-lock", "child-permission", "child-managed-group", "foreign-impact", "malformed-vm", "unknown-nic-field"} {
		t.Run(mode, func(t *testing.T) {
			request, _ := attachmentPlanRequest(t, "ip")
			live := attachmentResources()
			locks := []any{}
			switch mode {
			case "missing-impact":
				request.LifecycleImpacts = nil
			case "missing-nested-impact":
				for i, value := range request.LifecycleImpacts {
					if value.Asset.ID == "ip" {
						request.LifecycleImpacts = append(request.LifecycleImpacts[:i], request.LifecycleImpacts[i+1:]...)
						break
					}
				}
			case "changed-nic-ip":
				object(object(array(object(live["nic"]["properties"])["ipConfigurations"])[0])["properties"])["publicIPAddress"] = map[string]any{"id": resourceID("Microsoft.Network/publicIPAddresses", "new"), "properties": map[string]any{"deleteOption": "Delete"}}
			case "changed-delete-option":
				object(object(object(live["vm"]["properties"])["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
			case "nested-lock":
				locks = []any{map[string]any{"id": resourceID(nicType, "nic") + "/providers/Microsoft.Authorization/locks/lock", "properties": map[string]any{"level": "ReadOnly"}}}
			case "foreign-impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "another"
			case "malformed-vm":
				object(live["vm"]["properties"])["storageProfile"] = map[string]any{"osDisk": map[string]any{"deleteOption": "Delete"}}
			case "unknown-nic-field":
				object(live["nic"]["properties"])["futureSetting"] = "do-not-erase"
			}
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("mutated resource after failed cascade validation")
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/locks") {
					return jsonResponse(200, map[string]any{"value": locks}, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resourcegroups/test") {
					group := map[string]any{}
					if mode == "child-managed-group" {
						group["managedBy"] = resourceID("Microsoft.ContainerService/managedClusters", "cluster")
					}
					return jsonResponse(200, group, nil), nil
				}
				for name, resource := range live {
					if strings.EqualFold(text(resource["id"]), r.URL.Path) {
						if mode == "child-permission" && name == "nic" {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), nil
						}
						return jsonResponse(200, resource, nil), nil
					}
				}
				return nil, fmt.Errorf("unexpected request %s", r.URL)
			})
			driver, err := runtime.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("unsafe cascade allowed")
			}
		})
	}
}

func TestAzureRetainedNICWaitsForReadbackAndKeepsPublicIPWithoutMutation(t *testing.T) {
	for _, protocol := range []string{"synchronous", "async", "failed"} {
		t.Run(protocol, func(t *testing.T) {
			request, _ := attachmentPlanRequest(t, "nic")
			live := attachmentResources()
			updates, deletes := 0, 0
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Compute/locations/eastus/operations/retain", "2024-07-01")
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "PATCH" {
					updates++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					nic := object(array(object(object(body["properties"])["networkProfile"])["networkInterfaces"])[0])
					if object(nic["properties"])["deleteOption"] != "Detach" || object(nic["properties"])["primary"] != true {
						t.Fatalf("NIC retention patch lost attachment settings: %v", body)
					}
					headers := http.Header{}
					if protocol != "synchronous" {
						headers.Set("Azure-AsyncOperation", operation)
					}
					return jsonResponse(200, live["vm"], headers), nil
				}
				if r.Method == "PUT" {
					t.Fatal("retaining the NIC must retain its public IP without changing the NIC")
				}
				if r.Method == "DELETE" {
					deletes++
					return jsonResponse(204, nil, nil), nil
				}
				if r.URL.String() == operation {
					state := "Succeeded"
					if protocol == "failed" {
						state = "Failed"
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/locks") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resourcegroups/test") {
					return jsonResponse(200, map[string]any{}, nil), nil
				}
				for _, resource := range live {
					if strings.EqualFold(r.URL.Path, text(resource["id"])) {
						return jsonResponse(200, resource, nil), nil
					}
				}
				return nil, fmt.Errorf("unexpected request %s", r.URL)
			})
			driver, err := runtime.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if protocol == "failed" {
				if err == nil || deletes != 0 || updates != 1 {
					t.Fatalf("failed preparation ignored: wait=%+v err=%v", wait, err)
				}
				return
			}
			if err != nil || wait.Done || updates != 1 || deletes != 0 {
				t.Fatalf("mutation repeated before readback: wait=%+v err=%v updates=%d deletes=%d", wait, err, updates, deletes)
			}
			object(object(array(object(object(live["vm"]["properties"])["networkProfile"])["networkInterfaces"])[0])["properties"])["deleteOption"] = "Detach"
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done || text(wait.Data["phase"]) != "delete" || updates != 1 || deletes != 1 {
				t.Fatalf("retention did not advance: wait=%+v err=%v updates=%d deletes=%d", wait, err, updates, deletes)
			}
		})
	}
}
