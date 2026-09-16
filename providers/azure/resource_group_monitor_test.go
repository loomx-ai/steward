package azure

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceGroupMonitorNativeCascade(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		t.Run(kind, func(t *testing.T) { testResourceGroupMonitorNativeCascade(t, kind, "complete") })
	}
}
func TestResourceGroupMonitorNativeFailures(t *testing.T) {
	for _, mode := range []string{"listed", "mixed_paged", "late_member", "unreviewed", "native_omitted", "native_forbidden", "own_forbidden", "changed", "group_changed", "returned", "readback_forbidden", "invalid_receipt", "generic_duplicate", "generic_wrong_type"} {
		t.Run(mode, func(t *testing.T) { testResourceGroupMonitorNativeCascade(t, monitorConsumptionBudgetType, mode) })
	}
}
func testResourceGroupMonitorNativeCascade(t *testing.T, kind, mode string) {
	t.Helper()
	f := newMonitorInventoryFixture(t, kind)
	oldID := slices.Sorted(maps.Keys(f.objects))[0]
	raw := f.objects[oldID]
	groupID := "/subscriptions/" + testSubscription + "/resourcegroups/native-group"
	id := groupID + "/providers/" + strings.ToLower(kind) + "/member"
	raw["id"], raw["name"] = id, "member"
	clear(f.objects)
	f.objects[id] = raw
	if mode == "mixed_paged" {
		second := maps.Clone(raw)
		second["id"] = id + "-second"
		second["name"] = "member-second"
		f.objects[id+"-second"] = second
	}
	clear(f.groups)
	groupRaw := map[string]any{"id": groupID, "type": groupType, "name": "native-group", "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.groups[groupID] = groupRaw
	budget, _ := monitorBudgetKind(kind)
	f.groupOnly = budget != ""
	nativeCollection := f.collection
	if budget != "" {
		nativeCollection = groupID + "/providers/" + strings.ToLower(kind)
	}
	active, gone := false, false
	groupDeletes, genericLists, nativeLists, ownReads := 0, 0, 0, 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != groupID || q.URL.Query().Get("api-version") != resourcesVersion {
				t.Fatal("expected only native group delete", q.URL)
			}
			groupDeletes++
			gone = true
			clear(f.objects)
			delete(f.groups, groupID)
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if !active {
			return nil, false
		}
		if path == groupID+"/resources" {
			genericLists++
			rows := []any{} // Deliberately omitted from ARM: native indexes establish membership.
			if mode == "listed" || mode == "mixed_paged" {
				rows = []any{raw}
			}
			if mode == "generic_duplicate" {
				rows = []any{raw, raw}
			}
			if mode == "generic_wrong_type" {
				other := maps.Clone(raw)
				other["type"] = diskType
				rows = []any{other}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if path == nativeCollection {
			nativeLists++
			if mode == "late_member" && nativeLists >= 3 {
				other := maps.Clone(raw)
				other["id"] = id + "-new"
				other["name"] = "member-new"
				f.objects[id+"-new"] = other
			}
			if mode == "native_forbidden" {
				return jsonResponse(403, map[string]any{}, nil), true
			}
			if mode == "native_omitted" {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		if path == id {
			ownReads++
			if mode == "own_forbidden" || gone && mode == "readback_forbidden" {
				return jsonResponse(403, map[string]any{}, nil), true
			}
			if gone && mode == "returned" {
				return jsonResponse(200, raw, nil), true
			}
		}
		if mode == "changed" && nativeLists > 1 {
			object(raw["properties"])["amount"] = 123456789
		}
		if mode == "group_changed" && genericLists > 0 {
			groupRaw["tags"] = map[string]any{"changed": "yes"}
		}
		return nil, false
	}
	member := f.asset(t, id)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.runtime.inventoryItem(t.Context(), c, groupRaw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := asset.Asset{ID: "group", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: groupID, NativeType: groupType}, Location: "global", Normalized: item.Normalized}
	req := contracts.ActionRequest{Asset: group, Action: "delete", IdempotencyKey: "native-monitor-group", LifecycleImpacts: []contracts.ActionImpact{{Asset: member, ControllerID: group.ID, Delete: true}}}
	if mode == "mixed_paged" {
		second := f.asset(t, id+"-second")
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: second, ControllerID: group.ID, Delete: true})
	}
	if mode == "unreviewed" {
		req.LifecycleImpacts = nil
	}
	active = true
	before, _ := json.Marshal(req)
	result, err := f.runtime.resourceGroupStartDelete(t.Context(), req)
	accepts := mode == "listed" || mode == "mixed_paged" || mode == "complete" || mode == "returned" || mode == "readback_forbidden" || mode == "invalid_receipt"
	if !accepts {
		if err == nil || groupDeletes != 0 || result.Data != nil {
			t.Fatal("unverified native Monitor scope accepted", err, groupDeletes)
		}
		return
	}
	if err != nil || groupDeletes != 1 || genericLists != 2 || nativeLists < 4 || ownReads < 2 {
		t.Fatal("native Monitor cascade unavailable", err, groupDeletes, genericLists, nativeLists, ownReads)
	}
	saved := groupOperationJSON(t, result.Data)
	readsBefore := ownReads
	if mode == "invalid_receipt" {
		saved["binding"] = "forged"
	}
	for step := 0; step < 2; step++ {
		out, err := f.runtime.resourceGroupResumeDeletion(t.Context(), req, saved)
		if mode == "readback_forbidden" || mode == "invalid_receipt" {
			if err == nil || out.Done || out.Data != nil {
				t.Fatal("failed readback reported complete", out, err)
			}
			if mode == "invalid_receipt" && ownReads != readsBefore {
				t.Fatal("untrusted receipt read products")
			}
			break
		}
		if err != nil || out.Done != (mode == "complete" || mode == "listed" || mode == "mixed_paged") {
			t.Fatal("native Monitor readback mismatch", out, err)
		}
		saved = groupOperationJSON(t, out.Data)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) || groupDeletes != 1 || len(f.deletes) != 0 {
		t.Fatal("group cascade changed plan or repeated product deletion")
	}
}
