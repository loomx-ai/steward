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
	Method  string
	URL     string
	Body    []byte
	Headers map[string]string
}

func BindREST(operation Operation, parameters map[string]any) (RESTRequest, error) {
	call := operation.Call
	if call == nil || (call.Style != "google-rest" && call.Style != "azure-rest" && call.Style != "azure-batch-rest" && call.Style != "azure-communication-rest") {
		return RESTRequest{}, fmt.Errorf("operation %q is not a REST operation", operation.ID)
	}
	domain := "googleapis.com"
	if call.Style == "azure-rest" {
		domain = "management.azure.com"
	}
	origin, err := trustedRESTOrigin(call.Endpoint, domain)
	if call.Style == "azure-batch-rest" {
		endpoint, _ := parameters["endpoint"].(string)
		origin, err = trustedRESTOrigin(endpoint, "batch.azure.com")
		if call.Endpoint != "{endpoint}" || !slices.Equal(call.EndpointParameters, []string{"endpoint"}) || !azureBatchOrigin.MatchString(origin) {
			return RESTRequest{}, fmt.Errorf("invalid Azure Batch endpoint")
		}
	}
	if call.Style == "azure-communication-rest" {
		endpoint, _ := parameters["endpoint"].(string)
		origin, err = trustedRESTOrigin(endpoint, "communication.azure.com")
		if call.Endpoint != "{endpoint}" || !slices.Equal(call.EndpointParameters, []string{"endpoint"}) || !azureCommunicationOrigin.MatchString(origin) {
			return RESTRequest{}, fmt.Errorf("invalid Azure Communication endpoint")
		}
	}
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
	if call.Style == "azure-rest" || call.Style == "azure-batch-rest" || call.Style == "azure-communication-rest" {
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
		if call.Style == "azure-rest" && call.Version == "2015-05-01" && name == "exportId" &&
			call.Path == "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Insights/components/{resourceName}/exportconfiguration/{exportId}" &&
			((call.Method == "GET" && operation.ID == "Azure.Microsoft.Insights.ExportConfigurations_Get") || (call.Method == "DELETE" && operation.ID == "Azure.Microsoft.Insights.ExportConfigurations_Delete")) {
			// Native continuous-export IDs are opaque, case-sensitive base64
			// values. A slash belongs to this one parameter, not the ARM path.
			for _, part := range strings.Split(value, "/") {
				if part == "." || part == ".." || strings.ContainsAny(part, "\\%\x00\r\n") {
					return RESTRequest{}, fmt.Errorf("invalid export identifier")
				}
			}
			path = strings.ReplaceAll(path, match[0], url.PathEscape(value))
			continue
		}
		if call.Style == "azure-batch-rest" && call.Method == "HEAD" && call.Path == "/pools/{poolId}/nodes/{nodeId}/files/{filePath}" && name == "filePath" {
			// The native file parameter is a complete Windows or Linux path.
			// Encode it as one parameter; never allow directory traversal.
			value = strings.ReplaceAll(value, "\\", "/")
			for _, part := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
				if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "%\x00\r\n") {
					return RESTRequest{}, fmt.Errorf("invalid Batch file path")
				}
			}
			path = strings.ReplaceAll(path, match[0], url.PathEscape(value))
			continue
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
			text, isString := value.(string)
			// Monitor workspace names use this ECMA-262 leading-hyphen guard.
			// Evaluate it explicitly before compiling the remaining RE2 pattern.
			if strings.HasPrefix(pattern, "^(?!-)") {
				if strings.HasPrefix(text, "-") {
					return RESTRequest{}, fmt.Errorf("parameter %q does not match its API pattern", name)
				}
				pattern = "^" + strings.TrimPrefix(pattern, "^(?!-)")
			}
			// Azure MySQL uses an ECMAScript trailing-hyphen lookbehind.
			// Its anchored equivalent needs no backtracking regex engine.
			if strings.HasSuffix(pattern, "(?<!-)$") {
				if strings.HasSuffix(text, "-") {
					return RESTRequest{}, fmt.Errorf("parameter %q does not match its API pattern", name)
				}
				pattern = strings.TrimSuffix(pattern, "(?<!-)$") + "$"
			}
			// SQL VM names exclude a leading underscore and trailing dot or
			// hyphen with ECMA-262 lookarounds. Preserve both guards in RE2.
			if strings.HasPrefix(pattern, "^((?!_)") && strings.HasSuffix(pattern, "(?<![.-]))$") {
				if strings.HasPrefix(text, "_") || strings.HasSuffix(text, ".") || strings.HasSuffix(text, "-") {
					return RESTRequest{}, fmt.Errorf("parameter %q does not match its API pattern", name)
				}
				pattern = "^" + strings.TrimSuffix(strings.TrimPrefix(pattern, "^((?!_)"), "(?<![.-]))$") + "$"
			}
			// Redis Enterprise and Search names use bounded ECMA-262 lookaheads.
			// The remaining native pattern accepts only ASCII characters.
			for _, minimum := range []int{1, 2} {
				prefix := fmt.Sprintf("^(?=.{%d,60}$)", minimum)
				if !strings.HasPrefix(pattern, prefix) {
					continue
				}
				if len(text) < minimum || len(text) > 60 {
					return RESTRequest{}, fmt.Errorf("parameter %q does not match its API pattern", name)
				}
				pattern = "^" + strings.TrimPrefix(pattern, prefix)
			}
			rule, err := regexp.Compile(pattern)
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
		if position == "header" {
			text, ok := value.(string)
			if !ok || strings.ContainsAny(text, "\r\n\x00") {
				return RESTRequest{}, fmt.Errorf("invalid header parameter %q", name)
			}
			switch strings.ToLower(name) {
			case "authorization", "proxy-authorization", "cookie", "host":
				return RESTRequest{}, fmt.Errorf("credential headers cannot be supplied as operation parameters")
			}
			if result.Headers == nil {
				result.Headers = map[string]string{}
			}
			result.Headers[name] = text
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

// The runtime additionally binds this public-cloud endpoint to the Batch
// account returned by ARM for the explicitly selected subscription.
var azureBatchOrigin = regexp.MustCompile(`^https://[a-z0-9]{3,24}\.[a-z0-9-]+\.batch\.azure\.com$`)

// Runtime authorization additionally verifies the endpoint returned by the
// selected subscription's native Communication Services resource GET.
var azureCommunicationOrigin = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\.communication\.azure\.com$`)
