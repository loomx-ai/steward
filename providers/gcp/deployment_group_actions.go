package gcp

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func infraGroupChildRequest(request contracts.ActionRequest, root asset.Asset) contracts.ActionRequest {
	child := infraChildRequest(request, root)
	if request.Parameters["retain_all_resources"] == true {
		child.Parameters = map[string]any{"retain_all_resources": true}
	}
	return child
}

func (a *action) infraGroupActionIdentity(request contracts.ActionRequest) ([]infraMember, string, error) {
	if a.kind.NativeType != infraGroup || request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || text(request.Asset.Normalized[infraProof]) == "" {
		return nil, "", groupDenied("infra_group_action_changed")
	}
	if err := a.client.infraIdentity(infraGroup, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return nil, "", err
	}
	if err := infraRetentionOptions(request, true); err != nil {
		return nil, "", err
	}
	members, err := a.client.infraGroupSavedMembers(request.Asset)
	if err != nil {
		return nil, "", err
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, "", err
	}
	controllers := map[asset.AssetID]bool{}
	var retained, deleted bool
	for _, member := range members {
		impact, found := impacts[groupImpactKey{request.Asset.ID, member.ID}]
		if member.Absent {
			if found {
				return nil, "", groupDenied("infra_group_absent_member_in_plan")
			}
			continue
		}
		if !found || impact.Asset.Identity.NativeType != member.Kind || impact.Asset.Normalized[infraProof] != member.Proof {
			return nil, "", groupDenied("infra_group_member_impact_changed")
		}
		if member.Kind == infraGroupRevision {
			if !impact.Delete || impact.Asset.Normalized[infraRootProof] != request.Asset.Normalized[infraProof] {
				return nil, "", groupDenied("infra_group_metadata_impact_changed")
			}
		} else {
			if impact.Asset.Normalized[infraSnapshotKey] != member.Snapshot {
				return nil, "", groupDenied("infra_group_deployment_snapshot_changed")
			}
			controllers[impact.Asset.ID] = true
			retained, deleted = retained || !impact.Delete, deleted || impact.Delete
		}
		delete(impacts, groupImpactKey{request.Asset.ID, member.ID})
	}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		if seen[impact.Asset.ID] {
			return nil, "", groupDenied("infra_group_impact_duplicate")
		}
		seen[impact.Asset.ID] = true
	}
	for _, impact := range impacts {
		if !infraDescendant(request, impact, controllers) || impact.Asset.Identity.NativeType == infraGroup || impact.Asset.Identity.NativeType == infraGroupRevision {
			return nil, "", groupDenied("infra_group_extra_impact")
		}
	}
	for _, impact := range request.PrerequisiteDeletions {
		identity := impact.Asset.Identity
		if !impact.Delete || impact.Asset.ID == "" || seen[impact.Asset.ID] || identity.Provider != a.identity.Provider || identity.ConnectionID != a.identity.ConnectionID || identity.Partition != a.identity.Partition || !infraDescendant(request, impact, controllers) {
			return nil, "", groupDenied("infra_group_prerequisite_changed")
		}
		seen[impact.Asset.ID] = true
	}
	if retained && deleted {
		return nil, "", groupDenied("infra_group_partial_deployment_retention_unsupported")
	}
	if retained {
		if len(request.PrerequisiteDeletions) != 0 {
			return nil, "", groupDenied("infra_group_detach_has_prerequisites")
		}
		for _, impact := range request.LifecycleImpacts {
			if impact.Asset.Identity.NativeType != infraGroupRevision && impact.Delete {
				return nil, "", groupDenied("infra_group_detach_has_deletions")
			}
		}
		return members, "DETACH", nil
	}
	policy := ""
	if request.Parameters["retain_all_resources"] == true {
		policy = "ABANDON"
	}
	for _, impact := range request.LifecycleImpacts {
		if !controllers[impact.Asset.ID] {
			continue
		}
		driver, err := a.infraChildDriver(impact.Asset)
		if err != nil {
			return nil, "", err
		}
		children, childPolicy, err := driver.infraActionIdentity(infraGroupChildRequest(request, impact.Asset))
		if err != nil {
			return nil, "", err
		}
		physical := false
		for _, child := range children {
			physical = physical || !isInfra(child.Kind) && !child.Absent || child.Unmapped
		}
		if physical {
			if policy != "" && policy != childPolicy {
				return nil, "", groupDenied("infra_group_partial_physical_retention_unsupported")
			}
			policy = childPolicy
		}
	}
	if policy == "" {
		policy = "DELETE"
	}
	return members, policy, nil
}

func (a *action) infraGroupPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	members, policy, err := a.infraGroupActionIdentity(request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	live, err := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
	if isNotFound(err) {
		read, err := a.infraGroupReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"infra_stage": "group_delete"}}, err
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if protectedComputeLabels(live) {
		return contracts.PreflightResult{Reason: "protected_labels"}, nil
	}
	stage := "group_ready_deprovision"
	state, provisioning, err := infraGroupStates(live)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if state == "DELETING" || state == "DELETED" || provisioning == "DEPROVISIONING" || provisioning == "DEPROVISIONED" && policy != "DETACH" {
		_, err := a.infraGroupReadback(ctx, request)
		stage = "group_deprovision"
		if state == "DELETING" || state == "DELETED" {
			stage = "group_delete"
		}
		if policy == "DETACH" && provisioning == "DEPROVISIONING" {
			return contracts.PreflightResult{}, groupDenied("infra_group_retained_deployments_deprovisioning")
		}
		return contracts.PreflightResult{Allowed: err == nil, Evidence: map[string]any{"infra_stage": stage}}, err
	}
	if err := infraSame(request.Asset.Normalized, live); err != nil {
		return contracts.PreflightResult{}, err
	}
	if state == "CREATING" || state == "UPDATING" || provisioning == "PROVISIONING" {
		return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"infra_stage": "group_settle"}}, nil
	}
	if !slices.Contains([]string{"ACTIVE", "FAILED", "SUSPENDED"}, state) {
		return contracts.PreflightResult{}, groupDenied("infra_group_state_unknown")
	}
	current, err := a.client.infraGroupSnapshot(ctx, a.identity.NativeID, live)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !slices.Equal(members, current) {
		return contracts.PreflightResult{}, groupDenied("infra_group_reviewed_members_changed")
	}
	alive := false
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || impact.Asset.Identity.NativeType != infraDeployment {
			continue
		}
		alive = true
		driver, err := a.infraChildDriver(impact.Asset)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		child := infraGroupChildRequest(request, impact.Asset)
		if policy == "DETACH" {
			if err := driver.infraRetainedDescendants(ctx, child); err != nil {
				return contracts.PreflightResult{}, err
			}
			continue
		}
		check, err := driver.Preflight(ctx, child)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !check.Allowed || check.Absent {
			return contracts.PreflightResult{}, groupDenied("infra_group_deployment_not_ready")
		}
		if check.Evidence["infra_stage"] != "ready" {
			stage = "group_settle"
		}
	}
	if !alive || policy == "DETACH" {
		stage = "group_ready_delete"
	}
	again, err := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := infraSame(live, again); err != nil {
		return contracts.PreflightResult{}, err
	}
	if again["state"] != live["state"] || again["provisioningState"] != live["provisioningState"] {
		return contracts.PreflightResult{}, groupDenied("infra_group_state_changed")
	}
	return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"infra_stage": stage}}, nil
}

func (a *action) infraGroupPhase(request contracts.ActionRequest, stage, operation, initial string) map[string]any {
	return map[string]any{"phase": stage, "resource": a.identity.NativeID, "configuration": request.Asset.Normalized[infraProof], "structure": request.Asset.Normalized[infraGroupStructure], "members": request.Asset.Normalized[infraManifestKey], "review": serviceReview(request), "retain_all": request.Parameters["retain_all_resources"] == true, "operation": operation, "initial_operation": initial}
}

func (a *action) executeInfraGroup(ctx context.Context, request contracts.ActionRequest, stage string) (contracts.ActionResult, error) {
	if stage == "group_ready_delete" {
		return a.deleteInfraGroup(ctx, request)
	}
	if stage != "group_ready_deprovision" {
		return contracts.ActionResult{Data: a.infraGroupPhase(request, stage, "", ""), RetryAfter: 2 * time.Second}, nil
	}
	_, policy, err := a.infraGroupActionIdentity(request)
	if err != nil || policy == "DETACH" {
		if err == nil {
			err = groupDenied("infra_group_detach_cannot_deprovision")
		}
		return contracts.ActionResult{}, err
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, ok := metadata.catalog.Operation("config.projects.locations.deploymentGroups.deprovision")
	if !ok {
		return contracts.ActionResult{}, groupDenied("infra_group_deprovision_operation_missing")
	}
	name, err := a.client.infraName(infraGroup, a.identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	bound, err := catalog.BindREST(operation, map[string]any{"name": name, "body": map[string]any{"force": true, "deletePolicy": policy}})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.infraGroupReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("infra_group_deprovision_not_observed")
		}
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	op, err := a.infraGroupOperation(request, "group_deprovision", response.Data, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: op, Data: a.infraGroupPhase(request, "group_deprovision", op, op), RetryAfter: 2 * time.Second}, nil
}

func (a *action) deleteInfraGroup(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	_, policy, err := a.infraGroupActionIdentity(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	root, read, err := a.infraGroupObserve(ctx, request, false)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if read.Exists {
		return contracts.ActionResult{}, groupDenied("infra_group_deployments_still_exist")
	}
	if root == nil {
		read, err := a.infraGroupReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("infra_group_metadata_still_exists")
		}
		return contracts.ActionResult{}, nil
	}
	if root["state"] == "DELETING" || root["state"] == "DELETED" {
		return contracts.ActionResult{Data: a.infraGroupPhase(request, "group_delete", "", ""), RetryAfter: 2 * time.Second}, nil
	}
	if !slices.Contains([]string{"ACTIVE", "FAILED", "SUSPENDED"}, text(root["state"])) || root["provisioningState"] == "PROVISIONING" || root["provisioningState"] == "DEPROVISIONING" {
		return contracts.ActionResult{}, groupDenied("infra_group_metadata_not_ready")
	}
	parameters := cloneParameters(a.deleteParameters)
	parameters["force"] = true
	parameters["deploymentReferencePolicy"] = "FAIL_IF_ANY_REFERENCES_EXIST"
	if policy == "DETACH" {
		parameters["deploymentReferencePolicy"] = "IGNORE_DEPLOYMENT_REFERENCES"
	}
	parameters["requestId"] = googleRequestID(request.IdempotencyKey + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[infraProof]) + "/" + serviceReview(request) + "/" + policy)
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.infraGroupReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("infra_group_delete_not_observed")
		}
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	op, err := a.infraGroupOperation(request, "group_delete", response.Data, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: op, Data: a.infraGroupPhase(request, "group_delete", op, op), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitInfraGroup(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if _, _, err := a.infraGroupActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.infraGroupReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
	}
	stage, operation := text(result.Data["phase"]), text(result.Data["operation"])
	if !slices.Contains([]string{"group_settle", "group_deprovision", "group_delete"}, stage) || stage == "group_settle" && operation != "" {
		return contracts.WaitResult{}, groupDenied("infra_group_phase_invalid")
	}
	expected := a.infraGroupPhase(request, stage, operation, result.ProviderOperationID)
	if len(expected) != len(result.Data) {
		return contracts.WaitResult{}, groupDenied("infra_group_phase_changed")
	}
	for key, value := range expected {
		if value != result.Data[key] {
			return contracts.WaitResult{}, groupDenied("infra_group_phase_changed")
		}
	}
	if initial := result.ProviderOperationID; initial != "" {
		if _, err := a.infraGroupOperationURL(initial); err != nil {
			return contracts.WaitResult{}, err
		}
	}
	pending := false
	if operation != "" {
		name, err := a.infraGroupOperationURL(operation)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		response, err := a.client.requestResult(ctx, "GET", operation, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if response.Data["name"] != name {
				return contracts.WaitResult{}, groupDenied("infra_group_operation_name_changed")
			}
			actual, err := a.infraGroupOperation(request, stage, response.Data, response.RequestID)
			if err != nil || actual != operation {
				if err == nil {
					err = groupDenied("infra_group_operation_changed")
				}
				return contracts.WaitResult{}, err
			}
			pending = response.Data["done"] != true
		}
	}
	if stage == "group_delete" {
		read, err := a.infraGroupReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists && !pending, State: read.State, RetryAfter: 2 * time.Second}, err
	}
	var next contracts.ActionResult
	var err error
	if stage == "group_settle" {
		next, err = a.Execute(ctx, request)
	} else {
		root, read, readErr := a.infraGroupObserve(ctx, request, false)
		if readErr != nil {
			return contracts.WaitResult{}, readErr
		}
		if root != nil && root["provisioningState"] == "FAILED_TO_DEPROVISION" {
			return contracts.WaitResult{}, groupDenied("infra_group_deprovision_failed")
		}
		if pending || read.Exists || root != nil && root["provisioningState"] != "DEPROVISIONED" {
			return contracts.WaitResult{State: "group_deprovision", RetryAfter: 2 * time.Second}, nil
		}
		next, err = a.deleteInfraGroup(ctx, request)
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if len(next.Data) == 0 {
		read, err := a.infraGroupReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
	}
	next.Data["initial_operation"] = result.ProviderOperationID
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
}

func (a *action) infraGroupOperationURL(operation string) (string, error) {
	u, err := url.Parse(operation)
	if err != nil || u.Scheme != "https" || u.Host != infraHost || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/v1/projects/") {
		return "", groupDenied("infra_group_operation_url_invalid")
	}
	name := strings.TrimPrefix(u.Path, "/v1/")
	if actual, err := a.infraOperation(map[string]any{"name": name}, ""); err != nil || actual != operation {
		return "", groupDenied("infra_group_operation_identity_invalid")
	}
	return name, nil
}

func (a *action) infraGroupOperation(request contracts.ActionRequest, stage string, data map[string]any, requestID string) (string, error) {
	verb := "delete"
	if stage == "group_deprovision" {
		verb = "update" // Config's deprovision LRO uses update, per its native guide.
	}
	operation, err := a.infraOperationFor(data, requestID, verb, stage == "group_delete")
	if err != nil {
		return "", err
	}
	metadata := object(data["metadata"])
	if value, present := metadata["provisionDeploymentGroupMetadata"]; present {
		if stage != "group_deprovision" {
			return "", groupDenied("infra_group_delete_has_deprovision_metadata")
		}
		phase := object(value)
		if !slices.Contains([]string{"", "PROVISION_DEPLOYMENT_GROUP_STEP_UNSPECIFIED", "VALIDATING_DEPLOYMENT_GROUP", "ASSOCIATING_DEPLOYMENTS_TO_DEPLOYMENT_GROUP", "DEPROVISIONING_DEPLOYMENT_UNITS", "DISASSOCIATING_DEPLOYMENTS_FROM_DEPLOYMENT_GROUP", "SUCCEEDED"}, text(phase["step"])) {
			return "", groupDenied("infra_group_deprovision_step_invalid")
		}
		if err := a.infraGroupUnitProgress(request, phase); err != nil {
			return "", err
		}
	}
	if stage == "group_deprovision" {
		if value, present := data["response"]; present {
			response := object(value)
			if value, present := response["createTime"]; present && value != request.Asset.Normalized["createTime"] {
				return "", groupDenied("infra_group_operation_incarnation_changed")
			}
			// Typed LRO responses can omit labels/annotations. Validate any unit
			// data they expose, then verify the full configuration through GET.
			if _, present := response["deploymentUnits"]; present {
				before, err := a.client.infraGroupUnits(request.Asset.Normalized)
				if err != nil {
					return "", err
				}
				after, err := a.client.infraGroupUnits(response)
				if err != nil || len(before) != len(after) {
					return "", groupDenied("infra_group_operation_units_changed")
				}
				for i, unit := range after {
					if unit.ID != before[i].ID || !slices.Equal(unit.Dependencies, before[i].Dependencies) || unit.Deployment != "" && unit.Deployment != before[i].Deployment {
						return "", groupDenied("infra_group_operation_reference_changed")
					}
				}
			}
		}
	}
	return operation, nil
}

func (a *action) infraGroupUnitProgress(request contracts.ActionRequest, phase map[string]any) error {
	value, present := phase["deploymentUnitProgresses"]
	if !present {
		return nil
	}
	rows, ok := value.([]any)
	if !ok {
		return groupDenied("infra_group_progress_invalid")
	}
	members, err := a.client.infraGroupSavedMembers(request.Asset)
	if err != nil {
		return err
	}
	deployments, units := map[string]bool{}, map[string]string{}
	for _, member := range members {
		if member.Kind == infraDeployment {
			deployments[member.ID] = true
		}
	}
	current, _ := a.client.infraGroupUnits(request.Asset.Normalized)
	for _, unit := range current {
		units[unit.ID] = unit.Deployment
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || impact.Asset.Identity.NativeType != infraGroupRevision {
			continue
		}
		previous, err := a.client.infraGroupUnits(object(impact.Asset.Normalized["snapshot"]))
		if err != nil {
			return err
		}
		for _, unit := range previous {
			units["revisions/"+last(impact.Asset.Identity.NativeID)+"/deploymentUnits/"+unit.ID] = unit.Deployment
		}
	}
	seen := map[string]bool{}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		unit := text(row["unitId"])
		expected, found := units[unit]
		if !ok || !found || seen[unit] {
			return groupDenied("infra_group_progress_unit_changed")
		}
		seen[unit] = true
		if name := text(row["deployment"]); name != "" {
			id, err := a.client.infraID(infraDeployment, name)
			if err != nil || !deployments[id] || expected != id {
				return groupDenied("infra_group_progress_deployment_changed")
			}
		}
		if _, present := row["error"]; present || !slices.Contains([]string{"", "INTENT_UNSPECIFIED", "DELETE_DEPLOYMENT", "CLEAN_UP", "UNCHANGED"}, text(row["intent"])) || !slices.Contains([]string{"", "STATE_UNSPECIFIED", "QUEUED", "DELETING_DEPLOYMENT", "SUCCEEDED", "SKIPPED"}, text(row["state"])) {
			return groupDenied("infra_group_progress_failed_or_changed")
		}
	}
	return nil
}
