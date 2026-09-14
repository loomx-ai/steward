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

func (a *action) routerComponentActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || !isRouterComponent(a.kind.NativeType) || request.IdempotencyKey == "" || len(request.Parameters) != 0 || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("route_policy_action_changed")
	}
	if _, _, err := a.client.routerComponentOperation(a.kind.NativeType, a.identity.NativeID, "DELETE"); err != nil {
		return err
	}
	if err := routerComponentData(a.kind.NativeType, request.Asset.Normalized, last(a.identity.NativeID)); err != nil {
		return err
	}
	if a.kind.NativeType == cloudNatType {
		if err := cloudNatData(object(request.Asset.Normalized[cloudNatReview]), last(a.identity.NativeID)); err != nil {
			return groupDenied("cloud_nat_review_missing")
		}
	}
	// Fingerprints are opaque native revision tokens, compared without rewriting.
	if (a.kind.NativeType != cloudNatType && text(request.Asset.Normalized["fingerprint"]) == "") || !firewallNumericID(text(request.Asset.Normalized[a.routerComponentIncarnationKey()])) {
		return groupDenied("route_policy_review_missing")
	}
	if a.kind.NativeType == namedSetType || a.kind.NativeType == cloudNatType {
		return nil
	}
	_, _, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
	return err
}

func (a *action) routerComponentParent() string {
	if a.kind.NativeType == cloudNatType {
		parent, _, _ := strings.Cut(a.identity.NativeID, "/nats/")
		return parent
	}
	parent, _, _ := strings.Cut(a.identity.NativeID, "/routePolicies/")
	if a.kind.NativeType == namedSetType {
		parent, _, _ = strings.Cut(a.identity.NativeID, "/namedSets/")
	}
	return parent
}

func (a *action) routerComponentParentData(ctx context.Context, request contracts.ActionRequest) (map[string]any, error) {
	kind, _ := findType(routerType)
	endpoint, err := a.client.resourceURL(kind, a.routerComponentParent())
	if err != nil {
		return nil, err
	}
	data, err := a.client.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if err := checkListCompleteness(data); err != nil {
		return nil, err
	}
	if data["name"] != last(a.routerComponentParent()) || a.client.canonicalName(text(data["selfLink"])) != a.client.canonicalName(a.routerComponentParent()) || data["id"] != request.Asset.Normalized[a.routerComponentIncarnationKey()] {
		return nil, groupDenied("route_policy_router_recreated_or_changed")
	}
	return data, nil
}

func (a *action) checkRouterComponentParent(ctx context.Context, request contracts.ActionRequest) (bool, error) {
	data, err := a.routerComponentParentData(ctx, request)
	if err != nil {
		return false, err
	}
	if a.kind.NativeType == namedSetType {
		return false, nil
	}
	_, attached, err := a.routePolicyBGPMerge(ctx, request, data)
	return attached, err
}

func (a *action) routerComponentReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.routerComponentActionIdentity(request); err != nil {
		return read, err
	}
	if request.ExecutionResult != nil {
		if _, err := a.routerComponentReceipt(request, *request.ExecutionResult); err != nil {
			return read, err
		}
	}
	if a.kind.NativeType == cloudNatType {
		_, read.Exists, err = a.cloudNatLive(ctx, request)
		return read, err
	}
	if _, err := a.checkRouterComponentParent(ctx, request); err != nil {
		return read, err
	}
	live, err := a.client.routerComponentRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if err != nil && !isNotFound(err) {
		return read, err
	}
	if err == nil && a.routerComponentConfiguration(live) != a.routerComponentConfiguration(request.Asset.Normalized) {
		return read, groupDenied("route_policy_configuration_changed")
	}
	read.Exists = err == nil
	if a.kind.NativeType == namedSetType {
		if err := a.namedSetUnreferenced(ctx); err != nil {
			return read, err
		}
	}
	attached, err := a.checkRouterComponentParent(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	read.Exists = read.Exists || attached
	if attached {
		read.State = routePolicyDetach
	}
	return read, nil
}

func (a *action) routerComponentPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.routerComponentReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) routerComponentRequestID(request contracts.ActionRequest) string {
	return googleRequestID(request.IdempotencyKey + "/" + string(a.identity.ConnectionID) + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[a.routerComponentIncarnationKey()]) + "/" + a.routerComponentConfiguration(request.Asset.Normalized) + "/" + firewallDigest(request.Asset.Normalized[routePolicyPeers]))
}

func (a *action) routerComponentOperationURL(name string) (string, error) {
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

func (a *action) routerComponentOperationIdentity(request contracts.ActionRequest, data map[string]any, operationType, stage string) (string, error) {
	operation, err := a.routerComponentOperationURL(text(data["name"]))
	if err != nil {
		return "", err
	}
	if value, exists := data["selfLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(operation) {
		return "", groupDenied("route_policy_operation_scope_changed")
	}
	// Subresource mutations can identify their containing router. Optional native
	// echoes must agree; the receipt also binds the exact policy and request ID.
	if value, exists := data["targetLink"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(a.routerComponentParent()) && (stage == routePolicyDetach || a.client.canonicalName(text(value)) != a.client.canonicalName(a.endpoint)) {
		return "", groupDenied("route_policy_operation_target_changed")
	}
	if value, exists := data["region"]; exists && a.client.canonicalName(text(value)) != a.client.canonicalName(strings.TrimSuffix(operation, "/operations/"+text(data["name"]))) {
		return "", groupDenied("route_policy_operation_region_changed")
	}
	if value, exists := data["zone"]; exists && value != "" {
		return "", groupDenied("route_policy_operation_region_changed")
	}
	if value, exists := data["targetId"]; exists && value != request.Asset.Normalized[a.routerComponentIncarnationKey()] {
		return "", groupDenied("route_policy_operation_target_changed")
	}
	if value, exists := data["clientOperationId"]; exists && value != a.routePolicyNativeRequestID(request, stage) {
		return "", groupDenied("route_policy_operation_request_changed")
	}
	if text(data["operationType"]) == "" || operationType != "" && data["operationType"] != operationType || !slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, text(data["status"])) {
		return "", groupDenied("route_policy_operation_state_invalid")
	}
	return operation, nil
}

func (a *action) routerComponentOperationResult(request contracts.ActionRequest, data map[string]any, operationType, requestID, stage string) (string, error) {
	operation, err := a.routerComponentOperationIdentity(request, data, operationType, stage)
	if err != nil {
		return "", err
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

func (a *action) routerComponentStage(request contracts.ActionRequest, stage, operation, initial, operationType string) map[string]any {
	return map[string]any{"phase": stage, "resource": a.identity.NativeID, "connection": string(a.identity.ConnectionID), "configuration": a.routerComponentConfiguration(request.Asset.Normalized), "parent_id": request.Asset.Normalized[a.routerComponentIncarnationKey()], "bgp_review": firewallDigest(request.Asset.Normalized[routePolicyPeers]), "request_id": a.routePolicyNativeRequestID(request, stage), "operation": operation, "initial_operation": initial, "operation_type": operationType}
}

func (a *action) routerComponentReceipt(request contracts.ActionRequest, result contracts.ActionResult) (string, error) {
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		return "", nil
	}
	stage := text(result.Data["phase"])
	if stage != a.routerComponentDeletePhase() && (a.kind.NativeType != routePolicyType || stage != routePolicyDetach) {
		return "", groupDenied("route_policy_receipt_phase_invalid")
	}
	for _, endpoint := range []string{result.ProviderOperationID, text(result.Data["operation"])} {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", groupDenied("route_policy_receipt_invalid")
		}
		expected, err := a.routerComponentOperationURL(last(parsed.Path))
		if err != nil || expected != endpoint {
			return "", groupDenied("route_policy_receipt_invalid")
		}
	}
	operation := text(result.Data["operation"])
	if text(result.Data["operation_type"]) == "" || firewallDigest(result.Data) != firewallDigest(a.routerComponentStage(request, stage, operation, result.ProviderOperationID, text(result.Data["operation_type"]))) {
		return "", groupDenied("route_policy_receipt_changed")
	}
	return operation, nil
}

func (a *action) executeRouterComponent(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if a.kind.NativeType == cloudNatType {
		return a.deleteCloudNat(ctx, request)
	}
	attached, err := a.checkRouterComponentParent(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if attached {
		if request.ExecutionResult != nil {
			return contracts.ActionResult{}, groupDenied("route_policy_bgp_reference_reintroduced")
		}
		return a.detachRoutePolicy(ctx, request)
	}
	return a.deleteRouterComponent(ctx, request)
}

func (a *action) deleteRouterComponent(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	parameters := cloneParameters(a.deleteParameters)
	parameters["requestId"] = a.routerComponentRequestID(request)
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.routerComponentReadback(ctx, request)
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
	operation, err := a.routerComponentOperationResult(request, response.Data, "", response.RequestID, a.routerComponentDeletePhase())
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.routerComponentStage(request, a.routerComponentDeletePhase(), operation, operation, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitRouterComponent(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.routerComponentActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	operation, err := a.routerComponentReceipt(request, result)
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
			actual, err := a.routerComponentOperationResult(request, response.Data, text(result.Data["operation_type"]), response.RequestID, text(result.Data["phase"]))
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
	read, err := a.routerComponentReadback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if text(result.Data["phase"]) == routePolicyDetach && !pending && read.Exists && read.State != routePolicyDetach {
		next, err := a.executeRouterComponent(ctx, request)
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
