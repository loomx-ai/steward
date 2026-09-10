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

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type insightsWorkspaceFixture struct {
	*insightsInventoryFixture
	workspaceID, managedID string
	workspace, managed     map[string]any
	groups, members        map[string]map[string]any
	groupLists, lists      int
	requests               []string
	response               func(*http.Request) (*http.Response, bool)
}

// The component retains its original published GET fields with the example's
// scope substituted. Managed-group ownership and workspace responses below are
// composed ARM protocol data: the checked-in CLI records do not contain this
// newly documented managed-workspace lifecycle.
func newInsightsWorkspaceFixture(t *testing.T) *insightsWorkspaceFixture {
	t.Helper()
	f := &insightsWorkspaceFixture{insightsInventoryFixture: newInsightsInventoryFixture(t)}
	f.parent = insightsScopedExample(t, "stable/2020-02-02/examples/ComponentsGet.json", f.parentID)
	f.managedID = "/subscriptions/" + testSubscription + "/resourcegroups/managed-telemetry"
	f.workspaceID = f.managedID + "/providers/microsoft.operationalinsights/workspaces/logs"
	f.managed = map[string]any{"id": f.managedID, "name": "managed-telemetry", "location": "southcentralus", "managedBy": f.parentID, "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.workspace = map[string]any{"id": f.workspaceID, "type": insightsWorkspaceType, "name": "logs", "location": "southcentralus", "properties": map[string]any{"customerId": "00000000-1111-2222-3333-444444444444", "retentionInDays": float64(30), "provisioningState": "Succeeded"}}
	object(f.parent["properties"])["WorkspaceResourceId"] = f.workspaceID
	f.groups = map[string]map[string]any{f.groupID: f.group, f.managedID: f.managed}
	f.members = map[string]map[string]any{f.workspaceID: f.workspace}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "GET" || !strings.EqualFold(req.URL.Host, "management.azure.com") || !strings.HasPrefix(strings.ToLower(req.URL.Path), "/subscriptions/"+testSubscription+"/") {
			t.Fatal("workspace discovery crossed its read-only subscription boundary", req.Method, req.URL)
		}
		f.requests = append(f.requests, req.URL.String())
		if f.response != nil {
			if response, ok := f.response(req); ok {
				return response, true
			}
		}
		path := strings.ToLower(req.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
			f.groupLists++
			if req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("wrong resource-group list version")
			}
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.groups)) {
				rows = append(rows, f.groups[id])
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if group := f.groups[path]; group != nil {
			if req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("wrong resource-group read version")
			}
			return jsonResponse(200, group, nil), true
		}
		if path == f.managedID+"/resources" {
			f.lists++
			if req.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("wrong native group member list version")
			}
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.members)) {
				rows = append(rows, f.members[id])
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if path == f.workspaceID {
			kind, _ := findType(insightsWorkspaceType)
			if req.URL.Query().Get("api-version") != kind.Version {
				t.Fatal("workspace native read version changed", req.URL)
			}
			return jsonResponse(200, f.workspace, nil), true
		}
		return nil, false
	}
	return f
}

func workspaceInventory(t *testing.T, f *insightsWorkspaceFixture) contracts.InventoryItem {
	t.Helper()
	page, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType))
	if err != nil || len(page.Items) != 1 || !page.Complete {
		t.Fatal("managed workspace inventory failed", page, err)
	}
	return page.Items[0]
}

func TestApplicationInsightsManagedWorkspaceInventory(t *testing.T) {
	for _, mode := range []string{"managed", "shared", "other-owner", "foreign-shared", "detached", "classic-detached", "empty-classic", "unknown-member", "native-id-case", "paged"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsWorkspaceFixture(t)
			wantManaged, wantDetached := true, 0
			switch mode {
			case "shared":
				delete(f.managed, "managedBy") // Managed-looking names confer no ownership.
				wantManaged = false
			case "other-owner":
				f.managed["managedBy"] = resourceID(applicationInsightsType, "another")
				wantManaged = false
			case "foreign-shared":
				object(f.parent["properties"])["WorkspaceResourceId"] = strings.Replace(f.workspaceID, testSubscription, testTenant, 1)
				delete(f.groups, f.managedID)
				wantManaged = false
			case "detached", "classic-detached":
				if mode == "classic-detached" {
					delete(object(f.parent["properties"]), "WorkspaceResourceId")
					object(f.parent["properties"])["IngestionMode"] = "ApplicationInsights"
				} else {
					object(f.parent["properties"])["WorkspaceResourceId"] = f.groupID + "/providers/microsoft.operationalinsights/workspaces/shared"
				}
				wantManaged, wantDetached = false, 1
			case "empty-classic":
				delete(object(f.parent["properties"]), "WorkspaceResourceId")
				object(f.parent["properties"])["IngestionMode"] = "ApplicationInsights"
				delete(f.groups, f.managedID)
				wantManaged = false
			case "unknown-member":
				id := f.managedID + "/providers/contoso.telemetry/pipelines/custom"
				f.members[id] = map[string]any{"id": id, "type": "Contoso.Telemetry/pipelines", "properties": map[string]any{"credential": "PRIVATE_PIPELINE"}}
			case "native-id-case":
				f.managed["id"], f.managed["managedBy"] = strings.ToUpper(f.managedID), strings.ToUpper(f.parentID)
				f.workspace["id"], f.workspace["type"] = strings.ToUpper(f.workspaceID), strings.ToUpper(insightsWorkspaceType)
			case "paged":
				f.response = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
						if req.URL.Query().Get("$skiptoken") == "second" {
							return jsonResponse(200, map[string]any{"value": []any{f.managed}}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": []any{f.group}, "nextLink": apiURL(path, resourcesVersion) + "&%24skiptoken=second"}, nil), true
					}
					if path == f.managedID+"/resources" {
						if req.URL.Query().Get("skiptoken") == "second" {
							return jsonResponse(200, map[string]any{"value": []any{f.workspace}}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, resourcesVersion) + "&skiptoken=second"}, nil), true
					}
					return nil, false
				}
			}
			item := workspaceInventory(t, f)
			state := object(item.Normalized["_insights_workspace"])
			if (text(state["managed_group"]) != "") != wantManaged || len(object(state["detached_groups"])) != wantDetached || text(item.Normalized["_insights_workspace_configuration"]) == "" {
				t.Fatal("workspace ownership misclassified", state)
			}
			wantMembers := 0
			if wantManaged {
				wantMembers = len(f.members)
			}
			if len(object(state["members"])) != wantMembers {
				t.Fatal("managed membership lost", state)
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "PRIVATE_PIPELINE") || strings.Contains(string(encoded), "bc095013") {
				t.Fatal("workspace/private component configuration leaked")
			}
			if !wantManaged && f.lists != 0 {
				t.Fatal("independent workspace acquired managed-group inventory")
			}
		})
	}
}

func TestApplicationInsightsWorkspaceReferenceBoundaries(t *testing.T) {
	for _, mode := range []string{"missing", "null", "empty", "wrong-type", "workspace-child", "url", "whitespace", "alias", "array"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsWorkspaceFixture(t)
			properties := object(f.parent["properties"])
			switch mode {
			case "missing":
				delete(properties, "WorkspaceResourceId")
			case "null":
				properties["WorkspaceResourceId"] = nil
			case "empty":
				properties["WorkspaceResourceId"] = ""
			case "wrong-type":
				properties["WorkspaceResourceId"] = f.parentID
			case "workspace-child":
				properties["WorkspaceResourceId"] = f.workspaceID + "/tables/Telemetry"
			case "url":
				properties["WorkspaceResourceId"] = "https://management.azure.com" + f.workspaceID
			case "whitespace":
				properties["WorkspaceResourceId"] = " " + f.workspaceID
			case "alias":
				properties["workspaceResourceId"] = f.workspaceID
			case "array":
				properties["WorkspaceResourceId"] = []any{f.workspaceID}
			}
			if _, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType)); err == nil {
				t.Fatal("invalid native workspace reference accepted")
			}
			if f.groupLists != 0 || f.lists != 0 {
				t.Fatal("invalid reference reached dependent discovery")
			}
		})
	}
}

func TestApplicationInsightsWorkspaceIncompleteIndexes(t *testing.T) {
	for _, index := range []string{"groups", "members"} {
		for _, mode := range []string{"forbidden", "missing", "partial", "async", "error-code", "filtered", "wrong-version", "foreign-page", "cycle", "duplicate", "foreign-member", "wrong-kind", "owner-alias", "owner-invalid"} {
			t.Run(index+"/"+mode, func(t *testing.T) {
				f := newInsightsWorkspaceFixture(t)
				f.response = func(req *http.Request) (*http.Response, bool) {
					path := "/subscriptions/" + testSubscription + "/resourcegroups"
					entry := f.managed
					if index == "members" {
						path, entry = f.managedID+"/resources", f.workspace
					}
					if strings.ToLower(req.URL.Path) != path {
						return nil, false
					}
					body, headers, status := map[string]any{"value": []any{entry}}, http.Header{}, 200
					if index == "groups" {
						body["value"] = []any{f.group, entry}
					}
					switch mode {
					case "forbidden":
						status = 403
					case "missing":
						status = 404
					case "partial":
						status = 206
					case "async":
						headers["Azure-Asyncoperation"] = []string{apiURL(path+"/operation", resourcesVersion)}
					case "error-code":
						body["code"] = "PartialIndex"
					case "filtered":
						body["nextLink"] = apiURL(path, resourcesVersion) + "&$filter=name%20eq%20%27logs%27"
					case "wrong-version":
						body["nextLink"] = apiURL(path, "2020-01-01") + "&skiptoken=next"
					case "foreign-page":
						body["nextLink"] = apiURL(strings.Replace(path, testSubscription, testTenant, 1), resourcesVersion)
					case "cycle":
						body["nextLink"] = apiURL(path, resourcesVersion) + "&skiptoken=next"
					case "duplicate":
						body["value"] = append(array(body["value"]), maps.Clone(entry))
					case "foreign-member", "wrong-kind", "owner-alias", "owner-invalid":
						changed := maps.Clone(entry)
						if mode == "foreign-member" {
							changed["id"] = strings.Replace(text(entry["id"]), testSubscription, testTenant, 1)
						} else if mode == "wrong-kind" {
							changed["type"] = applicationInsightsType
						} else if mode == "owner-alias" {
							changed["ManagedBy"] = f.parentID
						} else {
							changed["managedBy"] = []any{f.parentID}
						}
						body["value"] = []any{changed}
						if index == "groups" {
							body["value"] = []any{f.group, changed}
						}
					}
					return jsonResponse(status, body, headers), true
				}
				if _, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType)); err == nil {
					t.Fatal("incomplete workspace authority accepted")
				}
			})
		}
	}
}

func TestApplicationInsightsWorkspaceChangingSnapshots(t *testing.T) {
	for _, mode := range []string{"member-added", "member-removed", "workspace-private", "workspace-incarnation", "group-private", "group-owner", "detached-added", "parent-workspace", "parent-missing", "member-read-missing", "member-read-partial", "member-read-async", "group-read-code"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsWorkspaceFixture(t)
			f.before = func(req *http.Request) {
				if f.groupLists != 1 || strings.ToLower(req.URL.Path) != "/subscriptions/"+testSubscription+"/resourcegroups" {
					return
				}
				switch mode {
				case "member-added":
					id := f.managedID + "/providers/contoso.telemetry/pipelines/added"
					f.members[id] = map[string]any{"id": id, "type": "Contoso.Telemetry/pipelines"}
				case "member-removed":
					delete(f.members, f.workspaceID)
				case "workspace-private":
					object(f.workspace["properties"])["credential"] = "NEW_PRIVATE_VALUE"
				case "workspace-incarnation":
					object(f.workspace["properties"])["customerId"] = "new-workspace"
				case "group-private":
					object(f.managed["properties"])["opaqueConfiguration"] = "CHANGED_PRIVATE_GROUP"
				case "group-owner":
					f.managed["managedBy"] = resourceID(applicationInsightsType, "replacement")
				case "detached-added":
					id := f.managedID + "-old"
					f.groups[id] = map[string]any{"id": id, "name": last(id), "managedBy": f.parentID}
				case "parent-workspace":
					object(f.parent["properties"])["WorkspaceResourceId"] = f.groupID + "/providers/microsoft.operationalinsights/workspaces/shared"
				}
			}
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if mode == "parent-missing" && f.lists > 0 && path == f.parentID {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				if mode == "group-read-code" && path == f.managedID {
					group := maps.Clone(f.managed)
					group["code"] = "PartialGroup"
					return jsonResponse(200, group, nil), true
				}
				if path != f.workspaceID {
					return nil, false
				}
				switch mode {
				case "member-read-missing":
					return jsonResponse(404, map[string]any{}, nil), true
				case "member-read-partial":
					return jsonResponse(206, f.workspace, nil), true
				case "member-read-async":
					return jsonResponse(200, f.workspace, http.Header{"Location": {apiURL(f.workspaceID+"/operations/pending", "2020-01-01")}}), true
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType)); err == nil {
				t.Fatal("changing workspace snapshot declared complete")
			}
		})
	}
}

func TestApplicationInsightsWorkspacePrivateLinkInventory(t *testing.T) {
	for _, mode := range []string{"local", "foreign", "denied", "contradictory", "changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsWorkspaceFixture(t)
			scope := monitorPrivateLinkExample(t, "PrivateLinkScopesGet")
			scope["id"], scope["name"], scope["type"] = resourceID(monitorPrivateLinkType, "telemetry"), "telemetry", monitorPrivateLinkType
			scopeID, _, _ := parseID(text(scope["id"]))
			linkID := scopeID + "/scopedresources/logs"
			link := monitorPrivateLinkExample(t, "PrivateLinkScopedResourceGet")
			link["id"], link["name"], link["type"] = linkID, "logs", monitorScopedResourceType
			object(link["properties"])["linkedResourceId"] = f.workspaceID
			if mode == "foreign" {
				linkID = strings.Replace(linkID, testSubscription, testTenant, 1)
			}
			object(f.workspace["properties"])["privateLinkScopedResources"] = []any{map[string]any{"resourceId": linkID, "scopeId": "native-opaque-id"}}
			if mode == "contradictory" {
				object(f.workspace["properties"])["privateLinkScopedResources"] = []any{}
			}
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == "/subscriptions/"+testSubscription+"/providers/microsoft.insights/privatelinkscopes" {
					if mode == "denied" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					rows := []any{}
					if mode != "foreign" {
						rows = append(rows, scope)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				if path == scopeID {
					return jsonResponse(200, scope, nil), true
				}
				if path == scopeID+"/scopedresources" {
					return jsonResponse(200, map[string]any{"value": []any{link}}, nil), true
				}
				if path == linkID {
					if mode == "changed" && f.groupLists > 1 {
						object(link["properties"])["opaqueConfiguration"] = "CHANGED_PRIVATE_LINK"
					}
					return jsonResponse(200, link, nil), true
				}
				return nil, false
			}
			page, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType))
			if mode == "denied" || mode == "contradictory" || mode == "changed" {
				if err == nil {
					t.Fatal("incomplete managed workspace AMPLS snapshot accepted")
				}
				return
			}
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			incoming := object(object(page.Items[0].Normalized["_insights_workspace"])["incoming"])
			configuration, found := incoming[linkID]
			if !found || len(incoming) != 1 || (text(configuration) == "") != (mode == "foreign") {
				t.Fatal("workspace unlink evidence or foreign boundary was lost", incoming)
			}
		})
	}
}

func TestApplicationInsightsWorkspaceInventoryCursor(t *testing.T) {
	for _, mode := range []string{"unchanged", "native-id-case", "unrelated-group", "private-workspace", "owned-member", "detached-group", "owner-removed", "component-settings"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsWorkspaceFixture(t)
			secondID := resourceID(applicationInsightsType, "zz-second")
			secondID, _, _ = parseID(secondID)
			second := maps.Clone(f.parent)
			second["id"], second["name"] = secondID, "zz-second"
			second["properties"] = maps.Clone(object(second["properties"]))
			object(second["properties"])["AppId"] = "second-incarnation"
			object(second["properties"])["WorkspaceResourceId"] = f.groupID + "/providers/microsoft.operationalinsights/workspaces/shared"
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == "/subscriptions/"+testSubscription+"/providers/microsoft.insights/components" {
					return jsonResponse(200, map[string]any{"value": []any{f.parent, second}}, nil), true
				}
				if path == secondID+"/proactivedetectionconfigs" {
					return jsonResponse(200, []any{}, nil), true
				}
				if strings.HasPrefix(path, secondID+"/") {
					if value := f.configurations[strings.TrimPrefix(path, secondID+"/")]; value != nil {
						value = maps.Clone(value)
						if strings.HasSuffix(path, "/quotastatus") {
							value["AppId"] = object(second["properties"])["AppId"]
						}
						if strings.HasSuffix(path, "/pricingplans/current") {
							value["id"] = secondID + "/pricingPlans/current"
						}
						return jsonResponse(200, value, nil), true
					}
				}
				if path == secondID {
					return jsonResponse(200, second, nil), true
				}
				return nil, false
			}
			request := productRequest(f.runtime, applicationInsightsType)
			request.Limit = 1
			page, err := f.runtime.List(t.Context(), request)
			if err != nil || page.Complete || page.NextCursor == "" || len(page.Items) != 1 {
				t.Fatal("component cursor not created", page, err)
			}
			switch mode {
			case "native-id-case":
				f.managed["id"], f.managed["managedBy"] = strings.ToUpper(f.managedID), strings.ToUpper(f.parentID)
				f.workspace["id"], f.workspace["type"] = strings.ToUpper(f.workspaceID), strings.ToUpper(insightsWorkspaceType)
			case "unrelated-group":
				id := f.managedID + "-unrelated"
				f.groups[id] = map[string]any{"id": id, "managedBy": "native-subscription-level-controller"}
			case "private-workspace":
				object(f.workspace["properties"])["opaqueConfiguration"] = "PRIVATE_CHANGED_VALUE"
			case "owned-member":
				id := f.managedID + "/providers/contoso.telemetry/pipelines/new"
				f.members[id] = map[string]any{"id": id, "type": "Contoso.Telemetry/pipelines"}
			case "detached-group":
				id := f.managedID + "-old"
				f.groups[id] = map[string]any{"id": id, "managedBy": f.parentID}
			case "component-settings":
				f.detections["slowpageloadtime"]["customEmails"] = []any{"PRIVATE_CURSOR_CHANGE"}
			case "owner-removed":
				delete(f.managed, "managedBy")
			}
			request.Cursor = page.NextCursor
			page, err = f.runtime.List(t.Context(), request)
			if mode == "unchanged" || mode == "native-id-case" || mode == "unrelated-group" {
				if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].NativeID != secondID {
					t.Fatal("stable component cursor rejected", page, err)
				}
			} else if err == nil {
				t.Fatal("stale workspace cursor accepted")
			}
		})
	}
}

func TestApplicationInsightsManagedWorkspaceProjection(t *testing.T) {
	f := newInsightsWorkspaceFixture(t)
	item := workspaceInventory(t, f)
	ctx := t.Context()
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "workspace.db"), "../../migrations")
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
	projection := inventory.NewService(repository)
	_, shards, err := projection.CreateScan(ctx, inventory.ScanRequest{ConnectionID: connection.ID, RequestedBy: "native-workspace-protocol", Shards: []inventory.ShardRequest{{Provider: asset.ProviderAzure, Source: productInventorySource, ScopeID: scope.ID, ResourceKindID: item.ResourceKind.ID, Authoritative: true}}})
	if err != nil {
		t.Fatal(err)
	}
	page := contracts.InventoryBatch{Items: []contracts.InventoryItem{item}, Complete: true}
	if err := projection.ProjectBatch(ctx, &shards[0], connection, page, inventory.ProjectionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := projection.FinishShard(ctx, &shards[0], asset.ShardSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 1 {
		t.Fatal(values, err)
	}
	c, _ := f.runtime.resolve(ctx, connection.ID)
	state := object(values[0].Normalized["_insights_workspace"])
	if text(state["managed_group"]) != f.managedID || object(state["members"])[f.workspaceID] == nil || text(values[0].Normalized["_insights_workspace_configuration"]) != text(item.Normalized["_insights_workspace_configuration"]) || c.privateConfiguration(state) != text(item.Normalized["_insights_workspace_configuration"]) {
		t.Fatal("native workspace proof did not survive real application projection", values)
	}
}
