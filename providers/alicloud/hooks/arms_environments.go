package hooks

import (
	"context"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const armsEnvironmentENIEvidence = "arms:prometheus-primary-eni"

type armsEnvironmentENICandidate struct {
	environment      asset.Asset
	networkInterface asset.Asset
}

// ARMSEnvironments associates the provider-created primary ENI of an ARMS VPC
// environment only when the full network tuple and the CloudMonitor signature
// resolve to exactly one environment and one ENI.
type ARMSEnvironments struct{}

func NewARMSEnvironments() *ARMSEnvironments { return &ARMSEnvironments{} }

func (*ARMSEnvironments) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	candidatesByENI := make(map[asset.AssetID][]armsEnvironmentENICandidate)
	for _, environment := range ordered {
		if environment.Identity.Provider != asset.ProviderAliCloud ||
			environment.Identity.NativeType != alicloud.ARMSEnvironmentNativeType ||
			!strings.EqualFold(diskStringValue(environment, "environmentType"), "ECS") ||
			!strings.EqualFold(diskStringValue(environment, "bindResourceType"), "VPC") {
			continue
		}
		matches := make([]asset.Asset, 0, 1)
		for _, networkInterface := range ordered {
			if armsEnvironmentNetworkInterfaceMatches(environment, networkInterface) {
				matches = append(matches, networkInterface)
			}
		}
		if len(matches) != 1 {
			continue
		}
		candidate := armsEnvironmentENICandidate{
			environment: environment, networkInterface: matches[0],
		}
		candidatesByENI[matches[0].ID] = append(candidatesByENI[matches[0].ID], candidate)
	}

	result := governance.Contribution{}
	eniIDs := make([]string, 0, len(candidatesByENI))
	for eniID := range candidatesByENI {
		eniIDs = append(eniIDs, string(eniID))
	}
	sort.Strings(eniIDs)
	for _, eniID := range eniIDs {
		candidates := candidatesByENI[asset.AssetID(eniID)]
		if len(candidates) != 1 {
			continue
		}
		candidate := candidates[0]
		evidence := map[string]any{
			"source":               armsEnvironmentENIEvidence,
			"controller_id":        candidate.environment.Identity.NativeID,
			"network_interface_id": candidate.networkInterface.Identity.NativeID,
			"match_strategy":       "unique_vpc_vswitch_security_group_primary_cloudmonitor_eni",
			"delete_by_default":    true,
			"lifecycle_kind":       "arms_environment_primary_eni",
			graph.LifecycleEvidenceControllerDeleteGuaranteed:      true,
			graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents: true,
			graph.LifecycleEvidenceWaitTimeoutSeconds:              600,
			graph.LifecycleEvidenceWaitPollSeconds:                 15,
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: candidate.networkInterface.ID,
			TargetAssetID: candidate.environment.ID,
			Type:          graph.RelationshipConnectedTo,
			Source:        armsEnvironmentENIEvidence,
			Evidence:      evidence,
			Confidence:    1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID:    candidate.environment.ID,
			ManagedAssetID:       candidate.networkInterface.ID,
			Authority:            graph.AuthorityAuthoritative,
			Ownership:            graph.OwnershipExclusive,
			CleanupPolicy:        graph.CleanupDelegate,
			DirectCleanupAllowed: false,
			EvidenceSource:       armsEnvironmentENIEvidence,
			Evidence:             evidence,
			Confidence:           1,
		})
	}
	return result, nil
}

func armsEnvironmentNetworkInterfaceMatches(
	environment asset.Asset,
	networkInterface asset.Asset,
) bool {
	if networkInterface.Identity.Provider != environment.Identity.Provider ||
		networkInterface.Identity.NativeType != networkInterfaceNativeType ||
		!sameLifecycleScope(environment, networkInterface) ||
		!strings.EqualFold(diskStringValue(networkInterface, "Type"), "Primary") ||
		!strings.EqualFold(
			diskStringValue(networkInterface, "Description"),
			"created by CloudMonitor Prometheus",
		) || diskStringValue(networkInterface, "InstanceId") != "" {
		return false
	}
	deleteOnRelease, known := diskBoolValue(networkInterface, "DeleteOnRelease")
	if !known || !deleteOnRelease {
		return false
	}
	for _, fields := range []struct {
		environment string
		network     string
	}{
		{environment: "vpcId", network: "vpc_id"},
		{environment: "vSwitchId", network: "vswitch_id"},
	} {
		want := diskStringValue(environment, fields.environment)
		if want == "" || want != diskStringValue(networkInterface, fields.network) {
			return false
		}
	}
	securityGroupID := diskStringValue(environment, "securityGroupId")
	if securityGroupID == "" {
		return false
	}
	for _, candidate := range normalizedStrings(
		networkInterface.Normalized[alicloud.NormalizedSecurityGroupIDsField],
	) {
		if candidate == securityGroupID {
			return true
		}
	}
	return false
}
