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

func TestResourceGroupInternalMonitorReferences(t *testing.T) {
	for _, kind := range monitorIncomingKinds(monitorActionGroupType) {
		t.Run(kind, func(t *testing.T) { testResourceGroupMonitorReferences(t, kind, "complete") })
	}
	t.Run("disk-scope", func(t *testing.T) { testResourceGroupMonitorReferences(t, monitorActivityAlertType, "disk-scope") })
	t.Run("group-scope", func(t *testing.T) { testResourceGroupMonitorReferences(t, monitorActivityAlertType, "group-scope") })
}

func TestResourceGroupMonitorReferenceBoundaries(t *testing.T) {
	for _, mode := range []string{"external", "external-reviewed", "unreviewed", "retained", "changed-after-index", "changed-at-product", "late-external", "reference-proof", "source-forbidden", "source-remains", "standalone"} {
		t.Run(mode, func(t *testing.T) { testResourceGroupMonitorReferences(t, monitorMetricAlertType, mode) })
	}
}

func testResourceGroupMonitorReferences(t *testing.T, kind, mode string) {
	t.Helper()
	f := newMonitorInventoryFixture(t, monitorActionGroupType)
	targetRaw := f.objects[slices.Sorted(maps.Keys(f.objects))[0]]
	groupID := "/subscriptions/" + testSubscription + "/resourcegroups/reference-group"
	targetID := groupID + "/providers/" + strings.ToLower(monitorActionGroupType) + "/target"
	targetRaw["id"], targetRaw["name"] = targetID, "target"
	clear(f.objects)
	clear(f.groups)
	f.objects[targetID] = targetRaw
	groupRaw := map[string]any{"id": groupID, "type": groupType, "name": "reference-group", "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.groups[groupID] = groupRaw
	sourceRaw := monitorSourceToActionGroup(t, kind, targetID)
	sourceID := groupID + "/providers/" + strings.ToLower(kind) + "/source"
	sourceRaw["id"], sourceRaw["name"] = sourceID, "source"
	if mode == "external" || mode == "external-reviewed" {
		externalGroup := strings.Replace(groupID, "reference-group", "external-group", 1)
		sourceID = strings.Replace(sourceID, groupID, externalGroup, 1)
		sourceRaw["id"] = sourceID
		externalRaw := maps.Clone(groupRaw)
		externalRaw["id"] = externalGroup
		externalRaw["name"] = "external-group"
		f.groups[externalGroup] = externalRaw
	}
	diskID := groupID + "/providers/microsoft.compute/disks/disk"
	diskRaw := map[string]any{"id": diskID, "type": diskType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "uniqueId": "original-disk"}}
	if mode == "disk-scope" {
		object(sourceRaw["properties"])["scopes"] = []any{diskID}
	}
	if mode == "group-scope" {
		object(sourceRaw["properties"])["scopes"] = []any{groupID}
	}
	f.otherObjects[sourceID] = sourceRaw
	deletes, nativeLists := 0, 0
	active, gone := false, false
	f.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != groupID {
				t.Fatal("unexpected independent deletion", q.URL)
			}
			deletes++
			gone = true
			clear(f.objects)
			clear(f.otherObjects)
			if mode == "source-remains" {
				f.otherObjects[sourceID] = sourceRaw
			}
			delete(f.groups, groupID)
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if path == groupID+"/resources" {
			rows := []any{}
			if mode == "disk-scope" {
				rows = append(rows, diskRaw)
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if mode == "disk-scope" && path == diskID {
			if gone {
				return jsonResponse(404, map[string]any{}, nil), true
			}
			return jsonResponse(200, diskRaw, nil), true
		}
		if active && path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(kind) {
			nativeLists++
			if mode == "late-external" && nativeLists >= 3 {
				externalGroup := strings.Replace(groupID, "reference-group", "late-group", 1)
				externalID := strings.Replace(sourceID, groupID, externalGroup, 1)
				externalRaw := maps.Clone(sourceRaw)
				externalRaw["id"] = externalID
				f.otherObjects[externalID] = externalRaw
				externalGroupRaw := maps.Clone(groupRaw)
				externalGroupRaw["id"] = externalGroup
				externalGroupRaw["name"] = "late-group"
				f.groups[externalGroup] = externalGroupRaw
			}
			if mode == "changed-after-index" && nativeLists >= 3 || mode == "changed-at-product" && nativeLists >= 5 {
				object(sourceRaw["properties"])["enabled"] = false
			}
		}
		if active && path == sourceID && mode == "source-forbidden" {
			return jsonResponse(403, map[string]any{}, nil), true
		}
		return nil, false
	}
	target, source := f.asset(t, targetID), f.asset(t, sourceID)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.runtime.inventoryItem(t.Context(), c, groupRaw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	group := asset.Asset{ID: "group", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: groupID, NativeType: groupType}, Location: "global", Normalized: item.Normalized}
	req := contracts.ActionRequest{Asset: group, Action: "delete", IdempotencyKey: "group-reference-job", LifecycleImpacts: []contracts.ActionImpact{{Asset: target, ControllerID: group.ID, Delete: true}, {Asset: source, ControllerID: group.ID, Delete: true}}}
	if mode == "disk-scope" {
		disk := actionAsset(diskType, "disk")
		disk.Identity.NativeID = diskID
		disk.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(diskRaw)}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: disk, ControllerID: group.ID, Delete: true})
	}
	if mode == "external" || mode == "unreviewed" {
		req.LifecycleImpacts = req.LifecycleImpacts[:1]
	}
	if mode == "retained" {
		req.LifecycleImpacts[1].Delete = false
	}
	if mode == "reference-proof" {
		source.Normalized[monitorReferencesProof] = "forged"
	}
	active = true
	before, _ := json.Marshal(req)
	if mode == "standalone" {
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
		if err != nil {
			t.Fatal(err)
		}
		standalone := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "standalone"}
		for pass := 0; pass < 2; pass++ {
			if _, err := driver.Execute(t.Context(), standalone); err == nil || deletes != 0 {
				t.Fatal("group scope leaked to standalone Execute", err)
			}
			if err := f.runtime.resourceGroupPreflight(t.Context(), req); err != nil {
				t.Fatal("valid group preflight", err)
			}
		}
	}
	result, err := f.runtime.resourceGroupStartDelete(t.Context(), req)
	accepts := mode == "complete" || mode == "group-scope" || mode == "disk-scope" || mode == "source-remains" || mode == "standalone"
	if !accepts {
		if err == nil || deletes != 0 || result.Data != nil {
			t.Fatal("unverified reference authorized cascade", err, deletes)
		}
		if (mode == "changed-after-index" || mode == "changed-at-product") && !strings.Contains(err.Error(), "resource_group_reference_source_changed") {
			t.Fatal("did not exercise stable-index comparison", err)
		}
		return
	}
	if err != nil || deletes != 1 {
		t.Fatal("reviewed internal reference blocked native cascade", err, deletes)
	}
	saved := groupOperationJSON(t, result.Data)
	if mode == "source-remains" {
		out, err := f.runtime.resourceGroupResumeDeletion(t.Context(), req, saved)
		if out.Done {
			t.Fatal("native group absence hid surviving reference", out, err)
		}
		clear(f.otherObjects)
	}
	for range 2 {
		out, err := f.runtime.resourceGroupResumeDeletion(t.Context(), req, saved)
		if err != nil || !out.Done {
			t.Fatal("native reference recovery incomplete", out, err)
		}
		saved = groupOperationJSON(t, out.Data)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("preflight changed reviewed request")
	}
	if deletes != 1 || len(f.deletes) != 0 {
		t.Fatal("repeated independent deletion")
	}
}
