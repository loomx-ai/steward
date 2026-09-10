package azure

import (
	"encoding/base64"
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
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var insightsInventoryTestKinds = []string{applicationInsightsType, insightsAnalyticsType, insightsMyAnalyticsType, insightsExportType, insightsFavoriteType, insightsWorkItemType}

type insightsInventoryFixture struct {
	runtime           *Runtime
	parent, group     map[string]any
	parentID, groupID string
	configurations    map[string]map[string]any
	detections        map[string]map[string]any
	children          map[string]map[string]any
	locks             []any
	componentLists    int
	childLists        int
	deletes           []string
	before            func(*http.Request)
	override          func(*http.Request) (*http.Response, bool)
}

func newInsightsInventoryFixture(t *testing.T) *insightsInventoryFixture {
	t.Helper()
	f := &insightsInventoryFixture{parent: nativeResource(applicationInsightsType, "App", "eastus", map[string]any{"AppId": "app-generation", "CreationDate": "2026-09-01T00:00:00Z", "InstrumentationKey": "PRIVATE_KEY"}), children: map[string]map[string]any{}, locks: []any{}}
	f.parentID, _, _ = parseID(text(f.parent["id"]))
	f.groupID = strings.Join(strings.Split(f.parentID, "/")[:5], "/")
	f.group = map[string]any{"id": f.groupID, "name": "test", "type": groupType, "location": "eastus"}
	for _, kind := range insightsInventoryTestKinds[1:] {
		row := insightsLegacyKind(kind)
		for _, selector := range []string{"OpaqueID", "opaqueid"} {
			raw := map[string]any{row.field: selector, "Name": "display name", "Content": "PRIVATE_QUERY", "Config": "PRIVATE_FAVORITE", "ConfigProperties": "PRIVATE_WORKITEM"}
			if kind == insightsFavoriteType {
				raw["FavoriteType"], raw["SourceType"] = "shared", "other"
			}
			id, err := insightsLegacyURL(f.parentID, kind, selector)
			if err != nil {
				t.Fatal(err)
			}
			f.children[id] = raw
		}
	}
	f.configurations = map[string]map[string]any{}
	for path, file := range map[string]string{
		"currentbillingfeatures":      "stable/2015-05-01/examples/CurrentBillingFeaturesGet.json",
		"pricingplans/current":        "stable/2017-10-01/examples/CurrentPricingPlanGet.json",
		"featurecapabilities":         "stable/2015-05-01/examples/FeatureCapabilitiesGet.json",
		"getavailablebillingfeatures": "stable/2015-05-01/examples/AvailableBillingFeaturesGet.json",
		"quotastatus":                 "stable/2015-05-01/examples/QuotaStatusGet.json",
	} {
		f.configurations[path] = insightsScopedExample(t, file, f.parentID)
	}
	// A one-rule collection composes the original native GET body. The original
	// LIST example has duplicate names and is tested separately without repair.
	detection := insightsScopedExample(t, "stable/2015-05-01/examples/ProactiveDetectionConfigurationGet.json", f.parentID)
	f.detections = map[string]map[string]any{text(detection["name"]): detection}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if f.before != nil {
			f.before(req)
		}
		if f.override != nil {
			if response, handled := f.override(req); handled {
				return response, nil
			}
		}
		path := strings.ToLower(req.URL.Path)
		root := "/subscriptions/" + testSubscription
		if path == root+"/providers/microsoft.insights/components" {
			f.componentLists++
			if req.Method != "GET" || req.URL.Query().Get("api-version") != insightsComponentVersion {
				t.Fatal("wrong native component list", req.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{f.parent}}, http.Header{"X-Ms-Request-Id": {"insights-list"}}), nil
		}
		if value := f.configurations[strings.TrimPrefix(path, f.parentID+"/")]; value != nil {
			version := insightsLegacyVersion
			if strings.HasSuffix(path, "/pricingplans/current") {
				version = "2017-10-01"
			}
			if req.Method != "GET" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != version {
				t.Fatal("configuration native contract changed", req.Method, req.URL)
			}
			if strings.HasSuffix(path, "/quotastatus") {
				value = maps.Clone(value)
				value["AppId"] = object(f.parent["properties"])["AppId"] // Bind composed component incarnation.
			}
			return jsonResponse(200, value, nil), nil
		}
		if path == f.parentID+"/proactivedetectionconfigs" || strings.HasPrefix(path, f.parentID+"/proactivedetectionconfigs/") {
			if req.Method != "GET" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != insightsLegacyVersion {
				t.Fatal("detection configuration native contract changed", req.Method, req.URL)
			}
			if path == f.parentID+"/proactivedetectionconfigs" {
				rows := []any{}
				for _, name := range slices.Sorted(maps.Keys(f.detections)) {
					rows = append(rows, f.detections[name])
				}
				return jsonResponse(200, rows, nil), nil
			}
			value := f.detections[last(req.URL.Path)]
			if value == nil {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, value, nil), nil
		}
		if path == f.parentID {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != insightsComponentVersion {
				t.Fatal("component unexpectedly mutated or changed version")
			}
			return jsonResponse(200, f.parent, nil), nil
		}
		if path == root+"/resourcegroups" {
			return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), nil
		}
		if path == f.groupID {
			return jsonResponse(200, f.group, nil), nil
		}
		if path == root+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if path == root+"/providers/microsoft.insights/privatelinkscopes" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if path == f.parentID+"/apikeys" && req.Method == "GET" && req.URL.Query().Get("api-version") == insightsLegacyVersion {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if path == f.parentID+"/linkedstorageaccounts/serviceprofiler" && req.Method == "GET" && req.URL.Query().Get("api-version") == insightsStorageVersion {
			return jsonResponse(404, map[string]any{}, nil), nil
		}
		for _, kind := range insightsInventoryTestKinds[1:] {
			row := insightsLegacyKind(kind)
			if path != f.parentID+"/"+strings.ToLower(row.collection) {
				continue
			}
			f.childLists++
			if req.Method != "GET" || req.URL.Query().Get("api-version") != insightsLegacyVersion {
				t.Fatal("wrong native child list")
			}
			var values []any
			if kind != insightsFavoriteType || req.URL.Query().Get("favoriteType") == "shared" && req.URL.Query().Get("sourceType") == "" {
				ids := slices.Sorted(maps.Keys(f.children))
				for _, id := range ids {
					_, _, typ, _, _ := insightsLegacyIdentity(id)
					if typ == kind {
						values = append(values, f.children[id])
					}
				}
			}
			if values == nil {
				values = []any{}
			}
			if kind == insightsWorkItemType {
				return jsonResponse(200, map[string]any{"value": values}, nil), nil
			}
			return jsonResponse(200, values, nil), nil
		}
		u := *req.URL
		query := u.Query()
		query.Del("api-version")
		u.RawQuery = query.Encode()
		id, _, kind, _, err := insightsLegacyIdentity(u.String())
		if err != nil || req.URL.Query().Get("api-version") != insightsLegacyVersion {
			t.Fatal("unexpected native endpoint", req.Method, req.URL)
		}
		raw, exists := f.children[id]
		if req.Method == "GET" {
			if !exists {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, raw, nil), nil
		}
		if req.Method != "DELETE" {
			t.Fatal("unexpected write", req.Method)
		}
		f.deletes = append(f.deletes, id)
		delete(f.children, id)
		if kind == insightsExportType {
			return jsonResponse(200, raw, nil), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	})
	return f
}

func TestApplicationInsightsNativeInventory(t *testing.T) {
	for _, kind := range insightsInventoryTestKinds {
		t.Run(kind, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			request := productRequest(f.runtime, kind)
			page, err := f.runtime.List(t.Context(), request)
			want := 2
			if kind == applicationInsightsType {
				want = 1
			}
			if err != nil || !page.Complete || len(page.Items) != want || f.componentLists != 2 || page.RequestID != "insights-list" {
				t.Fatal("native inventory incomplete", page, err, f.componentLists)
			}
			for _, item := range page.Items {
				if item.NativeType != kind || item.Location != "eastus" || item.Scope.Kind != asset.ScopeRegion || item.Normalized["_inventory_source"] != productInventorySource || item.Actionable == nil || !*item.Actionable {
					t.Fatal("lost native kind, location or reviewed action boundary", item)
				}
				if kind != applicationInsightsType && (!strings.HasPrefix(item.NativeID, "https://management.azure.com/") || item.Normalized["_insights_legacy_private_configuration"] == "" || !slices.Contains(item.NetworkReferences, f.parentID)) {
					t.Fatal("invented ARM identity or lost parent binding")
				}
			}
			encoded, _ := json.Marshal(page)
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("private component or flat child content leaked")
			}
			request.Source = ""
			if page, err := f.runtime.List(t.Context(), request); err != nil || len(page.Items) != want {
				t.Fatal("typed discovery used generic ARM paths", err)
			}
			request.Source = inventorySource
			if page, err := f.runtime.List(t.Context(), request); err != nil || !page.Complete || len(page.Items) != 0 {
				t.Fatal("broad inventory overwrote native items", err)
			}
		})
	}
}

func TestApplicationInsightsInventoryFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"component-list-403", "component-list-404", "component-list-206", "component-list-duplicate", "component-list-foreign", "group-unindexed", "group-owner-disagrees", "region", "global", "annotation", "protected-parent", "protected-group", "managed-group", "lock", "export-unresolved", "export-volatile"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			request := productRequest(f.runtime, insightsAnalyticsType)
			if strings.HasPrefix(mode, "component-list-") || strings.HasPrefix(mode, "group-") {
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
						if mode == "group-unindexed" {
							return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
						}
						if mode == "group-owner-disagrees" {
							group := maps.Clone(f.group)
							group["managedBy"] = f.parentID
							return jsonResponse(200, map[string]any{"value": []any{group}}, nil), true
						}
					}
					if !strings.HasSuffix(path, "/providers/microsoft.insights/components") {
						return nil, false
					}
					switch mode {
					case "component-list-403":
						return jsonResponse(403, nil, nil), true
					case "component-list-404":
						return jsonResponse(404, nil, nil), true
					case "component-list-206":
						return jsonResponse(206, map[string]any{"value": []any{f.parent}}, nil), true
					case "component-list-duplicate":
						return jsonResponse(200, map[string]any{"value": []any{f.parent, f.parent}}, nil), true
					case "component-list-foreign":
						parent := maps.Clone(f.parent)
						parent["id"] = strings.Replace(f.parentID, testSubscription, testTenant, 1)
						return jsonResponse(200, map[string]any{"value": []any{parent}}, nil), true
					}
					return nil, false
				}
			}
			switch mode {
			case "region":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
			case "global":
				request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}
			case "annotation":
				kind := f.runtime.resourceKind(insightsAnnotationType)
				request.ResourceKind = &kind
			case "protected-parent":
				f.parent["tags"] = map[string]any{"steward:protected": "true"}
			case "protected-group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "managed-group":
				f.group["managedBy"] = f.parentID
			case "lock":
				f.locks = []any{map[string]any{"id": f.parentID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "export-unresolved", "export-volatile":
				request = productRequest(f.runtime, insightsExportType)
				if mode == "export-unresolved" {
					for _, raw := range f.children {
						raw["DestinationAccountId"] = "bare-name"
					}
				} else {
					f.before = func(req *http.Request) {
						if req.Method == "GET" {
							for _, raw := range f.children {
								raw["LastSuccessTime"] = time.Now().UTC().Format(time.RFC3339Nano)
							}
						}
					}
				}
			}
			page, err := f.runtime.List(t.Context(), request)
			switch mode {
			case "region", "global":
				if err != nil || !page.Complete || len(page.Items) != 0 || f.childLists != 0 {
					t.Fatal("out-of-scope child inventory queried or returned", page, err)
				}
			case "protected-parent", "protected-group", "managed-group", "lock":
				if err != nil || len(page.Items) != 2 || text(page.Items[0].Normalized["cleanup_protection_reason"]) == "" {
					t.Fatal("inherited protection missing", page, err)
				}
				item := page.Items[0]
				value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, Location: item.Location, Normalized: item.Normalized}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				if check, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil || check.Allowed {
					t.Fatal("protected child allowed", check, err)
				}
			case "export-volatile":
				if err != nil || !page.Complete || len(page.Items) != 2 {
					t.Fatal("operational export timestamp invalidated configuration proof", err)
				}
			default:
				if err == nil {
					t.Fatal("incomplete inventory declared authoritative", mode)
				}
			}
			if len(f.deletes) != 0 {
				t.Fatal("inventory or preflight mutated resources")
			}
		})
	}
}

func TestApplicationInsightsInventoryCursorAndDrift(t *testing.T) {
	for _, change := range []string{"none", "private-content", "new-child", "removed-child", "component", "group", "lock", "scope", "kind", "network", "malformed"} {
		t.Run(change, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			request := productRequest(f.runtime, insightsAnalyticsType)
			request.Limit = 1
			page, err := f.runtime.List(t.Context(), request)
			if err != nil || page.Complete || len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatal("first page failed", err)
			}
			first := page.Items[0]
			request.Cursor = page.NextCursor
			switch change {
			case "private-content":
				f.children[first.NativeID]["Content"] = "CHANGED_PRIVATE_QUERY"
			case "new-child":
				id, _ := insightsLegacyURL(f.parentID, insightsAnalyticsType, "EarlierID")
				f.children[id] = map[string]any{"Id": "EarlierID"}
			case "removed-child":
				delete(f.children, first.NativeID)
			case "component":
				object(f.parent["properties"])["AppId"] = "replacement"
			case "group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.parentID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "scope":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			case "kind":
				kind := f.runtime.resourceKind(insightsMyAnalyticsType)
				request.ResourceKind = &kind
			case "network":
				request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: resourceID(vnetType, "network")}
			case "malformed":
				request.Cursor = base64.RawURLEncoding.EncodeToString([]byte(`{"fingerprint":"fabricated","target":-1}`))
			}
			page, err = f.runtime.List(t.Context(), request)
			if change == "none" {
				if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].NativeID == first.NativeID {
					t.Fatal("case-distinct cursor lost a resource", page, err)
				}
			} else if err == nil {
				t.Fatal("changed inventory cursor accepted", change)
			}
		})
	}
	for _, change := range []string{"membership", "private", "parent", "group"} {
		t.Run("during-scan/"+change, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			f.before = func(req *http.Request) {
				if f.componentLists != 1 || !strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.insights/components") {
					return
				}
				id, _ := insightsLegacyURL(f.parentID, insightsAnalyticsType, "OpaqueID")
				switch change {
				case "membership":
					delete(f.children, id)
				case "private":
					f.children[id]["Content"] = "changed"
				case "parent":
					object(f.parent["properties"])["AppId"] = "changed"
				case "group":
					f.group["managedBy"] = f.parentID
				}
			}
			if _, err := f.runtime.List(t.Context(), productRequest(f.runtime, insightsAnalyticsType)); err == nil {
				t.Fatal("changing snapshot declared complete")
			}
		})
	}
}

func TestApplicationInsightsInventoryProjectionGraphAndAction(t *testing.T) {
	testInsightsInventoryPipeline(t, newInsightsInventoryFixture(t), insightsInventoryTestKinds, nil)
}

func testInsightsInventoryPipeline(t *testing.T, f *insightsInventoryFixture, kinds []string, retained []asset.Asset) {
	t.Helper()
	childCount := len(f.children)
	r := f.runtime
	ctx := t.Context()
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "insights.db"), "../../migrations")
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
	retainedIDs := map[asset.AssetID]bool{}
	for _, value := range retained {
		value.ScopeID = scope.ID
		if err := repository.PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
		retainedIDs[value.ID] = true
	}
	projection := inventory.NewService(repository)
	workspaceConfiguration := ""
	for _, kind := range kinds {
		request := productRequest(r, kind)
		request.Limit = 1
		_, shards, err := projection.CreateScan(ctx, inventory.ScanRequest{ConnectionID: connection.ID, RequestedBy: "native-integration-test", Shards: []inventory.ShardRequest{{Provider: asset.ProviderAzure, Source: productInventorySource, ScopeID: scope.ID, ResourceKindID: request.ResourceKind.ID, Authoritative: true}}})
		if err != nil {
			t.Fatal(err)
		}
		for {
			page, err := r.List(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := projection.ProjectBatch(ctx, &shards[0], connection, page, inventory.ProjectionOptions{}); err != nil {
				t.Fatal("native inventory failed application projection", err)
			}
			for _, item := range page.Items {
				if item.NativeType == applicationInsightsType {
					workspaceConfiguration = text(item.Normalized["_insights_workspace_configuration"])
				}
			}
			if page.Complete {
				break
			}
			request.Cursor = page.NextCursor
		}
		if err := projection.FinishShard(ctx, &shards[0], asset.ShardSucceeded, ""); err != nil {
			t.Fatal(err)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != childCount+1+len(retained) {
		t.Fatal("native identities changed in persistence", len(values), err)
	}
	var parent asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == applicationInsightsType {
			parent = value
		}
	}
	if workspaceConfiguration == "" || text(parent.Normalized["_insights_workspace_configuration"]) != workspaceConfiguration || object(parent.Normalized["_insights_workspace"]) == nil {
		t.Fatal("workspace ownership snapshot was lost in application persistence")
	}
	lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := governance.NewService(repository, repository).RebuildGraph(ctx, scope.ID, connection.ID, "insights-native", r.bundle, []governance.Contributor{lifecycle, NewResourceAttachments()})
	if err != nil || len(result.Unresolved) != 0 || len(result.Relationships) != 2*childCount+len(retained) || len(result.Bindings) != childCount || parent.ID == "" {
		t.Fatal("native parent relationships lost", result, err)
	}
	for _, value := range values {
		if retainedIDs[value.ID] {
			selected := []asset.AssetID{value.ID}
			for _, ref := range result.Relationships {
				if ref.TargetAssetID == value.ID && ref.Type == graph.RelationshipUses {
					selected = append(selected, ref.SourceAssetID)
				}
			}
			planned, err := plan.Solve(plan.Input{Assets: values, Relationships: result.Relationships, LifecycleBindings: result.Bindings, ResolvedAssetIDs: selected})
			if err != nil || len(selected) < 2 || len(planned.Blockers) != 0 || len(planned.Steps) != len(selected) || planned.Steps[len(planned.Steps)-1].AssetID != value.ID {
				t.Fatal("shared target deletion order was lost", planned, err)
			}
			continue
		}
		if value.ID == parent.ID {
			if !value.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("registered component capability missing")
			}
			if _, err := r.ResolveAction(ctx, connection.ID, value); err != nil {
				t.Fatal("registered component cleanup failed resolution", err)
			}
			continue
		}
		if !slices.ContainsFunc(result.Relationships, func(ref graph.Relationship) bool {
			return ref.SourceAssetID == value.ID && ref.TargetAssetID == parent.ID && ref.Type == graph.RelationshipDependsOn
		}) {
			t.Fatal("native child did not reach its own graph edge", value.Identity.NativeID)
		}
		input := plan.Input{Assets: values, Relationships: result.Relationships, LifecycleBindings: result.Bindings, ResolvedAssetIDs: []asset.AssetID{value.ID}}
		planned, err := plan.Solve(input)
		if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != value.ID {
			t.Fatal("leaf selection affected its sibling or parent", planned, err)
		}
		request := servicePlanRequest(planned, values, value)
		driver, err := r.ResolveAction(ctx, connection.ID, value)
		if err != nil {
			t.Fatal("registered leaf driver unavailable", err)
		}
		check, err := driver.Preflight(ctx, request)
		if err != nil || !check.Allowed {
			t.Fatal("projected private proof failed preflight", check, err)
		}
		deleted, err := driver.Execute(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		driver, err = r.ResolveAction(ctx, connection.ID, value)
		if err != nil {
			t.Fatal(err)
		}
		if wait, err := driver.Wait(ctx, request, deleted); err != nil || !wait.Done {
			t.Fatal("native deletion failed after driver restart", wait, err)
		}
	}
	if len(f.deletes) != childCount || len(f.children) != 0 {
		t.Fatal("native cleanup lost distinct leaf operations")
	}
}
