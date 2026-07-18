package hooks

import (
	"context"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	natIPNativeType       = "ACS::NAT::NatIp"
	natIPLifecycleSource  = "nat:ListNatIps"
	natIPDefaultLifecycle = "nat_gateway_default_nat_ip"
)

// NATIPs assigns the default NAT IP to its NAT gateway. Alibaba Cloud does not
// allow deleting the default NAT IP directly; it disappears with the gateway.
// Non-default NAT IPs remain ordinary child resources and are deleted first by
// the declarative member_of relationship in nat-ip.yaml.
type NATIPs struct{}

func NewNATIPs() *NATIPs {
	return &NATIPs{}
}

func (*NATIPs) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	result := governance.Contribution{}
	for _, natIP := range ordered {
		if natIP.Identity.Provider != asset.ProviderAliCloud ||
			natIP.Identity.NativeType != natIPNativeType ||
			!normalizedBool(natIP.Normalized["isDefault"]) {
			continue
		}
		gatewayID := strings.TrimSpace(normalizedScalar(natIP.Normalized["natGatewayId"]))
		evidence := map[string]any{
			"source":         natIPLifecycleSource,
			"nat_ip_id":      natIP.Identity.NativeID,
			"nat_gateway_id": gatewayID,
			"is_default":     true,
		}
		gateway, found := resolveScopedAsset(
			natIP,
			natGatewayNativeType,
			gatewayID,
			ordered,
		)
		if gatewayID == "" || !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
				Provider: natIP.Identity.Provider, ConnectionID: natIP.Identity.ConnectionID,
				NativeType: natGatewayNativeType, NativeID: gatewayID,
				ControllerID: natIP.ID, Relationship: graph.RelationshipMemberOf,
				Evidence: evidence,
			})
			continue
		}
		evidence["delete_by_default"] = true
		evidence["lifecycle_kind"] = natIPDefaultLifecycle
		evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
		evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
		evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 30
		evidence[graph.LifecycleEvidenceWaitPollSeconds] = 2
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: natIP.ID, TargetAssetID: gateway.ID,
			Type: graph.RelationshipMemberOf, Source: natIPLifecycleSource,
			Evidence: evidence, Confidence: 1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: gateway.ID, ManagedAssetID: natIP.ID,
			Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
			CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
			EvidenceSource: natIPLifecycleSource, Evidence: evidence, Confidence: 1,
		})
	}
	return result, nil
}
