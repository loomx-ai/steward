package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func synapseCancelKind(operation string) synapseDataDefinition {
	switch operation {
	case synapseDataOperationPrefix + "SparkBatch_CancelSparkBatchJob":
		return synapseDataKind(synapseBatchType)
	case synapseDataOperationPrefix + "SparkSession_CancelSparkSession":
		return synapseDataKind(synapseSessionType)
	}
	return synapseDataDefinition{}
}

// Livy's state alone does not establish Synapse cleanup completion: a cancelled
// queued job may remain not_started, while an errored session may still clean up.
// Require the service result and both scheduler/plugin completion observations.
func synapseSparkQuiesced(raw map[string]any) bool {
	switch raw["state"] {
	case "not_started", "starting", "idle", "busy", "shutting_down", "error", "dead", "killed", "success", "running", "recovering":
	default:
		return false
	}
	switch raw["result"] {
	case "Succeeded", "Failed", "Cancelled":
		return object(raw["schedulerInfo"])["currentState"] == "Ended" && object(raw["pluginInfo"])["currentState"] == "Ended"
	}
	return false
}

func synapseSparkIncarnation(raw map[string]any) error {
	submitted, ok := object(raw["schedulerInfo"])["submittedAt"].(string)
	stamp, err := time.Parse(time.RFC3339Nano, submitted)
	if !ok || err != nil || stamp.IsZero() {
		return serviceDenied("synapse_spark_incarnation_missing")
	}
	return nil
}

func synapseCancelReceipt(res response) error {
	// Neither an asynchronous status nor a polling URL is part of this contract.
	if res.status != 200 || len(res.data) != 0 || operationLocation(res.header) != "" || res.header.Get("Location") != "" || res.header.Get("Operation-Location") != "" || res.header.Get("Azure-AsyncOperation") != "" {
		return serviceDenied("invalid_synapse_cancel_receipt")
	}
	return nil
}

func (c *synapseDataClient) synapseCancelProtection(ctx context.Context, target synapseDataTarget, raw map[string]any) error {
	groupID := strings.Join(strings.Split(target.workspace.id, "/")[:5], "/")
	group, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return err
	}
	if !validResourceResponse(group, groupID, groupType) || text(group.data["managedBy"]) != "" {
		return serviceDenied("azure_managed_resource_group")
	}
	for _, value := range []map[string]any{group.data, target.workspace.raw, target.pool, raw} {
		if protectedAzureTags(object(value["tags"])) {
			return serviceDenied("azure_protected_tag")
		}
	}
	locks, err := c.arm.managementLocks(ctx)
	if err != nil {
		return err
	}
	if locked(target.workspace.id, locks) || locked(text(target.pool["id"]), locks) {
		return serviceDenied("azure_management_lock")
	}
	return c.verifySynapseDataTarget(ctx, target)
}

// Native cancellation is an acknowledgement followed by a current observation,
// not an ARM deletion/LRO. Callers may subsequently GET until quiesced or absent;
// a retained historical record must never be reported as deleted.
func (c *synapseDataClient) invokeCancel(ctx context.Context, op catalog.Operation, inv contracts.Invocation) (contracts.InvocationResult, error) {
	d := synapseCancelKind(op.ID)
	if !d.spark || op.Call == nil || op.Call.Method != "DELETE" {
		return contracts.InvocationResult{}, serviceDenied("unsupported_synapse_cancellation")
	}
	bound, err := bindAzureREST(op, inv.Parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	workspace, err := c.arm.synapseWorkspaceForEndpoint(ctx, text(inv.Parameters["endpoint"]))
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	poolID := workspace.id + "/bigDataPools/" + text(inv.Parameters["sparkPoolName"])
	pool, err := c.arm.request(ctx, "GET", apiURL(poolID, synapseVersion))
	if err == nil {
		err = c.arm.synapseReadResponse(pool, poolID, synapseSparkType)
	}
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	target := synapseDataTarget{workspace: workspace, pool: pool.data}
	params := maps.Clone(inv.Parameters)
	params["detailed"] = true
	// Defaults remain owned by the native binder, never by a caller's opaque URL.
	read := func() (response, error) { return c.synapseReadData(ctx, target, d, params) }
	result := func(res response, accepted, absent bool) (contracts.InvocationResult, error) {
		if err := c.verifySynapseDataTarget(ctx, target); err != nil {
			return contracts.InvocationResult{}, contracts.DependencyReadError(err)
		}
		return contracts.InvocationResult{Data: map[string]any{"accepted": accepted, "exists": !absent, "quiesced": !absent && synapseSparkQuiesced(res.data), "observation": safePayload(object(synapseDataSafeValue(res.data)))}, RequestID: res.requestID}, nil
	}
	before, err := read()
	if isNotFound(err) {
		return result(before, false, true)
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if err = synapseSparkIncarnation(before.data); err != nil {
		return contracts.InvocationResult{}, err
	}
	fingerprint := c.arm.privateConfiguration(synapseDataSnapshot(d, before.data))
	if err = c.synapseCancelProtection(ctx, target, before.data); err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	current, err := read()
	if isNotFound(err) {
		return result(current, false, true)
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if fingerprint != c.arm.privateConfiguration(synapseDataSnapshot(d, current.data)) {
		return contracts.InvocationResult{}, serviceDenied("synapse_spark_changed_before_cancel")
	}
	if synapseSparkQuiesced(current.data) {
		return result(current, false, false)
	}
	if inv.IdempotencyKey != "" {
		bound.Headers = maps.Clone(bound.Headers)
		if bound.Headers == nil {
			bound.Headers = map[string]string{}
		}
		bound.Headers["x-ms-client-request-id"] = azureRequestID(inv.IdempotencyKey)
	}
	receipt, err := c.request(ctx, workspace, bound)
	accepted := err == nil
	// A DELETE 404 does not replace the resource's own final readback.
	if err != nil && !isNotFound(err) {
		return contracts.InvocationResult{}, err
	}
	if err == nil {
		if err = synapseCancelReceipt(receipt); err != nil {
			return contracts.InvocationResult{}, err
		}
	}
	after, err := read()
	if isNotFound(err) {
		if receipt.requestID != "" {
			after.requestID = receipt.requestID
		}
		return result(after, accepted, true)
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if fingerprint != c.arm.privateConfiguration(synapseDataSnapshot(d, after.data)) {
		return contracts.InvocationResult{}, serviceDenied("synapse_spark_changed_after_cancel")
	}
	if receipt.requestID != "" {
		after.requestID = receipt.requestID
	}
	return result(after, accepted, false)
}
