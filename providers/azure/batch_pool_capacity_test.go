package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestBatchPoolIndexesCannotConcealAutoPools(t *testing.T) {
	for _, mode := range []string{"arm_missing", "data_missing", "data_duplicate", "foreign_url", "changed_detail", "unreadable_page"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := newBatchScenario(t)
			pool := s.records["/pools/poolid"]
			switch mode {
			case "arm_missing":
				s.arm.lists[s.account+"/pools"] = []any{}
			case "data_missing":
				s.lists["/pools"] = []any{}
			case "data_duplicate":
				s.lists["/pools"] = []any{pool, pool}
			case "foreign_url":
				pool["url"] = "https://foreign.japaneast.batch.azure.com/pools/poolid"
			case "changed_detail":
				s.lists["/pools"] = []any{batchClone(pool)}
				pool["vmSize"] = "changed"
			case "unreadable_page":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Host != "management.azure.com" && req.URL.Path == "/pools" {
						return jsonResponse(403, map[string]any{"code": "AuthorizationFailure"}, nil), true
					}
					return nil, false
				}
			}
			for _, kind := range []string{batchPoolType, batchNodeType} {
				if page, err := r.List(t.Context(), productRequest(r, kind)); err == nil || page.Complete {
					t.Fatal("incomplete Batch pool index was accepted", mode, kind, err)
				}
			}
			c, _ := r.resolve(t.Context(), "connection")
			account, _ := c.batchAccount(t.Context(), s.account)
			if _, err := c.batchTopology(t.Context(), account); err == nil {
				t.Fatal("incomplete pool index entered the lifecycle graph")
			}
		})
	}
}

func TestBatchSequentialNodeRemovalKeepsFreshPoolConditionAcrossCapacityChanges(t *testing.T) {
	for _, mode := range []string{"native_decrement", "concurrent_scale_down", "concurrent_scale_up", "other_setting", "new_node_incarnation"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			batchMPI(t, s, r, assets) // Three native nodes; this test deletes only nodes.
			page, err := r.List(t.Context(), productRequest(r, batchNodeType))
			if err != nil || len(page.Items) != 3 {
				t.Fatal(err)
			}
			planned := []asset.Asset{}
			for _, item := range page.Items {
				planned = append(planned, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: batchNodeType}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
			}
			fixed := object(object(object(s.arm.records[s.account+"/pools/poolid"]["properties"])["scaleSettings"])["fixedScale"])
			removed := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "POST" || !strings.HasSuffix(req.URL.Path, "/removenodes") {
					return nil, false
				}
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				node := text(array(body["nodeList"])[0])
				if len(array(body["nodeList"])) != 1 || body["nodeDeallocationOption"] != "requeue" || req.Header.Get("If-Match") != s.records["/pools/poolid"]["eTag"] {
					t.Fatal("node removal changed membership or native condition")
				}
				removed++
				s.gone["/pools/poolid/nodes/"+node] = true
				fixed["targetDedicatedNodes"] = 6 - removed
				s.records["/pools/poolid"]["eTag"] = "after-node-" + node
				return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, true
			}
			request := contracts.ActionRequest{Asset: planned[0], Action: "delete"}
			driver, _ := r.ResolveAction(t.Context(), "connection", request.Asset)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("first node removal", err)
			}
			if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			second := planned[1]
			switch mode {
			case "concurrent_scale_down":
				fixed["targetDedicatedNodes"] = 4
			case "concurrent_scale_up":
				fixed["targetDedicatedNodes"] = 7
			case "other_setting":
				object(s.arm.records[s.account+"/pools/poolid"]["properties"])["vmSize"] = "changed"
			case "new_node_incarnation":
				reads := 0
				original := s.handle
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && req.URL.Host != "management.azure.com" && req.URL.Path == "/pools/poolid" {
						reads++
						if reads == 3 {
							s.records[strings.TrimPrefix(second.Identity.NativeID, s.origin)]["allocationTime"] = "2026-09-10T13:00:00Z"
						}
					}
					return original(req)
				}
			}
			request = contracts.ActionRequest{Asset: second, Action: "delete"}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			driver, _ = r.ResolveAction(t.Context(), "connection", request.Asset)
			_, err = driver.Execute(t.Context(), request)
			if mode == "native_decrement" || strings.HasPrefix(mode, "concurrent_scale_") {
				if err != nil || removed != 2 {
					t.Fatal("reviewed second node could not follow native capacity decrement", err)
				}
			} else if err == nil || removed != 1 {
				t.Fatal("pool configuration or node incarnation drift was accepted", mode, err, removed)
			}
		})
	}
}
