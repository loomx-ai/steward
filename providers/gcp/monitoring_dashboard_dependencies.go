package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) monitoringDashboardPolicyReference(data map[string]any, policy string) monitoringReference {
	nameReference := func(value string, relative bool) monitoringReference {
		parts := strings.Split(value, "/")
		if relative {
			if len(parts) != 2 || parts[0] != "alertPolicies" || !uptimeSegment(parts[1]) {
				return monitoringUnresolvedReference
			}
			value = "projects/" + c.project + "/" + value
		} else if len(parts) != 4 || parts[0] != "projects" || (!projectPattern.MatchString(parts[1]) && !firewallNumericID(parts[1])) || parts[2] != "alertPolicies" || !uptimeSegment(parts[3]) {
			return monitoringUnresolvedReference
		}
		if strings.ContainsAny(value, "${}") {
			return monitoringUnresolvedReference
		}
		return monitoringBool(c.canonicalName("//monitoring.googleapis.com/"+value) == policy)
	}
	return monitoringDashboardReference(data, func(kind string, data map[string]any) monitoringReference {
		switch kind {
		case "AlertChart":
			return nameReference(text(data["name"]), false)
		case "IncidentList":
			values, ok := data["policyNames"]
			// No explicit policy filter includes this project's policies. Resource
			// selectors do not prove that the policy can never produce an incident.
			if !ok {
				return monitoringHasReference
			}
			names, ok := values.([]any)
			if !ok {
				return monitoringUnresolvedReference
			}
			if len(names) == 0 {
				return monitoringHasReference
			}
			result := monitoringNoReference
			for _, raw := range names {
				result = monitoringOr(result, nameReference(text(raw), true))
			}
			return result
		}
		return monitoringNoReference
	})
}

func (h *monitoringDependencies) monitoringDashboardPolicyDependencies(ctx context.Context, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	targets := []asset.Asset{}
	for _, value := range assets {
		if value.ClosedAt == nil && value.Identity.Provider == asset.ProviderGCP && value.Identity.ConnectionID == h.connection && value.Identity.NativeType == alertPolicyType {
			targets = append(targets, value)
		}
	}
	if len(targets) == 0 {
		return result, nil
	}
	validate := func(value asset.Asset) error {
		if !gcpPartition(value.Identity.Partition) {
			return groupDenied("monitoring_policy_partition_invalid")
		}
		live, err := h.client.monitoringRead(ctx, alertPolicyType, value.Identity.NativeID)
		if err != nil {
			return contracts.DependencyReadError(err)
		}
		if monitoringConfiguration(alertPolicyType, value.Identity.NativeID, live) != text(value.Normalized[alertPolicyReview]) {
			return groupDenied("monitoring_configuration_changed")
		}
		return nil
	}
	for _, value := range targets {
		if err := validate(value); err != nil {
			return result, err
		}
	}
	dashboards, err := h.client.monitoringGroupConsumerSnapshot(ctx, monitoringDashboardType)
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(dashboards))
	for id := range dashboards {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, target := range targets {
		for _, id := range ids {
			reference := h.client.monitoringDashboardPolicyReference(dashboards[id], target.Identity.NativeID)
			if reference == monitoringNoReference {
				continue
			}
			reason := "monitoring_dashboard_policy_reference_unresolved"
			if reference == monitoringHasReference {
				consumer, found, err := findManagedAsset(assets, target, monitoringDashboardType, id)
				if err != nil {
					return result, err
				}
				if found && consumer.ClosedAt == nil && text(consumer.Normalized[monitoringDashboardReview]) == monitoringDashboardConfiguration(id, dashboards[id]) {
					result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: consumer.ID, Type: graph.RelationshipDependsOn, Source: monitoringDependencySource, Confidence: 1, Evidence: map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: string(graph.AuthorityAuthoritative), graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "native_dashboard": id, "configuration": consumer.Normalized[monitoringDashboardReview]}})
					continue
				}
				reason = "monitoring_dashboard_refresh_required"
			}
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, ControllerID: target.ID, NativeType: monitoringDashboardType, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"reason": reason, "source": monitoringDependencySource}})
		}
	}
	for _, value := range targets {
		if err := validate(value); err != nil {
			return result, err
		}
	}
	return result, nil
}
func (a *action) monitoringDashboardPolicyIncoming(ctx context.Context) error {
	dashboards, err := a.client.monitoringGroupConsumerSnapshot(ctx, monitoringDashboardType)
	if err != nil {
		return err
	}
	for _, data := range dashboards {
		switch a.client.monitoringDashboardPolicyReference(data, a.identity.NativeID) {
		case monitoringHasReference:
			return groupDenied("monitoring_policy_referenced_by_dashboard")
		case monitoringUnresolvedReference:
			return groupDenied("monitoring_dashboard_policy_reference_unresolved")
		}
	}
	return nil
}
