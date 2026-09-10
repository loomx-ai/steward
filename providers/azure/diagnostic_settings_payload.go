package azure

import "strings"

func diagnosticSettingsPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "providers" && parts[i+1] == "microsoft.insights" && parts[i+2] == "diagnosticsettings" {
			return true
		}
	}
	return false
}

// The public result retains identity and transport provenance. Unknown authored
// fields stay private alongside the complete settings configuration.
func diagnosticSettingsSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, entry := range value {
			switch strings.ToLower(key) {
			case "id", "name", "type", "tags", "request_id", "status_code":
				result[key] = entry
			case "body", "value", "nextlink":
				result[key] = diagnosticSettingsSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = diagnosticSettingsSafeValue(entry)
		}
		return result
	default:
		return value
	}
}
