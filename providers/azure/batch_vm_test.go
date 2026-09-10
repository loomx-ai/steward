package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

// Compose the Batch native examples with the existing Uniform Compute/Network
// protocol fixture. Allocation mode and the documented VM ID are explicit;
// this is not an Azure deployment or a recording of a UserSubscription pool.
func newBatchVMScenario(t *testing.T) (*batchScenario, *Runtime, []asset.Asset, []map[string]any) {
	t.Helper()
	s, r, assets := newBatchScenario(t)
	compute, raw := uniformScaleSetScenario()
	raw[0]["sku"] = map[string]any{"name": "Standard_D2_v5", "capacity": 1}
	for id, value := range compute.records {
		if value["location"] != nil {
			value["location"] = "japaneast"
		}
		s.arm.records[id], s.arm.version[id] = value, compute.version[id]
	}
	for id, values := range compute.lists {
		s.arm.lists[id], s.arm.version[id] = values, compute.version[id]
	}
	object(s.arm.records[s.account]["properties"])["poolAllocationMode"] = "UserSubscription"
	object(s.records["/pools/poolid/nodes/nodeid"]["virtualMachineInfo"])["scaleSetVmResourceId"] = raw[1]["id"]
	c, _ := r.resolve(t.Context(), "connection")
	account, err := c.batchAccount(t.Context(), s.account)
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range assets {
		if !isBatchDataType(value.Identity.NativeType) {
			assets[i] = dnsAsset(t, r, s.arm.records[value.Identity.NativeID])
			continue
		}
		item, err := r.batchDataItem(t.Context(), c, account, value.Identity.NativeType, s.records[strings.TrimPrefix(value.Identity.NativeID, s.origin)], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assets[i].Normalized = item.Normalized
	}
	for _, value := range raw {
		assets = append(assets, dnsAsset(t, r, value))
	}
	return s, r, assets, raw
}

func TestBatchUserSubscriptionReviewedPhysicalDeletion(t *testing.T) {
	for _, targetKind := range []string{batchNodeType, batchAccountType} {
		t.Run(targetKind, func(t *testing.T) {
			s, r, assets, raw := newBatchVMScenario(t)
			target := cdnAsset(t, assets, targetKind)
			solved := batchPlan(t, r, assets, target)
			expected := 7
			if targetKind == batchAccountType {
				expected = 10
			}
			if len(solved.Blockers) != 0 || len(solved.ImpactItems) != expected {
				t.Fatal("Batch physical review", solved.Blockers, len(solved.ImpactItems))
			}
			for _, step := range solved.Steps {
				value := assets[slices.IndexFunc(assets, func(value asset.Asset) bool { return value.ID == step.AssetID })]
				if !isBatchType(value.Identity.NativeType) {
					t.Fatal("Batch issued an independent Compute delete", value.Identity.NativeType)
				}
				request := servicePlanRequest(solved, assets, value)
				driver, err := r.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal("Batch physical delete", value.Identity.NativeType, err)
				}
				batchCascadeGone(s)
				if value.Identity.NativeType == batchNodeType {
					s.gone["/pools/poolid/nodes/nodeid"] = true
				}
				payload, _ := json.Marshal(request)
				json.Unmarshal(payload, &request)
				payload, _ = json.Marshal(receipt)
				json.Unmarshal(payload, &receipt)
				driver, _ = r.ResolveAction(t.Context(), "connection", request.Asset)
				if value.Identity.NativeType == batchNodeType || value.Identity.NativeType == batchPoolType {
					// The node is already absent. Every physical member is still
					// checked independently, including after VM disappearance.
					for _, index := range []int{1, 8, 4, 5, 6, 2, 3} {
						wait, err := driver.Wait(t.Context(), request, receipt)
						if err != nil || wait.Done {
							t.Fatal("remaining Batch VM resource was ignored", index, wait, err)
						}
						s.arm.gone[strings.ToLower(text(raw[index]["id"]))] = true
					}
				}
				if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
					t.Fatal("physical Batch absence", value.Identity.NativeType, wait, err)
				}
			}
			if s.arm.gone[strings.ToLower(text(raw[0]["id"]))] || s.arm.gone[strings.ToLower(text(raw[7]["id"]))] {
				t.Fatal("node deletion claimed its retained scale set")
			}
		})
	}
}

func TestBatchUserSubscriptionDriftAndProtection(t *testing.T) {
	for _, mode := range []string{"missing_vm", "foreign_subscription", "wrong_vm_kind", "vm_region", "flexible", "vm_recreated", "disk_recreated", "private_extension", "disk_owner", "nic_owner", "disk_detach", "shared_disk", "extra_nic", "vm_protected", "disk_protected", "scale_set_protected", "group_owner", "lock", "forbidden", "duplicate_node_vm", "final_vm_change"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, raw := newBatchVMScenario(t)
			node := cdnAsset(t, assets, batchNodeType)
			solved := batchPlan(t, r, assets, node)
			request := servicePlanRequest(solved, assets, node)
			switch mode {
			case "missing_vm":
				delete(object(s.records["/pools/poolid/nodes/nodeid"]["virtualMachineInfo"]), "scaleSetVmResourceId")
			case "foreign_subscription":
				object(s.records["/pools/poolid/nodes/nodeid"]["virtualMachineInfo"])["scaleSetVmResourceId"] = strings.Replace(text(raw[1]["id"]), testSubscription, "foreign", 1)
			case "wrong_vm_kind":
				object(s.records["/pools/poolid/nodes/nodeid"]["virtualMachineInfo"])["scaleSetVmResourceId"] = raw[2]["id"]
			case "vm_region":
				raw[1]["location"] = "eastus"
			case "flexible":
				object(raw[0]["properties"])["orchestrationMode"] = "Flexible"
			case "vm_recreated":
				object(raw[1]["properties"])["vmId"] = "replacement"
			case "disk_recreated":
				object(raw[2]["properties"])["uniqueId"] = "replacement"
			case "private_extension":
				object(raw[8]["properties"])["protectedSettings"] = map[string]any{"key": "SECRET_NEW_VALUE"}
			case "disk_owner":
				raw[2]["managedBy"] = resourceID(vmType, "other")
			case "nic_owner":
				object(object(raw[4]["properties"])["virtualMachine"])["id"] = resourceID(vmType, "other")
			case "disk_detach":
				object(object(object(raw[1]["properties"])["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
			case "shared_disk":
				raw[2]["managedByExtended"] = []any{raw[1]["id"], resourceID(vmType, "other")}
			case "extra_nic":
				id := strings.ToLower(text(raw[1]["id"])) + "/networkinterfaces"
				s.arm.lists[id] = append(s.arm.lists[id], raw[4])
			case "vm_protected":
				object(raw[1]["properties"])["protectionPolicy"] = map[string]any{"protectFromScaleIn": true}
			case "disk_protected":
				raw[2]["tags"] = map[string]any{"steward/protected": "true"}
			case "scale_set_protected":
				raw[0]["tags"] = map[string]any{"steward/protected": "true"}
			case "group_owner":
				id := strings.Join(strings.Split(strings.ToLower(text(raw[1]["id"])), "/")[:5], "/")
				s.arm.records[id] = map[string]any{"id": id, "managedBy": resourceID("Microsoft.ContainerService/managedClusters", "other")}
			case "lock":
				id := "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks"
				s.arm.lists[id] = []any{map[string]any{"id": text(raw[2]["id"]) + "/providers/Microsoft.Authorization/locks/lock", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forbidden":
				s.arm.status[strings.ToLower(text(raw[2]["id"]))] = 403
			case "duplicate_node_vm":
				other := batchClone(s.records["/pools/poolid/nodes/nodeid"])
				other["id"], other["url"] = "second", s.origin+"/pools/poolid/nodes/second"
				s.records["/pools/poolid/nodes/second"] = other
				s.lists["/pools/poolid/nodes"] = append(s.lists["/pools/poolid/nodes"], other)
			case "final_vm_change":
				poolReads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Scheme+"://"+req.URL.Host == s.origin && req.URL.Path == "/pools/poolid" {
						poolReads++
						if poolReads == 3 {
							object(raw[1]["properties"])["vmId"] = "replacement"
						}
					}
					return nil, false
				}
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", node)
			if _, err := driver.Execute(t.Context(), request); err == nil || len(s.writes)+len(s.arm.deletes) != 0 {
				t.Fatal("Batch physical drift/protection did not stop mutation", mode, err)
			}
		})
	}
}

func TestBatchUserSubscriptionReadbackBindsPhysicalSet(t *testing.T) {
	for _, mode := range []string{"missing_impact", "missing_resources", "foreign_id", "wrong_controller", "forbidden_readback"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, raw := newBatchVMScenario(t)
			node := cdnAsset(t, assets, batchNodeType)
			request := servicePlanRequest(batchPlan(t, r, assets, node), assets, node)
			driver, _ := r.ResolveAction(t.Context(), "connection", node)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			s.gone["/pools/poolid/nodes/nodeid"] = true
			for _, impact := range request.LifecycleImpacts {
				s.arm.gone[strings.ToLower(impact.Asset.Identity.NativeID)] = true
			}
			switch mode {
			case "missing_impact":
				request.LifecycleImpacts = request.LifecycleImpacts[:len(request.LifecycleImpacts)-1]
			case "missing_resources":
				request.Asset.Normalized["_batch_vm_resources"] = map[string]any{}
			case "foreign_id":
				request.LifecycleImpacts[0].Asset.Identity.NativeID = resourceID(diskType, "foreign")
			case "wrong_controller":
				request.LifecycleImpacts[0].ControllerID = "foreign"
			case "forbidden_readback":
				s.arm.status[strings.ToLower(text(raw[2]["id"]))] = 403
			}
			if wait, err := driver.Wait(t.Context(), request, receipt); err == nil || wait.Done {
				t.Fatal("forged/unauthorized Batch physical readback succeeded", wait, err)
			}
			if _, err := driver.Readback(t.Context(), request); err == nil {
				t.Fatal("direct readback accepted an unreviewed physical set", err)
			}
			if len(s.writes) != 1 || len(s.arm.deletes) != 0 {
				t.Fatal("readback mutated physical resources")
			}
		})
	}
}

func TestBatchVMRetentionAndAccountOwnedScope(t *testing.T) {
	s, r, assets, raw := newBatchVMScenario(t)
	node := cdnAsset(t, assets, batchNodeType)
	contributor, _ := r.ServiceLifecycle(t.Context(), "connection")
	contribution, err := contributor.Contribute(t.Context(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{node.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	for _, kind := range []string{scaleSetVMType, diskType, scaleSetNICType, scaleSetIPConfigType, scaleSetPublicIPType, scaleSetVMExtensionType} {
		physical := cdnAsset(t, assets, kind)
		input.RequestOptions = map[asset.AssetID]map[string]any{node.ID: {"retain_resources": []string{physical.Identity.NativeID}}}
		if solved, err := plan.Solve(input); err != nil || len(solved.Blockers) == 0 {
			t.Fatal("Batch node accepted physical resource retention", kind, err)
		}
	}
	input.RequestOptions = nil
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 {
		t.Fatal(err, solved.Blockers)
	}
	groupID := strings.Join(strings.Split(strings.ToLower(text(raw[1]["id"])), "/")[:5], "/")
	s.arm.records[groupID] = map[string]any{"id": groupID, "managedBy": s.account}
	raw[0]["managedBy"] = s.account
	driver, _ := r.ResolveAction(t.Context(), "connection", node)
	if _, err := driver.Execute(t.Context(), servicePlanRequest(solved, assets, node)); err != nil || len(s.writes) != 1 || len(s.arm.deletes) != 0 {
		t.Fatal("verified Batch account ownership of the physical scope blocked node removal", err)
	}
}

func TestBatchVMControllerPrecedesScaleSet(t *testing.T) {
	s, r, assets, raw := newBatchVMScenario(t)
	node, scale := cdnAsset(t, assets, batchNodeType), cdnAsset(t, assets, scaleSetType)
	contributor, _ := r.ServiceLifecycle(t.Context(), "connection")
	contribution, err := contributor.Contribute(t.Context(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatal(err, contribution.Unresolved)
	}
	for _, target := range []asset.Asset{scale, cdnAsset(t, assets, scaleSetVMType)} {
		result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
		if err != nil || len(result.Blockers) == 0 {
			t.Fatal("Compute deletion bypassed the retained Batch node", target.Identity.NativeType, result.Blockers, err)
		}
	}
	solved, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{scale.ID, node.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 2 || solved.Steps[0].AssetID != node.ID {
		t.Fatal("explicit Batch node/scale set selection", solved.Blockers, len(solved.Steps), err)
	}
	request := servicePlanRequest(solved, assets, scale)
	driver, _ := r.ResolveAction(t.Context(), "connection", scale)
	if _, err := driver.Execute(t.Context(), request); err == nil || len(s.arm.deletes) != 0 {
		t.Fatal("scale set mutated before its Batch prerequisite", err)
	}
	s.gone["/pools/poolid/nodes/nodeid"] = true
	for _, index := range []int{1, 8, 4, 5, 6, 2, 3} {
		s.arm.gone[strings.ToLower(text(raw[index]["id"]))] = true
	}
	object(raw[0]["sku"])["capacity"], raw[0]["etag"] = 0, "after-node"
	if _, err := driver.Execute(t.Context(), request); err != nil || len(s.arm.deletes) != 1 {
		t.Fatal("scale set could not follow its verified Batch prerequisite", err, s.arm.deletes)
	}
}

func TestBatchUserSubscriptionPoolFollowsExplicitNode(t *testing.T) {
	for _, mode := range []string{"native_decrement", "extra_decrement", "increase", "other_setting", "forged_capacity_class", "vm_remaining", "disk_remaining"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, raw := newBatchVMScenario(t)
			node, account := cdnAsset(t, assets, batchNodeType), cdnAsset(t, assets, batchAccountType)
			contributor, _ := r.ServiceLifecycle(t.Context(), "connection")
			contribution, err := contributor.Contribute(t.Context(), "scope", assets)
			if err != nil {
				t.Fatal(err)
			}
			solved, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{account.ID, node.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal(err, solved.Blockers)
			}
			for _, step := range solved.Steps {
				value := assets[slices.IndexFunc(assets, func(value asset.Asset) bool { return value.ID == step.AssetID })]
				request := servicePlanRequest(solved, assets, value)
				if value.Identity.NativeType == batchPoolType {
					fixed := object(object(object(s.arm.records[s.account+"/pools/poolid"]["properties"])["scaleSettings"])["fixedScale"])
					switch mode {
					case "extra_decrement":
						fixed["targetDedicatedNodes"] = 4
					case "increase":
						fixed["targetDedicatedNodes"] = 7
					case "other_setting":
						object(s.arm.records[s.account+"/pools/poolid"]["properties"])["vmSize"] = "changed"
					case "forged_capacity_class":
						fixed["targetDedicatedNodes"], fixed["targetLowPriorityNodes"] = 6, 27
						for i, prerequisite := range request.PrerequisiteDeletions {
							if prerequisite.Asset.Identity.NativeType == batchNodeType {
								request.PrerequisiteDeletions[i].Asset.Normalized["isDedicated"] = false
							}
						}
					case "vm_remaining":
						s.arm.gone[strings.ToLower(text(raw[1]["id"]))] = false
					case "disk_remaining":
						s.arm.gone[strings.ToLower(text(raw[2]["id"]))] = false
					}
				}
				driver, _ := r.ResolveAction(t.Context(), "connection", value)
				writes := len(s.writes) + len(s.arm.deletes)
				receipt, err := driver.Execute(t.Context(), request)
				if value.Identity.NativeType == batchPoolType && mode != "native_decrement" {
					if err == nil || len(s.writes)+len(s.arm.deletes) != writes {
						t.Fatal("pool accepted a changed/unverified node prerequisite", mode, err)
					}
					return
				}
				if err != nil {
					t.Fatal("explicit node sequence", value.Identity.NativeType, err)
				}
				if value.Identity.NativeType == batchNodeType {
					s.gone["/pools/poolid/nodes/nodeid"] = true
					object(object(object(s.arm.records[s.account+"/pools/poolid"]["properties"])["scaleSettings"])["fixedScale"])["targetDedicatedNodes"] = 5
					for _, impact := range request.LifecycleImpacts {
						s.arm.gone[strings.ToLower(impact.Asset.Identity.NativeID)] = true
					}
				}
				batchCascadeGone(s)
				if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
					t.Fatal("explicit node sequence readback", value.Identity.NativeType, wait, err)
				}
			}
			if mode != "native_decrement" {
				t.Fatal("pool step was not exercised")
			}
		})
	}
}
