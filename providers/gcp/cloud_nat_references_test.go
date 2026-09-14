package gcp

import (
	"github.com/loomx-ai/steward/internal/core/asset"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const natHubA = "//networkconnectivity.googleapis.com/projects/sample-project/locations/global/hubs/hub-a"
const natHubForeign = "//networkconnectivity.googleapis.com/projects/foreign-project/locations/global/hubs/hub-b"

func natHubFixture(expression string) map[string]any {
	nat := cloudNatFixture("nat-a")
	nat["type"] = "PRIVATE"
	nat["rules"] = []any{map[string]any{"ruleNumber": 1, "match": expression}}
	return nat
}

func TestCloudNatHubCEL(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, tc := range []struct {
		name, expression string
		hubs             []string
		membership, fail bool
	}{
		{name: "absolute", expression: ".nexthop.hub == '" + natHubA + "'", hubs: []string{natHubA}},
		{name: "literal", expression: "nexthop.hub == '" + natHubA + "'", hubs: []string{natHubA}},
		{name: "reversed-raw", expression: "r'" + natHubA + "' == nexthop.hub", hubs: []string{natHubA}},
		{name: "triple", expression: "nexthop.hub == '''" + natHubA + "'''", hubs: []string{natHubA}},
		{name: "escaped-number", expression: `nexthop.hub == '\x2f/networkconnectivity.googleapis.com/projects/123456/locations/global/hubs/hub-a'`, hubs: []string{natHubA}},
		{name: "relative", expression: "nexthop.hub == 'projects/sample-project/locations/global/hubs/hub-a'", hubs: []string{natHubA}},
		{name: "https", expression: "nexthop.hub == 'https://networkconnectivity.googleapis.com/v1/projects/sample-project/locations/global/hubs/hub-a'", hubs: []string{natHubA}},
		{name: "or", expression: "nexthop.hub == '" + natHubA + "' || nexthop.hub == '" + natHubForeign + "' || nexthop.hub == '" + natHubA + "'", hubs: []string{natHubForeign, natHubA}},
		{name: "hybrid", expression: "nexthop.hub == '" + natHubA + "' || nexthop.is_hybrid", hubs: []string{natHubA}},
		{name: "index", expression: "nexthop['hub'] == '" + natHubA + "'", hubs: []string{natHubA}},
		{name: "bare", expression: "nexthop.hub", membership: true},
		{name: "computed", expression: "nexthop.hub == selectedHub", membership: true},
		{name: "mixed", expression: "nexthop.hub == '" + natHubA + "' || nexthop.hub", hubs: []string{natHubA}, membership: true},
		{name: "unequal", expression: "nexthop.hub != '" + natHubA + "'", membership: true},
		{name: "comment", expression: "true // nexthop.hub == '" + natHubA + "'"},
		{name: "string", expression: `"nexthop.hub" == "` + natHubA + `"`},
		{name: "other", expression: "other.hub == '" + natHubA + "'"},
		{name: "public", expression: "inIpRange(destination.ip, '10.0.0.0/8')"},
		{name: "malformed", expression: "nexthop.hub ==", fail: true},
		{name: "wrong-host", expression: "nexthop.hub == '//evil.example/projects/sample-project/locations/global/hubs/hub-a'", fail: true},
		{name: "wrong-location", expression: "nexthop.hub == '//networkconnectivity.googleapis.com/projects/sample-project/locations/us-central1/hubs/hub-a'", fail: true},
		{name: "query", expression: "nexthop.hub == '" + natHubA + "?a=1'", fail: true},
		{name: "traversal", expression: "nexthop.hub == '" + natHubA + "/../hub-b'", fail: true},
		{name: "encoded", expression: "nexthop.hub == '//networkconnectivity.googleapis.com/projects/sample-project/locations/global/hubs/%68ub-a'", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hubs, membership, err := c.cloudNatHubReferences(natHubFixture(tc.expression))
			if (err != nil) != tc.fail || !tc.fail && (!slices.Equal(hubs, tc.hubs) || membership != tc.membership) {
				t.Fatal(hubs, membership, err)
			}
		})
	}
}

func TestCloudNatHubNativeMembership(t *testing.T) {
	for _, mode := range []string{"paged", "empty", "hybrid", "foreign", "multiple", "denied", "partial", "null", "token-null", "unreachable-null", "cycle", "duplicate", "name", "network", "detail-denied", "detail-missing", "detail-drift", "membership-drift", "router-drift", "router-recreated", "cel-invalid"} {
		t.Run(mode, func(t *testing.T) {
			lists, details, routers := 0, 0, 0
			expression := "nexthop.hub"
			if mode == "hybrid" {
				expression = "nexthop.is_hybrid"
			}
			if mode == "cel-invalid" {
				expression = "nexthop.hub =="
			}
			spoke := func(name string) map[string]any {
				hub := natHubA
				if mode == "foreign" || name == "spoke-b" {
					hub = natHubForeign
				}
				return map[string]any{"name": "projects/sample-project/locations/global/spokes/" + name, "hub": strings.TrimPrefix(hub, "//networkconnectivity.googleapis.com/"), "uniqueId": "unique-" + name, "state": "ACTIVE", "linkedVpcNetwork": map[string]any{"uri": "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/network-a"}}
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal("mutation", req.Method, req.URL)
				}
				if req.URL.Host == "compute.googleapis.com" {
					if strings.HasSuffix(req.URL.Path, "/routers") {
						return dataformResponse(req, 200, map[string]any{"items": []any{cloudNatParent(req.URL.Path + "/router-a")}}), nil
					}
					if !strings.HasSuffix(req.URL.Path, "/router-a") {
						t.Fatal(req.URL)
					}
					routers++
					parent := cloudNatParent(req.URL.Path)
					parent["nats"] = []any{natHubFixture(expression)}
					if routers > 1 && mode == "router-drift" {
						parent["description"] = "changed"
					}
					if routers > 1 && mode == "router-recreated" {
						parent["id"] = "2000"
					}
					return dataformResponse(req, 200, parent), nil
				}
				if req.URL.Host != "networkconnectivity.googleapis.com" {
					t.Fatal(req.URL)
				}
				if strings.HasSuffix(req.URL.Path, "/spokes") {
					if req.URL.Path != "/v1/projects/sample-project/locations/-/spokes" || req.URL.Query().Get("filter") != "" {
						t.Fatal(req.URL)
					}
					lists++
					data := map[string]any{}
					switch mode {
					case "denied":
						return apiResponse(req, 403, `{}`), nil
					case "partial":
						data["unreachable"] = []any{"us-central1"}
					case "null":
						data["spokes"] = nil
					case "token-null":
						data["nextPageToken"] = nil
					case "unreachable-null":
						data["unreachable"] = nil
					case "empty":
					default:
						a := spoke("spoke-a")
						if mode == "name" {
							a["name"] = "projects/foreign-project/locations/global/spokes/spoke-a"
						}
						if mode == "network" {
							a["linkedVpcNetwork"] = "bad"
						}
						if mode == "membership-drift" && lists > 1 {
							a["hub"] = natHubForeign
						}
						data["spokes"] = []any{a}
						if mode == "duplicate" {
							data["spokes"] = []any{a, a}
						}
						if mode == "multiple" {
							data["spokes"] = []any{a, spoke("spoke-b")}
						}
						if mode == "cycle" {
							data["nextPageToken"] = "next"
						}
						if mode == "paged" && req.URL.Query().Get("pageToken") == "" {
							other := spoke("other")
							object(other["linkedVpcNetwork"])["uri"] = "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/other"
							data["spokes"] = []any{other, map[string]any{"name": "projects/sample-project/locations/us-central1/spokes/hybrid", "linkedVpnTunnels": map[string]any{"uris": []any{"tunnel"}}}}
							data["nextPageToken"] = "next"
						}
					}
					return dataformResponse(req, 200, data), nil
				}
				details++
				if mode == "detail-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "detail-missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := spoke(last(req.URL.Path))
				if mode == "detail-drift" {
					data["uniqueId"] = "recreated"
				}
				if mode == "membership-drift" && lists > 1 {
					data["hub"] = natHubForeign
				}
				return dataformResponse(req, 200, data), nil
			})
			batch, err := r.List(t.Context(), productRequest(r, cloudNatType, "us-central1"))
			success := slices.Contains([]string{"paged", "empty", "hybrid", "foreign", "multiple"}, mode)
			if !success {
				if err == nil || len(batch.Items) != 0 {
					t.Fatal("accepted incomplete membership", batch, err)
				}
				return
			}
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			want := []string{natHubA}
			if mode == "foreign" {
				want = []string{natHubForeign}
			}
			if mode == "multiple" {
				want = []string{natHubForeign, natHubA}
			}
			if mode == "empty" || mode == "hybrid" {
				want = nil
			}
			if got, _ := item.Normalized[referenceKey(cloudNatHubType)].([]string); !slices.Equal(got, want) {
				t.Fatal("wrong Hub targets", got, want)
			}
			for _, hub := range want {
				if !slices.Contains(item.NetworkReferences, hub) {
					t.Fatal(item.NetworkReferences)
				}
			}
			if firewallDigest(cloudNatConfiguration(item.Normalized)) != firewallDigest(natHubFixture(expression)) {
				t.Fatal("dependency enrichment changed native NAT review")
			}
			if mode == "hybrid" && (lists != 0 || details != 0 || routers != 1) || mode == "paged" && (lists != 4 || details != 2 || routers != 2) || mode == "empty" && (lists != 2 || details != 0 || routers != 2) {
				t.Fatal("incomplete or extra reads", lists, details, routers)
			}
		})
	}
}

func TestCloudNatHubGraphUsesCompleteNativeIdentity(t *testing.T) {
	for _, mode := range []string{"global", "missing", "foreign-project", "foreign-connection", "foreign-partition", "ambiguous", "unscanned-membership"} {
		t.Run(mode, func(t *testing.T) {
			nat := asset.Asset{ID: "nat", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: cloudNatType, ScopeKey: "region:us-central1"}, Normalized: natHubFixture("nexthop.hub == '" + natHubA + "'")}
			hub := asset.Asset{ID: "hub", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: cloudNatHubType, NativeID: natHubA, ScopeKey: "global:sample-project/global"}}
			switch mode {
			case "foreign-project":
				hub.Identity.NativeID = natHubForeign
			case "foreign-connection":
				hub.Identity.ConnectionID = "other"
			case "foreign-partition":
				hub.Identity.Partition = "other"
			case "unscanned-membership":
				nat.Normalized = natHubFixture("nexthop.hub")
			}
			values := []asset.Asset{nat, hub}
			if mode == "missing" {
				values = values[:1]
			}
			if mode == "ambiguous" {
				duplicate := hub
				duplicate.ID = "duplicate"
				duplicate.Identity.ScopeKey = "region:us-central1"
				values = append(values, duplicate)
			}
			result, err := NewCloudNatHubs().Contribute(t.Context(), "project", values)
			if mode == "ambiguous" || mode == "unscanned-membership" {
				if err == nil {
					t.Fatal("unverified Hub relationship accepted")
				}
				return
			}
			if err != nil || len(result.Bindings) != 0 {
				t.Fatal(result, err)
			}
			if mode == "global" {
				if len(result.Relationships) != 1 || result.Relationships[0].SourceAssetID != nat.ID || result.Relationships[0].TargetAssetID != hub.ID || len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Relationships) != 0 || len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != natHubA || result.Unresolved[0].BlocksCleanup {
				t.Fatal(result)
			}
		})
	}
}
