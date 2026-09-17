package azure

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Native ARM collection and deletion contracts for kinds added for Alibaba
// Cloud parity: Purview, Service Fabric, managed applications, Storage Sync and
// Machine Learning. Paths come from the pinned official Swagger documents.
func TestParityKindsListNativeSubscriptionCollections(t *testing.T) {
	for nativeType, version := range map[string]string{
		"Microsoft.Purview/accounts":                   "2021-12-01",
		"Microsoft.ServiceFabric/clusters":             "2021-06-01",
		"Microsoft.Solutions/applications":             "2021-07-01",
		"Microsoft.StorageSync/storageSyncServices":    "2025-12-01",
		"Microsoft.MachineLearningServices/workspaces": "2026-07-01",
	} {
		t.Run(nativeType, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			collection := root + "/providers/" + strings.ToLower(nativeType)
			item := nativeResource(nativeType, "sample", "eastus", map[string]any{"provisioningState": "Succeeded"})
			listed := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				switch {
				case req.Method != "GET":
					t.Fatalf("mutation during inventory %s %s", req.Method, req.URL)
				case path == collection:
					if req.URL.Query().Get("api-version") != version {
						t.Fatalf("wrong API version %s", req.URL)
					}
					listed = true
					return jsonResponse(200, map[string]any{"value": []any{item}}, http.Header{"X-Ms-Request-Id": {"parity-list"}}), nil
				case path == strings.ToLower(text(item["id"])):
					return jsonResponse(200, item, nil), nil
				case path == root+"/resourcegroups" || path == root+"/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			batch, err := r.List(context.Background(), productRequest(r, nativeType))
			if err != nil || !listed || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(text(item["id"])) || batch.Items[0].State != "Succeeded" {
				t.Fatalf("batch=%+v listed=%v err=%v", batch, listed, err)
			}
		})
	}
}

func TestParityOnlineEndpointsListThroughWorkspaces(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	workspace := nativeResource("Microsoft.MachineLearningServices/workspaces", "mlworkspace", "eastus", map[string]any{})
	endpointID := text(workspace["id"]) + "/onlineEndpoints/scoring"
	endpoint := map[string]any{"id": endpointID, "name": "scoring", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "authMode": "Key"}}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		switch strings.ToLower(req.URL.Path) {
		case root + "/providers/microsoft.machinelearningservices/workspaces":
			return jsonResponse(200, map[string]any{"value": []any{workspace}}, nil), nil
		case strings.ToLower(text(workspace["id"])):
			return jsonResponse(200, workspace, nil), nil
		case strings.ToLower(text(workspace["id"]) + "/onlineEndpoints"):
			if req.URL.Query().Get("api-version") != "2026-07-01" {
				t.Fatalf("wrong endpoint version %s", req.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{endpoint}}, nil), nil
		case strings.ToLower(endpointID):
			return jsonResponse(200, endpoint, nil), nil
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	batch, err := r.List(context.Background(), productRequest(r, "Microsoft.MachineLearningServices/workspaces/onlineEndpoints"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(endpointID) {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
}

func TestParityKindsDeleteWithOperationAndFinalAbsence(t *testing.T) {
	for _, tc := range []struct {
		kind, name, protocol string
	}{
		{"Microsoft.StorageSync/storageSyncServices", "sync", "Location"},
		{"Microsoft.ServiceFabric/clusters", "fabric", ""},
		{"Microsoft.MachineLearningServices/workspaces/onlineEndpoints", "mlworkspace/onlineEndpoints/scoring", "Location"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			value := actionAsset(tc.kind, tc.name)
			if tc.kind == "Microsoft.MachineLearningServices/workspaces/onlineEndpoints" {
				value.Identity.NativeID = strings.ToLower(resourceID("Microsoft.MachineLearningServices/workspaces", tc.name))
			}
			id := value.Identity.NativeID
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/"+strings.Split(tc.kind, "/")[0]+"/locations/eastus/operationResults/op-1", "2025-01-01")
			deleted, deletes, polls := false, 0, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if response, handled := emptyMonitorIndexResponse(t, req); handled {
					return response, nil
				}
				if response, handled := emptyDiagnosticSourceIndexResponse(t, req); handled {
					return response, nil
				}
				if strings.EqualFold(req.URL.String(), operation) {
					polls++
					deleted = true
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				path := strings.ToLower(req.URL.Path)
				switch path {
				case id:
					if req.Method == "DELETE" {
						deletes++
						if tc.protocol == "" {
							deleted = true
							return jsonResponse(200, map[string]any{}, http.Header{"X-Ms-Request-Id": {"delete-request"}}), nil
						}
						return jsonResponse(202, map[string]any{}, http.Header{tc.protocol: {operation}, "X-Ms-Request-Id": {"delete-request"}, "Retry-After": {"1"}}), nil
					}
					if deleted {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					raw := nativeResource(tc.kind, tc.name, "eastus", map[string]any{"provisioningState": "Succeeded"})
					raw["id"] = id
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				return nil, fmt.Errorf("unexpected %s %s", req.Method, req.URL)
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-key"}
			preflight, err := driver.Preflight(context.Background(), request)
			if err != nil || !preflight.Allowed {
				t.Fatalf("preflight=%+v err=%v", preflight, err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.ProviderRequestID != "delete-request" {
				t.Fatalf("execute=%+v err=%v", result, err)
			}
			done := false
			for i := 0; i < 5 && !done; i++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				done = wait.Done
			}
			readback, err := driver.Readback(context.Background(), request)
			if !done || err != nil || readback.Exists || deletes != 1 || (tc.protocol != "" && polls == 0) {
				t.Fatalf("done=%v readback=%+v deletes=%d polls=%d err=%v", done, readback, deletes, polls, err)
			}
		})
	}
}

// Deleting these controllers also deletes a managed resource group, which is
// not modeled yet; they must stay read-only rather than hide that cascade.
func TestParityManagedGroupControllersAreReadOnly(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Fatalf("read-only kind reached the product API: %s", req.URL)
		return nil, nil
	})
	for _, kind := range []string{"Microsoft.Purview/accounts", "Microsoft.Solutions/applications", "Microsoft.MachineLearningServices/workspaces"} {
		if _, err := r.ResolveAction(context.Background(), "connection", actionAsset(kind, "controller")); err == nil {
			t.Errorf("%s exposed a delete action", kind)
		}
	}
}

func TestParityKeyVaultKeysListThroughVaultsReadOnly(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	vault := nativeResource("Microsoft.KeyVault/vaults", "keys-vault", "eastus", map[string]any{})
	keyID := text(vault["id"]) + "/keys/signing"
	key := map[string]any{"id": keyID, "name": "signing", "type": "Microsoft.KeyVault/vaults/keys", "location": "eastus", "properties": map[string]any{"kty": "RSA", "keyUri": "https://keys-vault.vault.azure.net/keys/signing"}}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "management.azure.com" {
			t.Fatalf("key inventory left the ARM read plane: %s %s", req.Method, req.URL)
		}
		switch strings.ToLower(req.URL.Path) {
		case root + "/providers/microsoft.keyvault/vaults":
			return jsonResponse(200, map[string]any{"value": []any{vault}}, nil), nil
		case strings.ToLower(text(vault["id"])):
			return jsonResponse(200, vault, nil), nil
		case strings.ToLower(text(vault["id"]) + "/keys"):
			if req.URL.Query().Get("api-version") != "2023-07-01" {
				t.Fatalf("wrong key API version %s", req.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{key}}, nil), nil
		case strings.ToLower(keyID):
			return jsonResponse(200, key, nil), nil
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	request := productRequest(r, "Microsoft.KeyVault/vaults/keys")
	request.Scope.Kind, request.Scope.NativeID = "region", "eastus"
	batch, err := r.List(context.Background(), request)
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(keyID) {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if _, err := r.ResolveAction(context.Background(), "connection", actionAsset("Microsoft.KeyVault/vaults/keys", "keys-vault/keys/signing")); err == nil {
		t.Fatal("ARM exposes no key deletion; the kind must stay read-only")
	}
}

func TestParityManagementGroupsListVisibleDirectoryReadOnly(t *testing.T) {
	groupID := func(name string) string { return "/providers/Microsoft.Management/managementGroups/" + name }
	groups := map[string]map[string]any{
		testTenant: {"id": groupID(testTenant), "type": "Microsoft.Management/managementGroups", "name": testTenant, "properties": map[string]any{"tenantId": testTenant, "displayName": "Tenant Root Group", "details": map[string]any{}}},
		"platform": {"id": groupID("platform"), "type": "Microsoft.Management/managementGroups", "name": "platform", "properties": map[string]any{"tenantId": testTenant, "displayName": "Platform", "details": map[string]any{"parent": map[string]any{"id": groupID(testTenant), "name": testTenant}}}},
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatalf("mutation %s %s", req.Method, req.URL)
		}
		path := req.URL.Path
		if strings.EqualFold(path, "/providers/Microsoft.Management/managementGroups") {
			if req.URL.Query().Get("api-version") != "2023-04-01" {
				t.Fatalf("wrong version %s", req.URL)
			}
			values := []any{}
			for _, name := range []string{testTenant, "platform"} {
				if group, ok := groups[name]; ok {
					values = append(values, map[string]any{"id": group["id"], "type": group["type"], "name": name, "properties": map[string]any{"tenantId": testTenant, "displayName": object(group["properties"])["displayName"]}})
				}
			}
			return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"mg-list"}}), nil
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if group, ok := groups[name]; ok {
			return jsonResponse(200, group, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), nil
	})
	request := productRequest(r, "Microsoft.Management/managementGroups")
	batch, err := r.List(context.Background(), request)
	if err != nil || !batch.Complete || len(batch.Items) != 2 || batch.RequestID != "mg-list" {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	var platform contracts.InventoryItem
	for _, item := range batch.Items {
		if item.Name == "Platform" {
			platform = item
		}
	}
	if platform.Name != "Platform" || platform.Normalized["parent_id"] != strings.ToLower(groupID(testTenant)) || platform.Actionable == nil || *platform.Actionable {
		t.Fatalf("platform = %+v", platform)
	}
	// A group missing from the visible list closes only after its own 404.
	delete(groups, "platform")
	request.KnownNativeIDs = []string{strings.ToLower(groupID("platform"))}
	batch, err = r.List(context.Background(), request)
	if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 1 {
		t.Fatalf("absence batch=%+v err=%v", batch, err)
	}
	groups["foreign"] = map[string]any{"id": groupID("foreign"), "type": "Microsoft.Management/managementGroups", "name": "foreign", "properties": map[string]any{"tenantId": "99999999-9999-4999-8999-999999999999"}}
	request.KnownNativeIDs = []string{strings.ToLower(groupID("foreign"))}
	if _, err := r.List(context.Background(), request); err == nil {
		t.Fatal("a group from another tenant was accepted")
	}
	value := actionAsset("Microsoft.Management/managementGroups", "x")
	value.Identity.NativeID = strings.ToLower(groupID(testTenant))
	if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
		t.Fatal("management groups must stay read-only")
	}
}
