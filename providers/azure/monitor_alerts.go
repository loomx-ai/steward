package azure

import "strings"

func monitorAlertPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "providers" {
			continue
		}
		switch parts[i+1] + "/" + parts[i+2] {
		case "microsoft.insights/metricalerts", "microsoft.insights/actiongroups", "microsoft.insights/activitylogalerts", "microsoft.insights/scheduledqueryrules",
			"microsoft.alertsmanagement/smartdetectoralertrules", "microsoft.alertsmanagement/prometheusrulegroups", "microsoft.alertsmanagement/actionrules":
			return true
		}
	}
	return false
}

// Alert expressions, dimensions and notification destinations can contain
// secrets under ordinary keys. Keep their original values in private reads for
// references and configuration proofs, never in inventory, logs or Invoke.
// List and status responses without resource identities use endpoint context.
func monitorAlertSafeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			field := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if strings.HasSuffix(field, "receivers") {
				continue
			}
			switch field {
			case "criteria", "condition", "conditions", "query", "expression", "dimensions", "labels", "annotations", "parameters", "parameterdefinitions",
				"customproperties", "actionproperties", "webhookproperties", "customemailsubject", "customwebhookpayload":
				continue
			}
			result[key] = monitorAlertSafeValue(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, value := range typed {
			result[i] = monitorAlertSafeValue(value)
		}
		return result
	default:
		return value
	}
}
