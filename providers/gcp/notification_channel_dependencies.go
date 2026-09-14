package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Keep both native reference fields. A stale/invalid policy may still retain a
// per-channel strategy after its main notification list was edited.
func (c *client) alertPolicyChannels(data map[string]any) ([]string, error) {
	if err := cloudNatScalars(data, nil, nil, nil, []string{"notificationChannels"}); err != nil {
		return nil, err
	}
	values := append([]any{}, array(data["notificationChannels"])...)
	if raw, present := data["alertStrategy"]; present {
		strategy := object(raw)
		if strategy == nil {
			return nil, groupDenied("alert_policy_strategy_invalid")
		}
		entries, err := cloudNatObjects(strategy, "notificationChannelStrategy")
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := cloudNatScalars(entry, []string{"renotifyInterval"}, nil, nil, []string{"notificationChannelNames"}); err != nil {
				return nil, err
			}
			values = append(values, array(entry["notificationChannelNames"])...)
		}
	}
	result := []string{}
	for _, raw := range values {
		name, _ := raw.(string)
		parts := strings.Split(name, "/")
		if len(parts) != 4 || parts[0] != "projects" || parts[2] != "notificationChannels" || !uptimeSegment(parts[1]) || !uptimeSegment(parts[3]) {
			return nil, groupDenied("alert_policy_channel_invalid")
		}
		result = append(result, c.canonicalName("//monitoring.googleapis.com/"+name))
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// This discovers concrete own-project AlertPolicy consumers. It does not prove
// absence of Billing Budget or other external consumers and does not enable
// channel deletion. Channel cleanup remains unavailable until those scopes and
// the remaining native write contracts are supported.
func (h *monitoringDependencies) notificationChannelDependencies(ctx context.Context, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	channels := []asset.Asset{}
	for _, value := range assets {
		if value.ClosedAt == nil && value.Identity.Provider == asset.ProviderGCP && value.Identity.ConnectionID == h.connection && value.Identity.NativeType == notificationChannelType {
			channels = append(channels, value)
		}
	}
	if len(channels) == 0 {
		return result, nil
	}
	validate := func(value asset.Asset) error {
		if !gcpPartition(value.Identity.Partition) {
			return groupDenied("monitoring_channel_partition_invalid")
		}
		data, err := h.client.notificationChannelRead(ctx, value.Identity.NativeID)
		if err != nil {
			return err
		}
		if notificationChannelConfiguration(value.Identity.NativeID, data) != text(value.Normalized[notificationChannelReview]) {
			return groupDenied("notification_channel_configuration_changed")
		}
		return nil
	}
	for _, channel := range channels {
		if err := validate(channel); err != nil {
			return result, err
		}
	}
	policies, err := h.client.monitoringPolicySnapshot(ctx)
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(policies))
	for id := range policies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		refs, err := h.client.alertPolicyChannels(policies[id])
		if err != nil {
			return result, err
		}
		for _, channel := range channels {
			if !slices.Contains(refs, channel.Identity.NativeID) {
				continue
			}
			policy, found, err := findManagedAsset(assets, channel, alertPolicyType, id)
			if err != nil {
				return result, err
			}
			if !found || policy.ClosedAt != nil || text(policy.Normalized[alertPolicyReview]) != monitoringConfiguration(alertPolicyType, id, policies[id]) {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: channel.Identity.Provider, ConnectionID: channel.Identity.ConnectionID, ControllerID: channel.ID, NativeType: alertPolicyType, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"reason": "monitoring_policy_refresh_required", "source": monitoringDependencySource}})
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: channel.ID, TargetAssetID: policy.ID, Type: graph.RelationshipDependsOn, Source: monitoringDependencySource, Confidence: 1, Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion:   true,
				graph.RelationshipEvidenceAutomaticSelection: false,
				graph.RelationshipEvidenceAuthority:          string(graph.AuthorityAuthoritative),
				graph.RelationshipEvidenceDeletionOrder:      graph.DeletionOrderTargetBeforeSource,
				"native_policy":                              id, "configuration": policy.Normalized[alertPolicyReview],
			}})
		}
	}
	for _, channel := range channels {
		if err := validate(channel); err != nil {
			return governance.Contribution{}, err
		}
	}
	return result, nil
}
