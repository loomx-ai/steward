package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const hybridComputeCleanup = "_hybrid_compute_cleanup"
const hybridComputeCleanupProof = "_hybrid_compute_cleanup_binding"

func hybridComputeChild(kind string) bool {
	return slices.Contains([]string{hybridExtensionType, hybridCommandType, hybridProfileType}, kind)
}

// Keep all authored and unknown child fields private and bound. Native DELETE
// has no If-Match, and changes provisioning state, ETags and execution output.
func hybridComputeChildSnapshot(raw map[string]any) map[string]any {
	out := batchClone(raw)
	out["id"] = strings.ToLower(text(raw["id"]))
	out["name"] = last(text(out["id"]))
	out["type"] = strings.ToLower(text(raw["type"]))
	delete(out, "etag")
	delete(out, "eTag")
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(out["systemData"]), key)
	}
	if len(object(out["systemData"])) == 0 {
		delete(out, "systemData")
	}
	props := object(out["properties"])
	delete(props, "provisioningState")
	if hybridComputeChild(hybridComputeKind(text(raw["type"]))) && hybridComputeKind(text(raw["type"])) != hybridProfileType {
		delete(props, "instanceView")
	}
	return out
}

// Child removal can change the machine's embedded extensions/license profile
// and ETag. Bind its registration identity and ownership, not those projections.
func hybridComputeParentStamp(raw map[string]any) map[string]any {
	props := object(raw["properties"])
	return map[string]any{"id": strings.ToLower(text(raw["id"])), "vm_id": props["vmId"], "vm_uuid": props["vmUuid"], "creation": creationGeneration(raw), "location": resourceRegion(raw), "kind": raw["kind"], "identity": raw["identity"], "tags": raw["tags"], "managed_by": raw["managedBy"], "cluster": props["parentClusterResourceId"], "private_link": props["privateLinkScopeResourceId"]}
}

func (c *client) hybridComputeCleanupBinding(id string, connection asset.ConnectionID, location string, cleanup map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "hybrid-compute-child-1", "id": id, "connection": connection, "location": location, "cleanup": cleanup})
}

func hybridComputeChildProtection(raw, parent map[string]any) string {
	if !uuidPattern.MatchString(text(object(parent["properties"])["vmId"])) {
		return "hybrid_compute_machine_registration_unverified"
	}
	for _, value := range []map[string]any{raw, parent} {
		if reason := protectionReason(resourceType{NativeType: hybridComputeKind(text(value["type"]))}, value); reason != "" {
			return reason
		}
	}
	return ""
}

func (c *client) hybridComputeCleanupRecord(value asset.Asset) error {
	if _, err := c.hybridComputeRecordedReferences(value); err != nil {
		return err
	}
	state := object(value.Normalized[hybridComputeCleanup])
	if value.ID == "" || value.Location == "" || value.Location != strings.ToLower(value.Location) {
		return serviceDenied("invalid_hybrid_compute_cleanup_identity")
	}
	if value.Identity.NativeType == hybridMachineType {
		if value.Location != text(state["location"]) {
			return serviceDenied("hybrid_compute_machine_location_changed")
		}
		return c.hybridComputeMachineRecorded(value.Identity.NativeID, value.Identity.ConnectionID, value.Normalized)
	}
	_, protected := state["protected"].(bool)
	if !hybridComputeChild(value.Identity.NativeType) || len(state) != 5 || text(state["inventory"]) != text(value.Normalized["_hybrid_compute_configuration"]) || text(state["resource"]) == "" || text(state["parent"]) == "" || !protected || value.Normalized["cleanup_protected"] != state["protected"] || value.Normalized[hybridComputeCleanupProof] != c.hybridComputeCleanupBinding(value.Identity.NativeID, value.Identity.ConnectionID, value.Location, state) {
		return serviceDenied("invalid_hybrid_compute_cleanup_asset")
	}
	return nil
}

func (c *client) hybridComputeCleanupAsset(value asset.Asset) error {
	if err := c.hybridComputeCleanupRecord(value); err != nil {
		return err
	}
	if value.Normalized["cleanup_protected"] != false {
		return serviceDenied("hybrid_compute_reviewed_resource_protected")
	}
	return nil
}

type hybridComputeAction struct {
	client     *client
	planned    asset.Asset
	kind       resourceType
	id, parent string
	deletion   catalog.RESTRequest
}

func newHybridComputeAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*hybridComputeAction, error) {
	if err := c.hybridComputeCleanupAsset(value); err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || kind.NativeType != value.Identity.NativeType || kind.ReadOnly {
		return nil, serviceDenied("hybrid_compute_action_connection_changed")
	}
	op, params, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	deletion, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	return &hybridComputeAction{client: c, planned: value, kind: kind, id: value.Identity.NativeID, parent: hybridComputeParent(value.Identity.NativeID, kind.NativeType), deletion: deletion}, nil
}

func (*hybridComputeAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

func (a *hybridComputeAction) identity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID != a.planned.ID || request.Asset.Identity != a.planned.Identity || request.Asset.Location != a.planned.Location || request.Asset.Normalized[hybridComputeCleanupProof] != a.planned.Normalized[hybridComputeCleanupProof] || len(request.Parameters)+len(request.LifecycleImpacts) != 0 {
		return serviceDenied("hybrid_compute_action_request_changed")
	}
	if err := a.client.hybridComputeCleanupAsset(request.Asset); err != nil {
		return err
	}
	if a.kind.NativeType == hybridMachineType {
		if err := a.machineRequest(request); err != nil {
			return err
		}
	} else if len(request.PrerequisiteDeletions) != 0 {
		return serviceDenied("unexpected_hybrid_compute_prerequisite")
	}
	if request.ExecutionResult != nil {
		return a.verifyPhase(request, *request.ExecutionResult)
	}
	return nil
}

func (a *hybridComputeAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "hybrid-compute-cleanup-1", "request": request, "origin": result.ProviderOperationID, "data": data})
}

func (a *hybridComputeAction) verifyPhase(request contracts.ActionRequest, result contracts.ActionResult) error {
	if len(result.Data) != 3 || result.Data["phase"] != "delete" || result.Data["binding"] != a.phaseBinding(request, result) {
		return serviceDenied("hybrid_compute_cleanup_receipt_changed")
	}
	return a.client.hybridComputeVerifyReceipt(a.id, object(result.Data["operation"]))
}

// Always read the selected child itself. Parent disappearance can prevent a
// mutation but cannot erase a surviving child or establish its absence.
func (a *hybridComputeAction) observe(ctx context.Context, request contracts.ActionRequest) (selected, parent map[string]any, err error) {
	res, err := a.client.hybridComputeRead(ctx, a.id, a.kind.NativeType)
	if isNotFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	state := object(request.Asset.Normalized[hybridComputeCleanup])
	if a.client.privateConfiguration(hybridComputeChildSnapshot(res.data)) != state["resource"] || resourceRegion(res.data) != request.Asset.Location {
		return nil, nil, serviceDenied("hybrid_compute_child_configuration_changed")
	}
	selected = res.data
	res, err = a.client.hybridComputeRead(ctx, a.parent, hybridMachineType)
	if isNotFound(err) {
		return selected, nil, nil
	}
	if err != nil {
		return selected, nil, err
	}
	if a.client.privateConfiguration(hybridComputeParentStamp(res.data)) != state["parent"] || resourceRegion(res.data) != request.Asset.Location {
		return selected, nil, serviceDenied("hybrid_compute_parent_registration_changed")
	}
	return selected, res.data, nil
}

func (a *hybridComputeAction) protection(ctx context.Context, selected, parent map[string]any) error {
	if reason := hybridComputeChildProtection(selected, parent); reason != "" {
		return serviceDenied(reason)
	}
	groupID := strings.Join(strings.Split(a.id, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return err
	}
	if !insightsARMReadValid(group, groupID, groupType) {
		return serviceDenied("hybrid_compute_group_unverified")
	}
	if text(group.data["managedBy"]) != "" {
		return serviceDenied("azure_managed_resource_group")
	}
	if protectedAzureTags(object(group.data["tags"])) {
		return serviceDenied("azure_protected_tag")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	if locked(a.id, locks) {
		return serviceDenied("azure_management_lock")
	}
	return nil
}

func (a *hybridComputeAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	if a.kind.NativeType == hybridMachineType {
		return a.machinePreflight(ctx, request)
	}
	for range 2 {
		selected, parent, err := a.observe(ctx, request)
		if err != nil {
			return check, err
		}
		if selected == nil {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if parent == nil || strings.EqualFold(text(object(selected["properties"])["provisioningState"]), "Deleting") {
			return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"hybrid_compute_wait": true}}, nil
		}
		if a.client.privateConfiguration(map[string]any{"etag": selected["etag"], "eTag": selected["eTag"]}) != object(request.Asset.Normalized[hybridComputeCleanup])["etag"] {
			return check, serviceDenied("hybrid_compute_child_etag_changed")
		}
		if err := a.protection(ctx, selected, parent); err != nil {
			return check, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *hybridComputeAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	res := response{}
	operation := a.client.hybridComputeSignReceipt(a.id, nil)
	if !check.Absent && check.Evidence["hybrid_compute_wait"] != true {
		headers := maps.Clone(a.deletion.Headers)
		if headers == nil {
			headers = map[string]string{}
		}
		if request.IdempotencyKey != "" {
			headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
		}
		res, err = a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
		if err != nil && !isNotFound(err) {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if err == nil {
			operation, err = a.client.hybridComputeDeleteReceipt(a.id, res)
			if err != nil {
				return contracts.ActionResult{}, err
			}
		}
	}
	out := contracts.ActionResult{ProviderOperationID: operationLocation(res.header), ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"phase": "delete", "operation": operation}}
	out.Data["binding"] = a.phaseBinding(request, out)
	return out, nil
}

func (a *hybridComputeAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.kind.NativeType == hybridMachineType {
		raw, children, err := a.machineObserve(ctx, request)
		return contracts.ReadbackResult{Exists: raw != nil || len(children) != 0, State: "hybrid_compute_machine_deleting"}, err
	}
	selected, _, err := a.observe(ctx, request)
	return contracts.ReadbackResult{Exists: selected != nil, State: text(object(selected["properties"])["provisioningState"])}, err
}

func (a *hybridComputeAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	poll, err := a.client.hybridComputePoll(ctx, a.id, object(result.Data["operation"]))
	if err != nil {
		return contracts.WaitResult{}, contracts.DependencyReadError(err)
	}
	out := contracts.WaitResult{State: poll.State, RetryAfter: max(2*time.Second, poll.RetryAfter), Data: maps.Clone(result.Data)}
	out.Data["operation"] = poll.Data
	result.Data = out.Data
	out.Data["binding"] = a.phaseBinding(request, result)
	if !poll.Done {
		return out, nil
	}
	read, err := a.Readback(ctx, request)
	out.Done, out.State = err == nil && !read.Exists, read.State
	return out, err
}

var _ contracts.ActionDriver = (*hybridComputeAction)(nil)
