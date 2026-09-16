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
	// Native cascades whose parent matches the verified product projection.
	// Direct prerequisites are never covered by this map.
	CascadeParents map[asset.AssetID]asset.AssetID
	// Native prerequisite relationships can have multiple parents. They are not
	// replacements for the frozen plan's execution-controller graph.
	Prerequisites map[asset.AssetID][]asset.AssetID
	// These children require their own product lifecycle, which may include an
	// abort, unlink or other preparation not performed by a native Stack DELETE.
	DirectChildren []contracts.ActionImpact
}

// Validate the existing service-specific child enumerations against the frozen
// Stack plan. Native membership does not establish ownership of implicit children.
// Parents lists exactly which service closures were checked; other resource
// families, attachments and managed groups still need their own checks.
func (c *client) deploymentStackObserveServiceClosure(ctx context.Context, req contracts.ActionRequest, preparations ...map[string]any) (out deploymentStackServiceClosure, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackServiceClosure{}
			err = contracts.DependencyReadError(err)
		}
	}()
	if _, _, err = c.deploymentStackDeletePlan(req); err != nil {
		return out, err
	}
	configurations, err := c.deploymentStackPreparedConfigurations(req, preparations)
	if err != nil {
		return out, err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	byAsset := map[asset.AssetID]asset.Asset{}
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
		byAsset[impact.Asset.ID] = impact.Asset
	}
	return c.deploymentStackCheckServiceClosure(ctx, req, configurations, func() (deploymentStackObservedProgress, error) {
		err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations)
		return deploymentStackObservedProgress{Members: byAsset}, err
	})
}

func (r *Runtime) deploymentStackObserveServiceClosureWithProgress(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress) (out deploymentStackServiceClosure, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	configurations, err := c.deploymentStackPreparedConfigurations(req, progress.Preparations)
	if err != nil {
		return out, err
	}
	return c.deploymentStackCheckServiceClosure(ctx, req, configurations, func() (deploymentStackObservedProgress, error) {
		return r.deploymentStackObserveProgress(ctx, req, progress)
	})
}

func (c *client) deploymentStackCheckServiceClosure(ctx context.Context, req contracts.ActionRequest, configurations map[string]any, observe func() (deploymentStackObservedProgress, error)) (out deploymentStackServiceClosure, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackServiceClosure{}
			err = contracts.DependencyReadError(err)
		}
	}()
	state, err := observe()
	if err != nil {
		return out, err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	byID := map[string]contracts.ActionImpact{}
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
		byID[strings.ToLower(impact.Asset.Identity.NativeID)] = impact
	}
	native := object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])
	projected, err := deploymentStackProductImpacts(req)
	if err != nil {
		return out, err
	}
	controllers := map[asset.AssetID]asset.AssetID{}
	for _, impact := range projected {
		controllers[impact.Asset.ID] = impact.ControllerID
	}
	direct := map[asset.AssetID]bool{}
	observed := map[string]bool{}
	checked := map[asset.AssetID]asset.Asset{}
	for _, impact := range req.LifecycleImpacts {
		parent := impact.Asset
		if !impact.Delete || state.Completed[parent.ID] || !HasServiceCascade(parent.Identity.NativeType) {
			continue
		}
		if current, found := state.Members[parent.ID]; found {
			parent = current
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
			if state.Completed[reviewed.Asset.ID] {
				if _, failure := c.deploymentStackMemberRead(ctx, reviewed.Asset); !isNotFound(failure) {
					if failure != nil {
						return out, failure
					}
					return out, serviceDenied("deployment_stack_completed_service_child_reappeared")
				}
				continue // An independently completed member may linger in a list.
			}
			planned := reviewed.Asset
			if current, found := state.Members[planned.ID]; found {
				planned = current
			}
			live, failure := c.deploymentStackMemberRead(ctx, reviewed.Asset)
			if failure != nil {
				return out, failure
			}
			if failure = c.deploymentStackPreparedMember(planned, live.data, object(configurations[id])); failure != nil {
				return out, failure
			}
			kind, known := findType(reviewed.Asset.Identity.NativeType)
			if !known {
				return out, serviceDenied("deployment_stack_service_child_kind_unknown")
			}
			if reason := protectionReason(kind, live.data); reason != "" && !serviceIntrinsicChild(parent.Identity.NativeType, child.kind, reason) {
				return out, serviceDenied(reason)
			}
			if !child.direct && controllers[reviewed.Asset.ID] == parent.ID {
				if out.CascadeParents == nil {
					out.CascadeParents = map[asset.AssetID]asset.AssetID{}
				}
				out.CascadeParents[reviewed.Asset.ID] = parent.ID
			}
			if child.direct {
				if out.Prerequisites == nil {
					out.Prerequisites = map[asset.AssetID][]asset.AssetID{}
				}
				out.Prerequisites[parent.ID] = append(out.Prerequisites[parent.ID], reviewed.Asset.ID)
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
		if !state.Completed[impact.Asset.ID] && native[id] == nil && covered && slices.ContainsFunc(serviceChildKinds(parent.Identity.NativeType), func(kind string) bool { return strings.EqualFold(kind, impact.Asset.Identity.NativeType) }) && !observed[id] {
			return out, serviceDenied("deployment_stack_service_child_membership_changed")
		}
	}

	if _, err = observe(); err != nil {
		return out, err
	}
	for id := range out.Prerequisites {
		slices.Sort(out.Prerequisites[id])
	}
	slices.Sort(out.Parents)
	slices.SortFunc(out.DirectChildren, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	return out, nil
}
