package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const recoveryHanaDatabase = "AzureVmWorkloadSAPHanaDatabase"
const recoveryHanaInstance = "AzureVmWorkloadSAPHanaDBInstance"

// HANA snapshot protection must stop before related database protection. Native
// parentName names the database's instance; friendlyName names the instance
// resource, and serverName scopes both to their host/cluster. Missing selectors
// are ambiguity, never evidence that a live snapshot can be ignored.
func recoveryHanaRelated(database, instance map[string]any) (bool, error) {
	db, snapshot := object(database["properties"]), object(instance["properties"])
	if db["protectedItemType"] != recoveryHanaDatabase || snapshot["protectedItemType"] != recoveryHanaInstance {
		return false, serviceDenied("invalid_recovery_hana_types")
	}
	for _, value := range []string{text(db["parentName"]), text(db["serverName"]), text(snapshot["friendlyName"]), text(snapshot["serverName"])} {
		if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t") {
			return false, serviceDenied("ambiguous_recovery_hana_instance")
		}
	}
	return strings.EqualFold(text(db["parentName"]), text(snapshot["friendlyName"])) && strings.EqualFold(text(db["serverName"]), text(snapshot["serverName"])), nil
}

func (c *client) recoveryItemPrerequisites(ctx context.Context, id string, raw map[string]any, known map[string]any) (map[string]any, map[string]any, error) {
	dependencies, snapshots := map[string]any{}, map[string]any{}
	if raw == nil || object(raw["properties"])["protectedItemType"] != recoveryHanaDatabase {
		return dependencies, snapshots, nil
	}
	candidates := map[string]bool{}
	for hint := range known {
		candidate, err := c.recoveryServicesIdentity(hint, recoveryServicesItem)
		if err != nil || hint != candidate || redisParentID(candidate) != redisParentID(id) {
			return nil, nil, serviceDenied("invalid_recovery_hana_hint")
		}
		if hint != id {
			candidates[hint] = true
		}
	}
	rows, err := c.recoveryServicesCollection(ctx, recoveryServicesVaultID(id)+"/backupprotecteditems", recoveryServicesItem)
	if err != nil {
		return nil, nil, err
	}
	for candidate, raw := range rows {
		if redisParentID(candidate) == redisParentID(id) && object(raw["properties"])["protectedItemType"] == recoveryHanaInstance {
			candidates[candidate] = true
		}
	}
	for _, candidate := range slices.Sorted(maps.Keys(candidates)) {
		own, err := c.recoveryServicesRead(ctx, candidate, recoveryServicesItem)
		if isNotFound(err) && rows[candidate] == nil {
			continue
		}
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		if rows[candidate] != nil && !nativeConfigurationContains(rows[candidate], own.data) {
			return nil, nil, serviceDenied("recovery_hana_instance_changed")
		}
		p := object(own.data["properties"])
		if p["protectedItemType"] != recoveryHanaInstance {
			continue
		}
		state := c.recoveryItemState(own.data)
		state["observed"] = c.privateConfiguration(own.data)
		snapshots[candidate] = state
		if p["protectionState"] == "ProtectionStopped" {
			continue
		}
		related, err := recoveryHanaRelated(raw, own.data)
		if err != nil {
			return nil, nil, err
		}
		if related {
			dependencies[candidate] = state
		}
	}
	if len(dependencies) > 1 {
		return nil, nil, serviceDenied("ambiguous_recovery_hana_snapshot_protection")
	}
	return dependencies, snapshots, nil
}

func (c *client) contributeRecoveryItemPrerequisites(ctx context.Context, connection asset.ConnectionID, value asset.Asset, assets []asset.Asset) (result governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection || value.Identity.NativeType != recoveryServicesItem || value.ID == "" {
		return result, serviceDenied("invalid_recovery_item_graph_identity")
	}
	if value.Normalized["retained"] == true {
		return result, nil
	}
	expected := object(value.Normalized[recoveryItemReviewKey])
	if value.Normalized[recoveryItemProofKey] != c.recoveryItemProof(value.Identity.NativeID, connection, expected) {
		return result, serviceDenied("recovery_item_graph_review_changed")
	}
	review, raw, err := c.recoveryItemReview(ctx, value.Identity.NativeID, expected)
	if err != nil {
		return result, err
	}
	if raw == nil || c.privateConfiguration(review) != c.privateConfiguration(expected) {
		return result, serviceDenied("recovery_item_graph_requires_refresh")
	}
	for id, entry := range object(review["prerequisites"]) {
		evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "reason": "hana_instance_protection_before_database"}
		var target asset.Asset
		for _, candidate := range assets {
			if candidate.ClosedAt == nil && candidate.Identity.Provider == value.Identity.Provider && candidate.Identity.Partition == value.Identity.Partition && candidate.Identity.ConnectionID == connection && candidate.Identity.NativeType == recoveryServicesItem && candidate.Identity.NativeID == id {
				if target.ID != "" {
					return result, serviceDenied("duplicate_recovery_hana_graph_target")
				}
				target = candidate
			}
		}
		if target.ID == "" {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: connection, NativeType: recoveryServicesItem, NativeID: id, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
			continue
		}
		if target.Normalized["_recovery_services_configuration"] != object(entry)["observed"] {
			return result, serviceDenied("recovery_hana_graph_snapshot_changed")
		}
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: "azure:recovery-hana-protection-order", Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
