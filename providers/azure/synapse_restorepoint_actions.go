package azure

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const synapseRestoreReview = "_synapse_restore_point_review"
const synapseRestoreProof = "_synapse_restore_point_proof"

func synapseUserRestorePoint(raw map[string]any) bool {
	props := object(raw["properties"])
	stamp, err := time.Parse(time.RFC3339Nano, text(props["restorePointCreationDate"]))
	return props["restorePointType"] == "DISCRETE" && strings.TrimSpace(text(props["restorePointLabel"])) != "" && err == nil && !stamp.IsZero()
}
func (c *client) restorePointProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "synapse-restore-point-review-1", "id": id, "connection": connection, "review": review})
}
func (c *client) restorePointParents(ctx context.Context, id string) (map[string]any, bool, bool, error) {
	if err := c.backupIdentity(id, synapseRestorePointType); err != nil {
		return nil, false, false, err
	}
	parts := strings.Split(id, "/")
	contextHashes := map[string]any{}
	raws := map[string]map[string]any{}
	for _, p := range []struct{ key, id, kind, version string }{{"workspace", strings.Join(parts[:9], "/"), synapseType, synapseVersion}, {"pool", redisParentID(id), synapseSQLType, synapseVersion}, {"group", strings.Join(parts[:5], "/"), groupType, resourcesVersion}} {
		own, err := c.request(ctx, "GET", apiURL(p.id, p.version))
		if err != nil {
			return nil, false, false, contracts.DependencyReadError(err)
		}
		if !validResourceResponse(own, p.id, p.kind) || operationLocation(own.header) != "" {
			return nil, false, false, serviceDenied("invalid_synapse_restore_point_parent")
		}
		if p.kind != groupType {
			if err = c.synapseReadResponse(own, p.id, p.kind); err != nil {
				return nil, false, false, err
			}
		}
		contextHashes[p.key] = c.privateConfiguration(synapseSnapshot(own.data))
		raws[p.key] = own.data
	}
	if resourceRegion(raws["pool"]) != resourceRegion(raws["workspace"]) {
		return nil, false, false, serviceDenied("synapse_restore_point_parent_region_changed")
	}
	contextHashes["region"] = resourceRegion(raws["pool"])
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, false, false, contracts.DependencyReadError(err)
	}
	protected := locked(id, locks) || text(raws["group"]["managedBy"]) != ""
	for _, raw := range raws {
		protected = protected || protectedAzureTags(object(raw["tags"]))
	}
	pool, workspace := object(raws["pool"]["properties"]), object(raws["workspace"]["properties"])
	stamp, err := time.Parse(time.RFC3339Nano, text(pool["creationDate"]))
	ready := err == nil && !stamp.IsZero() && text(workspace["workspaceUID"]) != "" && workspace["provisioningState"] == "Succeeded" && pool["provisioningState"] == "Succeeded" && slices.Contains([]string{"Online", "Paused"}, text(pool["status"]))
	return contextHashes, protected, ready, nil
}
func (c *client) restorePointReview(ctx context.Context, id string) (map[string]any, bool, error) {
	parents, protected, ready, err := c.restorePointParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	own, err := c.backupRead(ctx, id, synapseRestorePointType)
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, false, err
	}
	after, protectedAfter, readyAfter, err := c.restorePointParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if c.privateConfiguration(parents) != c.privateConfiguration(after) {
		return nil, false, serviceDenied("synapse_restore_point_parent_changed")
	}
	// A disappearing/deactivated lookup scope is not absence evidence for a
	// retained backup, even if its ARM parent has not vanished yet.
	if absent && (!ready || !readyAfter) {
		return nil, false, serviceDenied("synapse_restore_point_lookup_unavailable")
	}
	if absent {
		return parents, true, nil
	}
	if resourceRegion(own.data) != parents["region"] {
		return nil, false, serviceDenied("synapse_restore_point_region_changed")
	}
	parents["resource"] = c.privateConfiguration(synapseSnapshot(own.data))
	parents["protected"] = protected || protectedAfter || protectedAzureTags(object(own.data["tags"]))
	parents["ready"] = ready && readyAfter
	parents["user_defined"] = synapseUserRestorePoint(own.data)
	return parents, false, nil
}
func (r *Runtime) restorePointInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	review, absent, err := c.restorePointReview(ctx, item.NativeID)
	if err != nil {
		return err
	}
	if absent || review["resource"] != item.Normalized["_synapse_backup_configuration"] {
		return serviceDenied("synapse_restore_point_inventory_changed")
	}
	item.Normalized[synapseRestoreReview], item.Normalized[synapseRestoreProof] = review, c.restorePointProof(item.NativeID, req.ConnectionID, review)
	actionable := review["protected"] == false && review["ready"] == true && review["user_defined"] == true
	item.Normalized["cleanup_protected"] = !actionable
	item.Actionable = &actionable
	if actionable {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}

type synapseRestorePointAction struct {
	client  *client
	planned asset.Asset
}

func newSynapseRestorePointAction(c *client, connection asset.ConnectionID, value asset.Asset) (*synapseRestorePointAction, error) {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != synapseRestorePointType || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_synapse_restore_point_action")
	}
	if err := c.backupIdentity(value.Identity.NativeID, synapseRestorePointType); err != nil {
		return nil, err
	}
	a := &synapseRestorePointAction{client: c, planned: value}
	if err := a.identity(contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *synapseRestorePointAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "synapse-restore-point-delete-1", "request": req, "data": data, "origin": result.ProviderOperationID})
}
func (a *synapseRestorePointAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[synapseRestoreReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 8 || req.Asset.Location != text(review["region"]) || review["resource"] != req.Asset.Normalized["_synapse_backup_configuration"] || review["protected"] != false || review["ready"] != true || review["user_defined"] != true || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[synapseRestoreProof] != a.planned.Normalized[synapseRestoreProof] || req.Asset.Normalized[synapseRestoreProof] != a.client.restorePointProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("synapse_restore_point_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("synapse_restore_point_receipt_changed")
	}
	return nil
}
func (a *synapseRestorePointAction) current(ctx context.Context, req contracts.ActionRequest) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	review, absent, err := a.client.restorePointReview(ctx, a.planned.Identity.NativeID)
	if err != nil {
		return false, err
	}
	expected := object(req.Asset.Normalized[synapseRestoreReview])
	for _, key := range []string{"pool", "workspace", "group", "region"} {
		if review[key] != expected[key] {
			return false, serviceDenied("synapse_restore_point_context_changed")
		}
	}
	if !absent && a.client.privateConfiguration(review) != a.client.privateConfiguration(expected) {
		return false, serviceDenied("synapse_restore_point_changed")
	}
	return absent, nil
}
func (a *synapseRestorePointAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}

// Preserve the wire body for the shared decoder while separately verifying the
// native synchronous DELETE's empty acknowledgement. No shared transport state.
type synapseRestoreAckTransport struct {
	base  http.RoundTripper
	empty bool
}

func (t *synapseRestoreAckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil {
		return res, err
	}
	one := make([]byte, 1)
	n, err := io.ReadFull(res.Body, one)
	if err != nil && err != io.EOF {
		res.Body.Close()
		return nil, err
	}
	t.empty = n == 0 && err == io.EOF
	res.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(one[:n]), res.Body), res.Body}
	return res, nil
}
func (a *synapseRestorePointAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
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
		data, err := providerData()
		if err != nil {
			return result, err
		}
		op, _ := data.catalog.Operation("Azure.Microsoft.Synapse.SqlPoolRestorePoints_Delete")
		id := a.planned.Identity.NativeID
		parts := strings.Split(id, "/")
		bound, err := bindAzureREST(op, map[string]any{"subscriptionId": a.client.subscription, "resourceGroupName": parts[4], "workspaceName": parts[8], "sqlPoolName": parts[10], "restorePointName": parts[12]})
		if err != nil {
			return result, err
		}
		current := *a.client.http
		base := current.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		ack := &synapseRestoreAckTransport{base: base}
		current.Transport = ack
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + id)}, a.client.validateURL, &current, false)
		if err != nil && !isNotFound(err) {
			return result, err
		}
		if err == nil && (!slices.Contains([]int{200, 204}, res.status) || !ack.empty || operationLocation(res.header) != "") {
			return result, serviceDenied("invalid_synapse_restore_point_acknowledgement")
		}
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		result.Data["accepted_status"] = res.status
	}
	// Return the signed acknowledgement before any follow-up HTTP request.
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}
func (a *synapseRestorePointAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := "Retained"
	if absent {
		state = ""
	}
	return contracts.ReadbackResult{Exists: !absent, State: state}, nil
}
func (a *synapseRestorePointAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	read, err := a.Readback(ctx, req)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}, err
}
func (*synapseRestorePointAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
