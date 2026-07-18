package topology_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/topology"
)

func TestFocusKeyRoundTripsOpaqueIdentifiers(t *testing.T) {
	t.Parallel()

	if got := topology.AccountGlobalFocusKey(); got != "account-global" {
		t.Fatalf("account global key = %q", got)
	}
	account, err := topology.ParseFocusKey(topology.AccountGlobalFocusKey())
	if err != nil || account != (topology.Focus{Kind: topology.FocusAccountGlobal}) {
		t.Fatalf("account global focus = %+v, err = %v", account, err)
	}

	for _, id := range []string{"cn:hangzhou", "cn/hangzhou", "cn hangzhou", "华东 1"} {
		region, err := topology.ParseFocusKey(topology.RegionFocusKey(id))
		if err != nil || region != (topology.Focus{Kind: topology.FocusRegion, RegionID: id}) {
			t.Fatalf("region %q = %+v, err = %v", id, region, err)
		}
		public, err := topology.ParseFocusKey(topology.RegionPublicFocusKey(id))
		if err != nil || public != (topology.Focus{Kind: topology.FocusRegionPublic, RegionID: id}) {
			t.Fatalf("region public %q = %+v, err = %v", id, public, err)
		}
		vpcID := "vpc:" + id + "/应用"
		vpc, err := topology.ParseFocusKey(topology.VPCFocusKey(id, vpcID))
		if err != nil || vpc != (topology.Focus{Kind: topology.FocusVPC, RegionID: id, VPCID: vpcID}) {
			t.Fatalf("vpc %q/%q = %+v, err = %v", id, vpcID, vpc, err)
		}
	}
}

func TestFocusKeyRejectsMalformedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		topology.RegionFocusKey(""),
		topology.RegionPublicFocusKey(""),
		topology.VPCFocusKey("", "vpc-a"),
		topology.VPCFocusKey("cn-hangzhou", ""),
		"region:not-base64!",
		"unknown:YQ",
		"region:YQ:extra",
		"vpc:YQ",
		"vpc:YQ:Yg:extra",
	} {
		if _, err := topology.ParseFocusKey(value); err == nil {
			t.Fatalf("ParseFocusKey(%q) succeeded", value)
		}
	}
}

func TestTopologyResponseJSONUsesDiscriminatedViews(t *testing.T) {
	t.Parallel()

	responses := []struct {
		want string
		view topology.View
	}{
		{want: "account", view: topology.AccountView{Kind: topology.ViewAccount, Regions: []topology.EntrySummary{}}},
		{want: "region", view: topology.RegionView{Kind: topology.ViewRegion, VPCs: []topology.EntrySummary{}}},
		{want: "resource_graph", view: topology.ResourceGraphView{Kind: topology.ViewResourceGraph, Resources: []topology.Resource{}, Edges: []topology.ResourceEdge{}}},
		{want: "vpc", view: topology.VPCView{Kind: topology.ViewVPC, PublicResourceKeys: []string{}, VSwitches: []topology.VSwitch{}, Resources: []topology.Resource{}, Edges: []topology.ResourceEdge{}}},
	}
	for _, test := range responses {
		encoded, err := json.Marshal(topology.Response{View: test.view})
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := json.Unmarshal(encoded, &document); err != nil {
			t.Fatal(err)
		}
		view := document["view"].(map[string]any)
		if view["kind"] != test.want {
			t.Fatalf("view.kind = %#v, want %q: %s", view["kind"], test.want, encoded)
		}
		if strings.Contains(string(encoded), `"parent_key"`) {
			t.Fatalf("response contains removed parent field: %s", encoded)
		}
		for _, forbidden := range []string{"nodes", "has_children", "group_values"} {
			if strings.Contains(string(encoded), `"`+forbidden+`"`) {
				t.Fatalf("response contains legacy field %q: %s", forbidden, encoded)
			}
		}
	}
}
