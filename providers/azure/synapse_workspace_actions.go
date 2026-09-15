package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseWorkspaceAction struct {
	client   *synapseDataClient
	planned  asset.Asset
	boundary map[string]any
}

func newSynapseWorkspaceAction(c *synapseDataClient, connection asset.ConnectionID, value asset.Asset) (*synapseWorkspaceAction, error) {
	boundary, err := c.arm.synapseWorkspaceRecorded(value)
	if err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || value.Normalized["cleanup_protected"] != false || boundary["protected"] != false || boundary["ready"] != true {
		return nil, serviceDenied("synapse_workspace_not_ready")
	}
	return &synapseWorkspaceAction{client: c, planned: value, boundary: boundary}, nil
}
func (a *synapseWorkspaceAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.arm.privateConfiguration(map[string]any{"protocol": "synapse-workspace-action-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}
func (a *synapseWorkspaceAction) identity(req contracts.ActionRequest) error {
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.PrerequisiteDeletions) != 0 || req.Asset.Normalized[synapseWorkspaceBoundaryProof] != a.planned.Normalized[synapseWorkspaceBoundaryProof] {
		return serviceDenied("synapse_workspace_request_changed")
	}
	if _, err := a.client.arm.synapseWorkspaceRecorded(req.Asset); err != nil {
		return err
	}
	members := object(a.boundary["members"])
	seen := map[string]bool{}
	for _, impact := range req.LifecycleImpacts {
		v := impact.Asset
		id := v.Identity.NativeID
		entry := object(members[id])
		if seen[id] || entry == nil || !impact.Delete || impact.ControllerID != a.planned.ID || v.ID == "" || v.Identity.Provider != asset.ProviderAzure || v.Identity.ConnectionID != a.planned.Identity.ConnectionID || v.Identity.Partition != a.planned.Identity.Partition || v.Identity.NativeType != entry["kind"] || v.Normalized["_synapse_private_configuration"] != entry["configuration"] {
			return serviceDenied("synapse_workspace_impact_changed")
		}
		seen[id] = true
	}
	for id, value := range members {
		if !seen[id] && object(value)["absent"] != true {
			return serviceDenied("synapse_workspace_unreviewed_member")
		}
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("synapse_workspace_receipt_changed")
	}
	return nil
}
func (a *synapseWorkspaceAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id := a.planned.Identity.NativeID
	res, err := a.client.arm.request(ctx, "GET", apiURL(id, synapseVersion))
	if err == nil {
		if err = a.client.arm.synapseReadResponse(res, id, synapseType); err != nil {
			return contracts.ReadbackResult{}, err
		}
		if a.boundary["workspace_uid"] != object(res.data["properties"])["workspaceUID"] {
			return contracts.ReadbackResult{}, serviceDenied("synapse_workspace_recreated_or_changed")
		}
		return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
	}
	if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	// Pools are independent ARM resources. Workspace disappearance alone must
	// not close them. Code/run metadata is inseparable from its deleted workspace.
	for child, value := range object(a.boundary["members"]) {
		kind := text(object(value)["kind"])
		if kind != synapseSparkType && kind != synapseSQLType {
			continue
		}
		res, err := a.client.arm.request(ctx, "GET", apiURL(child, synapseVersion))
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
		if err = a.client.arm.synapseReadResponse(res, child, kind); err != nil {
			return contracts.ReadbackResult{}, err
		}
		return contracts.ReadbackResult{Exists: true, State: "Deleting"}, nil
	}
	// A saved terminal native workspace operation and its own absence prove
	// removal of integrated metadata. Without that receipt (including expired
	// callbacks or externally removed workspaces), verify each metadata GET.
	if req.ExecutionResult == nil || req.ExecutionResult.Data["operation_done"] != true {
		endpoint := "https://" + last(id) + ".dev.azuresynapse.net"
		w := synapseWorkspace{id: id, endpoint: endpoint, raw: map[string]any{"id": id, "name": last(id), "type": synapseType, "location": a.planned.Location, "properties": map[string]any{"connectivityEndpoints": map[string]any{"dev": endpoint}}}}
		for _, value := range object(a.boundary["members"]) {
			entry := object(value)
			d := synapseDataKind(text(entry["kind"]))
			if d.kind == "" {
				continue
			}
			_, err := a.client.synapseReadData(ctx, synapseDataTarget{workspace: w}, d, object(entry["parameters"]))
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
			}
			return contracts.ReadbackResult{Exists: true, State: "Deleting"}, nil
		}
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
func (a *synapseWorkspaceAction) current(ctx context.Context, req contracts.ActionRequest) (synapseWorkspace, error) {
	if err := a.identity(req); err != nil {
		return synapseWorkspace{}, err
	}
	w, err := a.client.arm.synapseWorkspaceRead(ctx, a.planned.Identity.NativeID)
	if isNotFound(err) {
		return synapseWorkspace{}, nil
	}
	if err != nil {
		return w, err
	}
	fresh, err := a.client.workspaceBoundary(ctx, w, object(a.boundary["members"]))
	if err != nil {
		return w, err
	}
	if fresh["workspace"] != a.boundary["workspace"] || fresh["group"] != a.boundary["group"] || fresh["protected"] != false || fresh["ready"] != true {
		return w, serviceDenied("synapse_workspace_context_changed")
	}
	old := object(a.boundary["members"])
	for id, value := range object(fresh["members"]) {
		entry, expected := object(value), object(old[id])
		if expected == nil || entry["kind"] != expected["kind"] || entry["configuration"] != expected["configuration"] || a.client.arm.privateConfiguration(object(entry["parameters"])) != a.client.arm.privateConfiguration(object(expected["parameters"])) {
			return w, serviceDenied("synapse_workspace_members_changed")
		}
	}
	return w, nil
}
func (a *synapseWorkspaceAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	_, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *synapseWorkspaceAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	w, err := a.current(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := contracts.ActionResult{Data: map[string]any{"operation_done": false}}
	if w.id != "" {
		metadata, err := providerData()
		if err != nil {
			return result, err
		}
		op, _ := metadata.catalog.Operation("Azure.Microsoft.Synapse.Workspaces_Delete")
		parts := strings.Split(w.id, "/")
		bound, err := bindAzureREST(op, map[string]any{"subscriptionId": a.client.arm.subscription, "resourceGroupName": parts[4], "workspaceName": parts[8]})
		if err != nil {
			return result, err
		}
		res, err := a.client.arm.requestBody(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + w.id)})
		if err != nil && !isNotFound(err) {
			return result, err
		}
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		if err == nil {
			receipt, err := a.client.arm.synapseDeleteReceipt(w.id, res)
			if err != nil {
				return result, err
			}
			result.Data["operation"] = receipt
		}
	}
	result.Data["binding"] = a.binding(req, result)
	// No network request follows acceptance before the worker can save it.
	return result, nil
}
func (a *synapseWorkspaceAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if receipt := object(result.Data["operation"]); receipt != nil && result.Data["operation_done"] != true && result.Data["scope_absent"] != true {
		poll, err := a.client.arm.synapsePoll(ctx, a.planned.Identity.NativeID, receipt)
		if err != nil {
			if ctx.Err() != nil {
				return out, err
			}
			read, ownErr := a.Readback(ctx, req)
			if ownErr == nil && !read.Exists {
				out.Done = true
				out.Data["scope_absent"] = true
				updated := result
				updated.Data = out.Data
				out.Data["binding"] = a.binding(req, updated)
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
func (*synapseWorkspaceAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
