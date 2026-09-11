package azure

import (
	"net/http"
	"net/url"
	"strings"
)

const applicationInsightsType = "Microsoft.Insights/components"

// Five native component operations return an array, including Annotations_Get.
// Adapt them to the provider's internal value envelope only after binding the
// method, API version, component parent and exact collection path.
func applicationInsightsResponseShape(method string, endpoint *url.URL) string {
	if method != http.MethodGet || endpoint.Host != "management.azure.com" || len(endpoint.Query()["api-version"]) != 1 {
		return ""
	}
	parts := strings.Split(endpoint.Path, "/")
	if endpoint.Query().Get("api-version") == "2021-03-08" {
		// MyWorkbooks publishes direct-array examples for both list scopes,
		// while its schema describes a value envelope without a type keyword.
		valid := len(parts) == 6 && strings.EqualFold(parts[1], "subscriptions") && uuidPattern.MatchString(parts[2]) && strings.EqualFold(strings.Join(parts[3:], "/"), "providers/Microsoft.Insights/myWorkbooks")
		if len(parts) == 8 && strings.EqualFold(strings.Join(parts[5:], "/"), "providers/Microsoft.Insights/myWorkbooks") {
			_, kind, err := parseID(strings.Join(parts[:5], "/"))
			valid = err == nil && strings.EqualFold(kind, groupType)
		}
		if valid {
			return "array-or-object"
		}
	}
	if endpoint.Query().Get("api-version") != "2015-05-01" {
		return ""
	}
	if len(parts) != 10 && len(parts) != 11 {
		return ""
	}
	_, kind, err := parseID(strings.Join(parts[:9], "/"))
	if err != nil || !strings.EqualFold(kind, applicationInsightsType) {
		return ""
	}
	if len(parts) == 11 {
		_, _, err := parseID(endpoint.Path)
		if err == nil && strings.EqualFold(parts[9], "Annotations") {
			return "array"
		}
		return ""
	}
	switch strings.ToLower(parts[9]) {
	case "analyticsitems", "myanalyticsitems", "exportconfiguration", "favorites", "proactivedetectionconfigs":
		return "array"
	}
	return ""
}

func applicationInsightsPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "providers" || parts[i+1] != "microsoft.insights" {
			continue
		}
		switch parts[i+2] {
		case "components", "workbooks", "myworkbooks", "workbooktemplates", "webtests":
			return true
		}
	}
	return false
}

func applicationInsightsRaw(raw map[string]any) bool {
	return applicationInsightsPath(text(raw["id"])) || applicationInsightsPath("/providers/"+text(raw["type"]))
}

// Legacy component children have no ARM type or full ID. Their endpoint carries
// the family context for logging and Invoke; normalized inventory carries it in
// type/id. Keep opaque content private even when it has no secret-shaped keys.
func applicationInsightsSafeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "apikey", "instrumentationkey", "hockeyapptoken", "content", "config", "configproperties", "serializeddata", "templatedata", "customemails", "ruledefinitions", "configuration", "request", "contentmatch", "permanenterrorreason":
				continue
			case "properties":
				if _, opaque := value.(string); opaque {
					continue // Annotation JSON is an opaque string, not ARM properties.
				}
			case "storageuri", "sourceid":
				if endpoint, err := url.Parse(text(value)); err == nil && endpoint.Scheme != "" {
					endpoint.RawQuery, endpoint.Fragment, endpoint.User = "", "", nil
					value = endpoint.String()
				}
			}
			result[key] = applicationInsightsSafeValue(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, value := range typed {
			result[i] = applicationInsightsSafeValue(value)
		}
		return result
	default:
		return value
	}
}

func safeAPIPayload(value map[string]any, endpoint string) map[string]any {
	u, err := url.Parse(endpoint)
	if err == nil && u.Host == "management.azure.com" {
		if applicationInsightsPath(u.Path) {
			value = object(applicationInsightsSafeValue(value))
		}
		if monitorAlertPath(u.Path) {
			value = object(monitorAlertSafeValue(value))
		}
		if monitorBudgetPath(u.Path) {
			value = object(monitorBudgetSafeValue(value))
		}
		if diagnosticSettingsPath(u.Path) || rbacPath(u.Path) {
			cleaned := object(diagnosticSettingsSafeValue(value))
			if value["path"] == u.Path && (value["method"] == "GET" || value["method"] == "DELETE") {
				cleaned["method"], cleaned["path"] = value["method"], u.Path
				cleaned["query"] = map[string]any{"api-version": u.Query().Get("api-version")}
			}
			value = cleaned
		}
	}
	return safePayload(value)
}
