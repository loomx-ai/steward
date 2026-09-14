package gcp

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const uptimeVM = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/server"
const uptimeVMNumber = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/87654321"
const uptimeNetwork = "//compute.googleapis.com/projects/sample-project/global/networks/private"

func uptimeGCEFixture() map[string]any {
	data := uptimeFixture()
	data["monitoredResource"] = map[string]any{"type": "gce_instance", "labels": map[string]any{"project_id": "123456", "zone": "us-central1-a", "instance_id": "87654321"}}
	return data
}

func TestUptimeTargetNativeIdentity(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, mode := range []string{"gce", "function", "run", "kubernetes", "directory", "group", "network", "foreign-network", "public", "aws", "unknown", "bad-project", "bad-zone", "bad-number", "missing-label", "label-space", "encoded", "function-host", "function-path", "group-path", "network-no-project"} {
		t.Run(mode, func(t *testing.T) {
			data := uptimeGCEFixture()
			kind := instanceType
			want := uptimeVMNumber
			bad := false
			labels := object(object(data["monitoredResource"])["labels"])
			switch mode {
			case "function":
				delete(data, "monitoredResource")
				data["syntheticMonitor"] = map[string]any{"cloudFunctionV2": map[string]any{"name": "projects/123456/locations/us-central1/functions/check", "cloudRunRevision": map[string]any{"labels": map[string]any{"service_name": "output-only"}}}}
				kind = "cloudfunctions.googleapis.com/CloudFunction"
				want = "//cloudfunctions.googleapis.com/projects/sample-project/locations/us-central1/functions/check"
			case "kubernetes":
				data["monitoredResource"] = map[string]any{"type": "k8s_service", "labels": map[string]any{"project_id": "sample-project", "location": "us-central1-a", "cluster_name": "cluster", "namespace_name": "ns", "service_name": "app"}}
				kind = clusterType
				want = "//container.googleapis.com/projects/sample-project/locations/us-central1-a/clusters/cluster"
			case "run":
				data["monitoredResource"] = map[string]any{"type": "cloud_run_revision", "labels": map[string]any{"project_id": "sample-project", "location": "us-central1", "service_name": "app", "revision_name": "app-0001"}}
				kind = "run.googleapis.com/Service"
				want = "//run.googleapis.com/projects/sample-project/locations/us-central1/services/app"
			case "directory":
				data["monitoredResource"] = map[string]any{"type": "servicedirectory_service", "labels": map[string]any{"project_id": "foreign-project", "location": "us-east1", "namespace_name": "ns", "service_name": "app"}}
				kind = "servicedirectory.googleapis.com/Service"
				want = "//servicedirectory.googleapis.com/projects/foreign-project/locations/us-east1/namespaces/ns/services/app"
			case "group", "group-path":
				delete(data, "monitoredResource")
				data["resourceGroup"] = map[string]any{"groupId": "9876", "resourceType": "INSTANCE"}
				kind = "monitoring.googleapis.com/Group"
				want = "//monitoring.googleapis.com/projects/sample-project/groups/9876"
				if mode == "group-path" {
					object(data["resourceGroup"])["groupId"] = "projects/sample-project/groups/9876"
					bad = true
				}
			case "network", "foreign-network", "network-no-project":
				data = uptimeFixture()
				project := "123456"
				if mode == "foreign-network" {
					project = "foreign-project"
				}
				if mode == "network-no-project" {
					project = ""
					bad = true
				}
				data["isInternal"] = true
				delete(data, "selectedRegions")
				data["internalCheckers"] = []any{map[string]any{"network": "private", "peerProjectId": project}, map[string]any{"network": "private", "peerProjectId": project}}
				kind = "compute.googleapis.com/Network"
				want = uptimeNetwork
				if mode == "foreign-network" {
					want = strings.Replace(want, "sample-project", project, 1)
				}
			case "public", "aws", "unknown":
				data = uptimeFixture()
				if mode != "public" {
					object(data["monitoredResource"])["type"] = mode
					if mode == "aws" {
						object(data["monitoredResource"])["type"] = "aws_ec2_instance"
					}
				}
				kind = ""
				want = ""
			case "bad-project":
				labels["project_id"] = "../other"
				bad = true
			case "bad-zone":
				labels["zone"] = "-"
				bad = true
			case "bad-number":
				labels["instance_id"] = "server"
				bad = true
			case "missing-label":
				delete(labels, "project_id")
				bad = true
			case "label-space":
				labels["project_id"] = " sample-project "
				bad = true
			case "encoded":
				labels["zone"] = "us-central1%2da"
				bad = true
			case "function-host", "function-path":
				delete(data, "monitoredResource")
				name := "projects/sample-project/locations/us-central1/functions/check/extra"
				if mode == "function-host" {
					name = "https://evil.example/projects/sample-project/locations/us-central1/functions/check"
				}
				data["syntheticMonitor"] = map[string]any{"cloudFunctionV2": map[string]any{"name": name}}
				bad = true
			}
			// These look like references but are user-controlled payload fields.
			data["userLabels"] = map[string]any{"network": uptimeNetwork}
			object(data["httpCheck"])["headers"] = map[string]any{"network": uptimeNetwork}
			data["contentMatchers"] = []any{map[string]any{"content": uptimeNetwork}}
			refs, err := c.uptimeReferences(uptimeID, data)
			if bad {
				if err == nil {
					t.Fatal("accepted malformed target", refs)
				}
				return
			}
			if err != nil || kind == "" && len(refs) != 0 || kind != "" && (len(refs) != 1 || !slices.Equal(refs[kind], []string{want})) {
				t.Fatal(refs, err)
			}
			item, err := (&Runtime{}).inventoryItem(c, map[string]any{"name": uptimeID, "assetType": uptimeType, "resource": map[string]any{"data": data, "location": "global"}})
			if err != nil {
				t.Fatal(err)
			}
			if kind != "" && !slices.Equal(item.NetworkReferences, []string{want}) || kind == "" && len(item.NetworkReferences) != 0 {
				t.Fatal(item.NetworkReferences)
			}
			if kind != "" {
				source := asset.Asset{ID: "check", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: uptimeType, NativeID: uptimeID, ScopeKey: "global:sample-project/global"}, Normalized: item.Normalized}
				target := asset.Asset{ID: "target", Identity: source.Identity}
				target.Identity.NativeType = kind
				target.Identity.NativeID = want
				target.Identity.ScopeKey = "region:elsewhere"
				if kind == instanceType {
					target.Identity.NativeID = uptimeVM
					target.Normalized = map[string]any{"id": "87654321"}
				}
				result, err := NewUptimeTargets().Contribute(t.Context(), "project", []asset.Asset{source, target})
				if err != nil || len(result.Relationships) != 1 || len(result.Unresolved) != 0 || len(result.Bindings) != 0 || result.Relationships[0].TargetAssetID != target.ID {
					t.Fatal(result, err)
				}
			}
			if kind == "" && len(inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: uptimeNetwork}, []contracts.InventoryItem{item})) != 0 {
				t.Fatal("arbitrary payload established network membership")
			}
		})
	}
}

func TestUptimeInstanceNetworkClosureAndGraph(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	r := &Runtime{}
	vmData := map[string]any{"id": "87654321", "name": "server", "networkInterfaces": []any{map[string]any{"network": uptimeNetwork}}}
	vm, err := r.inventoryItem(c, map[string]any{"name": uptimeVM, "assetType": instanceType, "resource": map[string]any{"data": vmData, "location": "us-central1-a"}})
	if err != nil {
		t.Fatal(err)
	}
	check, err := r.inventoryItem(c, map[string]any{"name": uptimeID, "assetType": uptimeType, "resource": map[string]any{"data": uptimeGCEFixture(), "location": "global"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vm.NativeAliases, uptimeVMNumber) {
		t.Fatal(vm.NativeAliases)
	}
	for _, recreated := range []bool{false, true} {
		candidate := vm
		if recreated {
			candidate.NativeAliases = []string{uptimeVM}
			candidate.Normalized = map[string]any{"id": "87654322"}
		}
		members := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: uptimeNetwork}, []contracts.InventoryItem{check, candidate})
		want := 2
		if recreated {
			want = 1
		}
		if len(members) != want {
			t.Fatal(recreated, members)
		}
	}
	source := asset.Asset{ID: "check", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: uptimeType, NativeID: uptimeID, ScopeKey: "global:sample-project/global"}, Normalized: check.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
	target := asset.Asset{ID: "vm", Identity: source.Identity, Normalized: vm.Normalized, Capabilities: source.Capabilities}
	target.Identity.NativeID = uptimeVM
	target.Identity.NativeType = instanceType
	target.Identity.ScopeKey = "region:us-central1"
	for _, mode := range []string{"present", "alias", "missing", "recreated", "foreign-project", "foreign-connection", "foreign-partition", "closed", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			candidate := target
			switch mode {
			case "alias":
				candidate.Identity.NativeID = strings.Replace(uptimeVM, "sample-project", "123456", 1)
			case "recreated":
				candidate.Normalized = map[string]any{"id": "87654322"}
			case "foreign-project":
				candidate.Identity.NativeID = strings.Replace(uptimeVM, "sample-project", "foreign-project", 1)
			case "foreign-connection":
				candidate.Identity.ConnectionID = "other"
			case "foreign-partition":
				candidate.Identity.Partition = "other"
			case "closed":
				now := time.Now()
				candidate.ClosedAt = &now
			}
			values := []asset.Asset{source, candidate}
			if mode == "missing" {
				values = values[:1]
			}
			if mode == "duplicate" {
				duplicate := candidate
				duplicate.ID = "duplicate"
				values = append(values, duplicate)
			}
			result, err := NewUptimeTargets().Contribute(t.Context(), "project", values)
			if mode == "duplicate" {
				if err == nil {
					t.Fatal("ambiguous target accepted")
				}
				return
			}
			if err != nil || len(result.Bindings) != 0 {
				t.Fatal(result, err)
			}
			if mode != "present" && mode != "alias" {
				if len(result.Unresolved) != 1 || len(result.Relationships) != 0 || result.Unresolved[0].BlocksCleanup {
					t.Fatal(result)
				}
				return
			}
			if len(result.Relationships) != 1 || result.Relationships[0].TargetAssetID != target.ID {
				t.Fatal(result)
			}
			for _, ids := range [][]asset.AssetID{{source.ID}, {source.ID, target.ID}} {
				solved, err := plan.Solve(plan.Input{CleanupTaskID: "test", Assets: values, ResolvedAssetIDs: ids, Relationships: result.Relationships})
				if err != nil || len(solved.Steps) != len(ids) || len(solved.ImpactItems) != 0 || solved.Steps[0].AssetID != source.ID {
					t.Fatal(solved, err)
				}
				if len(ids) == 2 && !slices.Contains(solved.Steps[1].DependsOn, solved.Steps[0].ID) {
					t.Fatal("target can delete before check", solved)
				}
			}
		})
	}
}

func TestUptimeInstanceAliasRequiresNativeNumericIdentity(t *testing.T) {
	for _, id := range []any{nil, 123.0, "", "0", "001", "server", "87654321/extra", "18446744073709551616", "87654321", "18446744073709551615"} {
		alias := uptimeInstanceAlias(uptimeVM, map[string]any{"id": id})
		valid := id == "87654321" || id == "18446744073709551615"
		if (alias != "") != valid {
			t.Fatal(id, alias)
		}
	}
	for _, path := range []string{strings.Replace(uptimeVM, "/zones/", "/regions/", 1), uptimeVM + "/extra", strings.Replace(uptimeVM, "/server", "/%73erver", 1), "https://evil.example/projects/sample-project/zones/us-central1-a/instances/server"} {
		if alias := uptimeInstanceAlias(path, map[string]any{"id": "87654321"}); alias != "" {
			t.Fatal(path, alias)
		}
	}
}
