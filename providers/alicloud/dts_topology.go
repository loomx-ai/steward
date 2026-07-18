package alicloud

import (
	"regexp"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var dtsVSwitchIDPattern = regexp.MustCompile(`\bvsw-[[:alnum:]]+\b`)

func enrichDTSInventoryTopology(
	items []contracts.InventoryItem,
	details map[string]map[string]any,
) []contracts.InventoryItem {
	result := enrichTopologyItems(items, dtsInstanceNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 4)
		copyTopologyString(normalized, "dtsJobId", detail["DtsJobId"])
		if reserved, ok := nonEmptyString(detail["Reserved"]); ok {
			normalized["reserved"] = reserved
			if vSwitchIDs := uniqueSortedStrings(dtsVSwitchIDPattern.FindAllString(reserved, -1)); len(vSwitchIDs) > 0 {
				normalized["vSwitchIds"] = vSwitchIDs
			}
		}
		if rdsInstanceIDs := dtsRDSInstanceIDs(detail); len(rdsInstanceIDs) > 0 {
			normalized["rdsInstanceIds"] = rdsInstanceIDs
		}
		return normalized
	})
	for index := range result {
		if result[index].NativeType != dtsInstanceNativeType {
			continue
		}
		result[index].NetworkReferences = appendUniqueReferences(
			result[index].NetworkReferences,
			stringValues(result[index].Normalized["vSwitchIds"]),
			stringValues(result[index].Normalized["rdsInstanceIds"]),
		)
	}
	return result
}

func dtsRDSInstanceIDs(value any) []string {
	result := make([]string, 0)
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			instanceType, _ := caseInsensitiveString(typed, "InstanceType")
			instanceID, _ := caseInsensitiveString(typed, "InstanceID")
			if strings.EqualFold(strings.TrimSpace(instanceType), "RDS") && strings.TrimSpace(instanceID) != "" {
				result = append(result, instanceID)
			}
			for _, nested := range typed {
				visit(nested)
			}
		case []any:
			for _, nested := range typed {
				visit(nested)
			}
		case []map[string]any:
			for _, nested := range typed {
				visit(nested)
			}
		}
	}
	visit(value)
	return uniqueSortedStrings(result)
}

func caseInsensitiveString(values map[string]any, name string) (string, bool) {
	for key, value := range values {
		if !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		return nonEmptyString(value)
	}
	return "", false
}

func stringValues(value any) []string {
	result := make([]string, 0)
	for _, item := range anySlice(value) {
		if text, ok := nonEmptyString(item); ok {
			result = append(result, text)
		}
	}
	return result
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
