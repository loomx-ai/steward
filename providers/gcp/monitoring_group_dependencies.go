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

const monitoringDashboardType = "monitoring.googleapis.com/Dashboard"

func (c *client) monitoringGroupConsumerData(kind, id string, data map[string]any) error {
	switch kind {
	case monitoringGroupType:
		return c.monitoringGroupData(id, data)
	case uptimeType:
		return c.uptimeData(id, data)
	case monitoringDashboardType:
		if err := checkListCompleteness(data); err != nil {
			return err
		}
		if err := cloudNatScalars(data, []string{"name", "displayName", "etag"}, nil, nil, nil); err != nil {
			return err
		}
		if !strings.HasPrefix(text(data["name"]), "projects/") || c.canonicalName("//monitoring.googleapis.com/"+text(data["name"])) != id || text(data["displayName"]) == "" {
			return groupDenied("monitoring_dashboard_identity_invalid")
		}
		spec, _ := findType(kind)
		_, err := c.resourceURL(spec, id)
		return err
	}
	return groupDenied("monitoring_group_consumer_type_invalid")
}
func (c *client) monitoringGroupConsumerConfiguration(kind, id string, data map[string]any) string {
	switch kind {
	case monitoringGroupType:
		return c.monitoringGroupConfiguration(id, data)
	case uptimeType:
		return uptimeConfiguration(id, data)
	case alertPolicyType:
		return monitoringConfiguration(kind, id, data)
	default:
		value := cloneParameters(data)
		value["name"] = id
		return firewallDigest(value)
	}
}

// Native own-project LIST/GET/re-LIST snapshots. No filter (including parent or
// ancestor filters) may narrow the collection, and a GET failure isn't absence.
func (c *client) monitoringGroupConsumerSnapshot(ctx context.Context, kind string) (map[string]map[string]any, error) {
	collection, field, parent := "groups", "group", "name"
	switch kind {
	case monitoringGroupType:
	case uptimeType:
		collection, field, parent = "uptimeCheckConfigs", "uptimeCheckConfigs", "parent"
	case monitoringDashboardType:
		collection, field, parent = "dashboards", "dashboards", "parent"
	default:
		return nil, groupDenied("monitoring_group_consumer_type_invalid")
	}
	list := func() (map[string]map[string]any, error) {
		rows, err := c.batchList(ctx, "monitoring.projects."+collection+".list", map[string]any{parent: "projects/" + c.project, "pageSize": 100}, field)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		result := map[string]map[string]any{}
		for _, row := range rows {
			id := c.canonicalName("//monitoring.googleapis.com/" + text(row["name"]))
			if result[id] != nil {
				return nil, groupDenied("monitoring_group_consumer_duplicate")
			}
			if err := c.monitoringGroupConsumerData(kind, id, row); err != nil {
				return nil, err
			}
			result[id] = row
		}
		return result, nil
	}
	reviews := func(values map[string]map[string]any) string {
		result := map[string]any{}
		for id, data := range values {
			result[id] = c.monitoringGroupConsumerConfiguration(kind, id, data)
		}
		return firewallDigest(result)
	}
	listed, err := list()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(listed))
	for id := range listed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := map[string]map[string]any{}
	for _, id := range ids {
		spec, _ := findType(kind)
		endpoint, err := c.resourceURL(spec, id)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if err := c.monitoringGroupConsumerData(kind, id, live); err != nil {
			return nil, err
		}
		if c.monitoringGroupConsumerConfiguration(kind, id, listed[id]) != c.monitoringGroupConsumerConfiguration(kind, id, live) {
			return nil, groupDenied("monitoring_group_consumer_configuration_changed")
		}
		result[id] = live
	}
	again, err := list()
	if err != nil {
		return nil, err
	}
	if reviews(listed) != reviews(again) {
		return nil, groupDenied("monitoring_group_consumer_set_changed")
	}
	return result, nil
}

func (c *client) monitoringGroupConsumerReference(kind string, data map[string]any, group string) monitoringReference {
	switch kind {
	case monitoringGroupType:
		return monitoringBool(c.canonicalName("//monitoring.googleapis.com/"+text(data["parentName"])) == group)
	case uptimeType:
		return monitoringBool(text(object(data["resourceGroup"])["groupId"]) == last(group))
	case alertPolicyType:
		return alertPolicyGroupReference(data, last(group))
	case monitoringDashboardType:
		return monitoringDashboardGroupReference(data, last(group))
	}
	return monitoringUnresolvedReference
}

func (h *monitoringDependencies) monitoringGroupDependencies(ctx context.Context, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	var groups []asset.Asset
	for _, value := range assets {
		if value.ClosedAt == nil && value.Identity.Provider == asset.ProviderGCP && value.Identity.ConnectionID == h.connection && value.Identity.NativeType == monitoringGroupType {
			groups = append(groups, value)
		}
	}
	if len(groups) == 0 {
		return result, nil
	}
	validate := func(group asset.Asset) error {
		if !gcpPartition(group.Identity.Partition) {
			return groupDenied("monitoring_group_partition_invalid")
		}
		data, err := h.client.monitoringGroupRead(ctx, group.Identity.NativeID)
		if err != nil {
			return contracts.DependencyReadError(err)
		}
		if h.client.monitoringGroupConfiguration(group.Identity.NativeID, data) != text(group.Normalized[monitoringGroupReview]) {
			return groupDenied("monitoring_group_configuration_changed")
		}
		return nil
	}
	for _, group := range groups {
		if err := validate(group); err != nil {
			return result, err
		}
	}
	for _, kind := range []string{monitoringGroupType, uptimeType, alertPolicyType, monitoringDashboardType} {
		var consumers map[string]map[string]any
		var err error
		if kind == alertPolicyType {
			consumers, err = h.client.monitoringPolicySnapshot(ctx)
		} else {
			consumers, err = h.client.monitoringGroupConsumerSnapshot(ctx, kind)
		}
		if err != nil {
			return result, err
		}
		if kind == monitoringGroupType {
			for _, group := range groups {
				if consumers[group.Identity.NativeID] == nil {
					return result, groupDenied("monitoring_group_missing_from_consumer_snapshot")
				}
			}
		}
		ids := make([]string, 0, len(consumers))
		for id := range consumers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, group := range groups {
			for _, id := range ids {
				ref := h.client.monitoringGroupConsumerReference(kind, consumers[id], group.Identity.NativeID)
				if ref == monitoringNoReference {
					continue
				}
				block := func(reason string) {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: group.Identity.Provider, ConnectionID: group.Identity.ConnectionID, ControllerID: group.ID, NativeType: kind, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"reason": reason, "source": monitoringDependencySource}})
				}
				if ref == monitoringUnresolvedReference {
					block("monitoring_group_consumer_reference_unresolved")
					continue
				}
				// Dashboard actions do not yet bind a full reviewed configuration. Keep the
				// consumer visible, without authorizing that generic action as a prerequisite.
				if kind == monitoringDashboardType {
					block("monitoring_group_dashboard_review_required")
					continue
				}
				consumer, found, err := findManagedAsset(assets, group, kind, id)
				if err != nil {
					return result, err
				}
				key := monitoringReviewKey(kind)
				if kind == monitoringGroupType {
					key = monitoringGroupReview
				}
				if !found || consumer.ClosedAt != nil || text(consumer.Normalized[key]) != h.client.monitoringGroupConsumerConfiguration(kind, id, consumers[id]) {
					block("monitoring_group_consumer_refresh_required")
					continue
				}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: group.ID, TargetAssetID: consumer.ID, Type: graph.RelationshipDependsOn, Source: monitoringDependencySource, Confidence: 1, Evidence: map[string]any{
					graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: string(graph.AuthorityAuthoritative), graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
					"native_consumer": id, "configuration": consumer.Normalized[key],
				}})
			}
		}
	}
	for _, group := range groups {
		if err := validate(group); err != nil {
			return result, err
		}
	}
	return result, nil
}
