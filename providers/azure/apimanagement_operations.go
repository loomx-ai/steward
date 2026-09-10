package azure

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var apimOperationName = regexp.MustCompile(`^[A-Za-z0-9_+-]+={0,2}$`)
var apimTenantOperationName = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

func validateAPIMOperationURL(subscription, id, location, version, endpoint string) error {
	u, err := url.Parse(endpoint)
	c := &client{subscription: strings.ToLower(subscription)}
	if err != nil || len(endpoint) > 32*1024 || c.validateURL(endpoint) != nil || strings.Contains(u.Path, "%") {
		return serviceDenied("invalid_apim_operation_url")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != version {
		return serviceDenied("invalid_apim_operation_version")
	}
	parts := strings.Split(u.Path, "/")
	root := apimRootID(id)
	if root == "" {
		return serviceDenied("invalid_apim_operation_owner")
	}
	valid := false
	signed := false
	if len(parts) == 11 && strings.EqualFold(parts[9], "operationResults") && strings.EqualFold(apimRootID(strings.Join(parts[:9], "/")), root) && apimOperationName.MatchString(parts[10]) {
		valid = true
		signed = true
	} else if len(parts) == 12 && strings.EqualFold(parts[9], "tenant") && strings.EqualFold(parts[10], "operationResults") && strings.EqualFold(strings.Join(parts[:9], "/"), root) && apimTenantOperationName.MatchString(parts[11]) {
		valid = true
	} else if len(parts) == 9 && strings.EqualFold(parts[3], "providers") && strings.EqualFold(parts[4], "Microsoft.ApiManagement") && strings.EqualFold(parts[5], "locations") && (strings.EqualFold(parts[7], "operationStatuses") || strings.EqualFold(parts[7], "operationResults")) && apimOperationName.MatchString(parts[8]) && location != "" && strings.EqualFold(parts[6], strings.ReplaceAll(location, " ", "")) {
		valid = true
		signed = true
	} else if strings.EqualFold(u.Path, id) && apimTenantOperationName.MatchString(query.Get("asyncId")) && query.Get("asyncCode") == "204" && len(query) == 3 && len(query["asyncId"]) == 1 && len(query["asyncCode"]) == 1 {
		return nil
	} else if _, kind, err := parseID(id); err == nil && strings.EqualFold(kind, apimGatewayConnectionType) && strings.EqualFold(u.Path, id) && len(query) == 1 {
		return nil // Native gateway-connection LRO polls the connection resource.
	}
	if !valid {
		return serviceDenied("apim_operation_owner_changed")
	}
	if len(query) == 1 {
		return nil
	}
	if !signed || len(query) != 5 && len(query) != 6 {
		return serviceDenied("invalid_apim_operation_parameters")
	}
	for _, key := range []string{"t", "c", "s", "h"} {
		if len(query[key]) != 1 || query.Get(key) == "" || strings.ContainsAny(query.Get(key), "\r\n\x00") {
			return serviceDenied("invalid_apim_operation_signature")
		}
	}
	if len(query) == 6 && (len(query["asyncResponse"]) != 1 || query.Get("asyncResponse") != "") {
		return serviceDenied("invalid_apim_async_response_flag")
	}
	return nil
}
func (a *action) apimDeleteHeaders(ctx context.Context, planned asset.Asset, headers map[string]string) error {
	live, err := a.client.apimResource(ctx, a.id)
	if err != nil {
		return err
	}
	if err := apimReady(a.kind.NativeType, live); err != nil {
		return err
	}
	if err := apimIncarnation(planned, live); err != nil {
		return err
	}
	if err := a.client.servicePrivateIncarnation(planned, live); err != nil {
		return err
	}
	if strings.EqualFold(a.kind.NativeType, apimGatewayConnectionType) {
		if err := a.client.apimVerifyTarget(ctx, planned, live, nil); err != nil {
			return err
		}
	}
	if _, required := headers["If-Match"]; required {
		etag := apimConditionalETag(a.kind.NativeType, live)
		if etag == "" || etag == "*" || etag != text(planned.Normalized["arm_etag"]) {
			return serviceDenied("apim_conditional_delete_requires_etag")
		}
		headers["If-Match"] = etag
	}
	return nil
}
func (a *action) apimResponseHeaders(res response) (string, string, error) {
	if len(res.header.Values("Operation-Location")) != 0 {
		return "", "", serviceDenied("unsupported_apim_polling_header")
	}
	operation, mode := "", "status"
	for _, key := range []string{"Azure-AsyncOperation", "Location"} {
		values := res.header.Values(key)
		if len(values) > 1 {
			return "", "", serviceDenied("ambiguous_apim_polling_header")
		}
		if len(values) == 0 {
			continue
		}
		if err := a.validateOperationURL(values[0]); err != nil {
			return "", "", err
		}
		if operation == "" {
			operation = values[0]
			if key == "Location" {
				mode = "location"
			}
		}
	}
	if async, location := res.header.Get("Azure-AsyncOperation"), res.header.Get("Location"); async != "" && location != "" {
		au, _ := url.Parse(async)
		lu, _ := url.Parse(location)
		if strings.EqualFold(lu.Path, a.id) && !strings.EqualFold(a.kind.NativeType, apimGatewayConnectionType) {
			if !strings.EqualFold(last(au.Path), lu.Query().Get("asyncId")) {
				return "", "", serviceDenied("apim_polling_headers_disagree")
			}
		} else if !strings.EqualFold(au.Path, lu.Path) {
			return "", "", serviceDenied("apim_polling_headers_disagree")
		}
	}
	return operation, mode, nil
}
func (a *action) apimOperationResult(res response) (contracts.ActionResult, error) {
	// The CLI records API DELETE with an empty 200 in addition to the
	// 202/204 Swagger variants. All cases still require native absence readback.
	if !slices.Contains([]int{200, 202, 204}, res.status) {
		return contracts.ActionResult{}, serviceDenied("incomplete_apim_delete_response")
	}
	if len(res.data) != 0 {
		if _, present := res.data["id"]; !present || !validResourceResponse(response{status: 200, data: res.data}, a.id, a.kind.NativeType) {
			return contracts.ActionResult{}, serviceDenied("apim_delete_response_identity_changed")
		}
	}
	operation, mode, err := a.apimResponseHeaders(res)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := map[string]any{"polling": mode}
	if operation != "" {
		data["apim_operation_binding"] = a.operationBinding(operation)
	}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: data}, nil
}
func (a *action) apimPoll(ctx context.Context, result contracts.ActionResult) (contracts.WaitResult, error) {
	endpoint := result.ProviderOperationID
	if next := text(result.Data["apim_poll_operation"]); next != "" {
		if text(result.Data["apim_poll_binding"]) != a.operationBinding(next) {
			return contracts.WaitResult{}, serviceDenied("apim_polling_receipt_changed")
		}
		endpoint = next
	}
	if result.ProviderOperationID == "" {
		if endpoint != "" || result.Data["apim_operation_binding"] != nil {
			return contracts.WaitResult{}, serviceDenied("invalid_apim_polling_receipt")
		}
		return contracts.WaitResult{Done: true}, nil
	}
	if err := a.validateOperationURL(result.ProviderOperationID); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.validateOperationURL(endpoint); err != nil {
		return contracts.WaitResult{}, err
	}
	if text(result.Data["apim_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
		return contracts.WaitResult{}, serviceDenied("apim_polling_receipt_changed")
	}
	mode := text(result.Data["polling"])
	if mode != "status" && mode != "location" {
		return contracts.WaitResult{}, serviceDenied("invalid_apim_polling_mode")
	}
	res, err := a.client.requestUsing(ctx, "GET", endpoint, nil, nil, a.validateOperationURL, a.client.http, true)
	if isNotFound(err) {
		return contracts.WaitResult{Done: true}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) {
		return contracts.WaitResult{}, serviceDenied("incomplete_apim_polling_response")
	}
	if err := a.operationError(res); err != nil {
		return contracts.WaitResult{}, err
	}
	u, _ := url.Parse(endpoint)
	for field, expected := range map[string]string{"resourceId": a.id, "id": u.Path, "name": last(u.Path)} {
		value, present := res.data[field]
		if (field == "resourceId" || field == "id") && strings.EqualFold(a.kind.NativeType, apimGatewayType) {
			value = responseID(a.kind.NativeType, text(value))
		}
		if present && !strings.EqualFold(text(value), expected) {
			// Some Location polls return the owning service resource itself.
			if field == "id" && strings.EqualFold(text(value), a.id) || field == "name" && strings.EqualFold(responseID(a.kind.NativeType, text(res.data["id"])), a.id) && strings.EqualFold(text(value), last(a.id)) {
				continue
			}
			return contracts.WaitResult{}, serviceDenied("apim_polling_response_identity_changed")
		}
	}
	next, nextMode, err := a.apimResponseHeaders(res)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	var data map[string]any
	if next != "" {
		nu, _ := url.Parse(next)
		if !strings.EqualFold(u.Path, nu.Path) || mode != nextMode {
			return contracts.WaitResult{}, serviceDenied("apim_polling_operation_changed")
		}
		data = maps.Clone(result.Data)
		data["apim_poll_operation"] = next
		data["apim_poll_binding"] = a.operationBinding(next)
	}
	state := text(res.data["status"])
	if state == "" {
		state = text(object(res.data["properties"])["targetProvisioningState"])
		if state == "" {
			state = text(object(res.data["properties"])["provisioningState"])
		}
	}
	if state != "" && !slices.Contains([]string{"Succeeded", "Accepted", "InProgress", "Running", "Creating", "Updating", "Deleting", "Failed", "Canceled", "Cancelled"}, state) {
		return contracts.WaitResult{}, serviceDenied("unknown_apim_operation_status")
	}
	done := state == "Succeeded" || state == "" && (res.status == 204 || mode == "location" && res.status == 200)
	if mode == "status" && state == "" && res.status == 200 {
		return contracts.WaitResult{}, serviceDenied("missing_apim_operation_status")
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: data}, nil
}
