package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const synapseSparkReview = "_synapse_spark_review"
const synapseSparkProof = "_synapse_spark_proof"

func (r *Runtime) synapseSparkInventory(ctx context.Context, c *client, request contracts.InventoryRequest, item *contracts.InventoryItem) error {
	data, err := r.synapseResolvedClient(request.ConnectionID, c)
	if err != nil {
		return err
	}
	known := map[string]any{}
	if prior := request.KnownNativeMetadata[item.NativeID]; prior != nil && prior[synapseSparkReview] != nil {
		review := object(prior[synapseSparkReview])
		// Old metadata supplies discovery hints, not authority. Fresh scoped
		// GETs validate every selector, including after credential rotation.
		known = object(review["work"])
	}
	target, err := data.sparkTarget(ctx, item.NativeID)
	if err != nil {
		return err
	}
	if item.Normalized["_synapse_private_configuration"] != c.privateConfiguration(synapseSnapshot(target.pool)) {
		return serviceDenied("synapse_spark_inventory_changed")
	}
	review, work, err := data.sparkReview(ctx, target, known)
	if err != nil {
		return err
	}
	item.Normalized[synapseSparkReview], item.Normalized[synapseSparkProof] = review, data.sparkReviewProof(item.NativeID, request.ConnectionID, review)
	if item.Normalized["cleanup_protection_reason"] == "synapse_cleanup_not_implemented" {
		item.Normalized["cleanup_protected"] = false
		delete(item.Normalized, "cleanup_protection_reason")
	}
	if review["protected"] == true {
		item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "synapse_spark_protected_context"
	}
	for _, raw := range []map[string]any{target.pool, target.workspace.raw} {
		if object(raw["properties"])["provisioningState"] != "Succeeded" {
			item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "synapse_spark_context_not_ready"
		}
	}
	for _, v := range work.manifest {
		entry := object(v)
		if entry["references"] == true || entry["unresolved"] == true {
			item.Normalized["cleanup_protected"] = true
			item.Normalized["cleanup_protection_reason"] = "synapse_spark_consumer_exists"
		}
	}
	for _, raw := range work.raw {
		if protectedAzureTags(object(raw["tags"])) {
			item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "azure_protected_tag"
		}
	}
	actionable := item.Normalized["cleanup_protected"] == false
	item.Actionable = &actionable
	return nil
}

type synapseSparkAction struct {
	client  *synapseDataClient
	planned asset.Asset
	id      string
}

func (c *synapseDataClient) sparkReviewProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.arm.privateConfiguration(map[string]any{"protocol": "synapse-spark-review-1", "id": id, "connection": connection, "review": review})
}

func (c *synapseDataClient) sparkTarget(ctx context.Context, id string) (synapseDataTarget, error) {
	canonical, kind, err := parseID(id)
	if err != nil || canonical != id || !strings.EqualFold(kind, synapseSparkType) || !strings.HasPrefix(id, c.arm.root()+"/") || len(strings.Split(id, "/")) != 11 {
		return synapseDataTarget{}, serviceDenied("invalid_synapse_spark_pool_identity")
	}
	res, err := c.arm.request(ctx, "GET", apiURL(id, synapseVersion))
	if err != nil {
		return synapseDataTarget{}, err
	}
	if err = c.arm.synapseReadResponse(res, id, synapseSparkType); err != nil {
		return synapseDataTarget{}, err
	}
	w, err := c.arm.synapseWorkspaceRead(ctx, strings.Join(strings.Split(id, "/")[:9], "/"))
	if err != nil {
		return synapseDataTarget{}, contracts.DependencyReadError(err)
	}
	if resourceRegion(w.raw) != resourceRegion(res.data) {
		return synapseDataTarget{}, serviceDenied("synapse_spark_parent_region_changed")
	}
	return synapseDataTarget{workspace: w, pool: res.data}, nil
}

func (c *synapseDataClient) sparkReview(ctx context.Context, target synapseDataTarget, known map[string]any) (map[string]any, synapseSparkWork, error) {
	groupID := strings.Join(strings.Split(target.workspace.id, "/")[:5], "/")
	group, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, synapseSparkWork{}, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(group, groupID, groupType) {
		return nil, synapseSparkWork{}, serviceDenied("invalid_synapse_spark_group")
	}
	work, err := c.synapseSparkWork(ctx, target, known)
	if err != nil {
		return nil, work, err
	}
	after, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, work, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(after, groupID, groupType) || c.arm.privateConfiguration(synapseSnapshot(after.data)) != c.arm.privateConfiguration(synapseSnapshot(group.data)) {
		return nil, work, serviceDenied("synapse_spark_group_changed")
	}
	protected := text(group.data["managedBy"]) != ""
	for _, raw := range []map[string]any{group.data, target.workspace.raw, target.pool} {
		protected = protected || protectedAzureTags(object(raw["tags"]))
	}
	return map[string]any{"pool": c.arm.privateConfiguration(synapseSnapshot(target.pool)), "workspace": c.arm.privateConfiguration(synapseSnapshot(target.workspace.raw)), "group": c.arm.privateConfiguration(synapseSnapshot(group.data)), "work": work.manifest, "protected": protected}, work, nil
}

func newSynapseSparkAction(c *synapseDataClient, connection asset.ConnectionID, value asset.Asset) (*synapseSparkAction, error) {
	a := &synapseSparkAction{client: c, planned: value, id: value.Identity.NativeID}
	if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != synapseSparkType || value.Identity.ConnectionID != connection || value.Identity.Partition != "azure" || value.ID == "" {
		return nil, serviceDenied("synapse_spark_action_identity_changed")
	}
	if err := a.identity(contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *synapseSparkAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[synapseSparkReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters) != 0 || len(req.LifecycleImpacts) != 0 || len(req.PrerequisiteDeletions) != 0 || review == nil || len(review) != 5 || review["protected"] != false || text(review["pool"]) == "" || text(review["workspace"]) == "" || text(review["group"]) == "" || object(review["work"]) == nil || req.Asset.Normalized[synapseSparkProof] != a.client.sparkReviewProof(a.id, req.Asset.Identity.ConnectionID, review) || req.Asset.Normalized[synapseSparkProof] != a.planned.Normalized[synapseSparkProof] {
		return serviceDenied("synapse_spark_review_changed")
	}
	if req.ExecutionResult != nil {
		result := *req.ExecutionResult
		if !slices.Contains([]string{"cancel", "delete"}, text(result.Data["phase"])) || result.Data["binding"] != a.phaseBinding(req, result) {
			return serviceDenied("synapse_spark_receipt_changed")
		}
	}
	return nil
}

func (a *synapseSparkAction) phaseBinding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.arm.privateConfiguration(map[string]any{"protocol": "synapse-spark-cleanup-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}

// Native readback is solely the pool's own identity. Neither a stopped Spark
// record nor a completed ARM operation grants absence authority over anything.
func (a *synapseSparkAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	res, err := a.client.arm.request(ctx, "GET", apiURL(a.id, synapseVersion))
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err = a.client.arm.synapseReadResponse(res, a.id, synapseSparkType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if object(req.Asset.Normalized[synapseSparkReview])["pool"] != a.client.arm.privateConfiguration(synapseSnapshot(res.data)) {
		return contracts.ReadbackResult{}, serviceDenied("synapse_spark_pool_changed")
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
}

func (a *synapseSparkAction) current(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, synapseDataTarget, synapseSparkWork, error) {
	if err := a.identity(req); err != nil {
		return contracts.PreflightResult{}, synapseDataTarget{}, synapseSparkWork{}, err
	}
	target, err := a.client.sparkTarget(ctx, a.id)
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, target, synapseSparkWork{}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, target, synapseSparkWork{}, err
	}
	for _, raw := range []map[string]any{target.pool, target.workspace.raw} {
		if object(raw["properties"])["provisioningState"] != "Succeeded" {
			return contracts.PreflightResult{}, target, synapseSparkWork{}, serviceDenied("synapse_spark_context_not_ready")
		}
	}
	expected := object(req.Asset.Normalized[synapseSparkReview])
	review, work, err := a.client.sparkReview(ctx, target, object(expected["work"]))
	if err != nil {
		return contracts.PreflightResult{}, target, work, err
	}
	for _, key := range []string{"pool", "workspace", "group"} {
		if review[key] != expected[key] {
			return contracts.PreflightResult{}, target, work, serviceDenied("synapse_spark_context_changed")
		}
	}
	for id, v := range work.manifest {
		entry, old := object(v), object(object(expected["work"])[id])
		if old == nil || a.client.arm.privateConfiguration(entry) != a.client.arm.privateConfiguration(old) {
			return contracts.PreflightResult{}, target, work, serviceDenied("synapse_spark_work_changed")
		}
		if entry["references"] == true || entry["unresolved"] == true {
			return contracts.PreflightResult{}, target, work, serviceDenied("synapse_spark_consumer_exists")
		}
		if protectedAzureTags(object(work.raw[id]["tags"])) {
			return contracts.PreflightResult{}, target, work, serviceDenied("azure_protected_tag")
		}
	}
	if err := a.client.synapseCancelProtection(ctx, target, nil); err != nil {
		return contracts.PreflightResult{}, target, work, contracts.DependencyReadError(err)
	}
	return contracts.PreflightResult{Allowed: true}, target, work, nil
}

func (a *synapseSparkAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	check, _, _, err := a.current(ctx, req)
	return check, err
}

func (a *synapseSparkAction) result(req contracts.ActionRequest, phase, item string, res response, operation map[string]any) contracts.ActionResult {
	result := contracts.ActionResult{ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"accepted": map[string]any{}}}
	if req.ExecutionResult != nil {
		result.ProviderOperationID = req.ExecutionResult.ProviderOperationID
		result.Data = batchClone(req.ExecutionResult.Data)
	}
	result.Data["phase"], result.Data["item"], result.Data["operation"] = phase, item, operation
	result.Data["operation_done"] = false
	object(result.Data["accepted"])[phase+"/"+item] = true
	result.Data["binding"] = a.phaseBinding(req, result)
	return result
}

// Return each acknowledgement before any subsequent network read so the
// worker can persist it. A cancellation receipt is never reused as a DELETE.
func (a *synapseSparkAction) advance(ctx context.Context, req contracts.ActionRequest, target synapseDataTarget, work synapseSparkWork) (contracts.ActionResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	for _, id := range slices.Sorted(maps.Keys(work.manifest)) {
		entry := object(work.manifest[id])
		d := synapseDataKind(text(entry["kind"]))
		if !d.spark || synapseSparkQuiesced(work.raw[id]) {
			continue
		}
		if req.ExecutionResult != nil && object(req.ExecutionResult.Data["accepted"])["cancel/"+id] == true {
			return contracts.ActionResult{}, serviceDenied("synapse_spark_cancelled_work_reactivated")
		}
		name := "SparkBatch_CancelSparkBatchJob"
		if d.kind == synapseSessionType {
			name = "SparkSession_CancelSparkSession"
		}
		op, _ := metadata.catalog.Operation(synapseDataOperationPrefix + name)
		params := maps.Clone(object(entry["parameters"]))
		delete(params, "detailed")
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		bound.Headers = map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":cancel:" + id)}
		res, err := a.client.request(ctx, target.workspace, bound)
		if err != nil && !isNotFound(err) {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if err == nil {
			if err = synapseCancelReceipt(res); err != nil {
				return contracts.ActionResult{}, err
			}
		}
		return a.result(req, "cancel", id, res, nil), nil
	}
	op, _ := metadata.catalog.Operation("Azure.Microsoft.Synapse.BigDataPools_Delete")
	parts := strings.Split(a.id, "/")
	bound, err := bindAzureREST(op, map[string]any{"subscriptionId": a.client.arm.subscription, "resourceGroupName": parts[4], "workspaceName": parts[8], "bigDataPoolName": parts[10]})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	res, err := a.client.arm.requestBody(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + a.id)})
	if isNotFound(err) {
		return a.result(req, "delete", a.id, res, nil), nil
	}
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	receipt, err := a.client.arm.synapseDeleteReceipt(a.id, res)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return a.result(req, "delete", a.id, res, receipt), nil
}

func (a *synapseSparkAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	check, target, work, err := a.current(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if check.Absent {
		return a.result(req, "delete", a.id, response{}, nil), nil
	}
	return a.advance(ctx, req, target, work)
}

func (a *synapseSparkAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: text(result.Data["phase"]), RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if result.Data["phase"] == "delete" {
		if receipt := object(result.Data["operation"]); receipt != nil && result.Data["operation_done"] != true {
			poll, err := a.client.arm.synapsePoll(ctx, a.id, receipt)
			if err != nil {
				return out, err
			}
			out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
			updated := result
			updated.Data = out.Data
			out.Data["binding"] = a.phaseBinding(req, updated)
			if poll.RetryAfter > 0 {
				out.RetryAfter = poll.RetryAfter
			}
			if !poll.Done {
				return out, nil
			}
		}
		read, err := a.Readback(ctx, req)
		out.Done = err == nil && !read.Exists
		return out, err
	}
	check, target, work, err := a.current(ctx, req)
	if err != nil {
		return out, err
	}
	if check.Absent {
		out.Done = true
		return out, nil
	}
	if raw := work.raw[text(result.Data["item"])]; raw != nil && !synapseSparkQuiesced(raw) {
		return out, nil
	}
	next, err := a.advance(ctx, req, target, work)
	if err != nil {
		return out, err
	}
	out.Data, out.State = next.Data, text(next.Data["phase"])
	if next.RetryAfter > 0 {
		out.RetryAfter = next.RetryAfter
	}
	return out, nil
}

func (*synapseSparkAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

var _ contracts.ActionDriver = (*synapseSparkAction)(nil)
