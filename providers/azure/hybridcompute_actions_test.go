package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type hybridCleanupFixture struct {
	*hybridInventoryFixture
	group        map[string]any
	locks        []any
	deleted      map[string]int
	polls        map[string]string
	hold         bool
	deleteStatus int
}

func newHybridCleanupFixture(t *testing.T) *hybridCleanupFixture {
	t.Helper()
	f := &hybridCleanupFixture{hybridInventoryFixture: newHybridInventoryFixture(t), deleted: map[string]int{}, polls: map[string]string{}, deleteStatus: 202}
	groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	f.group = map[string]any{"id": groupID, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("wrong group index")
			}
			return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), true
		}
		if path == groupID {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("wrong group read")
			}
			return jsonResponse(200, f.group, nil), true
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != locksVersion {
				t.Fatal("wrong lock read")
			}
			return jsonResponse(200, map[string]any{"value": append([]any{}, f.locks...)}, nil), true
		}
		if owner := f.polls[path]; owner != "" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != hybridComputeVersion {
				t.Fatal("wrong poll")
			}
			if strings.Contains(path, "/operationstatus/") {
				return jsonResponse(200, map[string]any{"name": last(path), "status": "Succeeded"}, nil), true
			}
			if !f.hold {
				delete(f.values, owner)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, true
		}
		if req.Method != "DELETE" {
			return nil, false
		}
		_, typ, err := parseID(path)
		if err != nil || !hybridComputeChild(hybridComputeKind(typ)) || f.values[path] == nil || req.URL.Query().Get("api-version") != hybridComputeVersion || len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != "" || req.Header.Get("X-Ms-Client-Request-Id") == "" {
			t.Fatal("wrong Arc deletion", req.Method, req.URL.Path)
		}
		f.deleted[path]++
		if f.deleted[path] != 1 {
			t.Fatal("duplicate Arc deletion")
		}
		object(f.values[path]["properties"])["provisioningState"] = "Deleting"
		f.values[path]["etag"] = "after-delete"
		parent := f.values[hybridComputeParent(path, hybridComputeKind(typ))]
		parent["etag"] = "changed-by-child-delete"
		object(parent["properties"])["extensions"] = []any{}
		if f.deleteStatus != 202 {
			if !f.hold {
				delete(f.values, path)
			}
			return &http.Response{StatusCode: f.deleteStatus, Header: http.Header{}, Body: http.NoBody}, true
		}
		status, result := hybridComputeTestURLs()
		status = strings.ReplaceAll(status, "11111111-2222-3333-4444-555555555555", azureRequestID(path))
		result = strings.ReplaceAll(result, "11111111-2222-3333-4444-555555555555", azureRequestID(path))
		for _, endpoint := range []string{status, result} {
			part, _, _ := strings.Cut(strings.TrimPrefix(endpoint, "https://management.azure.com"), "?")
			f.polls[strings.ToLower(part)] = path
		}
		return &http.Response{StatusCode: 202, Header: http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}, Body: http.NoBody}, true
	}
	return f
}

func (f *hybridCleanupFixture) requestAsset(t *testing.T, kind string) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), f.request(kind))
	if err != nil || len(batch.Items) != 1 || !*batch.Items[0].Actionable {
		t.Fatal("Arc cleanup inventory", err)
	}
	item := batch.Items[0]
	value := asset.Asset{ID: asset.AssetID("asset-" + last(kind)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Name: item.Name, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "arc-delete"}
}

func (f *hybridCleanupFixture) driver(t *testing.T, request contracts.ActionRequest) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal("Arc action resolution", err)
	}
	guard, ok := driver.(*monitorTargetAction)
	if !ok {
		t.Fatal("Arc escaped incoming ARM dependency checks")
	}
	if _, ok := guard.inner.(*hybridComputeAction); !ok {
		t.Fatal("Arc escaped native driver")
	}
	return driver
}

func TestHybridComputeChildCleanupRequiresOwnAbsence(t *testing.T) {
	for _, kind := range []string{hybridExtensionType, hybridCommandType, hybridProfileType} {
		t.Run(kind, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			if kind == hybridCommandType {
				for _, raw := range f.values {
					if raw["type"] == kind {
						object(object(raw["properties"])["instanceView"])["executionState"] = "Running"
					}
				}
			}
			request := f.requestAsset(t, kind)
			f.hold = true
			driver := f.driver(t, request)
			if check, err := driver.Preflight(t.Context(), request); err != nil || !check.Allowed || check.Absent {
				t.Fatal("preflight", check, err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("execute", err)
			}
			for range 3 {
				payload, _ := json.Marshal(result)
				if json.Unmarshal(payload, &result) != nil {
					t.Fatal("restore result")
				}
				fresh, err := NewRuntime(f.runtime.credentials)
				if err != nil {
					t.Fatal(err)
				}
				fresh.transport = f.runtime.transport
				f.runtime = fresh
				driver = f.driver(t, request)
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || wait.Done {
					t.Fatal("operation success closed live child", err)
				}
				result.Data = wait.Data
			}
			// Even losing its parent cannot establish a live child's absence.
			parent := hybridComputeParent(request.Asset.Identity.NativeID, kind)
			delete(f.values, parent)
			request.ExecutionResult = &result
			if read, err := driver.Readback(t.Context(), request); err != nil || !read.Exists {
				t.Fatal("parent 404 hid live child", err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || f.deleted[request.Asset.Identity.NativeID] != 1 {
				t.Fatal("resume repeated DELETE", err)
			}
			delete(f.values, request.Asset.Identity.NativeID)
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("own child 404 did not complete", err)
			}
			if f.values[strings.ToLower(resourceID(hybridLicenseType, "license"))] == nil {
				t.Fatal("shared license was deleted")
			}
		})
	}
}

func TestHybridComputeChildCleanupBoundaries(t *testing.T) {
	for _, mode := range []string{"private configuration", "registration", "child tag", "parent tag", "managed parent", "group tag", "managed group", "lock", "group permission", "incoming permission", "child permission", "wrong child", "etag", "parent missing", "own missing", "already deleting", "request tamper", "receipt tamper", "operation missing", "readback permission"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			request := f.requestAsset(t, hybridExtensionType)
			driver := f.driver(t, request)
			id := request.Asset.Identity.NativeID
			parent := hybridComputeParent(id, hybridExtensionType)
			previous := f.override
			failPath := ""
			switch mode {
			case "private configuration":
				object(f.values[id]["properties"])["futurePrivateConfiguration"] = "changed"
			case "registration":
				object(f.values[parent]["properties"])["vmId"] = testTenant
			case "child tag":
				f.values[id]["tags"] = map[string]any{"steward/protected": "true"}
			case "parent tag":
				f.values[parent]["tags"] = map[string]any{"steward/protected": "true"}
			case "managed parent":
				f.values[parent]["managedBy"] = resourceID(hybridMachineType, "other")
			case "group tag":
				f.group["tags"] = map[string]any{"steward/protected": "true"}
			case "managed group":
				f.group["managedBy"] = resourceID(hybridMachineType, "other")
			case "lock":
				f.locks = []any{map[string]any{"id": parent + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group permission":
				failPath = text(f.group["id"])
			case "incoming permission":
				failPath = "/subscriptions/" + testSubscription + "/providers/microsoft.insights/metricalerts"
			case "child permission":
				failPath = id
			case "wrong child":
				f.values[id]["id"] = id + "other"
			case "etag":
				f.values[id]["etag"] = "replaced"
			case "parent missing":
				delete(f.values, parent)
			case "own missing":
				delete(f.values, id)
			case "already deleting":
				object(f.values[id]["properties"])["provisioningState"] = "Deleting"
			case "request tamper":
				request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
				request.Asset.Normalized[hybridComputeCleanupProof] = "forged"
			}
			if failPath != "" {
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, failPath) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return previous(req)
				}
			}
			if mode == "receipt tamper" || mode == "operation missing" || mode == "readback permission" {
				f.hold = true
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "receipt tamper" {
					result.Data["binding"] = "forged"
				} else {
					f.override = func(req *http.Request) (*http.Response, bool) {
						if mode == "operation missing" && strings.Contains(req.URL.Path, "/operationstatus/") {
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), true
						}
						if mode == "readback permission" && strings.EqualFold(req.URL.Path, id) {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return previous(req)
					}
				}
				var failure error
				for range 3 {
					wait, err := driver.Wait(t.Context(), request, result)
					if err != nil {
						failure = err
						break
					}
					if wait.Done {
						t.Fatal("failed verification completed")
					}
					result.Data = wait.Data
				}
				if failure == nil || f.deleted[id] != 1 {
					t.Fatal("failed poll/readback was ignored", failure)
				}
				return
			}
			result, err := driver.Execute(t.Context(), request)
			waitOnly := mode == "parent missing" || mode == "own missing" || mode == "already deleting"
			if (err == nil) != waitOnly || len(f.deleted) != 0 {
				t.Fatal("unsafe preflight mutation", mode, err)
			}
			if waitOnly {
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || wait.Done != (mode == "own missing") {
					t.Fatal("incorrect absence result", err)
				}
			}
		})
	}
}

func TestHybridComputeSynchronousChildDelete(t *testing.T) {
	for _, status := range []int{200, 204, 404} {
		f := newHybridCleanupFixture(t)
		f.deleteStatus = status
		request := f.requestAsset(t, hybridExtensionType)
		driver := f.driver(t, request)
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("synchronous delete", status, err)
		}
		wait, err := driver.Wait(t.Context(), request, result)
		if err != nil || !wait.Done {
			t.Fatal("own read after synchronous delete", status, err)
		}
	}
}

func TestHybridComputeChildIncomingRuleAndUnverifiedRegistration(t *testing.T) {
	f := newHybridCleanupFixture(t)
	request := f.requestAsset(t, hybridExtensionType)
	driver := f.driver(t, request)
	monitor := newMonitorInventoryFixture(t, monitorActivityAlertType)
	var rule map[string]any
	for _, raw := range monitor.objects {
		rule = batchClone(raw)
		break
	}
	rule["id"], rule["name"] = resourceID(monitorActivityAlertType, "arc-alert"), "arc-alert"
	object(rule["properties"])["scopes"] = []any{request.Asset.Identity.NativeID}
	object(rule["properties"])["actions"] = map[string]any{"actionGroups": []any{}}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, text(rule["id"])) || strings.EqualFold(req.URL.Path, monitor.collection) {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != monitor.version {
				t.Fatal("wrong incoming rule read")
			}
			if strings.EqualFold(req.URL.Path, monitor.collection) {
				return jsonResponse(200, map[string]any{"value": []any{rule}}, nil), true
			}
			return jsonResponse(200, rule, nil), true
		}
		return previous(req)
	}
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", []asset.Asset{request.Asset})
	if err != nil {
		t.Fatal("Arc incoming dependency graph", err)
	}
	found := false
	for _, ref := range contribution.Unresolved {
		if strings.EqualFold(ref.NativeID, text(rule["id"])) && ref.Relationship == graph.RelationshipDependsOn && ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("unindexed incoming alert was not a required dependency")
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
		t.Fatal("Arc child deleted before incoming alert", err)
	}
	f.override = previous
	object(f.values[hybridComputeParent(request.Asset.Identity.NativeID, hybridExtensionType)]["properties"])["vmId"] = nil
	batch, err := f.runtime.List(t.Context(), f.request(hybridExtensionType))
	if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable || batch.Items[0].Normalized["cleanup_protected"] != true {
		t.Fatal("unverified machine registration became actionable", err)
	}
}

func TestHybridComputeRejectsMalformedCleanupMetadata(t *testing.T) {
	for _, field := range []string{"etag", "eTag", "managedBy", "kind", "tags"} {
		t.Run(field, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			for id, raw := range f.values {
				if raw["type"] == hybridExtensionType {
					raw[field] = map[string]any{"steward/protected": true}
					if _, err := f.client.hybridComputeRead(t.Context(), id, hybridExtensionType); err == nil {
						t.Fatal("malformed cleanup metadata accepted")
					}
				}
			}
		})
	}
}
