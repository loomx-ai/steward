package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestApplicationInsightsComponentPagination(t *testing.T) {
	for _, mode := range []string{"valid", "filter", "duplicate", "cycle", "version", "double-version", "partial", "operation", "error-envelope"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			pages := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if !strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.insights/components") {
					return nil, false
				}
				pages++
				if req.URL.Query().Get("$skiptoken") != "" {
					if mode == "partial" {
						return jsonResponse(206, map[string]any{"value": []any{f.parent}}, nil), true
					}
					headers := http.Header{}
					body := map[string]any{"value": []any{f.parent}}
					if mode == "operation" {
						headers.Set("Azure-AsyncOperation", "https://management.azure.com/operations/pending")
					}
					if mode == "error-envelope" {
						body["code"] = "PartialResult"
					}
					if mode == "cycle" {
						body["nextLink"] = apiURL(req.URL.Path, insightsComponentVersion)
					}
					return jsonResponse(200, body, headers), true
				}
				values := []any{}
				if mode == "duplicate" {
					values = append(values, f.parent)
				}
				next := apiURL(req.URL.Path, insightsComponentVersion) + "&%24skiptoken=page-two"
				switch mode {
				case "filter":
					next += "&%24filter=hidden"
				case "version":
					next = strings.Replace(next, insightsComponentVersion, "2015-05-01", 1)
				case "double-version":
					next += "&api-version=" + insightsComponentVersion
				}
				return jsonResponse(200, map[string]any{"value": values, "nextLink": next}, nil), true
			}
			batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType))
			if mode == "valid" {
				if err != nil || !batch.Complete || len(batch.Items) != 1 || pages != 4 {
					t.Fatal("native nextLink did not complete both indexes", batch, err, pages)
				}
			} else if err == nil {
				t.Fatal("untrustworthy native continuation accepted", mode)
			}
			if mode == "filter" && pages != 1 {
				t.Fatal("filtered continuation reached the service")
			}
		})
	}
}

func TestApplicationInsightsExportNativeStorageReferences(t *testing.T) {
	for _, mode := range []string{"arm", "bare", "storage-name", "unresolved", "foreign", "duplicate", "replacement", "forbidden", "name-disagrees", "subscription-disagrees", "wrong-kind", "container-path"} {
		t.Run(mode, func(t *testing.T) {
			storage := nativeResource(storageType, "exportstore", "westus", map[string]any{"creationTime": "2026-08-01T00:00:00Z"})
			storage["id"] = strings.Replace(text(storage["id"]), "/resourceGroups/test/", "/resourceGroups/exports/", 1)
			id, _, _ := parseID(text(storage["id"]))
			raw := map[string]any{"DestinationAccountId": id, "StorageName": "exportstore", "DestinationStorageSubscriptionId": testSubscription, "ContainerName": "telemetry", "DestinationType": "Blob"}
			if mode != "arm" && mode != "name-disagrees" && mode != "subscription-disagrees" && mode != "wrong-kind" && mode != "container-path" {
				raw["DestinationAccountId"] = "exportstore"
			}
			switch mode {
			case "storage-name":
				delete(raw, "DestinationAccountId")
			case "foreign", "subscription-disagrees":
				raw["DestinationStorageSubscriptionId"] = testTenant
			case "name-disagrees":
				raw["StorageName"] = "otherstore"
			case "wrong-kind":
				raw["DestinationAccountId"] = resourceID(vmType, "exportstore")
			case "container-path":
				raw["ContainerName"] = "../../escape"
			}
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Host != "management.azure.com" || !strings.Contains(strings.ToLower(req.URL.Path), testSubscription) {
					t.Fatal("storage reference escaped the selected connection", req.URL)
				}
				if strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/Microsoft.Storage/storageAccounts") {
					if mode == "forbidden" {
						return jsonResponse(403, nil, nil), nil
					}
					values := []any{storage}
					if mode == "unresolved" {
						values = []any{}
					}
					if mode == "duplicate" {
						other := maps.Clone(storage)
						other["id"] = strings.Replace(id, "/resourcegroups/exports/", "/resourcegroups/other/", 1)
						values = append(values, other)
					}
					return jsonResponse(200, map[string]any{"value": values}, nil), nil
				}
				live := maps.Clone(storage)
				live["id"] = req.URL.Path
				if mode == "replacement" {
					live["properties"] = map[string]any{"creationTime": "2026-09-01T00:00:00Z"}
				}
				return jsonResponse(200, live, nil), nil
			})
			refs := map[string][]string{}
			err := c.insightsExportReferences(t.Context(), raw, refs)
			switch mode {
			case "arm", "bare", "storage-name":
				if err != nil || !slices.Equal(refs[storageType], []string{id}) || !slices.Equal(refs[containerType], []string{id + "/blobservices/default/containers/telemetry"}) {
					t.Fatal("storage or container dependency lost", refs, err)
				}
			case "unresolved", "foreign":
				if err != nil || !slices.Equal(refs[storageType], []string{"exportstore"}) || len(refs[containerType]) != 0 {
					t.Fatal("unresolved name became an invented ARM resource", refs, err)
				}
				if mode == "foreign" && calls != 0 {
					t.Fatal("foreign storage subscription was queried")
				}
			default:
				if err == nil {
					t.Fatal("ambiguous or changed storage reference accepted", mode)
				}
			}
		})
	}
}

func TestApplicationInsightsExportRecordedSubscriptionContradiction(t *testing.T) {
	// The CLI recording sanitizes DestinationAccountId's subscription to zero
	// while retaining another DestinationStorageSubscriptionId. Keep that
	// original inconsistency visible; it cannot establish a storage graph edge.
	payload, err := os.ReadFile("fixtures/applicationinsights/cli-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	checked := 0
	c := directClient(func(req *http.Request) (*http.Response, error) {
		t.Fatal("contradictory storage identity reached HTTP")
		return nil, nil
	})
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["ExportId"] != nil && v["DestinationAccountId"] != nil {
				if err := c.insightsExportReferences(t.Context(), v, map[string][]string{}); err == nil {
					t.Fatal("recording's contradictory storage subscriptions were reconciled silently")
				}
				checked++
			}
			for _, entry := range v {
				walk(entry)
			}
		case []any:
			for _, entry := range v {
				walk(entry)
			}
		}
	}
	for _, record := range raw {
		walk(record)
	}
	if checked != 4 {
		t.Fatal("native export response coverage changed", checked)
	}
}

func TestApplicationInsightsInventoryReferencesReachSharedTargets(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	r := f.runtime
	storage := nativeResource(storageType, "exportstore", "westus", map[string]any{})
	workspace := nativeResource("Microsoft.OperationalInsights/workspaces", "sharedlogs", "westus2", map[string]any{"customerId": "00000000-1111-2222-3333-444444444444"})
	storageID, _, _ := parseID(text(storage["id"]))
	workspaceID, _, _ := parseID(text(workspace["id"]))
	container := map[string]any{"id": storageID + "/blobServices/default/containers/telemetry", "type": containerType, "name": "telemetry", "location": "westus", "properties": map[string]any{}}
	object(f.parent["properties"])["WorkspaceResourceId"] = workspaceID
	for id, raw := range f.children {
		_, _, kind, _, _ := insightsLegacyIdentity(id)
		if kind == insightsExportType {
			raw["DestinationAccountId"], raw["ContainerName"], raw["DestinationType"] = storageID, "telemetry", "Blob"
		}
	}
	values := []asset.Asset{dnsAsset(t, r, storage), dnsAsset(t, r, workspace), dnsAsset(t, r, container)}
	for _, kind := range []string{applicationInsightsType, insightsExportType} {
		page, err := r.List(t.Context(), productRequest(r, kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities})
		}
	}
	dependencies := slices.Clone(values[:3])
	store := batchReferenceGraph{assets: values}
	result, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "insights-exports", r.bundle, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatal("shared references failed graph construction", result, err)
	}
	for _, dependency := range dependencies {
		selected := []asset.AssetID{dependency.ID}
		for _, ref := range result.Relationships {
			if ref.TargetAssetID == dependency.ID && ref.Type == graph.RelationshipUses {
				selected = append(selected, ref.SourceAssetID)
			}
		}
		if len(selected) < 2 {
			t.Fatal("shared workspace, account or container reference lost", dependency.Identity.NativeID)
		}
		planned, err := plan.Solve(plan.Input{Assets: values, Relationships: result.Relationships, ResolvedAssetIDs: selected})
		if err != nil {
			t.Fatal(err)
		}
		if len(planned.Blockers) != 0 || len(planned.Steps) != len(selected) {
			t.Fatal("explicit export and storage selection failed", planned)
		}
		var target plan.CleanupTaskStep
		for _, step := range planned.Steps {
			if step.AssetID == dependency.ID {
				target = step
			}
		}
		for _, step := range planned.Steps {
			if step.AssetID != dependency.ID && !slices.Contains(target.DependsOn, step.ID) {
				t.Fatal("selected storage deletion was not ordered after its exports")
			}
		}
		// Selecting an export alone retains every shared target. Relationship
		// ordering in the core solver applies to explicitly selected resources.
		leaf, err := plan.Solve(plan.Input{Assets: values, Relationships: result.Relationships, ResolvedAssetIDs: selected[1:2]})
		if err != nil || len(leaf.Blockers) != 0 || len(leaf.Steps) != 1 || leaf.Steps[0].AssetID != selected[1] {
			t.Fatal("export cleanup acquired ownership of shared targets", leaf, err)
		}
	}
}

func TestApplicationInsightsCursorConnectionBinding(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	request := productRequest(f.runtime, insightsAnalyticsType)
	request.Limit = 1
	page, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil }))
	if err != nil {
		t.Fatal(err)
	}
	other.transport = f.runtime.transport
	request.ConnectionID, request.Cursor = "another-connection-same-credentials", page.NextCursor
	if _, err := other.List(t.Context(), request); err == nil {
		t.Fatal("cursor transferred to a different connection with the same credentials")
	}
}
