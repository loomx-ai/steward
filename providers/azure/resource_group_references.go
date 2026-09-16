package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Exists only within one group preflight pass, after native group/Monitor indexes
// and own reads have been reconciled. It is neither persisted nor supplied through
// action parameters. Product Execute and final Readback never receive this scope.
type resourceGroupMonitorScope struct {
	client  *client
	group   string
	targets map[string]asset.Asset
	indexed map[string]any
}

func (s *resourceGroupMonitorScope) owns(target asset.Asset, source monitorIncomingSource) (bool, error) {
	if s == nil {
		return false, nil
	}
	planned, ok := s.targets[strings.ToLower(target.Identity.NativeID)]
	if !ok || s.client.privateConfiguration(map[string]any{"asset": planned}) != s.client.privateConfiguration(map[string]any{"asset": target}) {
		return false, serviceDenied("resource_group_reference_target_changed")
	}
	resource := source.resource
	member, ok := s.targets[resource.id]
	if !ok || monitorResourceKind(resource.kind) == "" || !inResourceGroup(resource.id, s.group) || member.Identity.NativeType != resource.kind || s.indexed[resource.id] == nil {
		return false, nil
	}
	snapshot := s.client.privateConfiguration(monitorResourceSnapshot(resource.kind, resource.data))
	if snapshot != text(member.Normalized[monitorConfigurationProof]) || snapshot != s.client.privateConfiguration(monitorResourceSnapshot(resource.kind, object(s.indexed[resource.id]))) || source.group == nil || !strings.EqualFold(text(source.group["id"]), s.group) || text(member.Normalized[monitorGroupProof]) != s.client.privateConfiguration(insightsWorkspaceResourceSnapshot(source.group)) {
		return false, serviceDenied("resource_group_reference_source_changed")
	}
	if err := s.client.monitorReferencesUnchanged(member, source.references); err != nil {
		return false, err
	}
	return true, nil
}

// Preserve the real product checks while allowing verified group-local Monitor
// references. Native preparation and every member's outcome remain separate.
func resourceGroupPreflightProduct(ctx context.Context, driver contracts.ActionDriver, req contracts.ActionRequest, parentKind string, scope *resourceGroupMonitorScope, managed *resourceGroupManagedPreflight) (contracts.PreflightResult, error) {
	switch a := driver.(type) {
	case *monitorTargetAction:
		filtered, targets, err := a.request(ctx, req)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		check, err := resourceGroupPreflightProduct(ctx, a.inner, filtered, parentKind, scope, managed)
		if err == nil && check.Allowed {
			err = a.dependenciesInGroup(ctx, req, targets, scope)
		}
		return check, err
	case *monitorAction:
		return a.preflightWithManagedGroup(ctx, req, scope, managed)
	case *action:
		return a.preflightWithManagedGroup(ctx, req, parentKind, nil, managed)
	default:
		return deploymentStackPreflightInParent(ctx, driver, req, parentKind)
	}
}
