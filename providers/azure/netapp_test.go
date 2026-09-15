package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappFixture struct {
	runtime         *Runtime
	objects         map[string]map[string]any
	hidden, missing map[string]bool
	paged           bool
	override        func(*http.Request) (*http.Response, bool)
}

func newNetappFixture(t *testing.T) *netappFixture {
	f := &netappFixture{objects: map[string]map[string]any{}, hidden: map[string]bool{}, missing: map[string]bool{}}
	examples := map[string]string{"Backups": "BackupsUnderBackupVault_Get", "VolumeGroups": "VolumeGroups_Get_Oracle"}
	for _, account := range []string{"first", "second"} {
		parents := map[string]string{}
		for _, kind := range netappResources {
			example := examples[kind.family]
			if example == "" {
				example = kind.family + "_Get"
			}
			wire, err := os.ReadFile("fixtures/netapp/" + example + ".json")
			var d map[string]any
			if err != nil || json.Unmarshal(wire, &d) != nil {
				t.Fatal(example, err)
			}
			raw := object(object(object(d["responses"])["200"])["body"])
			id := parents[kind.parent] + "/" + strings.ToLower(last(kind.kind)) + "/item"
			if kind.parent == "" {
				id = strings.ToLower(resourceID(netappAccountType, account))
			}
			parents[kind.kind] = id
			raw["id"], raw["type"] = id, kind.kind
			parts := strings.Split(id, "/")
			names := []string{}
			for i := 8; i < len(parts); i += 2 {
				names = append(names, parts[i])
			}
			raw["name"] = strings.Join(names, "/")
			if kind.family != "Backups" && kind.family != "Subvolumes" {
				raw["location"] = "eastus"
				if account == "second" {
					raw["location"] = "westus"
				}
			}
			object(raw["properties"])["futurePrivateField"] = "netapp-private-canary"
			if kind.family == "Volumes" {
				object(raw["properties"])["enableSubvolumes"] = "Enabled"
				object(raw["properties"])["subnetId"] = strings.ToLower(resourceID(vnetType, "network")) + "/subnets/subnet"
			}
			if kind.family == "Backups" {
				object(raw["properties"])["volumeResourceId"] = parents[netappVolumeType]
				object(raw["properties"])["backupId"] = "32d91255-643d-d8b0-b52e-6990f6813a32"
			}
			f.objects[id] = raw
		}
	}
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if f.override != nil {
			if res, ok := f.override(q); ok {
				return res, nil
			}
		}
		path := strings.ToLower(q.URL.Path)
		if q.Method == "POST" && strings.HasSuffix(path, "/listreplications") && q.URL.Query().Get("api-version") == netappVersion {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if q.Method == "GET" && q.URL.Query().Get("api-version") == resourcesVersion && strings.EqualFold(path, "/subscriptions/"+testSubscription+"/resourceGroups/test") {
			return jsonResponse(200, map[string]any{"id": path, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}, nil), nil
		}
		if q.Method == "GET" && strings.HasSuffix(path, "/providers/microsoft.authorization/locks") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if q.Method != "GET" || q.URL.Host != "management.azure.com" || q.URL.Query().Get("api-version") != netappVersion {
			t.Fatal("unexpected request", q.Method, q.URL)
		}
		if f.missing[path] {
			return jsonResponse(404, nil, nil), nil
		}
		if raw := f.objects[path]; raw != nil {
			return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"netapp-own-request"}}), nil
		}
		if f.paged && q.URL.Query().Get("$skiptoken") == "" {
			return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, netappVersion) + "&$skiptoken=page2"}, nil), nil
		}
		rows := []any{}
		for _, id := range slices.Sorted(maps.Keys(f.objects)) {
			raw := f.objects[id]
			root := strings.HasSuffix(path, "/providers/microsoft.netapp/netappaccounts") && raw["type"] == netappAccountType
			if (root || strings.EqualFold(redisParentID(id)+"/"+last(text(raw["type"])), path)) && !f.hidden[id] && !f.missing[id] {
				rows = append(rows, raw)
			}
		}
		return jsonResponse(200, map[string]any{"value": rows}, nil), nil
	})
	return f
}
func netappRequest(r *Runtime, kind string) contracts.InventoryRequest {
	req := productRequest(r, kind)
	req.Source = netappSource
	return req
}
func TestNetappNativeInventory(t *testing.T) {
	for _, kind := range netappResources {
		t.Run(kind.family, func(t *testing.T) {
			f := newNetappFixture(t)
			f.paged = true
			req := netappRequest(f.runtime, kind.kind)
			req.Limit = 1
			page, err := f.runtime.List(t.Context(), req)
			if err != nil || len(page.Items) != 1 || page.Complete || page.RequestID != "netapp-own-request" {
				t.Fatal(page, err)
			}
			first := page.Items[0]
			wire, _ := json.Marshal(first)
			if strings.Contains(string(wire), "netapp-private-canary") || first.Actionable == nil || *first.Actionable != (kind.kind == netappVolumeType || netappRecoveryKind(kind.kind)) {
				t.Fatal("unsafe inventory", string(wire))
			}
			if kind.kind == netappVolumeType {
				subnet := strings.ToLower(resourceID(vnetType, "network")) + "/subnets/subnet"
				if !slices.Contains(first.NetworkReferences, subnet) || !slices.Contains(first.NetworkReferences, redisParentID(subnet)) {
					t.Fatal("network missing", first)
				}
			}
			req.Cursor = page.NextCursor
			page, err = f.runtime.List(t.Context(), req)
			if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].NativeID == first.NativeID {
				t.Fatal(page, err)
			}
			req.Cursor = ""
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			req.KnownNativeIDs = []string{first.NativeID, page.Items[0].NativeID}
			for id := range f.objects {
				f.hidden[id] = true
			}
			recovered, err := f.runtime.List(t.Context(), req)
			if err != nil || len(recovered.Items) != 1 || len(recovered.AbsentNativeIDs) != 0 {
				t.Fatal("known omission", recovered, err)
			}
			f.missing[first.NativeID] = true
			gone, err := f.runtime.List(t.Context(), req)
			if err != nil || len(gone.Items) != 0 || len(gone.AbsentNativeIDs) != 1 || gone.AbsentNativeIDs[0] != first.NativeID {
				t.Fatal("own absence", gone, err)
			}
		})
	}
}
func TestNetappInventoryRejectsIncompleteEvidence(t *testing.T) {
	for _, mode := range []string{"parent404", "own403", "collection404", "filter", "crosshost", "cycle", "duplicate", "name", "region", "reference", "source", "foreign known", "cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newNetappFixture(t)
			req := netappRequest(f.runtime, netappVolumeType)
			req.Limit = 1
			first, err := f.runtime.List(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			id := first.Items[0].NativeID
			req.KnownNativeIDs = []string{id}
			switch mode {
			case "parent404":
				f.missing[redisParentID(id)] = true
				f.missing[id] = true
			case "own403":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, id) {
						return jsonResponse(403, nil, nil), true
					}
					return nil, false
				}
			case "collection404":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/volumes") {
						return jsonResponse(404, nil, nil), true
					}
					return nil, false
				}
			case "filter", "crosshost", "cycle":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if !strings.HasSuffix(strings.ToLower(q.URL.Path), "/volumes") {
						return nil, false
					}
					next := q.URL.String()
					if mode == "filter" {
						next += "&$filter=name%20eq%20hidden"
					}
					if mode == "crosshost" {
						next = strings.Replace(next, "management.azure.com", "foreign.example", 1)
					}
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
				}
			case "duplicate":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, redisParentID(id)+"/volumes") {
						return jsonResponse(200, map[string]any{"value": []any{f.objects[id], f.objects[id]}}, nil), true
					}
					return nil, false
				}
			case "name":
				f.objects[id]["name"] = "another"
			case "region":
				f.objects[id]["location"] = "elsewhere"
			case "reference":
				object(f.objects[id]["properties"])["subnetId"] = resourceID(vnetType, "wrong-type")
			case "source":
				req.Source = productInventorySource
			case "foreign known":
				req.KnownNativeIDs = []string{strings.Replace(id, testSubscription, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)}
			case "cursor":
				req.KnownNativeIDs = nil
				req.Cursor = first.NextCursor
				object(f.objects[id]["properties"])["futurePrivateField"] = "changed"
			}
			batch, err := f.runtime.List(t.Context(), req)
			if err == nil || isNotFound(err) || batch.Complete || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("unsafe shard", mode, batch, err)
			}
		})
	}
}
func TestNetappOriginalSourceEvidence(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Version  string                                     `json:"api_version"`
		Examples []struct{ File, SourceURI, SHA256 string } `json:"examples"`
	}
	if json.Unmarshal(wire, &source) != nil || source.Version != netappVersion || len(source.Examples) != 40 {
		t.Fatal("manifest")
	}
	for _, example := range source.Examples {
		wire, err := os.ReadFile("fixtures/netapp/" + example.File)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(wire)
		if hex.EncodeToString(sum[:]) != example.SHA256 {
			t.Fatal("source changed", example.File)
		}
	}
}
func TestNetappPrivateAPIDiagnostics(t *testing.T) {
	f := newNetappFixture(t)
	for id, raw := range f.objects {
		wire, _ := json.Marshal(safeAPIPayload(raw, apiURL(id, netappVersion)))
		if strings.Contains(string(wire), "netapp-private-canary") {
			t.Fatal("private field in API diagnostics", string(wire))
		}
	}
}
func TestNetappRegisteredWorkerPreservesFailedShards(t *testing.T) {
	f := newNetappFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, kind := range netappResources {
		kinds = append(kinds, kind.kind)
	}
	azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, false)
	values, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != 22 {
		t.Fatal("registered inventory", len(values), err)
	}
	var target asset.Asset
	for _, v := range values {
		wire, _ := json.Marshal(v)
		if strings.Contains(string(wire), "netapp-private-canary") {
			t.Fatal("private data persisted")
		}
		if v.Identity.NativeType == netappVolumeType+"/snapshots" && v.Location == "eastus" {
			target = v
		}
	}
	for id := range f.objects {
		f.hidden[id] = true
	}
	azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, false)
	f.missing[target.Identity.NativeID] = true
	parent := redisParentID(target.Identity.NativeID)
	f.missing[parent] = true
	azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{target.Identity.NativeType}, true, false)
	values, err = repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != 22 {
		t.Fatal("failed parent erased backup", len(values), err)
	}
	f.missing[parent] = false
	azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{target.Identity.NativeType}, false, false)
	values, err = repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != 21 {
		t.Fatal("own absence did not reconcile", len(values), err)
	}
}
func TestNetappNativeExampleRequestsBind(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Examples []struct{ File string } `json:"examples"`
	}
	if json.Unmarshal(wire, &manifest) != nil {
		t.Fatal("manifest")
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Examples {
		t.Run(entry.File, func(t *testing.T) {
			wire, err := os.ReadFile("fixtures/netapp/" + entry.File)
			if err != nil {
				t.Fatal(err)
			}
			var example struct {
				Parameters  map[string]any `json:"parameters"`
				OperationID string         `json:"operationId"`
			}
			if json.Unmarshal(wire, &example) != nil {
				t.Fatal("example")
			}
			operation, ok := metadata.catalog.Operation("Azure.Microsoft.NetApp." + example.OperationID)
			if !ok {
				t.Fatal("missing native operation", example.OperationID)
			}
			delete(example.Parameters, "api-version")
			// Preserve original examples while enforcing the authoritative
			// Swagger parameter contract. Five upstream examples contain an
			// undeclared selector/body; do not teach the binder to accept it.
			extra := map[string]string{"Accounts_ListBySubscription": "resourceGroupName", "SnapshotPolicies_ListVolumes": "body", "Subvolumes_ListByVolume": "subvolumeName", "VolumeQuotaRules_ListByVolume": "volumeQuotaRuleName", "Volumes_ReplicationStatus": "body"}[example.OperationID]
			if extra != "" {
				if _, exists := example.Parameters[extra]; !exists {
					t.Fatal("documented upstream mismatch changed")
				}
				if _, err := bindAzureREST(operation, example.Parameters); err == nil {
					t.Fatal("undeclared native parameter accepted")
				}
				delete(example.Parameters, extra)
			}
			request, err := bindAzureREST(operation, example.Parameters)
			if err != nil {
				t.Fatal("native example does not bind", err)
			}
			if request.Method == "DELETE" && strings.Contains(request.URL, "forceDelete=true") {
				t.Fatal("unrequested forced cascade")
			}
			if !strings.HasPrefix(request.URL, "https://management.azure.com/subscriptions/") || !strings.Contains(request.URL, "api-version="+netappVersion) {
				t.Fatal("native scope", request)
			}
			if example.OperationID == "Volumes_ListReplications" && (request.Method != "POST" || !strings.Contains(string(request.Body), "exclude")) {
				t.Fatal("replication protocol", request)
			}
		})
	}
}
