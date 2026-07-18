package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const (
	vSwitchNativeType           = "ACS::VPC::VSwitch"
	securityGroupNativeType     = "ACS::ECS::SecurityGroup"
	networkInterfaceNativeType  = "ACS::ECS::NetworkInterface"
	dataWorksProjectEvidence    = "dataworks:ListResourceGroupAssociateProjects"
	dataWorksNetworkEvidence    = "dataworks:ListNetworks"
	dataWorksManagedENIEvidence = "dataworks:ListNetworks+ecs:DescribeNetworkInterfaces"
)

// DataWorks derives graph and lifecycle facts from DataWorks resource-group
// enrichment. It makes no Provider calls: the inventory snapshot carries the
// request IDs and exact network records returned by DataWorks.
type DataWorks struct{}

func NewDataWorks() *DataWorks {
	return &DataWorks{}
}

type dataWorksNetworkClaim struct {
	controller asset.Asset
	network    map[string]any
}

func (*DataWorks) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	byIdentity := make(map[string]asset.Asset, len(ordered))
	for _, value := range ordered {
		byIdentity[value.Identity.Key()] = value
	}

	result := governance.Contribution{}
	relationshipSeen := make(map[string]struct{})
	securityGroupClaims := make(map[string]map[asset.AssetID]dataWorksNetworkClaim)
	for _, resourceGroup := range ordered {
		if resourceGroup.Identity.Provider != asset.ProviderAliCloud ||
			resourceGroup.Identity.NativeType != alicloud.DataWorksResourceGroupNativeType {
			continue
		}
		for _, projectID := range normalizedStrings(
			resourceGroup.Normalized[alicloud.NormalizedDataWorksProjectIDsField],
		) {
			evidence := map[string]any{
				"request_id": strings.TrimSpace(normalizedScalar(
					resourceGroup.Normalized[alicloud.NormalizedDataWorksProjectRequestIDField],
				)),
				"resource_group_id": resourceGroup.Identity.NativeID,
				"project_id":        projectID,
			}
			appendDataWorksRelationship(
				&result,
				relationshipSeen,
				resourceGroup,
				byIdentity,
				alicloud.DataWorksProjectNativeType,
				projectID,
				graph.RelationshipMemberOf,
				dataWorksProjectEvidence,
				evidence,
			)
		}
		networks, err := normalizedDataWorksNetworks(
			resourceGroup.Normalized[alicloud.NormalizedDataWorksNetworksField],
		)
		if err != nil {
			return governance.Contribution{}, fmt.Errorf(
				"DataWorks resource group %q topology evidence: %w",
				resourceGroup.Identity.NativeID,
				err,
			)
		}
		for _, network := range networks {
			if got := strings.TrimSpace(normalizedScalar(network["resource_group_id"])); got != resourceGroup.Identity.NativeID {
				return governance.Contribution{}, fmt.Errorf(
					"DataWorks network evidence belongs to resource group %q, want %q",
					got,
					resourceGroup.Identity.NativeID,
				)
			}
			evidence := dataWorksNetworkEvidenceMap(resourceGroup, network)
			appendDataWorksRelationship(
				&result,
				relationshipSeen,
				resourceGroup,
				byIdentity,
				vpcNativeType,
				normalizedScalar(network["vpc_id"]),
				graph.RelationshipUses,
				dataWorksNetworkEvidence,
				evidence,
			)
			appendDataWorksRelationship(
				&result,
				relationshipSeen,
				resourceGroup,
				byIdentity,
				vSwitchNativeType,
				normalizedScalar(network["vswitch_id"]),
				graph.RelationshipUses,
				dataWorksNetworkEvidence,
				evidence,
			)
			securityGroupID := strings.TrimSpace(normalizedScalar(network["security_group_id"]))
			if securityGroupID == "" {
				continue
			}
			if securityGroupClaims[securityGroupID] == nil {
				securityGroupClaims[securityGroupID] = make(map[asset.AssetID]dataWorksNetworkClaim)
			}
			if _, exists := securityGroupClaims[securityGroupID][resourceGroup.ID]; !exists {
				securityGroupClaims[securityGroupID][resourceGroup.ID] = dataWorksNetworkClaim{
					controller: resourceGroup,
					network:    network,
				}
			}
		}
	}

	uniqueSecurityGroupClaims := make(map[string]dataWorksNetworkClaim)
	securityGroupIDs := sortedClaimKeys(securityGroupClaims)
	for _, securityGroupID := range securityGroupIDs {
		claims := securityGroupClaims[securityGroupID]
		if len(claims) != 1 {
			continue
		}
		var claim dataWorksNetworkClaim
		for _, candidate := range claims {
			claim = candidate
		}
		uniqueSecurityGroupClaims[securityGroupID] = claim
		evidence := dataWorksLifecycleEvidence(claim.controller, claim.network)
		evidence["resource_type"] = securityGroupNativeType
		evidence["resource_id"] = securityGroupID
		appendLifecycle(
			&result,
			claim.controller,
			byIdentity,
			securityGroupNativeType,
			securityGroupID,
			dataWorksNetworkEvidence,
			graph.OwnershipExclusive,
			graph.CleanupDelegate,
			1,
			evidence,
		)
	}

	for _, networkInterface := range ordered {
		if networkInterface.Identity.Provider != asset.ProviderAliCloud ||
			networkInterface.Identity.NativeType != networkInterfaceNativeType ||
			!normalizedBool(networkInterface.Normalized[alicloud.NormalizedServiceManagedField]) {
			continue
		}
		controllerClaims := make(map[asset.AssetID]dataWorksNetworkClaim)
		securityGroupMatches := make(map[asset.AssetID]string)
		for _, securityGroupID := range normalizedStrings(
			networkInterface.Normalized[alicloud.NormalizedSecurityGroupIDsField],
		) {
			claim, exists := uniqueSecurityGroupClaims[securityGroupID]
			if !exists ||
				!sameLifecycleScope(claim.controller, networkInterface) ||
				!networkInterfaceMatchesDataWorksNetwork(networkInterface, claim.network) ||
				!serviceIdentityCompatible(byIdentity, claim, securityGroupID, networkInterface) {
				continue
			}
			controllerClaims[claim.controller.ID] = claim
			securityGroupMatches[claim.controller.ID] = securityGroupID
		}
		if len(controllerClaims) != 1 {
			continue
		}
		var claim dataWorksNetworkClaim
		for _, candidate := range controllerClaims {
			claim = candidate
		}
		evidence := dataWorksLifecycleEvidence(claim.controller, claim.network)
		evidence["resource_type"] = networkInterfaceNativeType
		evidence["resource_id"] = networkInterface.Identity.NativeID
		evidence["managed_security_group_id"] = securityGroupMatches[claim.controller.ID]
		evidence["service_managed"] = true
		if serviceID := normalizedScalar(
			networkInterface.Normalized[alicloud.NormalizedServiceIDField],
		); serviceID != "" {
			evidence["service_id"] = serviceID
		}
		appendLifecycle(
			&result,
			claim.controller,
			byIdentity,
			networkInterfaceNativeType,
			networkInterface.Identity.NativeID,
			dataWorksManagedENIEvidence,
			graph.OwnershipExclusive,
			graph.CleanupDelegate,
			1,
			evidence,
		)
	}
	return result, nil
}

func appendDataWorksRelationship(
	result *governance.Contribution,
	seen map[string]struct{},
	source asset.Asset,
	byIdentity map[string]asset.Asset,
	targetType string,
	targetID string,
	relationshipType graph.RelationshipType,
	evidenceSource string,
	evidence map[string]any,
) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return
	}
	key := string(source.ID) + "\x00" + targetType + "\x00" + targetID + "\x00" + string(relationshipType)
	if _, duplicate := seen[key]; duplicate {
		return
	}
	seen[key] = struct{}{}
	targetIdentity := asset.Identity{
		Provider: source.Identity.Provider, Partition: source.Identity.Partition,
		ConnectionID: source.Identity.ConnectionID, ScopeKey: source.Identity.ScopeKey,
		NativeType: targetType, NativeID: targetID,
	}
	target, found := byIdentity[targetIdentity.Key()]
	if !found {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
			Provider: source.Identity.Provider, ConnectionID: source.Identity.ConnectionID,
			NativeType: targetType, NativeID: targetID, ControllerID: source.ID,
			Relationship: relationshipType, Evidence: evidence,
		})
		return
	}
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: source.ID, TargetAssetID: target.ID, Type: relationshipType,
		Source: evidenceSource, Evidence: evidence, Confidence: 1,
	})
}

func normalizedDataWorksNetworks(value any) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	var rawValues []any
	switch typed := value.(type) {
	case []any:
		rawValues = typed
	default:
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &rawValues); err != nil {
			return nil, fmt.Errorf("networks must be an array")
		}
	}
	result := make([]map[string]any, 0, len(rawValues))
	for index, raw := range rawValues {
		network, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("network %d has type %T", index, raw)
		}
		result = append(result, network)
	}
	sort.Slice(result, func(i, j int) bool {
		return normalizedScalar(result[i]["network_id"]) < normalizedScalar(result[j]["network_id"])
	})
	return result, nil
}

func normalizedStrings(value any) []string {
	seen := make(map[string]struct{})
	appendValue := func(raw any) {
		if text := strings.TrimSpace(normalizedScalar(raw)); text != "" {
			seen[text] = struct{}{}
		}
	}
	switch typed := value.(type) {
	case []any:
		for _, raw := range typed {
			appendValue(raw)
		}
	case []string:
		for _, raw := range typed {
			appendValue(raw)
		}
	default:
		appendValue(value)
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizedScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func normalizedBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		text := strings.TrimSpace(typed)
		if parsed, err := strconv.ParseBool(text); err == nil {
			return parsed
		}
		return text == "1"
	case json.Number:
		number, err := typed.Int64()
		return err == nil && number == 1
	case float64:
		return typed == 1
	case float32:
		return typed == 1
	case int:
		return typed == 1
	case int32:
		return typed == 1
	case int64:
		return typed == 1
	case uint:
		return typed == 1
	case uint32:
		return typed == 1
	case uint64:
		return typed == 1
	default:
		return false
	}
}

func dataWorksNetworkEvidenceMap(resourceGroup asset.Asset, network map[string]any) map[string]any {
	return map[string]any{
		"request_id":        normalizedScalar(network["request_id"]),
		"resource_group_id": resourceGroup.Identity.NativeID,
		"network_id":        normalizedScalar(network["network_id"]),
		"vpc_id":            normalizedScalar(network["vpc_id"]),
		"vswitch_id":        normalizedScalar(network["vswitch_id"]),
		"security_group_id": normalizedScalar(network["security_group_id"]),
		"network_status":    normalizedScalar(network["status"]),
	}
}

func dataWorksLifecycleEvidence(resourceGroup asset.Asset, network map[string]any) map[string]any {
	evidence := dataWorksNetworkEvidenceMap(resourceGroup, network)
	evidence["delete_by_default"] = true
	evidence["lifecycle_kind"] = "dataworks_serverless_resource_group"
	evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
	evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
	evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 300
	evidence[graph.LifecycleEvidenceWaitPollSeconds] = 15
	return evidence
}

func sortedClaimKeys(claims map[string]map[asset.AssetID]dataWorksNetworkClaim) []string {
	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameLifecycleScope(controller asset.Asset, managed asset.Asset) bool {
	if controller.Identity.ConnectionID != managed.Identity.ConnectionID ||
		controller.Identity.Partition != managed.Identity.Partition {
		return false
	}
	if controller.Identity.ScopeKey != "" && managed.Identity.ScopeKey != "" {
		return controller.Identity.ScopeKey == managed.Identity.ScopeKey
	}
	return strings.TrimSpace(controller.Location) == strings.TrimSpace(managed.Location)
}

func networkInterfaceMatchesDataWorksNetwork(
	networkInterface asset.Asset,
	network map[string]any,
) bool {
	for _, field := range []string{"vpc_id", "vswitch_id"} {
		want := strings.TrimSpace(normalizedScalar(network[field]))
		got := strings.TrimSpace(normalizedScalar(networkInterface.Normalized[field]))
		if want != "" && got != want {
			return false
		}
	}
	return true
}

func serviceIdentityCompatible(
	byIdentity map[string]asset.Asset,
	claim dataWorksNetworkClaim,
	securityGroupID string,
	networkInterface asset.Asset,
) bool {
	securityGroupIdentity := asset.Identity{
		Provider: claim.controller.Identity.Provider, Partition: claim.controller.Identity.Partition,
		ConnectionID: claim.controller.Identity.ConnectionID, ScopeKey: claim.controller.Identity.ScopeKey,
		NativeType: securityGroupNativeType, NativeID: securityGroupID,
	}
	securityGroup, found := byIdentity[securityGroupIdentity.Key()]
	if !found {
		return true
	}
	securityGroupServiceID := normalizedScalar(
		securityGroup.Normalized[alicloud.NormalizedServiceIDField],
	)
	networkInterfaceServiceID := normalizedScalar(
		networkInterface.Normalized[alicloud.NormalizedServiceIDField],
	)
	return securityGroupServiceID == "" ||
		networkInterfaceServiceID == "" ||
		securityGroupServiceID == networkInterfaceServiceID
}
