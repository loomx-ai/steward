package gcp

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const cloudNatReview = "_cloud_nat_configuration"

// Preserve unknown native configuration fields. Only the documented output-only
// effective timeout is omitted from a PATCH and the reviewed configuration hash.
func cloudNatConfiguration(data map[string]any) map[string]any {
	if review, ok := data[cloudNatReview].(map[string]any); ok {
		data = review
	}
	value := cloneParameters(data)
	delete(value, "effectiveTcpTimeWaitTimeoutSec")
	return value
}

func (a *action) cloudNatLive(ctx context.Context, request contracts.ActionRequest) ([]map[string]any, bool, error) {
	data, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if err != nil {
		return nil, false, contracts.DependencyReadError(err)
	}
	if err := checkListCompleteness(data); err != nil {
		return nil, false, err
	}
	values, err := a.client.cloudNatRouter(data, a.routerComponentParent(), text(request.Asset.Normalized[cloudNatRouterID]))
	if err != nil {
		return nil, false, err
	}
	exists := false
	for _, value := range values {
		if value["name"] != last(a.identity.NativeID) {
			continue
		}
		if a.routerComponentConfiguration(value) != a.routerComponentConfiguration(request.Asset.Normalized) {
			return nil, false, groupDenied("cloud_nat_configuration_changed")
		}
		exists = true
	}
	return values, exists, nil
}

func (a *action) deleteCloudNat(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	// Re-read immediately before constructing the merge-patch. Never replay the
	// scanned sibling array: another completed deletion must not recreate a NAT.
	values, exists, err := a.cloudNatLive(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !exists {
		return contracts.ActionResult{}, nil
	}
	remaining := make([]any, 0, len(values)-1)
	for _, value := range values {
		if value["name"] != last(a.identity.NativeID) {
			remaining = append(remaining, cloudNatConfiguration(value))
		}
	}
	parameters := cloneParameters(a.deleteParameters)
	parameters["requestId"] = a.routerComponentRequestID(request)
	parameters["body"] = map[string]any{"nats": remaining}
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	operation, err := a.routerComponentOperationResult(request, response.Data, "", response.RequestID, a.routerComponentDeletePhase())
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.routerComponentStage(request, a.routerComponentDeletePhase(), operation, operation, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
}
