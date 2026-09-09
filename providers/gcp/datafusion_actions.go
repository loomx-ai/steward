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

func (a *action) fusionActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || text(request.Asset.Normalized[fusionProof]) == "" || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("datafusion_action_identity_changed")
	}
	if err := a.client.fusionIdentity(a.kind.NativeType, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return err
	}
	endpoint, err := a.client.resourceURL(a.kind, a.identity.NativeID)
	if err != nil || endpoint != a.endpoint {
		return groupDenied("datafusion_action_endpoint_changed")
	}
	if a.kind.NativeType == fusionDNSType {
		if text(request.Asset.Normalized[fusionParentProof]) == "" || len(request.LifecycleImpacts) != 0 {
			return groupDenied("datafusion_dns_plan_invalid")
		}
		return nil
	}
	if a.kind.NativeType != fusionInstanceType {
		return groupDenied("datafusion_action_unsupported")
	}
	if _, err := groupImpacts(request); err != nil {
		return err
	}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		identity := impact.Asset.Identity
		if !impact.Delete || impact.ControllerID != request.Asset.ID || seen[impact.Asset.ID] || !slices.Contains([]string{fusionDNSType, fusionNamespaceType}, identity.NativeType) || !strings.HasPrefix(identity.NativeID, a.identity.NativeID+"/") || text(impact.Asset.Normalized[fusionProof]) == "" || impact.Asset.Normalized[fusionParentProof] != request.Asset.Normalized[fusionProof] {
			return groupDenied("datafusion_impact_changed")
		}
		seen[impact.Asset.ID] = true
		id, err := a.client.fusionID(identity.NativeType, text(impact.Asset.Normalized["name"]), a.identity.NativeID)
		if err != nil || id != identity.NativeID {
			return groupDenied("datafusion_impact_identity_changed")
		}
	}
	return nil
}

func (a *action) fusionReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.fusionActionIdentity(request); err != nil {
		return read, err
	}
	parent, proof := a.identity.NativeID, text(request.Asset.Normalized[fusionProof])
	if a.kind.NativeType == fusionDNSType {
		parent = strings.Join(strings.Split(parent, "/")[:9], "/")
		proof = text(request.Asset.Normalized[fusionParentProof])
	}
	verifyParent := func() (map[string]any, error) {
		live, err := a.client.fusionRead(ctx, fusionInstanceType, parent)
		if err != nil {
			return nil, err
		}
		if fusionConfiguration(live) != proof {
			return nil, groupDenied("datafusion_parent_changed")
		}
		if protectedComputeLabels(live) || protectionReason(fusionInstanceType, live) != "" {
			return nil, groupDenied("datafusion_parent_protected")
		}
		return live, nil
	}
	live, err := verifyParent()
	absent := isNotFound(err)
	if err != nil && !absent {
		return read, err
	}
	var children []serviceChild
	if !absent {
		if a.kind.NativeType == fusionInstanceType {
			children, err = a.client.fusionChildren(ctx, a.identity, live)
		} else {
			children, err = a.client.fusionSnapshot(ctx, parent, []string{fusionDNSType})
		}
		if err != nil {
			return read, err
		}
	} else {
		kinds := []string{fusionDNSType, fusionNamespaceType}
		if a.kind.NativeType == fusionDNSType {
			kinds = []string{fusionDNSType}
		}
		for _, kind := range kinds {
			found, err := a.client.fusionList(ctx, kind, parent)
			// A child collection may return 404 after its containing instance is
			// gone. This is accepted only with another root GET below. A collection
			// 404 under a live instance is never proof of an empty child set.
			if absent && isNotFound(err) {
				continue
			}
			if err != nil {
				return read, err
			}
			children = append(children, found...)
		}
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return read, err
	}
	childExists := false
	for _, child := range children {
		var planned map[string]any
		if a.kind.NativeType == fusionDNSType {
			if child.id != a.identity.NativeID {
				continue
			}
			planned = request.Asset.Normalized
		} else {
			impact, ok := impacts[groupImpactKey{request.Asset.ID, child.id}]
			if !ok || impact.Asset.Identity.NativeType != child.kind {
				return read, groupDenied("datafusion_unreviewed_child")
			}
			planned = impact.Asset.Normalized
		}
		if err := fusionSameResource(child.kind, planned, child.data); err != nil {
			return read, err
		}
		childExists = true
	}
	// Reconcile the parent after all child reads, including after an initial
	// 404. A replacement instance must not be hidden by its old delete operation.
	live, err = verifyParent()
	if err != nil && !isNotFound(err) {
		return read, err
	}
	if absent && err == nil {
		return read, groupDenied("datafusion_parent_visibility_changed")
	}
	read.Exists = childExists || a.kind.NativeType == fusionInstanceType && err == nil
	read.State = "DELETING"
	if err == nil {
		read.State = text(live["state"])
	}
	return read, nil
}

func fusionStage(state string) (string, error) {
	switch state {
	case "ACTIVE", "FAILED", "DISABLED":
		return "ready", nil
	case "DELETING":
		return "datafusion_delete", nil
	case "CREATING", "UPGRADING", "RESTARTING", "UPDATING", "AUTO_UPDATING", "AUTO_UPGRADING", "ENABLING":
		return "datafusion_settle", nil
	default:
		return "", groupDenied("datafusion_state_unknown")
	}
}

func (a *action) fusionPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.fusionReadback(ctx, request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !read.Exists {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	stage, err := fusionStage(read.State)
	return contracts.PreflightResult{Allowed: err == nil, Evidence: map[string]any{"datafusion_stage": stage}}, err
}

func fusionPhase(request contracts.ActionRequest, phase, operation string) contracts.ActionResult {
	return contracts.ActionResult{ProviderOperationID: operation, RetryAfter: 2 * time.Second, Data: map[string]any{
		"phase": phase, "resource": request.Asset.Identity.NativeID, "configuration": request.Asset.Normalized[fusionProof],
		"parent_configuration": text(request.Asset.Normalized[fusionParentProof]), "review": serviceReview(request), "operation": operation, "initial_operation": operation,
	}}
}

func (a *action) fusionOperation(data map[string]any, requestID string) (string, error) {
	if a.kind.NativeType == fusionDNSType {
		if len(data) != 0 {
			return "", groupDenied("datafusion_dns_response_invalid")
		}
		return "", nil
	}
	name := text(data["name"])
	parent := strings.Join(strings.Split(strings.TrimPrefix(a.identity.NativeID, "//"+fusionHost+"/"), "/")[:4], "/")
	id := a.client.canonicalName("//" + fusionHost + "/" + name)
	prefix := "//" + fusionHost + "/" + parent + "/operations/"
	if !strings.HasPrefix(id, prefix) || strings.Contains(strings.TrimPrefix(id, prefix), "/") || !segmentPattern.MatchString(last(id)) || last(id) == "." || last(id) == ".." {
		return "", groupDenied("datafusion_operation_scope_changed")
	}
	if done, present := data["done"]; present {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("datafusion_operation_state_invalid")
		}
	}
	if raw, present := data["error"]; present {
		if failure, ok := raw.(map[string]any); !ok || len(failure) == 0 {
			return "", groupDenied("datafusion_operation_error_invalid")
		}
		return "", operationError(data, requestID)
	}
	if raw, present := data["metadata"]; present {
		metadata, ok := raw.(map[string]any)
		if !ok {
			return "", groupDenied("datafusion_operation_metadata_invalid")
		}
		for field, expected := range map[string]string{"@type": "type.googleapis.com/google.cloud.datafusion.v1.OperationMetadata", "apiVersion": "v1", "verb": "delete"} {
			if raw, present := metadata[field]; present && raw != expected {
				return "", groupDenied("datafusion_operation_metadata_changed")
			}
		}
		if raw, present := metadata["target"]; present {
			id, err := a.client.fusionID(a.kind.NativeType, text(raw), "")
			if err != nil || id != a.identity.NativeID {
				return "", groupDenied("datafusion_operation_target_changed")
			}
		}
		if raw, present := metadata["requestedCancellation"]; present {
			if value, ok := raw.(bool); !ok || value {
				return "", groupDenied("datafusion_operation_cancelled")
			}
		}
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	operation, _ := metadata.catalog.Operation("datafusion.projects.locations.operations.get")
	bound, err := catalog.BindREST(operation, map[string]any{"name": name})
	return bound.URL, err
}

func (a *action) executeFusion(ctx context.Context, request contracts.ActionRequest, stage string) (contracts.ActionResult, error) {
	if stage != "ready" {
		return fusionPhase(request, stage, ""), nil
	}
	parameters := cloneParameters(a.deleteParameters)
	if a.kind.NativeType == fusionInstanceType {
		parameters["force"] = true
	}
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		return fusionPhase(request, "datafusion_delete", ""), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := a.fusionOperation(response.Data, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := fusionPhase(request, "datafusion_delete", operation)
	result.ProviderRequestID = response.RequestID
	return result, nil
}

func (a *action) waitFusion(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.fusionActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.fusionReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
	}
	phase := text(result.Data["phase"])
	operation, valid := result.Data["operation"].(string)
	if !valid || !slices.Contains([]string{"datafusion_delete", "datafusion_settle"}, phase) || result.Data["resource"] != a.identity.NativeID || result.Data["configuration"] != request.Asset.Normalized[fusionProof] || result.Data["parent_configuration"] != text(request.Asset.Normalized[fusionParentProof]) || result.Data["review"] != serviceReview(request) || result.Data["initial_operation"] != result.ProviderOperationID || (result.ProviderOperationID != "" && operation != result.ProviderOperationID) || (phase == "datafusion_settle" && operation != "") {
		return contracts.WaitResult{}, groupDenied("datafusion_phase_changed")
	}
	pending := false
	if operation != "" {
		endpoint, err := url.Parse(operation)
		if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return contracts.WaitResult{}, groupDenied("datafusion_operation_invalid")
		}
		name := strings.TrimPrefix(endpoint.Path, "/v1/")
		expected, err := a.fusionOperation(map[string]any{"name": name}, "")
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("datafusion_operation_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.fusionOperation(response.Data, response.RequestID)
			if err != nil || actual != operation {
				if err == nil {
					err = groupDenied("datafusion_operation_changed")
				}
				return contracts.WaitResult{}, err
			}
			pending = response.Data["done"] != true
		}
	}
	read, err := a.fusionReadback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if pending {
		return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
	}
	if !read.Exists {
		return contracts.WaitResult{Done: true}, nil
	}
	if phase == "datafusion_settle" {
		next, err := a.Execute(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(next.Data) == 0 {
			next = fusionPhase(request, "datafusion_delete", "")
		}
		next.Data["initial_operation"] = result.ProviderOperationID
		return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
	}
	return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
}
