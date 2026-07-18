package alicloud

import (
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const polarDBApplicationNativeType = "ACS::PolarDB::Application"

func enrichPolarDBApplicationTopology(
	items []contracts.InventoryItem,
	details map[string]map[string]any,
) []contracts.InventoryItem {
	result := enrichTopologyItems(
		items,
		polarDBApplicationNativeType,
		details,
		func(detail map[string]any) map[string]any {
			normalized := make(map[string]any, 10)
			for _, field := range []struct {
				normalized string
				provider   string
			}{
				{normalized: "name", provider: "Description"},
				{normalized: "state", provider: "Status"},
				{normalized: "applicationType", provider: "ApplicationType"},
				{normalized: "dbClusterId", provider: "DBClusterId"},
				{normalized: "polarFSInstanceId", provider: "PolarFSInstanceId"},
				{normalized: "vpcId", provider: "VPCId"},
				{normalized: "vSwitchId", provider: "VSwitchId"},
				{normalized: "zoneId", provider: "ZoneId"},
			} {
				copyTopologyString(normalized, field.normalized, detail[field.provider])
			}
			return normalized
		},
	)
	for index := range result {
		if result[index].NativeType != polarDBApplicationNativeType {
			continue
		}
		references := make([]string, 0, 3)
		for _, field := range []string{"vpcId", "vSwitchId", "dbClusterId"} {
			if value := strings.TrimSpace(stringValue(result[index].Normalized[field])); value != "" {
				references = append(references, value)
			}
		}
		result[index].NetworkReferences = appendUniqueReferences(
			result[index].NetworkReferences,
			references,
		)
	}
	return result
}
