package catalog

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// RESTRequest is bound only from checked-in API metadata. Credential selection
// and project/subscription ownership remain the provider runtime's responsibility.
type RESTRequest struct {
	Method string
	URL    string
	Body   []byte
}

func BindREST(operation Operation, parameters map[string]any) (RESTRequest, error) {
	call := operation.Call
	if call == nil || (call.Style != "google-rest" && call.Style != "azure-rest") {
		return RESTRequest{}, fmt.Errorf("operation %q is not a REST operation", operation.ID)
	}
	domain := "googleapis.com"
	if call.Style == "azure-rest" {
		domain = "management.azure.com"
	}
	origin, err := trustedRESTOrigin(call.Endpoint, domain)
	if err != nil || !strings.HasPrefix(call.Path, "/") || strings.ContainsAny(call.Path, "?#") {
		return RESTRequest{}, fmt.Errorf("invalid REST operation endpoint or path")
	}
	properties, _ := operation.InputSchema["properties"].(map[string]any)
	values := map[string]any{}
	for key, value := range parameters {
		if _, exists := properties[key]; !exists {
			return RESTRequest{}, fmt.Errorf("unknown parameter %q for %s", key, operation.ID)
		}
		values[key] = value
	}
	if call.Style == "azure-rest" {
		if version, ok := values["api-version"]; ok && version != call.Version {
			return RESTRequest{}, fmt.Errorf("Azure API version differs from catalog")
		}
		values["api-version"] = call.Version
	}
	query := url.Values{}
	result := RESTRequest{Method: call.Method}
	path := call.Path
	for _, match := range restPathParameter.FindAllStringSubmatch(path, -1) {
		name := match[2]
		value, ok := values[name].(string)
		if !ok || value == "" {
			return RESTRequest{}, fmt.Errorf("path parameter %q is required", name)
		}
		parts := []string{value}
		if slices.Contains(call.RawPathParameters, name) {
			parts = strings.Split(value, "/")
		} else if strings.Contains(value, "/") {
			return RESTRequest{}, fmt.Errorf("path parameter %q must be one segment", name)
		}
		for i, part := range parts {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
				return RESTRequest{}, fmt.Errorf("invalid path parameter %q", name)
			}
			parts[i] = url.PathEscape(part)
		}
		path = strings.ReplaceAll(path, match[0], strings.Join(parts, "/"))
	}
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		value, present := values[name]
		if !present {
			if property["required"] == true {
				return RESTRequest{}, fmt.Errorf("parameter %q is required", name)
			}
			continue
		}
		if pattern, ok := property["pattern"].(string); ok {
			rule, err := regexp.Compile(pattern)
			text, isString := value.(string)
			if err != nil || !isString || !rule.MatchString(text) {
				return RESTRequest{}, fmt.Errorf("parameter %q does not match its API pattern", name)
			}
		}
		position, _ := property["location"].(string)
		if position == "" {
			position, _ = property["in"].(string)
		}
		if position == "path" {
			continue
		}
		if name == "body" || position == "body" {
			if result.Body != nil {
				return RESTRequest{}, fmt.Errorf("multiple REST request bodies")
			}
			result.Body, err = json.Marshal(value)
			if err != nil {
				return RESTRequest{}, fmt.Errorf("invalid REST request body")
			}
			continue
		}
		if position != "query" {
			return RESTRequest{}, fmt.Errorf("unsupported parameter position for %q", name)
		}
		add := func(value any) error {
			switch value.(type) {
			case string, bool, int, int32, int64, float64, json.Number:
				query.Add(name, fmt.Sprint(value))
				return nil
			default:
				return fmt.Errorf("invalid query parameter %q", name)
			}
		}
		switch values := value.(type) {
		case []any:
			for _, item := range values {
				if err := add(item); err != nil {
					return RESTRequest{}, err
				}
			}
		case []string:
			for _, item := range values {
				query.Add(name, item)
			}
		default:
			if err := add(value); err != nil {
				return RESTRequest{}, err
			}
		}
	}
	result.URL = origin + path
	if len(query) > 0 {
		result.URL += "?" + query.Encode()
	}
	return result, nil
}
