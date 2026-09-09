package gcp

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func tpuPhase(request contracts.ActionRequest, phase, operation string) contracts.ActionResult {
	return contracts.ActionResult{ProviderOperationID: operation, RetryAfter: 2 * time.Second, Data: map[string]any{
		"phase": phase, "resource": request.Asset.Identity.NativeID, "configuration": request.Asset.Normalized[tpuProof],
		"review": tpuReview(request), "operation": operation, "initial_operation": operation,
	}}
}

func (a *action) tpuOperation(data map[string]any, verb string, requestIDs ...string) (string, error) {
	name := text(data["name"])
	parts := strings.Split(name, "/")
	root, err := a.client.tpuName(a.kind.NativeType, a.identity.NativeID)
	if err != nil {
		return "", err
	}
	rootParts := strings.Split(root, "/")
	if len(parts) != 6 || parts[0] != "projects" || (parts[1] != a.client.project && parts[1] != a.client.number) || parts[2] != "locations" || parts[3] != rootParts[3] || parts[4] != "operations" || !tpuSegment(parts[5]) {
		return "", groupDenied("tpu_operation_scope_changed")
	}
	if done, present := data["done"]; present {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("tpu_operation_state_invalid")
		}
	}
	if raw, present := data["error"]; present {
		if failure, ok := raw.(map[string]any); !ok || len(failure) == 0 {
			return "", groupDenied("tpu_operation_error_invalid")
		}
		requestID := ""
		if len(requestIDs) > 0 {
			requestID = requestIDs[0]
		}
		return "", operationError(data, requestID)
	}
	if raw, present := data["metadata"]; present {
		metadata, ok := raw.(map[string]any)
		if !ok {
			return "", groupDenied("tpu_operation_metadata_invalid")
		}
		for field, expected := range map[string]string{"@type": "type.googleapis.com/google.cloud.tpu.v2.OperationMetadata", "apiVersion": "v2", "verb": verb} {
			if raw, present := metadata[field]; present && raw != expected {
				return "", groupDenied("tpu_operation_metadata_changed")
			}
		}
		if raw, present := metadata["target"]; present {
			id, err := a.client.tpuID(a.kind.NativeType, text(raw))
			if err != nil || id != a.identity.NativeID {
				return "", groupDenied("tpu_operation_target_changed")
			}
		}
		if raw, present := metadata["cancelRequested"]; present {
			if value, ok := raw.(bool); !ok || value {
				return "", groupDenied("tpu_operation_cancelled")
			}
		}
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	operation, _ := metadata.catalog.Operation("tpu.projects.locations.operations.get")
	bound, err := catalog.BindREST(operation, map[string]any{"name": name})
	return bound.URL, err
}

func (a *action) tpuWrite(ctx context.Context, request contracts.ActionRequest, phase string) (contracts.ActionResult, error) {
	operation := a.deleteOperation
	parameters := cloneParameters(a.deleteParameters)
	verb := "delete"
	if phase == "tpu_detach" {
		metadata, err := providerData()
		if err != nil {
			return contracts.ActionResult{}, err
		}
		operation, _ = metadata.catalog.Operation("tpu.projects.locations.nodes.patch")
		name, err := a.client.tpuName(tpuNodeType, a.identity.NativeID)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		// The official gcloud detach-disk command uses this protobuf mask spelling.
		parameters = map[string]any{"name": name, "updateMask": "data_disks", "body": map[string]any{"name": name, "dataDisks": []any{}}}
		verb = "update"
	} else if a.kind.NativeType == tpuQueueType && request.IdempotencyKey != "" {
		parameters["requestId"] = googleRequestID(request.IdempotencyKey)
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		return tpuPhase(request, "tpu_delete", ""), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operationID, err := a.tpuOperation(response.Data, verb, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := tpuPhase(request, phase, operationID)
	result.ProviderRequestID = response.RequestID
	return result, nil
}

func tpuStage(kind string, live map[string]any) (string, error) {
	if kind == tpuNodeType {
		switch text(live["state"]) {
		case "DELETING":
			if len(array(live["dataDisks"])) > 0 {
				return "", groupDenied("tpu_deleting_with_attached_disks")
			}
			return "tpu_delete", nil
		case "CREATING", "RESTARTING", "REIMAGING", "REPAIRING", "STOPPING", "STARTING", "HIDING", "UNHIDING":
			return "tpu_node_settle", nil
		case "READY", "STOPPED", "PREEMPTED", "TERMINATED", "HIDDEN":
			return "ready", nil
		default:
			return "", groupDenied("tpu_node_state_unknown")
		}
	}
	switch text(object(live["state"])["state"]) {
	case "DELETING":
		return "tpu_delete", nil
	case "CREATING", "PROVISIONING", "ACTIVE", "SUSPENDING":
		return "tpu_queue_settle", nil
	case "ACCEPTED", "FAILED", "SUSPENDED", "WAITING_FOR_RESOURCES":
		return "ready", nil
	default:
		return "", groupDenied("tpu_queue_state_unknown")
	}
}

func (a *action) prepareTPU(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.tpuActionIdentity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	live, err := a.readResource(ctx)
	if err != nil && !isNotFound(err) {
		return contracts.ActionResult{}, err
	}
	if err := a.tpuValidateLive(ctx, request, live); err != nil {
		return contracts.ActionResult{}, err
	}
	if isNotFound(err) {
		return tpuPhase(request, "tpu_delete", ""), nil
	}
	// Re-read after dependency checks, immediately before choosing the write.
	live, err = a.readResource(ctx)
	if isNotFound(err) {
		return tpuPhase(request, "tpu_delete", ""), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if protectedComputeLabels(live) || protectionReason(a.kind.NativeType, live) != "" {
		return contracts.ActionResult{}, groupDenied("tpu_protection_changed")
	}
	if a.kind.NativeType == tpuNodeType {
		if err := a.tpuSameNode(request, live); err != nil {
			return contracts.ActionResult{}, err
		}
		disks, err := a.client.tpuAttachments(live)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		stage, err := tpuStage(a.kind.NativeType, live)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if stage != "ready" {
			return tpuPhase(request, stage, ""), nil
		}
		if len(disks) > 0 {
			return a.tpuWrite(ctx, request, "tpu_detach")
		}
	} else {
		if err := tpuSameResource(tpuQueueType, request.Asset.Normalized, live); err != nil {
			return contracts.ActionResult{}, err
		}
		stage, err := tpuStage(a.kind.NativeType, live)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if stage != "ready" {
			return tpuPhase(request, stage, ""), nil
		}
	}
	return a.tpuWrite(ctx, request, "tpu_delete")
}

func (a *action) waitTPU(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.tpuActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.tpuReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
	}
	phase, operation := text(result.Data["phase"]), text(result.Data["operation"])
	if _, ok := result.Data["operation"].(string); !ok {
		return contracts.WaitResult{}, groupDenied("tpu_phase_operation_invalid")
	}
	if _, ok := result.Data["initial_operation"].(string); !ok {
		return contracts.WaitResult{}, groupDenied("tpu_phase_operation_invalid")
	}
	if initial := result.ProviderOperationID; initial != "" {
		u, err := url.Parse(initial)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return contracts.WaitResult{}, groupDenied("tpu_initial_operation_invalid")
		}
		expected, err := a.tpuOperation(map[string]any{"name": strings.TrimPrefix(u.Path, "/v2/")}, "delete")
		if err != nil || expected != initial {
			return contracts.WaitResult{}, groupDenied("tpu_initial_operation_changed")
		}
	}
	valid := phase == "tpu_delete" || a.kind.NativeType == tpuNodeType && (phase == "tpu_detach" || phase == "tpu_node_settle") || a.kind.NativeType == tpuQueueType && phase == "tpu_queue_settle"
	if !valid || result.Data["resource"] != a.identity.NativeID || result.Data["configuration"] != request.Asset.Normalized[tpuProof] || result.Data["review"] != tpuReview(request) || text(result.Data["initial_operation"]) != result.ProviderOperationID || strings.HasSuffix(phase, "_settle") && operation != "" {
		return contracts.WaitResult{}, groupDenied("tpu_phase_changed")
	}
	pending := false
	if operation != "" {
		endpoint, err := url.Parse(operation)
		if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return contracts.WaitResult{}, groupDenied("tpu_operation_invalid")
		}
		verb := "delete"
		if phase == "tpu_detach" {
			verb = "update"
		}
		expected, err := a.tpuOperation(map[string]any{"name": strings.TrimPrefix(endpoint.Path, "/v2/")}, verb)
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("tpu_operation_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.tpuOperation(response.Data, verb, response.RequestID)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != operation {
				return contracts.WaitResult{}, groupDenied("tpu_operation_changed")
			}
			pending = response.Data["done"] != true
		}
	}
	read, err := a.tpuReadback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if pending {
		return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
	}
	if !read.Exists {
		return contracts.WaitResult{Done: true}, nil
	}
	if phase == "tpu_delete" {
		return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
	}
	if phase == "tpu_detach" {
		live, err := a.readResource(ctx)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if err := a.tpuSameNode(request, live); err != nil {
				return contracts.WaitResult{}, err
			}
			disks, err := a.client.tpuAttachments(live)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			// Completion or expiry of an LRO does not prove that the attachment changed.
			if len(disks) > 0 {
				return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
			}
		}
	}
	next, err := a.prepareTPU(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	next.Data["initial_operation"] = result.ProviderOperationID
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
}
