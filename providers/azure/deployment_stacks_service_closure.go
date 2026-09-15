package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackServiceClosure struct {
	Parents []asset.AssetID
	// These children require their own product lifecycle, which may include an
	// abort, unlink or other preparation not performed by a native Stack DELETE.
	DirectChildren []contracts.ActionImpact
}

// Validate the existing service-specific child enumerations against the frozen
// Stack plan. Native membership does not establish ownership of implicit children.
// Parents lists exactly which service closures were checked; other resource
// families, attachments and managed groups still need their own checks.
func (c *client) deploymentStackObserveServiceClosure(ctx context.Context, req contracts.ActionRequest) (out deploymentStackServiceClosure, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackServiceClosure{}
			err = contracts.DependencyReadError(err)
		}
	}()
	if _, _, err = c.deploymentStackDeletePlan(req); err != nil {
		return out, err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	byID := map[string]contracts.ActionImpact{}
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
		byID[strings.ToLower(impact.Asset.Identity.NativeID)] = impact
	}
	if err = c.deploymentStackObserveMembers(ctx, req.Asset, members); err != nil {
		return out, err
	}
	native := object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])
	direct := map[asset.AssetID]bool{}
	observed := map[string]bool{}
	checked := map[asset.AssetID]asset.Asset{}
	for _, impact := range req.LifecycleImpacts {
		parent := impact.Asset
		if !impact.Delete || !HasServiceCascade(parent.Identity.NativeType) {
			continue
		}
		current, failure := c.deploymentStackMemberRead(ctx, parent)
		if failure != nil {
			return out, failure
		}
		children, failure := c.plannedServiceChildren(ctx, parent, current.data, members...)
		if failure != nil {
			return out, failure
		}
		seen := map[string]bool{}
		for _, child := range children {
			id := strings.ToLower(child.id)
			reviewed, found := byID[id]
			if seen[id] || !found || !strings.EqualFold(child.kind, reviewed.Asset.Identity.NativeType) || !reviewed.Delete {
				return out, serviceDenied("deployment_stack_service_child_not_reviewed")
			}
			seen[id] = true
			observed[id] = true
			// An explicitly listed child can have the Stack as its execution controller.
			// Implicit children require the native product controller from the plan.
			if native[id] == nil && reviewed.ControllerID != parent.ID {
				return out, serviceDenied("deployment_stack_service_child_controller_changed")
			}
			live, failure := c.deploymentStackMemberRead(ctx, reviewed.Asset)
			if failure != nil {
				return out, failure
			}
			if failure = c.servicePrivateIncarnation(reviewed.Asset, live.data); failure != nil {
				return out, failure
			}
			if failure = serviceIncarnation(reviewed.Asset, live.data); failure != nil {
				return out, failure
			}
			kind, known := findType(reviewed.Asset.Identity.NativeType)
			if !known {
				return out, serviceDenied("deployment_stack_service_child_kind_unknown")
			}
			if reason := protectionReason(kind, live.data); reason != "" && !serviceIntrinsicChild(parent.Identity.NativeType, child.kind, reason) {
				return out, serviceDenied(reason)
			}
			if child.direct && !direct[reviewed.Asset.ID] {
				direct[reviewed.Asset.ID] = true
				out.DirectChildren = append(out.DirectChildren, reviewed)
			}
		}
		out.Parents = append(out.Parents, parent.ID)
		checked[parent.ID] = parent
	}
	for id, impact := range byID {
		parent, covered := checked[impact.ControllerID]
		if native[id] == nil && covered && slices.ContainsFunc(serviceChildKinds(parent.Identity.NativeType), func(kind string) bool { return strings.EqualFold(kind, impact.Asset.Identity.NativeType) }) && !observed[id] {
			return out, serviceDenied("deployment_stack_service_child_membership_changed")
		}
	}

	if err = c.deploymentStackObserveMembers(ctx, req.Asset, members); err != nil {
		return out, err
	}
	slices.Sort(out.Parents)
	slices.SortFunc(out.DirectChildren, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	return out, nil
}
