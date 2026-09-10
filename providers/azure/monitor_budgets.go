package azure

import "strings"

func monitorBudgetPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "providers" && parts[i+2] == "budgets" && (parts[i+1] == "microsoft.consumption" || parts[i+1] == "microsoft.costmanagement") {
			return true
		}
	}
	return false
}

// Notification names/destinations and arbitrary dimension/tag filters remain
// private. Full native reads retain them for references and drift detection.
func monitorBudgetSafeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			if strings.EqualFold(key, "notifications") || strings.EqualFold(key, "filter") {
				continue
			}
			result[key] = monitorBudgetSafeValue(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, value := range typed {
			result[i] = monitorBudgetSafeValue(value)
		}
		return result
	default:
		return value
	}
}
