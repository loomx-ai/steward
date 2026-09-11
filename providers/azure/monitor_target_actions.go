package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitorTargetReceipt = "_monitor_target_request"

// All ARM action families share this dependency boundary. The native driver
// keeps its own mutation, polling phases, receipts and residual checks.
type monitorTargetAction struct {
	client  *client
	inner   contracts.ActionDriver
	planned asset.Asset
}

func (a *monitorTargetAction) DeletionCheckTimeout() time.Duration {
	if provider, ok := a.inner.(contracts.DeletionCheckTimeoutProvider); ok {
		return provider.DeletionCheckTimeout()
	}
	return time.Hour
}

func (a *monitorTargetAction) receipt(request contracts.ActionRequest) string {
	request.IdempotencyKey = ""
	request.ExecutionResult = nil
	return a.client.privateConfiguration(map[string]any{"request": request, "protocol": "native-monitor-target-1"})
}

func (a *monitorTargetAction) resultForInner(request contracts.ActionRequest, result contracts.ActionResult) (contracts.ActionResult, error) {
	if text(result.Data[monitorTargetReceipt]) != a.receipt(request) {
		return contracts.ActionResult{}, serviceDenied("monitor_target_receipt_changed")
	}
	result.Data = maps.Clone(result.Data)
	delete(result.Data, monitorTargetReceipt)
	if diagnostic, ok := a.inner.(*diagnosticAction); ok {
		if err := diagnostic.verifyReceipt(result); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	if fleet, ok := a.inner.(*fleetAction); ok {
		innerRequest := request
		innerRequest.PrerequisiteDeletions = nil // This wrapper binds and verifies their full original request.
		if err := fleet.verifyPhase(innerRequest, result); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return result, nil
}

func (a *monitorTargetAction) request(ctx context.Context, request contracts.ActionRequest) (filtered contracts.ActionRequest, targets []asset.Asset, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	value := request.Asset
	if request.Action != "delete" || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Location != a.planned.Location || value.Identity.ConnectionID == "" {
		return filtered, nil, serviceDenied("monitor_target_action_identity_changed")
	}
	filtered = request
	if proof := text(a.planned.Normalized[rbacIdentityProof]); proof != "" && text(value.Normalized[rbacIdentityProof]) != proof {
		return filtered, nil, serviceDenied("monitor_target_principal_identity_changed")
	}
	if diagnostic, ok := a.inner.(*diagnosticAction); ok {
		identity := request
		identity.PrerequisiteDeletions = nil // This wrapper authenticates the prerequisites below.
		if err := diagnostic.identity(identity); err != nil {
			return filtered, nil, err
		}
	}
	if fleet, ok := a.inner.(*fleetAction); ok {
		identity := request
		identity.PrerequisiteDeletions, identity.ExecutionResult = nil, nil
		if err := fleet.identity(identity); err != nil {
			return filtered, nil, err
		}
	}
	if request.ExecutionResult != nil {
		result, err := a.resultForInner(request, *request.ExecutionResult)
		if err != nil {
			return filtered, nil, err
		}
		filtered.ExecutionResult = &result
	}
	targets = []asset.Asset{value}
	seenIDs, seenAssets := map[string]bool{value.Identity.NativeID: true}, map[asset.AssetID]bool{value.ID: true}
	controllers := map[asset.AssetID]asset.AssetID{}
	for _, impact := range request.LifecycleImpacts {
		member := impact.Asset
		if member.Identity.NativeType == diagnosticSettingsType || rbacResourceKind(member.Identity.NativeType) != "" {
			return filtered, nil, serviceDenied("diagnostic_requires_independent_deletion")
		}
		if member.ID == "" || seenAssets[member.ID] || member.Identity.Provider != value.Identity.Provider || member.Identity.ConnectionID != value.Identity.ConnectionID || member.Identity.Partition != value.Identity.Partition {
			return filtered, nil, serviceDenied("invalid_monitor_target_impact")
		}
		seenAssets[member.ID] = true
		controllers[member.ID] = impact.ControllerID
		if impact.Delete && monitorARMTarget(member) {
			if seenIDs[member.Identity.NativeID] {
				return filtered, nil, serviceDenied("ambiguous_monitor_target_impact")
			}
			seenIDs[member.Identity.NativeID] = true
			targets = append(targets, member)
		}
	}
	for id, controller := range controllers {
		seen := map[asset.AssetID]bool{id: true}
		for controller != value.ID {
			if seen[controller] {
				return filtered, nil, serviceDenied("invalid_monitor_target_controller_chain")
			}
			seen[controller] = true
			var ok bool
			controller, ok = controllers[controller]
			if !ok {
				return filtered, nil, serviceDenied("invalid_monitor_target_controller_chain")
			}
		}
	}
	for _, target := range targets {
		if target.Identity.NativeType == insightsWorkspaceType {
			if _, err := a.client.monitorReceiverTargetCustomer(target); err != nil {
				return filtered, nil, err
			}
		}
	}
	filtered.PrerequisiteDeletions = nil
	for _, prerequisite := range request.PrerequisiteDeletions {
		member := prerequisite.Asset
		if member.ID == "" || seenAssets[member.ID] || seenIDs[member.Identity.NativeID] {
			return filtered, nil, serviceDenied("ambiguous_monitor_target_prerequisite")
		}
		seenIDs[member.Identity.NativeID], seenAssets[member.ID] = true, true
		if monitorResourceKind(member.Identity.NativeType) == "" && member.Identity.NativeType != diagnosticSettingsType && rbacResourceKind(member.Identity.NativeType) == "" && fleetKind(member.Identity.NativeType).kind == "" {
			filtered.PrerequisiteDeletions = append(filtered.PrerequisiteDeletions, prerequisite)
			continue // The native driver authenticates its own prerequisite families.
		}
		id, _, kind, err := monitorResourceID(member.Identity.NativeID)
		if member.Identity.NativeType == diagnosticSettingsType {
			id, _, kind, err = diagnosticResourceID(member.Identity.NativeID)
		}
		if rbacResourceKind(member.Identity.NativeType) != "" {
			id, _, kind, err = rbacResourceID(member.Identity.NativeID)
		}
		if fleetKind(member.Identity.NativeType).kind != "" {
			id, kind, err = fleetIdentity(member.Identity.NativeID)
			if kind == fleetGateType {
				return filtered, nil, serviceDenied("fleet_gate_requires_owning_run")
			}
		}
		if err != nil || id != member.Identity.NativeID || kind != member.Identity.NativeType || !strings.HasPrefix(id, a.client.root()+"/") || !prerequisite.Delete || prerequisite.ControllerID != value.ID || member.Identity.Provider != value.Identity.Provider || member.Identity.ConnectionID != value.Identity.ConnectionID || member.Identity.Partition != value.Identity.Partition {
			return filtered, nil, serviceDenied("invalid_monitor_target_prerequisite")
		}
		var refs map[string]any
		if fleetKind(kind).kind != "" {
			refs, err = a.client.fleetRecordedReferences(member)
		} else if rbacResourceKind(kind) != "" {
			refs, err = a.client.rbacRecordedReferences(member)
		} else if kind == diagnosticSettingsType {
			refs, err = a.client.diagnosticRecordedReferences(member)
		} else {
			refs, err = a.client.monitorRecordedReferences(member)
		}
		if err != nil {
			return filtered, nil, err
		}
		linked := false
		for _, target := range targets {
			if fleetKind(kind).kind != "" {
				linked = linked || fleetReferenceMatches(target, member, refs)
				continue
			}
			for kind, ids := range refs {
				for _, id := range stringValues(ids) {
					matches, err := a.client.monitorReferenceMatches(target, kind, id)
					if err != nil {
						return filtered, nil, err
					}
					linked = linked || matches
				}
			}
		}
		if !linked {
			return filtered, nil, serviceDenied("monitor_target_prerequisite_reference_changed")
		}
		if fleetKind(kind).kind != "" {
			_, err = a.client.fleetRead(ctx, kind, id)
		} else if rbacResourceKind(kind) != "" {
			_, err = a.client.rbacRead(ctx, kind, text(member.Normalized[rbacWireSelector]))
		} else if kind == diagnosticSettingsType {
			_, err = a.client.diagnosticRead(ctx, text(member.Normalized[diagnosticWireSelector]), kind)
		} else {
			_, err = a.client.monitorResourceRead(ctx, kind, id)
		}
		if !isNotFound(err) {
			if err != nil {
				return filtered, nil, err
			}
			return filtered, nil, serviceDenied("monitor_target_prerequisite_still_exists")
		}
	}
	return filtered, targets, nil
}

func (a *monitorTargetAction) ownedSource(request contracts.ActionRequest, source monitorIncomingSource) (bool, error) {
	if fleetKind(source.resource.kind).kind != "" {
		for _, impact := range request.LifecycleImpacts {
			if source.resource.kind == fleetGateType && impact.Delete && impact.ControllerID == request.Asset.ID && impact.Asset.Identity.NativeID == source.resource.id && impact.Asset.Identity.NativeType == fleetGateType && fleetChildRelation(request.Asset, impact.Asset) {
				if err := a.client.fleetIncomingUnchanged(impact.Asset, source); err != nil {
					return false, err
				}
				return true, nil
			}
		}
		return false, nil
	}
	if source.resource.kind == diagnosticSettingsType || rbacResourceKind(source.resource.kind) != "" {
		return false, nil // Extensions require independent deletion even in managed groups.
	}
	group, ok := a.client.monitorControllerGroup(request.Asset)
	if !ok || !inResourceGroup(source.resource.id, group) {
		return false, nil
	}
	for _, impact := range request.LifecycleImpacts {
		if !impact.Delete || impact.Asset.Identity.NativeID != source.resource.id || impact.Asset.Identity.NativeType != source.resource.kind {
			continue
		}
		if err := a.client.servicePrivateIncarnation(impact.Asset, source.resource.data); err != nil {
			return false, err
		}
		if err := a.client.monitorReferencesUnchanged(impact.Asset, source.references); err != nil {
			return false, err
		}
		// Only these native group controllers prove every known member absent
		// in their own readback. Their preflight validates native ownership;
		// recovery additionally authenticates the entire original request.
		return true, nil
	}
	return false, nil
}

func (a *monitorTargetAction) dependencies(ctx context.Context, request contracts.ActionRequest, targets []asset.Asset) (err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	known := []asset.Asset{}
	for _, prerequisite := range request.PrerequisiteDeletions {
		known = append(known, prerequisite.Asset)
	}
	incoming, err := a.client.monitorIncomingTargets(ctx, targets, known...)
	if err != nil {
		return err
	}
	for _, sources := range incoming {
		for _, source := range sources {
			owned, err := a.ownedSource(request, source)
			if err != nil {
				return err
			}
			if !owned {
				return serviceDenied("monitor_target_has_incoming_references")
			}
		}
	}
	return nil
}

func (a *monitorTargetAction) preflight(ctx context.Context, request contracts.ActionRequest) (contracts.ActionRequest, contracts.PreflightResult, error) {
	filtered, targets, err := a.request(ctx, request)
	if err != nil {
		return filtered, contracts.PreflightResult{}, err
	}
	check, err := a.inner.Preflight(ctx, filtered)
	if err == nil && check.Allowed {
		err = a.dependencies(ctx, request, targets)
	}
	return filtered, check, err
}

func (a *monitorTargetAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	_, check, err := a.preflight(ctx, request)
	return check, err
}

func (a *monitorTargetAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	filtered, targets, err := a.request(ctx, request)
	if err == nil {
		err = a.dependencies(ctx, request, targets)
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	// Every native Execute performs its own preflight, including actual cascade
	// ownership validation. Preserve that sequence without running it twice.
	result, err := a.inner.Execute(ctx, filtered)
	if err != nil {
		return result, err
	}
	result.Data = maps.Clone(result.Data)
	if result.Data == nil {
		result.Data = map[string]any{}
	}
	result.Data[monitorTargetReceipt] = a.receipt(request)
	return result, nil
}

func (a *monitorTargetAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	innerResult, err := a.resultForInner(request, result)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	filtered, targets, err := a.request(ctx, request)
	if err == nil {
		err = a.dependencies(ctx, request, targets)
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	waited, err := a.inner.Wait(ctx, filtered, innerResult)
	if err == nil && waited.Data != nil {
		waited.Data = maps.Clone(waited.Data)
		waited.Data[monitorTargetReceipt] = a.receipt(request)
	}
	return waited, err
}

func (a *monitorTargetAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	filtered, targets, err := a.request(ctx, request)
	if err == nil {
		err = a.dependencies(ctx, request, targets)
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return a.inner.Readback(ctx, filtered)
}

var _ contracts.ActionDriver = (*monitorTargetAction)(nil)
