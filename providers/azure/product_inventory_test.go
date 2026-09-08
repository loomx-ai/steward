package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func productRequest(r *Runtime, nativeType string) contracts.InventoryRequest {
	kind := r.resourceKind(nativeType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

// These paths are native ARM collection contracts. They deliberately do not
// derive request URLs from the implementation's catalog binding.
func TestNativeProductSubscriptionLists(t *testing.T) {
	for _, nativeType := range []string{
		"Microsoft.Compute/virtualMachines", "Microsoft.Compute/disks", "Microsoft.Compute/snapshots", "Microsoft.Compute/images", "Microsoft.Compute/availabilitySets", "Microsoft.Compute/virtualMachineScaleSets",
		"Microsoft.Network/virtualNetworks", "Microsoft.Network/networkInterfaces", "Microsoft.Network/networkSecurityGroups", "Microsoft.Network/routeTables", "Microsoft.Network/publicIPAddresses", "Microsoft.Network/publicIPPrefixes", "Microsoft.Network/natGateways", "Microsoft.Network/loadBalancers", "Microsoft.Network/applicationGateways", "Microsoft.Network/privateEndpoints",
		"Microsoft.Storage/storageAccounts", "Microsoft.Sql/servers", "Microsoft.DBforPostgreSQL/flexibleServers", "Microsoft.DBforMySQL/flexibleServers", "Microsoft.Web/sites", "Microsoft.Web/serverfarms", "Microsoft.ContainerRegistry/registries", "Microsoft.ContainerService/managedClusters", "Microsoft.KeyVault/vaults", "Microsoft.OperationalInsights/workspaces", "Microsoft.ManagedIdentity/userAssignedIdentities", "Microsoft.App/containerApps", "Microsoft.App/managedEnvironments", "Microsoft.Resources/resourceGroups",
	} {
		t.Run(nativeType, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			collection := root + "/providers/" + strings.ToLower(nativeType)
			if nativeType == groupType {
				collection = root + "/resourcegroups"
			}
			listed := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				if req.Method != "GET" || req.URL.Host != "management.azure.com" || req.URL.Query().Get("api-version") == "" {
					t.Fatalf("invalid product call %s %s", req.Method, req.URL)
				}
				switch path {
				case collection:
					listed = true
				default:
					if path != root+"/resourcegroups" && path != root+"/providers/microsoft.authorization/locks" {
						t.Fatalf("used index or incorrect product collection: %s", req.URL)
					}
				}
				return jsonResponse(200, map[string]any{"value": []any{}}, http.Header{"X-Ms-Request-Id": {"native-list"}}), nil
			})
			batch, err := r.List(context.Background(), productRequest(r, nativeType))
			if err != nil || !listed || !batch.Complete || len(batch.Items) != 0 || batch.RequestID != "native-list" {
				t.Fatalf("batch=%+v listed=%v error=%v", batch, listed, err)
			}
		})
	}
}

func TestProductVMDetailPagingNetworkAndScope(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	collection := root + "/providers/Microsoft.Compute/virtualMachines"
	nic := nativeResource(nicType, "nic", "eastus", map[string]any{"ipConfigurations": []any{map[string]any{"properties": map[string]any{"subnet": map[string]any{"id": resourceID(vnetType, "vnet") + "/subnets/subnet"}}}}})
	vm := nativeResource(vmType, "vm", "eastus", map[string]any{"vmId": "vm-incarnation", "customData": "never-in-inventory", "networkProfile": map[string]any{"networkInterfaces": []any{map[string]any{"id": resourceID(nicType, "nic")}}}})
	west := nativeResource(vmType, "west", "westus", map[string]any{})
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		var data any
		switch strings.ToLower(req.URL.Path) {
		case strings.ToLower(collection):
			if req.URL.Query().Get("api-version") != "2024-07-01" {
				t.Fatalf("wrong VM API version %s", req.URL)
			}
			if req.URL.Query().Get("$skiptoken") == "two" {
				data = map[string]any{"value": []any{west}}
			} else {
				data = map[string]any{"value": []any{map[string]any{"id": vm["id"]}}, "nextLink": apiURL(collection, "2024-07-01") + "&%24skiptoken=two"}
			}
		case strings.ToLower(text(vm["id"])):
			data = vm
		case strings.ToLower(text(west["id"])):
			data = west
		case strings.ToLower(text(nic["id"])):
			data = nic
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
			data = map[string]any{"value": []any{}}
		default:
			t.Fatalf("unexpected product request %s", req.URL)
		}
		return jsonResponse(200, data, http.Header{"X-Ms-Request-Id": {"vm-page"}}), nil
	})
	request := productRequest(r, vmType)
	page, err := r.List(context.Background(), request)
	if err != nil || page.Complete || len(page.Items) != 1 || page.RequestID != "vm-page" {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	item := page.Items[0]
	if item.Normalized["_inventory_source"] != productInventorySource || item.Normalized["vmId"] != "vm-incarnation" || item.Normalized["vpc_id"] != strings.ToLower(resourceID(vnetType, "vnet")) {
		t.Fatalf("missing product detail/source/network: %+v", item)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "never-in-inventory") {
		t.Fatal("product scan leaked a secret")
	}
	request.Cursor = page.NextCursor
	page, err = r.List(context.Background(), request)
	if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].Location != "westus" {
		t.Fatalf("second page=%+v error=%v", page, err)
	}
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	if _, err := r.List(context.Background(), request); err == nil {
		t.Fatal("cursor accepted another region")
	}
	request.Cursor = ""
	page, err = r.List(context.Background(), request)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("regional page=%+v error=%v", page, err)
	}
	request.Cursor = page.NextCursor
	page, err = r.List(context.Background(), request)
	if err != nil || !page.Complete || len(page.Items) != 0 {
		t.Fatalf("regional last page=%+v error=%v", page, err)
	}
}

func TestNativeChildrenRequireProductParentsAndReadback(t *testing.T) {
	for _, tc := range []struct{ parent, child, suffix, version string }{
		{vnetType, subnetType, "/subnets", "2024-05-01"},
		{storageType, containerType, "/blobServices/default/containers", "2023-05-01"},
		{"Microsoft.Sql/servers", "Microsoft.Sql/servers/databases", "/databases", "2023-08-01"},
		{"Microsoft.Sql/servers", "Microsoft.Sql/servers/elasticPools", "/elasticPools", "2023-08-01"},
	} {
		t.Run(tc.child, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			parent := nativeResource(tc.parent, "parent", "East US", map[string]any{})
			parent["etag"] = "original"
			childID := text(parent["id"]) + tc.suffix + "/child"
			child := map[string]any{"id": childID, "name": "child", "properties": map[string]any{"provisioningState": "Succeeded"}}
			reads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				var data any
				switch strings.ToLower(req.URL.Path) {
				case root + "/providers/" + strings.ToLower(tc.parent):
					data = map[string]any{"value": []any{parent}}
				case strings.ToLower(text(parent["id"])):
					reads++
					data = parent
				case strings.ToLower(text(parent["id"]) + tc.suffix):
					if req.URL.Query().Get("api-version") != tc.version {
						t.Fatalf("wrong child version %s", req.URL)
					}
					data = map[string]any{"value": []any{child}}
				case strings.ToLower(childID):
					data = child
				case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
					data = map[string]any{"value": []any{}}
				default:
					t.Fatalf("unexpected child API %s", req.URL)
				}
				return jsonResponse(200, data, nil), nil
			})
			request := productRequest(r, tc.child)
			request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			batch, err := r.List(context.Background(), request)
			if err != nil || !batch.Complete || len(batch.Items) != 1 || reads != 2 {
				t.Fatalf("batch=%+v parent reads=%d error=%v", batch, reads, err)
			}
			item := batch.Items[0]
			if item.NativeID != strings.ToLower(childID) || item.NativeType != tc.child || item.Location != "eastus" || !slices.Contains(item.NetworkReferences, strings.ToLower(text(parent["id"]))) {
				t.Fatalf("incorrect native child: %+v", item)
			}
		})
	}
}

func TestBroadARMIndexCannotOverwriteProductInventory(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	unknown := nativeResource("Microsoft.Example/things", "unknown", "eastus", map[string]any{})
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		values := []any{}
		switch strings.ToLower(req.URL.Path) {
		case root + "/resources":
			values = []any{nativeResource(vmType, "stale-vm", "eastus", map[string]any{}), unknown}
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
		default:
			t.Fatalf("broad index enriched stale product record: %s", req.URL)
		}
		return jsonResponse(200, map[string]any{"value": values}, nil), nil
	})
	batch, err := r.List(context.Background(), contracts.InventoryRequest{ConnectionID: "connection", Source: inventorySource, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}})
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeType != "microsoft.example/things" {
		t.Fatalf("batch=%+v error=%v", batch, err)
	}
	for _, source := range r.InventorySources() {
		if source.Name == inventorySource && source.AuthoritativeDefault || source.Name == productInventorySource && (!source.AuthoritativeDefault || !source.KindSpecific || !source.NetworkClosure) {
			t.Fatalf("incorrect authority: %+v", source)
		}
	}
}

func TestProductPaginationRejectsCyclesAndPartialOrForeignResponses(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	collection := root + "/providers/Microsoft.Compute/disks"
	for _, failure := range []string{"cycle", "next-type", "partial", "foreign-id", "duplicate", "foreign-version", "foreign-collection", "permission"} {
		t.Run(failure, func(t *testing.T) {
			count := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				count++
				status := 200
				body := map[string]any{"value": []any{}}
				switch failure {
				case "cycle":
					body["nextLink"] = apiURL(collection, "2024-03-02") + fmt.Sprintf("&skiptoken=%d", count%2)
				case "next-type":
					body["nextLink"] = 123
				case "partial":
					status = 206
				case "permission":
					status = 403
				case "foreign-id":
					body["value"] = []any{map[string]any{"id": strings.Replace(resourceID(diskType, "disk"), testSubscription, testTenant, 1)}}
				case "duplicate":
					body["value"] = []any{nativeResource(diskType, "disk", "eastus", nil), nativeResource(diskType, "disk", "eastus", nil)}
				case "foreign-version":
					body["nextLink"] = apiURL(collection, "other")
				case "foreign-collection":
					body["nextLink"] = apiURL(root+"/resources", "2024-03-02")
				}
				return jsonResponse(status, body, nil), nil
			})
			request := productRequest(r, diskType)
			var err error
			for i := 0; i < 4; i++ {
				var batch contracts.InventoryBatch
				batch, err = r.List(context.Background(), request)
				if err != nil {
					break
				}
				if batch.Complete {
					t.Fatalf("%s was an authoritative empty result", failure)
				}
				request.Cursor = batch.NextCursor
			}
			if err == nil {
				t.Fatalf("accepted %s", failure)
			}
		})
	}
}

func TestProductCursorRejectsChangedSubscriptionKindAndVersion(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	collection := root + "/providers/Microsoft.Compute/disks"
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		body := map[string]any{"value": []any{}}
		if strings.EqualFold(req.URL.Path, collection) {
			body["nextLink"] = apiURL(collection, "2024-03-02") + "&skiptoken=two"
		}
		return jsonResponse(200, body, nil), nil
	})
	request := productRequest(r, diskType)
	batch, err := r.List(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor = batch.NextCursor
	for _, mutation := range []string{"subscription", "kind", "version"} {
		changed := request
		switch mutation {
		case "subscription":
			changed.Scope.NativeID = testTenant
		case "kind":
			kind := r.resourceKind(vmType)
			changed.ResourceKind = &kind
		case "version":
			data, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
			var cursor productCursor
			_ = json.Unmarshal(data, &cursor)
			cursor.Next = apiURL(collection, "other") + "&skiptoken=two"
			data, _ = json.Marshal(cursor)
			changed.Cursor = base64.RawURLEncoding.EncodeToString(data)
		}
		if _, err := r.List(context.Background(), changed); err == nil {
			t.Fatalf("accepted cursor with changed %s", mutation)
		}
	}
}

func TestChildFanoutRejectsChangedParentsAndIncompleteDiscovery(t *testing.T) {
	for _, failure := range []string{"", "parent-set", "parent-generation", "live-generation", "parent-permission", "child-permission", "child-missing", "foreign-child", "duplicate-parents", "lock-permission"} {
		t.Run(failure, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			parents := []map[string]any{nativeResource(vnetType, "alpha", "eastus", nil), nativeResource(vnetType, "beta", "eastus", nil)}
			parents[0]["etag"], parents[1]["etag"] = "alpha-original", "beta-original"
			childListCalled := false
			requests := []string{}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				requests = append(requests, path)
				if path == root+"/providers/microsoft.network/virtualnetworks" {
					values := []any{}
					for _, parent := range parents {
						values = append(values, parent)
					}
					if failure == "duplicate-parents" {
						values = append(values, parents[0])
					}
					return jsonResponse(200, map[string]any{"value": values}, nil), nil
				}
				if path == root+"/providers/microsoft.authorization/locks" && failure == "lock-permission" {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), nil
				}
				if path == root+"/resourcegroups" || path == root+"/providers/microsoft.authorization/locks" {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				for _, parent := range parents {
					id := strings.ToLower(text(parent["id"]))
					if path == id {
						if childListCalled && failure == "parent-permission" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						return jsonResponse(200, parent, nil), nil
					}
					childID := id + "/subnets/child"
					child := map[string]any{"id": childID, "name": "child", "properties": map[string]any{}}
					if path == id+"/subnets" {
						childListCalled = true
						if failure == "live-generation" {
							parent["etag"] = "changed-after-list"
						}
						if failure == "child-permission" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						if failure == "child-missing" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						if failure == "foreign-child" {
							child["id"] = resourceID(vnetType, "foreign") + "/subnets/child"
						}
						return jsonResponse(200, map[string]any{"value": []any{child}}, nil), nil
					}
					if path == childID {
						return jsonResponse(200, child, nil), nil
					}
				}
				return nil, fmt.Errorf("unexpected child API %s", req.URL)
			})
			request := productRequest(r, subnetType)
			first, err := r.List(context.Background(), request)
			if failure != "" && failure != "parent-set" && failure != "parent-generation" {
				if err == nil {
					t.Fatalf("%s accepted: %+v", failure, first)
				}
				return
			}
			if err != nil || first.Complete || len(first.Items) != 1 || !strings.Contains(first.Items[0].NativeID, "/alpha/") {
				t.Fatalf("first=%+v error=%v", first, err)
			}
			if failure == "parent-set" {
				parents = parents[:1]
			}
			if failure == "parent-generation" {
				parents[1]["etag"] = "new-beta"
			}
			request.Cursor = first.NextCursor
			second, err := r.List(context.Background(), request)
			if failure != "" {
				if err == nil {
					t.Fatalf("accepted changed parents: %+v", second)
				}
				return
			}
			if err != nil || !second.Complete || len(second.Items) != 1 || !strings.Contains(second.Items[0].NativeID, "/beta/") {
				t.Fatalf("second=%+v error=%v calls=%v", second, err, requests)
			}
		})
	}
}

func TestLiveNetworkSelectionUsesProductCollectionsAndRetainsLocks(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	vnet := nativeResource(vnetType, "vnet", "eastus", nil)
	id := strings.ToLower(text(vnet["id"]))
	subnetID := id + "/subnets/subnet"
	subnet := map[string]any{"id": subnetID, "name": "subnet", "properties": map[string]any{}}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		var data any
		switch strings.ToLower(req.URL.Path) {
		case root + "/providers/microsoft.network/virtualnetworks":
			data = map[string]any{"value": []any{vnet}}
		case id:
			data = vnet
		case id + "/subnets":
			data = map[string]any{"value": []any{subnet}}
		case subnetID:
			data = subnet
		case root + "/resourcegroups":
			data = map[string]any{"value": []any{}}
		case root + "/providers/microsoft.authorization/locks":
			data = map[string]any{"value": []any{map[string]any{"id": root + "/resourcegroups/test/providers/Microsoft.Authorization/locks/retained", "properties": map[string]any{"level": "CanNotDelete"}}}}
		default:
			t.Fatalf("network picker used non-product API %s", req.URL)
		}
		return jsonResponse(200, data, nil), nil
	})
	for _, kind := range []asset.ScanTargetKind{asset.ScanTargetVPC, asset.ScanTargetVSwitch} {
		query := contracts.NetworkTargetQuery{ConnectionID: "connection", Kind: kind, RegionID: "eastus"}
		if kind == asset.ScanTargetVSwitch {
			query.ParentNativeID = id
			query.Query = "subnet"
		}
		page, err := r.SearchNetworkTargets(context.Background(), query)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("network page=%+v error=%v", page, err)
		}
		if kind == asset.ScanTargetVSwitch && page.Items[0].ParentNativeID != id {
			t.Fatalf("wrong parent: %+v", page.Items[0])
		}
	}
	batch, err := r.List(context.Background(), productRequest(r, subnetType))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Normalized["cleanup_protected"] != true || batch.Items[0].Normalized["cleanup_protection_reason"] != "azure_management_lock" {
		t.Fatalf("native discovery lost inherited lock: %+v %v", batch, err)
	}
}

func TestRegionalProductNetworkScanIncludesGlobalBindings(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	nativeType := "Microsoft.Web/sites"
	global := nativeResource(nativeType, "global", "global", nil)
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		data := map[string]any{"value": []any{}}
		if strings.EqualFold(req.URL.Path, root+"/providers/Microsoft.Web/sites") {
			data["value"] = []any{global}
		}
		if strings.EqualFold(req.URL.Path, text(global["id"])) {
			data = global
		}
		return jsonResponse(200, data, nil), nil
	})
	request := productRequest(r, nativeType)
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	batch, err := r.List(context.Background(), request)
	if err != nil || len(batch.Items) != 0 {
		t.Fatalf("regional result=%+v error=%v", batch, err)
	}
	request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: resourceID(vnetType, "vnet")}
	batch, err = r.List(context.Background(), request)
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("network scan lost global resource=%+v error=%v", batch, err)
	}
}
