package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Fleet and AKS share ContainerService's regional operation endpoints. Native
// CLI recordings use signed 2016-03-30 URLs; the pinned Swagger examples also
// publish the unsigned 2022-02-01 resource-group operationResults route.
func containerServiceOperationPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	if len(parts) == 11 && parts[3] == "resourcegroups" && parts[4] != "" {
		parts = append(slices.Clone(parts[:3]), parts[5:]...)
	}
	return len(parts) == 9 && parts[0] == "" && parts[1] == "subscriptions" && uuidPattern.MatchString(parts[2]) && parts[3] == "providers" && parts[4] == "microsoft.containerservice" && parts[5] == "locations" && cosmosOperationRegion.MatchString(parts[6]) && (parts[7] == "operations" || parts[7] == "operationresults") && uuidPattern.MatchString(parts[8])
}

func validateFleetOperationURL(subscription, id, location, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32<<10 || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" && u.RawPath != u.Path || !containerServiceOperationPath(u.Path) {
		return serviceDenied("invalid_fleet_operation_endpoint")
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	resourceID, _, err := fleetIdentity(id)
	if err != nil || !strings.HasPrefix(resourceID, "/subscriptions/"+strings.ToLower(subscription)+"/") || parts[2] != strings.ToLower(subscription) {
		return serviceDenied("fleet_operation_subscription_changed")
	}
	if len(parts) == 11 && parts[4] != strings.Split(resourceID, "/")[4] {
		return serviceDenied("fleet_operation_group_changed")
	}
	if location == "" || location == "global" || parts[len(parts)-3] != strings.ReplaceAll(strings.ToLower(location), " ", "") {
		return serviceDenied("fleet_operation_region_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 {
		return serviceDenied("invalid_fleet_operation_query")
	}
	switch query.Get("api-version") {
	case "2016-03-30":
		if len(query) != 5 {
			return serviceDenied("invalid_fleet_signed_operation")
		}
		for _, name := range []string{"t", "c", "s", "h"} {
			if len(query[name]) != 1 || query.Get(name) == "" || strings.ContainsAny(query.Get(name), "\x00\r\n\t ") {
				return serviceDenied("invalid_fleet_signed_operation")
			}
		}
	case "2022-02-01":
		if len(query) != 1 || parts[len(parts)-2] != "operationresults" {
			return serviceDenied("invalid_fleet_unsigned_operation")
		}
	default:
		return serviceDenied("fleet_operation_version_changed")
	}
	return nil
}

func fleetOperationSafeValue(value map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"id", "name", "status", "startTime", "endTime", "request_id", "status_code"} {
		switch entry := value[key].(type) {
		case string, int:
			result[key] = entry
		}
	}
	if body, ok := value["body"].(map[string]any); ok {
		result["body"] = fleetOperationSafeValue(body)
	}
	return result
}

func (a *action) fleetOperationBinding(endpoint, polling string) string {
	return a.client.privateConfiguration(map[string]any{"id": a.id, "kind": a.kind.NativeType, "connection": a.connectionID, "partition": a.partition, "location": a.location, "endpoint": endpoint, "polling": polling, "protocol": "fleet-operation-1"})
}

func (a *action) fleetOperationResult(res response) (contracts.ActionResult, error) {
	if err := operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	if res.status != 0 && res.status != 200 && res.status != 202 && res.status != 204 || res.status == 0 && len(res.data)+len(res.header) != 0 || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil || monitorRuleFields(res.data, "id", "name", "status", "properties", "code", "error", "nextLink") != nil {
		return contracts.ActionResult{}, serviceDenied("invalid_fleet_operation_response")
	}
	if len(res.data) != 0 && (!strings.EqualFold(text(res.data["id"]), a.id) || fleetValidate(a.kind.NativeType, res.data) != nil) {
		return contracts.ActionResult{}, serviceDenied("fleet_mutation_response_identity_changed")
	}
	if len(res.header.Values("Operation-Location")) != 0 || len(res.header.Values("Azure-AsyncOperation")) > 1 || len(res.header.Values("Location")) > 1 {
		return contracts.ActionResult{}, serviceDenied("ambiguous_fleet_operation_headers")
	}
	operation, polling := res.header.Get("Azure-AsyncOperation"), "status"
	if operation == "" {
		operation, polling = res.header.Get("Location"), "location"
	}
	if res.status == 202 && operation == "" {
		return contracts.ActionResult{}, serviceDenied("fleet_async_operation_missing")
	}
	for _, endpoint := range []string{res.header.Get("Azure-AsyncOperation"), res.header.Get("Location")} {
		if endpoint == "" {
			continue
		}
		if err := validateFleetOperationURL(a.client.subscription, a.id, a.location, endpoint); err != nil {
			return contracts.ActionResult{}, err
		}
		if !strings.EqualFold(last(strings.Split(endpoint, "?")[0]), last(strings.Split(operation, "?")[0])) {
			return contracts.ActionResult{}, serviceDenied("fleet_operation_headers_disagree")
		}
	}
	if operation != "" && polling == "location" {
		u, _ := url.Parse(operation)
		parts := strings.Split(u.Path, "/")
		if !strings.EqualFold(parts[len(parts)-2], "operationresults") {
			return contracts.ActionResult{}, serviceDenied("fleet_location_protocol_changed")
		}
	}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"polling": polling, "fleet_operation_binding": a.fleetOperationBinding(operation, polling)}}, nil
}

// Operation success permits the caller to perform its own native resource and
// residual reads. Neither an expired operation URL nor a 204 proves deletion.
func (a *action) fleetPoll(ctx context.Context, result contracts.ActionResult) (contracts.WaitResult, error) {
	polling := text(result.Data["polling"])
	if polling != "status" && polling != "location" || text(result.Data["fleet_operation_binding"]) != a.fleetOperationBinding(result.ProviderOperationID, polling) {
		return contracts.WaitResult{}, serviceDenied("fleet_operation_receipt_changed")
	}
	operation := result.ProviderOperationID
	if next, present := result.Data["fleet_poll_operation"]; present {
		before, _ := url.Parse(operation)
		after, err := url.Parse(text(next))
		if err != nil || before == nil || !strings.EqualFold(before.Path, after.Path) || text(result.Data["fleet_poll_binding"]) != a.fleetOperationBinding(text(next), polling) {
			return contracts.WaitResult{}, serviceDenied("fleet_resumed_operation_changed")
		}
		operation = text(next)
	} else if result.Data["fleet_poll_binding"] != nil {
		return contracts.WaitResult{}, serviceDenied("incomplete_fleet_poll_receipt")
	}
	if operation == "" {
		return contracts.WaitResult{Done: true}, nil
	}
	validate := func(endpoint string) error {
		return validateFleetOperationURL(a.client.subscription, a.id, a.location, endpoint)
	}
	res, err := a.client.requestAt(ctx, "GET", operation, nil, nil, validate)
	if isNotFound(err) {
		return contracts.WaitResult{Done: true}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if res.status != 200 && res.status != 202 && res.status != 204 || res.data["code"] != nil || res.data["nextLink"] != nil || monitorRuleFields(res.data, "id", "name", "status", "code", "error", "nextLink", "properties") != nil {
		return contracts.WaitResult{}, serviceDenied("invalid_fleet_poll_response")
	}
	if err := operationError(res); err != nil {
		return contracts.WaitResult{}, err
	}
	if res.data["error"] != nil {
		return contracts.WaitResult{}, serviceDenied("invalid_fleet_poll_error")
	}
	u, _ := url.Parse(operation)
	if name, present := res.data["name"]; present && (name != text(name) || !strings.EqualFold(text(name), last(u.Path))) {
		return contracts.WaitResult{}, serviceDenied("fleet_poll_operation_identity_changed")
	}
	if res.data["id"] != nil && !strings.EqualFold(text(res.data["id"]), u.Path) {
		return contracts.WaitResult{}, serviceDenied("fleet_poll_operation_identity_changed")
	}
	state := text(res.data["status"])
	if res.data["status"] != nil && res.data["status"] != state {
		return contracts.WaitResult{}, serviceDenied("invalid_fleet_poll_state")
	}
	done := state == "Succeeded"
	if state == "" {
		if polling != "location" || res.status != 204 && res.status != 200 || len(res.data) != 0 {
			return contracts.WaitResult{}, serviceDenied("fleet_poll_state_missing")
		}
		done = true
	} else if !slices.Contains([]string{"Succeeded", "InProgress", "Running", "Accepted"}, state) {
		return contracts.WaitResult{}, serviceDenied("unknown_fleet_poll_state")
	}
	var data map[string]any
	if !done {
		header := "Azure-AsyncOperation"
		if polling == "location" {
			header = "Location"
		}
		if len(res.header.Values(header)) > 1 {
			return contracts.WaitResult{}, serviceDenied("ambiguous_fleet_poll_successor")
		}
		if next := res.header.Get(header); next != "" {
			if err := validate(next); err != nil {
				return contracts.WaitResult{}, err
			}
			after, _ := url.Parse(next)
			if !strings.EqualFold(after.Path, u.Path) {
				return contracts.WaitResult{}, serviceDenied("fleet_poll_successor_changed")
			}
			data = maps.Clone(result.Data)
			data["fleet_poll_operation"], data["fleet_poll_binding"] = next, a.fleetOperationBinding(next, polling)
		}
	}
	return contracts.WaitResult{Done: done, RetryAfter: retryAfter(res.header), State: state, Data: data}, nil
}
