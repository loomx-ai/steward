package gcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) MutationSettled(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.MutationSettlement, error) {
	if a.kind.NativeType == billingBudgetType {
		if err := a.billingBudgetActionIdentity(request); err != nil {
			return contracts.MutationSettlement{}, err
		}
		if len(result.Data) == 0 {
			return contracts.MutationSettlement{}, nil
		}
		wait, err := a.waitBillingBudget(ctx, request, result)
		if err != nil || !wait.Done {
			return contracts.MutationSettlement{}, err
		}
		return contracts.MutationSettlement{Settled: true, Operation: "billing-budget-synchronous-response:" + text(result.Data["review"])}, nil
	}
	if a.kind.NativeType == alertPolicyType || a.kind.NativeType == notificationChannelType || a.kind.NativeType == monitoringGroupType || a.kind.NativeType == monitoringDashboardType {
		if err := a.monitoringActionIdentity(request); err != nil {
			return contracts.MutationSettlement{}, err
		}
		// A lost response cannot prove that a synchronous request has returned.
		// Continue the original task to verify absence instead of releasing scope.
		if len(result.Data) == 0 {
			return contracts.MutationSettlement{}, nil
		}
		wait, err := a.waitMonitoring(ctx, request, result)
		if err != nil || !wait.Done {
			return contracts.MutationSettlement{}, err
		}
		return contracts.MutationSettlement{Settled: true, Operation: "monitoring-synchronous-response:" + text(result.Data["review"])}, nil
	}
	if !isRouterComponent(a.kind.NativeType) && a.kind.NativeType != routerType {
		return contracts.MutationSettlement{}, groupDenied("mutation_settlement_unsupported")
	}

	if a.kind.NativeType == routerType && len(result.Data) == 1 && result.Data["operation"] != nil {
		return a.routerPriorMutationSettled(ctx, request, result)
	}
	if err := a.routerComponentActionIdentity(request); err != nil {
		return contracts.MutationSettlement{}, err
	}
	operation, err := a.routerComponentReceipt(request, result)
	if err != nil {
		return contracts.MutationSettlement{}, err
	}
	// Every phase that the frozen review could have invoked needs terminal proof.
	// Wait may have sent deletion after detach before its new cursor was durable.
	stages := []string{a.routerComponentDeletePhase()}
	if a.kind.NativeType == routePolicyType {
		before, after, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
		if err != nil {
			return contracts.MutationSettlement{}, err
		}
		if firewallDigest(before) != firewallDigest(after) {
			stages = append([]string{routePolicyDetach}, stages...)
		} else if result.Data["phase"] == routePolicyDetach || result.ProviderOperationID != "" && result.ProviderOperationID != operation {
			return contracts.MutationSettlement{}, groupDenied("route_policy_unreviewed_detach")
		}
	}
	operations := make([]string, 0, len(stages))
	for _, stage := range stages {
		expected, operationType := "", ""
		if result.Data["phase"] == stage {
			expected, operationType = operation, text(result.Data["operation_type"])
		} else if stage == routePolicyDetach && result.ProviderOperationID != operation {
			expected = result.ProviderOperationID
		}
		settled, err := a.routerMutationStageSettled(ctx, request, stage, expected, operationType)
		if err != nil || !settled.Settled {
			return contracts.MutationSettlement{}, err
		}
		operations = append(operations, settled.Operation)
	}
	return contracts.MutationSettlement{Settled: true, Operation: strings.Join(operations, "\n")}, nil
}

func (a *action) routerMutationStageSettled(ctx context.Context, request contracts.ActionRequest, stage, operation, operationType string) (contracts.MutationSettlement, error) {
	if operation != "" {
		data, err := a.client.request(ctx, "GET", operation, nil)
		if err != nil && !isNotFound(err) {
			return contracts.MutationSettlement{}, err
		}
		if err == nil {
			return a.routerMutationOperationSettled(request, data, operation, operationType, stage, operationType == "" || stage == routerPriorDelete)
		}
	}
	// A lost/expired receipt may still have a native operation indexed by its
	// original request UUID. An empty list is uncertainty, never proof of no write.
	metadata, err := providerData()
	if err != nil {
		return contracts.MutationSettlement{}, err
	}
	op, ok := metadata.catalog.Operation("compute.regionOperations.list")
	if !ok {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_list_missing")
	}
	parameters := map[string]any{"project": a.client.project, "region": a.deleteParameters["region"]}
	parameters["filter"] = "clientOperationId = \"" + a.routePolicyNativeRequestID(request, stage) + "\""
	parameters["maxResults"] = 500
	seen := map[string]bool{}
	var found map[string]any
	for {
		bound, err := catalog.BindREST(op, parameters)
		if err != nil {
			return contracts.MutationSettlement{}, err
		}
		data, err := a.client.request(ctx, bound.Method, bound.URL, nil)
		if err != nil {
			return contracts.MutationSettlement{}, err
		}
		if err := checkListCompleteness(data); err != nil {
			return contracts.MutationSettlement{}, err
		}
		items, err := cloudNatObjects(data, "items")
		if err != nil {
			return contracts.MutationSettlement{}, err
		}
		for _, item := range items {
			// LIST recovery has no trusted operation name yet: require full native echoes.
			if found != nil || !a.routerMutationEchoes(request, item, stage) {
				return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_lookup_ambiguous")
			}
			found = item
		}
		token, present := data["nextPageToken"]
		if !present || token == "" {
			break
		}
		next, ok := token.(string)
		if !ok || seen[next] {
			return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_pagination_invalid")
		}
		seen[next] = true
		parameters["pageToken"] = next
	}
	if found == nil {
		return contracts.MutationSettlement{}, nil
	}
	if operation != "" {
		actual, err := a.routerComponentOperationURL(text(found["name"]))
		if err != nil || actual != operation {
			return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_lookup_changed")
		}
	}
	return a.routerMutationOperationSettled(request, found, operation, operationType, stage, true)
}

func (a *action) routerMutationOperationSettled(request contracts.ActionRequest, data map[string]any, expected, operationType, stage string, requireEchoes bool) (contracts.MutationSettlement, error) {
	if name, ok := data["name"].(string); !ok || name == "" {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_name_invalid")
	}
	if kind, ok := data["operationType"].(string); !ok || kind == "" || a.kind.NativeType == routerType && kind != "delete" {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_type_invalid")
	}

	operation, err := a.routerComponentOperationIdentity(request, data, operationType, stage)
	if err != nil {
		return contracts.MutationSettlement{}, err
	}
	if requireEchoes && !a.routerMutationEchoes(request, data, stage) {
		return contracts.MutationSettlement{}, groupDenied("router_operation_echoes_missing")
	}
	if expected != "" && expected != operation {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_lookup_changed")
	}
	statusData := cloneParameters(data)
	delete(statusData, "error")
	if err := checkListCompleteness(statusData); err != nil {
		return contracts.MutationSettlement{}, err
	}
	// DONE is terminal even when the operation failed. Validate error shapes, but
	// do not confuse a failed deletion with an operation that can still write.
	if value, present := data["error"]; present {
		failures, err := cloudNatObjects(object(value), "errors")
		if err != nil || len(failures) == 0 {
			return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_error_invalid")
		}
		for _, failure := range failures {
			if code, ok := failure["code"].(string); !ok || code == "" {
				return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_error_invalid")
			}
		}
	}
	if value, present := data["httpErrorStatusCode"]; present {
		raw, err := json.Marshal(value)
		var code int
		if err != nil || value == nil || json.Unmarshal(raw, &code) != nil || code != 0 && (code < 100 || code > 599) {
			return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_http_error_invalid")
		}
	}
	if value, present := data["httpErrorMessage"]; present {
		if _, ok := value.(string); !ok {
			return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_http_error_invalid")
		}
	}
	return contracts.MutationSettlement{Settled: data["status"] == "DONE", Operation: operation}, nil
}

// A lookup or reconstructed phase has no trusted receipt for this operation:
// require native request, resource and incarnation echoes, then validate scope.
func (a *action) routerMutationEchoes(request contracts.ActionRequest, data map[string]any, stage string) bool {
	target := a.client.canonicalName(text(data["targetLink"]))
	return data["clientOperationId"] == a.routePolicyNativeRequestID(request, stage) && data["targetId"] == request.Asset.Normalized[a.routerComponentIncarnationKey()] && (target == a.client.canonicalName(a.routerComponentParent()) || stage != routePolicyDetach && target == a.client.canonicalName(a.endpoint))
}
