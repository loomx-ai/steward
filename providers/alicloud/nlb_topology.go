package alicloud

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	eipAddressNativeType          = "ACS::EIP::EipAddress"
	NormalizedNLBEIPIDsField      = "nlb_eip_ids"
	nlbManagedEIPResourceNameBase = "CREATE_BY_NLB."
)

// enrichNLBInventoryTopology projects the EIP allocation IDs returned by both
// ListLoadBalancers and Resource Center into one stable normalized field.
// Resource Center currently flattens AllocationId into each ZoneMapping while
// ListLoadBalancers nests it under LoadBalancerAddresses, so the extraction is
// intentionally recursive within ZoneMappings.
func enrichNLBInventoryTopology(items []contracts.InventoryItem) []contracts.InventoryItem {
	result := cloneTopologySlice(items)
	for index := range result {
		if result[index].NativeType != nlbLoadBalancerNativeType {
			continue
		}
		ids := nlbEIPIDs(result[index])
		if len(ids) == 0 {
			continue
		}
		normalized := cloneTopologyMap(result[index].Normalized)
		if normalized == nil {
			normalized = make(map[string]any)
		}
		values := make([]any, len(ids))
		for position, id := range ids {
			values[position] = id
		}
		normalized[NormalizedNLBEIPIDsField] = values
		result[index].Normalized = normalized
		result[index].NetworkReferences = appendUniqueReferences(
			result[index].NetworkReferences,
			ids,
		)
	}
	return result
}

func nlbEIPIDs(item contracts.InventoryItem) []string {
	seen := make(map[string]struct{})
	collectNLBAllocationIDs(item.Normalized[NormalizedNLBEIPIDsField], true, seen)

	configuration := item.Normalized["configuration"]
	if zoneMappings := configurationValueAtPath(configuration, "ZoneMappings"); zoneMappings != nil {
		collectNLBAllocationIDs(zoneMappings, false, seen)
	}
	if zoneMappings := valueAtPath(item.Raw, "ZoneMappings"); zoneMappings != nil {
		collectNLBAllocationIDs(zoneMappings, false, seen)
	}

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func collectNLBAllocationIDs(value any, allocationValue bool, result map[string]struct{}) {
	switch typed := value.(type) {
	case string:
		if allocationValue {
			if id := strings.TrimSpace(typed); id != "" {
				result[id] = struct{}{}
			}
		}
	case []string:
		for _, item := range typed {
			collectNLBAllocationIDs(item, allocationValue, result)
		}
	case []any:
		for _, item := range typed {
			collectNLBAllocationIDs(item, allocationValue, result)
		}
	case map[string]any:
		for key, item := range typed {
			collectNLBAllocationIDs(
				item,
				allocationValue || canonicalConfigurationKey(key) == "allocationid",
				result,
			)
		}
	}
}

// IsNLBManagedEIP accepts only provider-managed EIPs whose provider-generated
// name or description identifies the exact NLB. Existing EIPs manually
// associated with an NLB are related in the topology but are not delegated for
// deletion.
func IsNLBManagedEIP(value asset.Asset, nlbNativeID string) bool {
	configuration, _ := value.Normalized["configuration"].(map[string]any)
	if configuration == nil {
		configuration = value.Normalized
	}
	return isNLBManagedEIPConfiguration(configuration, value.Name, nlbNativeID)
}

func isNLBManagedEIPRecord(record map[string]any, nlbNativeID string) bool {
	return isNLBManagedEIPConfiguration(
		record,
		strings.TrimSpace(stringValue(valueAtPath(record, "Name"))),
		nlbNativeID,
	)
}

func isNLBManagedEIPConfiguration(
	configuration map[string]any,
	fallbackName string,
	nlbNativeID string,
) bool {
	nlbNativeID = strings.TrimSpace(nlbNativeID)
	if nlbNativeID == "" ||
		!nlbManagedFlag(configurationValueAtPath(configuration, "ServiceManaged")) {
		return false
	}
	want := nlbManagedEIPResourceNameBase + nlbNativeID
	for _, field := range []string{"Name", "Description", "Descritpion"} {
		if strings.EqualFold(
			strings.TrimSpace(stringValue(configurationValueAtPath(configuration, field))),
			want,
		) {
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(fallbackName), want)
}

func nlbManagedFlag(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") ||
			strings.TrimSpace(typed) == "1"
	case json.Number:
		return strings.TrimSpace(typed.String()) == "1"
	case int:
		return typed == 1
	case int32:
		return typed == 1
	case int64:
		return typed == 1
	case float32:
		return typed == 1
	case float64:
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
