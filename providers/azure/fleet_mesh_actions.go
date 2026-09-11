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

func (a *fleetAction) meshExpectedConfiguration(request contracts.ActionRequest) string {
	if request.ExecutionResult != nil {
		return text(request.ExecutionResult.Data["fleet_mesh_configuration"])
	}
	return text(request.Asset.Normalized[fleetConfigurationProof])
}

func (a *fleetAction) incarnation(request contracts.ActionRequest, raw map[string]any) error {
	if a.kind.NativeType != fleetMeshType {
		return a.client.fleetIncarnation(request.Asset, raw)
	}
	// identity authenticates the original inventory and any phase receipt.
	// A conditional PUT legitimately changes the configuration and systemData;
	// only its signed response digest can replace the original expected digest.
	if fleetValidate(fleetMeshType, raw) != nil || !strings.EqualFold(text(raw["id"]), a.id) || a.client.privateConfiguration(fleetSnapshot(fleetMeshType, raw)) != a.meshExpectedConfiguration(request) {
		return serviceDenied("fleet_mesh_configuration_changed")
	}
	return nil
}

func (a *fleetAction) meshPreflightMembers(ctx context.Context, request contracts.ActionRequest, raw response, locks []any) (response, contracts.PreflightResult, error) {
	previous := object(request.Asset.Normalized[fleetMeshState])
	state, members, err := a.client.readFleetMesh(ctx, a.id, raw.data, previous)
	if err != nil {
		return raw, contracts.PreflightResult{}, err
	}
	phase := ""
	if request.ExecutionResult != nil {
		phase = text(request.ExecutionResult.Data["fleet_phase"])
	}
	preparing := phase == "mesh-prepare" || phase == "mesh-apply" || phase == "delete"
	if !preparing && a.client.privateConfiguration(previous) != a.client.privateConfiguration(state) {
		return raw, contracts.PreflightResult{}, serviceDenied("fleet_mesh_membership_changed")
	}
	profileState := text(object(object(raw.data["properties"])["status"])["state"])
	if !slices.Contains([]string{"NotConnected", "Applying", "Connected", "Degraded", "Failed"}, profileState) {
		return raw, contracts.PreflightResult{}, serviceDenied("unknown_fleet_mesh_state")
	}
	busy := profileState == "Applying"
	for id, member := range members {
		if a.client.privateConfiguration(object(object(previous["members"])[id])) != a.client.privateConfiguration(a.client.fleetMeshMemberSnapshot(member)) {
			return raw, contracts.PreflightResult{}, serviceDenied("fleet_mesh_member_changed")
		}
		if locked(id, locks) || protectedAzureTags(object(member["tags"])) {
			return raw, contracts.PreflightResult{}, serviceDenied("fleet_mesh_member_protected")
		}
		memberState := text(object(object(object(member["properties"])["meshProperties"])["status"])["state"])
		if !slices.Contains([]string{"Connecting", "Connected", "Disconnecting", "Failed"}, memberState) {
			return raw, contracts.PreflightResult{}, serviceDenied("unknown_fleet_mesh_member_state")
		}
		busy = busy || memberState == "Connecting" || memberState == "Disconnecting"
	}
	return raw, contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"mesh_busy": busy, "mesh_attached": len(members)}}, nil
}

func fleetMeshSelectorEmpty(raw map[string]any) bool {
	selector := object(object(raw["properties"])["memberSelector"])
	return len(selector) == 0 || len(selector) == 1 && selector["byLabel"] == ""
}

func (a *fleetAction) meshAdvance(ctx context.Context, request contracts.ActionRequest, raw response, check contracts.PreflightResult) (contracts.ActionResult, error) {
	if check.Evidence["mesh_busy"] == true {
		return a.phaseResult(request, "mesh-wait", response{})
	}
	if check.Evidence["mesh_attached"] == 0 {
		return a.mutate(ctx, request, raw, "delete")
	}
	if fleetMeshSelectorEmpty(raw.data) {
		return a.mutate(ctx, request, raw, "mesh-apply")
	}
	return a.mutate(ctx, request, raw, "mesh-prepare")
}

func fleetMeshDisconnectBody(raw map[string]any) (map[string]any, error) {
	props := maps.Clone(object(raw["properties"]))
	selector := object(props["memberSelector"])
	for key := range selector {
		if key != "byLabel" {
			return nil, serviceDenied("unsupported_fleet_mesh_selector")
		}
	}
	// The native contract defines an empty selector as selecting no members.
	// Preserve all other authored fields, including future private settings.
	props["memberSelector"] = map[string]any{"byLabel": ""}
	delete(props, "provisioningState")
	delete(props, "status")
	body := maps.Clone(raw)
	for _, key := range []string{"id", "name", "type", "eTag", "systemData"} {
		delete(body, key)
	}
	body["properties"] = props
	return body, nil
}

func (a *fleetAction) meshMutate(ctx context.Context, request contracts.ActionRequest, raw response, phase, etag string) (contracts.ActionResult, error) {
	operation := a.deletion
	var expected map[string]any
	if phase == "mesh-prepare" {
		body, err := fleetMeshDisconnectBody(raw.data)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		operation, err = a.client.fleetRequest(fleetMeshType, fleetParent(a.id, fleetMeshType), last(a.id), "PUT", body)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		expected = fleetSnapshot(fleetMeshType, raw.data)
		expected["properties"] = body["properties"]
		delete(expected, "systemData")
	} else if phase == "mesh-apply" {
		if !fleetMeshSelectorEmpty(raw.data) {
			return contracts.ActionResult{}, serviceDenied("fleet_mesh_disconnect_not_prepared")
		}
		var err error
		operation, err = a.client.fleetRequest(fleetMeshType, fleetParent(a.id, fleetMeshType), last(a.id), "POST")
		if err != nil {
			return contracts.ActionResult{}, err
		}
	} else if phase != "delete" {
		return contracts.ActionResult{}, serviceDenied("invalid_fleet_mesh_mutation_phase")
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
	if phase == "mesh-prepare" {
		if res.status != 200 && res.status != 201 || fleetValidate(fleetMeshType, res.data) != nil || !strings.EqualFold(text(res.data["id"]), a.id) {
			return contracts.ActionResult{}, serviceDenied("invalid_fleet_mesh_update_response")
		}
		actual := fleetSnapshot(fleetMeshType, res.data)
		delete(actual, "systemData")
		if a.client.privateConfiguration(expected) != a.client.privateConfiguration(actual) {
			return contracts.ActionResult{}, serviceDenied("fleet_mesh_update_configuration_changed")
		}
		// The native PUT's 201 envelope is also synchronous; retain its native
		// body and headers while using the existing operation-envelope parser.
		parsed := res
		if parsed.status == 201 {
			parsed.status = 200
		}
		result, err := a.phaseResult(request, phase, parsed)
		if err != nil {
			return result, err
		}
		result.Data["fleet_mesh_configuration"] = a.client.privateConfiguration(fleetSnapshot(fleetMeshType, res.data))
		result.Data["fleet_phase_binding"] = a.phaseBinding(request, result)
		return result, nil
	}
	if phase == "delete" && res.status != 202 && res.status != 204 || phase == "mesh-apply" && res.status != 200 && res.status != 202 {
		return contracts.ActionResult{}, serviceDenied("invalid_fleet_mesh_mutation_response")
	}
	if len(res.data) != 0 {
		if err := a.incarnation(request, res.data); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return a.phaseResult(request, phase, res)
}

func (a *fleetAction) meshWait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	raw, check, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if raw.data == nil {
		return contracts.WaitResult{Done: check.Absent, State: "Disconnecting", RetryAfter: 2 * time.Second, Data: result.Data}, nil
	}
	if check.Evidence["mesh_busy"] == true {
		return contracts.WaitResult{State: "Disconnecting", RetryAfter: 2 * time.Second, Data: result.Data}, nil
	}
	if result.Data["fleet_phase"] == "mesh-apply" {
		if check.Evidence["mesh_attached"] != 0 || !fleetMeshSelectorEmpty(raw.data) {
			return contracts.WaitResult{}, serviceDenied("fleet_mesh_disconnection_not_completed")
		}
	}
	next, err := a.meshAdvance(ctx, request, raw, check)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	next.ProviderOperationID = result.ProviderOperationID
	next.Data["fleet_phase_binding"] = a.phaseBinding(request, next)
	return contracts.WaitResult{State: "Disconnecting", RetryAfter: next.RetryAfter, Data: next.Data}, nil
}

func (a *fleetAction) meshResidualReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	state := object(request.Asset.Normalized[fleetMeshState]) // identity authenticated it before the root read.
	var known []asset.Asset
	for id := range object(state["members"]) {
		known = append(known, asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeType: fleetMemberType, NativeID: id}})
	}
	members := map[string]map[string]any{}
	parent := fleetParent(a.id, fleetMeshType)
	_, err := a.client.fleetRead(ctx, fleetType, parent)
	if err == nil {
		members, err = a.client.fleetKnownIndex(ctx, fleetMemberType, parent, known)
	} else if isNotFound(err) {
		members, err = a.client.fleetRecoverKnown(ctx, fleetMemberType, parent, members, known)
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, member := range members {
		profile, err := fleetMemberMesh(member)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if profile == a.id {
			return contracts.ReadbackResult{Exists: true, State: "Disconnecting"}, nil
		}
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
