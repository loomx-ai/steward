package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Resource-group Location callbacks use an opaque operation identifier, not a
// UUID. Signing parameters may rotate, but the operation path must not change.
func resourceGroupOperationURL(subscription, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.ForceQuery {
		return "", serviceDenied("invalid_resource_group_operation_url")
	}
	p := strings.Split(u.Path, "/")
	if len(p) != 5 || !strings.EqualFold(p[1], "subscriptions") || !strings.EqualFold(p[2], subscription) || !uuidPattern.MatchString(p[2]) || !strings.EqualFold(p[3], "operationresults") || p[4] == "" || strings.Trim(p[4], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-=") != "" {
		return "", serviceDenied("resource_group_operation_scope_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["api-version"]) != 1 || q.Get("api-version") != resourcesVersion {
		return "", serviceDenied("resource_group_operation_version_changed")
	}
	signatures := 0
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return "", serviceDenied("ambiguous_resource_group_operation_query")
		}
		switch key {
		case "api-version":
		case "t", "c", "s", "h":
			signatures++
		default:
			return "", serviceDenied("unknown_resource_group_operation_query")
		}
	}
	if signatures != 0 && signatures != 4 {
		return "", serviceDenied("incomplete_resource_group_operation_signature")
	}
	return p[4], nil
}

func resourceGroupOperationLocation(subscription string, header http.Header) (string, error) {
	for _, key := range []string{"Azure-AsyncOperation", "Operation-Location"} {
		if len(header.Values(key)) != 0 {
			return "", serviceDenied("unknown_resource_group_operation_header")
		}
	}
	values := header.Values("Location")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || values[0] == "" {
		return "", serviceDenied("ambiguous_resource_group_operation_header")
	}
	if _, err := resourceGroupOperationURL(subscription, values[0]); err != nil {
		return "", err
	}
	return values[0], nil
}

func (c *client) resourceGroupOperationBinding(req contracts.ActionRequest, receipt map[string]any) (string, error) {
	p := strings.Split(req.Asset.Identity.NativeID, "/")
	if req.Action != "delete" || req.IdempotencyKey == "" || req.Asset.Identity.NativeType != groupType || len(req.Parameters) != 0 || len(p) != 5 || !strings.EqualFold(p[1], "subscriptions") || !strings.EqualFold(p[2], c.subscription) || !uuidPattern.MatchString(p[2]) || !strings.EqualFold(p[3], "resourcegroups") || p[4] == "" || strings.ContainsAny(p[4], "%?#\\\x00\r\n\t") || p[4] == "." || p[4] == ".." {
		return "", serviceDenied("invalid_resource_group_operation_owner")
	}
	req.ExecutionResult = nil
	wire, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	data := maps.Clone(receipt)
	delete(data, "binding")
	return c.privateConfiguration(map[string]any{"protocol": "resource-group-operation-1", "request": string(wire), "receipt": data}), nil
}

// This receipt proves acceptance of a native operation only. Neither a terminal
// poll nor an absent group proves that its reviewed products have been removed.
func (c *client) resourceGroupOperationReceipt(req contracts.ActionRequest, res response) (map[string]any, error) {
	if res.status != 200 && res.status != 202 && res.status != 204 || len(res.data) != 0 {
		return nil, serviceDenied("invalid_resource_group_delete_response")
	}
	endpoint, err := resourceGroupOperationLocation(c.subscription, res.header)
	if err != nil {
		return nil, err
	}
	if (res.status == 202) != (endpoint != "") {
		return nil, serviceDenied("incomplete_resource_group_delete_receipt")
	}
	receipt := map[string]any{"url": endpoint, "done": res.status != 202}
	binding, err := c.resourceGroupOperationBinding(req, receipt)
	if err != nil {
		return nil, err
	}
	receipt["binding"] = binding
	return receipt, nil
}

func (c *client) resourceGroupPollOperation(ctx context.Context, req contracts.ActionRequest, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	binding, err := c.resourceGroupOperationBinding(req, receipt)
	if err != nil {
		return out, err
	}
	endpoint, endpointOK := receipt["url"].(string)
	done, doneOK := receipt["done"].(bool)
	if len(receipt) != 3 || receipt["binding"] != binding || !endpointOK || !doneOK || !done && endpoint == "" {
		return out, serviceDenied("resource_group_operation_receipt_changed")
	}
	operation := ""
	if endpoint != "" {
		operation, err = resourceGroupOperationURL(c.subscription, endpoint)
		if err != nil {
			return out, err
		}
	}
	current := maps.Clone(receipt)
	if done {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("resource_group_operation_redirected")
		}
		_, err := resourceGroupOperationURL(c.subscription, next)
		return err
	}
	res, err := c.requestUsing(ctx, http.MethodGet, endpoint, nil, nil, validate, c.http, true)
	if err != nil {
		return out, err
	}
	if res.status != 200 && res.status != 202 && res.status != 204 || len(res.data) != 0 {
		return out, serviceDenied("invalid_resource_group_operation_result")
	}
	next, err := resourceGroupOperationLocation(c.subscription, res.header)
	if err != nil {
		return out, err
	}
	if next != "" {
		nextOperation, err := resourceGroupOperationURL(c.subscription, next)
		if err != nil {
			return out, err
		}
		if operation != nextOperation {
			return out, serviceDenied("resource_group_operation_changed")
		}
		current["url"] = next
	}
	current["done"] = res.status != 202
	current["binding"], err = c.resourceGroupOperationBinding(req, current)
	if err != nil {
		return out, err
	}
	return contracts.WaitResult{Done: res.status != 202, RetryAfter: retryAfter(res.header), Data: current}, nil
}
