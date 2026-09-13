package azure

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type hybridInventoryFixture struct {
	runtime  *Runtime
	client   *client
	values   map[string]map[string]any
	omitted  map[string]bool
	override func(*http.Request) (*http.Response, bool)
}

func newHybridInventoryFixture(t *testing.T) *hybridInventoryFixture {
	t.Helper()
	f := &hybridInventoryFixture{values: map[string]map[string]any{}, omitted: map[string]bool{}}
	machine := strings.ToLower(resourceID(hybridMachineType, "machine"))
	license := strings.ToLower(resourceID(hybridLicenseType, "license"))
	for _, row := range []struct{ kind, id, file string }{
		{hybridMachineType, machine, "Machines_Get"},
		{hybridExtensionType, machine + "/extensions/extension", "Extension_Get"},
		{hybridCommandType, machine + "/runcommands/command", "RunCommands_Get"},
		{hybridProfileType, machine + "/licenseprofiles/default", "LicenseProfile_Get"},
		{hybridLicenseType, license, "License_Get"},
	} {
		payload, err := os.ReadFile("fixtures/hybridcompute/" + row.file + ".json")
		var example map[string]any
		if err != nil || json.Unmarshal(payload, &example) != nil {
			t.Fatal(err)
		}
		raw := object(object(object(example["responses"])["200"])["body"])
		raw["id"], raw["name"], raw["type"], raw["location"] = row.id, last(row.id), row.kind, "eastus"
		props := object(raw["properties"])
		if row.kind == hybridMachineType {
			props["privateLinkScopeResourceId"] = resourceID("Microsoft.HybridCompute/privateLinkScopes", "scope")
			props["parentClusterResourceId"] = resourceID("Microsoft.AzureStackHCI/clusters", "hci")
		}
		if row.kind == hybridProfileType {
			object(props["esuProfile"])["assignedLicense"] = license
		}
		props["futurePrivateConfiguration"] = "private-arc-configuration"
		f.values[row.id] = raw
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		if armPathProvider(req.URL.Path) != "microsoft.hybridcompute" {
			if res, ok := fleetGraphEmptyIndexes(t, req); ok {
				return res, nil
			}
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" || req.URL.Query().Get("api-version") != hybridComputeVersion {
			return nil, fmt.Errorf("unexpected Arc request %s %s", req.Method, req.URL.Path)
		}
		path := strings.ToLower(req.URL.Path)
		if value := f.values[path]; value != nil {
			return jsonResponse(200, value, http.Header{"X-Ms-Request-Id": {"arc-read"}}), nil
		}
		if _, kind, err := parseID(path); err == nil && hybridComputeKind(kind) != "" {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		valid := path == "/subscriptions/"+testSubscription+"/providers/microsoft.hybridcompute/machines" || path == "/subscriptions/"+testSubscription+"/providers/microsoft.hybridcompute/licenses"
		for id, raw := range f.values {
			if raw["type"] == hybridMachineType && slices.Contains([]string{id + "/extensions", id + "/runcommands", id + "/licenseprofiles"}, path) {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("unexpected Arc collection %s", path)
		}
		rows := []any{}
		for _, id := range slices.Sorted(maps.Keys(f.values)) {
			raw := f.values[id]
			kind := text(raw["type"])
			collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
			if parent := hybridComputeParent(id, kind); parent != "" {
				collection = parent + "/" + strings.ToLower(last(kind))
			}
			if path == collection && !f.omitted[id] {
				rows = append(rows, raw)
			}
		}
		return jsonResponse(200, map[string]any{"value": rows}, nil), nil
	})
	var err error
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *hybridInventoryFixture) request(kind string) contracts.InventoryRequest {
	k := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: hybridComputeSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func TestHybridComputeNativeInventoryAndWorkers(t *testing.T) {
	f := newHybridInventoryFixture(t)
	kinds := []string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType}
	for _, kind := range kinds {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(kind, batch, err)
		}
		item := batch.Items[0]
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "private-arc-configuration") || strings.Contains(string(encoded), "commandToExecute") || strings.Contains(string(encoded), "runAsPassword") || item.Actionable == nil || *item.Actionable != hybridComputeChild(kind) {
			t.Fatal("private configuration or wrong cleanup capability exposed", kind)
		}
		if kind == hybridMachineType && item.Normalized["status"] != nil {
			t.Fatal("null connection status became a state")
		}
		value := asset.Asset{ID: "test", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized}
		if _, err := f.client.hybridComputeRecordedReferences(value); err != nil {
			t.Fatal(err)
		}
		value.Normalized = maps.Clone(value.Normalized)
		value.Normalized["_hybrid_compute_reference_binding"] = "forged"
		if _, err := f.client.hybridComputeRecordedReferences(value); err == nil {
			t.Fatal("forged reference proof accepted")
		}
	}
	repository, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, kinds, false, true)
	if len(values) != 5 {
		t.Fatal("native worker inventory", len(values))
	}
	relations, err := repository.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil || len(relations) != 4 {
		t.Fatal("native Arc references", len(relations), err)
	}
	for _, relation := range relations {
		if relation.Type != graph.RelationshipUses {
			t.Fatal("Arc reference became ownership", relation)
		}
	}
	for _, value := range values {
		if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); (err == nil) != hybridComputeChild(value.Identity.NativeType) {
			t.Fatal("wrong Arc cleanup dispatch", err)
		}
	}
	for id := range f.values {
		f.omitted[id] = true
	}
	values = azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, kinds, false, true)
	if len(values) != 5 {
		t.Fatal("known index omissions closed assets", len(values))
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(req.URL.Path), "/runcommands/command") {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return nil, false
	}
	values = azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, kinds, true, true)
	if len(values) != 5 {
		t.Fatal("failed known read closed an asset")
	}
	f.override = nil
	delete(f.values, strings.ToLower(resourceID(hybridMachineType, "machine"))+"/runcommands/command")
	values = azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, kinds, false, true)
	if len(values) != 4 {
		t.Fatal("own absence did not reconcile", len(values))
	}
}

func TestHybridComputeInventoryFailureAndContinuation(t *testing.T) {
	for _, mode := range []string{"permission", "partial", "filtered-page", "foreign-page", "duplicate", "wrong-child", "malformed-state"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridInventoryFixture(t)
			machine := strings.ToLower(resourceID(hybridMachineType, "machine"))
			child := machine + "/extensions/extension"
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.ToLower(req.URL.Path) == child && mode == "malformed-state" {
					raw := maps.Clone(f.values[child])
					props := maps.Clone(object(raw["properties"]))
					props["status"] = map[string]any{"secret": "private"}
					raw["properties"] = props
					return jsonResponse(200, raw, nil), true
				}
				if strings.ToLower(req.URL.Path) != machine+"/extensions" {
					return nil, false
				}
				switch mode {
				case "permission":
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				case "partial":
					return jsonResponse(206, map[string]any{"value": []any{}}, nil), true
				case "filtered-page":
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": req.URL.String() + "&$filter=name"}, nil), true
				case "foreign-page":
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://example.com/next"}, nil), true
				case "duplicate":
					return jsonResponse(200, map[string]any{"value": []any{f.values[child], f.values[child]}}, nil), true
				case "wrong-child":
					raw := maps.Clone(f.values[child])
					raw["id"] = strings.Replace(child, "/machines/machine/", "/machines/other/", 1)
					return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), f.request(hybridExtensionType)); err == nil {
				t.Fatal("uncertain inventory accepted")
			}
		})
	}
	f := newHybridInventoryFixture(t)
	id := strings.ToLower(resourceID(hybridLicenseType, "license"))
	other := maps.Clone(f.values[id])
	other["id"], other["name"] = id+"2", "license2"
	f.values[id+"2"] = other
	request := f.request(hybridLicenseType)
	request.Limit = 1
	first, err := f.runtime.List(t.Context(), request)
	if err != nil || first.Complete || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	request.Cursor = first.NextCursor
	last, err := f.runtime.List(t.Context(), request)
	if err != nil || !last.Complete || len(last.Items) != 1 {
		t.Fatal(last, err)
	}
	object(f.values[id]["properties"])["futurePrivateConfiguration"] = "changed-private-value"
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("private change did not invalidate continuation")
	}
}

func TestHybridComputeParentPagesAndRequestBoundaries(t *testing.T) {
	f := newHybridInventoryFixture(t)
	id := strings.ToLower(resourceID(hybridMachineType, "machine"))
	second := maps.Clone(f.values[id])
	second["id"], second["name"] = id+"2", "machine2"
	f.values[id+"2"] = second
	pages := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) != f.client.root()+"/providers/microsoft.hybridcompute/machines" {
			return nil, false
		}
		pages++
		if req.URL.Query().Get("$skiptoken") == "second" {
			return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
		}
		return jsonResponse(200, map[string]any{"value": []any{f.values[id]}, "nextLink": req.URL.String() + "&$skiptoken=second"}, nil), true
	}
	batch, err := f.runtime.List(t.Context(), f.request(hybridExtensionType))
	if err != nil || len(batch.Items) != 1 || pages != 4 {
		t.Fatal("native parent continuation", pages, err)
	}
	for _, mode := range []string{"source", "subscription", "duplicate-known", "foreign-known", "unknown-metadata", "known-parent-absent", "wrong-license-type"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridInventoryFixture(t)
			request := f.request(hybridProfileType)
			child := id + "/licenseprofiles/default"
			switch mode {
			case "source":
				request.Source = productInventorySource
			case "subscription":
				request.Scope.NativeID = testTenant
			case "duplicate-known":
				request.KnownNativeIDs = []string{child, child}
			case "foreign-known":
				request.KnownNativeIDs = []string{strings.Replace(child, testSubscription, testTenant, 1)}
			case "unknown-metadata":
				request.KnownNativeMetadata = map[string]map[string]any{child: {}}
			case "known-parent-absent":
				request.KnownNativeIDs = []string{child}
				delete(f.values, id)
			case "wrong-license-type":
				object(object(f.values[child]["properties"])["esuProfile"])["assignedLicense"] = id
			}
			if _, err := f.runtime.List(t.Context(), request); err == nil {
				t.Fatal("invalid request/reference accepted")
			}
		})
	}
}
