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
	_, _, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
	return err
}

func (a *action) routePolicyParent() string {
	return strings.TrimSuffix(a.identity.NativeID, "/routePolicies/"+last(a.identity.NativeID))
}

func (a *action) checkRoutePolicyParent(ctx context.Context, request contracts.ActionRequest) (bool, error) {
	kind, _ := findType(routerType)
	endpoint, err := a.client.resourceURL(kind, a.routePolicyParent())
	if err != nil {
		return false, err
	}
	data, err := a.client.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return false, contracts.DependencyReadError(err)
	}
	if data["name"] != last(a.routePolicyParent()) || a.client.canonicalName(text(data["selfLink"])) != a.client.canonicalName(a.routePolicyParent()) || data["id"] != request.Asset.Normalized[routePolicyRouterID] {
		return false, groupDenied("route_policy_router_recreated_or_changed")
	}
	return a.routePolicyBGPState(request, data)
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
	if _, err := a.checkRoutePolicyParent(ctx, request); err != nil {
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
	attached, err := a.checkRoutePolicyParent(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	read.Exists = read.Exists || attached
	if attached {
		read.State = routePolicyDetach
	}
	return read, nil
}

func (a *action) routePolicyPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.routePolicyReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) routePolicyRequestID(request contracts.ActionRequest) string {
	return googleRequestID(request.IdempotencyKey + "/" + string(a.identity.ConnectionID) + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[routePolicyRouterID]) + "/" + routePolicyConfiguration(request.Asset.Normalized) + "/" + firewallDigest(request.Asset.Normalized[routePolicyPeers]))
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

func (a *action) routePolicyOperationResult(request contracts.ActionRequest, data map[string]any, operationType, requestID, stage string) (string, error) {
	operation, err := a.routePolicyOperationURL(text(data["name"]))
	if err != nil {
		return "", err
	}
	if value, exists := data["selfLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(operation) {
		return "", groupDenied("route_policy_operation_scope_changed")
	}
	// Subresource mutations can identify their containing router. Optional native
	// echoes must agree; the receipt also binds the exact policy and request ID.
	if value, exists := data["targetLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(a.routePolicyParent()) && (stage == routePolicyDetach || a.client.canonicalName(text(value)) != a.client.canonicalName(a.endpoint)) {
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
	if value, exists := data["clientOperationId"]; exists && value != a.routePolicyNativeRequestID(request, stage) {
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

func (a *action) routePolicyStage(request contracts.ActionRequest, stage, operation, initial, operationType string) map[string]any {
	return map[string]any{"phase": stage, "resource": a.identity.NativeID, "connection": string(a.identity.ConnectionID), "configuration": routePolicyConfiguration(request.Asset.Normalized), "parent_id": request.Asset.Normalized[routePolicyRouterID], "bgp_review": firewallDigest(request.Asset.Normalized[routePolicyPeers]), "request_id": a.routePolicyNativeRequestID(request, stage), "operation": operation, "initial_operation": initial, "operation_type": operationType}
}

func (a *action) routePolicyReceipt(request contracts.ActionRequest, result contracts.ActionResult) (string, error) {
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		return "", nil
	}
	stage := text(result.Data["phase"])
	if stage != routePolicyDetach && stage != "route_policy_delete" {
		return "", groupDenied("route_policy_receipt_phase_invalid")
	}
	for _, endpoint := range []string{result.ProviderOperationID, text(result.Data["operation"])} {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", groupDenied("route_policy_receipt_invalid")
		}
		expected, err := a.routePolicyOperationURL(last(parsed.Path))
		if err != nil || expected != endpoint {
			return "", groupDenied("route_policy_receipt_invalid")
		}
	}
	operation := text(result.Data["operation"])
	if text(result.Data["operation_type"]) == "" || firewallDigest(result.Data) != firewallDigest(a.routePolicyStage(request, stage, operation, result.ProviderOperationID, text(result.Data["operation_type"]))) {
		return "", groupDenied("route_policy_receipt_changed")
	}
	return operation, nil
}

func (a *action) executeRoutePolicy(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	attached, err := a.checkRoutePolicyParent(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if attached {
		if request.ExecutionResult != nil {
			return contracts.ActionResult{}, groupDenied("route_policy_bgp_reference_reintroduced")
		}
		return a.detachRoutePolicy(ctx, request)
	}
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
	operation, err := a.routePolicyOperationResult(request, response.Data, "", response.RequestID, "route_policy_delete")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.routePolicyStage(request, "route_policy_delete", operation, operation, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
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
			actual, err := a.routePolicyOperationResult(request, response.Data, text(result.Data["operation_type"]), response.RequestID, text(result.Data["phase"]))
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != operation {
				return contracts.WaitResult{}, groupDenied("route_policy_operation_changed")
			}
			pending = response.Data["status"] != "DONE"
		}
	}
	request.ExecutionResult = &result
	read, err := a.routePolicyReadback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if text(result.Data["phase"]) == routePolicyDetach && !pending && read.Exists && read.State != routePolicyDetach {
		next, err := a.executeRoutePolicy(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(next.Data) == 0 {
			return contracts.WaitResult{Done: true, Data: result.Data}, nil
		}
		next.Data["initial_operation"] = result.ProviderOperationID
		return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
	}
	return contracts.WaitResult{Done: !pending && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, nil
}
