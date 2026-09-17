package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) recoveryItemPollHeaders(id string, headers http.Header) (map[string]any, error) {
	if len(headers.Values("Operation-Location")) != 0 {
		return nil, serviceDenied("unsupported_recovery_item_callback")
	}
	result := map[string]any{}
	token := ""
	for _, entry := range []struct{ header, role string }{{"Azure-AsyncOperation", "status"}, {"Location", "result"}} {
		values := headers.Values(entry.header)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] == "" {
			return nil, serviceDenied("ambiguous_recovery_item_callback")
		}
		current, legacy, err := c.recoveryBackupCallback(id, recoveryServicesItem, values[0], entry.role)
		if err != nil {
			return nil, err
		}
		if token != "" && current != token {
			return nil, serviceDenied("recovery_item_callbacks_disagree")
		}
		token = current
		endpoint := values[0]
		if legacy {
			u, _ := url.Parse(endpoint)
			endpoint = apiURL(u.Path, recoveryServicesBackupVersion)
		}
		result[entry.role+"_url"] = endpoint
	}
	return result, nil
}

func (c *client) recoveryItemReceipt(id string, data map[string]any) map[string]any {
	result := maps.Clone(data)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "recovery-item-operation-1", "owner": id, "data": result})
	return result
}

func (c *client) recoveryItemDeleteReceipt(id string, res response, empty bool) (map[string]any, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesItem)
	if err != nil || owner != id || !empty || len(res.data) != 0 || !slices.Contains([]int{200, 202, 204}, res.status) {
		return nil, serviceDenied("invalid_recovery_item_acknowledgement")
	}
	receipt, err := c.recoveryItemPollHeaders(id, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status != 202 && len(receipt) != 0 {
		return nil, serviceDenied("incomplete_recovery_item_acknowledgement")
	}
	receipt["operation_done"], receipt["status_done"] = res.status != 202, res.status != 202
	return c.recoveryItemReceipt(id, receipt), nil
}

func (c *client) recoveryItemVerifyReceipt(id string, receipt map[string]any) ([]string, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesItem)
	if err != nil || owner != id || receipt["binding"] != c.recoveryItemReceipt(id, receipt)["binding"] {
		return nil, serviceDenied("recovery_item_receipt_changed")
	}
	for key := range receipt {
		if !slices.Contains([]string{"binding", "operation_done", "status_done", "status_url", "result_url", "jobs"}, key) {
			return nil, serviceDenied("unknown_recovery_item_receipt_field")
		}
	}
	done, ok := receipt["operation_done"].(bool)
	if !ok {
		return nil, serviceDenied("invalid_recovery_item_receipt_phase")
	}
	statusDone, ok := receipt["status_done"].(bool)
	if !ok || done && !statusDone {
		return nil, serviceDenied("invalid_recovery_item_receipt_phase")
	}
	token := ""
	for _, role := range []string{"status", "result"} {
		raw, exists := receipt[role+"_url"]
		if !exists {
			continue
		}
		endpoint, ok := raw.(string)
		if !ok {
			return nil, serviceDenied("invalid_recovery_item_receipt_url")
		}
		current, legacy, err := c.recoveryBackupCallback(id, recoveryServicesItem, endpoint, role)
		if err != nil || legacy || token != "" && current != token {
			return nil, serviceDenied("invalid_recovery_item_saved_callback")
		}
		token = current
	}
	if !statusDone && token == "" {
		return nil, serviceDenied("missing_recovery_item_saved_callback")
	}
	var jobs []string
	if raw, exists := receipt["jobs"]; exists {
		if !statusDone || receipt["status_url"] == nil {
			return nil, serviceDenied("recovery_item_jobs_precede_status")
		}
		jobs, err = recoveryItemOperationJobs(map[string]any{"properties": map[string]any{"objectType": "OperationStatusJobsExtendedInfo", "jobIds": raw}})
		if err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func (c *client) recoveryItemPoll(ctx context.Context, id string, receipt map[string]any) (map[string]any, time.Duration, error) {
	jobs, err := c.recoveryItemVerifyReceipt(id, receipt)
	if err != nil {
		return nil, 0, err
	}
	next := maps.Clone(receipt)
	if receipt["operation_done"] == true {
		return next, 0, nil
	}
	if receipt["status_done"] == true {
		done, err := c.recoveryItemJobs(ctx, id, jobs)
		if err != nil {
			return nil, 0, err
		}
		next["operation_done"] = done
		return c.recoveryItemReceipt(id, next), 2 * time.Second, nil
	}
	role := "status"
	endpoint := text(receipt["status_url"])
	if endpoint == "" {
		role = "result"
		endpoint = text(receipt["result_url"])
	}
	token, _, _ := c.recoveryBackupCallback(id, recoveryServicesItem, endpoint, role)
	transport := *c.http
	base := transport.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	ack := &synapseRestoreAckTransport{base: base}
	transport.Transport = ack
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, c.validateURL, &transport, role == "result")
	if err != nil {
		return nil, 0, contracts.DependencyReadError(err)
	}
	if err = operationError(res); err != nil {
		return nil, 0, err
	}
	headers, err := c.recoveryItemPollHeaders(id, res.header)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		successorRole := strings.TrimSuffix(key, "_url")
		successor, _, err := c.recoveryBackupCallback(id, recoveryServicesItem, text(value), successorRole)
		if err != nil || successor != token {
			return nil, 0, serviceDenied("recovery_item_poll_redirected")
		}
		// Do not replace the saved capability, including its signing fields.
		if old := text(receipt[key]); old != "" && old != value {
			return nil, 0, serviceDenied("recovery_item_poll_capability_changed")
		}
	}
	if role == "status" {
		u, _ := url.Parse(endpoint)
		rawID := text(res.data["id"])
		if res.status != 200 || text(res.data["name"]) != token || rawID != token && (!strings.EqualFold(rawID, u.Path) || last(rawID) != token) {
			return nil, 0, serviceDenied("recovery_item_status_identity_changed")
		}
		switch res.data["status"] {
		case "InProgress":
			return next, retryAfter(res.header), nil
		case "Succeeded":
			jobs, err = recoveryItemOperationJobs(res.data)
			if err != nil {
				return nil, 0, err
			}
			if len(jobs) != 0 {
				values := make([]any, len(jobs))
				for i, job := range jobs {
					values[i] = job
				}
				next["jobs"] = values
			}
		case "Failed", "Canceled":
			return nil, 0, serviceDenied("recovery_item_operation_failed")
		default:
			return nil, 0, serviceDenied("unknown_recovery_item_operation_status")
		}
	} else {
		switch res.status {
		case 202:
			if len(res.data) != 0 {
				return nil, 0, serviceDenied("invalid_recovery_item_pending_result")
			}
			return next, retryAfter(res.header), nil
		case 200:
			if len(res.data) == 0 && !ack.empty {
				return nil, 0, serviceDenied("invalid_recovery_item_empty_result")
			}
			if len(res.data) != 0 {
				if err = c.recoveryServicesMetadata(res.data, id, recoveryServicesItem); err != nil {
					return nil, 0, err
				}
			}
		case 204:
			if len(res.data) != 0 || !ack.empty {
				return nil, 0, serviceDenied("invalid_recovery_item_completed_result")
			}
		default:
			return nil, 0, serviceDenied("invalid_recovery_item_result_status")
		}
	}
	next["status_done"], next["operation_done"] = true, len(jobs) == 0
	return c.recoveryItemReceipt(id, next), retryAfter(res.header), nil
}
