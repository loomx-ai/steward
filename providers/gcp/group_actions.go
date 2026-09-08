package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func groupDenied(reason string) error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: reason, Message: contracts.SafeProviderValidationMessage}}
}

type groupImpactKey struct {
	controller asset.AssetID
	id         string
}

func groupImpacts(request contracts.ActionRequest) (map[groupImpactKey]contracts.ActionImpact, error) {
	result := map[groupImpactKey]contracts.ActionImpact{}
	for _, impact := range request.LifecycleImpacts {
		id := impact.Asset.Identity
		key := groupImpactKey{impact.ControllerID, id.NativeID}
		if id.Provider != asset.ProviderGCP || id.ConnectionID != request.Asset.Identity.ConnectionID || id.Partition != request.Asset.Identity.Partition || impact.Asset.ID == "" || result[key].Asset.ID != "" {
			return nil, fmt.Errorf("invalid managed instance group impact identity")
		}
		result[key] = impact
	}
	return result, nil
}

func (a *action) plannedGroup(ctx context.Context, request contracts.ActionRequest, live map[string]any, pendingVM ...string) (managedGroup, string, error) {
	owned, err := a.client.gkeOwnsGroup(ctx, request.Asset.Identity.NativeID)
	if err != nil || owned {
		return managedGroup{}, "managed_group_requires_gke_cleanup", err
	}
	group, err := a.client.loadManagedGroup(ctx, request.Asset.Identity.NativeID, live, pendingVM...)
	if err != nil {
		return group, "", err
	}
	if text(live["id"]) == "" || text(live["id"]) != text(request.Asset.Normalized["id"]) {
		return group, "managed_group_identity_changed", nil
	}
	if link := text(live["selfLink"]); link != "" && a.client.canonicalName(link) != group.id {
		return group, "managed_group_identity_changed", nil
	}
	plannedIG, err := a.client.computeID(text(request.Asset.Normalized["instanceGroup"]), instanceGroupType)
	if err != nil || plannedIG != group.instanceGroup {
		return group, "managed_instance_group_changed", nil
	}
	if group.autoscaler != "" {
		return group, "managed_group_autoscaler_requires_cleanup", nil
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return group, "", err
	}
	ig := impacts[groupImpactKey{request.Asset.ID, group.instanceGroup}]
	if ig.ControllerID != request.Asset.ID || ig.Asset.Identity.NativeType != instanceGroupType || !ig.Delete {
		return group, "managed_instance_group_missing_from_plan", nil
	}
	if err := a.checkGroupResource(ctx, ig, true); err != nil {
		return group, "", err
	}
	present := map[groupImpactKey]bool{{request.Asset.ID, group.instanceGroup}: true}
	for _, node := range group.nodes {
		vm := impacts[groupImpactKey{request.Asset.ID, node.id}]
		if vm.ControllerID != request.Asset.ID || vm.Asset.Identity.NativeType != instanceType || text(vm.Asset.Normalized["id"]) != text(node.data["id"]) {
			return group, "managed_vm_missing_or_changed", nil
		}
		present[groupImpactKey{request.Asset.ID, node.id}] = true
		for _, resource := range node.resources {
			impact := impacts[groupImpactKey{vm.Asset.ID, resource.id}]
			if impact.ControllerID != vm.Asset.ID || impact.Asset.Identity.NativeType != resource.kind {
				return group, "managed_resource_missing_from_plan", nil
			}
			if impact.Delete && (!vm.Delete || !resource.delete || resource.shared) {
				return group, "managed_resource_deletion_policy_changed", nil
			}
			present[groupImpactKey{vm.Asset.ID, resource.id}] = true
			// Re-read reservations/disks before their controller can delete them.
			// This also verifies the incarnation frozen in the reviewed plan.
			if err := a.checkGroupResource(ctx, impact, vm.Delete && impact.Delete); err != nil {
				return group, "", err
			}
		}
		if vm.Delete && protectedComputeLabels(node.data) {
			return group, "managed_vm_protected", nil
		}
	}
	for key, impact := range impacts {
		if present[key] {
			continue
		}
		if impact.Asset.Identity.NativeType == instanceType && impact.ControllerID == request.Asset.ID && !impact.Delete {
			// A retained VM was abandoned by an earlier, persisted preparation.
			if err := a.checkGroupResource(ctx, impact, false); err != nil {
				return group, "", err
			}
			continue
		}
		retainedParent := false
		for _, vm := range impacts {
			retainedParent = retainedParent || (vm.Asset.ID == impact.ControllerID && vm.Asset.Identity.NativeType == instanceType && vm.ControllerID == request.Asset.ID && !vm.Delete && !impact.Delete)
		}
		if retainedParent {
			continue
		}
		return group, "managed_group_members_changed", nil
	}
	return group, "", nil
}

func protectedComputeLabels(data map[string]any) bool {
	for key, value := range object(data["labels"]) {
		if key == "steward-protected" || key == "steward_protected" {
			switch strings.ToLower(text(value)) {
			case "true", "1", "yes", "on", "protected":
				return true
			}
		}
	}
	return false
}

func (a *action) checkGroupResource(ctx context.Context, impact contracts.ActionImpact, deletes bool) error {
	kind, ok := findType(impact.Asset.Identity.NativeType)
	if !ok {
		return fmt.Errorf("unknown managed resource type")
	}
	endpoint, err := a.client.resourceURL(kind, impact.Asset.Identity.NativeID)
	if err != nil {
		return err
	}
	live, err := a.client.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	if text(live["id"]) == "" || text(live["id"]) != text(impact.Asset.Normalized["id"]) || (text(live["selfLink"]) != "" && a.client.canonicalName(text(live["selfLink"])) != impact.Asset.Identity.NativeID) {
		return groupDenied("managed_resource_identity_changed")
	}
	if deletes && (protectedComputeLabels(live) || protectionReason(kind.NativeType, live) != "") {
		return groupDenied("managed_resource_protected")
	}
	return nil
}

func (a *action) prepareManagedGroup(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	deferRead := func(err error) (contracts.ActionResult, error) {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return deferRead(err)
	}
	group, reason, err := a.plannedGroup(ctx, request, live)
	if err != nil {
		return deferRead(err)
	}
	if reason != "" {
		return contracts.ActionResult{}, groupDenied(reason)
	}
	impacts, _ := groupImpacts(request)
	for _, node := range group.nodes {
		if !impacts[groupImpactKey{request.Asset.ID, node.id}].Delete {
			vm, err := a.computeAction(node.id, instanceType)
			if err != nil {
				return contracts.ActionResult{}, err
			}
			return a.groupMutation(ctx, request, a, "abandonInstances", "abandon", node.id, "", map[string]any{"body": map[string]any{"instances": []string{vm.endpoint}}})
		}
	}
	for _, node := range group.nodes {
		vmImpact := impacts[groupImpactKey{request.Asset.ID, node.id}]
		for _, resource := range node.resources {
			if !resource.delete || impacts[groupImpactKey{vmImpact.Asset.ID, resource.id}].Delete {
				continue
			}
			if resource.stateful {
				// Preserve the complete live per-instance state, including custom
				// metadata. Only the selected resource's autoDelete is changed.
				config := map[string]any{}
				raw, err := json.Marshal(node.config)
				if err != nil {
					return contracts.ActionResult{}, err
				}
				if node.config != nil {
					if err := json.Unmarshal(raw, &config); err != nil {
						return contracts.ActionResult{}, err
					}
					if text(config["fingerprint"]) == "" {
						return contracts.ActionResult{}, fmt.Errorf("per-instance configuration has no concurrency fingerprint")
					}
				}
				config["name"] = last(node.id)
				delete(config, "status")
				state := object(config["preservedState"])
				if state == nil {
					state = map[string]any{}
					config["preservedState"] = state
				}
				parts := strings.SplitN(resource.slot, ":", 2)
				values := object(state[parts[0]])
				if values == nil {
					values = map[string]any{}
					state[parts[0]] = values
				}
				entry := map[string]any{}
				for key, value := range resource.state {
					entry[key] = value
				}
				entry["autoDelete"] = "NEVER"
				values[parts[1]] = entry
				result, err := a.groupMutation(ctx, request, a, "patchPerInstanceConfigs", "retain_stateful", node.id, resource.id, map[string]any{"body": map[string]any{"perInstanceConfigs": []any{config}}})
				if err == nil {
					result.Data["configuration_hash"] = configurationHash(config)
					result.Data["previous_configuration_hash"] = configurationHash(node.config)
				}
				return result, err
			}
			vm, err := a.computeAction(node.id, instanceType)
			if err != nil {
				return contracts.ActionResult{}, err
			}
			return a.groupMutation(ctx, request, vm, "setDiskAutoDelete", "retain_disk", node.id, resource.id, map[string]any{"deviceName": strings.TrimPrefix(resource.slot, "disks:"), "autoDelete": false})
		}
		if node.data["deletionProtection"] == true {
			vm, err := a.computeAction(node.id, instanceType)
			if err != nil {
				return contracts.ActionResult{}, err
			}
			return a.groupMutation(ctx, request, vm, "setDeletionProtection", "disable_protection", node.id, "", map[string]any{"resource": last(node.id), "deletionProtection": false})
		}
	}
	return a.delete(ctx, request)
}

func configurationHash(config map[string]any) string {
	var state any
	if config != nil {
		state = map[string]any{"name": config["name"], "preservedState": config["preservedState"]}
	}
	encoded, _ := json.Marshal(state)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (a *action) computeAction(id, nativeType string) (*action, error) {
	kind, _ := findType(nativeType)
	endpoint, err := a.client.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	operation, parameters, err := a.client.resourceOperation(kind, id, "DELETE")
	if err != nil {
		return nil, err
	}
	return &action{client: a.client, kind: kind, endpoint: endpoint, deleteOperation: operation, deleteParameters: parameters}, nil
}

func (a *action) groupMutation(ctx context.Context, request contracts.ActionRequest, target *action, method, change, vm, resource string, values map[string]any) (contracts.ActionResult, error) {
	metadata, _ := providerData()
	operation, ok := metadata.catalog.Operation(strings.TrimSuffix(target.deleteOperation.ID, ".delete") + "." + method)
	if !ok || operation.Call == nil {
		return contracts.ActionResult{}, fmt.Errorf("managed group preparation operation is unavailable")
	}
	parameters := map[string]any{}
	for key, value := range target.deleteParameters {
		parameters[key] = value
	}
	if method == "setDeletionProtection" {
		delete(parameters, "instance")
	}
	for key, value := range values {
		parameters[key] = value
	}
	if token := operation.Call.IdempotencyParameter; token != "" && request.IdempotencyKey != "" {
		parameters[token] = googleRequestID(request.IdempotencyKey + ":" + change + ":" + vm + ":" + resource)
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(response.Data, response.RequestID); err != nil {
		return contracts.ActionResult{}, err
	}
	poll, err := target.operationURL(response.Data)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderOperationID: poll, ProviderRequestID: response.RequestID, RetryAfter: 2 * time.Second, Data: map[string]any{"phase": "prepare_group", "operation": poll, "change": change, "vm": vm, "resource": resource}}, nil
}

func (a *action) waitManagedGroup(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase, operation := text(result.Data["phase"]), text(result.Data["operation"])
	if phase == "delete" {
		result.ProviderOperationID, result.Data = operation, nil
		return a.Wait(ctx, request, result)
	}
	if phase != "prepare_group" || operation == "" {
		return contracts.WaitResult{}, fmt.Errorf("invalid managed group action phase")
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	vm, resource, change := text(result.Data["vm"]), text(result.Data["resource"]), text(result.Data["change"])
	impact := impacts[groupImpactKey{request.Asset.ID, vm}]
	if impact.ControllerID != request.Asset.ID || impact.Asset.Identity.NativeType != instanceType {
		return contracts.WaitResult{}, fmt.Errorf("preparation target is not a reviewed managed VM")
	}
	target := a
	switch change {
	case "abandon":
		if impact.Delete {
			return contracts.WaitResult{}, fmt.Errorf("unreviewed VM retention")
		}
	case "retain_stateful", "apply_stateful", "retain_disk":
		if child := impacts[groupImpactKey{impact.Asset.ID, resource}]; child.Asset.ID == "" || child.ControllerID != impact.Asset.ID || child.Delete || !impact.Delete {
			return contracts.WaitResult{}, fmt.Errorf("unreviewed managed resource retention")
		}
	case "disable_protection":
		if !impact.Delete {
			return contracts.WaitResult{}, fmt.Errorf("cannot change a retained VM")
		}
	default:
		return contracts.WaitResult{}, fmt.Errorf("invalid managed group preparation")
	}
	if change == "retain_disk" || change == "disable_protection" {
		target, err = a.computeAction(vm, instanceType)
		if err != nil {
			return contracts.WaitResult{}, err
		}
	}
	wait, err := target.waitOperation(ctx, operation)
	if err != nil || !wait.Done {
		return wait, err
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if err != nil && !isNotFound(err) {
		return contracts.WaitResult{}, err
	}
	if err == nil {
		var pending []string
		if change == "retain_stateful" || change == "apply_stateful" {
			configs, err := a.client.groupList(ctx, request.Asset.Identity.NativeID, "listPerInstanceConfigs", "items")
			if err != nil {
				return contracts.WaitResult{}, contracts.DependencyReadError(err)
			}
			var config map[string]any
			for _, value := range configs {
				if text(value["name"]) == last(vm) {
					if config != nil {
						return contracts.WaitResult{}, fmt.Errorf("duplicate pending per-instance configuration")
					}
					config = value
				}
			}
			hash := configurationHash(config)
			if hash == text(result.Data["previous_configuration_hash"]) && change == "retain_stateful" {
				return contracts.WaitResult{State: "waiting_for_instance_configuration", RetryAfter: 2 * time.Second}, nil
			}
			if hash != text(result.Data["configuration_hash"]) {
				return contracts.WaitResult{}, groupDenied("pending_instance_configuration_changed")
			}
			switch text(config["status"]) {
			case "EFFECTIVE":
			case "UNAPPLIED":
				if change == "retain_stateful" {
					pending = []string{vm}
					break
				}
				fallthrough
			case "APPLYING":
				return contracts.WaitResult{State: "applying_instance_configuration", RetryAfter: 2 * time.Second}, nil
			default:
				return contracts.WaitResult{}, groupBusy()
			}
		}
		group, reason, err := a.plannedGroup(ctx, request, live, pending...)
		if err != nil {
			return contracts.WaitResult{}, contracts.DependencyReadError(err)
		}
		if reason != "" {
			return contracts.WaitResult{}, groupDenied(reason)
		}
		if len(pending) != 0 {
			for _, node := range group.nodes {
				if node.id != vm {
					continue
				}
				if configurationHash(node.config) != text(result.Data["configuration_hash"]) {
					return contracts.WaitResult{}, groupDenied("pending_instance_configuration_changed")
				}
				target, err := a.computeAction(vm, instanceType)
				if err != nil {
					return contracts.WaitResult{}, err
				}
				next, err := a.groupMutation(ctx, request, a, "applyUpdatesToInstances", "apply_stateful", vm, resource, map[string]any{"body": map[string]any{"instances": []string{target.endpoint}, "minimalAction": "REFRESH", "mostDisruptiveAllowedAction": "REFRESH"}})
				if err != nil {
					return contracts.WaitResult{}, err
				}
				next.Data["configuration_hash"] = result.Data["configuration_hash"]
				return contracts.WaitResult{State: "prepare_group", Data: next.Data, RetryAfter: 2 * time.Second}, nil
			}
			return contracts.WaitResult{}, groupDenied("pending_managed_vm_missing")
		}
		for _, node := range group.nodes {
			if node.id != vm {
				continue
			}
			applied := change != "abandon"
			if change == "disable_protection" {
				applied = node.data["deletionProtection"] != true
			}
			for _, attached := range node.resources {
				if attached.id == resource {
					applied = !attached.delete
				}
			}
			if !applied {
				return contracts.WaitResult{State: "preparing_managed_group", RetryAfter: 2 * time.Second}, nil
			}
		}
	}
	next, err := a.Execute(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if next.Data == nil {
		next.Data = map[string]any{}
	}
	if text(next.Data["phase"]) == "" {
		next.Data["phase"] = "delete"
	}
	next.Data["operation"] = next.ProviderOperationID
	return contracts.WaitResult{State: text(next.Data["phase"]), Data: next.Data, RetryAfter: 2 * time.Second}, nil
}

func (a *action) managedGroupReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	id, err := a.client.computeID(text(request.Asset.Normalized["instanceGroup"]), instanceGroupType)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	kind, _ := findType(instanceGroupType)
	endpoint, _ := a.client.resourceURL(kind, id)
	live, err := a.client.request(ctx, "GET", endpoint, nil)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if link := text(live["selfLink"]); link != "" && a.client.canonicalName(link) != id {
		return contracts.ReadbackResult{}, fmt.Errorf("managed instance group readback identity mismatch")
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting_instance_group"}, nil
}

func (a *action) unmanagedGroupPreflight(ctx context.Context, request contracts.ActionRequest) (string, error) {
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation("compute.instanceGroupManagers.aggregatedList")
	groups, err := a.client.nativeList(ctx, operation, map[string]any{"project": a.client.project}, "items.*.instanceGroupManagers")
	if err != nil {
		return "", err
	}
	for _, group := range groups {
		id, err := a.client.computeID(text(group["instanceGroup"]), instanceGroupType)
		if err != nil {
			return "", err
		}
		if id == request.Asset.Identity.NativeID {
			return "instance_group_managed_by_controller", nil
		}
	}
	return "", nil
}

func (a *action) managedVMPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) (string, error) {
	for _, raw := range array(object(live["metadata"])["items"]) {
		item := object(raw)
		if text(item["key"]) != "created-by" || !strings.Contains(text(item["value"]), "/instanceGroupManagers/") {
			continue
		}
		id, err := a.client.computeID(text(item["value"]), managerType)
		if err != nil {
			return "", err
		}
		kind, _ := findType(managerType)
		endpoint, _ := a.client.resourceURL(kind, id)
		if _, err := a.client.request(ctx, "GET", endpoint, nil); isNotFound(err) {
			continue
		} else if err != nil {
			return "", err
		}
		members, err := a.client.groupList(ctx, id, "listManagedInstances", "managedInstances")
		if err != nil {
			return "", err
		}
		for _, member := range members {
			vm, err := a.client.computeID(text(member["instance"]), instanceType)
			if err != nil {
				return "", err
			}
			if vm == request.Asset.Identity.NativeID {
				return "instance_managed_by_group", nil
			}
		}
	}
	return "", nil
}
