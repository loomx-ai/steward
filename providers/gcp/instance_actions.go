package gcp

import (
	"context"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) prepareInstance(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	retained, reason, err := a.plannedDisks(request, live)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if reason != "" {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: reason, Message: contracts.SafeProviderValidationMessage}}
	}
	operationID, suffix := "", ""
	parameters := map[string]any{}
	for key, value := range a.deleteParameters {
		parameters[key] = value
	}
	if len(retained) > 0 {
		operationID, suffix = "compute.instances.setDiskAutoDelete", "retain:"+retained[0].device
		parameters["deviceName"], parameters["autoDelete"] = retained[0].device, false
	} else if live["deletionProtection"] == true {
		operationID, suffix = "compute.instances.setDeletionProtection", "deletion-protection"
		parameters["resource"] = parameters["instance"]
		delete(parameters, "instance")
		parameters["deletionProtection"] = false
	} else {
		return a.delete(ctx, request)
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok || operation.Call == nil {
		return contracts.ActionResult{}, fmt.Errorf("GCP instance preparation operation is unavailable")
	}
	if token := operation.Call.IdempotencyParameter; token != "" && request.IdempotencyKey != "" {
		parameters[token] = googleRequestID(request.IdempotencyKey + ":" + suffix)
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(response.Data, response.RequestID); err != nil {
		return contracts.ActionResult{}, err
	}
	poll, err := a.operationURL(response.Data)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderOperationID: poll, ProviderRequestID: response.RequestID, Data: map[string]any{"phase": "prepare_instance", "operation": poll, "change": suffix}, RetryAfter: 2 * time.Second}, nil
}

func (a *action) instancePreparationApplied(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (bool, error) {
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, reason, err := a.plannedDisks(request, live); reason != "" || err != nil {
		if err != nil {
			return false, err
		}
		return false, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: reason, Message: contracts.SafeProviderValidationMessage}}
	}
	change := text(result.Data["change"])
	if change == "deletion-protection" {
		return live["deletionProtection"] != true, nil
	}
	disks, err := instanceDisks(a.client, live)
	if err != nil {
		return false, err
	}
	for _, disk := range disks {
		if change == "retain:"+disk.device {
			return !disk.autoDelete, nil
		}
	}
	return false, fmt.Errorf("GCP preparation change no longer matches an attachment")
}
