package execution

import (
	"net/url"
	"strings"
)

// PublicOperationID preserves an operation's identity without exposing signed
// polling credentials. The execution journal must keep the original value.
func PublicOperationID(value string) string {
	if !strings.HasPrefix(strings.ToLower(value), "https://") && !strings.HasPrefix(strings.ToLower(value), "http://") {
		return value
	}
	u, err := url.Parse(value)
	if err != nil {
		return "[redacted operation URL]"
	}
	query := url.Values{}
	if values := u.Query()["api-version"]; len(values) == 1 {
		query.Set("api-version", values[0])
	}
	u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, query.Encode(), "", false
	return u.String()
}

// PublicActionAttempt is a display copy; it never rewrites persisted receipts.
func PublicActionAttempt(value ActionAttempt) ActionAttempt {
	value.ProviderOperationID = PublicOperationID(value.ProviderOperationID)
	if value.ProviderResult != nil {
		value.ProviderResult = publicOperationData(value.ProviderResult).(map[string]any)
	}
	return value
}

func publicOperationData(value any) any {
	switch value := value.(type) {
	case string:
		return PublicOperationID(value)
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = publicOperationData(item)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = publicOperationData(item)
		}
		return result
	default:
		return value
	}
}
