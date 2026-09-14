package gcp

import (
	"context"
	"encoding/json"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) MutationSettled(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.MutationSettlement, error) {
	if a.kind.NativeType != cloudNatType {
		return contracts.MutationSettlement{}, groupDenied("mutation_settlement_unsupported")
	}
	if err := a.routerComponentActionIdentity(request); err != nil {
		return contracts.MutationSettlement{}, err
	}
	operation, err := a.routerComponentReceipt(request, result)
	if err != nil {
		return contracts.MutationSettlement{}, err
	}
	if operation != "" {
		data, err := a.client.request(ctx, "GET", operation, nil)
		if err != nil && !isNotFound(err) {
			return contracts.MutationSettlement{}, err
		}
		if err == nil {
			return a.cloudNatOperationSettled(request, data, operation, text(result.Data["operation_type"]))
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
	parameters := cloneParameters(a.deleteParameters)
	delete(parameters, "router")
	parameters["filter"] = "clientOperationId = \"" + a.routerComponentRequestID(request) + "\""
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
			if found != nil || item["clientOperationId"] != a.routerComponentRequestID(request) || item["targetId"] != request.Asset.Normalized[cloudNatRouterID] || a.client.canonicalName(text(item["targetLink"])) != a.client.canonicalName(a.routerComponentParent()) {
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
	return a.cloudNatOperationSettled(request, found, operation, text(result.Data["operation_type"]))
}

func (a *action) cloudNatOperationSettled(request contracts.ActionRequest, data map[string]any, expected, operationType string) (contracts.MutationSettlement, error) {
	if name, ok := data["name"].(string); !ok || name == "" {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_name_invalid")
	}
	if kind, ok := data["operationType"].(string); !ok || kind == "" {
		return contracts.MutationSettlement{}, groupDenied("cloud_nat_operation_type_invalid")
	}

	operation, err := a.routerComponentOperationIdentity(request, data, operationType, a.routerComponentDeletePhase())
	if err != nil {
		return contracts.MutationSettlement{}, err
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
