package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const azureLocalCleanup = "_azure_local_cleanup"
const azureLocalCleanupProof = "_azure_local_cleanup_binding"

// Retain authored and unknown fields privately. Provisioning, connection/power
// status and ETags may change while deletion is being observed.
func azureLocalCleanupSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	delete(object(out["properties"]), "status")
	return out
}

func azureLocalGuestProtection(raw, vm, machine map[string]any) string {
	if !uuidPattern.MatchString(text(object(machine["properties"])["vmId"])) || !strings.EqualFold(text(machine["kind"]), "HCI") {
		return "azure_local_registration_unverified"
	}
	for _, value := range []map[string]any{raw, vm, machine} {
		if reason := protectionReason(resourceType{NativeType: text(value["type"])}, value); reason != "" {
			return reason
		}
	}
	return ""
}

func (c *client) azureLocalCleanupBinding(id string, connection asset.ConnectionID, location string, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "azure-local-guest-1", "id": id, "connection": connection, "location": location, "state": state})
}

func (c *client) azureLocalGuestRecord(value asset.Asset) error {
	if _, err := c.azureLocalRecordedReferences(value); err != nil {
		return err
	}
	state := object(value.Normalized[azureLocalCleanup])
	if value.ID == "" || value.Identity.NativeType != azureLocalAgentType || value.Location == "" || value.Location != strings.ToLower(value.Location) || len(state) != 6 || text(state["resource"]) == "" || text(state["vm"]) == "" || text(state["machine"]) == "" || text(state["etag"]) == "" || state["inventory"] != value.Normalized["_azure_local_configuration"] || state["protected"] != false || value.Normalized["cleanup_protected"] != false || value.Normalized[azureLocalCleanupProof] != c.azureLocalCleanupBinding(value.Identity.NativeID, value.Identity.ConnectionID, value.Location, state) {
		return serviceDenied("invalid_azure_local_guest_cleanup_asset")
	}
	return nil
}

type azureLocalGuestAction struct {
	client   *client
	planned  asset.Asset
	deletion catalog.RESTRequest
}

func newAzureLocalGuestAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*azureLocalGuestAction, error) {
	if err := c.azureLocalGuestRecord(value); err != nil {
		return nil, err
	}
	if connection != value.Identity.ConnectionID || kind.NativeType != azureLocalAgentType || kind.ReadOnly {
		return nil, serviceDenied("azure_local_action_connection_changed")
	}
	op, params, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	deletion, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	return &azureLocalGuestAction{client: c, planned: value, deletion: deletion}, nil
}

func (*azureLocalGuestAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

func (a *azureLocalGuestAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "azure-local-guest-delete-1", "request": request, "origin": result.ProviderOperationID, "data": data})
}

func (a *azureLocalGuestAction) identity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID != a.planned.ID || request.Asset.Identity != a.planned.Identity || request.Asset.Location != a.planned.Location || request.Asset.Normalized[azureLocalCleanupProof] != a.planned.Normalized[azureLocalCleanupProof] || len(request.Parameters)+len(request.LifecycleImpacts)+len(request.PrerequisiteDeletions) != 0 {
		return serviceDenied("azure_local_guest_request_changed")
	}
	if err := a.client.azureLocalGuestRecord(request.Asset); err != nil {
		return err
	}
	if result := request.ExecutionResult; result != nil {
		if len(result.Data) != 3 || result.Data["phase"] != "delete" || result.Data["binding"] != a.phaseBinding(request, *result) {
			return serviceDenied("azure_local_guest_receipt_changed")
		}
		return a.client.azureLocalVerifyReceipt(request.Asset.Identity.NativeID, object(result.Data["operation"]))
	}
	return nil
}

// The guest's own GET decides absence. A missing VM or registration can stop a
// new mutation, but never turns a surviving guest into a completed deletion.
func (a *azureLocalGuestAction) observe(ctx context.Context) (guest, vm, machine map[string]any, err error) {
	id := a.planned.Identity.NativeID
	res, err := a.client.azureLocalRead(ctx, id, azureLocalAgentType)
	if isNotFound(err) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	guest = res.data
	state := object(a.planned.Normalized[azureLocalCleanup])
	if a.client.privateConfiguration(azureLocalCleanupSnapshot(guest)) != state["resource"] {
		return guest, nil, nil, serviceDenied("azure_local_guest_configuration_changed")
	}
	res, err = a.client.azureLocalRead(ctx, azureLocalParent(id, azureLocalAgentType), azureLocalVMType)
	if isNotFound(err) {
		return guest, nil, nil, nil
	}
	if err != nil {
		return guest, nil, nil, err
	}
	vm = res.data
	if a.client.privateConfiguration(azureLocalCleanupSnapshot(vm)) != state["vm"] {
		return guest, vm, nil, serviceDenied("azure_local_vm_configuration_changed")
	}
	res, err = a.client.hybridComputeRead(ctx, azureLocalMachine(id), hybridMachineType)
	if isNotFound(err) {
		return guest, vm, nil, nil
	}
	if err != nil {
		return guest, vm, nil, err
	}
	machine = res.data
	if a.client.privateConfiguration(hybridComputeParentStamp(machine)) != state["machine"] || resourceRegion(machine) != a.planned.Location {
		return guest, vm, machine, serviceDenied("azure_local_machine_registration_changed")
	}
	return guest, vm, machine, nil
}

func (a *azureLocalGuestAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	for range 2 {
		guest, vm, machine, err := a.observe(ctx)
		if err != nil {
			return check, err
		}
		if guest == nil {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if vm == nil || machine == nil || strings.EqualFold(text(object(guest["properties"])["provisioningState"]), "Deleting") || strings.EqualFold(text(object(vm["properties"])["provisioningState"]), "Deleting") {
			return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"azure_local_wait": true}}, nil
		}
		if a.client.privateConfiguration(map[string]any{"etag": guest["etag"], "eTag": guest["eTag"]}) != object(a.planned.Normalized[azureLocalCleanup])["etag"] {
			return check, serviceDenied("azure_local_guest_etag_changed")
		}
		if reason := azureLocalGuestProtection(guest, vm, machine); reason != "" {
			return check, serviceDenied(reason)
		}
		groupID := strings.Join(strings.Split(a.planned.Identity.NativeID, "/")[:5], "/")
		group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
		if err != nil {
			return check, err
		}
		if !insightsARMReadValid(group, groupID, groupType) {
			return check, serviceDenied("azure_local_group_unverified")
		}
		if reason := protectionReason(resourceType{NativeType: groupType}, group.data); reason != "" {
			return check, serviceDenied(reason)
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return check, err
		}
		if locked(a.planned.Identity.NativeID, locks) {
			return check, serviceDenied("azure_management_lock")
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *azureLocalGuestAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
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
	operation := a.client.azureLocalSignReceipt(a.planned.Identity.NativeID, nil)
	if !check.Absent && check.Evidence["azure_local_wait"] != true {
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
			operation, err = a.client.azureLocalDeleteReceipt(a.planned.Identity.NativeID, res)
			if err != nil {
				return contracts.ActionResult{}, err
			}
		}
	}
	result := contracts.ActionResult{ProviderOperationID: operationLocation(res.header), ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"phase": "delete", "operation": operation}}
	result.Data["binding"] = a.phaseBinding(request, result)
	return result, nil
}

func (a *azureLocalGuestAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	guest, _, _, err := a.observe(ctx)
	return contracts.ReadbackResult{Exists: guest != nil, State: text(object(guest["properties"])["provisioningState"])}, contracts.DependencyReadError(err)
}

func (a *azureLocalGuestAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	poll, err := a.client.azureLocalPoll(ctx, a.planned.Identity.NativeID, object(result.Data["operation"]))
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

var _ contracts.ActionDriver = (*azureLocalGuestAction)(nil)
