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

const netappGroupType = netappAccountType + "/volumeGroups"
const netappGroupReview = "_netapp_group_review"
const netappGroupProof = "_netapp_group_proof"
const netappGroupSource = "azure:netapp-group-volumes"

func netappVolumeGroupName(raw map[string]any) (string, error) {
	value := object(raw["properties"])["volumeGroupName"]
	if value == nil {
		return "", nil
	}
	name, ok := value.(string)
	if !ok || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\\?#%") {
		return "", serviceDenied("invalid_netapp_volume_group_name")
	}
	return strings.ToLower(name), nil
}
func (c *client) netappGroupProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-group-review-1", "id": id, "connection": connection, "review": review})
}
func (r *Runtime) netappGroupInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem, raw map[string]any) error {
	id, region := item.NativeID, item.Location
	parents, _, location, _, _, err := c.netappRecoveryParents(ctx, id, netappGroupType)
	if err != nil {
		return err
	}
	if location != region {
		return serviceDenied("netapp_group_region_changed")
	}
	known := object(object(req.KnownNativeMetadata[id][netappGroupReview])["members"])
	members, complete, err := c.netappAssignmentConsumers(ctx, id, netappGroupType, region, raw, known)
	if err != nil {
		return err
	}
	if !complete {
		return serviceDenied("incomplete_netapp_group_members")
	}
	hints := maps.Clone(known)
	if hints == nil {
		hints = map[string]any{}
	}
	maps.Copy(hints, members)
	after, complete, err := c.netappAssignmentConsumers(ctx, id, netappGroupType, region, raw, hints)
	if err != nil {
		return err
	}
	if !complete || c.privateConfiguration(members) != c.privateConfiguration(after) {
		return serviceDenied("netapp_group_members_changed")
	}
	own, err := c.netappRead(ctx, id, netappGroupType)
	if err != nil {
		return err
	}
	later, _, other, _, _, err := c.netappRecoveryParents(ctx, id, netappGroupType)
	if err != nil {
		return err
	}
	if c.privateConfiguration(own.data) != item.Normalized["_netapp_configuration"] || c.privateConfiguration(parents) != c.privateConfiguration(later) || other != region {
		return serviceDenied("netapp_group_context_changed")
	}
	review := map[string]any{"members": members, "parents": parents, "region": region, "configuration": item.Normalized["_netapp_configuration"]}
	item.Normalized[netappGroupReview], item.Normalized[netappGroupProof] = review, c.netappGroupProofFor(id, req.ConnectionID, review)
	// Group deletion also removes service-managed NICs. Membership alone cannot
	// authorize that cascade; keep the group protected until those effects are modeled.
	return nil
}
func (c *client) netappGroupContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	out := governance.Contribution{}
	unresolved := func(id, reason string) {
		out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: netappVolumeType, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, BlocksCleanup: true, Evidence: map[string]any{"reason": reason}})
	}
	review := object(parent.Normalized[netappGroupReview])
	if len(review) != 4 || parent.Identity.Partition != "azure" || c.netappIdentity(parent.Identity.NativeID, netappGroupType) != nil || review["configuration"] != parent.Normalized["_netapp_configuration"] || review["region"] != parent.Location || parent.Normalized[netappGroupProof] != c.netappGroupProofFor(parent.Identity.NativeID, parent.Identity.ConnectionID, review) {
		unresolved(parent.Identity.NativeID, "netapp_group_requires_refresh")
		return out, nil
	}
	members := object(review["members"])
	byID := map[string]asset.Asset{}
	for _, v := range assets {
		if v.Identity.Provider != parent.Identity.Provider || v.Identity.Partition != parent.Identity.Partition || v.Identity.ConnectionID != parent.Identity.ConnectionID || v.Identity.NativeType != netappVolumeType || redisParentID(redisParentID(v.Identity.NativeID)) != redisParentID(parent.Identity.NativeID) {
			continue
		}
		if byID[v.Identity.NativeID].ID != "" {
			return out, serviceDenied("ambiguous_netapp_group_volume")
		}
		byID[v.Identity.NativeID] = v
		if strings.EqualFold(text(v.Normalized["volumeGroupName"]), last(parent.Identity.NativeID)) && members[v.Identity.NativeID] == nil {
			unresolved(v.Identity.NativeID, "netapp_group_membership_requires_refresh")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(members)) {
		v := byID[id]
		entry := object(members[id])
		boundary, err := c.netappVolumeRecorded(v)
		if v.ID == "" || err != nil || v.Location != parent.Location || entry["configuration"] != v.Normalized["_netapp_configuration"] || entry["uid"] != boundary["file_system_id"] {
			unresolved(id, "netapp_group_volume_requires_refresh")
			continue
		}
		out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: v.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: netappGroupSource, Evidence: map[string]any{"native_membership": true}, Confidence: 1})
	}
	return out, nil
}
