package azure

// Public operational state and network addresses are useful inventory. Guest
// credentials, OS setup, SSH keys, proxy settings, paths and error text are not.
func azureLocalSafeValue(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, v := range value {
			result[i] = azureLocalSafeValue(v)
		}
		return result
	case map[string]any:
		result := object(hybridComputeSafeValue(value))
		if props := object(value["properties"]); props != nil {
			public := object(result["properties"])
			for _, key := range []string{"vmId", "resourceUid", "provisioningAction", "macAddress", "diskFileFormat", "hyperVGeneration", "osType", "vmSwitchName", "ipAddress", "ipAllocationMethod", "addressPrefix", "powerState", "vmSize", "publisher", "offer", "sku"} {
				if v, ok := props[key].(string); ok {
					public[key] = v
				}
			}
			for _, key := range []string{"diskSizeGB", "memoryMB", "processors", "blockSizeBytes", "logicalSectorBytes", "physicalSectorBytes", "vlan", "availableSizeMB", "containerSizeMB", "progressPercentage"} {
				if v, err := batchInteger(props[key], 64); err == nil && v >= 0 {
					public[key] = v
				}
			}
			if v, ok := props["dynamic"].(bool); ok {
				public["dynamic"] = v
			}
			if values, ok := props["addressPrefixes"].([]any); ok {
				prefixes := []string{}
				for _, v := range values {
					if str, ok := v.(string); ok {
						prefixes = append(prefixes, str)
					}
				}
				public["addressPrefixes"] = prefixes
			}
			for _, key := range []string{"status", "hardwareProfile", "identifier"} {
				if nested := object(props[key]); nested != nil {
					public[key] = object(azureLocalSafeValue(map[string]any{"properties": nested}))["properties"]
				}
			}
			for _, key := range []string{"ipConfigurations", "subnets"} {
				if rows, ok := props[key].([]any); ok {
					public[key] = azureLocalSafeValue(rows)
				}
			}
		}
		for _, key := range []string{"body", "value"} {
			if v, ok := value[key]; ok {
				result[key] = azureLocalSafeValue(v)
			}
		}
		return result
	default:
		return nil
	}
}
