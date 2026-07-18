package hooks

import (
	"context"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const configurationTopologyEvidence = "resource-center:configuration-topology"

type configurationRelationshipRule struct {
	sourceType  string
	targetTypes []string
	key         string
	kind        graph.RelationshipType
	crossScope  bool
	evidence    string
}

var configurationRelationshipRules = []configurationRelationshipRule{
	{
		sourceType: "ACS::SLB::LoadBalancer",
		targetTypes: []string{
			"ACS::ECS::NetworkInterface",
			"ACS::ECS::Instance",
		},
		key: "ServerId", kind: graph.RelationshipUses,
		evidence: "slb_backend",
	},
	{
		sourceType: "ACS::PrivateZone::Zone",
		targetTypes: []string{
			"ACS::VPC::VPC",
		},
		key: "VpcId", kind: graph.RelationshipConnectedTo, crossScope: true,
		evidence: "private_zone_vpc_binding",
	},
	{
		sourceType: "ACS::VPC::PeerConnection",
		targetTypes: []string{
			"ACS::VPC::VPC",
		},
		key: "AcceptingVpcId", kind: graph.RelationshipMemberOf, crossScope: true,
		evidence: "accepting_vpc",
	},
	{
		sourceType: "ACS::VPC::RouteTable",
		targetTypes: []string{
			"ACS::NAT::NatGateway",
			"ACS::VPC::Ipv4Gateway",
			"ACS::VPC::Ipv6Gateway",
			"ACS::CEN::TransitRouterVpcAttachment",
		},
		key: "InstanceId", kind: graph.RelationshipConnectedTo,
		evidence: "route_next_hop",
	},
	{
		sourceType: "ACS::VPC::NetworkAcl",
		targetTypes: []string{
			"ACS::VPC::VSwitch",
		},
		key: "ResourceId", kind: graph.RelationshipConnectedTo,
		evidence: "network_acl_association",
	},
	{
		sourceType: "ACS::VPC::VSwitch",
		targetTypes: []string{
			"ACS::VPC::RouteTable",
		},
		key: "RouteTableId", kind: graph.RelationshipConnectedTo,
		evidence: "vswitch_route_table",
	},
	{
		sourceType: "ACS::ECS::Snapshot",
		targetTypes: []string{
			"ACS::ECS::Snapshot",
		},
		key: "SourceSnapshotId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "source_snapshot",
	},
	{
		sourceType: "ACS::ECS::Snapshot",
		targetTypes: []string{
			"ACS::ECS::Disk",
		},
		key: "SourceDiskId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "source_disk",
	},
	{
		sourceType: "ACS::ECS::Image",
		targetTypes: []string{
			"ACS::ECS::Snapshot",
		},
		key: "SnapshotId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "image_snapshot",
	},
	{
		sourceType: "ACS::ECS::Disk",
		targetTypes: []string{
			"ACS::ECS::Disk",
		},
		key: "SourceDiskId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "source_disk",
	},
	{
		sourceType: "ACS::ECI::ImageCache",
		targetTypes: []string{
			"ACS::ECS::Snapshot",
		},
		key: "SnapshotId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "image_cache_snapshot",
	},
	{
		sourceType: "ACS::HBR::Vault",
		targetTypes: []string{
			"ACS::HBR::Vault",
		},
		key: "ReplicationSourceVaultId", kind: graph.RelationshipCreatedFrom, crossScope: true,
		evidence: "replication_source_vault",
	},
	{
		sourceType: "ACS::ECS::Snapshot",
		targetTypes: []string{
			"ACS::KMS::Key",
		},
		key: "KMSKeyId", kind: graph.RelationshipUses, crossScope: true,
		evidence: "snapshot_kms_key",
	},
	{
		sourceType: "ACS::NAS::FileSystem",
		targetTypes: []string{
			"ACS::KMS::Key",
		},
		key: "KmsKeyId", kind: graph.RelationshipUses, crossScope: true,
		evidence: "nas_kms_key",
	},
	{
		sourceType: "ACS::OSS::Bucket",
		targetTypes: []string{
			"ACS::KMS::Key",
		},
		key: "KMSMasterKeyID", kind: graph.RelationshipUses, crossScope: true,
		evidence: "oss_kms_key",
	},
}

// ConfigurationTopology derives only reviewed relationships whose exact
// provider configuration fields cannot be represented safely by a static
// same-scope spec mapping.
type ConfigurationTopology struct{}

func NewConfigurationTopology() *ConfigurationTopology {
	return &ConfigurationTopology{}
}

func (*ConfigurationTopology) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	result := governance.Contribution{}
	seen := make(map[configurationRelationshipKey]struct{})
	for _, source := range ordered {
		if source.Identity.Provider != asset.ProviderAliCloud {
			continue
		}
		for _, rule := range configurationRelationshipRules {
			if source.Identity.NativeType != rule.sourceType {
				continue
			}
			for _, nativeID := range configurationStringsByKey(source, rule.key) {
				target, found := resolveConfigurationTarget(
					source,
					rule.targetTypes,
					nativeID,
					ordered,
					rule.crossScope,
				)
				if !found || target.ID == source.ID {
					continue
				}
				key := configurationRelationshipKey{
					source: source.ID,
					target: target.ID,
					kind:   rule.kind,
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				result.Relationships = append(result.Relationships, graph.Relationship{
					SourceAssetID: source.ID,
					TargetAssetID: target.ID,
					Type:          rule.kind,
					Source:        configurationTopologyEvidence,
					Confidence:    1,
					Evidence: map[string]any{
						"source":            configurationTopologyEvidence,
						"relationship":      rule.evidence,
						"configuration_key": rule.key,
						"target_native_id":  nativeID,
					},
				})
			}
		}
	}
	return result, nil
}

type configurationRelationshipKey struct {
	source asset.AssetID
	target asset.AssetID
	kind   graph.RelationshipType
}

func resolveConfigurationTarget(
	source asset.Asset,
	nativeTypes []string,
	nativeID string,
	assets []asset.Asset,
	crossScope bool,
) (asset.Asset, bool) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return asset.Asset{}, false
	}
	typeAllowed := make(map[string]struct{}, len(nativeTypes))
	for _, nativeType := range nativeTypes {
		typeAllowed[nativeType] = struct{}{}
	}

	sameScope := make([]asset.Asset, 0, 1)
	connectionWide := make([]asset.Asset, 0, 1)
	for _, candidate := range assets {
		if candidate.Identity.Provider != source.Identity.Provider ||
			candidate.Identity.ConnectionID != source.Identity.ConnectionID ||
			candidate.Identity.Partition != source.Identity.Partition ||
			strings.TrimSpace(candidate.Identity.NativeID) != nativeID {
			continue
		}
		if _, allowed := typeAllowed[candidate.Identity.NativeType]; !allowed {
			continue
		}
		connectionWide = append(connectionWide, candidate)
		if sameLifecycleScope(source, candidate) {
			sameScope = append(sameScope, candidate)
		}
	}
	if len(sameScope) == 1 {
		return sameScope[0], true
	}
	if len(sameScope) > 1 || !crossScope || len(connectionWide) != 1 {
		return asset.Asset{}, false
	}
	return connectionWide[0], true
}
