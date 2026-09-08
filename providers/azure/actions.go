package azure

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type action struct {
	client       *client
	kind         resourceType
	id, endpoint string
	location     string
	deletion     catalog.RESTRequest
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
	operation, parameters, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	if kind.NativeType == "Microsoft.Web/sites" {
		// Keep deletion of the App Service plan an explicit plan action.
		parameters["deleteEmptyServerFarm"] = false
	}
	deletion, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	return &action{client: c, kind: kind, id: nativeID, endpoint: endpoint, deletion: deletion, location: strings.ToLower(value.Location)}, nil
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
	if a.kind.NativeType == vmType || a.kind.NativeType == nicType {
		if _, reason, err := a.evaluateAttachments(ctx, request, res.data, locks); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
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
	if a.kind.NativeType == vmType || a.kind.NativeType == nicType {
		return a.prepareAttachments(ctx, request)
	}
	return a.delete(ctx, request)
}

func (a *action) delete(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	headers := map[string]string{}
	for name, value := range a.deletion.Headers {
		headers[name] = value
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	res, err := a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	return a.operationResult(res)
}

func (a *action) operationResult(res response) (contracts.ActionResult, error) {
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
		if err := a.validateOperationURL(operation); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, Data: map[string]any{"polling": polling}, RetryAfter: retryAfter(res.header)}, nil
}
func operationError(response response) error {
	data := response.data
	state := strings.ToLower(text(data["status"]))
	if state == "" {
		state = strings.ToLower(text(object(data["properties"])["provisioningState"]))
	}
	if state == "failed" || state == "canceled" || state == "cancelled" || len(object(data["error"])) > 0 {
		return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage, RequestID: response.requestID}}
	}
	return nil
}
func (a *action) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase := text(result.Data["phase"])
	if phase == "prepare_attachments" {
		return a.waitAttachmentPreparation(ctx, request, result)
	}
	if phase != "" {
		if phase != "delete" || (a.kind.NativeType != vmType && a.kind.NativeType != nicType) {
			return contracts.WaitResult{}, fmt.Errorf("invalid Azure action phase")
		}
		result.ProviderOperationID = text(result.Data["operation"])
	}
	poll, err := a.poll(ctx, result)
	if err != nil || !poll.Done {
		return poll, err
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}

func (a *action) poll(ctx context.Context, result contracts.ActionResult) (contracts.WaitResult, error) {
	if result.ProviderOperationID != "" {
		if err := a.validateOperationURL(result.ProviderOperationID); err != nil {
			return contracts.WaitResult{}, err
		}
		if polling := text(result.Data["polling"]); polling != "status" && polling != "location" {
			return contracts.WaitResult{}, fmt.Errorf("invalid Azure polling protocol")
		}
		res, err := a.client.request(ctx, "GET", result.ProviderOperationID)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if err := operationError(res); err != nil {
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
	return contracts.WaitResult{Done: true}, nil
}

func (a *action) validateOperationURL(endpoint string) error {
	if err := a.client.validateURL(endpoint); err != nil {
		return err
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(strings.ToLower(u.Path), "/")
	namespace := strings.ToLower(strings.Split(a.kind.NativeType, "/")[0])
	resourceParts := strings.Split(a.id, "/")
	found := false
	for i, part := range parts {
		if i+1 >= len(parts) {
			continue
		}
		switch part {
		case "providers":
			if parts[i+1] != namespace {
				return fmt.Errorf("Azure operation belongs to another resource provider")
			}
			found = true
		case "resourcegroups":
			if len(resourceParts) < 5 || parts[i+1] != resourceParts[4] {
				return fmt.Errorf("Azure operation belongs to another resource group")
			}
		case "locations":
			if a.location != "" && a.location != "global" && parts[i+1] != a.location {
				return fmt.Errorf("Azure operation belongs to another region")
			}
		}
	}
	if !found {
		return fmt.Errorf("Azure operation has no resource provider")
	}
	return nil
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
	if kind.NativeType == containerType && (properties["hasLegalHold"] == true || properties["hasImmutabilityPolicy"] == true) {
		return "blob_container_retention_policy"
	}
	return ""
}
