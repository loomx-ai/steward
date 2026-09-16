package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Compose the original component and ARM child examples with the documented
// managed-workspace lifecycle. The latter is protocol data, not a cloud capture.
type insightsComponentFixture struct {
	*insightsWorkspaceFixture
	parentGone, groupGone, workspaceGone bool
	holdGroup, holdWorkspace             bool
	deleteStatus                         int
	deleteBody                           any
	deleteHeader                         http.Header
	response                             func(*http.Request) (*http.Response, bool)
	genericRows                          []any
}

func newInsightsComponentFixture(t *testing.T) *insightsComponentFixture {
	t.Helper()
	f := &insightsComponentFixture{insightsWorkspaceFixture: newInsightsWorkspaceFixture(t), deleteStatus: 204}
	workspaceHandler := f.override
	arm := newInsightsARMInventoryFixture(t)
	armHandler := arm.override
	maps.Copy(f.children, arm.children)
	arm.insightsInventoryFixture = f.insightsInventoryFixture
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if f.response != nil {
			if result, ok := f.response(req); ok {
				return result, true
			}
		}
		if path == f.parentID {
			if req.URL.Query().Get("api-version") != insightsComponentVersion || len(req.URL.Query()) != 1 {
				t.Fatal("component native version changed", req.URL)
			}
			if req.Method == "DELETE" {
				if req.Header.Get("If-Match") != "" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") == "" {
					t.Fatal("component DELETE departed from its native contract")
				}
				f.deletes = append(f.deletes, path)
				f.parentGone = true
				workspace, _ := insightsWorkspaceID(f.parent)
				owner, _ := insightsManagedBy(f.managed)
				if workspace == f.workspaceID && owner == f.parentID {
					f.groupGone, f.workspaceGone = !f.holdGroup, !f.holdWorkspace
				}
				if f.deleteBody != nil {
					return jsonResponse(f.deleteStatus, f.deleteBody, f.deleteHeader), true
				}
				return &http.Response{StatusCode: f.deleteStatus, Header: f.deleteHeader, Body: http.NoBody}, true
			}
			if f.parentGone {
				return jsonResponse(404, map[string]any{}, nil), true
			}
		}
		if path == f.managedID && f.groupGone || path == f.workspaceID && f.workspaceGone {
			return jsonResponse(404, map[string]any{}, nil), true
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.operationalinsights/workspaces" {
			return jsonResponse(200, map[string]any{"value": []any{f.workspace}}, nil), true
		}
		if path == "/subscriptions/"+testSubscription+"/resources" && f.genericRows != nil {
			return jsonResponse(200, map[string]any{"value": f.genericRows}, nil), true
		}
		if result, ok := armHandler(req); ok {
			return result, true
		}
		if req.Method == "GET" {
			return workspaceHandler(req)
		}
		return nil, false // Original legacy fixture owns each exact child DELETE.
	}
	return f
}

// Use native inventory, SQLite projection and application graph rebuilding;
// action capabilities and ownership come from registered production adapters.
func insightsComponentPlan(t *testing.T, f *insightsComponentFixture, extraKinds ...string) (contracts.ActionRequest, plan.Result, []asset.Asset) {
	t.Helper()
	ctx, r := t.Context(), f.runtime
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "component.db"), "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: time.Now().UTC()}
	if err := repository.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	scope := asset.Scope{ID: "scope", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := repository.PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	// The original linked-storage example references this shared account.
	// It was discovered separately and must remain outside the component plan.
	storageRaw := nativeResource(storageType, "storageAccountName", "southcentralus", map[string]any{})
	storageRaw["kind"] = "StorageV2"
	storage := dnsAsset(t, r, storageRaw)
	f.groupMembers[storage.Identity.NativeID] = storageRaw
	storage.ScopeID = scope.ID
	if err := repository.PutAsset(ctx, storage); err != nil {
		t.Fatal(err)
	}
	projection := inventory.NewService(repository)
	if f.genericRows != nil {
		_, shards, err := projection.CreateScan(ctx, inventory.ScanRequest{ConnectionID: connection.ID, RequestedBy: "native-unknown-member", Shards: []inventory.ShardRequest{{Provider: asset.ProviderAzure, Source: inventorySource, ScopeID: scope.ID}}})
		if err != nil {
			t.Fatal(err)
		}
		page, err := r.List(ctx, contracts.InventoryRequest{ConnectionID: connection.ID, Source: inventorySource, Scope: scope})
		if err != nil || !page.Complete {
			t.Fatal("unknown native member inventory", err)
		}
		if err := projection.ProjectBatch(ctx, &shards[0], connection, page, inventory.ProjectionOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := projection.FinishShard(ctx, &shards[0], asset.ShardSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	kinds := append([]string{applicationInsightsType, groupType, insightsWorkspaceType}, insightsComponentChildKinds()...)
	for _, kind := range append(kinds, extraKinds...) {
		request := productRequest(r, kind)
		_, shards, err := projection.CreateScan(ctx, inventory.ScanRequest{ConnectionID: connection.ID, RequestedBy: "native-component-test", Shards: []inventory.ShardRequest{{Provider: asset.ProviderAzure, Source: request.Source, ScopeID: scope.ID, ResourceKindID: request.ResourceKind.ID, Authoritative: request.Source != insightsAnnotationSource}}})
		if err != nil {
			t.Fatal(err)
		}
		page, err := r.List(ctx, request)
		if err != nil || !page.Complete {
			t.Fatal("native component inventory", kind, err)
		}
		if err := projection.ProjectBatch(ctx, &shards[0], connection, page, inventory.ProjectionOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := projection.FinishShard(ctx, &shards[0], asset.ShardSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var parent asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == applicationInsightsType {
			parent = value
		}
	}
	if !parent.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatal("registered component action missing")
	}
	lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := governance.NewService(repository, repository).RebuildGraph(ctx, scope.ID, connection.ID, "component-native", r.bundle, []governance.Contributor{lifecycle, NewResourceAttachments()})
	if err != nil || len(graph.Unresolved) != 0 {
		t.Fatal("component graph incomplete", graph.Unresolved, err)
	}
	planned, err := plan.Solve(plan.Input{Assets: values, Relationships: graph.Relationships, LifecycleBindings: graph.Bindings, ResolvedAssetIDs: []asset.AssetID{parent.ID}})
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) == 0 || planned.Steps[len(planned.Steps)-1].AssetID != parent.ID {
		t.Fatal("component plan incomplete", planned, err)
	}
	request := servicePlanRequest(planned, values, parent)
	request.IdempotencyKey = "component-native-delete"
	return request, planned, values
}

func deleteInsightsPrerequisites(t *testing.T, f *insightsComponentFixture, planned plan.Result, values []asset.Asset, parent asset.AssetID) {
	t.Helper()
	for _, step := range planned.Steps {
		if step.AssetID == parent {
			continue
		}
		index := slices.IndexFunc(values, func(value asset.Asset) bool { return value.ID == step.AssetID })
		request := servicePlanRequest(planned, values, values[index])
		request.IdempotencyKey = string(step.AssetID)
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("prerequisite deletion failed", request.Asset.Identity.NativeID, err)
		}
		if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
			t.Fatal("prerequisite absence unverified", wait, err)
		}
	}
}

func TestApplicationInsightsComponentManagedDeletion(t *testing.T) {
	f := newInsightsComponentFixture(t)
	request, planned, values := insightsComponentPlan(t, f)
	if len(planned.Steps) != 14 || len(request.PrerequisiteDeletions) != 13 || len(request.LifecycleImpacts) != 2 {
		t.Fatal("component plan lost independent children or delegated workspace", request, planned)
	}
	contributor, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	for _, impact := range request.LifecycleImpacts {
		input := plan.Input{Assets: values, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings, ResolvedAssetIDs: []asset.AssetID{request.Asset.ID}, RequestOptions: map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_resources": []string{impact.Asset.Identity.NativeID}}}}
		if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
			t.Fatal("managed group retention was ignored", retained, err)
		}
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
		t.Fatal("component ignored live native prerequisites")
	}
	deleteInsightsPrerequisites(t, f, planned, values, request.Asset.ID)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || !f.parentGone || !f.groupGone || !f.workspaceGone || len(f.deletes) != 14 || f.deletes[13] != f.parentID {
		t.Fatal("registered component deletion incomplete", result, f.deletes, err)
	}
	payload, _ := json.Marshal(request)
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(result)
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("component receipt failed restart", wait, err)
	}
	request.ExecutionResult = &result
	if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
		t.Fatal("component or managed workspace remains", read, err)
	}
	if check, err := driver.Preflight(t.Context(), request); err != nil || !check.Allowed || !check.Absent {
		t.Fatal("completed component is not idempotent", check, err)
	}
}

func TestApplicationInsightsComponentResidualReadback(t *testing.T) {
	for _, mode := range []string{"group", "workspace", "already-absent", "group-owner", "group-lro", "workspace-replaced", "workspace-partial", "workspace-lro", "workspace-code", "workspace-denied", "parent-reappears", "group-reappears"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			request, _, _ := insightsComponentPlan(t, f)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			f.holdGroup = strings.HasPrefix(mode, "group")
			f.holdWorkspace = strings.HasPrefix(mode, "workspace")
			if mode == "already-absent" {
				f.parentGone = true
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "already-absent" && len(f.deletes) != 0 {
				t.Fatal("absent component was deleted again")
			}
			switch mode {
			case "group-owner":
				f.managed["managedBy"] = resourceID(applicationInsightsType, "replacement")
			case "workspace-replaced":
				object(f.workspace["properties"])["customerId"] = "replacement"
			case "parent-reappears", "group-reappears":
				f.groupGone, f.workspaceGone = true, true
				f.response = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, f.workspaceID) {
						if mode == "parent-reappears" {
							f.parentGone = false
							object(f.parent["properties"])["AppId"] = "replacement"
						} else {
							f.groupGone = false
						}
					}
					return nil, false
				}
			case "group-lro", "workspace-partial", "workspace-lro", "workspace-code", "workspace-denied":
				f.response = func(req *http.Request) (*http.Response, bool) {
					target, raw := f.workspaceID, f.workspace
					if mode == "group-lro" {
						target, raw = f.managedID, f.managed
					}
					if !strings.EqualFold(req.URL.Path, target) {
						return nil, false
					}
					status, headers := 200, http.Header{}
					switch mode {
					case "workspace-partial":
						status = 206
					case "workspace-denied":
						status = 403
					case "workspace-code":
						raw = maps.Clone(raw)
						raw["code"] = "Incomplete"
					default:
						headers.Set("Location", apiURL(target, resourcesVersion))
					}
					return jsonResponse(status, raw, headers), true
				}
			}
			wait, err := driver.Wait(t.Context(), request, result)
			pending := mode == "group" || mode == "workspace" || mode == "already-absent" || mode == "group-reappears"
			if pending {
				if err != nil || wait.Done {
					t.Fatal("residual native resource was treated as complete", wait, err)
				}
				f.response = nil
				f.groupGone, f.workspaceGone = true, true
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal("residual absence did not complete", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatal("invalid or replaced native residual accepted", wait, err)
			}
		})
	}
}

func TestApplicationInsightsComponentRetainsUnownedWorkspace(t *testing.T) {
	for _, mode := range []string{"shared", "other-owner", "foreign", "detached", "classic"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			switch mode {
			case "shared":
				delete(f.managed, "managedBy")
			case "other-owner":
				f.managed["managedBy"] = resourceID(applicationInsightsType, "other")
			case "foreign":
				object(f.parent["properties"])["WorkspaceResourceId"] = strings.Replace(f.workspaceID, testSubscription, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
			case "detached":
				object(f.parent["properties"])["WorkspaceResourceId"] = resourceID(insightsWorkspaceType, "shared")
			case "classic":
				delete(object(f.parent["properties"]), "WorkspaceResourceId")
				delete(object(f.parent["properties"]), "IngestionMode")
			}
			// A shared/foreign workspace reference is intentionally unresolved
			// by inventory in this subscription. Root planning needs no ownership
			// of that retained target, so use the native component asset directly.
			item := workspaceInventory(t, f.insightsWorkspaceFixture)
			value := asset.Asset{ID: "component", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: applicationInsightsType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "retained-workspace"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.groupGone || f.workspaceGone || len(f.deletes) != 1 || f.deletes[0] != f.parentID {
				t.Fatal("component cleanup acquired an unowned workspace", result, f.deletes, err)
			}
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("retained workspace incorrectly delayed component completion", wait, err)
			}
		})
	}
}

func TestApplicationInsightsComponentPreflightBoundaries(t *testing.T) {
	for _, mode := range []string{"parent-replaced", "workspace-switched", "workspace-private-change", "group-owner", "new-member", "protected", "lock", "child-added", "missing-impact", "impact-retained", "extra-impact", "state-tampered", "connection", "parameters"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			request, _, _ := insightsComponentPlan(t, f)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "parent-replaced":
				object(f.parent["properties"])["AppId"] = "replacement"
			case "workspace-switched":
				object(f.parent["properties"])["WorkspaceResourceId"] = resourceID(insightsWorkspaceType, "other")
			case "workspace-private-change":
				object(f.workspace["properties"])["privateConfiguration"] = "changed"
			case "group-owner":
				f.managed["managedBy"] = resourceID(applicationInsightsType, "other")
			case "new-member":
				id := f.managedID + "/providers/unknown.provider/widgets/new"
				f.members[id] = map[string]any{"id": id, "type": "unknown.provider/widgets", "name": "new"}
			case "protected":
				f.workspace["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.workspaceID + "/tables/table/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "child-added":
				id, _ := insightsLegacyURL(f.parentID, insightsAnalyticsType, "new")
				f.children[id] = map[string]any{"Id": "new", "Name": "new"}
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[:1]
			case "impact-retained":
				request.LifecycleImpacts[0].Delete = false
			case "extra-impact":
				copy := request.LifecycleImpacts[0]
				copy.Asset.ID = "unrelated"
				copy.Asset.Identity.NativeID = resourceID(insightsWorkspaceType, "unrelated")
				request.LifecycleImpacts = append(request.LifecycleImpacts, copy)
			case "state-tampered":
				object(request.Asset.Normalized["_insights_workspace"])["managed_group"] = ""
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			}
			if check, err := driver.Preflight(t.Context(), request); err == nil && check.Allowed {
				t.Fatal("changed component scope passed preflight", check)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
				t.Fatal("changed component scope reached DELETE", f.deletes, err)
			}
		})
	}
}

func TestApplicationInsightsComponentDeleteProtocolAndReceipt(t *testing.T) {
	for _, mode := range []string{"200", "204", "202", "body", "lro", "receipt", "operation", "impact", "prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			request, _, _ := insightsComponentPlan(t, f)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "200":
				f.deleteStatus = 200
			case "202":
				f.deleteStatus = 202
			case "body":
				f.deleteStatus, f.deleteBody = 200, map[string]any{"status": "Succeeded"}
			case "lro":
				f.deleteHeader = http.Header{"Location": {apiURL(f.parentID, insightsComponentVersion)}}
			}
			result, err := driver.Execute(t.Context(), request)
			if mode == "202" || mode == "body" || mode == "lro" {
				if err == nil {
					t.Fatal("unsupported component DELETE protocol accepted", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "receipt":
				result.Data["_insights_component_receipt"] = "changed"
			case "operation":
				result.ProviderOperationID = apiURL(f.parentID, insightsComponentVersion)
			case "impact":
				request.LifecycleImpacts[0].Asset.Normalized["changed"] = true
			case "prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.LifecycleImpacts[0])
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if mode == "200" || mode == "204" {
				if err != nil || !wait.Done {
					t.Fatal("native synchronous deletion did not complete", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatal("changed persisted execution evidence accepted", wait, err)
			}
		})
	}
}

func insightsComponentPrivateLinks(t *testing.T, f *insightsComponentFixture) (string, map[string]map[string]any) {
	t.Helper()
	return insightsComponentPrivateLinksAt(t, f, strings.ToLower(resourceID(monitorPrivateLinkType, "shared-scope")))
}

func insightsComponentPrivateLinksAt(t *testing.T, f *insightsComponentFixture, scopeID string) (string, map[string]map[string]any) {
	t.Helper()
	scope := monitorPrivateLinkExample(t, "PrivateLinkScopesGet")
	scope["id"], scope["name"] = scopeID, "shared-scope"
	if inResourceGroup(scopeID, f.groupID) {
		f.groupMembers[scopeID] = scope
	} else {
		group := strings.Join(strings.Split(scopeID, "/")[:5], "/")
		f.groups[group] = map[string]any{"id": group, "name": last(group), "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	}
	capability := monitorPrivateLinkExample(t, "PrivateLinkScopePrivateLinkResourceGet")
	capability["id"] = scopeID + "/privateLinkResources/azuremonitor"
	links := map[string]map[string]any{}
	for _, target := range []struct {
		name, id, field, idField, scopeField string
		raw                                  map[string]any
	}{
		{"component", f.parentID, "PrivateLinkScopedResources", "ResourceId", "ScopeId", f.parent},
		{"workspace", f.workspaceID, "privateLinkScopedResources", "resourceId", "scopeId", f.workspace},
	} {
		id := scopeID + "/scopedresources/" + target.name
		raw := monitorPrivateLinkExample(t, "PrivateLinkScopedResourceGet")
		raw["id"], raw["name"] = id, target.name
		object(raw["properties"])["linkedResourceId"] = target.id
		links[id] = raw
		object(target.raw["properties"])[target.field] = []any{map[string]any{target.idField: id, target.scopeField: "immutable-scope-identifier"}}
	}
	previous := f.response
	f.response = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		group := strings.Join(strings.Split(scopeID, "/")[:5], "/")
		if group != f.groupID && path == group+"/resources" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("unexpected external scope group list", req.Method, req.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{scope}}, nil), true
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.insights/privatelinkscopes" {
			return jsonResponse(200, map[string]any{"value": []any{scope}}, nil), true
		}
		if path == scopeID || strings.HasPrefix(path, scopeID+"/") && !strings.Contains(strings.TrimPrefix(path, scopeID), "/providers/") {
			if req.URL.Query().Get("api-version") != monitorPrivateLinkVersion || len(req.URL.Query()) != 1 {
				t.Fatal("AMPLS native API version changed", req.URL)
			}
			if req.Method == "DELETE" {
				link := links[path]
				if link == nil || req.Header.Get("If-Match") != "" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") == "" {
					t.Fatal("cleanup attempted to delete shared scope or changed unlink contract", req.URL)
				}
				f.deletes = append(f.deletes, path)
				target, _ := monitorPrivateLinkReference(link)
				if target == f.parentID {
					object(f.parent["properties"])["PrivateLinkScopedResources"] = []any{}
					f.parent["etag"] = "after-component-unlink"
				} else {
					object(f.workspace["properties"])["privateLinkScopedResources"] = []any{}
					f.workspace["etag"] = "after-workspace-unlink"
				}
				delete(links, path)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, true
			}
			switch path {
			case scopeID:
				return jsonResponse(200, scope, nil), true
			case scopeID + "/scopedresources":
				rows := []any{}
				for _, id := range slices.Sorted(maps.Keys(links)) {
					rows = append(rows, links[id])
				}
				return jsonResponse(200, map[string]any{"value": rows}, nil), true
			case scopeID + "/privateendpointconnections":
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			case scopeID + "/privatelinkresources":
				return jsonResponse(200, map[string]any{"value": []any{capability}}, nil), true
			case scopeID + "/privatelinkresources/azuremonitor":
				return jsonResponse(200, capability, nil), true
			default:
				if link := links[path]; link != nil {
					return jsonResponse(200, link, nil), true
				}
				return jsonResponse(404, map[string]any{}, nil), true
			}
		}
		if previous != nil {
			return previous(req)
		}
		return nil, false
	}
	return scopeID, links
}

func TestApplicationInsightsComponentPrivateLinkPrerequisites(t *testing.T) {
	f := newInsightsComponentFixture(t)
	scopeID, links := insightsComponentPrivateLinks(t, f)
	request, planned, values := insightsComponentPlan(t, f, monitorPrivateLinkType, monitorScopedResourceType)
	if len(planned.Steps) != 16 || len(request.PrerequisiteDeletions) != 15 || len(request.LifecycleImpacts) != 2 {
		t.Fatal("workspace association was not ordered before component deletion", len(planned.Steps), request)
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.Asset.Identity.NativeID == scopeID {
			t.Fatal("component acquired ownership of shared private-link scope")
		}
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
		t.Fatal("component deletion bypassed live prerequisites")
	}
	deleteInsightsPrerequisites(t, f, planned, values, request.Asset.ID)
	if len(links) != 0 || len(f.deletes) != 15 {
		t.Fatal("native associations did not reach independent deletion", links, f.deletes)
	}
	// Association removal changes ETags and reverse references legitimately.
	// The frozen workspace identity and private settings must still match.
	result, err := driver.Execute(t.Context(), request)
	if err != nil || !f.groupGone || !f.workspaceGone || f.deletes[len(f.deletes)-1] != f.parentID {
		t.Fatal("reviewed AMPLS unlink invalidated component cleanup", result, err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("component with reviewed workspace unlink did not finish", wait, err)
	}
}

func TestApplicationInsightsComponentManagedNativeTree(t *testing.T) {
	for _, mode := range []string{"delayed", "private-change", "duplicate-indexed-child", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			vmID := f.managedID + "/providers/microsoft.compute/virtualmachines/worker"
			extensionID := vmID + "/extensions/bootstrap"
			vm := map[string]any{"id": vmID, "name": "worker", "type": vmType, "location": "southcentralus", "properties": map[string]any{"vmId": "immutable-vm", "provisioningState": "Succeeded"}}
			// A proxy GET can omit location while inventory inherits the VM's.
			extension := map[string]any{"id": extensionID, "name": "bootstrap", "type": vmExtensionType, "properties": map[string]any{"publisher": "publisher", "type": "CustomScript", "protectedSettings": map[string]any{"private": "PRIVATE_TREE_CONFIGURATION"}, "provisioningState": "Succeeded"}}
			f.members[vmID] = vm
			if mode == "duplicate-indexed-child" {
				f.members[extensionID] = extension
			}
			if mode == "unknown" {
				id := f.managedID + "/providers/unknown.provider/widgets/opaque"
				raw := map[string]any{"id": id, "name": "opaque", "type": "unknown.provider/widgets", "location": "southcentralus", "properties": map[string]any{"password": "PRIVATE_UNKNOWN_CONFIGURATION"}}
				f.members[id], f.genericRows = raw, []any{raw}
			}
			vmGone, extensionGone := false, false
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == vmID || path == extensionID || path == vmID+"/extensions" || path == "/subscriptions/"+testSubscription+"/providers/microsoft.compute/virtualmachines" {
					if req.Method != "GET" {
						t.Fatal("managed group member received an independent DELETE", req.URL)
					}
					switch path {
					case vmID:
						if !vmGone {
							return jsonResponse(200, vm, nil), true
						}
					case extensionID:
						if !extensionGone {
							return jsonResponse(200, extension, nil), true
						}
					case vmID + "/extensions":
						return jsonResponse(200, map[string]any{"value": []any{extension}}, nil), true
					default:
						return jsonResponse(200, map[string]any{"value": []any{vm}}, nil), true
					}
					return jsonResponse(404, map[string]any{}, nil), true
				}
				return nil, false
			}
			request, planned, _ := insightsComponentPlan(t, f, vmType, vmExtensionType)
			want := 4
			if mode == "unknown" {
				want++
			}
			if len(planned.Steps) != 1 || len(request.LifecycleImpacts) != want || len(request.PrerequisiteDeletions) != 0 {
				t.Fatal("known native descendants or unknown group member omitted", len(request.LifecycleImpacts), planned)
			}
			payload, _ := json.Marshal(request)
			if strings.Contains(string(payload), "PRIVATE_TREE_CONFIGURATION") || strings.Contains(string(payload), "PRIVATE_UNKNOWN_CONFIGURATION") {
				t.Fatal("native private group configuration leaked into persisted plan")
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "private-change" {
				object(object(extension["properties"])["protectedSettings"])["private"] = "changed"
				if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
					t.Fatal("changed private descendant configuration reached root DELETE", err)
				}
				return
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("native group subtree deletion failed", err)
			}
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
				t.Fatal("VM remains after group absence", wait, err)
			}
			vmGone = true
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
				t.Fatal("extension remains after VM absence", wait, err)
			}
			extensionGone = true
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done || len(f.deletes) != 1 {
				t.Fatal("native subtree absence did not complete root deletion", wait, err)
			}
		})
	}
}

func TestApplicationInsightsComponentNestedManagedController(t *testing.T) {
	for _, kind := range []string{aksType, monitorWorkspaceType, applicationInsightsType} {
		t.Run(kind, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			clear(f.children)
			id := f.managedID + "/providers/" + strings.ToLower(kind) + "/nested"
			raw := map[string]any{"id": id, "name": "nested", "type": kind, "location": "southcentralus", "properties": map[string]any{}}
			f.members[id] = raw
			f.response = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, id) {
					return jsonResponse(200, raw, nil), true
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType)); err == nil || len(f.deletes) != 0 {
				t.Fatal("unmodeled nested managed groups gained deletion authority", err)
			}
		})
	}
}
