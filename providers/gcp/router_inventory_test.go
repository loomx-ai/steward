package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
)

func routerInventoryFixture(path string) map[string]any {
	data := cloudNatParent(path)
	base := "https://www.googleapis.com" + strings.Split(path, "/routers/")[0]
	data["region"] = base
	data["description"] = "current-router"
	data["bgp"] = map[string]any{"asn": 64512, "advertiseMode": "CUSTOM", "advertisedIpRanges": []any{map[string]any{"range": "10.0.0.0/8"}}}
	data["bgpPeers"] = []any{map[string]any{"name": "peer-a", "interfaceName": "vpn", "peerAsn": 64513, "importPolicies": []any{"first", "second"}, "md5AuthenticationKeyName": "key"}}
	data["interfaces"] = []any{map[string]any{"name": "vpn", "linkedVpnTunnel": base + "/vpnTunnels/tunnel-a"}, map[string]any{"name": "vlan", "linkedInterconnectAttachment": base + "/interconnectAttachments/vlan-a"}}
	data["nats"] = []any{cloudNatFixture("nat-a"), cloudNatFixture("nat-b")}
	return data
}

func TestRouterNativeDetailPaginationAndReview(t *testing.T) {
	for _, scope := range []string{"us-central1", "project", "global", "network"} {
		t.Run(scope, func(t *testing.T) {
			gets := 0
			logs := []execution.JobLogEntry{}
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal(req.Method, req.URL)
				}
				if req.URL.Host == "cloudasset.googleapis.com" {
					return apiResponse(req, 200, `{"readTime":"2026-09-14T00:00:00Z"}`), nil
				}
				if strings.HasSuffix(req.URL.Path, "/regions") {
					return apiResponse(req, 200, `{"items":[{"name":"us-central1"}]}`), nil
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					name := "router-a"
					data := map[string]any{}
					if req.URL.Query().Get("pageToken") == "next" {
						name = "router-b"
					} else {
						data["nextPageToken"] = "next"
					}
					listed := cloudNatParent(req.URL.Path + "/" + name)
					listed["description"] = "stale-list"
					data["items"] = []any{listed}
					return dataformResponse(req, 200, data), nil
				}
				if strings.HasSuffix(req.URL.Path, "/router-a") || strings.HasSuffix(req.URL.Path, "/router-b") {
					gets++
					return dataformResponse(req, 200, routerInventoryFixture(req.URL.Path)), nil
				}
				t.Fatal(req.URL)
				return nil, nil
			})
			request := productRequest(r, routerType, scope)
			if scope == "network" {
				request = productRequest(r, routerType, "us-central1")
				request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: "//compute.googleapis.com/projects/sample-project/global/networks/network-a"}
			}
			seen := map[string]bool{}
			for pages := 0; ; pages++ {
				if pages > 4 {
					t.Fatal("nonterminating router scan")
				}
				batch, err := r.List(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					if seen[item.NativeID] || item.Normalized["description"] != "current-router" || text(item.Normalized[routerReview]) == "" || text(item.Normalized[routerBaseReview]) == "" {
						t.Fatal(item)
					}
					seen[item.NativeID] = true
					if len(array(item.Normalized["nats"])) != 2 || len(array(item.Normalized["interfaces"])) != 2 || !slices.Contains(item.NetworkReferences, "//compute.googleapis.com/projects/sample-project/global/networks/network-a") {
						t.Fatal(item)
					}
					raw, _ := json.Marshal(item)
					if strings.Contains(string(raw), "router-only-secret") {
						t.Fatal("Router secret persisted")
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := 2
			if scope == "global" {
				want = 0
			}
			if len(seen) != want || gets != want {
				t.Fatal(seen, gets)
			}
			raw, _ := json.Marshal(logs)
			if scope != "global" && len(logs) == 0 || strings.Contains(string(raw), "router-only-secret") {
				t.Fatal("Router secret escaped API logs")
			}
		})
	}
}

func TestRouterInventoryRejectsIncompleteOrChangedNativeDetail(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "list-id", "list-link", "id", "name", "self-link", "region", "partial", "pagination", "empty", "null-nats", "duplicate-nat", "bgp", "peers", "interfaces", "duplicate-interface", "interface-field", "interface-region", "keys", "duplicate-key", "key-field"} {
		t.Run(mode, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal(req.Method)
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					data := cloudNatParent(req.URL.Path + "/router-a")
					if mode == "list-id" {
						delete(data, "id")
					}
					if mode == "list-link" {
						data["selfLink"] = "https://www.googleapis.com/compute/v1/projects/foreign/regions/us-central1/routers/router-a"
					}
					return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
				}
				if mode == "denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := routerInventoryFixture(req.URL.Path)
				switch mode {
				case "id":
					data["id"] = "3000"
				case "name":
					data["name"] = "foreign"
				case "self-link":
					data["selfLink"] = "https://www.googleapis.com/compute/v1/projects/foreign/regions/us-central1/routers/router-a"
				case "region":
					data["region"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/europe-west1"
				case "partial":
					data["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
				case "pagination":
					data["nextPageToken"] = "unexpected"
				case "empty":
					data = map[string]any{}
				case "null-nats":
					data["nats"] = nil
				case "duplicate-nat":
					data["nats"] = []any{cloudNatFixture("nat-a"), cloudNatFixture("nat-a")}
				case "bgp":
					data["bgp"] = nil
				case "peers":
					data["bgpPeers"] = []any{false}
				case "interfaces":
					data["interfaces"] = nil
				case "duplicate-interface":
					data["interfaces"] = []any{map[string]any{"name": "same"}, map[string]any{"name": "same"}}
				case "interface-field":
					object(array(data["interfaces"])[0])["linkedVpnTunnel"] = false
				case "interface-region":
					object(array(data["interfaces"])[0])["linkedVpnTunnel"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/europe-west1/vpnTunnels/tunnel-a"
				case "keys":
					data["md5AuthenticationKeys"] = false
				case "duplicate-key":
					data["md5AuthenticationKeys"] = []any{map[string]any{"name": "same"}, map[string]any{"name": "same"}}
				case "key-field":
					object(array(data["md5AuthenticationKeys"])[0])["key"] = false
				}
				return dataformResponse(req, 200, data), nil
			})
			batch, err := r.List(t.Context(), productRequest(r, routerType, "us-central1"))
			if err == nil || len(batch.Items) != 0 {
				t.Fatal("unsafe Router observation", batch, err)
			}
		})
	}
}

func TestRouterReviewRetainsNativeUnknownsAndOrderedPolicyMeaning(t *testing.T) {
	original := routerInventoryFixture("/compute/v1/projects/sample-project/regions/us-central1/routers/router-a")
	for _, mode := range []string{"same", "reorder", "output", "nat", "policy-order", "interface", "key", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			data := roundTripDataformJSON(t, original)
			switch mode {
			case "reorder":
				slices.Reverse(array(data["nats"]))
				slices.Reverse(array(data["interfaces"]))
			case "output":
				data["creationTimestamp"] = "2026-09-14T00:00:00Z"
				object(array(data["nats"])[0])["effectiveTcpTimeWaitTimeoutSec"] = 60
			case "nat":
				object(array(data["nats"])[0])["minPortsPerVm"] = 256
			case "policy-order":
				slices.Reverse(array(object(array(data["bgpPeers"])[0])["importPolicies"]))
			case "interface":
				object(array(data["interfaces"])[0])["ipRange"] = "169.254.1.1/30"
			case "key":
				object(array(data["md5AuthenticationKeys"])[0])["key"] = "changed-secret"
			case "unknown":
				data["futureNativeSetting"] = map[string]any{"enabled": true}
			}
			fullSame := routerConfiguration(original, false) == routerConfiguration(data, false)
			baseSame := routerConfiguration(original, true) == routerConfiguration(data, true)
			wantFull := mode == "same" || mode == "reorder" || mode == "output"
			wantBase := wantFull || mode == "nat" || mode == "policy-order"
			if fullSame != wantFull || baseSame != wantBase {
				t.Fatal(mode, fullSame, baseSame)
			}
		})
	}
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	source := map[string]any{}
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("schema source")
	}
	schemas := object(object(object(array(source["documents"])[0])["document"])["schemas"])
	schema, err := discoveryFixtureSchemaCompiler(t, schemas).Compile("https://fixture.test/infra-manager.json#/definitions/Router")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(roundTripDataformJSON(t, original)); err != nil {
		t.Fatal(err)
	}
	// Body and response use the same sanitizer, including nested Invoke payloads.
	safe, _ := json.Marshal(safePayload(map[string]any{"body": original}))
	if strings.Contains(string(safe), "router-only-secret") {
		t.Fatal("MD5 key escaped native body redaction")
	}
}

func TestRouterSQLiteFailureAbsenceAndRecovery(t *testing.T) {
	testRouterComponentSQLiteRecovery(t, routerType)
}

func TestRouterNativeRequestRedactsMD5WithoutChangingWireData(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	body := map[string]any{"md5AuthenticationKeys": []any{map[string]any{"name": "key-a", "key": "router-only-secret"}}}
	c := &client{project: "sample-project", http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		wire, err := io.ReadAll(req.Body)
		if err != nil || req.Method != "PATCH" || !strings.Contains(string(wire), "router-only-secret") {
			t.Fatal("sanitizer changed native request", err)
		}
		return dataformResponse(req, 200, body), nil
	})}}
	encodedBody, _ := json.Marshal(body)
	result, err := c.requestResult(ctx, "PATCH", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/routers/router-a", nil, encodedBody)
	if err != nil || object(array(result.Data["md5AuthenticationKeys"])[0])["key"] != "router-only-secret" {
		t.Fatal("sanitizer changed native response", err)
	}
	encoded, _ := json.Marshal(logs)
	if len(logs) != 2 || strings.Contains(string(encoded), "router-only-secret") {
		t.Fatal("Router MD5 secret entered request/response logs")
	}
}
