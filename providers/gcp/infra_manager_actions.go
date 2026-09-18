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

func (a *action) infraActionIdentity(request contracts.ActionRequest) ([]infraMember, string, error) {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || text(request.Asset.Normalized[infraProof]) == "" || (a.kind.NativeType != infraDeployment && a.kind.NativeType != infraPreview) {
		return nil, "", groupDenied("infra_action_changed")
	}
	if err := a.client.infraIdentity(a.kind.NativeType, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return nil, "", err
	}
	if err := infraRetentionOptions(request, false); err != nil {
		return nil, "", err
	}
	members, err := a.client.infraSavedMembers(request.Asset)
	if err != nil {
		return nil, "", err
	}
	return a.infraReviewedMembers(request, members)
}

func infraRetentionOptions(request contracts.ActionRequest, deployments bool) error {
	for name, value := range request.Parameters {
		switch name {
		case "retain_all_resources":
			if _, ok := value.(bool); !ok {
				return groupDenied("infra_retention_option_invalid")
			}
		case "retain_resources":
			var ids []string
			switch value := value.(type) {
			case []string:
				ids = value
			case []any:
				for _, item := range value {
					id, ok := item.(string)
					if !ok {
						return groupDenied("infra_retention_option_invalid")
					}
					ids = append(ids, id)
				}
			default:
				return groupDenied("infra_retention_option_invalid")
			}
			for _, id := range ids {
				found := false
				for _, impact := range request.LifecycleImpacts {
					allowed := !isInfra(impact.Asset.Identity.NativeType) || deployments && impact.Asset.Identity.NativeType == infraDeployment
					found = found || id != "" && (id == string(impact.Asset.ID) || id == impact.Asset.Identity.NativeID) && !impact.Delete && allowed
				}
				if !found {
					return groupDenied("infra_retention_target_invalid")
				}
			}
		default:
			return groupDenied("infra_option_unsupported")
		}
	}
	return nil
}

func (a *action) infraReviewedMembers(request contracts.ActionRequest, members []infraMember) ([]infraMember, string, error) {
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, "", err
	}
	var retained, deleted, unmapped bool
	physical := map[asset.AssetID]bool{}
	for _, member := range members {
		unmapped = unmapped || member.Unmapped
		impact, found := impacts[groupImpactKey{request.Asset.ID, member.ID}]
		if member.Absent {
			if found {
				return nil, "", groupDenied("infra_absent_member_in_plan")
			}
			continue
		}
		if !found || impact.Asset.Identity.NativeType != member.Kind {
			return nil, "", groupDenied("infra_member_missing_from_plan")
		}
		if isInfra(member.Kind) {
			if !impact.Delete || impact.Asset.Normalized[infraProof] != member.Proof || impact.Asset.Normalized[infraRootProof] != request.Asset.Normalized[infraProof] {
				return nil, "", groupDenied("infra_metadata_impact_changed")
			}
		} else {
			if infraPhysicalVisible(member.Kind, impact.Asset.Normalized) != member.VisibleProof {
				return nil, "", groupDenied("infra_physical_impact_changed")
			}
			physical[impact.Asset.ID] = true
			retained, deleted = retained || !impact.Delete, deleted || impact.Delete
		}
		delete(impacts, groupImpactKey{request.Asset.ID, member.ID})
	}
	// Descendant impacts belong to an already reviewed native physical controller.
	// That controller's own driver validates the exact nested membership below.
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		if seen[impact.Asset.ID] {
			return nil, "", groupDenied("infra_impact_asset_duplicate")
		}
		seen[impact.Asset.ID] = true
	}
	for _, impact := range impacts {
		if !infraDescendant(request, impact, physical) || isInfra(impact.Asset.Identity.NativeType) {
			return nil, "", groupDenied("infra_extra_impact")
		}
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		identity := prerequisite.Asset.Identity
		if !prerequisite.Delete || prerequisite.Asset.ID == "" || seen[prerequisite.Asset.ID] || identity.Provider != a.identity.Provider || identity.ConnectionID != a.identity.ConnectionID || identity.Partition != a.identity.Partition || !infraDescendant(request, prerequisite, physical) {
			return nil, "", groupDenied("infra_prerequisite_changed")
		}
		seen[prerequisite.Asset.ID] = true
	}
	policy := "DELETE"
	if retained || request.Parameters["retain_all_resources"] == true {
		policy = "ABANDON"
	}
	if retained && deleted || policy == "ABANDON" && deleted {
		return nil, "", groupDenied("infra_partial_retention_not_supported")
	}
	if policy == "ABANDON" {
		if len(request.PrerequisiteDeletions) != 0 {
			return nil, "", groupDenied("infra_abandon_has_prerequisite_deletions")
		}
		for _, impact := range request.LifecycleImpacts {
			if !isInfra(impact.Asset.Identity.NativeType) && impact.Delete {
				return nil, "", groupDenied("infra_abandon_has_descendant_deletions")
			}
		}
	}
	if unmapped && (policy != "ABANDON" || request.Parameters["retain_all_resources"] != true) {
		return nil, "", groupDenied("infra_unmapped_deployment_resource_requires_explicit_abandon")
	}
	return members, policy, nil
}

func infraDescendant(request contracts.ActionRequest, impact contracts.ActionImpact, roots map[asset.AssetID]bool) bool {
	seen := map[asset.AssetID]bool{impact.Asset.ID: true}
	for {
		if roots[impact.ControllerID] {
			return true
		}
		if seen[impact.ControllerID] {
			return false
		}
		seen[impact.ControllerID] = true
		found := false
		for _, parent := range request.LifecycleImpacts {
			if parent.Asset.ID == impact.ControllerID {
				impact, found = parent, true
				break
			}
		}
		if !found {
			return false
		}
	}
}

func infraChildRequest(request contracts.ActionRequest, root asset.Asset) contracts.ActionRequest {
	child := contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: request.IdempotencyKey + "/" + string(root.ID)}
	for _, impact := range request.LifecycleImpacts {
		if infraDescendant(request, impact, map[asset.AssetID]bool{root.ID: true}) {
			child.LifecycleImpacts = append(child.LifecycleImpacts, impact)
		}
	}
	for _, impact := range request.PrerequisiteDeletions {
		if infraDescendant(request, impact, map[asset.AssetID]bool{root.ID: true}) {
			child.PrerequisiteDeletions = append(child.PrerequisiteDeletions, impact)
		}
	}
	return child
}

func (a *action) infraChildDriver(value asset.Asset) (*action, error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok {
		return nil, groupDenied("infra_managed_type_unknown")
	}
	endpoint, err := a.client.resourceURL(kind, value.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	op, params, err := a.client.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	return &action{identity: value.Identity, client: a.client, kind: kind, endpoint: endpoint, deleteOperation: op, deleteParameters: params}, nil
}

func (a *action) infraPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	members, policy, err := a.infraActionIdentity(request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	live, err := a.client.infraRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if isNotFound(err) {
		read, err := a.infraReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"infra_stage": "infra_delete"}}, err
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := infraSame(request.Asset.Normalized, live); err != nil {
		return contracts.PreflightResult{}, err
	}
	if protectedComputeLabels(live) {
		return contracts.PreflightResult{Reason: "protected_labels"}, nil
	}
	state := text(live["state"])
	if state == "DELETING" || state == "DELETED" {
		_, err := a.infraReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Evidence: map[string]any{"infra_stage": "infra_delete"}}, err
	}
	if slices.Contains([]string{"CREATING", "UPDATING", "APPLYING"}, state) {
		return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"infra_stage": "infra_settle"}}, nil
	}
	states := []string{"ACTIVE", "FAILED", "SUSPENDED"}
	if a.kind.NativeType == infraPreview {
		states = []string{"SUCCEEDED", "STALE", "FAILED"}
	}
	if !slices.Contains(states, state) {
		return contracts.PreflightResult{}, groupDenied("infra_state_unknown")
	}
	if a.kind.NativeType == infraDeployment {
		lock := text(live["lockState"])
		if lock != "UNLOCKED" {
			return contracts.PreflightResult{Reason: "infra_deployment_locked_or_unknown"}, nil
		}
		if policy == "DELETE" && text(live["latestRevision"]) != "" {
			revision, err := a.client.infraID(infraRevision, text(live["latestRevision"]))
			if err != nil {
				return contracts.PreflightResult{}, err
			}
			last, err := a.client.infraRead(ctx, infraRevision, revision)
			if err != nil {
				return contracts.PreflightResult{}, err
			}
			if last["state"] != "APPLIED" {
				return contracts.PreflightResult{}, groupDenied("infra_latest_revision_not_applied")
			}
		}
	}
	current, err := a.client.infraSnapshot(ctx, a.kind.NativeType, a.identity.NativeID, live)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !slices.Equal(members, current) {
		return contracts.PreflightResult{}, groupDenied("infra_reviewed_members_changed")
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || isInfra(impact.Asset.Identity.NativeType) || policy == "ABANDON" {
			continue
		}
		driver, err := a.infraChildDriver(impact.Asset)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		check, err := driver.Preflight(ctx, infraChildRequest(request, impact.Asset))
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !check.Allowed || check.Absent {
			return contracts.PreflightResult{}, groupDenied("infra_managed_resource_not_ready")
		}
		if err := driver.infraNativeDeleteReady(ctx, infraChildRequest(request, impact.Asset)); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	if policy == "ABANDON" {
		if err := a.infraRetainedDescendants(ctx, request); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	again, err := a.client.infraRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := infraSame(live, again); err != nil {
		return contracts.PreflightResult{}, err
	}
	if again["state"] != live["state"] || again["lockState"] != live["lockState"] {
		return contracts.PreflightResult{}, groupDenied("infra_controller_state_changed")
	}
	return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"infra_stage": "ready"}}, nil
}

func (a *action) infraReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	members, policy, err := a.infraActionIdentity(request)
	if err != nil {
		return read, err
	}
	read.State = "infra_delete"
	root, rootErr := a.client.infraRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if rootErr != nil && !isNotFound(rootErr) {
		return read, rootErr
	}
	if rootErr == nil {
		if err := infraSame(request.Asset.Normalized, root); err != nil {
			return read, err
		}
		read.Exists, read.State = true, text(root["state"])
	}
	for _, member := range members {
		var live map[string]any
		var err error
		if isInfra(member.Kind) {
			live, err = a.client.infraRead(ctx, member.Kind, member.ID)
		} else {
			live, err = a.client.infraPhysicalRead(ctx, member.Kind, member.ID)
		}
		if isNotFound(err) {
			if !isInfra(member.Kind) && policy == "ABANDON" && !member.Absent {
				return read, groupDenied("infra_retained_resource_missing")
			}
			continue
		}
		if err != nil {
			return read, err
		}
		proof, expected := infraPhysicalConfiguration(live), member.Proof
		if isInfra(member.Kind) {
			proof = infraConfiguration(live)
		} else if policy == "DELETE" && member.Incarnation != "" {
			// Native deletion can remove attachments and child collections before
			// the containing resource disappears. Keep checking its immutable ID
			// during that transition; retained resources still require full config.
			proof, expected = infraPhysicalIncarnation(member.Kind, live), member.Incarnation
		}
		if member.Absent || proof != expected {
			return read, groupDenied("infra_member_recreated_or_changed")
		}
		if isInfra(member.Kind) || policy == "DELETE" {
			read.Exists = true
		}
	}
	// Native physical drivers also verify their transitive cascades; an absent
	// deployment or resource record is never proof that a VM/disk/table is gone.
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || isInfra(impact.Asset.Identity.NativeType) || policy == "ABANDON" {
			continue
		}
		driver, err := a.infraChildDriver(impact.Asset)
		if err != nil {
			return read, err
		}
		child, err := driver.Readback(ctx, infraChildRequest(request, impact.Asset))
		if err != nil {
			return read, err
		}
		read.Exists = read.Exists || child.Exists
	}
	if policy == "ABANDON" {
		if err := a.infraRetainedDescendants(ctx, request); err != nil {
			return read, err
		}
	}
	if isNotFound(rootErr) {
		remaining, err := a.client.infraRecords(ctx, a.kind.NativeType, a.identity.NativeID, false)
		if err != nil && !isNotFound(err) {
			return read, err
		}
		for _, record := range remaining {
			index := slices.IndexFunc(members, func(member infraMember) bool { return member.Kind == record.kind && member.ID == record.id })
			if index < 0 || infraConfiguration(record.data) != members[index].Proof {
				return read, groupDenied("infra_unreviewed_remaining_metadata")
			}
			read.Exists = true
		}
	}
	again, err := a.client.infraRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if err != nil && !isNotFound(err) {
		return read, err
	}
	if err == nil {
		if rootErr != nil {
			return read, groupDenied("infra_root_reappeared")
		}
		if err := infraSame(request.Asset.Normalized, again); err != nil {
			return read, err
		}
		read.Exists, read.State = true, text(again["state"])
	}
	return read, nil
}

func (a *action) infraRetainedDescendants(ctx context.Context, request contracts.ActionRequest) error {
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID == request.Asset.ID || isInfra(impact.Asset.Identity.NativeType) {
			continue
		}
		kind := impact.Asset.Identity.NativeType
		live, err := a.client.nativeGet(ctx, kind, impact.Asset.Identity.NativeID)
		if isNotFound(err) {
			return groupDenied("infra_retained_descendant_missing")
		}
		if err != nil {
			return err
		}
		if _, present := live["error"]; present || infraPhysicalVisible(kind, live) != infraPhysicalVisible(kind, impact.Asset.Normalized) {
			return groupDenied("infra_retained_descendant_changed")
		}
	}
	return nil
}

func (a *action) infraPhase(request contracts.ActionRequest, stage, operation string) map[string]any {
	return map[string]any{"phase": stage, "resource": a.identity.NativeID, "configuration": request.Asset.Normalized[infraProof], "members": request.Asset.Normalized[infraManifestKey], "review": serviceReview(request), "abandon": request.Parameters["retain_all_resources"] == true, "operation": operation, "initial_operation": operation}
}

func (a *action) executeInfra(ctx context.Context, request contracts.ActionRequest, stage string) (contracts.ActionResult, error) {
	if stage != "ready" {
		return contracts.ActionResult{Data: a.infraPhase(request, stage, ""), RetryAfter: 2 * time.Second}, nil
	}
	_, policy, err := a.infraActionIdentity(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	parameters := cloneParameters(a.deleteParameters)
	if a.kind.NativeType == infraDeployment {
		parameters["force"], parameters["deletePolicy"] = true, policy
	}
	parameters["requestId"] = googleRequestID(request.IdempotencyKey + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[infraProof]) + "/" + serviceReview(request) + "/" + policy)
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.infraReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("infra_delete_not_observed")
		}
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := a.infraOperation(response.Data, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.infraPhase(request, "infra_delete", operation), RetryAfter: 2 * time.Second}, nil
}

func (a *action) infraOperation(data map[string]any, requestID string) (string, error) {
	return a.infraOperationFor(data, requestID, "delete", true)
}

func (a *action) infraOperationFor(data map[string]any, requestID, verb string, deleted bool) (string, error) {
	name := text(data["name"])
	p := strings.Split(name, "/")
	resource, err := a.client.infraName(a.kind.NativeType, a.identity.NativeID)
	if err != nil || len(p) != 6 || p[0] != "projects" || (p[1] != a.client.project && p[1] != a.client.number) || p[2] != "locations" || p[3] != strings.Split(resource, "/")[3] || p[4] != "operations" || !segmentPattern.MatchString(p[5]) || p[5] == "." || p[5] == ".." {
		return "", groupDenied("infra_operation_identity_invalid")
	}
	if done, present := data["done"]; present {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("infra_operation_done_invalid")
		}
	}
	if _, present := data["error"]; present {
		if err := operationError(data, requestID); err != nil {
			return "", err
		}
		return "", groupDenied("infra_operation_error_invalid")
	}
	if value, present := data["metadata"]; present {
		metadata, ok := value.(map[string]any)
		if !ok {
			return "", groupDenied("infra_operation_metadata_invalid")
		}
		for field, expected := range map[string]string{"@type": "type.googleapis.com/google.cloud.config.v1.OperationMetadata", "verb": verb, "apiVersion": "v1"} {
			if value, present := metadata[field]; present && value != expected {
				return "", groupDenied("infra_operation_metadata_changed")
			}
		}
		if value, present := metadata["target"]; present {
			id, err := a.client.infraID(a.kind.NativeType, text(value))
			if err != nil || id != a.identity.NativeID {
				return "", groupDenied("infra_operation_target_changed")
			}
		}
		if value, present := metadata["requestedCancellation"]; present {
			if cancelled, ok := value.(bool); !ok || cancelled {
				return "", groupDenied("infra_operation_cancelled")
			}
		}
		for field, kind := range map[string]string{"deploymentMetadata": infraDeployment, "previewMetadata": infraPreview, "provisionDeploymentGroupMetadata": infraGroup} {
			if value, present := metadata[field]; present {
				phase, ok := value.(map[string]any)
				if !ok || kind != a.kind.NativeType {
					return "", groupDenied("infra_operation_phase_invalid")
				}
				if phase["step"] == "FAILED" {
					return "", groupDenied("infra_operation_failed")
				}
			}
		}
	}
	if value, present := data["response"]; present {
		response, ok := value.(map[string]any)
		if !ok || data["done"] != true {
			return "", groupDenied("infra_operation_response_invalid")
		}
		// Config's native operation_info returns the deleted Deployment/Preview,
		// rather than google.protobuf.Empty as many other Google services do.
		if response["@type"] != "type.googleapis.com/google.cloud.config.v1."+last(a.kind.NativeType) {
			return "", groupDenied("infra_operation_response_changed")
		}
		id, err := a.client.infraID(a.kind.NativeType, text(response["name"]))
		if err != nil || id != a.identity.NativeID {
			return "", groupDenied("infra_operation_response_target_changed")
		}
		if state, present := response["state"]; deleted && present && state != "DELETED" {
			return "", groupDenied("infra_operation_response_not_deleted")
		}
		if !deleted && (response["state"] != "ACTIVE" || response["provisioningState"] != "DEPROVISIONED") {
			return "", groupDenied("infra_group_operation_not_deprovisioned")
		}
	}
	return "https://" + infraHost + "/v1/" + name, nil
}

func (a *action) waitInfra(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if _, _, err := a.infraActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.infraReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
	}
	stage, operation := text(result.Data["phase"]), text(result.Data["operation"])
	if !slices.Contains([]string{"infra_settle", "infra_delete"}, stage) {
		return contracts.WaitResult{}, groupDenied("infra_phase_invalid")
	}
	expectedPhase := a.infraPhase(request, stage, operation)
	expectedPhase["initial_operation"] = result.ProviderOperationID
	for key, value := range expectedPhase {
		if result.Data[key] != value {
			return contracts.WaitResult{}, groupDenied("infra_phase_changed")
		}
	}
	if result.ProviderOperationID != "" && operation != result.ProviderOperationID || stage == "infra_settle" && operation != "" {
		return contracts.WaitResult{}, groupDenied("infra_phase_operation_changed")
	}
	if stage == "infra_settle" {
		next, err := a.Execute(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(next.Data) == 0 {
			read, err := a.infraReadback(ctx, request)
			return contracts.WaitResult{Done: err == nil && !read.Exists}, err
		}
		next.Data["initial_operation"] = result.ProviderOperationID
		return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
	}
	pending := false
	if operation != "" {
		u, err := url.Parse(operation)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return contracts.WaitResult{}, groupDenied("infra_operation_url_invalid")
		}
		expected, err := a.infraOperation(map[string]any{"name": strings.TrimPrefix(u.Path, "/v1/")}, "")
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("infra_operation_url_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", operation, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.infraOperation(response.Data, response.RequestID)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != expected {
				return contracts.WaitResult{}, groupDenied("infra_operation_changed")
			}
			pending = response.Data["done"] != true
		}
	}
	read, err := a.infraReadback(ctx, request)
	if err == nil && read.Exists && read.State == "FAILED" {
		return contracts.WaitResult{}, groupDenied("infra_delete_failed")
	}
	return contracts.WaitResult{Done: err == nil && !pending && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
}
