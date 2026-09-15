package azure

import (
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const deploymentStackReviewKey = "_deployment_stack_review"
const deploymentStackProofKey = "_deployment_stack_proof"
const deploymentStackGraphSource = "azure:deployment-stack-members"

func (c *client) deploymentStackProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"stack": strings.ToLower(id), "connection": connection, "review": review})
}

// These are observed membership edges only. Cleanup delegation additionally
// requires live member/denial/cascade review and native operation readback.
func (c *client) deploymentStackContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	out := governance.Contribution{}
	unresolved := func(id, kind, reason string) {
		out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeID: id, NativeType: kind, ControllerID: parent.ID, Relationship: graph.RelationshipMemberOf, BlocksCleanup: true, Evidence: map[string]any{"reason": reason}})
	}
	review := object(parent.Normalized[deploymentStackReviewKey])
	scope, params, err := deploymentStackParameters(parent.Identity.NativeID)
	if parent.ID == "" || parent.Identity.Provider != asset.ProviderAzure || parent.Identity.Partition != "azure" || !strings.EqualFold(parent.Identity.NativeType, deploymentStackType) || err != nil || scope == "ManagementGroup" || !strings.EqualFold(text(params["subscriptionId"]), c.subscription) || len(review) != 8 || object(review["members"]) == nil || text(review["configuration"]) == "" || parent.Normalized[deploymentStackProofKey] != c.deploymentStackProof(parent.Identity.NativeID, parent.Identity.ConnectionID, review) {
		unresolved(parent.Identity.NativeID, deploymentStackType, "deployment_stack_requires_refresh")
		return out, nil
	}
	if review["arm_members_complete"] != true {
		unresolved(parent.Identity.NativeID, deploymentStackType, "deployment_stack_unresolved_members")
	}
	members := object(review["members"])
	for _, id := range slices.Sorted(maps.Keys(members)) {
		entry := object(members[id])
		kind := text(entry["type"])
		if entry["subscription_local"] != true {
			unresolved(id, kind, "deployment_stack_member_outside_subscription")
			continue
		}
		var match *asset.Asset
		for i := range assets {
			candidate := &assets[i]
			if candidate.Identity.Provider != parent.Identity.Provider || candidate.Identity.Partition != parent.Identity.Partition || candidate.Identity.ConnectionID != parent.Identity.ConnectionID || !strings.EqualFold(candidate.Identity.NativeID, id) {
				continue
			}
			if match != nil {
				return out, serviceDenied("ambiguous_deployment_stack_member")
			}
			match = candidate
		}
		if match == nil || match.ID == "" || match.ID == parent.ID || !strings.EqualFold(match.Identity.NativeType, kind) {
			unresolved(id, kind, "deployment_stack_member_requires_refresh")
			continue
		}
		evidence := map[string]any{"native_membership": true, "status": entry["status"], "deny_status": entry["deny_status"]}
		out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: match.ID, TargetAssetID: parent.ID, Type: graph.RelationshipMemberOf, Source: deploymentStackGraphSource, Evidence: evidence, Confidence: 1})
		if entry["status"] != "managed" || entry["deny_status"] == "unknown" {
			unresolved(id, kind, "deployment_stack_member_state_requires_review")
		}
	}
	return out, nil
}
