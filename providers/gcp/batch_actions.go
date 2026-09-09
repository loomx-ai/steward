package gcp

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchSameChild(child serviceChild, planned map[string]any) error {
	if child.kind == batchTaskType {
		if text(planned[batchParentProof]) == "" || text(planned[batchParentProof]) != text(child.data[batchParentProof]) || text(planned["_batch_job_uid"]) != text(child.data["_batch_job_uid"]) {
			return groupDenied("batch_task_parent_changed")
		}
	} else if text(planned["id"]) == "" || batchComputeConfiguration(child.kind, planned) != batchComputeConfiguration(child.kind, child.data) {
		return groupDenied("batch_compute_changed")
	}
	return nil
}

func (a *action) batchJobIdentity(request contracts.ActionRequest, live map[string]any) error {
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity.Provider != asset.ProviderGCP || request.Asset.Identity.NativeType != batchJobType || a.client.canonicalName("//batch.googleapis.com/"+text(live["name"])) != request.Asset.Identity.NativeID {
		return groupDenied("batch_job_identity_changed")
	}
	if text(request.Asset.Normalized[batchProof]) == "" {
		return groupDenied("batch_job_proof_missing")
	}
	if err := batchSameResource(batchJobType, request.Asset.Normalized, live); err != nil {
		return err
	}
	switch text(object(live["status"])["state"]) {
	case "QUEUED", "SCHEDULED", "RUNNING", "SUCCEEDED", "FAILED", "DELETION_IN_PROGRESS", "CANCELLATION_IN_PROGRESS", "CANCELLED":
		return nil
	default:
		return groupDenied("batch_job_state_unknown")
	}
}

// Validate the frozen ancestry even after Batch has removed the Job. A 404 for
// a forged sibling/foreign resource is not evidence that reviewed work finished.
func (a *action) batchPlan(request contracts.ActionRequest) (map[groupImpactKey]contracts.ActionImpact, error) {
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity.NativeType != batchJobType || request.Asset.Identity.Provider != asset.ProviderGCP || text(request.Asset.Normalized[batchProof]) == "" || text(request.Asset.Normalized["uid"]) == "" || len(request.PrerequisiteDeletions) > 0 {
		return nil, groupDenied("batch_plan_invalid")
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, err
	}
	groups, err := a.client.batchTaskGroups(request.Asset.Identity.NativeID, request.Asset.Normalized)
	if err != nil {
		return nil, err
	}
	vms := map[asset.AssetID]asset.Asset{}
	for _, impact := range impacts {
		if impact.Asset.Identity.NativeType == instanceType && impact.ControllerID == request.Asset.ID {
			if !impact.Delete {
				return nil, groupDenied("batch_vm_retention_not_supported")
			}
			if err := a.client.batchComputeLabel(request.Asset.Identity, request.Asset.Normalized, impact.Asset.Normalized, instanceType); err != nil {
				return nil, err
			}
			vms[impact.Asset.ID] = impact.Asset
		}
	}
	for _, impact := range impacts {
		identity := impact.Asset.Identity
		kind, ok := findType(identity.NativeType)
		if !ok {
			return nil, groupDenied("batch_impact_kind_invalid")
		}
		if _, err := a.client.resourceURL(kind, identity.NativeID); err != nil {
			return nil, err
		}
		switch identity.NativeType {
		case batchTaskType:
			found := false
			for _, group := range groups {
				prefix := "//batch.googleapis.com/" + group + "/tasks/"
				found = found || strings.HasPrefix(identity.NativeID, prefix) && !strings.Contains(strings.TrimPrefix(identity.NativeID, prefix), "/")
			}
			if !found || !impact.Delete || impact.ControllerID != request.Asset.ID || text(impact.Asset.Normalized[batchParentProof]) != text(request.Asset.Normalized[batchProof]) || text(impact.Asset.Normalized["_batch_job_uid"]) != text(request.Asset.Normalized["uid"]) {
				return nil, groupDenied("batch_task_scope_changed")
			}
		case instanceType:
			if vms[impact.Asset.ID].ID == "" {
				return nil, groupDenied("batch_vm_scope_changed")
			}
		case "compute.googleapis.com/Disk", "compute.googleapis.com/RegionDisk":
			found := false
			for _, vm := range vms {
				disks, err := instanceDisks(a.client, vm.Normalized)
				if err != nil {
					return nil, err
				}
				for _, disk := range disks {
					if disk.id == identity.NativeID && disk.kind == identity.NativeType && disk.autoDelete == impact.Delete && ((disk.autoDelete && impact.ControllerID == vm.ID) || (!disk.autoDelete && impact.ControllerID == request.Asset.ID)) {
						found = true
					}
				}
			}
			if !found && impact.Delete && impact.ControllerID == request.Asset.ID {
				// Orphan disks discovered in a complete UID query have no remaining
				// VM controller. Only Batch can clean them; there is no disk DELETE.
				found = a.client.batchComputeLabel(request.Asset.Identity, request.Asset.Normalized, impact.Asset.Normalized, identity.NativeType) == nil && len(array(impact.Asset.Normalized["users"])) == 0
			}
			if !found || text(impact.Asset.Normalized["id"]) == "" {
				return nil, groupDenied("batch_disk_scope_changed")
			}
		default:
			return nil, groupDenied("batch_impact_kind_invalid")
		}
	}
	return impacts, nil
}

func (a *action) batchPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	if err := a.batchJobIdentity(request, live); err != nil {
		return err
	}
	impacts, err := a.batchPlan(request)
	if err != nil {
		return err
	}
	if object(live["status"])["state"] == "DELETION_IN_PROGRESS" {
		// A native deletion already owns this transition. Observing it must not
		// replay DELETE or demand that its disappearing members still be listed.
		_, err := a.batchReadback(ctx, request)
		return err
	}
	children, err := a.client.batchChildren(ctx, request.Asset.Identity, live)
	if err != nil {
		return err
	}
	present := map[groupImpactKey]bool{}
	check := func(controller asset.AssetID, child serviceChild) (contracts.ActionImpact, error) {
		key := groupImpactKey{controller, child.id}
		impact, exists := impacts[key]
		if !exists || present[key] || impact.Asset.Identity.NativeType != child.kind || impact.Delete == child.retain {
			return impact, groupDenied("batch_member_missing_or_policy_changed")
		}
		if err := batchSameChild(child, impact.Asset.Normalized); err != nil {
			return impact, err
		}
		if impact.Delete && (protectedComputeLabels(child.data) || protectionReason(child.kind, child.data) != "" || child.data["deletionProtection"] == true) {
			return impact, groupDenied("batch_member_protected")
		}
		present[key] = true
		return impact, nil
	}
	for _, child := range children {
		controller := request.Asset.ID
		if child.kind != instanceType && child.kind != batchTaskType && !child.retain {
			for key, frozen := range impacts {
				if key.id != child.id || key.controller == request.Asset.ID || !frozen.Delete {
					continue
				}
				for _, parent := range impacts {
					if parent.Asset.ID == key.controller && parent.Asset.Identity.NativeType == instanceType {
						if _, err := a.client.nativeGet(ctx, instanceType, parent.Asset.Identity.NativeID); !isNotFound(err) {
							if err != nil {
								return err
							}
							return groupDenied("batch_disk_detached_from_live_vm")
						}
						controller = key.controller
					}
				}
			}
		}
		impact, err := check(controller, child)
		if err != nil {
			return err
		}
		if child.kind != instanceType {
			continue
		}
		disks, err := instanceDisks(a.client, child.data)
		if err != nil {
			return err
		}
		for _, disk := range disks {
			if !disk.autoDelete {
				continue
			}
			live, err := a.client.nativeGet(ctx, disk.kind, disk.id)
			if err != nil {
				return err
			}
			if err := a.client.batchDiskUsers(live, child.id, true); err != nil {
				return err
			}
			if _, err := check(impact.Asset.ID, serviceChild{kind: disk.kind, id: disk.id, data: live}); err != nil {
				return err
			}
		}
	}
	for key, impact := range impacts {
		if present[key] {
			continue
		}
		live, err := a.client.nativeGet(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if isNotFound(err) && impact.Delete {
			continue
		}
		if err != nil {
			return err
		}
		if impact.Delete {
			return groupDenied("batch_member_membership_changed")
		}
		if err := batchSameChild(serviceChild{kind: impact.Asset.Identity.NativeType, data: live}, impact.Asset.Normalized); err != nil {
			return err
		}
	}
	return a.client.verifyBatchParent(ctx, productTarget{ParentType: batchJobType, ParentID: request.Asset.Identity.NativeID, ParentUID: text(request.Asset.Normalized["uid"]), ParentConfiguration: text(request.Asset.Normalized[batchProof])})
}

func (a *action) batchReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	impacts, err := a.batchPlan(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	pending := false
	retained := map[string]contracts.ActionImpact{}
	for _, impact := range impacts {
		live, err := a.client.nativeGet(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if isNotFound(err) && impact.Delete {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
		if impact.Asset.Identity.NativeType != batchTaskType {
			if text(live["id"]) != text(impact.Asset.Normalized["id"]) || a.client.canonicalName(text(live["selfLink"])) != impact.Asset.Identity.NativeID {
				return contracts.ReadbackResult{}, groupDenied("batch_member_recreated")
			}
		}
		if impact.Delete {
			pending = true
		} else {
			retained[impact.Asset.Identity.NativeID] = impact
		}
	}
	groups, err := a.client.batchTaskGroups(request.Asset.Identity.NativeID, request.Asset.Normalized)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, group := range groups {
		tasks, err := a.client.batchList(ctx, "batch.projects.locations.jobs.taskGroups.tasks.list", map[string]any{"parent": group, "pageSize": 100}, "tasks")
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		for _, task := range tasks {
			id := a.client.canonicalName("//batch.googleapis.com/" + text(task["name"]))
			if err := a.client.batchChildIdentity(batchTaskType, id, group); err != nil {
				return contracts.ReadbackResult{}, err
			}
			pending = true
		}
	}
	current, err := a.client.batchCompute(ctx, text(request.Asset.Normalized["uid"]))
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, member := range current {
		if keep, ok := retained[member.id]; !ok || text(keep.Asset.Normalized["id"]) != text(member.data["id"]) {
			pending = true
		}
	}
	// The parent might have been recreated while checking the last child.
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if err == nil {
		if err := a.batchJobIdentity(request, live); err != nil {
			return contracts.ReadbackResult{}, err
		}
		pending = true
	} else if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: pending, State: "batch_resources_deleting"}, nil
}

func batchPhase(request contracts.ActionRequest, operation string) map[string]any {
	return map[string]any{"phase": "batch_delete", "resource": request.Asset.Identity.NativeID, "uid": request.Asset.Normalized["uid"], "configuration": request.Asset.Normalized[batchProof], "operation": operation}
}

func (a *action) batchOperation(data map[string]any, request contracts.ActionRequest) (string, error) {
	if done, present := data["done"]; present {
		if _, valid := done.(bool); !valid {
			return "", groupDenied("batch_operation_status_invalid")
		}
	}
	if text(data["name"]) == "" {
		return "", groupDenied("batch_operation_name_missing")
	}
	operation, err := a.operationURL(data)
	if err != nil {
		return "", err
	}
	metadata := object(data["metadata"])
	if raw := data["metadata"]; raw != nil && metadata == nil {
		return "", groupDenied("batch_operation_metadata_invalid")
	}
	for _, key := range []string{"target", "verb", "apiVersion", "createTime"} {
		if value, exists := metadata[key]; exists {
			if _, valid := value.(string); !valid {
				return "", groupDenied("batch_operation_metadata_invalid")
			}
		}
	}
	if target := text(metadata["target"]); target != "" && a.client.canonicalName("//batch.googleapis.com/"+target) != request.Asset.Identity.NativeID {
		return "", groupDenied("batch_operation_target_changed")
	}
	if verb := text(metadata["verb"]); verb != "" && verb != "delete" {
		return "", groupDenied("batch_operation_verb_changed")
	}
	if version := text(metadata["apiVersion"]); version != "" && version != "v1" {
		return "", groupDenied("batch_operation_version_changed")
	}
	if created := text(metadata["createTime"]); created != "" {
		stamp, err := time.Parse(time.RFC3339Nano, created)
		job, jobErr := time.Parse(time.RFC3339Nano, text(request.Asset.Normalized["createTime"]))
		if err != nil || jobErr != nil || stamp.Before(job) {
			return "", groupDenied("batch_operation_incarnation_changed")
		}
	}
	return operation, nil
}

func (a *action) waitBatch(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if text(result.Data["phase"]) != "batch_delete" || text(result.Data["resource"]) != request.Asset.Identity.NativeID || text(result.Data["uid"]) != text(request.Asset.Normalized["uid"]) || text(result.Data["configuration"]) == "" || text(result.Data["configuration"]) != text(request.Asset.Normalized[batchProof]) || text(result.Data["operation"]) != result.ProviderOperationID {
		return contracts.WaitResult{}, groupDenied("batch_action_phase_invalid")
	}
	if _, err := a.batchPlan(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if operation := result.ProviderOperationID; operation != "" {
		parsed, err := url.Parse(operation)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		name := strings.TrimPrefix(parsed.Path, "/v1/")
		expected, err := a.batchOperation(map[string]any{"name": name}, request)
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("batch_operation_scope_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.batchOperation(response.Data, request)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != expected {
				return contracts.WaitResult{}, groupDenied("batch_operation_identity_changed")
			}
			if err := operationError(response.Data, response.RequestID); err != nil {
				return contracts.WaitResult{}, err
			}
			if response.Data["done"] != true {
				return contracts.WaitResult{RetryAfter: 2 * time.Second, State: "batch_delete"}, nil
			}
		}
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
