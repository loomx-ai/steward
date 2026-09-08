package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestNetworkAndCapacityNativeResourceWire(t *testing.T) {
	// Literal native resource and list paths exercise subscription, resource
	// group, and child routes independently of generated catalog templates.
	for _, tc := range []struct{ kind, resource, list, version, location string }{
		{"Microsoft.Compute/capacityReservationGroups", "Microsoft.Compute/capacityReservationGroups/cg", "/providers/Microsoft.Compute/capacityReservationGroups", "2024-07-01", "eastus"},
		{"Microsoft.Compute/capacityReservationGroups/capacityReservations", "Microsoft.Compute/capacityReservationGroups/cg/capacityReservations/reservation", "/resourceGroups/test/providers/Microsoft.Compute/capacityReservationGroups/cg/capacityReservations", "2024-07-01", "eastus"},
		{"Microsoft.Compute/hostGroups", "Microsoft.Compute/hostGroups/hg", "/providers/Microsoft.Compute/hostGroups", "2024-07-01", "eastus"},
		{"Microsoft.Compute/hostGroups/hosts", "Microsoft.Compute/hostGroups/hg/hosts/host", "/resourceGroups/test/providers/Microsoft.Compute/hostGroups/hg/hosts", "2024-07-01", "eastus"},
		{"Microsoft.Compute/sshPublicKeys", "Microsoft.Compute/sshPublicKeys/key", "/providers/Microsoft.Compute/sshPublicKeys", "2024-07-01", "eastus"},
		{"Microsoft.Network/azureFirewalls", "Microsoft.Network/azureFirewalls/firewall", "/providers/Microsoft.Network/azureFirewalls", "2024-05-01", "eastus"},
		{"Microsoft.Network/bastionHosts", "Microsoft.Network/bastionHosts/bastion", "/providers/Microsoft.Network/bastionHosts", "2024-05-01", "eastus"},
		{"Microsoft.Network/connections", "Microsoft.Network/connections/connection", "/resourceGroups/test/providers/Microsoft.Network/connections", "2024-05-01", "eastus"},
		{"Microsoft.Network/ddosProtectionPlans", "Microsoft.Network/ddosProtectionPlans/plan", "/providers/Microsoft.Network/ddosProtectionPlans", "2024-05-01", "eastus"},
		{"Microsoft.Network/dnsZones", "Microsoft.Network/dnsZones/example.com", "/resourceGroups/test/providers/Microsoft.Network/dnsZones", "2018-05-01", "global"},
		{"Microsoft.Network/expressRouteCircuits", "Microsoft.Network/expressRouteCircuits/circuit", "/providers/Microsoft.Network/expressRouteCircuits", "2024-05-01", "eastus"},
		{"Microsoft.Network/expressRouteCircuits/peerings", "Microsoft.Network/expressRouteCircuits/circuit/peerings/peering", "/resourceGroups/test/providers/Microsoft.Network/expressRouteCircuits/circuit/peerings", "2024-05-01", "eastus"},
		{"Microsoft.Network/expressRouteGateways", "Microsoft.Network/expressRouteGateways/gateway", "/providers/Microsoft.Network/expressRouteGateways", "2024-05-01", "eastus"},
		{"Microsoft.Network/firewallPolicies", "Microsoft.Network/firewallPolicies/policy", "/providers/Microsoft.Network/firewallPolicies", "2024-05-01", "eastus"},
		{"Microsoft.Network/localNetworkGateways", "Microsoft.Network/localNetworkGateways/local", "/resourceGroups/test/providers/Microsoft.Network/localNetworkGateways", "2024-05-01", "eastus"},
		{"Microsoft.Network/networkWatchers", "Microsoft.Network/networkWatchers/watcher", "/providers/Microsoft.Network/networkWatchers", "2024-05-01", "eastus"},
		{"Microsoft.Network/networkWatchers/connectionMonitors", "Microsoft.Network/networkWatchers/watcher/connectionMonitors/monitor", "/resourceGroups/test/providers/Microsoft.Network/networkWatchers/watcher/connectionMonitors", "2024-05-01", "eastus"},
		{"Microsoft.Network/networkWatchers/packetCaptures", "Microsoft.Network/networkWatchers/watcher/packetCaptures/capture", "/resourceGroups/test/providers/Microsoft.Network/networkWatchers/watcher/packetCaptures", "2024-05-01", "eastus"},
		{"Microsoft.Network/networkWatchers/flowLogs", "Microsoft.Network/networkWatchers/watcher/flowLogs/log", "/resourceGroups/test/providers/Microsoft.Network/networkWatchers/watcher/flowLogs", "2024-05-01", "eastus"},
		{"Microsoft.Network/privateDnsZones", "Microsoft.Network/privateDnsZones/private.example", "/providers/Microsoft.Network/privateDnsZones", "2024-06-01", "global"},
		{"Microsoft.Network/privateEndpoints", "Microsoft.Network/privateEndpoints/endpoint", "/providers/Microsoft.Network/privateEndpoints", "2024-05-01", "eastus"},
		{"Microsoft.Network/privateEndpoints/privateDnsZoneGroups", "Microsoft.Network/privateEndpoints/endpoint/privateDnsZoneGroups/default", "/resourceGroups/test/providers/Microsoft.Network/privateEndpoints/endpoint/privateDnsZoneGroups", "2024-05-01", "eastus"},
		{"Microsoft.Network/privateDnsZones/virtualNetworkLinks", "Microsoft.Network/privateDnsZones/private.example/virtualNetworkLinks/link", "/resourceGroups/test/providers/Microsoft.Network/privateDnsZones/private.example/virtualNetworkLinks", "2024-06-01", "global"},
		{"Microsoft.Network/privateLinkServices", "Microsoft.Network/privateLinkServices/service", "/providers/Microsoft.Network/privateLinkServices", "2024-05-01", "eastus"},
		{"Microsoft.Network/trafficManagerProfiles", "Microsoft.Network/trafficManagerProfiles/profile", "/providers/Microsoft.Network/trafficManagerProfiles", "2022-04-01", "global"},
		{"Microsoft.Network/virtualHubs", "Microsoft.Network/virtualHubs/hub", "/providers/Microsoft.Network/virtualHubs", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualHubs/hubRouteTables", "Microsoft.Network/virtualHubs/hub/hubRouteTables/custom", "/resourceGroups/test/providers/Microsoft.Network/virtualHubs/hub/hubRouteTables", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualHubs/hubVirtualNetworkConnections", "Microsoft.Network/virtualHubs/hub/hubVirtualNetworkConnections/connection", "/resourceGroups/test/providers/Microsoft.Network/virtualHubs/hub/hubVirtualNetworkConnections", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualHubs/routeMaps", "Microsoft.Network/virtualHubs/hub/routeMaps/map", "/resourceGroups/test/providers/Microsoft.Network/virtualHubs/hub/routeMaps", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualNetworkGateways", "Microsoft.Network/virtualNetworkGateways/gateway", "/resourceGroups/test/providers/Microsoft.Network/virtualNetworkGateways", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualNetworks/virtualNetworkPeerings", "Microsoft.Network/virtualNetworks/vnet/virtualNetworkPeerings/peer", "/resourceGroups/test/providers/Microsoft.Network/virtualNetworks/vnet/virtualNetworkPeerings", "2024-05-01", "eastus"},
		{"Microsoft.Network/virtualWans", "Microsoft.Network/virtualWans/wan", "/providers/Microsoft.Network/virtualWans", "2024-05-01", "eastus"},
		{"Microsoft.Network/vpnGateways", "Microsoft.Network/vpnGateways/vpn", "/providers/Microsoft.Network/vpnGateways", "2024-05-01", "eastus"},
		{"Microsoft.Network/vpnGateways/vpnConnections", "Microsoft.Network/vpnGateways/vpn/vpnConnections/connection", "/resourceGroups/test/providers/Microsoft.Network/vpnGateways/vpn/vpnConnections", "2024-05-01", "eastus"},
		{"Microsoft.Storage/storageAccounts/fileServices/shares", "Microsoft.Storage/storageAccounts/storage/fileServices/default/shares/share", "/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/storage/fileServices/default/shares", "2023-05-01", "eastus"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			group := root + "/resourceGroups/test"
			id := group + "/providers/" + tc.resource
			raw := map[string]any{"id": id, "name": last(id), "type": tc.kind, "location": tc.location, "etag": "native-etag", "properties": map[string]any{"provisioningState": "Succeeded"}}
			if tc.kind == privateEndpointType {
				object(raw["properties"])["networkInterfaces"] = []any{}
			}
			if tc.kind == privateDNSZoneGroupType {
				object(raw["properties"])["privateDnsZoneConfigs"] = []any{}
			}
			if tc.kind == privateDNSLinkType {
				object(raw["properties"])["registrationEnabled"] = false
			}
			if strings.Count(tc.kind, "/") > 1 {
				delete(raw, "type")
			}
			details := map[string]map[string]any{strings.ToLower(group): {"id": group, "name": "test", "location": "eastus"}}
			lists := map[string][]any{strings.ToLower(root + "/resourcegroups"): {details[strings.ToLower(group)]}, strings.ToLower(root + "/providers/Microsoft.Authorization/locks"): {}}
			for _, parent := range []struct{ kind, name string }{
				{privateDNSZoneType, "private.example"}, {"Microsoft.Network/privateEndpoints", "endpoint"},
				{"Microsoft.Compute/capacityReservationGroups", "cg"}, {"Microsoft.Compute/hostGroups", "hg"},
				{"Microsoft.Network/expressRouteCircuits", "circuit"}, {"Microsoft.Network/networkWatchers", "watcher"},
				{"Microsoft.Network/virtualHubs", "hub"}, {"Microsoft.Network/vpnGateways", "vpn"}, {vnetType, "vnet"}, {storageType, "storage"},
			} {
				location := "eastus"
				if parent.kind == privateDNSZoneType {
					location = "global"
				}
				value := nativeResource(parent.kind, parent.name, location, map[string]any{})
				details[strings.ToLower(text(value["id"]))] = value
				lists[strings.ToLower(root+"/providers/"+parent.kind)] = []any{value}
			}
			for _, collection := range []string{"flowLogs", "connectionMonitors", "packetCaptures"} {
				lists[strings.ToLower(group+"/providers/Microsoft.Network/networkWatchers/watcher/"+collection)] = []any{}
			}
			for _, collection := range []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT", "virtualNetworkLinks", "privateDnsZoneGroups"} {
				lists[strings.ToLower(id+"/"+collection)] = []any{}
			}
			details[strings.ToLower(id)] = raw
			lists[strings.ToLower(root+tc.list)] = []any{raw}
			deleted, listed := false, false
			deletes := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				if strings.EqualFold(path, id) {
					if req.URL.Query().Get("api-version") != tc.version {
						t.Fatalf("wrong native resource version %s", req.URL)
					}
					if req.Method == "DELETE" {
						if req.Header.Get("x-ms-client-request-id") != azureRequestID("native-delete") {
							t.Fatal("missing client request ID")
						}
						if strings.HasSuffix(tc.kind, "/fileServices/shares") && req.URL.Query().Get("$include") != "none" {
							t.Fatal("file share deletion would silently delete snapshots")
						}
						deleted = true
						deletes++
						return jsonResponse(204, nil, http.Header{"X-Ms-Request-Id": {"native-delete-request"}}), nil
					}
					if deleted {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
				}
				if req.Method != "GET" {
					t.Fatalf("unexpected native write %s %s", req.Method, req.URL)
				}
				if values, ok := lists[path]; ok {
					if strings.EqualFold(path, root+tc.list) {
						listed = true
						if req.URL.Query().Get("api-version") != tc.version {
							t.Fatalf("wrong native list version %s", req.URL)
						}
					}
					return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"native-list-request"}}), nil
				}
				if detail, ok := details[path]; ok {
					return jsonResponse(200, detail, nil), nil
				}
				t.Fatalf("unexpected native API %s", req.URL)
				return nil, nil
			})
			batch, err := r.List(context.Background(), productRequest(r, tc.kind))
			if err != nil || !batch.Complete || !listed || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(id) || batch.Items[0].NativeType != tc.kind || batch.Items[0].Location != tc.location || batch.RequestID != "native-list-request" {
				t.Fatalf("native inventory=%+v error=%v", batch, err)
			}
			value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeType: tc.kind, NativeID: id}, Location: tc.location, Normalized: batch.Items[0].Normalized}
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-delete"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || deletes != 1 || result.ProviderRequestID != "native-delete-request" {
				t.Fatalf("native deletion=%+v count=%d error=%v", result, deletes, err)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatalf("native deletion readback=%+v error=%v", wait, err)
			}
		})
	}
}

func TestNativeNetworkReferencesAndSecretRedaction(t *testing.T) {
	id := resourceID("Microsoft.Network/connections", "vpn")
	properties := map[string]any{
		"virtualNetworkGateway1":   map[string]any{"id": resourceID("Microsoft.Network/virtualNetworkGateways", "gateway")},
		"localNetworkGateway2":     map[string]any{"id": resourceID("Microsoft.Network/localNetworkGateways", "local")},
		"remoteVirtualNetwork":     map[string]any{"id": resourceID(vnetType, "remote")},
		"virtualHub":               map[string]any{"id": resourceID("Microsoft.Network/virtualHubs", "hub")},
		"virtualWan":               map[string]any{"id": resourceID("Microsoft.Network/virtualWans", "wan")},
		"firewallPolicy":           map[string]any{"id": resourceID("Microsoft.Network/firewallPolicies", "policy")},
		"ddosProtectionPlan":       map[string]any{"id": resourceID("Microsoft.Network/ddosProtectionPlans", "plan")},
		"host":                     map[string]any{"id": resourceID("Microsoft.Compute/hostGroups", "hg") + "/hosts/host"},
		"hostGroup":                map[string]any{"id": resourceID("Microsoft.Compute/hostGroups", "hg")},
		"capacityReservationGroup": map[string]any{"id": resourceID("Microsoft.Compute/capacityReservationGroups", "cg")},
		"targetResourceId":         resourceID(nicType, "nic"), "storageId": resourceID(storageType, "storage"),
		"routingConfiguration":                 map[string]any{"associatedRouteTable": map[string]any{"id": resourceID("Microsoft.Network/virtualHubs", "hub") + "/hubRouteTables/defaultRouteTable"}, "propagatedRouteTables": map[string]any{"ids": []any{resourceID("Microsoft.Network/virtualHubs", "hub") + "/hubRouteTables/custom"}}},
		"loadBalancerFrontendIpConfigurations": []any{map[string]any{"id": resourceID("Microsoft.Network/loadBalancers", "lb") + "/frontendIPConfigurations/front"}},
		"sharedKey":                            "vpn-key-sensitive", "authorizationKey": "er-key-sensitive", "serviceKey": "service-key-sensitive", "radiusServerSecret": "radius-sensitive",
	}
	refs := references("Microsoft.Network/connections", strings.ToLower(id), map[string]any{"properties": properties})
	for _, kind := range []string{"Microsoft.Network/virtualNetworkGateways", "Microsoft.Network/localNetworkGateways", vnetType, "Microsoft.Network/virtualHubs", "Microsoft.Network/virtualWans", "Microsoft.Network/firewallPolicies", "Microsoft.Network/ddosProtectionPlans", "Microsoft.Compute/hostGroups/hosts", "Microsoft.Compute/hostGroups", "Microsoft.Compute/capacityReservationGroups", nicType, storageType, "Microsoft.Network/loadBalancers"} {
		if len(refs[kind]) != 1 {
			t.Fatalf("missing native %s reference: %v", kind, refs)
		}
	}
	if len(refs["Microsoft.Network/virtualHubs/hubRouteTables"]) != 2 || !slices.Contains(refs["Microsoft.Network/loadBalancers"], strings.ToLower(resourceID("Microsoft.Network/loadBalancers", "lb"))) {
		t.Fatalf("nested references=%v", refs)
	}
	payload, _ := json.Marshal(safePayload(map[string]any{"properties": properties}))
	if strings.Contains(string(payload), "sensitive") {
		t.Fatalf("native network secret leaked: %s", payload)
	}
	if properties["sharedKey"] != "vpn-key-sensitive" {
		t.Fatal("redaction changed live provider data")
	}
}

func TestNativePreflightRejectsPartialOrForeignResourceIdentity(t *testing.T) {
	for _, mode := range []string{"resource-id", "resource-type", "resource-partial", "group-id", "group-type", "group-partial"} {
		t.Run(mode, func(t *testing.T) {
			value := actionAsset("Microsoft.Network/azureFirewalls", "firewall")
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatalf("invalid native identity reached a write: %s", req.URL)
				}
				status := 200
				data := map[string]any{"id": req.URL.Path}
				if strings.EqualFold(req.URL.Path, value.Identity.NativeID) {
					data = nativeResource(value.Identity.NativeType, "firewall", "eastus", nil)
					switch mode {
					case "resource-id":
						data["id"] = resourceID(value.Identity.NativeType, "other")
					case "resource-type":
						data["type"] = vmType
					case "resource-partial":
						status = 206
					}
				} else {
					switch mode {
					case "group-id":
						data["id"] = "/subscriptions/" + testTenant + "/resourceGroups/test"
					case "group-type":
						data["type"] = vmType
					case "group-partial":
						status = 206
					}
				}
				return jsonResponse(status, data, nil), nil
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if err == nil || check.Allowed || check.Absent {
				t.Fatalf("accepted %s: %+v %v", mode, check, err)
			}
		})
	}
}
