package alicloud

import "github.com/loomx-ai/steward/internal/provider/contracts"

func enrichOSSBucketProperties(
	items []contracts.InventoryItem,
	details map[string]map[string]any,
) []contracts.InventoryItem {
	return enrichTopologyItems(
		items,
		ossBucketNativeType,
		details,
		func(detail map[string]any) map[string]any {
			normalized := make(map[string]any, 7)
			copyTopologyValue(normalized, "name", detail["Name"])
			copyTopologyValue(normalized, "createdAt", detail["CreationDate"])
			copyTopologyValue(normalized, "location", detail["Location"])
			copyTopologyValue(normalized, "region", detail["Region"])
			copyTopologyValue(normalized, "resourceGroupId", detail["ResourceGroupId"])
			copyTopologyValue(normalized, "storageClass", detail["StorageClass"])
			copyTopologyValue(
				normalized,
				"acl",
				valueAtPath(detail, "AccessControlList.Grant"),
			)
			return normalized
		},
	)
}
