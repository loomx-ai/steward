package azure

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
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
	if strings.HasPrefix(mode, "public") {
		targetRaw["properties"] = map[string]any{"groupShortName": "target", "enabled": true}
	}
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
	if mode == "group-scope" || strings.HasPrefix(mode, "public") {
		object(sourceRaw["properties"])["scopes"] = []any{groupID}
	}
	f.otherObjects[sourceID] = sourceRaw
	deletes, nativeLists, polls := 0, 0, 0
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
			status, header := 204, http.Header{}
			if mode == "public-async" {
				status = 202
				header.Set("Location", groupOperationEndpoint())
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if mode == "public-async" && strings.Contains(path, "/operationresults/") {
			polls++
			status := 202
			if polls > 1 {
				status = 200
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
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
	if strings.HasPrefix(mode, "public") {
		groupKind := f.runtime.resourceKind(groupType)
		listRequest := f.request()
		listRequest.ResourceKind = &groupKind
		batch, err := f.runtime.List(t.Context(), listRequest)
		if err != nil {
			t.Fatal("public group inventory", err)
		}
		found := false
		for _, listed := range batch.Items {
			if listed.NativeID == groupID {
				item = listed
				found = true
			}
		}
		if !found || item.Actionable == nil || !*item.Actionable {
			t.Fatal("group missing from actionable inventory")
		}
		group.Normalized = item.Normalized
		group.Location = item.Location
		group.Capabilities = item.ResourceKind.Capabilities
		group.ResourceKindID = item.ResourceKind.ID
		values := []asset.Asset{group, target, source}
		hook, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		contributed, err := hook.Contribute(t.Context(), "scope", values)
		if err != nil {
			t.Fatal("public graph", err)
		}
		input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{group.ID}, Relationships: contributed.Relationships, LifecycleBindings: contributed.Bindings, Unresolved: contributed.Unresolved}
		planned, err := plan.Solve(input)
		if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != group.ID || len(planned.ImpactItems) != 2 {
			t.Fatal("public group plan", planned, err)
		}
		ownership, err := graph.ResolveAuthority(source.ID, contributed.Bindings)
		if err != nil || ownership.ControllerAssetID != source.ID {
			t.Fatal("group effect replaced member ownership", ownership, err)
		}
		input.ResolvedAssetIDs = []asset.AssetID{source.ID}
		independent, err := plan.Solve(input)
		if err != nil || len(independent.Blockers) != 0 || len(independent.Steps) != 1 || independent.Steps[0].AssetID != source.ID {
			t.Fatal("independent selection expanded to group", independent, err)
		}
		req := servicePlanRequest(planned, values, group)
		req.IdempotencyKey = "public-group-job"
		currentGroup := group
		currentGroup.LastSeenAt = time.Now().UTC()
		currentGroup.CurrentObservationID = "new-observation"
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", currentGroup)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := driver.(*resourceGroupAction); !ok {
			t.Fatal("wrong public driver")
		}
		snapshotCalls := maps.Clone(f.calls)
		originalReview := req.Asset.Normalized["_resource_group_configuration"]
		req.Asset.Normalized["_resource_group_configuration"] = "changed-review"
		_, snapshotErr := driver.Preflight(t.Context(), req)
		req.Asset.Normalized["_resource_group_configuration"] = originalReview
		if snapshotErr == nil || !maps.Equal(snapshotCalls, f.calls) {
			t.Fatal("mutable asset changed the driver's captured review", snapshotErr)
		}
		invalid := req
		invalid.Parameters = map[string]any{"force": true}
		if _, err := driver.Execute(t.Context(), invalid); err == nil || deletes != 0 {
			t.Fatal("unsupported force accepted", err)
		}
		check, err := driver.Preflight(t.Context(), req)
		if err != nil || !check.Allowed || check.Absent {
			t.Fatal("public preflight", check, err)
		}
		result, err := driver.Execute(t.Context(), req)
		if err != nil || deletes != 1 {
			t.Fatal("public Execute", result, err)
		}
		result.Data = groupOperationJSON(t, result.Data)
		driver, err = f.runtime.ResolveAction(t.Context(), "connection", group)
		if err != nil {
			t.Fatal(err)
		}
		forged := result
		forged.Data = maps.Clone(result.Data)
		forged.Data["binding"] = "forged"
		callsBefore := maps.Clone(f.calls)
		if out, err := driver.Wait(t.Context(), req, forged); err == nil || out.Done || !maps.Equal(callsBefore, f.calls) {
			t.Fatal("forged public checkpoint performed reads", out, err)
		}
		wait, err := driver.Wait(t.Context(), req, result)
		if mode == "public-async" {
			if err != nil || wait.Done || polls != 1 {
				t.Fatal("pending native operation finished early", wait, err, polls)
			}
			result.Data = groupOperationJSON(t, wait.Data)
			wait, err = driver.Wait(t.Context(), req, result)
		}
		if err != nil || !wait.Done {
			t.Fatal("public recovery", wait, err)
		}
		result.Data = wait.Data
		req.ExecutionResult = &result
		read, err := driver.Readback(t.Context(), req)
		if err != nil || read.Exists {
			t.Fatal("public outcome", read, err)
		}
		if _, err := driver.Execute(t.Context(), req); err == nil || deletes != 1 {
			t.Fatal("public Execute resubmitted accepted job", err, deletes)
		}
		req.ExecutionResult = nil
		read, err = driver.Readback(t.Context(), req)
		if err != nil || read.Exists {
			t.Fatal("already absent group/product observation", read, err)
		}
		if deletes != 1 || len(f.deletes) != 0 {
			t.Fatal("unexpected member delete")
		}
		return
	}
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
