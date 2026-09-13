package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type elasticSanFixture struct {
	runtime  *Runtime
	client   *client
	values   map[string]map[string]any
	retained map[string]bool
	omitted  map[string]bool
	ids      map[string]string
	override func(*http.Request) (*http.Response, bool)
}

func newElasticSanFixture(t *testing.T) *elasticSanFixture {
	t.Helper()
	f := &elasticSanFixture{values: map[string]map[string]any{}, retained: map[string]bool{}, omitted: map[string]bool{}, ids: map[string]string{}}
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		raw := elasticSanTestRecord(kind)
		id := text(raw["id"])
		f.ids[kind], f.values[id] = id, raw
		raw["etag"] = "generation-one"
	}
	group := f.values[f.ids[elasticSanGroupType]]
	subnet := strings.ToLower(resourceID(vnetType, "vnet")) + "/subnets/subnet"
	f.ids[subnetType] = subnet
	object(group["properties"])["networkAcls"] = map[string]any{"virtualNetworkRules": []any{map[string]any{"id": subnet, "action": "Allow"}}}
	object(group["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Enabled", "retentionPeriodDays": 7}
	object(group["properties"])["encryptionProperties"] = map[string]any{"keyVaultProperties": map[string]any{"keyVaultUri": "https://private-elastic-vault.vault.azure.net/"}}
	volume := f.values[f.ids[elasticSanVolumeType]]
	object(volume["properties"])["sizeGiB"] = 8
	object(volume["properties"])["volumeId"] = testTenant
	object(volume["properties"])["storageTarget"] = map[string]any{"targetIqn": "private-elastic-target", "targetPortalHostname": "private-elastic-address"}
	object(f.values[f.ids[elasticSanSnapshotType]]["properties"])["creationData"] = map[string]any{"sourceId": f.ids[elasticSanVolumeType]}
	object(f.values[f.ids[elasticSanEndpointType]]["properties"])["privateEndpoint"] = map[string]any{"id": strings.ToLower(resourceID("Microsoft.Network/privateEndpoints", "endpoint"))}
	object(f.values[f.ids[elasticSanEndpointType]]["properties"])["privateLinkServiceConnectionState"] = map[string]any{"status": "Pending", "actionsRequired": "None", "description": "private-elastic-connection-description"}
	object(f.values[f.ids[elasticSanEndpointType]]["properties"])["groupIds"] = []any{"volumegroup"}
	for _, entry := range []struct{ original, id string }{
		{f.ids[elasticSanGroupType], f.ids[elasticSanGroupType] + "-retained"},
		{f.ids[elasticSanVolumeType], f.ids[elasticSanVolumeType] + "-1751081600"},
		{f.ids[elasticSanVolumeType], f.ids[elasticSanGroupType] + "-retained/volumes/child-1751081600"},
	} {
		raw := maps.Clone(f.values[entry.original])
		raw["properties"] = maps.Clone(object(raw["properties"]))
		raw["id"], raw["name"] = entry.id, last(entry.id)
		object(raw["properties"])["provisioningState"] = "Deleted"
		f.values[entry.id], f.retained[entry.id] = raw, true
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if f.override != nil {
			if response, handled := f.override(req); handled {
				return response, nil
			}
		}
		if armPathProvider(req.URL.Path) != "microsoft.elasticsan" {
			if response, handled := fleetGraphEmptyIndexes(t, req); handled {
				return response, nil
			}
		}
		if req.Method != "GET" || req.URL.Query().Get("api-version") != elasticSanVersion {
			t.Fatal("unexpected Elastic SAN transport", req.Method, req.URL.Path)
		}
		path := strings.ToLower(req.URL.Path)
		if raw := f.values[path]; raw != nil && !f.retained[path] {
			if req.Header.Get("x-ms-access-soft-deleted-resources") != "" {
				t.Fatal("GET gained retained selector")
			}
			return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"elastic-read"}}), nil
		}
		for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
			rootCollection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(elasticSanType)
			if kind == elasticSanType && path != rootCollection || kind != elasticSanType && !strings.HasSuffix(path, "/"+strings.ToLower(last(kind))) {
				continue
			}
			retained := req.Header.Get("x-ms-access-soft-deleted-resources") == "true"
			if kind == elasticSanGroupType || kind == elasticSanVolumeType {
				if !slices.Contains([]string{"true", "false"}, req.Header.Get("x-ms-access-soft-deleted-resources")) {
					t.Fatal("missing population selector")
				}
			}
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.values)) {
				raw := f.values[id]
				if raw["type"] != kind || f.omitted[id] || f.retained[id] != retained {
					continue
				}
				if kind == elasticSanType || id[:strings.LastIndex(id, "/")] == path {
					rows = append(rows, raw)
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, http.Header{"X-Ms-Request-Id": {"elastic-index"}}), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	var err error
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *elasticSanFixture) request(kind string) contracts.InventoryRequest {
	k := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: elasticSanSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func elasticSanTestAsset(item contracts.InventoryItem) asset.Asset {
	return asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}
}

func TestElasticSanInventoryNativePopulationsAndSQLite(t *testing.T) {
	f := newElasticSanFixture(t)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	logs, items := []execution.JobLogEntry{}, []contracts.InventoryItem{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	for _, kind := range kinds {
		batch, err := f.runtime.List(ctx, f.request(kind))
		expected := map[string]int{elasticSanType: 1, elasticSanGroupType: 2, elasticSanVolumeType: 3, elasticSanSnapshotType: 1, elasticSanEndpointType: 1}[kind]
		if err != nil || !batch.Complete || len(batch.Items) != expected {
			t.Fatal("native population", kind, len(batch.Items), err)
		}
		for _, item := range batch.Items {
			if item.Location != "eastus" || item.Actionable == nil || *item.Actionable || item.Normalized["retained"] != f.retained[item.NativeID] {
				t.Fatal("region, retention or actionability changed", item.NativeID)
			}
			if _, err := f.client.elasticSanRecorded(elasticSanTestAsset(item)); err != nil {
				t.Fatal("inventory proof", err)
			}
			if kind == elasticSanEndpointType && (object(item.Normalized["privateLinkServiceConnectionState"])["status"] != "Pending" || !slices.Equal(stringValues(item.Normalized["groupIds"]), []string{"volumegroup"})) {
				t.Fatal("private endpoint operational status lost")
			}
			if _, err := f.runtime.ResolveAction(ctx, "connection", elasticSanTestAsset(item)); err == nil {
				t.Fatal("unreviewed cleanup registered")
			}
		}
		items = append(items, batch.Items...)
	}
	encoded, _ := json.Marshal(map[string]any{"items": items, "logs": logs})
	if strings.Contains(string(encoded), "private-elastic-") {
		t.Fatal("private configuration escaped")
	}
	selected := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVSwitch, NativeID: f.ids[subnetType]}, items)
	if len(selected) != 8 || len(inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVSwitch, NativeID: f.ids[subnetType] + "-other"}, items)) != 0 {
		t.Fatal("native network closure", len(selected))
	}
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 8 {
		t.Fatal("SQLite inventory", len(values))
	}
	relations, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil || len(relations) != 8 {
		t.Fatal("native parent/source references", len(relations), err)
	}
	for _, relation := range relations {
		if relation.Type != graph.RelationshipUses {
			t.Fatal("ordinary reference became ownership", relation)
		}
	}
	// Reopen persisted observations with a fresh runtime and credential client.
	repo, err = sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	f.runtime = fresh
	registry = providerruntime.NewRegistry()
	if err := registry.Register(fresh); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(fresh.Bundle()); err != nil {
		t.Fatal(err)
	}
	for id := range f.values {
		if !f.retained[id] {
			f.omitted[id] = true
		}
	}
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 8 {
		t.Fatal("known-ID index omissions closed assets", len(values))
	}
	retainedID := f.ids[elasticSanVolumeType] + "-1751081600"
	delete(f.values, retainedID)
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 7 {
		t.Fatal("retained absence not reconciled", len(values))
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, f.ids[elasticSanVolumeType]) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return nil, false
	}
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, true, true)
	if len(values) != 7 {
		t.Fatal("permission failure closed assets")
	}
}

func TestElasticSanInventoryHistoricalParentsAndRegionalAbsence(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanVolumeType)
	initial, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.KnownNativeMetadata = map[string]map[string]any{}
	for _, item := range initial.Items {
		request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
		request.KnownNativeMetadata[item.NativeID] = item.Normalized
	}
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	outside, err := f.runtime.List(t.Context(), request)
	if err != nil || !outside.Complete || len(outside.Items)+len(outside.AbsentNativeIDs) != 0 {
		t.Fatal("regional filtering closed a live known ID", outside, err)
	}
	request.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}
	delete(f.values, f.ids[elasticSanType])
	delete(f.values, f.ids[elasticSanGroupType])
	recovered, err := f.runtime.List(t.Context(), request)
	if err != nil || len(recovered.Items) != 3 || len(recovered.AbsentNativeIDs) != 0 {
		t.Fatal("retained child collections lost verified historical region", recovered, err)
	}
	for _, item := range recovered.Items {
		if item.Location != "eastus" || !slices.Contains(item.NetworkReferences, f.ids[subnetType]) {
			t.Fatal("historical region/network evidence lost")
		}
	}
	request.KnownNativeMetadata = nil
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("invented region for unverified orphan")
	}
}

func TestElasticSanReferencesRemainOrdinaryAndSubscriptionBound(t *testing.T) {
	f := newElasticSanFixture(t)
	foreign := strings.ToLower(strings.Replace(resourceID(vmType, "controller"), testSubscription, testTenant, 1))
	object(f.values[f.ids[elasticSanVolumeType]]["properties"])["managedBy"] = map[string]any{"resourceId": foreign}
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
	if err != nil {
		t.Fatal(err)
	}
	var value asset.Asset
	for _, item := range batch.Items {
		if item.NativeID == f.ids[elasticSanVolumeType] {
			value = elasticSanTestAsset(item)
		}
	}
	refs, err := f.client.elasticSanRecordedReferences(value)
	if err != nil || !slices.Contains(refs[vmType], foreign) {
		t.Fatal("native foreign controller reference lost", refs, err)
	}
	contribution, err := f.client.contributeNativeReferences(value, nil, refs, "azure:elastic-san-reference")
	if err != nil || len(contribution.Relationships) != 0 || len(contribution.Unresolved) != 2 {
		t.Fatal("external reference resolution broadened scope", contribution, err)
	}
	for _, reference := range contribution.Unresolved {
		if reference.Relationship != graph.RelationshipUses {
			t.Fatal("controller reference became ownership")
		}
	}
	value.Location = "westus"
	if _, err := f.client.elasticSanRecordedReferences(value); err == nil {
		t.Fatal("graph accepted altered recorded location")
	}
}

func TestElasticSanRestoredNativeIDIsNotARetainedAlias(t *testing.T) {
	f := newElasticSanFixture(t)
	activeID := f.ids[elasticSanVolumeType]
	retainedID := activeID + "-1751081600"
	delete(f.values, activeID)
	request := f.request(elasticSanVolumeType)
	initial, err := f.runtime.List(t.Context(), request)
	if err != nil || len(initial.Items) != 2 {
		t.Fatal("initial retained inventory", initial, err)
	}
	request.KnownNativeMetadata = map[string]map[string]any{}
	for _, item := range initial.Items {
		request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
		request.KnownNativeMetadata[item.NativeID] = item.Normalized
	}
	restored := maps.Clone(f.values[retainedID])
	restored["properties"] = maps.Clone(object(restored["properties"]))
	restored["id"], restored["name"] = activeID, last(activeID)
	object(restored["properties"])["provisioningState"] = "Succeeded"
	f.values[activeID] = restored
	delete(f.values, retainedID)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || !batch.Complete || len(batch.Items) != 2 || !slices.Equal(batch.AbsentNativeIDs, []string{retainedID}) {
		t.Fatal("restored ID reconciliation", batch, err)
	}
	found := false
	for _, item := range batch.Items {
		if item.NativeID == activeID {
			found = true
			if item.Normalized["retained"] != false || item.Normalized["volumeId"] != testTenant || slices.Contains(item.NativeAliases, retainedID) || slices.Contains(item.NativeAliases, testTenant) {
				t.Fatal("restoration conflated native identities", item.NativeAliases)
			}
		}
	}
	if !found {
		t.Fatal("restored original ID missing")
	}
}

func TestElasticSanInventorySourceAndAncestorBoundaries(t *testing.T) {
	f := newElasticSanFixture(t)
	for _, source := range []string{productInventorySource, azureLocalSource, hybridComputeSource} {
		request := f.request(elasticSanType)
		request.Source = source
		if _, err := f.runtime.List(t.Context(), request); err == nil {
			t.Fatal("wrong inventory source accepted", source)
		}
	}
	request := f.request(elasticSanType)
	request.Source = inventorySource
	if batch, err := f.runtime.List(t.Context(), request); err != nil || !batch.Complete || len(batch.Items) != 0 {
		t.Fatal("generic graph duplicate inventory", batch, err)
	}
	request.Source = ""
	if batch, err := f.runtime.List(t.Context(), request); err != nil || len(batch.Items) != 1 {
		t.Fatal("default source did not route natively", batch, err)
	}
	request.Scope.NativeID = testTenant
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("foreign subscription accepted")
	}
	f.values[f.ids[elasticSanGroupType]]["location"] = "westus"
	if _, err := f.runtime.List(t.Context(), f.request(elasticSanType)); err == nil {
		t.Fatal("SAN network context accepted a conflicting group region")
	}
}

func TestElasticSanInventoryCursorAndProofBoundaries(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanVolumeType)
	request.Limit = 1
	first, err := f.runtime.List(t.Context(), request)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" || first.Complete {
		t.Fatal("initial cursor", first, err)
	}
	request.Cursor = first.NextCursor
	second, err := f.runtime.List(t.Context(), request)
	if err != nil || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
		t.Fatal("continuation", second, err)
	}
	for _, change := range []string{"scope", "options", "known", "kind", "data"} {
		changed := request
		switch change {
		case "scope":
			changed.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
		case "options":
			changed.Options = map[string]any{"retained": false}
		case "known":
			changed.KnownNativeIDs = []string{f.ids[elasticSanVolumeType]}
		case "kind":
			k := f.runtime.resourceKind(elasticSanGroupType)
			changed.ResourceKind = &k
		case "data":
			object(f.values[f.ids[elasticSanVolumeType]]["properties"])["sizeGiB"] = 16
		}
		if _, err := f.runtime.List(t.Context(), changed); err == nil {
			t.Fatal("changed cursor boundary accepted", change)
		}
	}
	item := first.Items[0]
	for _, change := range []string{"proof", "location", "retained", "references", "network", "record", "source", "foreign-metadata"} {
		request := f.request(elasticSanVolumeType)
		request.KnownNativeIDs = []string{item.NativeID}
		metadata := maps.Clone(item.Normalized)
		record := maps.Clone(object(metadata[elasticSanInventoryRecord]))
		metadata[elasticSanInventoryRecord] = record
		switch change {
		case "proof":
			metadata[elasticSanInventoryProof] = "forged"
		case "location":
			record["location"] = "westus"
		case "retained":
			metadata["retained"] = !(metadata["retained"] == true)
		case "references":
			record["references"] = map[string]any{}
		case "network":
			record["network"] = []string{"forged"}
		case "record":
			delete(metadata, elasticSanInventoryRecord)
		case "source":
			metadata["_inventory_source"] = productInventorySource
		}
		request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: metadata}
		if change == "foreign-metadata" {
			request.KnownNativeMetadata[item.NativeID+"-other"] = metadata
		}
		if _, err := f.runtime.List(t.Context(), request); err == nil {
			t.Fatal("forged inventory evidence accepted", change)
		}
	}
}

func TestElasticSanInventoryIncompleteAndChangingCollections(t *testing.T) {
	for _, failure := range []string{"retained-forbidden", "missing-parent-list", "active-get-missing", "changed-between-passes", "cross-population", "bad-reference"} {
		t.Run(failure, func(t *testing.T) {
			f := newElasticSanFixture(t)
			calls := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				switch failure {
				case "retained-forbidden":
					if req.Header.Get("x-ms-access-soft-deleted-resources") == "true" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
				case "missing-parent-list":
					if path == f.ids[elasticSanGroupType]+"/volumes" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ParentNotFound"}}, nil), true
					}
				case "active-get-missing":
					if path == f.ids[elasticSanVolumeType] {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
				case "changed-between-passes":
					if path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(elasticSanType) {
						calls++
						f.values[f.ids[elasticSanType]]["etag"] = fmt.Sprint(calls)
					}
				case "cross-population":
					if path == f.ids[elasticSanGroupType]+"/volumes" && req.Header.Get("x-ms-access-soft-deleted-resources") == "true" {
						return jsonResponse(200, map[string]any{"value": []any{f.values[f.ids[elasticSanVolumeType]]}}, nil), true
					}
				case "bad-reference":
					object(f.values[f.ids[elasticSanVolumeType]]["properties"])["managedBy"] = map[string]any{"resourceId": "https://untrusted.invalid/owner"}
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
			if err == nil || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 || batch.Complete {
				t.Fatal("incomplete inventory returned data", batch, err)
			}
			if failure == "changed-between-passes" && calls != 2 {
				t.Fatal("did not compare two complete native snapshots", calls)
			}
		})
	}
}
