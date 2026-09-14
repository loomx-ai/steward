package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) routerActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || request.IdempotencyKey == "" || len(request.Parameters) != 0 {
		return groupDenied("router_action_changed")
	}
	if err := a.client.routerSaved(request.Asset); err != nil {
		return err
	}
	ids := map[asset.AssetID]bool{request.Asset.ID: true}
	names := map[string]bool{}
	for _, group := range []struct {
		values []contracts.ActionImpact
		nat    bool
	}{{request.LifecycleImpacts, true}, {request.PrerequisiteDeletions, false}} {
		for _, impact := range group.values {
			id := impact.Asset.Identity
			if !impact.Delete || impact.ControllerID != request.Asset.ID || ids[impact.Asset.ID] || names[id.NativeID] || id.Provider != a.identity.Provider || id.ConnectionID != a.identity.ConnectionID || id.Partition != a.identity.Partition || !strings.HasPrefix(id.NativeID, a.identity.NativeID+"/") || (group.nat && id.NativeType != cloudNatType) || (!group.nat && id.NativeType != routePolicyType && id.NativeType != namedSetType) {
				return groupDenied("router_child_impact_invalid")
			}
			child := action{identity: id, client: a.client}
			child.kind, _ = findType(id.NativeType)
			if err := child.routerComponentActionIdentity(contracts.ActionRequest{Asset: impact.Asset, Action: "delete", IdempotencyKey: request.IdempotencyKey}); err != nil {
				return err
			}
			if text(impact.Asset.Normalized[child.routerComponentIncarnationKey()]) != text(request.Asset.Normalized["id"]) {
				return groupDenied("router_child_parent_changed")
			}
			ids[impact.Asset.ID], names[id.NativeID] = true, true
		}
	}
	return nil
}

func (a *action) routerPrerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	for _, impact := range request.PrerequisiteDeletions {
		if _, err := a.client.routerComponentRead(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID); !isNotFound(err) {
			if err != nil {
				return err
			}
			return groupDenied("router_prerequisite_still_exists")
		}
	}
	return nil
}

func (a *action) routerReviewLive(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	if err := a.client.routerData(a.identity.NativeID, text(request.Asset.Normalized["id"]), live); err != nil {
		return err
	}
	if protectedComputeLabels(live) || protectionReason(routerType, live) != "" {
		return groupDenied("router_protected")
	}
	if routerConfiguration(live, true) != text(request.Asset.Normalized[routerBaseReview]) {
		return groupDenied("router_base_configuration_changed")
	}
	if len(a.client.routerUseReferences(asset.Asset{Normalized: live})) != 0 {
		return groupDenied("router_in_use")
	}
	peers, err := routePolicyBGPPeers(request.Asset.Normalized)
	if err != nil {
		return err
	}
	for _, impact := range request.PrerequisiteDeletions {
		if impact.Asset.Identity.NativeType == routePolicyType {
			_, peers, err = routePolicyBGPReview(map[string]any{routePolicyPeers: peers}, last(impact.Asset.Identity.NativeID))
			if err != nil {
				return err
			}
		}
	}
	actual, err := routePolicyBGPPeers(live)
	if err != nil {
		return err
	}
	if firewallDigest(peers) != firewallDigest(actual) {
		return groupDenied("router_bgp_configuration_changed")
	}
	if err := a.routerPrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	children, err := a.client.routerLiveChildren(ctx, a.identity, live)
	if err != nil {
		return err
	}
	impacts := map[string]contracts.ActionImpact{}
	for _, impact := range request.LifecycleImpacts {
		impacts[impact.Asset.Identity.NativeID] = impact
	}
	for _, child := range children {
		if child.direct {
			return groupDenied("router_unreviewed_prerequisite")
		}
		if protectedComputeLabels(child.data) || protectionReason(child.kind, child.data) != "" {
			return groupDenied("router_nat_protected")
		}
		impact, ok := impacts[child.id]
		if !ok {
			return groupDenied("router_unreviewed_nat")
		}
		if err := routerSameChild(request.Asset, impact.Asset, child); err != nil {
			return err
		}
	}
	// Missing reviewed NATs are absent in this validated, complete native array.
	return nil
}

func (a *action) routerReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.routerActionIdentity(request); err != nil {
		return read, err
	}
	if request.ExecutionResult != nil {
		if _, err := a.routerComponentReceipt(request, *request.ExecutionResult); err != nil {
			return read, err
		}
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return a.routerAbsentReadback(ctx, request)
	}
	if err != nil {
		return read, err
	}
	if err := a.routerReviewLive(ctx, request, live); err != nil {
		if isNotFound(err) {
			// A child LIST may lose its parent while asynchronous deletion becomes visible.
			// Its 404 alone cannot prove Router absence; confirm the native parent first.
			again, parentErr := a.client.request(ctx, "GET", a.endpoint, nil)
			if isNotFound(parentErr) {
				return a.routerAbsentReadback(ctx, request)
			}
			if parentErr != nil {
				return read, parentErr
			}
			if identityErr := a.client.routerData(a.identity.NativeID, text(request.Asset.Normalized["id"]), again); identityErr != nil {
				return read, identityErr
			}
		}
		return read, err
	}
	again, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return a.routerAbsentReadback(ctx, request)
	}
	if err != nil {
		return read, err
	}
	if err := a.client.routerData(a.identity.NativeID, text(request.Asset.Normalized["id"]), again); err != nil {
		return read, err
	}
	if routerConfiguration(live, false) != routerConfiguration(again, false) {
		return read, groupDenied("router_configuration_changed_during_review")
	}
	return contracts.ReadbackResult{Exists: true, State: "router_deleting"}, nil
}

func (a *action) routerPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.routerReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) deleteRouter(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	read, err := a.routerReadback(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !read.Exists {
		return contracts.ActionResult{}, nil
	}
	return a.deleteRouterComponent(ctx, request)
}

// Parent 404 plus prerequisite readback and a second parent 404 establishes the
// documented embedded NAT cascade without treating NAT IDs as REST endpoints.
func (a *action) routerAbsentReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.routerPrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	again, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ReadbackResult{}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.client.routerData(a.identity.NativeID, text(request.Asset.Normalized["id"]), again); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: "router_deleting"}, nil
}
