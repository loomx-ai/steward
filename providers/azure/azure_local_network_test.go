package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func localNetworkFixture(t *testing.T) *azureLocalFixture {
	t.Helper()
	f := newAzureLocalFixture(t)
	vnet := nativeResource(vnetType, "cloud-net", "eastus", nil)
	vnetID := strings.ToLower(text(vnet["id"]))
	f.ids[vnetType] = vnetID
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		switch path {
		case "/subscriptions/" + testSubscription + "/providers/microsoft.network/virtualnetworks":
			return jsonResponse(200, map[string]any{"value": []any{vnet}}, http.Header{"X-Ms-Request-Id": {"vnet-index"}}), true
		case vnetID:
			return jsonResponse(200, vnet, http.Header{"X-Ms-Request-Id": {"vnet-read"}}), true
		case vnetID + "/subnets", "/subscriptions/" + testSubscription + "/resources", "/subscriptions/" + testSubscription + "/resourcegroups", "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return nil, false
	}
	return f
}

func TestAzureLocalLiveNetworkPagesAndScope(t *testing.T) {
	f := localNetworkFixture(t)
	id := f.ids[azureLocalNetworkType]
	secondID := id + "-b"
	raw := maps.Clone(f.values[id])
	raw["id"], raw["name"] = secondID, last(secondID)
	f.values[secondID] = raw
	query := contracts.NetworkTargetQuery{ConnectionID: "connection", Kind: asset.ScanTargetVPC, RegionID: "eastus", Limit: 1}
	first, err := f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(first.Items) != 1 || first.Items[0].NativeID != f.ids[vnetType] || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	query.Cursor = first.NextCursor
	second, err := f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(second.Items) != 1 || second.Items[0].NativeID != id || second.NextCursor == "" || second.RequestID != "local-native-read" {
		t.Fatal(second, err)
	}
	query.Cursor = second.NextCursor
	third, err := f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(third.Items) != 1 || third.Items[0].NativeID != secondID || third.NextCursor != "" {
		t.Fatal(third, err)
	}
	f.runtime.clients["other"] = f.client
	f.runtime.credentials = credentialFunc(func(_ context.Context, _ asset.ConnectionID) (contracts.Credential, error) {
		return testCredential(), nil
	})
	for _, field := range []string{"query", "region", "connection", "kind", "proof"} {
		changed := query
		changed.Cursor = first.NextCursor
		switch field {
		case "query":
			changed.Query = "different"
		case "region":
			changed.RegionID = "westus"
		case "connection":
			changed.ConnectionID = "other"
		case "kind":
			changed.Kind = asset.ScanTargetVSwitch
		case "proof":
			encoded, _ := base64.RawURLEncoding.DecodeString(changed.Cursor)
			var cursor azureNetworkCursor
			_ = json.Unmarshal(encoded, &cursor)
			cursor.Kind = vnetType
			encoded, _ = json.Marshal(cursor)
			changed.Cursor = base64.RawURLEncoding.EncodeToString(encoded)
		}
		if _, err := f.runtime.SearchNetworkTargets(t.Context(), changed); err == nil {
			t.Fatal("foreign/forged cursor", field)
		}
	}
	query.Cursor = ""
	query.Query = "logicalnetworks"
	filtered, err := f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].NativeID != id {
		t.Fatal("empty VNet page concealed Local results", filtered, err)
	}
	query.Query = "does-not-exist"
	for {
		filtered, err = f.runtime.SearchNetworkTargets(t.Context(), query)
		if err != nil || len(filtered.Items) != 0 {
			t.Fatal(filtered, err)
		}
		if filtered.NextCursor == "" {
			break
		}
		query.Cursor = filtered.NextCursor
	}
	query.Cursor = ""
	query.Query = id
	f.omitted[id] = true
	vnetReads := f.reads[f.ids[vnetType]]
	filtered, err = f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].NativeID != id || filtered.NextCursor != "" || f.reads[f.ids[vnetType]] != vnetReads {
		t.Fatal("exact live lookup lost known omission or queried VNet", filtered, err)
	}
	query.RegionID = "westus"
	filtered, err = f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(filtered.Items) != 0 {
		t.Fatal("foreign region", filtered, err)
	}
	query.RegionID = "eastus"
	query.Kind = asset.ScanTargetVSwitch
	query.Query = ""
	query.ParentNativeID = id
	filtered, err = f.runtime.SearchNetworkTargets(t.Context(), query)
	if err != nil || len(filtered.Items) != 0 || filtered.NextCursor != "" {
		t.Fatal("invented Local subnet IDs", filtered, err)
	}
	query.ParentNativeID = strings.Replace(id, testSubscription, testTenant, 1)
	if _, err := f.runtime.SearchNetworkTargets(t.Context(), query); err == nil {
		t.Fatal("foreign network parent")
	}
}

func TestAzureLocalNetworkPermissionsAndDiskMembership(t *testing.T) {
	f := localNetworkFixture(t)
	id := f.ids[azureLocalNetworkType]
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return previous(req)
	}
	query := contracts.NetworkTargetQuery{ConnectionID: "connection", Kind: asset.ScanTargetVPC, RegionID: "eastus", Query: id}
	if _, err := f.runtime.SearchNetworkTargets(t.Context(), query); err == nil {
		t.Fatal("denied Local lookup became an empty list")
	}
	f.override = previous
	request := f.request(azureLocalDiskType)
	request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: id}
	batch, err := f.runtime.List(t.Context(), request)
	vm, disk := f.ids[azureLocalVMType], f.ids[azureLocalDiskType]
	if err != nil || len(batch.Items) != 1 || !slices.Contains(batch.Items[0].NetworkReferences, vm) {
		t.Fatal("disk lost reviewed VM attachment", batch, err)
	}
	if slices.Contains(stringValues(batch.Items[0].Normalized[referenceKey(azureLocalVMType)]), vm) {
		t.Fatal("network membership became a reverse dependency")
	}
	request.KnownNativeIDs = []string{disk}
	request.KnownNativeMetadata = map[string]map[string]any{disk: batch.Items[0].Normalized}
	f.omitted[f.ids[hybridMachineType]] = true
	f.omitted[vm] = true
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || !slices.Contains(batch.Items[0].NetworkReferences, vm) {
		t.Fatal("known VM omission concealed attached disk", batch, err)
	}
	request.KnownNativeMetadata[disk] = maps.Clone(request.KnownNativeMetadata[disk])
	request.KnownNativeMetadata[disk]["_azure_local_network_binding"] = "forged"
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("forged disk membership")
	}
	request.KnownNativeMetadata[disk] = batch.Items[0].Normalized
	object(f.values[vm]["properties"])["storageProfile"] = map[string]any{}
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || slices.Contains(batch.Items[0].NetworkReferences, vm) {
		t.Fatal("detached disk retained network membership", batch, err)
	}
	// An empty, signed membership survives persistence and a subsequent scan.
	encoded, _ := json.Marshal(batch.Items[0].Normalized)
	var persisted map[string]any
	_ = json.Unmarshal(encoded, &persisted)
	request.KnownNativeMetadata[disk] = persisted
	if _, err := f.runtime.List(t.Context(), request); err != nil {
		t.Fatal("empty persisted membership", err)
	}
}

func TestAzureLocalSelectedNetworkCreatorAndSQLiteWorker(t *testing.T) {
	f := localNetworkFixture(t)
	// Exercise the real application with the nine Local rules enabled. Other
	// service families retain their own independently composed integration tests.
	bundle := f.runtime.Bundle()
	filtered := bundle.Specs[:0]
	for _, compiled := range bundle.Specs {
		if azureLocalKind(compiled.ResourceKind.NativeType) != "" {
			filtered = append(filtered, compiled)
		}
	}
	bundle.Specs = filtered
	f.runtime.bundle = bundle
	repository, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	id := f.ids[azureLocalNetworkType]
	request := inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "azure-local-network-test", ScopeMode: asset.ScanSelectedNetworks, NetworkTargets: []inventory.NetworkTargetRequest{{Kind: asset.ScanTargetVPC, RegionID: "eastus", NativeID: id, Name: "untrusted submitted name"}}}
	created, err := creator.Create(t.Context(), request)
	if err != nil || len(created.Jobs) != 1 || len(created.Shards) != 10 || len(created.ScanRun.Targets) != 1 || created.ScanRun.Targets[0].Name != last(id) {
		t.Fatal("live network creation", created, err)
	}
	if err := inventory.NewScanHandler(repository, registry, inventory.NewService(repository)).Handle(t.Context(), created.Jobs[0]); err != nil {
		t.Fatal("selected Local network execution", err)
	}
	for _, shard := range created.Shards {
		current, err := repository.GetScanShard(t.Context(), shard.ID)
		if err != nil || current.Status != asset.ShardSucceeded {
			t.Fatal("Local network shard", current, err)
		}
	}
	jobs, err := repository.ListJobsByAggregate(t.Context(), "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	graphs := 0
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			graphs++
			if err := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{f.runtime}).Handle(t.Context(), job); err != nil {
				t.Fatal(err)
			}
		}
	}
	if graphs != 1 {
		t.Fatal("network graph not scheduled", graphs)
	}
	assets, err := repository.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(assets) != 6 {
		t.Fatal("network selection omitted VM disk or retained unrelated storage/images", len(assets), err)
	}
	for _, value := range assets {
		if !slices.Contains([]string{azureLocalNetworkType, azureLocalNICType, azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalDiskType}, value.Identity.NativeType) {
			t.Fatal("unrelated network asset", value.Identity.NativeType)
		}
	}
	delete(f.values, id)
	if _, err := creator.Create(t.Context(), request); err == nil {
		t.Fatal("deleted selected network accepted")
	}
	runs, err := repository.ListScanRunsByConnection(t.Context(), "connection")
	if err != nil || len(runs) != 1 {
		t.Fatal("invalid target persisted a scan", len(runs), err)
	}
}
