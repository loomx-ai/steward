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

const synapseSQLReview = "_synapse_sql_review"
const synapseSQLProof = "_synapse_sql_proof"

func (c *client) sqlReviewProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "synapse-sql-review-1", "id": id, "connection": connection, "review": review})
}

// Replication links describe independently managed peers. A pool-only action
// cannot authorize their removal, including links on later native pages.
func (c *client) sqlReplication(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	collection := id + "/replicationLinks"
	next := apiURL(collection, synapseVersion)
	seen := map[string]bool{}
	links := map[string]any{}
	for next != "" {
		if seen[next] {
			return nil, serviceDenied("synapse_sql_replication_page_cycle")
		}
		seen[next] = true
		if err := c.synapseListQuery(next, collection); err != nil {
			return nil, err
		}
		rows, following, res, err := c.listPageResult(ctx, next, collection)
		if err != nil {
			return nil, err
		}
		if res.status != 200 || operationLocation(res.header) != "" {
			return nil, serviceDenied("synapse_sql_replication_incomplete")
		}
		if following != "" {
			if err := c.synapseListQuery(following, collection); err != nil {
				return nil, err
			}
		}
		for _, v := range rows {
			raw := object(v)
			child, kind, err := parseID(text(raw["id"]))
			if err != nil || kind != strings.ToLower(synapseSQLType+"/replicationLinks") || redisParentID(child) != id || !strings.EqualFold(text(raw["type"]), kind) || !strings.EqualFold(text(raw["name"]), last(child)) || object(raw["properties"]) == nil || links[child] != nil {
				return nil, serviceDenied("invalid_synapse_sql_replication_link")
			}
			own, err := c.request(ctx, "GET", apiURL(child, synapseVersion))
			if err != nil {
				return nil, contracts.DependencyReadError(err)
			}
			if !validResourceResponse(own, child, synapseSQLType+"/replicationLinks") || operationLocation(own.header) != "" || !nativeConfigurationContains(synapseSnapshot(raw), synapseSnapshot(own.data)) {
				return nil, serviceDenied("synapse_sql_replication_changed")
			}
			links[child] = c.privateConfiguration(synapseSnapshot(own.data))
		}
		next = following
	}
	for child := range known {
		canonical, kind, err := parseID(child)
		if err != nil || child != canonical || kind != strings.ToLower(synapseSQLType+"/replicationLinks") || redisParentID(child) != id {
			return nil, serviceDenied("synapse_sql_known_replication_scope_changed")
		}
		if links[child] != nil {
			continue
		}
		own, err := c.request(ctx, "GET", apiURL(child, synapseVersion))
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if !validResourceResponse(own, child, synapseSQLType+"/replicationLinks") || operationLocation(own.header) != "" || !strings.EqualFold(text(own.data["name"]), last(child)) || object(own.data["properties"]) == nil {
			return nil, serviceDenied("invalid_synapse_sql_known_replication")
		}
		links[child] = c.privateConfiguration(synapseSnapshot(own.data))
	}
	return links, nil
}
func (c *client) sqlReview(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	canonical, kind, err := parseID(id)
	if err != nil || canonical != id || kind != strings.ToLower(synapseSQLType) || len(strings.Split(id, "/")) != 11 || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("invalid_synapse_sql_identity")
	}
	pool, err := c.request(ctx, "GET", apiURL(id, synapseVersion))
	if err != nil {
		return nil, err
	}
	if err = c.synapseReadResponse(pool, id, synapseSQLType); err != nil {
		return nil, err
	}
	workspaceID := redisParentID(id)
	workspace, err := c.synapseWorkspaceRead(ctx, workspaceID)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	group, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(group, groupID, groupType) || resourceRegion(pool.data) != resourceRegion(workspace.raw) {
		return nil, serviceDenied("synapse_sql_parent_changed")
	}
	links, err := c.sqlReplication(ctx, id, known)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	again, err := c.sqlReplication(ctx, id, known)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(links) != c.privateConfiguration(again) {
		return nil, serviceDenied("synapse_sql_replication_changed")
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	protected := text(group.data["managedBy"]) != "" || locked(id, locks) || locked(workspaceID, locks)
	for _, raw := range []map[string]any{group.data, workspace.raw, pool.data} {
		protected = protected || protectedAzureTags(object(raw["tags"]))
	}
	creation, e := time.Parse(time.RFC3339Nano, text(object(pool.data["properties"])["creationDate"]))
	ready := object(pool.data["properties"])["provisioningState"] == "Succeeded" && e == nil && !creation.IsZero() && text(object(workspace.raw["properties"])["workspaceUID"]) != "" && object(workspace.raw["properties"])["provisioningState"] == "Succeeded" && slices.Contains([]string{"Online", "Paused"}, text(object(pool.data["properties"])["status"]))
	// Recheck the actual pool and both parents after dependency observations.
	for _, pair := range []struct {
		id, kind string
		raw      map[string]any
	}{{id, synapseSQLType, pool.data}, {workspaceID, synapseType, workspace.raw}, {groupID, groupType, group.data}} {
		version := synapseVersion
		if pair.kind == groupType {
			version = resourcesVersion
		}
		own, err := c.request(ctx, "GET", apiURL(pair.id, version))
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if pair.kind == synapseSQLType {
			ready = ready && object(own.data["properties"])["provisioningState"] == "Succeeded" && slices.Contains([]string{"Online", "Paused"}, text(object(own.data["properties"])["status"]))
		}
		if pair.kind == synapseType {
			ready = ready && object(own.data["properties"])["provisioningState"] == "Succeeded"
		}
		if !validResourceResponse(own, pair.id, pair.kind) || operationLocation(own.header) != "" || c.privateConfiguration(synapseSnapshot(own.data)) != c.privateConfiguration(synapseSnapshot(pair.raw)) {
			return nil, serviceDenied("synapse_sql_context_changed")
		}
	}
	return map[string]any{"pool": c.privateConfiguration(synapseSnapshot(pool.data)), "workspace": c.privateConfiguration(synapseSnapshot(workspace.raw)), "group": c.privateConfiguration(synapseSnapshot(group.data)), "links": links, "protected": protected, "ready": ready}, nil
}
func (r *Runtime) synapseSQLInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	review, err := c.sqlReview(ctx, item.NativeID, object(object(req.KnownNativeMetadata[item.NativeID][synapseSQLReview])["links"]))
	if err != nil {
		return err
	}
	if review["pool"] != item.Normalized["_synapse_private_configuration"] {
		return serviceDenied("synapse_sql_inventory_changed")
	}
	item.Normalized[synapseSQLReview], item.Normalized[synapseSQLProof] = review, c.sqlReviewProof(item.NativeID, req.ConnectionID, review)
	if item.Normalized["cleanup_protection_reason"] == "synapse_cleanup_not_implemented" {
		delete(item.Normalized, "cleanup_protection_reason")
		delete(item.Normalized, "cleanup_controller_only")
		item.Normalized["cleanup_protected"] = false
	}
	if review["protected"] != false || review["ready"] != true {
		item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "synapse_sql_context_not_ready"
	}
	actionable := item.Normalized["cleanup_protected"] == false && len(object(review["links"])) == 0
	if !actionable && item.Normalized["cleanup_protected"] == false {
		item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "synapse_sql_replication_exists"
	}
	item.Actionable = &actionable
	return nil
}

type synapseSQLAction struct {
	client  *client
	planned asset.Asset
}

func newSynapseSQLAction(c *client, connection asset.ConnectionID, v asset.Asset) (*synapseSQLAction, error) {
	a := &synapseSQLAction{client: c, planned: v}
	id, kind, err := parseID(v.Identity.NativeID)
	if err != nil || id != v.Identity.NativeID || kind != strings.ToLower(synapseSQLType) || len(strings.Split(id, "/")) != 11 || !strings.HasPrefix(id, c.root()+"/") || v.Identity.Provider != asset.ProviderAzure || v.Identity.NativeType != synapseSQLType || v.Identity.Partition != "azure" || v.Identity.ConnectionID != connection || v.ID == "" {
		return nil, serviceDenied("synapse_sql_action_identity_changed")
	}
	if err = a.identity(contracts.ActionRequest{Asset: v, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *synapseSQLAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "synapse-sql-action-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}
func (a *synapseSQLAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[synapseSQLReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 6 || review["pool"] != req.Asset.Normalized["_synapse_private_configuration"] || review["protected"] != false || review["ready"] != true || object(review["links"]) == nil || len(object(review["links"])) != 0 || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[synapseSQLProof] != a.planned.Normalized[synapseSQLProof] || req.Asset.Normalized[synapseSQLProof] != a.client.sqlReviewProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("synapse_sql_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("synapse_sql_receipt_changed")
	}
	return nil
}
func (a *synapseSQLAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.PreflightResult{}, err
	}
	fresh, err := a.client.sqlReview(ctx, a.planned.Identity.NativeID, object(object(req.Asset.Normalized[synapseSQLReview])["links"]))
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if a.client.privateConfiguration(fresh) != a.client.privateConfiguration(object(req.Asset.Normalized[synapseSQLReview])) {
		return contracts.PreflightResult{}, serviceDenied("synapse_sql_context_changed")
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *synapseSQLAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
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
	out := contracts.ActionResult{Data: map[string]any{"operation_done": false}}
	if !pre.Absent {
		data, err := providerData()
		if err != nil {
			return out, err
		}
		op, _ := data.catalog.Operation("Azure.Microsoft.Synapse.SqlPools_Delete")
		id := a.planned.Identity.NativeID
		parts := strings.Split(id, "/")
		bound, err := bindAzureREST(op, map[string]any{"subscriptionId": a.client.subscription, "resourceGroupName": parts[4], "workspaceName": parts[8], "sqlPoolName": parts[10]})
		if err != nil {
			return out, err
		}
		res, err := a.client.requestBody(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + id)})
		if err != nil && !isNotFound(err) {
			return out, err
		}
		out.ProviderRequestID, out.RetryAfter = res.requestID, retryAfter(res.header)
		if err == nil {
			receipt, err := a.client.synapseDeleteReceipt(id, res)
			if err != nil {
				return out, err
			}
			out.Data["operation"] = receipt
		}
	}
	out.Data["binding"] = a.binding(req, out)
	return out, nil
}
func (a *synapseSQLAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id := a.planned.Identity.NativeID
	res, err := a.client.request(ctx, "GET", apiURL(id, synapseVersion))
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err = a.client.synapseReadResponse(res, id, synapseSQLType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.client.privateConfiguration(synapseSnapshot(res.data)) != object(req.Asset.Normalized[synapseSQLReview])["pool"] {
		return contracts.ReadbackResult{}, serviceDenied("synapse_sql_pool_recreated_or_changed")
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["status"])}, nil
}
func (a *synapseSQLAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if receipt := object(result.Data["operation"]); receipt != nil && result.Data["operation_done"] != true {
		poll, err := a.client.synapsePoll(ctx, a.planned.Identity.NativeID, receipt)
		if err != nil {
			if ctx.Err() != nil {
				return out, err
			}
			read, ownErr := a.Readback(ctx, req)
			if ownErr == nil && !read.Exists {
				out.Done = true
				return out, nil
			}
			return out, err
		}
		out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done = err == nil && !read.Exists
	return out, err
}
func (*synapseSQLAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
