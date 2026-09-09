package gcp

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func dataprocJobPhase(request contracts.ActionRequest, phase string) contracts.ActionResult {
	return contracts.ActionResult{Data: map[string]any{"phase": phase, "resource": request.Asset.Identity.NativeID, "uuid": request.Asset.Normalized["jobUuid"], "configuration": request.Asset.Normalized[dataprocProof]}, RetryAfter: 2 * time.Second}
}

func (a *action) prepareDataprocJob(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err = a.dataprocPreflight(request, live); err != nil {
		return contracts.ActionResult{}, err
	}
	terminal, err := dataprocTerminalJob(live)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if terminal {
		result, err := a.delete(ctx, request)
		if err != nil {
			return result, err
		}
		phase := dataprocJobPhase(request, "dataproc_job_delete")
		phase.ProviderRequestID = result.ProviderRequestID
		return phase, nil
	}
	state := text(object(live["status"])["state"])
	result := dataprocJobPhase(request, "dataproc_job_cancel")
	if state == "CANCEL_PENDING" || state == "CANCEL_STARTED" {
		return result, nil
	}
	_, parameters, err := a.client.resourceOperation(a.kind, request.Asset.Identity.NativeID, "GET")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, _ := metadata.catalog.Operation("dataproc.projects.regions.jobs.cancel")
	parameters["body"] = map[string]any{}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err = a.dataprocPreflight(request, response.Data); err != nil {
		return contracts.ActionResult{}, err
	}
	result.ProviderRequestID = response.RequestID
	return result, nil
}

func (a *action) waitDataprocJob(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase := text(result.Data["phase"])
	if (phase != "dataproc_job_cancel" && phase != "dataproc_job_delete") || result.Data["resource"] != request.Asset.Identity.NativeID || text(result.Data["uuid"]) == "" || result.Data["uuid"] != request.Asset.Normalized["jobUuid"] || result.Data["configuration"] != request.Asset.Normalized[dataprocProof] || result.ProviderOperationID != "" {
		return contracts.WaitResult{}, groupDenied("dataproc_job_phase_invalid")
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.WaitResult{Done: true}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err = a.dataprocPreflight(request, live); err != nil {
		return contracts.WaitResult{}, err
	}
	terminal, err := dataprocTerminalJob(live)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !terminal || phase == "dataproc_job_delete" {
		return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
	}
	next, err := a.prepareDataprocJob(ctx, request)
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, err
}

func dataprocMetadata(data map[string]any) map[string]string {
	result := map[string]string{}
	for _, raw := range array(object(data["metadata"])["items"]) {
		item := object(raw)
		key := text(item["key"])
		if key == "dataproc-cluster-uuid" || key == "dataproc-cluster-name" || key == "dataproc-region" {
			result[key] = text(item["value"])
		}
	}
	return result
}
