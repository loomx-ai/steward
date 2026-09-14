package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func storagePoolFixture(zone, name string) map[string]any {
	root := "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/" + zone
	return map[string]any{"kind": "compute#storagePool", "id": "9007199254740993", "name": name, "selfLink": root + "/storagePools/" + name, "zone": root, "state": "READY", "creationTimestamp": "2026-09-01T00:00:00Z", "storagePoolType": root + "/storagePoolTypes/hyperdisk-balanced", "poolProvisionedCapacityGb": "20480", "poolProvisionedIops": "100000", "poolProvisionedThroughput": "4096", "capacityProvisioningType": "ADVANCED", "performanceProvisioningType": "STANDARD", "labels": map[string]any{"team": "storage"}, "resourceStatus": map[string]any{"poolUsedCapacityBytes": "1099511627776", "poolUsedIops": "12000", "poolUsedThroughput": "512", "diskCount": "2"}}
}

func TestStoragePoolNativeInventoryPagingAndScope(t *testing.T) {
	for _, region := range []string{"project", "us-central1", "europe-west1"} {
		t.Run(region, func(t *testing.T) {
			first := storagePoolFixture("us-central1-a", "pool-a")
			second := storagePoolFixture("europe-west1-b", "pool-b")
			// Native old/new usage field names are both kept, without mistaking the
			// structured usage object for the pool creation state.
			second["status"] = second["resourceStatus"]
			delete(second, "resourceStatus")
			second["storagePoolType"] = "exapool"
			second["exapoolProvisionedCapacityGb"] = map[string]any{"hyperdiskBalancedCapacityGb": "20480"}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				for _, pool := range []map[string]any{first, second} {
					if strings.HasSuffix(text(pool["selfLink"]), req.URL.Path) {
						body, _ := json.Marshal(pool)
						return apiResponse(req, 200, string(body)), nil
					}
				}
				if response, ok := storagePoolInventoryResponse(req); ok {
					return response, nil
				}
				if req.Method != "GET" || req.URL.Path != "/compute/v1/projects/sample-project/aggregated/storagePools" || req.URL.Query().Get("includeAllScopes") != "true" || req.URL.Query().Get("maxResults") != "100" {
					t.Fatalf("unexpected native list %s %s", req.Method, req.URL)
				}
				body := map[string]any{"nextPageToken": "second"}
				if req.URL.Query().Get("pageToken") == "second" {
					body = map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{first}}, "zones/europe-west1-b": map[string]any{"storagePools": []any{second}}}}
				}
				encoded, _ := json.Marshal(body)
				return apiResponse(req, 200, string(encoded)), nil
			})
			request := productRequest(r, storagePoolType, region)
			page, err := r.List(t.Context(), request)
			if err != nil || page.Complete || len(page.Items) != 0 || page.NextCursor == "" {
				t.Fatal("empty page lost continuation", page, err)
			}
			request.Cursor = page.NextCursor
			page, err = r.List(t.Context(), request)
			expected := 1
			if region == "project" {
				expected = 2
			}
			if err != nil || !page.Complete || len(page.Items) != expected {
				t.Fatal("native scope", page, err)
			}
			for _, item := range page.Items {
				if item.State != "READY" || item.Normalized["id"] != "9007199254740993" || item.Normalized["poolProvisionedCapacityGb"] != "20480" || object(item.Normalized["pool_usage"])["diskCount"] != "2" || item.Tags["team"] != "storage" || item.Actionable == nil || *item.Actionable != (item.Normalized["storagePoolType"] != "exapool") {
					t.Fatal("native pool fields/capability lost", item)
				}
				if _, err := r.ResolveAction(t.Context(), "connection", asset.Asset{ID: "pool", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeID: item.NativeID, NativeType: storagePoolType}, Normalized: item.Normalized}); err != nil {
					t.Fatal("missing pool action driver", err)
				}
			}
			changed := request
			changed.Scope.NativeID = "changed"
			if _, err := r.List(t.Context(), changed); err == nil {
				t.Fatal("cursor changed scope")
			}
		})
	}
}

func TestStoragePoolNativeInventoryRejectsUncertainResults(t *testing.T) {
	for _, mode := range []string{"foreign-project", "foreign-host", "zone-mismatch", "scope-mismatch", "wrong-kind", "name-mismatch", "duplicate", "partial", "denied"} {
		t.Run(mode, func(t *testing.T) {
			raw := storagePoolFixture("us-central1-a", "pool")
			scope := "zones/us-central1-a"
			switch mode {
			case "foreign-project":
				raw["selfLink"] = strings.Replace(text(raw["selfLink"]), "sample-project", "other-project", 1)
			case "foreign-host":
				raw["selfLink"] = strings.Replace(text(raw["selfLink"]), "compute.googleapis.com", "example.invalid", 1)
			case "zone-mismatch":
				raw["zone"] = strings.Replace(text(raw["zone"]), "us-central1-a", "us-central1-b", 1)
			case "scope-mismatch":
				scope = "zones/us-central1-b"
			case "wrong-kind":
				raw["kind"] = "compute#disk"
			case "name-mismatch":
				raw["name"] = "different"
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if response, ok := storagePoolInventoryResponse(req); ok {
					return response, nil
				}
				if mode == "denied" {
					return apiResponse(req, 403, `{"error":{"code":403}}`), nil
				}
				rows := []any{raw}
				if mode == "duplicate" {
					rows = append(rows, raw)
				}
				group := map[string]any{"storagePools": rows}
				if mode == "partial" {
					group["warning"] = map[string]any{"code": "UNREACHABLE", "message": "scope incomplete"}
				}
				payload, _ := json.Marshal(map[string]any{"items": map[string]any{scope: group}})
				return apiResponse(req, 200, string(payload)), nil
			})
			page, err := r.List(t.Context(), productRequest(r, storagePoolType, "project"))
			if err == nil || len(page.Items) != 0 || page.Complete {
				t.Fatal("uncertain native pool inventory accepted", mode, page, err)
			}
		})
	}
}

func TestStoragePoolDiskReferencesAndNativeBindings(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	id := "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/storagePools/pool"
	for _, value := range []string{id, "https://www.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/storagePools/pool", "projects/sample-project/zones/us-central1-a/storagePools/pool", "zones/us-central1-a/storagePools/pool"} {
		got := references(c, map[string]any{"storagePool": value, "config": map[string]any{"storagePools": []any{value}}})
		if !slices.Equal(got[storagePoolType], []string{id}) {
			t.Fatal("native pool reference", value, got)
		}
	}
	for _, value := range []string{"https://untrusted.invalid/compute/v1/projects/sample-project/zones/us-central1-a/storagePools/pool", "projects/other/zones/us-central1-a/storagePools/pool", "projects/sample-project/regions/us-central1/storagePools/pool"} {
		if len(references(c, map[string]any{"storagePool": value})[storagePoolType]) != 0 {
			t.Fatal("foreign/invalid pool reference", value)
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"get", "delete", "listDisks"} {
		op, ok := metadata.catalog.Operation("compute.storagePools." + name)
		if !ok {
			t.Fatal("missing original method", name)
		}
		args := map[string]any{"project": "sample-project", "zone": "us-central1-a", "storagePool": "pool"}
		expected := "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/storagePools/pool"
		method := "GET"
		if name == "delete" {
			method = "DELETE"
		}
		if name == "listDisks" {
			expected += "/listDisks"
		}
		bound, err := catalog.BindREST(op, args)
		if err != nil || bound.Method != method || bound.URL != expected || op.SourceURI != "https://www.googleapis.com/discovery/v1/apis/compute/v1/rest" {
			t.Fatal("native pool binding", name, bound, err)
		}
	}
	// A disk keeps an ordinary dependency on its pool; no cascade is inferred.
	definition, ok := metadata.catalog.ResourceType(storagePoolType)
	if !ok || !slices.Equal(definition.REST.DeleteOperations, []string{"compute.storagePools.delete"}) {
		t.Fatal("native delete binding missing")
	}
}

// Default native member and own-read responses for inventory regression tests.
func storagePoolInventoryResponse(req *http.Request) (*http.Response, bool) {
	parts := strings.Split(req.URL.Path, "/")
	if req.Method != "GET" || len(parts) < 9 || parts[5] != "zones" || parts[7] != "storagePools" {
		return nil, false
	}
	data := storagePoolFixture(parts[6], parts[8])
	if len(parts) == 10 && parts[9] == "listDisks" {
		data = map[string]any{"kind": "compute#storagePoolListDisks", "items": []any{storagePoolDiskFixture(parts[6], parts[8]+"-disk")}}
	} else if len(parts) != 9 {
		return nil, false
	}
	body, _ := json.Marshal(data)
	return apiResponse(req, 200, string(body)), true
}

func storagePoolDiskFixture(zone, name string) map[string]any {
	root := "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/" + zone
	return map[string]any{"disk": root + "/disks/" + name, "name": name, "status": "READY", "type": root + "/diskTypes/hyperdisk-balanced", "sizeGb": "9007199254740993", "provisionedIops": "3000", "provisionedThroughput": "140", "usedBytes": "1099511627776", "creationTimestamp": "2026-09-01T00:00:00Z", "attachedInstances": []any{root + "/instances/vm"}, "resourcePolicies": []any{"https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/resourcePolicies/snapshots"}}
}
