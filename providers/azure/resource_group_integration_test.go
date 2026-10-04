package azure

import (
	"fmt"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"maps"
	"net/http"
	"strings"
	"testing"
)

func TestResourceGroupPublicMonitorLifecycle(t *testing.T) {
	for _, mode := range []string{"public", "public-async"} {
		t.Run(mode, func(t *testing.T) { testResourceGroupMonitorReferences(t, monitorActivityAlertType, mode) })
	}
}

func TestResourceGroupGraphBoundaries(t *testing.T) {
	for _, mode := range []string{"native", "nested", "unselected", "unknown", "omitted", "duplicate", "wrong-kind", "foreign", "forbidden", "changed", "paged", "repeated-page", "other-connection", "mixed-case-asset", "duplicate-asset"} {
		t.Run(mode, func(t *testing.T) {
			groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
			groupRaw := map[string]any{"id": groupID, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			disk := actionAsset(diskType, "disk")
			diskRaw := map[string]any{"id": disk.Identity.NativeID, "name": "disk", "type": diskType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "uniqueId": "original"}}
			disk.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(diskRaw)}
			vm := actionAsset(vmType, "vm")
			vmRaw := map[string]any{"id": vm.Identity.NativeID, "type": vmType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "storageProfile": map[string]any{"osDisk": map[string]any{"managedDisk": map[string]any{"id": disk.Identity.NativeID}, "deleteOption": "Delete"}}}}
			vm.Normalized = maps.Clone(object(vmRaw["properties"]))
			vm.Normalized["subscription_id"] = testSubscription
			lists := 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				if q.Method != "GET" {
					t.Fatal("graph attempted mutation", q.Method, q.URL)
				}
				if reply, ok := emptyMonitorIndexResponse(t, q); ok {
					return reply, nil
				}
				switch strings.ToLower(q.URL.Path) {
				case groupID:
					if mode == "changed" && lists > 0 {
						groupRaw["tags"] = map[string]any{"changed": "yes"}
					}
					return jsonResponse(200, groupRaw, nil), nil
				case disk.Identity.NativeID:
					return jsonResponse(200, diskRaw, nil), nil
				case vm.Identity.NativeID:
					return jsonResponse(200, vmRaw, nil), nil
				case groupID + "/resources":
					lists++
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					rows := []any{diskRaw}
					if mode == "nested" {
						rows = append(rows, vmRaw)
					}
					switch mode {
					case "unknown":
						rows = append(rows, map[string]any{"id": groupID + "/providers/microsoft.unknown/widgets/hidden", "type": "Microsoft.Unknown/widgets"})
					case "omitted":
						rows = []any{}
					case "duplicate":
						rows = append(rows, diskRaw)
					case "wrong-kind":
						other := maps.Clone(diskRaw)
						other["type"] = vmType
						rows = []any{other}
					case "foreign":
						other := maps.Clone(diskRaw)
						other["id"] = strings.Replace(disk.Identity.NativeID, "/test/", "/test-other/", 1)
						rows = []any{other}
					}
					body := map[string]any{"value": rows}
					if mode == "paged" || mode == "repeated-page" {
						if q.URL.Query().Get("$skiptoken") == "" {
							body["value"] = []any{}
							body["nextLink"] = apiURL(groupID+"/resources", resourcesVersion) + "&%24skiptoken=next"
						} else if mode == "repeated-page" {
							body["nextLink"] = q.URL.String()
						}
					}
					return jsonResponse(200, body, nil), nil
				}
				t.Fatal("unexpected graph request", q.URL)
				return nil, nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			group := actionAsset(groupType, "test")
			group.ID = "group"
			group.Identity.NativeID = groupID
			group.Location = "global"
			group.Normalized = map[string]any{"_resource_group_configuration": c.privateConfiguration(groupRaw), "_resource_group_location": "eastus"}
			group.Capabilities = r.resourceKind(groupType).Capabilities
			disk.Capabilities = r.resourceKind(diskType).Capabilities
			vm.Capabilities = r.resourceKind(vmType).Capabilities
			values := []asset.Asset{group, disk}
			if mode == "nested" {
				values = append(values, vm)
			}
			if mode == "other-connection" {
				values[0].Identity.ConnectionID = "other"
			}
			if mode == "mixed-case-asset" {
				// Members bucket by case-folded group prefix; a sibling group
				// sharing the name prefix is not a member.
				values[1].Identity.NativeID = strings.Replace(disk.Identity.NativeID, "/resourcegroups/test/", "/resourceGroups/TEST/", 1)
				sibling := actionAsset(diskType, "sibling")
				sibling.Identity.NativeID = strings.Replace(sibling.Identity.NativeID, "/test/", "/test-other/", 1)
				values = append(values, sibling)
			}
			if mode == "duplicate-asset" {
				copy := disk
				copy.ID = "disk-copy"
				copy.Identity.NativeID = strings.Replace(disk.Identity.NativeID, "/disks/", "/DISKS/", 1)
				values = append(values, copy)
			}
			contributed, err := c.contributeResourceGroups(t.Context(), "connection", values, managedGroupMembers(values))
			fails := mode == "duplicate-asset" || mode == "omitted" || mode == "duplicate" || mode == "wrong-kind" || mode == "foreign" || mode == "forbidden" || mode == "changed" || mode == "repeated-page"
			if fails {
				if err == nil || len(contributed.Bindings)+len(contributed.Unresolved) != 0 || mode == "duplicate-asset" && (lists != 0 || !strings.Contains(err.Error(), "resource_group_graph_duplicate_asset")) {
					t.Fatal("invalid native graph accepted", contributed, err)
				}
				return
			}
			if mode == "mixed-case-asset" && (len(contributed.Bindings) != 1 || contributed.Bindings[0].ManagedAssetID != disk.ID) {
				t.Fatal("mixed-case member not bound", contributed)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "other-connection" {
				if lists != 0 || len(contributed.Bindings) != 0 {
					t.Fatal("cross-connection graph reads")
				}
				return
			}
			if mode == "nested" {
				attachments, err := NewResourceAttachments().Contribute(t.Context(), "scope", values)
				if err != nil {
					t.Fatal(err)
				}
				contributed.Bindings = append(contributed.Bindings, attachments.Bindings...)
				contributed.Relationships = append(contributed.Relationships, attachments.Relationships...)
			}
			selected := group.ID
			if mode == "unselected" {
				selected = disk.ID
			}
			planned, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{selected}, LifecycleBindings: contributed.Bindings, Relationships: contributed.Relationships, Unresolved: contributed.Unresolved})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown" {
				if len(contributed.Unresolved) != 1 || len(planned.Blockers) == 0 {
					t.Fatal("unknown member disappeared", contributed, planned)
				}
				return
			}
			deletions := 0
			for _, step := range planned.Steps {
				if step.Action == "delete" {
					deletions++
					if step.AssetID != selected {
						t.Fatal("extra deletion", step)
					}
				}
			}
			if len(planned.Blockers) != 0 || deletions != 1 {
				t.Fatal("native group plan", planned)
			}
			if mode == "nested" {
				for _, impact := range planned.ImpactItems {
					if impact.AssetID == disk.ID && impact.ControllerID != vm.ID {
						t.Fatal("lost immediate VM controller", impact)
					}
				}
			}
		})
	}
}

func TestReadsInOrderReportsEarliestFailure(t *testing.T) {
	var reads []func() (map[string]any, error)
	for i := range 20 {
		reads = append(reads, func() (map[string]any, error) {
			if i == 7 || i == 15 {
				return nil, fmt.Errorf("read %d", i)
			}
			return map[string]any{"i": i}, nil
		})
	}
	if _, err := readsInOrder(reads); err == nil || err.Error() != "read 7" {
		t.Fatal("not the earliest failure", err)
	}
	results, err := readsInOrder(reads[:7])
	if err != nil || len(results) != 7 || results[6]["i"] != 6 {
		t.Fatal("results out of order", results, err)
	}
}
