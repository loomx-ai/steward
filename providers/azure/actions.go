package azure

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type action struct {
	client       *client
	kind         resourceType
	id, endpoint string
}

func (r *Runtime) ResolveAction(ctx context.Context, id asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok || kind.ReadOnly || value.Identity.Provider != asset.ProviderAzure {
		return nil, fmt.Errorf("Azure resource has no action driver")
	}
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	endpoint, err := c.resourceURL(kind, value.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	nativeID, _, _ := parseID(value.Identity.NativeID)
	return &action{client: c, kind: kind, id: nativeID, endpoint: endpoint}, nil
}
func (*action) DeletionCheckTimeout() time.Duration { return time.Hour }
func (a *action) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if request.Action != "delete" {
		return contracts.PreflightResult{Reason: "unsupported_action"}, nil
	}
	res, err := a.client.request(ctx, "GET", a.endpoint)
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !strings.EqualFold(text(res.data["id"]), a.id) || !strings.EqualFold(text(res.data["type"]), a.kind.NativeType) {
		return contracts.PreflightResult{}, fmt.Errorf("Azure preflight identity mismatch")
	}
	if reason := protectionReason(a.kind, res.data); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	parts := strings.Split(a.id, "/")
	group, err := a.client.request(ctx, "GET", apiURL(strings.Join(parts[:5], "/"), resourcesVersion))
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if text(group.data["managedBy"]) != "" {
		return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
	}
	locks, err := a.client.listAll(ctx, a.client.root()+"/providers/Microsoft.Authorization/locks", locksVersion)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if locked(a.id, locks) {
		return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
	}
	switch a.kind.NativeType {
	case vnetType:
		children, err := a.client.listAll(ctx, a.id+"/subnets", a.kind.Version)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(children) > 0 {
			return contracts.PreflightResult{Reason: "virtual_network_has_subnets"}, nil
		}
	case storageType:
		empty, err := a.client.storageAccountEmpty(ctx, a.id, res.data)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !empty {
			return contracts.PreflightResult{Reason: "storage_account_not_empty"}, nil
		}
	case containerType:
		empty, err := a.client.blobContainerEmpty(ctx, a.id)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !empty {
			return contracts.PreflightResult{Reason: "blob_container_not_empty"}, nil
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *action) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: check.Reason, Message: contracts.SafeProviderValidationMessage}}
	}
	if check.Absent {
		return contracts.ActionResult{}, nil
	}
	endpoint := a.endpoint
	if a.kind.NativeType == "Microsoft.Web/sites" {
		// Azure otherwise deletes an empty App Service plan as a side effect.
		// Its lifecycle must remain an explicit, separately selected action.
		endpoint += "&deleteEmptyServerFarm=false"
	}
	res, err := a.client.request(ctx, "DELETE", endpoint)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(res.data); err != nil {
		return contracts.ActionResult{}, err
	}
	operation := res.header.Get("Azure-AsyncOperation")
	polling := "status"
	if operation == "" {
		operation = res.header.Get("Operation-Location")
	}
	if operation == "" {
		operation = res.header.Get("Location")
		polling = "location"
	}
	if operation != "" {
		if err := a.client.validateURL(operation); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return contracts.ActionResult{ProviderOperationID: operation, Data: map[string]any{"polling": polling}, RetryAfter: retryAfter(res.header)}, nil
}
func operationError(data map[string]any) error {
	state := strings.ToLower(text(data["status"]))
	if state == "" {
		state = strings.ToLower(text(object(data["properties"])["provisioningState"]))
	}
	if state == "failed" || state == "canceled" || state == "cancelled" || len(object(data["error"])) > 0 {
		return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage}}
	}
	return nil
}
func (a *action) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if result.ProviderOperationID != "" {
		res, err := a.client.request(ctx, "GET", result.ProviderOperationID)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if err := operationError(res.data); err != nil {
				return contracts.WaitResult{}, err
			}
			state := text(res.data["status"])
			if state == "" {
				state = text(object(res.data["properties"])["provisioningState"])
			}
			done := strings.EqualFold(state, "Succeeded")
			if text(result.Data["polling"]) == "location" && state == "" {
				done = res.status == 200 || res.status == 204
			}
			if !done {
				return contracts.WaitResult{RetryAfter: retryAfter(res.header), State: state}, nil
			}
		}
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
func (a *action) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	res, err := a.client.request(ctx, "GET", a.endpoint)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !strings.EqualFold(text(res.data["id"]), a.id) {
		return contracts.ReadbackResult{}, fmt.Errorf("Azure readback identity mismatch")
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
}
func protectionReason(kind resourceType, raw map[string]any) string {
	if kind.NativeType == "Microsoft.Sql/servers/databases" && strings.EqualFold(last(text(raw["id"])), "master") {
		return "azure_system_database"
	}
	if owner := text(raw["managedBy"]); owner != "" {
		_, ownerType, err := parseID(owner)
		// An attached disk's managedBy is its VM. The VM -> disk dependency
		// orders detachment; other provider-managed resources remain protected.
		if kind.NativeType != diskType || err != nil || !strings.EqualFold(ownerType, vmType) {
			return "azure_managed_resource"
		}
	}
	properties := object(raw["properties"])
	if kind.NativeType == vmType && text(object(properties["virtualMachineScaleSet"])["id"]) != "" {
		return "azure_scale_set_managed_vm"
	}
	if kind.NativeType == nicType && text(object(properties["privateEndpoint"])["id"]) != "" {
		return "azure_private_endpoint_managed_nic"
	}
	if kind.NativeType == vmType || kind.NativeType == nicType {
		var autoDelete func(any) bool
		autoDelete = func(value any) bool {
			switch typed := value.(type) {
			case map[string]any:
				for key, value := range typed {
					if key == "deleteOption" && strings.EqualFold(text(value), "Delete") {
						return true
					}
					if autoDelete(value) {
						return true
					}
				}
			case []any:
				for _, value := range typed {
					if autoDelete(value) {
						return true
					}
				}
			}
			return false
		}
		if autoDelete(properties) {
			return "attached_resource_auto_delete_enabled"
		}
	}
	if kind.NativeType == containerType && (properties["hasLegalHold"] == true || properties["hasImmutabilityPolicy"] == true) {
		return "blob_container_retention_policy"
	}
	return ""
}
