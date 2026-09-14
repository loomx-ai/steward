package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestStoragePoolMemberInventoryPagingAndNetwork(t *testing.T) {
	calls := []string{}
	pool := storagePoolFixture("us-central1-a", "pool")
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatal("member inventory wrote", req.Method)
		}
		calls = append(calls, req.URL.RequestURI())
		var data map[string]any
		switch {
		case strings.HasSuffix(req.URL.Path, "/aggregated/storagePools"):
			data = map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{pool}}}}
		case strings.HasSuffix(req.URL.Path, "/listDisks"):
			if req.URL.Query().Get("maxResults") != "500" || req.URL.Query().Get("filter") != "" || req.URL.Query().Get("returnPartialSuccess") != "" {
				t.Fatal("member query changed", req.URL)
			}
			data = map[string]any{"kind": "compute#storagePoolListDisks", "nextPageToken": "members", "warning": map[string]any{"code": "NO_RESULTS_ON_PAGE"}}
			if req.URL.Query().Get("pageToken") == "members" {
				data = map[string]any{"kind": "compute#storagePoolListDisks", "items": []any{storagePoolDiskFixture("us-central1-a", "b"), storagePoolDiskFixture("us-central1-a", "a")}}
			}
		case strings.HasSuffix(req.URL.Path, "/storagePools/pool"):
			data = storagePoolFixture("us-central1-a", "pool")
			data["resourceStatus"] = map[string]any{"diskCount": "3"} // Utilization isn't creation identity.
		default:
			t.Fatal("unexpected request", req.URL)
		}
		encoded, _ := json.Marshal(data)
		return apiResponse(req, 200, string(encoded)), nil
	})
	batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || len(calls) != 4 {
		t.Fatal("native member inventory", batch, err, calls)
	}
	item := batch.Items[0]
	members, ok := item.Normalized["storage_pool_disks"].([]any)
	if !ok || len(members) != 2 || object(members[0])["name"] != "a" || object(members[0])["sizeGb"] != "9007199254740993" || object(members[0])["usedBytes"] != "1099511627776" {
		t.Fatal("native members lost", item)
	}
	if len(object(item.Raw["storagePoolDisks"])["items"].([]any)) != 2 || item.Normalized[referenceKey("compute.googleapis.com/ResourcePolicy")] != nil {
		t.Fatal("summaries became direct pool dependencies", item)
	}
	diskID := "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/a"
	if !slices.Contains(item.NetworkReferences, diskID) {
		t.Fatal("member disk network reference missing", item)
	}
	target := asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: "network"}
	filtered := inventory.FilterNetworkClosure(target, []contracts.InventoryItem{item, {NativeID: diskID, NetworkReferences: []string{"network"}}})
	if len(filtered) != 2 {
		t.Fatal("pool did not follow its member into network scan", filtered)
	}
	// A regional scan must not demand member permissions for pools outside it.
	calls = nil
	batch, err = r.List(t.Context(), productRequest(r, storagePoolType, "europe-west1"))
	if err != nil || len(batch.Items) != 0 || len(calls) != 1 {
		t.Fatal("queried out-of-scope members", batch, err, calls)
	}
}

func TestStoragePoolMemberInventoryRejectsIncompleteOrChangedData(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "partial", "warning", "wrong-kind", "missing-kind", "null-items", "object-items", "bad-row", "missing-disk", "wrong-name", "wrong-zone", "empty-project", "foreign-host", "query-url", "regional-disk", "duplicate", "duplicate-pages", "token-cycle", "invalid-token", "null-token", "late-denied", "parent-denied", "parent-missing", "parent-replaced", "parent-created-at", "parent-self-link", "parent-zone"} {
		t.Run(mode, func(t *testing.T) {
			pageCalls := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				var data map[string]any
				switch {
				case strings.HasSuffix(req.URL.Path, "/aggregated/storagePools"):
					data = map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{storagePoolFixture("us-central1-a", "pool")}}}}
				case strings.HasSuffix(req.URL.Path, "/listDisks"):
					pageCalls++
					if pageCalls > 3 {
						t.Fatal("member pagination did not stop")
					}
					if mode == "denied" || mode == "late-denied" && pageCalls == 2 {
						return apiResponse(req, 403, `{"error":{"code":403}}`), nil
					}
					if mode == "missing" {
						return apiResponse(req, 404, `{"error":{"code":404}}`), nil
					}
					row := storagePoolDiskFixture("us-central1-a", "a")
					data = map[string]any{"kind": "compute#storagePoolListDisks", "items": []any{row}}
					switch mode {
					case "partial":
						data["unreachables"] = []any{"zone"}
					case "warning":
						data["warning"] = map[string]any{"code": "UNREACHABLE"}
					case "wrong-kind":
						data["kind"] = "compute#diskList"
					case "missing-kind":
						delete(data, "kind")
					case "null-items":
						data["items"] = nil
					case "object-items":
						data["items"] = map[string]any{}
					case "bad-row":
						data["items"] = []any{42}
					case "missing-disk":
						delete(row, "disk")
					case "wrong-name":
						row["name"] = "other"
					case "wrong-zone":
						row["disk"] = strings.Replace(text(row["disk"]), "us-central1-a", "us-central1-b", 1)
					case "empty-project":
						row["disk"] = strings.Replace(text(row["disk"]), "sample-project", "", 1)
					case "foreign-host":
						row["disk"] = strings.Replace(text(row["disk"]), "compute.googleapis.com", "example.invalid", 1)
					case "query-url":
						row["disk"] = text(row["disk"]) + "?other=1"
					case "regional-disk":
						row["disk"] = strings.Replace(text(row["disk"]), "zones/us-central1-a", "regions/us-central1", 1)
					case "duplicate":
						data["items"] = []any{row, row}
					case "duplicate-pages", "late-denied":
						if pageCalls == 1 {
							data["nextPageToken"] = "next"
						}
					case "token-cycle":
						data["nextPageToken"] = "same"
					case "invalid-token":
						data["nextPageToken"] = 42
					case "null-token":
						data["nextPageToken"] = nil
					}
				case strings.HasSuffix(req.URL.Path, "/storagePools/pool"):
					if mode == "parent-denied" {
						return apiResponse(req, 403, `{"error":{"code":403}}`), nil
					}
					if mode == "parent-missing" {
						return apiResponse(req, 404, `{"error":{"code":404}}`), nil
					}
					data = storagePoolFixture("us-central1-a", "pool")
					switch mode {
					case "parent-replaced":
						data["id"] = "9007199254740994"
					case "parent-created-at":
						data["creationTimestamp"] = "2026-09-02T00:00:00Z"
					case "parent-self-link":
						data["selfLink"] = strings.Replace(text(data["selfLink"]), "/pool", "/other", 1)
					case "parent-zone":
						data["zone"] = strings.Replace(text(data["zone"]), "us-central1-a", "us-central1-b", 1)
					}
				default:
					t.Fatal("unexpected request", req.URL)
				}
				raw, _ := json.Marshal(data)
				return apiResponse(req, 200, string(raw)), nil
			})
			batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
			if err == nil || batch.Complete || len(batch.Items) != 0 {
				t.Fatal("uncertain membership accepted", mode, batch, err)
			}
		})
	}
}

func TestStoragePoolMemberInventoryEmptyNativeList(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/listDisks") {
			return apiResponse(req, 200, `{"kind":"compute#storagePoolListDisks"}`), nil
		}
		if response, ok := storagePoolInventoryResponse(req); ok {
			return response, nil
		}
		raw, _ := json.Marshal(map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{storagePoolFixture("us-central1-a", "pool")}}}})
		return apiResponse(req, 200, string(raw)), nil
	})
	batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	if rows, ok := batch.Items[0].Normalized["storage_pool_disks"].([]any); !ok || len(rows) != 0 {
		t.Fatal("empty native members", batch)
	}
}

func TestStoragePoolSharedProjectMembersStayWithinConnection(t *testing.T) {
	calls := 0
	foreign := storagePoolDiskFixture("us-central1-a", "shared-disk")
	foreign["disk"] = strings.Replace(text(foreign["disk"]), "sample-project", "consumer-project", 1)
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if strings.Contains(req.URL.Path, "consumer-project") {
			t.Fatal("crossed connection boundary", req.URL)
		}
		if strings.HasSuffix(req.URL.Path, "/listDisks") {
			data, _ := json.Marshal(map[string]any{"kind": "compute#storagePoolListDisks", "items": []any{foreign}})
			return apiResponse(req, 200, string(data)), nil
		}
		if response, ok := storagePoolInventoryResponse(req); ok {
			return response, nil
		}
		pool := storagePoolFixture("us-central1-a", "pool")
		pool["shareSettings"] = map[string]any{"projectMap": map[string]any{"consumer-project": map[string]any{"projectId": "consumer-project"}}}
		data, _ := json.Marshal(map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{pool}}}})
		return apiResponse(req, 200, string(data)), nil
	})
	batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
	if err != nil || len(batch.Items) != 1 || calls != 3 {
		t.Fatal("shared member inventory", batch, err, calls)
	}
	item := batch.Items[0]
	rows := item.Normalized["storage_pool_disks"].([]any)
	if len(rows) != 1 || object(rows[0])["disk"] != foreign["disk"] || len(item.NetworkReferences) != 0 || item.Normalized[referenceKey("compute.googleapis.com/Disk")] != nil || item.Normalized["shareSettings"] == nil {
		t.Fatal("shared membership boundary", item)
	}
}
