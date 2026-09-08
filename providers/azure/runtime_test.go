package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type credentialFunc func(context.Context, asset.ConnectionID) (contracts.Credential, error)

func (f credentialFunc) Resolve(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
	return f(ctx, id)
}
func protocolRuntime(t *testing.T, product roundTripFunc) *Runtime {
	t.Helper()
	r, err := NewRuntime(credentialFunc(func(_ context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		if id != "connection" {
			t.Fatalf("unexpected connection %q", id)
		}
		return testCredential(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "login.microsoftonline.com" {
			return jsonResponse(200, map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 3600}, nil), nil
		}
		if req.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing token")
		}
		if req.URL.Path == "/subscriptions/"+testSubscription {
			return jsonResponse(200, map[string]any{"subscriptionId": testSubscription, "tenantId": testTenant, "state": "Enabled", "displayName": "Fixture subscription"}, nil), nil
		}
		return product(req)
	})
	return r
}
func resourceID(kind, name string) string {
	return "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/" + kind + "/" + name
}
func nativeResource(kind, name, location string, properties map[string]any) map[string]any {
	return map[string]any{"id": resourceID(kind, name), "type": kind, "name": name, "location": location, "properties": properties}
}

func TestInvokeUsesNativeOperationsAndRejectsForeignParameters(t *testing.T) {
	var calls int
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || !strings.EqualFold(req.URL.Path, resourceID(vmType, "vm")) || req.URL.Query().Get("api-version") != "2024-07-01" {
			t.Fatalf("wrong request %s %s", req.Method, req.URL)
		}
		return jsonResponse(200, nativeResource(vmType, "vm", "eastus", map[string]any{"customData": "hidden"}), http.Header{"X-Ms-Request-Id": {"invoke-id"}}), nil
	})
	invocation := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Compute.VirtualMachines_Get", Parameters: map[string]any{"resourceGroupName": "test", "vmName": "vm"}}
	result, err := r.Invoke(context.Background(), invocation)
	if err != nil || result.RequestID != "invoke-id" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	payload, _ := json.Marshal(result)
	if strings.Contains(string(payload), "hidden") {
		t.Fatal("Invoke returned custom data")
	}
	invocation.Parameters["subscriptionId"] = testTenant
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Error("foreign subscription accepted")
	}
	delete(invocation.Parameters, "subscriptionId")
	invocation.Parameters["api-version"] = "1900-01-01"
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Error("foreign API version accepted")
	}
	invocation.Operation = "azure.resources.get"
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Error("synthetic operation accepted")
	}
	if calls != 1 {
		t.Errorf("invalid input reached product API %d times", calls)
	}
}

func TestLegacySubscriptionInventoryPagingChildrenAndVMNetworking(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	subnet := resourceID(vnetType, "vnet") + "/subnets/subnet"
	vm := nativeResource(vmType, "vm", "eastus", map[string]any{"provisioningState": "Succeeded", "networkProfile": map[string]any{"networkInterfaces": []any{map[string]any{"id": resourceID(nicType, "nic")}}}, "customData": "private-startup"})
	nic := nativeResource(nicType, "nic", "eastus", map[string]any{"ipConfigurations": []any{map[string]any{"properties": map[string]any{"subnet": map[string]any{"id": subnet}}}}})
	vnet := nativeResource(vnetType, "vnet", "eastus", map[string]any{"subnets": []any{map[string]any{"id": subnet}}})
	disk := nativeResource(diskType, "disk", "westus", map[string]any{"diskSizeGB": 32})
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		var data any
		switch strings.ToLower(req.URL.Path) {
		case root + "/resources":
			if req.URL.Query().Get("$skiptoken") == "two" {
				data = map[string]any{"value": []any{disk}}
			} else {
				data = map[string]any{"value": []any{vm, nic, vnet}, "nextLink": apiURL(root+"/resources", resourcesVersion) + "&%24skiptoken=two"}
			}
		case root + "/resourcegroups":
			data = map[string]any{"value": []any{map[string]any{"id": root + "/resourceGroups/test", "name": "test", "location": "eastus", "properties": map[string]any{}}}}
		case root + "/providers/microsoft.authorization/locks":
			data = map[string]any{"value": []any{}}
		case strings.ToLower(resourceID(vmType, "vm")):
			data = vm
		case strings.ToLower(resourceID(nicType, "nic")):
			data = nic
		case strings.ToLower(resourceID(vnetType, "vnet")):
			data = vnet
		case strings.ToLower(resourceID(vnetType, "vnet") + "/subnets"):
			data = map[string]any{"value": []any{map[string]any{"id": subnet, "name": "subnet", "properties": map[string]any{"addressPrefix": "10.0.0.0/24"}}}}
		case strings.ToLower(resourceID(diskType, "disk")):
			data = disk
		default:
			return nil, fmt.Errorf("unexpected API %s", req.URL)
		}
		return jsonResponse(200, data, http.Header{"X-Ms-Request-Id": {"inventory-page"}}), nil
	})
	request := contracts.InventoryRequest{ConnectionID: "connection", Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
	first, err := r.List(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.NextCursor == "" || first.RequestID != "inventory-page" || len(first.Items) != 5 {
		t.Fatalf("first=%+v", first)
	}
	var foundVM, foundSubnet bool
	for _, item := range first.Items {
		if item.NativeType == vmType {
			foundVM = true
			if text(item.Normalized["vpc_id"]) != strings.ToLower(resourceID(vnetType, "vnet")) || text(item.Normalized["vswitch_id"]) != strings.ToLower(subnet) {
				t.Fatalf("missing VM NIC network closure: %+v", item.Normalized)
			}
			raw, _ := json.Marshal(item)
			if strings.Contains(string(raw), "private-startup") {
				t.Fatal("inventory leaked startup data")
			}
		}
		if item.NativeType == subnetType {
			foundSubnet = true
			if item.Location != "eastus" {
				t.Error("child location not inherited")
			}
		}
	}
	if !foundVM || !foundSubnet {
		t.Fatal("missing product detail or child")
	}
	request.Cursor = first.NextCursor
	second, err := r.List(context.Background(), request)
	if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].Location != "westus" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	request.Scope.Kind = asset.ScopeRegion
	request.Scope.NativeID = "eastus"
	second, err = r.List(context.Background(), request)
	if err != nil || len(second.Items) != 0 {
		t.Fatalf("region filtering=%+v %v", second, err)
	}
}

func TestPaginationRejectsForeignCollectionVersionAndRepeatedPages(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	endpoint := apiURL(root+"/resources", resourcesVersion)
	for _, next := range []string{endpoint, apiURL(root+"/locations", resourcesVersion), apiURL(root+"/resources", "other"), strings.Replace(endpoint, testSubscription, testTenant, 1), "https://evil.invalid" + root + "/resources"} {
		c := directClient(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), nil
		})
		if _, _, err := c.listPage(context.Background(), endpoint, root+"/resources"); err == nil {
			t.Errorf("accepted nextLink %s", next)
		}
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Error("invalid cursor reached product API")
		return nil, fmt.Errorf("unexpected")
	})
	for _, cursor := range []string{"not+base64", base64.RawURLEncoding.EncodeToString([]byte(apiURL(root+"/resources", "foreign")))} {
		if _, err := r.List(context.Background(), contracts.InventoryRequest{ConnectionID: "connection", Cursor: cursor}); err == nil {
			t.Error("invalid inventory cursor accepted")
		}
	}
}

func TestInventoryManagedGroupAndInheritedLocks(t *testing.T) {
	c := directClient(nil)
	r, err := NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil }))
	if err != nil {
		t.Fatal(err)
	}
	raw := nativeResource(diskType, "disk", "eastus", map[string]any{})
	id := strings.ToLower(resourceID(diskType, "disk"))
	group := c.root() + "/resourcegroups/test"
	item, err := r.inventoryItem(context.Background(), c, raw, map[string]string{group: "controller"}, nil)
	if err != nil || item.Normalized["cleanup_protection_reason"] != "azure_managed_resource_group" || item.Normalized["cleanup_controller_only"] != true || item.Normalized["cleanup_protected"] == true {
		t.Fatalf("managed group=%+v %v", item, err)
	}
	locks := []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/retain", "properties": map[string]any{"level": "CanNotDelete"}}}
	item, err = r.inventoryItem(context.Background(), c, raw, nil, locks)
	if err != nil || item.Normalized["cleanup_protection_reason"] != "azure_management_lock" || item.Normalized["cleanup_protected"] != true || !locked(id, locks) {
		t.Fatalf("inherited lock=%+v %v", item, err)
	}
	container := map[string]any{"id": resourceID(storageType, "storage") + "/blobServices/default/containers/held", "type": containerType, "properties": map[string]any{"hasLegalHold": true}}
	item, err = r.inventoryItem(context.Background(), c, container, map[string]string{group: "controller"}, nil)
	if err != nil || item.Normalized["cleanup_protection_reason"] != "blob_container_retention_policy" || item.Normalized["cleanup_protected"] != true {
		t.Fatalf("managed group hides retention protection: %+v %v", item, err)
	}
}

func TestOfficialResponseFixturesMapResourceProperties(t *testing.T) {
	for _, file := range []string{"VirtualMachine_Get.json", "PublicIpAddressGet.json"} {
		t.Run(file, func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/" + file)
			if err != nil {
				t.Fatal(err)
			}
			payload = []byte(strings.NewReplacer("{subscription-id}", testSubscription, "subid", testSubscription, "{myNIC}", "testnic", "{MyVmss}", "testvmss").Replace(string(payload)))
			var example map[string]any
			if err := json.Unmarshal(payload, &example); err != nil {
				t.Fatal(err)
			}
			raw := object(object(object(example["responses"])["200"])["body"])
			c := directClient(func(req *http.Request) (*http.Response, error) {
				return jsonResponse(200, map[string]any{"id": req.URL.Path, "type": nicType, "properties": map[string]any{}}, nil), nil
			})
			r, err := NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil }))
			if err != nil {
				t.Fatal(err)
			}
			item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if item.Location != "westus" || item.State != "Succeeded" {
				t.Fatalf("official location/state not normalized: %+v", item)
			}
			if file == "VirtualMachine_Get.json" {
				if len(item.Normalized[referenceKey(diskType)].([]string)) != 3 {
					t.Fatalf("official VM disk references missing: %+v", item.Normalized)
				}
				if object(item.Normalized["hardwareProfile"])["vmSize"] != "Standard_DS3_v2" {
					t.Error("official VM size missing")
				}
				safe, _ := json.Marshal(item)
				if strings.Contains(string(safe), "RXhhbXBsZSBVc2VyRGF0YQ==") {
					t.Error("official userData example leaked")
				}
			}
		})
	}
}
