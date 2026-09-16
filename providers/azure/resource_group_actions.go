package azure

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type resourceGroupAction struct {
	runtime *Runtime
	client  *client
	planned string
}

// Bind native identity and the reviewed configuration, while allowing unrelated
// inventory timestamps to advance between planning and worker recovery.
func resourceGroupActionIdentity(value asset.Asset) map[string]any {
	return map[string]any{"id": value.ID, "identity": value.Identity, "location": value.Location, "configuration": value.Normalized["_resource_group_configuration"], "native_location": value.Normalized["_resource_group_location"], "creation": value.Normalized["_arm_creation_generation"]}
}

func newResourceGroupAction(r *Runtime, c *client, connection asset.ConnectionID, value asset.Asset) (*resourceGroupAction, error) {
	if connection == "" || value.Identity.ConnectionID != connection || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.ID == "" || text(value.Normalized["_resource_group_configuration"]) == "" || text(value.Normalized["_resource_group_location"]) == "" {
		return nil, serviceDenied("invalid_resource_group_action_identity")
	}
	if _, err := c.resourceGroupOperationBinding(contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "identity-check"}, nil); err != nil {
		return nil, err
	}
	return &resourceGroupAction{runtime: r, client: c, planned: c.privateConfiguration(resourceGroupActionIdentity(value))}, nil
}
func (*resourceGroupAction) DeletionCheckTimeout() time.Duration { return 2 * time.Hour }
func (a *resourceGroupAction) identity(req contracts.ActionRequest) error {
	if a.planned != a.client.privateConfiguration(resourceGroupActionIdentity(req.Asset)) {
		return serviceDenied("resource_group_action_identity_changed")
	}
	_, err := a.client.resourceGroupProductRequests(req)
	return err
}
func (a *resourceGroupAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.PreflightResult{}, err
	}
	if req.ExecutionResult != nil {
		return contracts.PreflightResult{}, serviceDenied("resource_group_requires_operation_resume")
	}
	_, err := a.client.deploymentStackMemberRead(ctx, req.Asset)
	if isNotFound(err) {
		read, err := a.Readback(ctx, req)
		return contracts.PreflightResult{Allowed: err == nil && !read.Exists, Absent: err == nil && !read.Exists}, err
	}
	if err != nil {
		return contracts.PreflightResult{}, contracts.DependencyReadError(err)
	}
	err = a.runtime.resourceGroupPreflight(ctx, req)
	return contracts.PreflightResult{Allowed: err == nil}, err
}
func (a *resourceGroupAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	return a.runtime.resourceGroupStartDelete(ctx, req)
}
func (a *resourceGroupAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	return a.runtime.resourceGroupResumeDeletion(ctx, req, result.Data)
}
func (a *resourceGroupAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if req.ExecutionResult != nil {
		out, err := a.runtime.resourceGroupResumeDeletion(ctx, req, req.ExecutionResult.Data)
		return contracts.ReadbackResult{Exists: !out.Done}, err
	}
	// Read-only absence also covers a resource already removed before this job;
	// no accepted-operation receipt is fabricated for that observation.
	products, err := a.client.resourceGroupProductRequests(req)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	absent, err := a.runtime.resourceGroupProductsAbsent(ctx, a.client, req, products)
	return contracts.ReadbackResult{Exists: !absent}, contracts.DependencyReadError(err)
}
