package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type fleetAction struct {
	action
	planned asset.Asset
}

func newFleetAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*fleetAction, error) {
	if !slices.Contains(fleetDirectKinds, kind.NativeType) || value.ID == "" || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Identity.NativeType != kind.NativeType || value.Location == "" || value.Location == "global" || value.Location != strings.ToLower(value.Location) {
		return nil, serviceDenied("invalid_fleet_action_identity")
	}
	if _, err := c.fleetRecordedReferences(value); err != nil {
		return nil, err
	}
	id := value.Identity.NativeID
	deletion, err := c.fleetRequest(kind.NativeType, fleetParent(id, kind.NativeType), last(id), "DELETE")
	if err != nil {
		return nil, err
	}
	return &fleetAction{action: action{client: c, kind: kind, id: id, wireID: id, location: value.Location, connectionID: connection, partition: value.Identity.Partition, deletion: deletion}, planned: value}, nil
}

func (a *fleetAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Location != a.location || text(value.Normalized[fleetReferencesProof]) != text(a.planned.Normalized[fleetReferencesProof]) || len(request.PrerequisiteDeletions) != 0 || len(request.Parameters) != 0 {
		return serviceDenied("fleet_action_request_changed")
	}
	if _, err := a.serviceImpacts(request); err != nil {
		return err
	}
	if request.ExecutionResult != nil {
		return a.verifyPhase(request, *request.ExecutionResult)
	}
	return nil
}

// Stop has its own LRO and may finish while the run is still Stopping. Keep
// the current operation inside the signed phase because the execution worker
// preserves the original ProviderOperationID across Wait data updates.
func (a *fleetAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.IdempotencyKey, request.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "fleet_phase_binding")
	return a.client.privateConfiguration(map[string]any{"request": request, "origin": result.ProviderOperationID, "data": data, "protocol": "fleet-cleanup-1"})
}

func (a *fleetAction) verifyPhase(request contracts.ActionRequest, result contracts.ActionResult) error {
	phase := text(result.Data["fleet_phase"])
	if phase != "delete" && (phase != "stop" || a.kind.NativeType != fleetRunType) || text(result.Data["fleet_phase_binding"]) != a.phaseBinding(request, result) {
		return serviceDenied("fleet_cleanup_receipt_changed")
	}
	return nil
}

func (a *fleetAction) phaseResult(request contracts.ActionRequest, phase string, res response) (contracts.ActionResult, error) {
	result, err := a.fleetOperationResult(res)
	if err != nil {
		return result, err
	}
	result.Data = map[string]any{"fleet_phase": phase, "fleet_phase_operation": result.ProviderOperationID, "fleet_operation": result.Data}
	result.Data["fleet_phase_binding"] = a.phaseBinding(request, result)
	return result, nil
}

func fleetRunState(raw map[string]any) (string, error) {
	props := object(raw["properties"])
	status := object(props["status"])
	progress := object(status["status"])
	state := text(progress["state"])
	if monitorRuleFields(status, "status") != nil || monitorRuleFields(progress, "state") != nil || !slices.Contains([]string{"NotStarted", "Running", "Pending", "Stopping", "Stopped", "Skipped", "Failed", "Completed"}, state) {
		return "", serviceDenied("unknown_fleet_run_state")
	}
	return state, nil
}

// The native lifecycle permits Stop for Skipped as well as Running/Pending;
// wait for Stopped instead of treating Skipped as terminal.
// https://learn.microsoft.com/azure/kubernetes-fleet/concepts-update-orchestration#update-run-states
func fleetRunNeedsStop(state string) bool {
	return state == "Running" || state == "Pending" || state == "Skipped"
}

func (a *fleetAction) preflight(ctx context.Context, request contracts.ActionRequest) (raw response, check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return raw, check, err
	}
	raw, err = a.client.fleetRead(ctx, a.kind.NativeType, a.id)
	if isNotFound(err) {
		read, err := a.serviceCascadeReadback(ctx, request)
		return response{}, contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
	}
	if err != nil {
		return raw, check, err
	}
	if err := a.client.fleetIncarnation(request.Asset, raw.data); err != nil {
		return raw, check, err
	}
	context, err := a.client.fleetVerifiedContext(ctx, request.Asset, raw.data)
	if err != nil {
		return raw, check, err
	}
	for _, resource := range []map[string]any{raw.data, object(context["group"]), object(context["parent"])} {
		if protectedAzureTags(object(resource["tags"])) {
			return raw, check, serviceDenied("azure_protected_tag")
		}
	}
	if text(object(context["group"])["managedBy"]) != "" {
		return raw, check, serviceDenied("azure_managed_resource_group")
	}
	properties := object(raw.data["properties"])
	state := text(properties["provisioningState"])
	// The stable managed-namespace GET example omits this optional field.
	// Its authored policy and native ETag still bind the conditional deletion.
	missingNamespaceState := a.kind.NativeType == fleetNamespaceType && properties["provisioningState"] == nil
	if !missingNamespaceState && !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Deleting"}, state) {
		return raw, check, serviceDenied("fleet_resource_not_ready")
	}
	if a.kind.NativeType == fleetRunType && state != "Deleting" {
		if _, err := fleetRunState(raw.data); err != nil {
			return raw, check, err
		}
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return raw, check, err
	}
	if locked(a.id, locks) {
		return raw, check, serviceDenied("azure_management_lock")
	}
	if err := a.serviceCascadePreflight(ctx, request, raw.data, locks); err != nil {
		return raw, check, err
	}
	return raw, contracts.PreflightResult{Allowed: true}, nil
}

func (a *fleetAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	_, check, err := a.preflight(ctx, request)
	return check, err
}

func (a *fleetAction) mutate(ctx context.Context, request contracts.ActionRequest, raw response, phase string) (contracts.ActionResult, error) {
	etag := text(raw.data["eTag"])
	if raw.data["eTag"] != etag || etag == "" || etag == "*" || len(etag) > 8192 || strings.TrimSpace(etag) != etag || strings.ContainsAny(etag, "\x00\r\n\t") || len(raw.header.Values("ETag")) > 1 || raw.header.Get("ETag") != "" && raw.header.Get("ETag") != etag {
		return contracts.ActionResult{}, serviceDenied("invalid_fleet_conditional_etag")
	}
	operation := a.deletion
	if phase == "stop" {
		var err error
		operation, err = a.client.fleetRequest(a.kind.NativeType, fleetParent(a.id, a.kind.NativeType), last(a.id), "POST")
		if err != nil {
			return contracts.ActionResult{}, err
		}
	}
	headers := maps.Clone(operation.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["If-Match"] = etag
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":fleet:" + phase)
	}
	res, err := a.client.requestBody(ctx, operation.Method, operation.URL, operation.Body, headers)
	if isNotFound(err) {
		return a.phaseResult(request, "delete", response{})
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if len(res.data) != 0 {
		if err := a.client.fleetIncarnation(request.Asset, res.data); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return a.phaseResult(request, phase, res)
}

func (a *fleetAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	raw, _, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if raw.data == nil || object(raw.data["properties"])["provisioningState"] == "Deleting" {
		return a.phaseResult(request, "delete", response{})
	}
	if a.kind.NativeType == fleetRunType {
		state, _ := fleetRunState(raw.data) // Preflight validated the native state.
		if state == "Stopping" {
			return a.phaseResult(request, "stop", response{})
		}
		if fleetRunNeedsStop(state) {
			return a.mutate(ctx, request, raw, "stop")
		}
	}
	return a.mutate(ctx, request, raw, "delete")
}

func (a *fleetAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.verifyPhase(request, result); err != nil {
		return contracts.WaitResult{}, err
	}
	operation := result
	operation.ProviderOperationID = text(result.Data["fleet_phase_operation"])
	operation.Data = object(result.Data["fleet_operation"])
	poll, err := a.fleetPoll(ctx, operation)
	if err != nil {
		return poll, err
	}
	if poll.Data != nil {
		result.Data = maps.Clone(result.Data)
		result.Data["fleet_operation"] = poll.Data
		result.Data["fleet_phase_binding"] = a.phaseBinding(request, result)
		poll.Data = result.Data
	}
	if !poll.Done {
		return poll, nil
	}
	if result.Data["fleet_phase"] == "stop" {
		raw, _, err := a.preflight(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if raw.data != nil && object(raw.data["properties"])["provisioningState"] != "Deleting" {
			state, _ := fleetRunState(raw.data)
			if fleetRunNeedsStop(state) || state == "Stopping" {
				return contracts.WaitResult{State: state, RetryAfter: 2 * time.Second, Data: poll.Data}, nil
			}
		}
		var next contracts.ActionResult
		if raw.data == nil || object(raw.data["properties"])["provisioningState"] == "Deleting" {
			next, err = a.phaseResult(request, "delete", response{})
		} else {
			next, err = a.mutate(ctx, request, raw, "delete")
		}
		if err != nil {
			return contracts.WaitResult{}, err
		}
		next.ProviderOperationID = result.ProviderOperationID
		next.Data["fleet_phase_binding"] = a.phaseBinding(request, next)
		return contracts.WaitResult{State: "Deleting", RetryAfter: next.RetryAfter, Data: next.Data}, nil
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State, Data: poll.Data}, err
}

func (a *fleetAction) Readback(ctx context.Context, request contracts.ActionRequest) (out contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return out, err
	}
	live, err := a.client.fleetRead(ctx, a.kind.NativeType, a.id)
	if isNotFound(err) {
		return a.serviceCascadeReadback(ctx, request)
	}
	if err != nil {
		return out, err
	}
	if err := a.client.fleetIncarnation(request.Asset, live.data); err != nil {
		return out, err
	}
	if err := a.client.fleetContext(ctx, request.Asset, live.data); err != nil {
		return out, err
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(live.data["properties"])["provisioningState"])}, nil
}

var _ contracts.ActionDriver = (*fleetAction)(nil)
