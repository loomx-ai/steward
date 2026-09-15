package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) netappPollURL(id, region, endpoint, role string) (string, error) {
	_, kind, err := parseID(id)
	if err != nil || c.netappIdentity(id, kind) != nil || region == "" || region != strings.ToLower(strings.TrimSpace(region)) || c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_netapp_operation_owner")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	parts := strings.Split(u.Path, "/")
	expected := c.root() + "/providers/microsoft.netapp/locations/" + region + "/operationresults/"
	if len(parts) != 9 || !strings.EqualFold(strings.Join(parts[:8], "/")+"/", expected) || !uuidPattern.MatchString(parts[8]) || u.RawPath != "" || u.ForceQuery {
		return "", serviceDenied("netapp_operation_scope_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["api-version"]) != 1 || q.Get("api-version") != netappVersion {
		return "", serviceDenied("netapp_operation_version_changed")
	}
	signed := 0
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return "", serviceDenied("invalid_netapp_operation_query")
		}
		switch key {
		case "api-version":
		case "operationResultResponseType":
			if role != "result_url" || values[0] != "Location" {
				return "", serviceDenied("netapp_operation_role_changed")
			}
		case "t", "c", "s", "h":
			signed++
		default:
			return "", serviceDenied("unknown_netapp_operation_query")
		}
	}
	if role != "status_url" && role != "result_url" || role == "result_url" && q.Get("operationResultResponseType") != "Location" || signed != 0 && signed != 4 {
		return "", serviceDenied("invalid_netapp_operation_role")
	}
	return strings.ToLower(parts[8]), nil
}
func (c *client) netappOperationHeaders(id, region string, h http.Header) (map[string]any, error) {
	if len(h.Values("Operation-Location")) != 0 || len(h.Values("Azure-AsyncOperation")) > 1 || len(h.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_netapp_operation_headers")
	}
	result := map[string]any{}
	identity := ""
	for _, entry := range []struct{ header, role string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		value := h.Get(entry.header)
		if value == "" {
			if len(h.Values(entry.header)) != 0 {
				return nil, serviceDenied("empty_netapp_operation_header")
			}
			continue
		}
		current, err := c.netappPollURL(id, region, value, entry.role)
		if err != nil {
			return nil, err
		}
		if identity != "" && identity != current {
			return nil, serviceDenied("netapp_operation_headers_disagree")
		}
		identity = current
		result[entry.role] = value
	}
	return result, nil
}
func (c *client) netappSignReceipt(id, region string, receipt map[string]any) map[string]any {
	result := maps.Clone(receipt)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "netapp-delete-1", "resource": id, "region": region, "receipt": result})
	return result
}
func (c *client) netappDeleteReceipt(id, region string, res response) (map[string]any, error) {
	if err := operationError(res); err != nil {
		return nil, err
	}
	_, kind, err := parseID(id)
	if err != nil || c.netappIdentity(id, kind) != nil || region == "" || region != strings.ToLower(strings.TrimSpace(region)) {
		return nil, serviceDenied("invalid_netapp_delete_owner")
	}
	allowed := res.status == 202 || res.status == 204 || res.status == 200 && slices.Contains([]string{"Snapshots", "Subvolumes", "VolumeQuotaRules", "VolumeGroups", "BackupPolicies", "SnapshotPolicies"}, netappKind(kind).family)
	if !allowed || len(res.data) != 0 {
		return nil, serviceDenied("invalid_netapp_delete_response")
	}
	receipt, err := c.netappOperationHeaders(id, region, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status != 202 && len(receipt) != 0 {
		return nil, serviceDenied("incomplete_netapp_delete_receipt")
	}
	return c.netappSignReceipt(id, region, receipt), nil
}
func (c *client) netappVerifyReceipt(id, region string, receipt map[string]any) error {
	_, kind, err := parseID(id)
	if err != nil || c.netappIdentity(id, kind) != nil || region == "" || region != strings.ToLower(strings.TrimSpace(region)) {
		return serviceDenied("invalid_netapp_saved_owner")
	}
	if receipt["binding"] != c.netappSignReceipt(id, region, receipt)["binding"] {
		return serviceDenied("netapp_receipt_changed")
	}
	h := http.Header{}
	for key, v := range receipt {
		switch key {
		case "binding":
		case "status_done", "complete":
			if v != true {
				return serviceDenied("invalid_netapp_saved_phase")
			}
		case "status_url", "result_url":
			value, ok := v.(string)
			if !ok || value == "" {
				return serviceDenied("invalid_netapp_saved_url")
			}
			header := "Location"
			if key == "status_url" {
				header = "Azure-AsyncOperation"
			}
			h.Set(header, value)
		default:
			return serviceDenied("unknown_netapp_saved_field")
		}
	}
	if receipt["status_done"] == true && (receipt["status_url"] == nil || receipt["result_url"] == nil) {
		return serviceDenied("invalid_netapp_saved_phase")
	}
	_, err = c.netappOperationHeaders(id, region, h)
	return err
}
func (c *client) netappPoll(ctx context.Context, id, region string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err = c.netappVerifyReceipt(id, region, receipt); err != nil {
		return out, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true || current["status_url"] == nil && current["result_url"] == nil {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	role := "status_url"
	if current[role] == nil || current["status_done"] == true {
		role = "result_url"
	}
	endpoint := text(current[role])
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("netapp_poll_url_changed")
		}
		_, err := c.netappPollURL(id, region, next, role)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, role == "result_url")
	if err != nil {
		return out, err
	}
	if err = operationError(res); err != nil {
		return out, err
	}
	if res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return out, serviceDenied("invalid_netapp_poll_body")
	}
	next, err := c.netappOperationHeaders(id, region, res.header)
	if err != nil {
		return out, err
	}
	for key, v := range next {
		if v != current[key] {
			return out, serviceDenied("netapp_poll_continuation_changed")
		}
	}
	done := false
	state := ""
	if role == "result_url" {
		if !slices.Contains([]int{200, 202, 204}, res.status) || len(res.data) != 0 {
			return out, serviceDenied("invalid_netapp_poll_result")
		}
		done = res.status != 202
	} else {
		operation, _ := c.netappPollURL(id, region, endpoint, role)
		u, _ := url.Parse(endpoint)
		if res.status != 200 || !strings.EqualFold(text(res.data["id"]), u.Path) || !strings.EqualFold(text(res.data["name"]), operation) {
			return out, serviceDenied("netapp_poll_identity_changed")
		}
		props := object(res.data["properties"])
		if !strings.EqualFold(text(props["resourceName"]), id) || props["action"] != "DELETE" {
			return out, serviceDenied("netapp_poll_target_changed")
		}
		state = text(res.data["status"])
		if !slices.Contains([]string{"Accepted", "InProgress", "Deleting", "Succeeded"}, state) {
			return out, serviceDenied("netapp_poll_state_unverified")
		}
		done = state == "Succeeded"
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.netappSignReceipt(id, region, current)}, nil
}
