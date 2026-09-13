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

// Accept only returned ARM operation endpoints in the selected subscription and
// StackHCI provider. Keep their signed queries intact; never use the Swagger
// examples' http://azure.async.operation/status placeholder as a callback.
func (c *client) azureLocalPollURL(id, endpoint string) (string, error) {
	if canonical, err := c.azureLocalIdentity(id, azureLocalAgentType); err != nil || canonical != id {
		return "", serviceDenied("invalid_azure_local_operation_owner")
	}
	if c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_azure_local_operation_url")
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if u.RawPath != "" || u.ForceQuery || len(parts) != 9 || strings.Join(parts[:6], "/") != c.root()+"/providers/microsoft.azurestackhci/locations" || !cosmosOperationRegion.MatchString(parts[6]) || !slices.Contains([]string{"operations", "operationstatus", "operationstatuses", "operationresults"}, parts[7]) || !uuidPattern.MatchString(parts[8]) {
		return "", serviceDenied("azure_local_operation_scope_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["api-version"]) != 1 || q.Get("api-version") != azureLocalVersion || len(q) != 1 && len(q) != 5 {
		return "", serviceDenied("azure_local_operation_query_changed")
	}
	if len(q) == 5 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(q[key]) != 1 || q.Get(key) == "" || strings.ContainsAny(q.Get(key), "\x00\r\n\t ") {
				return "", serviceDenied("invalid_azure_local_operation_signature")
			}
		}
	}
	return parts[6] + "/" + parts[8], nil
}

func (c *client) azureLocalOperationHeaders(id string, headers http.Header) (map[string]any, error) {
	if len(headers.Values("Operation-Location")) != 0 || len(headers.Values("Azure-AsyncOperation")) > 1 || len(headers.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_azure_local_operation_headers")
	}
	selected, identity := map[string]any{}, ""
	for _, name := range []string{"Azure-AsyncOperation", "Location"} {
		endpoint := headers.Get(name)
		if endpoint == "" {
			if len(headers.Values(name)) != 0 {
				return nil, serviceDenied("empty_azure_local_operation_header")
			}
			continue
		}
		current, err := c.azureLocalPollURL(id, endpoint)
		if err != nil {
			return nil, err
		}
		if identity != "" && identity != current {
			return nil, serviceDenied("azure_local_operation_headers_disagree")
		}
		identity = current
		if len(selected) == 0 {
			selected["url"], selected["mode"] = endpoint, name
		}
	}
	return selected, nil
}

func (c *client) azureLocalSignReceipt(id string, receipt map[string]any) map[string]any {
	out := maps.Clone(receipt)
	if out == nil {
		out = map[string]any{}
	}
	delete(out, "binding")
	out["binding"] = c.privateConfiguration(map[string]any{"protocol": "azure-local-delete-1", "id": id, "receipt": out})
	return out
}

func (c *client) azureLocalDeleteReceipt(id string, res response) (map[string]any, error) {
	if _, err := c.azureLocalIdentity(id, azureLocalAgentType); err != nil {
		return nil, err
	}
	if err := operationError(res); err != nil {
		return nil, err
	}
	if len(res.data) != 0 || res.status != 202 && res.status != 204 {
		return nil, serviceDenied("invalid_azure_local_delete_response")
	}
	receipt, err := c.azureLocalOperationHeaders(id, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status == 204 && len(receipt) != 0 {
		return nil, serviceDenied("invalid_azure_local_delete_receipt")
	}
	return c.azureLocalSignReceipt(id, receipt), nil
}

func (c *client) azureLocalVerifyReceipt(id string, receipt map[string]any) error {
	if canonical, err := c.azureLocalIdentity(id, azureLocalAgentType); err != nil || canonical != id {
		return serviceDenied("invalid_azure_local_receipt_owner")
	}
	if receipt["binding"] != c.azureLocalSignReceipt(id, receipt)["binding"] {
		return serviceDenied("azure_local_saved_receipt_changed")
	}
	for key, value := range receipt {
		switch key {
		case "binding":
		case "complete":
			if value != true {
				return serviceDenied("invalid_azure_local_saved_phase")
			}
		case "url", "mode":
			if text(value) == "" {
				return serviceDenied("invalid_azure_local_saved_url")
			}
		default:
			return serviceDenied("unknown_azure_local_saved_field")
		}
	}
	if receipt["url"] == nil && receipt["mode"] == nil {
		return nil
	}
	if !slices.Contains([]string{"Azure-AsyncOperation", "Location"}, text(receipt["mode"])) {
		return serviceDenied("invalid_azure_local_saved_mode")
	}
	_, err := c.azureLocalPollURL(id, text(receipt["url"]))
	return err
}

// ARM and the vendored 2024 SDK prefer Azure-AsyncOperation over Location.
// Operation success is only a transport result; the action must read its own
// resource again. In particular an operation 404 is not resource absence.
func (c *client) azureLocalPoll(ctx context.Context, id string, receipt map[string]any) (contracts.WaitResult, error) {
	if err := c.azureLocalVerifyReceipt(id, receipt); err != nil {
		return contracts.WaitResult{}, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true || current["url"] == nil {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	endpoint, mode := text(current["url"]), text(current["mode"])
	validate := func(candidate string) error {
		if candidate != endpoint {
			return serviceDenied("azure_local_poll_url_changed")
		}
		_, err := c.azureLocalPollURL(id, candidate)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, mode == "Location")
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err := operationError(res); err != nil {
		return contracts.WaitResult{}, err
	}
	if res.status != 200 && res.status != 202 && !(mode == "Location" && res.status == 204) || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return contracts.WaitResult{}, serviceDenied("invalid_azure_local_poll_response")
	}
	u, _ := url.Parse(endpoint)
	for key, expected := range map[string]string{"id": u.Path, "name": last(u.Path), "resourceId": id} {
		if value, present := res.data[key]; present && !strings.EqualFold(text(value), expected) {
			return contracts.WaitResult{}, serviceDenied("azure_local_poll_identity_changed")
		}
	}
	state := text(res.data["status"])
	done := mode == "Location" && len(res.data) == 0 && (res.status == 200 || res.status == 204)
	if !done && !(mode == "Location" && res.status == 202 && len(res.data) == 0) {
		if state == "" || res.data["status"] != state || !slices.Contains([]string{"Accepted", "Queued", "InProgress", "Running", "Succeeded"}, state) || state == "Succeeded" && res.status != 200 {
			return contracts.WaitResult{}, serviceDenied("azure_local_poll_state_unverified")
		}
		done = state == "Succeeded"
	}
	next, err := c.azureLocalOperationHeaders(id, res.header)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if next["url"] != nil {
		oldURL, _ := url.Parse(endpoint)
		newURL, _ := url.Parse(text(next["url"]))
		if !strings.EqualFold(oldURL.Path, newURL.Path) || next["mode"] != mode {
			return contracts.WaitResult{}, serviceDenied("azure_local_poll_rotation_changed_operation")
		}
		current["url"] = next["url"]
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.azureLocalSignReceipt(id, current)}, nil
}
