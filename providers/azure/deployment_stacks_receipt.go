package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func deploymentStackReceiptParameters(value map[string]any) (url.Values, error) {
	q := url.Values{}
	for key, raw := range value {
		v, ok := raw.(string)
		if !ok {
			return nil, serviceDenied("invalid_deployment_stack_saved_parameters")
		}
		valid := false
		switch key {
		case "api-version":
			valid = v == deploymentStackVersion
		case "unmanageAction.Resources", "unmanageAction.ResourceGroups", "unmanageAction.ManagementGroups":
			valid = v == "delete" || v == "detach"
		case "unmanageAction.ResourcesWithoutDeleteSupport":
			valid = v == "fail" || v == "detach"
		case "bypassStackOutOfSyncError":
			valid = v == "false"
		}
		if !valid {
			return nil, serviceDenied("invalid_deployment_stack_saved_parameters")
		}
		q.Set(key, v)
	}
	for _, key := range []string{"unmanageAction.Resources", "unmanageAction.ResourceGroups", "unmanageAction.ManagementGroups", "bypassStackOutOfSyncError"} {
		if !q.Has(key) {
			return nil, serviceDenied("missing_deployment_stack_saved_parameters")
		}
	}
	return q, nil
}
func (c *client) deploymentStackReceiptOwner(id, region string) error {
	scope, params, err := deploymentStackParameters(id)
	if err != nil || scope == "ManagementGroup" || !strings.EqualFold(text(params["subscriptionId"]), c.subscription) || region == "" || strings.Trim(region, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return serviceDenied("invalid_deployment_stack_receipt_owner")
	}
	return nil
}
func (c *client) deploymentStackSignReceipt(id, region string, receipt map[string]any) map[string]any {
	out := maps.Clone(receipt)
	if out == nil {
		out = map[string]any{}
	}
	delete(out, "binding")
	out["binding"] = c.privateConfiguration(map[string]any{"protocol": "deployment-stack-delete-1", "owner": strings.ToLower(id), "region": region, "receipt": out})
	return out
}
func deploymentStackReceiptHeaders(id, region string, parameters url.Values, header http.Header) (map[string]any, error) {
	if len(header.Values("Operation-Location")) != 0 {
		return nil, serviceDenied("unknown_deployment_stack_receipt_header")
	}
	out := map[string]any{}
	operation := ""
	for _, item := range []struct{ header, role string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		values := header.Values(item.header)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] == "" {
			return nil, serviceDenied("ambiguous_deployment_stack_receipt_header")
		}
		current, err := deploymentStackPollURL(id, region, values[0], item.role, parameters)
		if err != nil {
			return nil, err
		}
		if operation != "" && operation != current {
			return nil, serviceDenied("deployment_stack_receipt_operations_disagree")
		}
		operation = current
		out[item.role] = values[0]
	}
	return out, nil
}
func (c *client) deploymentStackDeleteReceipt(id, region string, parameters map[string]any, res response) (map[string]any, error) {
	if err := c.deploymentStackReceiptOwner(id, region); err != nil {
		return nil, err
	}
	q, err := deploymentStackReceiptParameters(parameters)
	if err != nil {
		return nil, err
	}
	if res.status != 200 && res.status != 202 && res.status != 204 || len(res.data) != 0 {
		return nil, serviceDenied("invalid_deployment_stack_delete_response")
	}
	receipt, err := deploymentStackReceiptHeaders(id, region, q, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status != 202 && len(receipt) != 0 {
		return nil, serviceDenied("incomplete_deployment_stack_delete_receipt")
	}
	receipt["parameters"] = maps.Clone(parameters)
	return c.deploymentStackSignReceipt(id, region, receipt), nil
}
func (c *client) deploymentStackVerifyReceipt(id, region string, receipt map[string]any) (url.Values, error) {
	if err := c.deploymentStackReceiptOwner(id, region); err != nil {
		return nil, err
	}
	if receipt["binding"] != c.deploymentStackSignReceipt(id, region, receipt)["binding"] {
		return nil, serviceDenied("deployment_stack_receipt_changed")
	}
	q, err := deploymentStackReceiptParameters(object(receipt["parameters"]))
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	for key, v := range receipt {
		switch key {
		case "binding", "parameters":
		case "complete", "status_done":
			if v != true {
				return nil, serviceDenied("invalid_deployment_stack_saved_phase")
			}
		case "status_url", "result_url":
			wire, ok := v.(string)
			if !ok || wire == "" {
				return nil, serviceDenied("invalid_deployment_stack_saved_url")
			}
			name := "Azure-AsyncOperation"
			if key == "result_url" {
				name = "Location"
			}
			h.Set(name, wire)
		default:
			return nil, serviceDenied("unknown_deployment_stack_receipt_field")
		}
	}
	if receipt["status_done"] == true && (receipt["status_url"] == nil || receipt["result_url"] == nil) {
		return nil, serviceDenied("invalid_deployment_stack_saved_phase")
	}
	_, err = deploymentStackReceiptHeaders(id, region, q, h)
	return q, err
}

// Done only confirms operation completion. The cleanup action must separately
// verify the reviewed stack and every affected member before reporting deletion.
func (c *client) deploymentStackPoll(ctx context.Context, id, region string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	q, err := c.deploymentStackVerifyReceipt(id, region, receipt)
	if err != nil {
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
			return serviceDenied("deployment_stack_poll_url_changed")
		}
		_, err := deploymentStackPollURL(id, region, next, role, q)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, role == "result_url")
	if err != nil {
		return out, err
	}
	done, state := false, ""
	if role == "status_url" {
		done, err = deploymentStackPollState(endpoint, res)
		if err != nil {
			return out, err
		}
		state = text(res.data["status"])
	} else {
		if res.status != 200 && res.status != 202 && res.status != 204 || len(res.data) != 0 {
			return out, serviceDenied("invalid_deployment_stack_poll_result")
		}
		headers, err := deploymentStackReceiptHeaders(id, region, q, res.header)
		if err != nil {
			return out, err
		}
		for key, value := range headers {
			if current[key] != value {
				return out, serviceDenied("deployment_stack_result_continuation_changed")
			}
		}
		done = res.status != 202
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.deploymentStackSignReceipt(id, region, current)}, nil
}

// Legacy ARM operation-result bodies are diagnostics only; never expose
// unexpected private payloads before the strict empty-result check rejects them.
func deploymentStackLegacyResultEndpoint(u *url.URL) bool {
	p := strings.Split(strings.ToLower(u.Path), "/")
	return len(p) == 5 && p[1] == "subscriptions" && uuidPattern.MatchString(p[2]) && p[3] == "operationresults" && uuidPattern.MatchString(p[4]) && u.Query().Get("api-version") == "2018-08-01"
}
