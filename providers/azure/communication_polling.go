package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var communicationSignedOperation = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\*[0-9a-fA-F]{64}$`)

// The pinned examples return a provider-global URL without api-version.
// ProviderHub recordings also return signed, subscription-scoped operation
// URLs. Their polling region is independent of the resource's Global location.
func communicationARMOperationURL(subscription, resourceID, version, endpoint string) (string, error) {
	id, typ, err := parseID(resourceID)
	if err != nil || communicationKind(typ) == "" || !strings.HasPrefix(id, "/subscriptions/"+strings.ToLower(subscription)+"/") {
		return "", serviceDenied("invalid_communication_operation_owner")
	}
	u, err := url.Parse(endpoint)
	if err != nil || endpoint == "" || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawPath != "" && u.RawPath != u.Path || u.ForceQuery {
		return "", serviceDenied("invalid_communication_arm_operation")
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	signed := false
	if len(parts) == 9 && parts[1] == "subscriptions" && parts[2] == strings.ToLower(subscription) {
		parts = append([]string{""}, parts[3:]...)
		signed = true
	}
	if len(parts) != 7 || parts[0] != "" || parts[1] != "providers" || parts[2] != "microsoft.communication" || parts[3] != "locations" || !cosmosOperationRegion.MatchString(parts[4]) || parts[5] != "operationstatuses" || !signed && !uuidPattern.MatchString(parts[6]) || signed && !communicationSignedOperation.MatchString(parts[6]) {
		return "", serviceDenied("communication_arm_operation_path_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", serviceDenied("invalid_communication_arm_operation_query")
	}
	if !signed && len(query) == 0 {
		query.Set("api-version", version)
		u.RawQuery = query.Encode()
		return u.String(), nil
	}
	if len(query["api-version"]) != 1 || query.Get("api-version") != version || !signed && len(query) != 1 || signed && len(query) != 5 {
		return "", serviceDenied("communication_arm_operation_version_changed")
	}
	if signed {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" || strings.ContainsAny(query.Get(key), "\x00\r\n\t ") {
				return "", serviceDenied("invalid_communication_arm_operation_signature")
			}
		}
	}
	return endpoint, nil // Preserve the native signed query byte-for-byte.
}

func communicationARMOperationHeaders(subscription, id, version string, header http.Header) (operation, polling string, err error) {
	if len(header.Values("Operation-Location")) != 0 || len(header.Values("Azure-AsyncOperation")) > 1 || len(header.Values("Location")) > 1 {
		return "", "", serviceDenied("ambiguous_communication_arm_operation_headers")
	}
	for _, name := range []string{"Azure-AsyncOperation", "Location"} {
		value := header.Get(name)
		if value == "" {
			continue
		}
		next, err := communicationARMOperationURL(subscription, id, version, value)
		if err != nil {
			return "", "", err
		}
		if operation != "" {
			before, _ := url.Parse(operation)
			after, _ := url.Parse(next)
			if !strings.EqualFold(before.Path, after.Path) {
				return "", "", serviceDenied("communication_arm_operation_headers_disagree")
			}
		} else {
			operation, polling = next, "status"
			if name == "Location" {
				polling = "location"
			}
		}
	}
	return operation, polling, nil
}

func communicationDeleteReceipt(subscription, id, kind, endpoint string, res response) (operation, polling string, err error) {
	if err := operationError(res); err != nil {
		return "", "", err
	}
	if len(res.data) != 0 {
		return "", "", serviceDenied("invalid_communication_delete_body")
	}
	if isCommunicationDataType(kind) {
		if kind == communicationPhoneType {
			if res.status != 202 || len(res.header.Values("Operation-Location")) != 1 || len(res.header.Values("operation-id")) != 1 || len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation")) != 0 {
				return "", "", serviceDenied("invalid_communication_release_receipt")
			}
			operation, err := communicationReleaseOperation(endpoint, res.header.Get("Operation-Location"), res.header.Get("operation-id"))
			return operation, "phone", err
		}
		if res.status != 204 || operationLocation(res.header) != "" {
			return "", "", serviceDenied("invalid_communication_data_delete_receipt")
		}
		return "", "", nil
	}
	if res.status != 200 && res.status != 204 && (res.status != 202 || kind != communicationType && kind != communicationEmailType && kind != communicationDomainType) {
		return "", "", serviceDenied("invalid_communication_arm_delete_status")
	}
	operation, polling, err = communicationARMOperationHeaders(subscription, id, communicationARMVersion, res.header)
	if err != nil {
		return "", "", err
	}
	if res.status == 202 && operation == "" || res.status != 202 && operation != "" {
		return "", "", serviceDenied("communication_arm_delete_receipt_changed")
	}
	return operation, polling, nil
}

func communicationARMOperationState(id, operation, polling string, res response) (bool, string, error) {
	if res.status != 200 && res.status != 202 && res.status != 204 || res.data["code"] != nil || res.data["nextLink"] != nil || monitorRuleFields(res.data, "id", "name", "status", "resourceId", "code", "error", "nextLink") != nil {
		return false, "", serviceDenied("invalid_communication_arm_poll_response")
	}
	if err := operationError(res); err != nil {
		return false, "", err
	}
	if res.data["error"] != nil {
		return false, "", serviceDenied("invalid_communication_arm_poll_error")
	}
	u, _ := url.Parse(operation)
	for key, expected := range map[string]string{"id": u.Path, "name": last(u.Path), "resourceId": id} {
		if value, present := res.data[key]; present && (value != text(value) || !strings.EqualFold(text(value), expected)) {
			return false, "", serviceDenied("communication_arm_poll_identity_changed")
		}
	}
	for _, key := range []string{"startTime", "endTime"} {
		if value, exists := res.data[key]; exists {
			if _, err := time.Parse(time.RFC3339Nano, text(value)); err != nil {
				return false, "", serviceDenied("invalid_communication_arm_poll_time")
			}
		}
	}
	state := text(res.data["status"])
	if state == "" && polling == "location" && (res.status == 200 || res.status == 204) && len(res.data) == 0 {
		return true, "", nil
	}
	if !slices.Contains([]string{"Succeeded", "Accepted", "Deleting", "Running", "InProgress"}, state) || res.status == 204 {
		return false, "", serviceDenied("unknown_communication_arm_poll_state")
	}
	return state == "Succeeded", state, nil
}

func communicationPhoneOperationState(operation string, res response) (bool, string, error) {
	u, _ := url.Parse(operation)
	if res.status != 200 || res.data["id"] != last(u.Path) || res.data["operationType"] != "releasePhoneNumber" || monitorRuleFields(res.data, "id", "operationType", "status", "createdDateTime", "error") != nil {
		return false, "", serviceDenied("communication_release_poll_identity_changed")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(res.data["createdDateTime"])); err != nil {
		return false, "", serviceDenied("communication_release_poll_time_missing")
	}
	if err := operationError(res); err != nil {
		return false, "", err
	}
	state := text(res.data["status"])
	if res.data["error"] != nil || !slices.Contains([]string{"notStarted", "running", "succeeded"}, state) {
		return false, "", serviceDenied("invalid_communication_release_poll_state")
	}
	// resourceLocation and Location describe an optional result, not another
	// operation. Release defines no result-read API; never follow those URLs.
	return state == "succeeded", state, nil
}

func (a *communicationAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	data := maps.Clone(result.Data)
	delete(data, "communication_phase_binding")
	return a.client.privateConfiguration(map[string]any{"request": request, "origin": result.ProviderOperationID, "data": data, "protocol": "communication-delete-1"})
}

func (a *communicationAction) phaseResult(request contracts.ActionRequest, operation, polling string, res response) contracts.ActionResult {
	phase := "absence"
	if operation != "" {
		phase = "poll"
	}
	result := contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"communication_phase": phase, "communication_operation": operation, "communication_polling": polling}}
	result.Data["communication_phase_binding"] = a.phaseBinding(request, result)
	return result
}

func (a *communicationAction) verifyReceipt(request contracts.ActionRequest, result contracts.ActionResult) error {
	phase, polling, operation := text(result.Data["communication_phase"]), text(result.Data["communication_polling"]), text(result.Data["communication_operation"])
	if len(result.Data) != 4 || phase != "poll" && phase != "absence" || result.Data["communication_phase_binding"] != a.phaseBinding(request, result) {
		return serviceDenied("communication_delete_receipt_changed")
	}
	if result.ProviderOperationID == "" {
		if operation != "" || polling != "" || phase != "absence" {
			return serviceDenied("invalid_communication_synchronous_receipt")
		}
		return nil
	}
	if operation == "" {
		return serviceDenied("communication_resumed_operation_missing")
	}
	for _, endpoint := range []string{operation, result.ProviderOperationID} {
		u, err := url.Parse(endpoint)
		if err != nil {
			return serviceDenied("invalid_communication_resumed_operation")
		}
		if a.kind.NativeType == communicationPhoneType {
			bound, err := communicationReleaseOperation(a.endpoint, endpoint, last(u.Path))
			if polling != "phone" || err != nil || bound != endpoint {
				return serviceDenied("communication_resumed_release_changed")
			}
		} else {
			bound, err := communicationARMOperationURL(a.client.subscription, a.id, communicationARMVersion, endpoint)
			if isCommunicationDataType(a.kind.NativeType) || polling != "status" && polling != "location" || err != nil || bound != endpoint {
				return serviceDenied("communication_resumed_arm_operation_changed")
			}
		}
	}
	first, _ := url.Parse(result.ProviderOperationID)
	current, _ := url.Parse(operation)
	if a.kind.NativeType == communicationPhoneType && first.Path != current.Path || a.kind.NativeType != communicationPhoneType && !strings.EqualFold(first.Path, current.Path) {
		return serviceDenied("communication_resumed_operation_identity_changed")
	}
	return nil
}

func (a *communicationAction) poll(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	operation := text(result.Data["communication_operation"])
	polling := text(result.Data["communication_polling"])
	var res response
	var err error
	if polling == "phone" {
		var observed communicationObservation
		observed, err = a.observe(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, contracts.DependencyReadError(err)
		}
		res, err = a.client.communicationRequest(ctx, observed.account, catalog.RESTRequest{Method: "GET", URL: operation})
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
	} else {
		res, err = a.client.requestAt(ctx, "GET", operation, nil, nil, func(endpoint string) error {
			_, err := communicationARMOperationURL(a.client.subscription, a.id, communicationARMVersion, endpoint)
			return err
		})
	}
	if isNotFound(err) {
		return contracts.WaitResult{Done: true}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if polling == "phone" {
		if len(res.header.Values("Operation-Location"))+len(res.header.Values("Azure-AsyncOperation")) != 0 {
			return contracts.WaitResult{}, serviceDenied("communication_release_poll_protocol_changed")
		}
		done, state, err := communicationPhoneOperationState(operation, res)
		return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header)}, err
	}
	// Validate both native headers, including a secondary signed Location.
	// A rotated signature may replace only this exact operation's URL.
	next, _, err := communicationARMOperationHeaders(a.client.subscription, a.id, communicationARMVersion, res.header)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if next != "" {
		before, _ := url.Parse(operation)
		after, _ := url.Parse(next)
		if !strings.EqualFold(before.Path, after.Path) {
			return contracts.WaitResult{}, serviceDenied("communication_poll_successor_changed")
		}
	}
	done, state, err := communicationARMOperationState(a.id, operation, polling, res)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	var data map[string]any
	if !done && next != "" {
		header := "Azure-AsyncOperation"
		if polling == "location" {
			header = "Location"
		}
		if value := res.header.Get(header); value != "" {
			next, _ = communicationARMOperationURL(a.client.subscription, a.id, communicationARMVersion, value)
			data = maps.Clone(result.Data)
			data["communication_operation"] = next
		}
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: data}, nil
}

func (a *communicationAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.requestIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.verifyReceipt(request, result); err != nil {
		return contracts.WaitResult{}, err
	}
	retry := time.Minute
	if result.Data["communication_phase"] == "poll" {
		polled, err := a.poll(ctx, request, result)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if polled.RetryAfter > retry {
			retry = polled.RetryAfter
		}
		if polled.Data != nil {
			result.Data = polled.Data
		}
		if !polled.Done {
			result.Data = maps.Clone(result.Data)
			result.Data["communication_phase_binding"] = a.phaseBinding(request, result)
			return contracts.WaitResult{State: polled.State, RetryAfter: retry, Data: result.Data}, nil
		}
		result.Data = maps.Clone(result.Data)
		result.Data["communication_phase"] = "absence"
		result.Data["communication_phase_binding"] = a.phaseBinding(request, result)
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	if read.Exists && (a.kind.NativeType == communicationPhoneType || a.kind.NativeType == communicationType) {
		retry = max(retry, time.Hour)
	}
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: retry, Data: result.Data}, err
}
