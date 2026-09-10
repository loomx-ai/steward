package azure

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var batchPoolOperationSuffix = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)

func (a *batchAction) validateOperationURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 64<<10 || u.Scheme != "https" || u.Host != "management.azure.com" || u.RawPath != "" || u.User != nil || u.Fragment != "" {
		return serviceDenied("invalid_batch_operation_endpoint")
	}
	path := strings.ToLower(u.Path)
	name := last(path)
	switch a.kind.NativeType {
	case batchAccountType:
		prefix := a.client.root() + "/providers/microsoft.batch/locations/" + a.location + "/accountoperationresults/"
		if path != prefix+name || !strings.HasPrefix(name, last(a.accountID)+"-") || !uuidPattern.MatchString(strings.TrimPrefix(name, last(a.accountID)+"-")) {
			return serviceDenied("batch_account_operation_owner_changed")
		}
	case batchPoolType:
		prefix := a.accountID + "/pooloperationresults/"
		if path != prefix+name || !strings.HasPrefix(name, "delete-"+last(a.id)+"-") || !batchPoolOperationSuffix.MatchString(strings.TrimPrefix(name, "delete-"+last(a.id)+"-")) {
			return serviceDenied("batch_pool_operation_owner_changed")
		}
	case batchPECType:
		prefix := a.id + "/accountoperationresults/"
		if path != prefix+name || !strings.HasPrefix(name, last(a.accountID)+"-") || !uuidPattern.MatchString(strings.TrimPrefix(name, last(a.accountID)+"-")) {
			return serviceDenied("batch_endpoint_operation_owner_changed")
		}
	default:
		return serviceDenied("batch_operation_not_declared")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != batchVersion || (len(query) != 1 && len(query) != 5) {
		return serviceDenied("invalid_batch_operation_query")
	}
	if len(query) == 5 {
		for _, name := range []string{"t", "c", "s", "h"} {
			if len(query[name]) != 1 || query.Get(name) == "" || strings.ContainsAny(query.Get(name), "\r\n\x00") {
				return serviceDenied("invalid_batch_operation_signature")
			}
		}
	}
	return nil
}

func (a *batchAction) operationBinding(operation string, request contracts.ActionRequest) string {
	return a.client.privateConfiguration(map[string]any{"resource": request.Asset, "impacts": request.LifecycleImpacts, "prerequisites": request.PrerequisiteDeletions, "operation": operation, "account": a.accountID, "endpoint": a.endpoint})
}

func (a *batchAction) operationResult(request contracts.ActionRequest, res response) (contracts.ActionResult, error) {
	operation, polling := "", ""
	for _, name := range []string{"Azure-AsyncOperation", "Operation-Location", "Location"} {
		if len(res.header.Values(name)) > 1 {
			return contracts.ActionResult{}, serviceDenied("ambiguous_batch_operation_header")
		}
		if value := res.header.Get(name); value != "" {
			if operation != "" {
				return contracts.ActionResult{}, serviceDenied("ambiguous_batch_operation_header")
			}
			operation, polling = value, "status"
			if name == "Location" {
				polling = "location"
			}
		}
	}
	if operation != "" {
		if err := a.validateOperationURL(operation); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	if res.status == 202 && !isBatchDataType(a.kind.NativeType) && operation == "" {
		return contracts.ActionResult{}, serviceDenied("batch_async_delete_missing_operation")
	}
	data := map[string]any{"polling": polling, "batch_operation_binding": a.operationBinding(operation, request)}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, Data: data, RetryAfter: retryAfter(res.header)}, nil
}

func (a *batchAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	data := maps.Clone(result.Data)
	operation := result.ProviderOperationID
	if text(data["batch_operation_binding"]) != a.operationBinding(operation, request) {
		return contracts.WaitResult{}, serviceDenied("batch_operation_receipt_changed")
	}
	if len(batchMPITasks(request)) > 0 {
		request.ExecutionResult = &result
		if _, err := a.taskReceipt(request); err != nil {
			return contracts.WaitResult{}, err
		}
		if data["batch_task_phase"] == "terminating" {
			next, err := a.Execute(ctx, request)
			return contracts.WaitResult{State: "batch_tasks_terminating", RetryAfter: 2 * time.Second, Data: next.Data}, err
		}
	}
	if operation != "" {
		if err := a.validateOperationURL(operation); err != nil {
			return contracts.WaitResult{}, err
		}
		polling := text(data["polling"])
		if polling != "location" && polling != "status" {
			return contracts.WaitResult{}, serviceDenied("invalid_batch_polling_protocol")
		}
		initial, _ := url.Parse(operation)
		if next, exists := data["batch_poll_operation"]; exists {
			if err := a.validateOperationURL(text(next)); err != nil {
				return contracts.WaitResult{}, err
			}
			u, _ := url.Parse(text(next))
			if polling != "location" || !strings.EqualFold(u.Path, initial.Path) || text(data["batch_poll_binding"]) != a.operationBinding(text(next), request) {
				return contracts.WaitResult{}, serviceDenied("batch_resumed_poll_changed")
			}
			operation = text(next)
		}
		// Only this bound native Location endpoint may finish with an empty 200.
		// Ordinary resource GETs and JSON null continue to fail validation.
		res, err := a.client.requestUsing(ctx, "GET", operation, nil, nil, a.validateOperationURL, a.client.http, polling == "location")
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if !slices.Contains([]int{200, 202, 204}, res.status) || res.data["code"] != nil {
				return contracts.WaitResult{}, serviceDenied("invalid_batch_poll_response")
			}
			if err := operationError(res); err != nil {
				return contracts.WaitResult{}, err
			}
			if len(res.header.Values("Location")) > 0 {
				next := res.header.Get("Location")
				if polling != "location" || len(res.header.Values("Location")) != 1 || res.header.Get("Azure-AsyncOperation") != "" || res.header.Get("Operation-Location") != "" {
					return contracts.WaitResult{}, serviceDenied("batch_polling_protocol_changed")
				}
				if err := a.validateOperationURL(next); err != nil {
					return contracts.WaitResult{}, err
				}
				u, _ := url.Parse(next)
				if !strings.EqualFold(u.Path, initial.Path) {
					return contracts.WaitResult{}, serviceDenied("batch_polling_operation_changed")
				}
				data["batch_poll_operation"], data["batch_poll_binding"] = next, a.operationBinding(next, request)
			}
			state := strings.ToLower(text(res.data["status"]))
			if state == "" {
				state = strings.ToLower(text(object(res.data["properties"])["provisioningState"]))
			}
			if state != "" && !slices.Contains([]string{"succeeded", "inprogress", "running", "accepted", "deleting"}, state) {
				return contracts.WaitResult{}, serviceDenied("unknown_batch_operation_state")
			}
			if state == "" && len(res.data) != 0 {
				return contracts.WaitResult{}, serviceDenied("invalid_batch_poll_body")
			}
			done := state == "succeeded"
			if polling == "location" && len(res.data) == 0 {
				done = res.status == 200 || res.status == 204
			}
			if !done {
				return contracts.WaitResult{Data: data, State: state, RetryAfter: retryAfter(res.header)}, nil
			}
		}
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second, Data: data}, err
}
