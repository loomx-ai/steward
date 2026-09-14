package gcp

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func routePolicyConfiguration(data map[string]any) string {
	value := map[string]any{}
	for _, key := range []string{"name", "type", "description", "terms", "fingerprint"} {
		if field, exists := data[key]; exists {
			value[key] = field
		}
	}
	// Native term order is immaterial; action order within a term is significant.
	if terms, ok := value["terms"].([]any); ok {
		terms = slices.Clone(terms)
		slices.SortFunc(terms, func(a, b any) int { return strings.Compare(firewallDigest(a), firewallDigest(b)) })
		value["terms"] = terms
	}
	return firewallDigest(value)
}

func (a *action) routePolicyActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || a.kind.NativeType != routePolicyType || request.IdempotencyKey == "" || len(request.Parameters) != 0 || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("route_policy_action_changed")
	}
	if _, _, err := a.client.routePolicyOperation(a.identity.NativeID, "DELETE"); err != nil {
		return err
	}
	if err := routePolicyData(request.Asset.Normalized, last(a.identity.NativeID)); err != nil {
		return err
	}
	// Fingerprints are opaque native revision tokens, compared without rewriting.
	if text(request.Asset.Normalized["fingerprint"]) == "" || !firewallNumericID(text(request.Asset.Normalized[routePolicyRouterID])) {
		return groupDenied("route_policy_review_missing")
	}
	return nil
}

func (a *action) routePolicyParent() string {
	return strings.TrimSuffix(a.identity.NativeID, "/routePolicies/"+last(a.identity.NativeID))
}

func (a *action) checkRoutePolicyParent(ctx context.Context, request contracts.ActionRequest) error {
	kind, _ := findType(routerType)
	endpoint, err := a.client.resourceURL(kind, a.routePolicyParent())
	if err != nil {
		return err
	}
	data, err := a.client.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if data["name"] != last(a.routePolicyParent()) || a.client.canonicalName(text(data["selfLink"])) != a.client.canonicalName(a.routePolicyParent()) || data["id"] != request.Asset.Normalized[routePolicyRouterID] {
		return groupDenied("route_policy_router_recreated_or_changed")
	}
	return nil
}

func (a *action) routePolicyReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.routePolicyActionIdentity(request); err != nil {
		return read, err
	}
	if request.ExecutionResult != nil {
		if _, err := a.routePolicyReceipt(request, *request.ExecutionResult); err != nil {
			return read, err
		}
	}
	if err := a.checkRoutePolicyParent(ctx, request); err != nil {
		return read, err
	}
	live, err := a.client.routePolicyRead(ctx, a.identity.NativeID)
	if err != nil && !isNotFound(err) {
		return read, err
	}
	if err == nil && routePolicyConfiguration(live) != routePolicyConfiguration(request.Asset.Normalized) {
		return read, groupDenied("route_policy_configuration_changed")
	}
	read.Exists = err == nil
	if err := a.checkRoutePolicyParent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return read, nil
}

func (a *action) routePolicyPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.routePolicyReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) routePolicyRequestID(request contracts.ActionRequest) string {
	return googleRequestID(request.IdempotencyKey + "/" + string(a.identity.ConnectionID) + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[routePolicyRouterID]) + "/" + routePolicyConfiguration(request.Asset.Normalized))
}

func (a *action) routePolicyOperationURL(name string) (string, error) {
	if !segmentPattern.MatchString(name) || name == "." || name == ".." {
		return "", groupDenied("route_policy_operation_name_invalid")
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	operation, ok := metadata.catalog.Operation("compute.regionOperations.get")
	if !ok {
		return "", groupDenied("route_policy_operation_method_missing")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"project": a.client.project, "region": a.deleteParameters["region"], "operation": name})
	return bound.URL, err
}

func (a *action) routePolicyOperationResult(request contracts.ActionRequest, data map[string]any, operationType, requestID string) (string, error) {
	operation, err := a.routePolicyOperationURL(text(data["name"]))
	if err != nil {
		return "", err
	}
	if value, exists := data["selfLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(operation) {
		return "", groupDenied("route_policy_operation_scope_changed")
	}
	// Subresource mutations can identify their containing router. Optional native
	// echoes must agree; the receipt also binds the exact policy and request ID.
	if value, exists := data["targetLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(a.routePolicyParent()) && a.client.canonicalName(text(value)) != a.client.canonicalName(a.endpoint) {
		return "", groupDenied("route_policy_operation_target_changed")
	}
	if value, exists := data["region"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(strings.TrimSuffix(operation, "/operations/"+text(data["name"]))) {
		return "", groupDenied("route_policy_operation_region_changed")
	}
	if value, exists := data["zone"]; exists && value != "" {
		return "", groupDenied("route_policy_operation_region_changed")
	}
	if value, exists := data["targetId"]; exists && value != request.Asset.Normalized[routePolicyRouterID] {
		return "", groupDenied("route_policy_operation_target_changed")
	}
	if value, exists := data["clientOperationId"]; exists && value != a.routePolicyRequestID(request) {
		return "", groupDenied("route_policy_operation_request_changed")
	}
	if text(data["operationType"]) == "" || operationType != "" && data["operationType"] != operationType || !slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, text(data["status"])) {
		return "", groupDenied("route_policy_operation_state_invalid")
	}
	if _, exists := data["error"]; exists {
		if err := operationError(data, requestID); err != nil {
			return "", err
		}
		return "", groupDenied("route_policy_operation_error_invalid")
	}
	if value, exists := data["httpErrorStatusCode"]; exists {
		raw, err := json.Marshal(value)
		var code int
		if err != nil || value == nil || json.Unmarshal(raw, &code) != nil || code != 0 {
			return "", groupDenied("route_policy_operation_http_error")
		}
	}
	if value, exists := data["httpErrorMessage"]; exists && value != "" {
		return "", groupDenied("route_policy_operation_http_error")
	}
	return operation, nil
}

func (a *action) routePolicyPhase(request contracts.ActionRequest, operation, operationType string) map[string]any {
	return map[string]any{"phase": "route_policy_delete", "resource": a.identity.NativeID, "connection": string(a.identity.ConnectionID), "configuration": routePolicyConfiguration(request.Asset.Normalized), "parent_id": request.Asset.Normalized[routePolicyRouterID], "request_id": a.routePolicyRequestID(request), "operation": operation, "operation_type": operationType}
}

func (a *action) routePolicyReceipt(request contracts.ActionRequest, result contracts.ActionResult) (string, error) {
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		return "", nil
	}
	parsed, err := url.Parse(result.ProviderOperationID)
	if err != nil {
		return "", groupDenied("route_policy_receipt_invalid")
	}
	expected, err := a.routePolicyOperationURL(last(parsed.Path))
	if err != nil || expected != result.ProviderOperationID || text(result.Data["operation_type"]) == "" || firewallDigest(result.Data) != firewallDigest(a.routePolicyPhase(request, expected, text(result.Data["operation_type"]))) {
		return "", groupDenied("route_policy_receipt_changed")
	}
	return expected, nil
}

func (a *action) executeRoutePolicy(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	parameters := cloneParameters(a.deleteParameters)
	parameters["requestId"] = a.routePolicyRequestID(request)
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.routePolicyReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("route_policy_delete_not_observed")
		}
		return contracts.ActionResult{ProviderRequestID: response.RequestID}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := a.routePolicyOperationResult(request, response.Data, "", response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.routePolicyPhase(request, operation, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitRoutePolicy(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.routePolicyActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	operation, err := a.routePolicyReceipt(request, result)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	pending := false
	if operation != "" {
		response, err := a.client.requestResult(ctx, "GET", operation, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.routePolicyOperationResult(request, response.Data, text(result.Data["operation_type"]), response.RequestID)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != operation {
				return contracts.WaitResult{}, groupDenied("route_policy_operation_changed")
			}
			pending = response.Data["status"] != "DONE"
		}
	}
	read, err := a.routePolicyReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !pending && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
