package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type azureLocalFixture struct {
	runtime     *Runtime
	client      *client
	ids         map[string]string
	values      map[string]map[string]any
	omitted     map[string]bool
	collections map[string]string
	override    func(*http.Request) (*http.Response, bool)
	reads       map[string]int
}

func newAzureLocalFixture(t *testing.T) *azureLocalFixture {
	t.Helper()
	f := &azureLocalFixture{ids: map[string]string{}, values: map[string]map[string]any{}, omitted: map[string]bool{}, collections: map[string]string{}, reads: map[string]int{}}
	machine := strings.ToLower(resourceID(hybridMachineType, "local-vm"))
	f.ids[hybridMachineType] = machine
	f.ids[azureLocalVMType] = machine + "/providers/microsoft.azurestackhci/virtualmachineinstances/default"
	f.ids[azureLocalAgentType] = f.ids[azureLocalVMType] + "/guestagents/default"
	f.ids[azureLocalIdentityType] = f.ids[azureLocalVMType] + "/hybrididentitymetadata/default"
	for _, kind := range []string{azureLocalNICType, azureLocalDiskType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType} {
		f.ids[kind] = strings.ToLower(resourceID(kind, "local-"+last(kind)))
	}
	f.values[machine] = map[string]any{"id": machine, "name": last(machine), "type": hybridMachineType, "location": "eastus", "kind": "HCI", "properties": map[string]any{"vmId": "native-registration", "provisioningState": "Succeeded"}}
	f.collections["/subscriptions/"+testSubscription+"/providers/microsoft.hybridcompute/machines"] = hybridMachineType
	for kind, file := range map[string]string{azureLocalVMType: "GetVirtualMachineInstance", azureLocalAgentType: "GetGuestAgent", azureLocalIdentityType: "GetHybridIdentityMetadata", azureLocalNICType: "GetNetworkInterface", azureLocalDiskType: "GetVirtualHardDisk", azureLocalNetworkType: "GetLogicalNetwork", azureLocalStorageType: "GetStorageContainer", azureLocalImageType: "GetGalleryImage", azureLocalMarketplaceType: "GetMarketplaceGalleryImage"} {
		payload, err := os.ReadFile("fixtures/azure-local/" + file + ".json")
		var example map[string]any
		if err != nil || json.Unmarshal(payload, &example) != nil {
			t.Fatal(err)
		}
		raw := object(object(object(example["responses"])["200"])["body"])
		id := f.ids[kind]
		// Correct only fixture identities/references for this composed scenario.
		// Original examples remain unchanged and have separate schema tests.
		raw["id"], raw["type"], raw["name"] = id, kind, last(id)
		if azureLocalMachine(id) == "" {
			raw["location"] = "eastus"
		}
		if raw["extendedLocation"] != nil {
			raw["extendedLocation"] = map[string]any{"name": resourceID(azureLocalLocationType, "local-location"), "type": "CustomLocation"}
		}
		props := object(raw["properties"])
		props["futurePrivateConfiguration"] = "private-local-future"
		if props["containerId"] != nil {
			props["containerId"] = f.ids[azureLocalStorageType]
		}
		if kind == azureLocalVMType {
			props["storageProfile"] = map[string]any{"vmConfigStoragePathId": f.ids[azureLocalStorageType], "imageReference": map[string]any{"id": f.ids[azureLocalImageType]}, "osDisk": map[string]any{"id": f.ids[azureLocalDiskType]}, "dataDisks": []any{}}
			props["networkProfile"] = map[string]any{"networkInterfaces": []any{map[string]any{"id": f.ids[azureLocalNICType]}}}
			props["osProfile"] = map[string]any{"adminPassword": "private-local-password", "linuxConfiguration": map[string]any{"ssh": map[string]any{"publicKeys": []any{"private-local-key"}}}}
			props["hardwareProfile"] = map[string]any{"vmSize": "Default", "memoryMB": float64(4096), "processors": float64(2)}
			props["status"] = map[string]any{"powerState": "Running", "errorMessage": "private-local-error"}
		}
		if kind == azureLocalAgentType {
			props["credentials"] = map[string]any{"username": "private-local-user", "password": "private-local-password"}
		}
		if kind == azureLocalIdentityType {
			props["publicKey"] = "private-local-key"
		}
		if kind == azureLocalNICType {
			props["ipConfigurations"] = []any{map[string]any{"name": "ipconfig", "properties": map[string]any{"ipAddress": "10.4.0.8", "subnet": map[string]any{"id": f.ids[azureLocalNetworkType]}}}}
		}
		f.values[id] = raw
		collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
		if kind == azureLocalVMType {
			collection = machine + "/providers/microsoft.azurestackhci/virtualmachineinstances"
		} else if parent := azureLocalParent(id, kind); parent != "" {
			collection = parent + "/" + strings.ToLower(last(kind))
		}
		f.collections[collection] = kind
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.reads[path]++
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		provider := armPathProvider(path)
		if provider != "microsoft.azurestackhci" && provider != "microsoft.hybridcompute" {
			if res, ok := fleetGraphEmptyIndexes(t, req); ok {
				return res, nil
			}
		}
		version := azureLocalVersion
		if provider == "microsoft.hybridcompute" {
			version = hybridComputeVersion
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" || req.URL.Query().Get("api-version") != version {
			return nil, fmt.Errorf("unexpected Local request %s %s", req.Method, req.URL.String())
		}
		if value := f.values[path]; value != nil {
			return jsonResponse(200, value, http.Header{"X-Ms-Request-Id": {"local-native-read"}}), nil
		}
		if kind := f.collections[path]; kind != "" {
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.values)) {
				raw := f.values[id]
				if raw["type"] == kind && !f.omitted[id] {
					rows = append(rows, raw)
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		if _, typ, err := parseID(path); err == nil && (azureLocalKind(typ) != "" || typ == strings.ToLower(hybridMachineType)) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		return nil, fmt.Errorf("unexpected Local collection %s", path)
	})
	var err error
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *azureLocalFixture) request(kind string) contracts.InventoryRequest {
	k := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: azureLocalSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func TestAzureLocalNativeInventoryAndWorkers(t *testing.T) {
	f := newAzureLocalFixture(t)
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	kinds := []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalNICType, azureLocalDiskType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType}
	items := []contracts.InventoryItem{}
	for _, kind := range kinds {
		batch, err := f.runtime.List(ctx, f.request(kind))
		if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.RequestID != "local-native-read" {
			t.Fatal(kind, batch, err)
		}
		item := batch.Items[0]
		items = append(items, item)
		if item.NativeID != f.ids[kind] || item.Location != "eastus" || item.Actionable == nil || *item.Actionable {
			t.Fatal("native identity, region or cleanup capability", kind)
		}
		if kind == azureLocalVMType && (object(item.Normalized["status"])["powerState"] != "Running" || fmt.Sprint(object(item.Normalized["hardwareProfile"])["memoryMB"]) != "4096") {
			t.Fatal("missing VM state/capacity")
		}
		if kind == azureLocalNICType && !slices.Contains(item.NetworkReferences, f.ids[azureLocalNetworkType]) {
			t.Fatal("missing native logical network reference")
		}
		encoded, _ := json.Marshal(map[string]any{"item": item, "logs": logs})
		if strings.Contains(string(encoded), "private-local-") {
			t.Fatal("private configuration escaped", kind)
		}
		value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: kind}, Normalized: item.Normalized}
		if _, err := f.client.azureLocalRecordedReferences(value); err != nil {
			t.Fatal(err)
		}
		value.Normalized = maps.Clone(value.Normalized)
		value.Normalized["_azure_local_reference_binding"] = "forged"
		if _, err := f.client.azureLocalRecordedReferences(value); err == nil {
			t.Fatal("forged reference binding accepted")
		}
	}
	selected := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: f.ids[azureLocalNetworkType]}, items)
	if len(selected) != 5 {
		t.Fatal("logical network closure must include its NIC, VM and guest resources", len(selected))
	}
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, kinds, false, true)
	if len(values) != 9 {
		t.Fatal("native SQLite scan", len(values))
	}
	relations, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil || len(relations) != 9 {
		t.Fatal("native dependency graph", len(relations), err)
	}
	for _, value := range values {
		if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
			t.Fatal("unfinished Local lifecycle exposed generic cleanup")
		}
	}
	for id := range f.values {
		f.omitted[id] = true
	}
	values = azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, kinds, false, true)
	if len(values) != 9 {
		t.Fatal("known index omissions closed assets", len(values))
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, f.ids[azureLocalAgentType]) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return nil, false
	}
	values = azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, kinds, true, true)
	if len(values) != 9 {
		t.Fatal("failed known read closed assets")
	}
	f.override = nil
	delete(f.values, f.ids[azureLocalAgentType])
	values = azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, kinds, false, true)
	if len(values) != 8 {
		t.Fatal("known own absence not reconciled", len(values))
	}
}

func TestAzureLocalInventoryFailuresAndOwnAbsence(t *testing.T) {
	for _, mode := range []string{"denied", "partial", "missing-value", "filtered-page", "version-page", "foreign-page", "duplicate", "wrong-parent", "wrong-name", "async-read", "bad-state", "bad-reference", "surviving-orphan", "parent-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newAzureLocalFixture(t)
			req := f.request(azureLocalAgentType)
			id := f.ids[azureLocalAgentType]
			collection := id[:strings.LastIndex(id, "/")]
			if mode == "surviving-orphan" {
				req.KnownNativeIDs = []string{id}
				delete(f.values, f.ids[hybridMachineType])
			}
			f.override = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if mode == "parent-drift" && path == f.ids[hybridMachineType] && f.reads[path] > 1 {
					raw := maps.Clone(f.values[path])
					raw["etag"] = "changed"
					return jsonResponse(200, raw, nil), true
				}
				if path == id {
					raw := maps.Clone(f.values[id])
					props := maps.Clone(object(raw["properties"]))
					raw["properties"] = props
					switch mode {
					case "denied":
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					case "wrong-name":
						raw["name"] = "other"
					case "async-read":
						return jsonResponse(202, raw, nil), true
					case "bad-state":
						props["provisioningState"] = map[string]any{"password": "private"}
					case "bad-reference":
						raw["managedBy"] = "not-an-arm-id"
					default:
						return nil, false
					}
					return jsonResponse(200, raw, nil), true
				}
				if path != collection {
					return nil, false
				}
				switch mode {
				case "partial":
					return jsonResponse(206, map[string]any{"value": []any{}}, nil), true
				case "missing-value":
					return jsonResponse(200, map[string]any{}, nil), true
				case "filtered-page", "version-page", "foreign-page":
					next := r.URL.String() + "&$filter=name"
					if mode == "version-page" {
						next = strings.Replace(r.URL.String(), azureLocalVersion, "2023-09-01-preview", 1)
					}
					if mode == "foreign-page" {
						next = "https://evil.invalid/next"
					}
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
				case "duplicate":
					return jsonResponse(200, map[string]any{"value": []any{f.values[id], f.values[id]}}, nil), true
				case "wrong-parent":
					raw := maps.Clone(f.values[id])
					raw["id"] = strings.Replace(id, "/machines/local-vm/", "/machines/other/", 1)
					return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), req); err == nil {
				t.Fatal("invalid Local scan accepted", mode)
			}
		})
	}
	f := newAzureLocalFixture(t)
	id := f.ids[azureLocalAgentType]
	req := f.request(azureLocalAgentType)
	req.KnownNativeIDs = []string{id}
	delete(f.values, id)
	delete(f.values, f.ids[azureLocalVMType])
	delete(f.values, f.ids[hybridMachineType])
	batch, err := f.runtime.List(t.Context(), req)
	if err != nil || !batch.Complete || !slices.Equal(batch.AbsentNativeIDs, []string{id}) || f.reads[id] < 2 {
		t.Fatal("parent absence substituted for own reads", batch, err)
	}
}

func TestAzureLocalContinuationAndIdentityBoundaries(t *testing.T) {
	f := newAzureLocalFixture(t)
	kind := azureLocalDiskType
	id := f.ids[kind]
	other := id + "-second"
	raw := maps.Clone(f.values[id])
	raw["id"], raw["name"] = other, last(other)
	f.values[other] = raw
	req := f.request(kind)
	req.Limit = 1
	first, err := f.runtime.List(t.Context(), req)
	if err != nil || first.Complete || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	req.Cursor = first.NextCursor
	second, err := f.runtime.List(t.Context(), req)
	if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
		t.Fatal(second, err)
	}
	object(f.values[id]["properties"])["futurePrivateConfiguration"] = "changed-private-local"
	if _, err := f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("private config drift accepted continuation")
	}
	vm := f.ids[azureLocalVMType]
	for _, bad := range []string{strings.ToLower(resourceID(azureLocalVMType, "default")), strings.Replace(vm, "/machines/", "/virtualmachines/", 1), strings.Replace(vm, "/default", "/other", 1), vm + "/guestagents/default/extra/name", vm + "?x=y", vm + "/", strings.Replace(vm, testSubscription, testTenant, 1)} {
		if _, err := f.client.azureLocalIdentity(bad, azureLocalVMType); err == nil {
			t.Fatal("malformed Local identity", bad)
		}
	}
	for _, source := range []string{productInventorySource, hybridComputeSource} {
		req = f.request(kind)
		req.Source = source
		if _, err := f.runtime.List(t.Context(), req); err == nil {
			t.Fatal("wrong inventory source accepted")
		}
	}
	for _, cursor := range []string{"not-base64", "eyJUYXJnZXQiOjF9"} {
		req = f.request(kind)
		req.Cursor = cursor
		if _, err := f.runtime.List(t.Context(), req); err == nil {
			t.Fatal("forged continuation accepted")
		}
	}
	req = f.request(kind)
	req.Scope.NativeID = testTenant
	if _, err := f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("foreign subscription accepted")
	}
	req = f.request(kind)
	req.KnownNativeIDs = []string{id, id}
	if _, err := f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("duplicate known identity accepted")
	}
	req = f.request(kind)
	req.KnownNativeMetadata = map[string]map[string]any{id: {"unrelated": "metadata"}}
	if _, err := f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("unrelated known metadata accepted")
	}
}

func TestAzureLocalSingletonOmissionsAndNativePagination(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newAzureLocalFixture(t)
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if f.collections[path] != "" && strings.Contains(path, "/virtualmachineinstances") {
					if status == 404 {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), f.request(azureLocalAgentType))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("unrecorded singleton omission lost guest", batch, err)
			}
			delete(f.values, f.ids[azureLocalAgentType])
			batch, err = f.runtime.List(t.Context(), f.request(azureLocalAgentType))
			if err != nil || len(batch.Items) != 0 {
				t.Fatal("absent singleton", batch, err)
			}
		})
	}
	f := newAzureLocalFixture(t)
	id := f.ids[azureLocalDiskType]
	second := id + "-b"
	raw := maps.Clone(f.values[id])
	raw["id"], raw["name"] = second, last(second)
	f.values[second] = raw
	pages := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if f.collections[strings.ToLower(req.URL.Path)] != azureLocalDiskType {
			return nil, false
		}
		pages++
		if req.URL.Query().Get("$skiptoken") == "next" {
			return jsonResponse(200, map[string]any{"value": []any{f.values[second]}}, nil), true
		}
		return jsonResponse(200, map[string]any{"value": []any{f.values[id]}, "nextLink": req.URL.String() + "&$skiptoken=next"}, nil), true
	}
	batch, err := f.runtime.List(t.Context(), f.request(azureLocalDiskType))
	if err != nil || len(batch.Items) != 2 || pages != 4 {
		t.Fatal("native pagination omitted resources", batch, pages, err)
	}
	request := f.request(azureLocalDiskType)
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || !batch.Complete || len(batch.Items) != 0 {
		t.Fatal("regional projection", batch, err)
	}
}
