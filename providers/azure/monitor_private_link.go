package azure

import (
	"net/http"
	"net/url"
	"strings"
)

const (
	monitorPrivateLinkType       = "Microsoft.Insights/privateLinkScopes"
	monitorScopedResourceType    = monitorPrivateLinkType + "/scopedResources"
	monitorPrivateConnectionType = monitorPrivateLinkType + "/privateEndpointConnections"
	monitorPrivateLinkVersion    = "2021-09-01"
)

func monitorPrivateLinkKind(kind string) string {
	for _, candidate := range []string{monitorPrivateLinkType, monitorScopedResourceType, monitorPrivateConnectionType} {
		if strings.EqualFold(candidate, kind) {
			return candidate
		}
	}
	return ""
}

// The native Location example is relative and omits api-version. Resolve only
// this documented operation-status path in the initiating resource's group.
// A scope name is absent from that path; the action receipt must separately bind
// the returned operation to its initiating resource before a resumed poll.
func monitorPrivateLinkOperationURL(subscription, resource, endpoint string) (string, error) {
	owner, kind, err := parseID(resource)
	if err != nil || monitorPrivateLinkKind(kind) == "" || !strings.HasPrefix(owner, "/subscriptions/"+strings.ToLower(subscription)+"/") {
		return "", serviceDenied("invalid_monitor_private_link_operation_owner")
	}
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32*1024 || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "%\\\x00\r\n") {
		return "", serviceDenied("invalid_monitor_private_link_operation_url")
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
		u.Scheme, u.Host = "https", "management.azure.com"
	}
	if u.Scheme != "https" || u.Host != "management.azure.com" {
		return "", serviceDenied("invalid_monitor_private_link_operation_origin")
	}
	parts := strings.Split(u.Path, "/")
	group := strings.Join(strings.Split(owner, "/")[:5], "/")
	if len(parts) != 9 || !strings.EqualFold(strings.Join(parts[:5], "/"), group) || !strings.EqualFold(strings.Join(parts[5:8], "/"), "providers/Microsoft.Insights/privateLinkScopeOperationStatuses") || !uuidPattern.MatchString(parts[8]) {
		return "", serviceDenied("monitor_private_link_operation_scope_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) > 1 || len(query["api-version"]) > 1 || len(query) == 1 && query.Get("api-version") != monitorPrivateLinkVersion {
		return "", serviceDenied("invalid_monitor_private_link_operation_version")
	}
	query.Set("api-version", monitorPrivateLinkVersion)
	u.RawQuery = query.Encode()
	u.ForceQuery = false
	return u.String(), nil
}

func monitorPrivateLinkResponse(method string, endpoint *url.URL, result *response) error {
	_, kind, err := parseID(endpoint.Path)
	if err == nil && method == http.MethodGet && strings.EqualFold(kind, "Microsoft.Insights/privateLinkScopeOperationStatuses") && endpoint.Query().Get("api-version") == monitorPrivateLinkVersion {
		if result.status != http.StatusOK || text(result.data["status"]) == "" {
			return serviceDenied("invalid_monitor_private_link_operation_response")
		}
		for field, expected := range map[string]string{"id": endpoint.Path, "name": last(endpoint.Path)} {
			if value, present := result.data[field]; present && !strings.EqualFold(text(value), expected) {
				return serviceDenied("monitor_private_link_operation_response_changed")
			}
		}
		return nil
	}
	if err != nil || monitorPrivateLinkKind(kind) == "" || method != http.MethodDelete || endpoint.Query().Get("api-version") != monitorPrivateLinkVersion {
		return nil
	}
	if result.status != http.StatusAccepted && result.status != http.StatusNoContent && (result.status != http.StatusOK || strings.EqualFold(kind, monitorPrivateLinkType)) || len(result.data) != 0 {
		return serviceDenied("invalid_monitor_private_link_delete_response")
	}
	location := ""
	for _, key := range []string{"Location", "Azure-AsyncOperation", "Operation-Location"} {
		values := result.header.Values(key)
		if len(values) > 1 || len(values) == 1 && values[0] == "" {
			return serviceDenied("invalid_monitor_private_link_operation_header")
		}
		if len(values) == 0 {
			continue
		}
		value, err := monitorPrivateLinkOperationURL(strings.Split(endpoint.Path, "/")[2], endpoint.Path, values[0])
		if err != nil {
			return err
		}
		if location != "" && !strings.EqualFold(location, value) {
			return serviceDenied("monitor_private_link_operation_headers_disagree")
		}
		location = value
	}
	if result.status == http.StatusAccepted && location == "" {
		return serviceDenied("monitor_private_link_operation_location_missing")
	}
	if location != "" {
		// All these headers describe the same operation-status resource. Use
		// status polling even when Azure returned it through Location.
		result.header = result.header.Clone()
		for _, key := range []string{"Location", "Operation-Location"} {
			result.header.Del(key)
		}
		result.header.Set("Azure-AsyncOperation", location)
	}
	return nil
}
