package azure

import (
	"context"
	"encoding/hex"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) dataFactoryStopURL(id, endpoint string) (string, string, error) {
	if err := c.dataFactoryIdentity(id, dataFactoryIRType); err != nil {
		return "", "", err
	}
	if err := c.validateURL(endpoint); err != nil {
		return "", "", err
	}
	u, _ := url.Parse(endpoint)
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["api-version"]) != 1 || query.Get("api-version") != dataFactoryVersion || u.RawPath != "" || endpoint != strings.TrimSpace(endpoint) {
		return "", "", serviceDenied("datafactory_stop_operation_query_changed")
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if len(parts) != 14 || strings.Join(parts[:11], "/") != id || !slices.Contains([]string{"stop", "integrationruntimesstop"}, parts[11]) || !slices.Contains([]string{"operationstatuses", "operationresults"}, parts[12]) {
		return "", "", serviceDenied("datafactory_stop_operation_scope_changed")
	}
	guid, err := hex.DecodeString(parts[13])
	if err != nil || len(guid) != 16 || len(parts[13]) != 32 {
		return "", "", serviceDenied("invalid_datafactory_stop_operation_id")
	}
	mode := "result"
	if parts[12] == "operationstatuses" {
		mode = "status"
	}
	return mode, strings.Join([]string{parts[11], parts[13]}, "/"), nil
}

func (c *client) dataFactoryStopReceipt(id string, res response) (map[string]any, error) {
	if operationError(res) != nil || len(res.data) != 0 || res.status != 200 && res.status != 202 || len(res.header.Values("Operation-Location")) != 0 || len(res.header.Values("Azure-AsyncOperation")) > 1 || len(res.header.Values("Location")) > 1 {
		return nil, serviceDenied("invalid_datafactory_stop_receipt")
	}
	operation := map[string]any{}
	if res.status == 200 {
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("unexpected_datafactory_stop_operation")
		}
		return operation, nil
	}
	var signature string
	for name, key := range map[string]string{"Azure-AsyncOperation": "status_url", "Location": "result_url"} {
		endpoint := res.header.Get(name)
		if endpoint == "" {
			if len(res.header.Values(name)) != 0 {
				return nil, serviceDenied("empty_datafactory_stop_operation")
			}
			continue
		}
		mode, identity, err := c.dataFactoryStopURL(id, endpoint)
		if err != nil {
			return nil, err
		}
		if signature != "" && signature != identity || name == "Location" && mode != "result" {
			return nil, serviceDenied("datafactory_stop_operation_headers_disagree")
		}
		signature = identity
		operation[key] = endpoint
	}
	if operation["result_url"] == nil {
		return nil, serviceDenied("datafactory_stop_final_location_missing")
	}
	return operation, nil
}

func dataFactoryStopResponse(id, endpoint, mode string, res response) (bool, error) {
	if res.status != 200 && res.status != 202 || res.data["error"] != nil || res.data["nextLink"] != nil || res.data["code"] != nil {
		if err := operationError(res); err != nil {
			return false, err
		}
		return false, serviceDenied("invalid_datafactory_stop_poll_response")
	}
	if err := operationError(res); err != nil {
		return false, err
	}
	u, _ := url.Parse(endpoint)
	for key, expected := range map[string]string{"resourceId": id, "id": u.Path} {
		if value, present := res.data[key]; present && !strings.EqualFold(text(value), expected) {
			return false, serviceDenied("datafactory_stop_poll_identity_changed")
		}
	}
	if value, present := res.data["name"]; present && !strings.EqualFold(text(value), last(u.Path)) {
		return false, serviceDenied("datafactory_stop_poll_name_changed")
	}
	if value := res.data["properties"]; value != nil && object(value) == nil {
		return false, serviceDenied("invalid_datafactory_stop_poll_properties")
	}
	state := text(res.data["status"])
	if state == "" {
		if mode != "result" || len(res.data) != 0 {
			return false, serviceDenied("datafactory_stop_poll_state_missing")
		}
		return res.status == 200, nil // Only a verified operation-result URL.
	}
	if res.data["status"] != state || !slices.Contains([]string{"InProgress", "Running", "Accepted", "Succeeded"}, state) || state == "Succeeded" && res.status != 200 {
		return false, serviceDenied("unknown_datafactory_stop_poll_state")
	}
	return state == "Succeeded", nil
}

// A successful status response is followed by the native final Location
// result. The caller must still observe the runtime's own Stopped state.
func (c *client) dataFactoryPollStop(ctx context.Context, id string, operation map[string]any) (contracts.WaitResult, error) {
	current := maps.Clone(operation)
	if len(current) == 0 {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	header := http.Header{}
	for key, value := range current {
		switch key {
		case "status_url", "result_url":
			endpoint, ok := value.(string)
			if !ok || endpoint == "" {
				return contracts.WaitResult{}, serviceDenied("invalid_datafactory_stop_saved_url")
			}
			name := "Location"
			if key == "status_url" {
				name = "Azure-AsyncOperation"
			}
			header.Set(name, endpoint)
		case "status_done":
			if value != true || current["status_url"] == nil {
				return contracts.WaitResult{}, serviceDenied("invalid_datafactory_stop_saved_phase")
			}
		default:
			return contracts.WaitResult{}, serviceDenied("invalid_datafactory_stop_saved_receipt")
		}
	}
	if _, err := c.dataFactoryStopReceipt(id, response{status: 202, header: header}); err != nil {
		return contracts.WaitResult{}, err
	}
	result := contracts.WaitResult{Data: current}
	for _, key := range []string{"status_url", "result_url"} {
		endpoint := text(current[key])
		if endpoint == "" || key == "status_url" && current["status_done"] == true {
			continue
		}
		mode, _, err := c.dataFactoryStopURL(id, endpoint)
		if err != nil {
			return result, err
		}
		validate := func(candidate string) error {
			if candidate != endpoint {
				return serviceDenied("datafactory_stop_poll_url_changed")
			}
			_, _, err := c.dataFactoryStopURL(id, candidate)
			return err
		}
		res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, mode == "result")
		if err != nil {
			return result, err
		}
		result.RetryAfter = retryAfter(res.header)
		if operationLocation(res.header) != "" {
			// A rotated URL must identify this same operation and final result.
			receipt := response{status: 202, data: map[string]any{}, header: res.header}
			next, err := c.dataFactoryStopReceipt(id, receipt)
			if err != nil {
				return result, err
			}
			for _, field := range []string{"status_url", "result_url"} {
				if next[field] != nil && next[field] != current[field] {
					return result, serviceDenied("datafactory_stop_poll_receipt_changed")
				}
			}
		}
		done, err := dataFactoryStopResponse(id, endpoint, mode, res)
		if err != nil || !done {
			result.State = text(res.data["status"])
			return result, err
		}
		if key == "status_url" {
			current["status_done"] = true
			if current["result_url"] == endpoint {
				result.Done = true
				return result, nil
			}
		} else {
			result.Done = true
			return result, nil
		}
	}
	result.Done = current["result_url"] != nil
	return result, nil
}

func (c *client) dataFactoryUnsubscribeReceipt(id string, res response) (string, error) {
	if res.status != 200 && res.status != 202 || res.data["error"] != nil || len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 || len(res.header.Values("Location")) > 1 {
		return "", serviceDenied("invalid_datafactory_unsubscribe_receipt")
	}
	if res.status == 200 {
		if operationLocation(res.header) != "" || !strings.EqualFold(text(res.data["triggerName"]), last(id)) || !slices.Contains([]string{"Enabled", "Provisioning", "Deprovisioning", "Disabled", "Unknown"}, text(res.data["status"])) {
			return "", serviceDenied("invalid_datafactory_unsubscribe_status")
		}
		return "", nil
	}
	request, err := c.dataFactoryOperation(id, dataFactoryTriggerType, "Triggers_GetEventSubscriptionStatus", nil)
	if err != nil {
		return "", err
	}
	endpoint := res.header.Get("Location")
	if len(res.data) != 0 || c.validateURL(endpoint) != nil {
		return "", serviceDenied("invalid_datafactory_unsubscribe_location")
	}
	u, _ := url.Parse(endpoint)
	expected, _ := url.Parse(request.URL)
	if !strings.EqualFold(u.Path, expected.Path) || u.RawQuery != expected.RawQuery || u.RawPath != "" {
		return "", serviceDenied("datafactory_unsubscribe_operation_changed")
	}
	return endpoint, nil
}

func dataFactorySynchronousReceipt(res response, deleting bool) error {
	valid := res.status == http.StatusOK || deleting && res.status == http.StatusNoContent
	if !valid || len(res.data) != 0 || operationLocation(res.header) != "" {
		return serviceDenied("invalid_datafactory_synchronous_receipt")
	}
	return nil
}
