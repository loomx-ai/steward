package azure

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type localCleanupFixture struct {
	*azureLocalFixture
	group          map[string]any
	locks          []any
	deleted, polls int
	hold           bool
	status         int
	mode           string
}

func newLocalCleanupFixture(t *testing.T) *localCleanupFixture {
	t.Helper()
	f := &localCleanupFixture{azureLocalFixture: newAzureLocalFixture(t), status: 202, mode: "Azure-AsyncOperation"}
	object(f.values[f.ids[hybridMachineType]]["properties"])["vmId"] = testTenant
	groupID := strings.Join(strings.Split(f.ids[azureLocalAgentType], "/")[:5], "/")
	f.group = map[string]any{"id": groupID, "name": last(groupID), "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path == groupID || path == "/subscriptions/"+testSubscription+"/resourcegroups" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("group request")
			}
			if path == groupID {
				return jsonResponse(200, f.group, nil), true
			}
			return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), true
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != locksVersion {
				t.Fatal("lock request")
			}
			return jsonResponse(200, map[string]any{"value": append([]any{}, f.locks...)}, nil), true
		}
		if strings.Contains(path, "/providers/microsoft.azurestackhci/locations/") {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != azureLocalVersion {
				t.Fatal("poll request")
			}
			f.polls++
			if !f.hold {
				delete(f.values, f.ids[azureLocalAgentType])
			}
			if f.mode == "Location" {
				return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
			}
			return jsonResponse(200, map[string]any{"status": "Succeeded", "name": last(path)}, http.Header{"X-Ms-Request-Id": {"local-poll"}}), true
		}
		if req.Method != "DELETE" {
			return nil, false
		}
		body, _ := io.ReadAll(req.Body)
		if path != f.ids[azureLocalAgentType] || req.URL.Query().Get("api-version") != azureLocalVersion || len(req.URL.Query()) != 1 || len(body) != 0 || req.Header.Get("If-Match") != "" || req.Header.Get("X-Ms-Client-Request-Id") == "" {
			t.Fatal("wrong native guest DELETE", req.URL)
		}
		f.deleted++
		if f.deleted != 1 {
			t.Fatal("guest DELETE replayed")
		}
		object(f.values[path]["properties"])["provisioningState"] = "Deleting"
		object(f.values[path]["properties"])["status"] = "disconnected"
		f.values[path]["etag"] = "after-delete"
		header := http.Header{"X-Ms-Request-Id": {"local-delete"}, "Retry-After": {"3"}}
		if f.status == 202 {
			header.Set(f.mode, localCleanupPollURL(f.mode))
		} else if !f.hold {
			delete(f.values, path)
		}
		return &http.Response{StatusCode: f.status, Header: header, Body: http.NoBody}, true
	}
	return f
}

func localCleanupPollURL(mode string) string {
	collection := "operationStatuses"
	if mode == "Location" {
		collection = "operationResults"
	}
	return "https://management.azure.com/subscriptions/" + testSubscription + "/providers/Microsoft.AzureStackHCI/locations/eastus/" + collection + "/11111111-2222-3333-4444-555555555555?api-version=" + azureLocalVersion
}

func (f *localCleanupFixture) requestAsset(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), f.request(azureLocalAgentType))
	if err != nil || len(batch.Items) != 1 || !*batch.Items[0].Actionable {
		t.Fatal("guest cleanup inventory", err)
	}
	item := batch.Items[0]
	value := asset.Asset{ID: "local-guest", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Name: item.Name, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "local-guest-delete"}
}

func (f *localCleanupFixture) driver(t *testing.T, request contracts.ActionRequest) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := driver.(*monitorTargetAction)
	if !ok {
		t.Fatal("missing incoming ARM dependency guard")
	}
	if _, ok := guard.inner.(*azureLocalAction); !ok {
		t.Fatal("missing native guest driver")
	}
	return driver
}

func TestAzureLocalGuestCleanupAndOwnReadback(t *testing.T) {
	for _, mode := range []string{"Azure-AsyncOperation", "Location", "synchronous"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalCleanupFixture(t)
			if mode == "synchronous" {
				f.status = 204
			} else {
				f.mode = mode
			}
			request := f.requestAsset(t)
			driver := f.driver(t, request)
			f.hold = true
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.deleted != 1 || result.ProviderRequestID != "local-delete" {
				t.Fatal("native guest deletion", err, result)
			}
			request.ExecutionResult = &result
			if _, err := driver.Execute(t.Context(), request); err != nil || f.deleted != 1 {
				t.Fatal("saved deletion replay", err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || wait.Done {
				t.Fatal("operation success erased surviving guest", err, wait)
			}
			result.Data = wait.Data
			// Restore persisted phase data and a fresh action driver.
			encoded, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if json.Unmarshal(encoded, &restored) != nil {
				t.Fatal("receipt persistence")
			}
			request.ExecutionResult = &restored
			driver = f.driver(t, request)
			delete(f.values, f.ids[azureLocalAgentType])
			wait, err = driver.Wait(t.Context(), request, restored)
			if err != nil || !wait.Done || f.deleted != 1 {
				t.Fatal("own absence", err, wait)
			}
			if f.values[f.ids[azureLocalVMType]] == nil || f.values[f.ids[hybridMachineType]] == nil || f.values[f.ids[azureLocalIdentityType]] == nil {
				t.Fatal("guest cleanup removed parent or identity")
			}
		})
	}
}

func TestAzureLocalGuestCleanupDriftAndProtection(t *testing.T) {
	for _, change := range []string{"guest", "vm", "machine", "guest-etag", "guest-tag", "vm-tag", "machine-tag", "group-tag", "group-owner", "vm-owner", "lock", "forged-record", "connection", "parameter", "prerequisite"} {
		t.Run(change, func(t *testing.T) {
			f := newLocalCleanupFixture(t)
			request := f.requestAsset(t)
			driver := f.driver(t, request)
			guest, vm, machine := f.values[f.ids[azureLocalAgentType]], f.values[f.ids[azureLocalVMType]], f.values[f.ids[hybridMachineType]]
			switch change {
			case "guest":
				object(guest["properties"])["futurePrivateConfiguration"] = "changed"
			case "vm":
				object(vm["properties"])["hardwareProfile"] = map[string]any{"memoryMB": 8192}
			case "machine":
				object(machine["properties"])["vmId"] = testSubscription
			case "guest-etag":
				guest["etag"] = "replaced"
			case "guest-tag":
				guest["tags"] = map[string]any{"steward:protected": "true"}
			case "vm-tag":
				vm["tags"] = map[string]any{"steward:protected": "true"}
			case "machine-tag":
				machine["tags"] = map[string]any{"steward:protected": "true"}
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "group-owner":
				f.group["managedBy"] = resourceID(azureLocalVMType, "owner")
			case "vm-owner":
				vm["managedBy"] = resourceID(azureLocalStorageType, "owner")
			case "lock":
				f.locks = []any{map[string]any{"id": f.ids[hybridMachineType] + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forged-record":
				request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
				request.Asset.Normalized[azureLocalCleanupProof] = "forged"
			case "connection":
				request.Asset.Identity.ConnectionID = "foreign"
			case "parameter":
				request.Parameters = map[string]any{"force": true}
			case "prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{}}
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || f.deleted != 0 {
				t.Fatal("unreviewed guest mutation", change, err)
			}
		})
	}
	for _, parent := range []string{azureLocalVMType, hybridMachineType} {
		t.Run("missing-"+last(parent), func(t *testing.T) {
			f := newLocalCleanupFixture(t)
			request := f.requestAsset(t)
			driver := f.driver(t, request)
			delete(f.values, f.ids[parent])
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.deleted != 0 {
				t.Fatal("missing parent caused mutation", err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || wait.Done {
				t.Fatal("parent absence erased child", err)
			}
		})
	}
	f := newLocalCleanupFixture(t)
	request := f.requestAsset(t)
	driver := f.driver(t, request)
	original := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, f.ids[azureLocalVMType]) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return original(req)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || f.deleted != 0 {
		t.Fatal("denied parent caused mutation")
	}
}
