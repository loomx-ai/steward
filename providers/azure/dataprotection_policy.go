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

const dataProtectionPolicyReview = "_data_protection_policy_review"
const dataProtectionPolicyProof = "_data_protection_policy_proof"

func (c *client) protectionPolicyProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "data-protection-policy-review-1", "id": id, "connection": connection, "review": review})
}

// Known consumers are read hints, never proof of absence or permission to alter
// backups. Both active and soft-deleted instances retain their policy reference.
func (c *client) protectionPolicyConsumers(ctx context.Context, policy string, known map[string]any) (map[string]any, error) {
	vault := redisParentID(policy)
	ids := map[string]string{}
	listed := map[string]map[string]any{}
	for id, value := range known {
		kind := text(object(value)["kind"])
		canonical, err := c.dataProtectionIdentity(id, kind)
		if err != nil || canonical != id || redisParentID(id) != vault || kind != dataProtectionInstance && kind != dataProtectionDeletedInstance {
			return nil, serviceDenied("invalid_backup_policy_consumer_hint")
		}
		ids[id] = kind
	}
	for _, kind := range []string{dataProtectionInstance, dataProtectionDeletedInstance} {
		rows, err := c.dataProtectionCollection(ctx, vault+"/"+strings.ToLower(last(kind)), kind)
		if err != nil {
			return nil, err
		}
		for id, raw := range rows {
			ids[id] = kind
			listed[id] = raw
		}
	}
	consumers := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.dataProtectionRead(ctx, id, ids[id])
		if isNotFound(err) && listed[id] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if listed[id] != nil && !nativeConfigurationContains(listed[id], own.data) {
			return nil, serviceDenied("backup_policy_consumer_changed")
		}
		if strings.EqualFold(text(object(object(own.data["properties"])["policyInfo"])["policyId"]), policy) {
			consumers[id] = map[string]any{"kind": ids[id], "configuration": c.privateConfiguration(hybridComputeChildSnapshot(own.data))}
		}
	}
	return consumers, nil
}

func (c *client) protectionPolicyParents(ctx context.Context, id string) (map[string]any, error) {
	canonical, err := c.dataProtectionIdentity(id, dataProtectionPolicy)
	if err != nil || id != canonical {
		return nil, serviceDenied("invalid_backup_policy_identity")
	}
	vault, err := c.dataProtectionRead(ctx, redisParentID(id), dataProtectionVault)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	group, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(group, groupID, groupType) || operationLocation(group.header) != "" {
		return nil, serviceDenied("invalid_backup_policy_group")
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	protected := locked(id, locks)
	for _, raw := range []map[string]any{vault.data, group.data} {
		protected = protected || protectedAzureTags(object(raw["tags"])) || text(raw["managedBy"]) != ""
	}
	groupSnapshot := hybridComputeChildSnapshot(group.data)
	groupSnapshot["type"] = strings.ToLower(groupType)
	return map[string]any{"vault": c.privateConfiguration(hybridComputeChildSnapshot(vault.data)), "parent_observed": c.privateConfiguration(vault.data), "group": c.privateConfiguration(groupSnapshot), "region": resourceRegion(vault.data), "protected": protected, "ready": object(vault.data["properties"])["provisioningState"] == "Succeeded"}, nil
}
func (c *client) protectionPolicyReview(ctx context.Context, id string, known map[string]any) (map[string]any, bool, error) {
	parents, err := c.protectionPolicyParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	own, err := c.dataProtectionRead(ctx, id, dataProtectionPolicy)
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, false, err
	}
	consumers, err := c.protectionPolicyConsumers(ctx, id, known)
	if err != nil {
		return nil, false, err
	}
	hints := maps.Clone(known)
	if hints == nil {
		hints = map[string]any{}
	}
	maps.Copy(hints, consumers)
	later, err := c.protectionPolicyConsumers(ctx, id, hints)
	if err != nil {
		return nil, false, err
	}
	after, err := c.protectionPolicyParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if c.privateConfiguration(parents) != c.privateConfiguration(after) || c.privateConfiguration(consumers) != c.privateConfiguration(later) {
		return nil, false, serviceDenied("backup_policy_context_changed_during_read")
	}
	current, err := c.dataProtectionRead(ctx, id, dataProtectionPolicy)
	if err != nil && !isNotFound(err) {
		return nil, false, err
	}
	if absent != isNotFound(err) {
		return nil, false, serviceDenied("backup_policy_changed_during_read")
	}
	parents["consumers"] = consumers
	parents["observed"], parents["configuration"] = "", ""
	if !absent {
		configuration := c.privateConfiguration(hybridComputeChildSnapshot(own.data))
		if configuration != c.privateConfiguration(hybridComputeChildSnapshot(current.data)) {
			return nil, false, serviceDenied("backup_policy_changed_during_read")
		}
		props := object(own.data["properties"])
		_, rules := props["policyRules"].([]any)
		parents["ready"] = parents["ready"] == true && props["objectType"] == "BackupPolicy" && rules
		parents["protected"] = parents["protected"] == true || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
		parents["observed"], parents["configuration"] = c.privateConfiguration(own.data), configuration
	}
	return parents, absent, nil
}
func (r *Runtime) protectionPolicyInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	known := object(object(req.KnownNativeMetadata[item.NativeID][dataProtectionPolicyReview])["consumers"])
	review, absent, err := c.protectionPolicyReview(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if absent || review["observed"] != item.Normalized["_data_protection_configuration"] || review["parent_observed"] != item.Normalized["_data_protection_parent_configuration"] || review["region"] != item.Location {
		return serviceDenied("backup_policy_inventory_changed")
	}
	item.Normalized[dataProtectionPolicyReview] = review
	item.Normalized[dataProtectionPolicyProof] = c.protectionPolicyProof(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false && len(object(review["consumers"])) == 0
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	reason := "backup_policy_not_ready"
	if len(object(review["consumers"])) != 0 {
		reason = "backup_policy_in_use"
	}
	if review["protected"] == true {
		reason = "backup_policy_protected"
	}
	item.Normalized["cleanup_protection_reason"] = reason
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}

type protectionPolicyAction struct {
	client  *client
	planned asset.Asset
}

func newProtectionPolicyAction(c *client, connection asset.ConnectionID, value asset.Asset) (*protectionPolicyAction, error) {
	id, err := c.dataProtectionIdentity(value.Identity.NativeID, dataProtectionPolicy)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.NativeType != dataProtectionPolicy || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_backup_policy_action")
	}
	a := &protectionPolicyAction{client: c, planned: value}
	if err = a.identity(contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *protectionPolicyAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "data-protection-policy-delete-1", "request": req, "data": data, "operation": result.ProviderOperationID})
}
func (a *protectionPolicyAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[dataProtectionPolicyReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 9 || review["region"] != req.Asset.Location || review["observed"] != req.Asset.Normalized["_data_protection_configuration"] || review["parent_observed"] != req.Asset.Normalized["_data_protection_parent_configuration"] || review["ready"] != true || review["protected"] != false || len(object(review["consumers"])) != 0 || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[dataProtectionPolicyProof] != a.planned.Normalized[dataProtectionPolicyProof] || req.Asset.Normalized[dataProtectionPolicyProof] != a.client.protectionPolicyProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("backup_policy_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("backup_policy_receipt_changed")
	}
	return nil
}
func (a *protectionPolicyAction) current(ctx context.Context, req contracts.ActionRequest) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	expected := object(req.Asset.Normalized[dataProtectionPolicyReview])
	review, absent, err := a.client.protectionPolicyReview(ctx, a.planned.Identity.NativeID, object(expected["consumers"]))
	if err != nil {
		return false, err
	}
	for _, key := range []string{"vault", "group", "region"} {
		if review[key] != expected[key] {
			return false, serviceDenied("backup_policy_parent_changed")
		}
	}
	if review["ready"] != true || review["protected"] != false || len(object(review["consumers"])) != 0 {
		return false, serviceDenied("backup_policy_not_deletable")
	}
	if !absent && review["configuration"] != expected["configuration"] {
		return false, serviceDenied("backup_policy_configuration_changed")
	}
	return absent, nil
}
func (a *protectionPolicyAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}
func (a *protectionPolicyAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := contracts.ActionResult{Data: map[string]any{"accepted_status": 0}}
	if !absent {
		kind, _ := findType(dataProtectionPolicy)
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
		// Reuse the synchronous native acknowledgement reader without mutating the
		// client's shared transport. This operation has no asynchronous response.
		ack := &synapseRestoreAckTransport{base: base}
		transport.Transport = ack
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + a.planned.Identity.NativeID)}, a.client.validateURL, &transport, false)
		if err != nil {
			return result, err
		}
		if !slices.Contains([]int{200, 204}, res.status) || !ack.empty || operationLocation(res.header) != "" {
			return result, serviceDenied("invalid_backup_policy_acknowledgement")
		}
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		result.Data["accepted_status"] = res.status
	}
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}
func (a *protectionPolicyAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: !absent}, nil
}
func (a *protectionPolicyAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	read, err := a.Readback(ctx, req)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}, err
}
func (*protectionPolicyAction) DeletionCheckTimeout() time.Duration { return time.Hour }
