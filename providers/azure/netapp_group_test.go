package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func netappGroupFixture(t *testing.T) (*netappFixture, string, string) {
	f := newNetappFixture(t)
	f.override = func(q *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, q) }
	id := strings.ToLower(resourceID(netappAccountType, "first")) + "/volumegroups/item"
	volume := redisParentID(id) + "/capacitypools/item/volumes/item"
	object(f.objects[volume]["properties"])["volumeGroupName"] = "item"
	return f, id, volume
}
func TestNetappGroupNativeMembership(t *testing.T) {
	f, id, volume := netappGroupFixture(t)
	f.paged = true
	item := netappAssignmentItem(t, f, netappGroupType, nil)
	members := object(object(item.Normalized[netappGroupReview])["members"])
	if len(members) != 1 || object(members[volume])["uid"] != object(f.objects[volume]["properties"])["fileSystemId"] || item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protected"] != true {
		t.Fatal("group review and cleanup boundary", item)
	}
	wire, _ := json.Marshal(item)
	if strings.Contains(string(wire), "netapp-private-canary") {
		t.Fatal("private group/volume fields exposed")
	}
	known := map[string]map[string]any{id: item.Normalized}
	f.hidden[volume], f.hidden[redisParentID(volume)] = true, true
	again := netappAssignmentItem(t, f, netappGroupType, known)
	if len(object(object(again.Normalized[netappGroupReview])["members"])) != 1 {
		t.Fatal("own-live omitted member lost")
	}
	f.missing[volume] = true
	object(f.objects[id]["properties"])["volumes"] = []any{}
	object(object(f.objects[id]["properties"])["groupMetaData"])["volumesCount"] = 0
	gone := netappAssignmentItem(t, f, netappGroupType, known)
	if len(object(object(gone.Normalized[netappGroupReview])["members"])) != 0 {
		t.Fatal("own-absent member retained")
	}
	req := netappRequest(f.runtime, netappGroupType)
	req.KnownNativeIDs, req.KnownNativeMetadata = []string{id}, known
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.AbsentNativeIDs) != 0 {
		t.Fatal("group discovery reconciled volume", err)
	}
}
func TestNetappGroupIncompleteAndChangedReads(t *testing.T) {
	for _, fault := range []string{"member forbidden", "pool forbidden", "group forbidden", "member absent", "duplicate", "foreign account", "wrong type", "count", "missing list", "invalid name", "another group", "unlisted member", "unknown omitted hint", "recreated omitted", "embedded uuid", "own uuid", "region", "pool changed", "group changed", "late member", "foreign hint"} {
		t.Run(fault, func(t *testing.T) {
			f, id, volume := netappGroupFixture(t)
			item := netappAssignmentItem(t, f, netappGroupType, nil)
			req := netappRequest(f.runtime, netappGroupType)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			req.KnownNativeIDs, req.KnownNativeMetadata = []string{id}, map[string]map[string]any{id: item.Normalized}
			p := object(f.objects[id]["properties"])
			embedded := object(p["volumes"].([]any)[0])
			switch fault {
			case "duplicate":
				p["volumes"] = []any{embedded, embedded}
				object(p["groupMetaData"])["volumesCount"] = 2
			case "foreign account":
				embedded["id"] = strings.Replace(volume, "/first/", "/second/", 1)
			case "wrong type":
				embedded["type"] = netappPoolType
			case "count":
				object(p["groupMetaData"])["volumesCount"] = 0
			case "missing list":
				delete(p, "volumes")
			case "invalid name":
				object(f.objects[volume]["properties"])["volumeGroupName"] = "../item"
			case "another group":
				object(f.objects[volume]["properties"])["volumeGroupName"] = "other"
			case "unlisted member", "unknown omitted hint", "recreated omitted":
				p["volumes"] = []any{}
				object(p["groupMetaData"])["volumesCount"] = 0
				if fault == "unknown omitted hint" || fault == "recreated omitted" {
					delete(object(f.objects[volume]["properties"]), "volumeGroupName")
					f.hidden[volume] = true
				}
			case "embedded uuid":
				object(embedded["properties"])["fileSystemId"] = testTenant
			case "own uuid":
				object(f.objects[volume]["properties"])["fileSystemId"] = testTenant
			case "region":
				f.objects[volume]["location"] = "westus"
			case "foreign hint":
				object(object(req.KnownNativeMetadata[id][netappGroupReview])["members"])[strings.Replace(volume, "/first/", "/second/", 1)] = map[string]any{}
			}
			reads := map[string]int{}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				reads[path]++
				if fault == "member forbidden" && path == volume || fault == "pool forbidden" && path == redisParentID(volume) || fault == "group forbidden" && path == id {
					return jsonResponse(403, nil, nil), true
				}
				if fault == "recreated omitted" && path == volume && reads[path] == 1 {
					return jsonResponse(404, nil, nil), true
				}
				if fault == "member absent" && path == volume {
					return jsonResponse(404, nil, nil), true
				}
				if fault == "pool changed" && path == redisParentID(volume) && reads[path] == 2 {
					object(f.objects[path]["properties"])["size"] = 42
				}
				if fault == "group changed" && path == id && reads[path] == 2 {
					object(p["groupMetaData"])["groupDescription"] = "changed"
				}
				if fault == "late member" && path == redisParentID(volume)+"/volumes" && reads[path] == 2 {
					other := batchClone(f.objects[volume])
					other["id"], other["name"] = redisParentID(volume)+"/volumes/late", "late"
					object(other["properties"])["fileSystemId"] = testTenant
					f.objects[text(other["id"])] = other
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), req); err == nil {
				t.Fatal("incomplete group accepted", fault)
			}
		})
	}
}
func TestNetappGroupWorkerGraphAndFailedRescan(t *testing.T) {
	f, id, volume := netappGroupFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(values) != 22 {
		t.Fatal("full graph", len(values))
	}
	var group, member asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == id {
			group = v
		}
		if v.Identity.NativeID == volume {
			member = v
		}
	}
	rows, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.SourceAssetID == member.ID && row.TargetAssetID == group.ID && row.Evidence["native_membership"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("native group membership missing")
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing", "stale", "forged", "legacy", "extra", "duplicate"} {
		t.Run(fault, func(t *testing.T) {
			g, v := group, member
			g.Normalized = batchClone(group.Normalized)
			v.Normalized = batchClone(member.Normalized)
			all := []asset.Asset{v}
			switch fault {
			case "missing":
				all = nil
			case "stale":
				v.Normalized["_netapp_configuration"] = "stale"
			case "forged":
				object(g.Normalized[netappGroupReview])["members"] = map[string]any{}
			case "legacy":
				delete(g.Normalized, netappGroupProof)
			case "extra":
				extra := v
				extra.ID = "extra"
				extra.Identity.NativeID += "-extra"
				all = append(all, extra)
			case "duplicate":
				all = append(all, v)
			}
			out, err := c.netappGroupContribution(g, all)
			if fault == "duplicate" {
				if err == nil {
					t.Fatal("duplicate group member accepted")
				}
				return
			}
			if err != nil || len(out.Unresolved) == 0 || len(out.Bindings) != 0 {
				t.Fatal("unreviewed group graph", out, err)
			}
		})
	}
	for _, failed := range []string{volume, "/subscriptions/" + testSubscription + "/providers/microsoft.network/networkinterfaces"} {
		f.override = func(q *http.Request) (*http.Response, bool) {
			if strings.EqualFold(q.URL.Path, failed) {
				return jsonResponse(503, nil, nil), true
			}
			return fleetGraphEmptyIndexes(t, q)
		}
		after := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{netappGroupType}, true, true)
		if len(after) != len(values) {
			t.Fatal("failed group rescan removed resources")
		}
		for _, observed := range after {
			if observed.ID == group.ID && observed.Normalized[netappGroupProof] != group.Normalized[netappGroupProof] {
				t.Fatal("failed network read replaced group review")
			}
		}
	}
}
