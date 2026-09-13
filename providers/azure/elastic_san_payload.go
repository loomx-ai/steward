package azure

// Publish operational capacity and retention state. Key Vault configuration,
// identities, client/target addresses and unknown properties stay private.
func elasticSanSafeValue(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = elasticSanSafeValue(entry)
		}
		return result
	case map[string]any:
		result := object(hybridComputeSafeValue(value))
		if props := object(value["properties"]); props != nil {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "volumeId", "volumeName", "protocolType", "encryption", "publicNetworkAccess"} {
				if v, ok := props[key].(string); ok {
					public[key] = v
				}
			}
			for _, key := range []string{"baseSizeTiB", "extendedCapacitySizeTiB", "totalVolumeSizeGiB", "volumeGroupCount", "totalIops", "totalMBps", "totalSizeTiB", "sizeGiB", "sourceVolumeSizeGiB"} {
				if v, err := batchInteger(props[key], 64); err == nil && v >= 0 {
					public[key] = v
				}
			}
			for _, key := range []string{"enforceDataIntegrityCheckForIscsi", "encryptionInTransit"} {
				if v, ok := props[key].(bool); ok {
					public[key] = v
				}
			}
			if policy := object(props["deleteRetentionPolicy"]); policy != nil {
				retention := map[string]any{}
				if state, ok := policy["policyState"].(string); ok {
					retention["policyState"] = state
				}
				if days, err := batchInteger(policy["retentionPeriodDays"], 32); err == nil && days >= 0 {
					retention["retentionPeriodDays"] = days
				}
				public["deleteRetentionPolicy"] = retention
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if v, ok := value[key]; ok {
				result[key] = elasticSanSafeValue(v)
			}
		}
		return result
	default:
		return nil
	}
}
