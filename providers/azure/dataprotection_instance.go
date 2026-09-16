package azure

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const dataProtectionInstanceReview = "_data_protection_instance_review"
const dataProtectionInstanceProof = "_data_protection_instance_proof"

func protectionInstanceConfiguration(raw map[string]any) map[string]any {
	result := hybridComputeChildSnapshot(raw)
	props := object(result["properties"])
	for _, key := range []string{"currentProtectionState", "protectionStatus", "protectionErrorDetails"} {
		delete(props, key)
	}
	return result
}
func (c *client) protectionInstanceProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "data-protection-instance-review-1", "id": id, "connection": connection, "review": review})
}
func (c *client) protectionRetainedInstances(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	vault := redisParentID(id)
	rows, err := c.dataProtectionCollection(ctx, vault+"/deletedbackupinstances", dataProtectionDeletedInstance)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for key := range known {
		canonical, err := c.dataProtectionIdentity(key, dataProtectionDeletedInstance)
		if err != nil || key != canonical || redisParentID(key) != vault {
			return nil, serviceDenied("invalid_retained_backup_hint")
		}
		ids[key] = true
	}
	for key := range rows {
		ids[key] = true
	}
	result := map[string]any{}
	for _, key := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.dataProtectionRead(ctx, key, dataProtectionDeletedInstance)
		if isNotFound(err) && rows[key] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if rows[key] != nil && !nativeConfigurationContains(rows[key], own.data) {
			return nil, serviceDenied("retained_backup_changed_during_read")
		}
		props := object(own.data["properties"])
		result[key] = map[string]any{"configuration": c.privateConfiguration(own.data), "source": c.privateConfiguration(object(props["dataSourceInfo"])), "policy": strings.ToLower(text(object(props["policyInfo"])["policyId"]))}
	}
	return result, nil
}
func (c *client) protectionInstanceReview(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	parents, err := c.dataProtectionParents(ctx, id, dataProtectionInstance)
	if err != nil {
		return nil, err
	}
	own, err := c.dataProtectionRead(ctx, id, dataProtectionInstance)
	if err != nil {
		return nil, err
	}
	props := object(own.data["properties"])
	policyID := strings.ToLower(text(object(props["policyInfo"])["policyId"]))
	policy, err := c.dataProtectionRead(ctx, policyID, dataProtectionPolicy)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	retained, err := c.protectionRetainedInstances(ctx, id, known)
	if err != nil {
		return nil, err
	}
	later, err := c.protectionRetainedInstances(ctx, id, retained)
	if err != nil {
		return nil, err
	}
	after, err := c.dataProtectionParents(ctx, id, dataProtectionInstance)
	if err != nil {
		return nil, err
	}
	current, err := c.dataProtectionRead(ctx, id, dataProtectionInstance)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	currentPolicy, err := c.dataProtectionRead(ctx, policyID, dataProtectionPolicy)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(parents) != c.privateConfiguration(after) || c.privateConfiguration(own.data) != c.privateConfiguration(current.data) || c.privateConfiguration(policy.data) != c.privateConfiguration(currentPolicy.data) || c.privateConfiguration(retained) != c.privateConfiguration(later) {
		return nil, serviceDenied("backup_instance_changed_during_review")
	}
	parents["observed"] = c.privateConfiguration(own.data)
	parents["configuration"] = c.privateConfiguration(protectionInstanceConfiguration(own.data))
	parents["policy_id"], parents["policy_configuration"] = policyID, c.privateConfiguration(hybridComputeChildSnapshot(policy.data))
	parents["source"] = c.privateConfiguration(object(props["dataSourceInfo"]))
	parents["retained"] = retained
	parents["protected"] = parents["protected"] == true || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
	parents["ready"] = parents["ready"] == true && props["objectType"] == "BackupInstance" && slices.Contains([]string{"NotProtected", "ProtectionConfigured", "BackupSchedulesSuspended", "RetentionSchedulesSuspended", "ProtectionStopped", "ProtectionError", "ConfiguringProtectionFailed"}, text(props["currentProtectionState"])) && (props["provisioningState"] == nil || props["provisioningState"] == "Succeeded" || props["provisioningState"] == "Failed")
	return parents, nil
}
func (r *Runtime) protectionInstanceInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	known := object(object(req.KnownNativeMetadata[item.NativeID][dataProtectionInstanceReview])["retained"])
	review, err := c.protectionInstanceReview(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if review["observed"] != item.Normalized["_data_protection_configuration"] || review["parent_observed"] != item.Normalized["_data_protection_parent_configuration"] || review["region"] != item.Location {
		return serviceDenied("backup_instance_inventory_changed")
	}
	item.Normalized[dataProtectionInstanceReview] = review
	item.Normalized[dataProtectionInstanceProof] = c.protectionInstanceProof(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	} else {
		item.Normalized["cleanup_protection_reason"] = "backup_instance_not_ready_or_protected"
	}
	return nil
}

type protectionInstanceAction struct {
	client  *client
	planned asset.Asset
}

func newProtectionInstanceAction(c *client, connection asset.ConnectionID, value asset.Asset) (*protectionInstanceAction, error) {
	id, err := c.dataProtectionIdentity(value.Identity.NativeID, dataProtectionInstance)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.NativeType != dataProtectionInstance || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_backup_instance_action")
	}
	a := &protectionInstanceAction{client: c, planned: value}
	return a, a.identity(contracts.ActionRequest{Asset: value, Action: "delete"})
}
func (a *protectionInstanceAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "data-protection-instance-delete-1", "request": req, "data": data, "operation": result.ProviderOperationID})
}
func (a *protectionInstanceAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[dataProtectionInstanceReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 12 || review["region"] != req.Asset.Location || review["observed"] != req.Asset.Normalized["_data_protection_configuration"] || review["parent_observed"] != req.Asset.Normalized["_data_protection_parent_configuration"] || review["ready"] != true || review["protected"] != false || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[dataProtectionInstanceProof] != a.planned.Normalized[dataProtectionInstanceProof] || req.Asset.Normalized[dataProtectionInstanceProof] != a.client.protectionInstanceProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("backup_instance_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("backup_instance_receipt_changed")
	}
	return nil
}
func (a *protectionInstanceAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id := a.planned.Identity.NativeID
	expected := object(req.Asset.Normalized[dataProtectionInstanceReview])
	parents, err := a.client.dataProtectionParents(ctx, id, dataProtectionInstance)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, key := range []string{"vault", "group", "region"} {
		if parents[key] != expected[key] {
			return contracts.ReadbackResult{}, serviceDenied("backup_instance_parent_changed")
		}
	}
	if parents["protected"] != false || parents["ready"] != true {
		return contracts.ReadbackResult{}, serviceDenied("backup_instance_parent_unavailable")
	}
	own, err := a.client.dataProtectionRead(ctx, id, dataProtectionInstance)
	if err == nil {
		if a.client.privateConfiguration(protectionInstanceConfiguration(own.data)) != expected["configuration"] {
			return contracts.ReadbackResult{}, serviceDenied("backup_instance_configuration_changed")
		}
		return contracts.ReadbackResult{Exists: true, State: text(object(own.data["properties"])["currentProtectionState"])}, nil
	}
	if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	retained, err := a.client.protectionRetainedInstances(ctx, id, object(expected["retained"]))
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	after, err := a.client.dataProtectionParents(ctx, id, dataProtectionInstance)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.client.privateConfiguration(parents) != a.client.privateConfiguration(after) {
		return contracts.ReadbackResult{}, serviceDenied("backup_instance_parent_changed_during_read")
	}
	_, err = a.client.dataProtectionRead(ctx, id, dataProtectionInstance)
	if !isNotFound(err) {
		if err == nil {
			err = serviceDenied("backup_instance_reappeared")
		}
		return contracts.ReadbackResult{}, err
	}
	state := "active_absent"
	matches := []string{}
	for key, value := range retained {
		v := object(value)
		if v["source"] == expected["source"] && v["policy"] == expected["policy_id"] {
			matches = append(matches, key)
		}
	}
	slices.Sort(matches)
	if slices.Contains(matches, redisParentID(id)+"/deletedbackupinstances/"+last(id)) {
		state = "soft_deleted"
	}
	return contracts.ReadbackResult{Exists: false, State: state, Data: map[string]any{"retained_backup_ids": matches, "permanent_purge_verified": false}}, nil
}
func (a *protectionInstanceAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.Readback(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !read.Exists {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	review, err := a.client.protectionInstanceReview(ctx, a.planned.Identity.NativeID, object(object(req.Asset.Normalized[dataProtectionInstanceReview])["retained"]))
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	expected := object(req.Asset.Normalized[dataProtectionInstanceReview])
	for _, key := range []string{"vault", "group", "region", "configuration", "policy_id", "policy_configuration", "source"} {
		if review[key] != expected[key] {
			return contracts.PreflightResult{}, serviceDenied("backup_instance_context_changed")
		}
	}
	if review["ready"] != true || review["protected"] != false {
		return contracts.PreflightResult{}, serviceDenied("backup_instance_not_deletable")
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *protectionInstanceAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	pre, err := a.Preflight(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := contracts.ActionResult{Data: map[string]any{"already_absent": pre.Absent}}
	if !pre.Absent {
		kind, _ := findType(dataProtectionInstance)
		op, params, err := a.client.resourceOperation(kind, a.planned.Identity.NativeID, "DELETE")
		if err != nil {
			return result, err
		}
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return result, err
		}
		transport := *a.client.http
		base := transport.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		ack := &synapseRestoreAckTransport{base: base}
		transport.Transport = ack
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + a.planned.Identity.NativeID)}, a.client.validateURL, &transport, false)
		if err != nil {
			return result, err
		}
		receipt, err := a.client.dataProtectionDeleteReceipt(a.planned.Identity.NativeID, a.planned.Location, res, ack.empty)
		if err != nil {
			return result, err
		}
		result.Data["operation"] = receipt
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
	}
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}
func (a *protectionInstanceAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if result.Data["already_absent"] != true && result.Data["operation_done"] != true {
		poll, err := a.client.dataProtectionPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, object(result.Data["operation"]))
		if err != nil {
			return out, err
		}
		out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done, out.State = err == nil && !read.Exists, read.State
	if out.Done {
		out.Data["retention"] = read.Data
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
	}
	return out, err
}
func (*protectionInstanceAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
