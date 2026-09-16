package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) resourceGroupReviewedRead(ctx context.Context, req contracts.ActionRequest) (response, error) {
	current, err := c.deploymentStackMemberRead(ctx, req.Asset)
	if err != nil {
		return response{}, err
	}
	if text(req.Asset.Normalized["_resource_group_configuration"]) == "" || req.Asset.Normalized["_resource_group_configuration"] != c.privateConfiguration(current.data) || text(req.Asset.Normalized["_resource_group_location"]) == "" || req.Asset.Normalized["_resource_group_location"] != current.data["location"] {
		return response{}, serviceDenied("resource_group_review_changed")
	}
	if protectedAzureTags(object(current.data["tags"])) {
		return response{}, serviceDenied("azure_protected_tag")
	}
	if text(current.data["managedBy"]) != "" {
		return response{}, serviceDenied("resource_group_has_native_manager")
	}
	if !strings.EqualFold(text(object(current.data["properties"])["provisioningState"]), "Succeeded") {
		return response{}, serviceDenied("resource_group_not_ready")
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return response{}, err
	}
	if locked(req.Asset.Identity.NativeID, locks) {
		return response{}, serviceDenied("azure_management_lock")
	}
	return current, nil
}

// Require the actual registered product checks. An intrinsic child may use its
// successfully checked native parent, but no live prerequisite is waived here.
func (r *Runtime) resourceGroupCheckProducts(ctx context.Context, req contracts.ActionRequest, products map[asset.AssetID]contracts.ActionRequest, scope *resourceGroupMonitorScope) error {
	parents := map[asset.AssetID]asset.AssetID{}
	for _, impact := range req.LifecycleImpacts {
		parents[impact.Asset.ID] = impact.ControllerID
	}
	checked := map[asset.AssetID]bool{}
	var check func(asset.AssetID) error
	check = func(id asset.AssetID) error {
		if checked[id] {
			return nil
		}
		member := products[id]
		parentKind := ""
		if parent, found := products[parents[id]]; found {
			if err := check(parent.Asset.ID); err != nil {
				return err
			}
			parentKind = parent.Asset.Identity.NativeType
		}
		driver, err := r.ResolveAction(ctx, req.Asset.Identity.ConnectionID, member.Asset)
		if err != nil {
			return err
		}
		result, err := resourceGroupPreflightProduct(ctx, driver, member, parentKind, scope)
		if err != nil {
			return err
		}
		if !result.Allowed || result.Absent {
			reason := result.Reason
			if reason == "" {
				reason = "resource_group_product_not_ready"
			}
			return serviceDenied(reason)
		}
		if err = resourceGroupNativeProductReady(ctx, driver, member); err != nil {
			return err
		}
		checked[id] = true
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(products)) {
		if err := check(id); err != nil {
			return err
		}
	}
	return nil
}

// Native group deletion does not execute a product driver's preparation state
// machine. Each driver family needs an explicit native-cascade readiness contract;
// registration must not silently opt a new multi-phase driver into group deletion.
func resourceGroupNativeProductReady(ctx context.Context, driver contracts.ActionDriver, member contracts.ActionRequest) error {
	switch a := driver.(type) {
	case *monitorTargetAction:
		filtered, _, err := a.request(ctx, member)
		if err != nil {
			return err
		}
		return resourceGroupNativeProductReady(ctx, a.inner, filtered)
	case *monitorAction:
		// Monitor Execute performs its bound DELETE after the same preflight;
		// it has no hidden cancellation, detach, purge or preparation phase.
		return a.identity(member)
	case *action:
		if a.kind.NativeType == serviceBusMigrationType || recoveryType(a.kind.NativeType) {
			return serviceDenied("resource_group_independent_preparation_required")
		}
		return a.client.resourceGroupAttachmentsReady(ctx, member)
	default:
		return serviceDenied("resource_group_native_preparation_contract_required")
	}
}

// Product Preflight may permit Execute to prepare retention. A native group
// DELETE skips product Execute, so every required attachment update must already
// be visible in the live configuration before submitting the group cascade.
func (c *client) resourceGroupAttachmentsReady(ctx context.Context, member contracts.ActionRequest) error {
	if member.Asset.Identity.NativeType != vmType && member.Asset.Identity.NativeType != nicType {
		return nil
	}
	live, err := c.deploymentStackMemberRead(ctx, member.Asset)
	if err != nil {
		return err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return err
	}
	kind, _ := findType(member.Asset.Identity.NativeType)
	a := &action{client: c, kind: kind, id: strings.ToLower(member.Asset.Identity.NativeID)}
	updates, reason, err := a.evaluateAttachments(ctx, member, live.data, locks)
	if err != nil {
		return err
	}
	if reason != "" {
		return serviceDenied(reason)
	}
	if len(updates) != 0 {
		return serviceDenied("resource_group_attachment_preparation_pending")
	}
	return nil
}

// Prerequisites retain their actual product readback requirements; own ARM 404
// alone cannot hide a soft-deleted or otherwise residual product.
func (r *Runtime) resourceGroupPrerequisitesAbsent(ctx context.Context, c *client, req contracts.ActionRequest) error {
	for _, impact := range req.PrerequisiteDeletions {
		member := contracts.ActionRequest{Asset: impact.Asset, Action: "delete", IdempotencyKey: req.IdempotencyKey + ":prerequisite:" + string(impact.Asset.ID)}
		driver, err := r.ResolveAction(ctx, req.Asset.Identity.ConnectionID, member.Asset)
		if err != nil {
			return err
		}
		result, err := c.deploymentStackProductReadback(ctx, member, driver)
		if err != nil {
			return err
		}
		if result.Exists {
			return serviceDenied("resource_group_prerequisite_still_exists")
		}
	}
	return nil
}

// ARM resource lists can omit Monitor extension resources such as scoped budgets.
// Query their native indexes even when the generic group list is empty.
func (c *client) resourceGroupMonitorMembers(ctx context.Context, req contracts.ActionRequest, indexed map[string]any) error {
	known := map[string]map[string]any{}
	for id, raw := range indexed {
		known[id] = object(raw)
	}
	members, err := c.monitorManagedGroupMembers(ctx, strings.ToLower(req.Asset.Identity.NativeID), known)
	if err != nil {
		return err
	}
	reviewed := map[string]contracts.ActionImpact{}
	for _, impact := range req.LifecycleImpacts {
		reviewed[strings.ToLower(impact.Asset.Identity.NativeID)] = impact
	}
	for _, raw := range members {
		id := strings.ToLower(text(raw["id"]))
		impact, found := reviewed[id]
		if !found || !impact.Delete || !strings.EqualFold(impact.Asset.Identity.NativeType, text(raw["type"])) {
			return serviceDenied("resource_group_monitor_member_not_reviewed")
		}
		live, err := c.deploymentStackMemberRead(ctx, impact.Asset)
		if err != nil {
			return err
		}
		if c.privateConfiguration(monitorResourceSnapshot(impact.Asset.Identity.NativeType, raw)) != c.privateConfiguration(monitorResourceSnapshot(impact.Asset.Identity.NativeType, live.data)) {
			return serviceDenied("resource_group_monitor_member_changed")
		}
		if err = c.deploymentStackPreparedMember(impact.Asset, live.data, nil); err != nil {
			return err
		}
		indexed[id] = live.data
	}
	return nil
}

// Native group enumeration and every product's own closure/preflight are separate
// requirements. The generic index alone cannot establish hidden child membership.
func (r *Runtime) resourceGroupPreflight(ctx context.Context, req contracts.ActionRequest) (err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return err
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		return err
	}
	if req.ExecutionResult != nil {
		return serviceDenied("resource_group_requires_operation_resume")
	}
	var first map[string]any
	for pass := 0; pass < 2; pass++ {
		if _, err = c.resourceGroupReviewedRead(ctx, req); err != nil {
			return err
		}
		current, err := c.reviewedResourceGroupIndex(ctx, req, req.Asset, nil, nil, nil, func(indexed map[string]any) error {
			return c.resourceGroupMonitorMembers(ctx, req, indexed)
		})
		if err != nil {
			return err
		}
		if pass == 0 {
			first = current
		} else if c.privateConfiguration(first) != c.privateConfiguration(current) {
			return serviceDenied("resource_group_members_changed")
		}
		scope := &resourceGroupMonitorScope{client: c, group: strings.ToLower(req.Asset.Identity.NativeID), indexed: current, targets: map[string]asset.Asset{strings.ToLower(req.Asset.Identity.NativeID): req.Asset}}
		for _, member := range products {
			scope.targets[strings.ToLower(member.Asset.Identity.NativeID)] = member.Asset
		}
		// Includes group-scoped diagnostics and RBAC; these are not silently dropped
		// as they are from managed-group cascade enumeration. Existing wrappers check
		// both the recorded prerequisite relationship and its current absence.
		wrapper := &monitorTargetAction{client: c, planned: req.Asset}
		_, targets, err := wrapper.request(ctx, req)
		if err != nil {
			return err
		}
		if err = wrapper.dependenciesInGroup(ctx, req, targets, scope); err != nil {
			return err
		}
		if err = r.resourceGroupCheckProducts(ctx, req, products, scope); err != nil {
			return err
		}
		if err = r.resourceGroupPrerequisitesAbsent(ctx, c, req); err != nil {
			return err
		}
		for _, impact := range req.LifecycleImpacts {
			if impact.Delete {
				continue
			}
			live, err := c.deploymentStackMemberRead(ctx, impact.Asset)
			if err != nil {
				return err
			}
			if err = c.deploymentStackPreparedMember(impact.Asset, live.data, nil); err != nil {
				return err
			}
		}

	}
	_, err = c.resourceGroupReviewedRead(ctx, req)
	return err
}

// Submit the reviewed native cascade once. Never resubmit an accepted operation
// to recover its result. Product preparation requirements remain explicit.
func (r *Runtime) resourceGroupStartDelete(ctx context.Context, req contracts.ActionRequest) (out contracts.ActionResult, err error) {
	defer func() {
		if err != nil {
			out = contracts.ActionResult{}
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	if err = r.resourceGroupPreflight(ctx, req); err != nil {
		return out, err
	}
	metadata, err := providerData()
	if err != nil {
		return out, err
	}
	operation, found := metadata.catalog.Operation("Azure.ResourceManagementClient.ResourceGroups_Delete")
	if !found {
		return out, serviceDenied("resource_group_delete_operation_missing")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": last(req.Asset.Identity.NativeID)})
	if err != nil {
		return out, err
	}
	headers := maps.Clone(bound.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["x-ms-client-request-id"] = azureRequestID(req.IdempotencyKey + ":resource-group-delete")
	res, err := c.requestBody(ctx, bound.Method, bound.URL, bound.Body, headers)
	if err != nil {
		return out, err
	}
	receipt, err := c.resourceGroupOperationReceipt(req, res)
	if err != nil {
		return out, err
	}
	return contracts.ActionResult{ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: receipt}, nil
}

// A completed native operation still needs group and actual product readbacks.
// Native-cascaded members have no fabricated independent execution receipt.
func (r *Runtime) resourceGroupResumeDeletion(ctx context.Context, req contracts.ActionRequest, saved map[string]any) (out contracts.WaitResult, err error) {
	defer func() {
		if err != nil {
			out = contracts.WaitResult{}
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		return out, err
	}
	out, err = c.resourceGroupPollOperation(ctx, req, saved)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	allAbsent, err := r.resourceGroupProductsAbsent(ctx, c, req, products)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	out.Done = out.Done && allAbsent
	return out, nil
}

func (r *Runtime) resourceGroupProductsAbsent(ctx context.Context, c *client, req contracts.ActionRequest, products map[asset.AssetID]contracts.ActionRequest) (bool, error) {
	var err error
	allAbsent := true
	for pass := 0; pass < 2; pass++ {
		group, readErr := c.deploymentStackMemberRead(ctx, req.Asset)
		if readErr != nil && !isNotFound(readErr) {
			return false, readErr
		}
		if readErr == nil {
			if group.data["location"] != req.Asset.Normalized["_resource_group_location"] {
				return false, serviceDenied("resource_group_location_changed")
			}
			if err = serviceCreationIdentity(req.Asset, group.data); err != nil {
				return false, err
			}
			allAbsent = false
		}
		for _, id := range slices.Sorted(maps.Keys(products)) {
			member := products[id]
			driver, resolveErr := r.ResolveAction(ctx, req.Asset.Identity.ConnectionID, member.Asset)
			if resolveErr != nil {
				return false, resolveErr
			}
			read, readErr := c.deploymentStackProductReadback(ctx, member, driver)
			if readErr != nil {
				return false, readErr
			}
			allAbsent = allAbsent && !read.Exists
		}
		for _, impact := range req.LifecycleImpacts {
			if impact.Delete {
				continue
			}
			live, readErr := c.deploymentStackMemberRead(ctx, impact.Asset)
			if readErr != nil {
				return false, readErr
			}
			if err = serviceCreationIdentity(impact.Asset, live.data); err != nil {
				return false, err
			}
		}
		if err = r.resourceGroupPrerequisitesAbsent(ctx, c, req); err != nil {
			return false, err
		}
		// A group recreated during member reads must not be hidden by the earlier 404.
		_, readErr = c.deploymentStackMemberRead(ctx, req.Asset)
		if readErr != nil && !isNotFound(readErr) {
			return false, readErr
		}
		allAbsent = allAbsent && isNotFound(readErr)
	}
	return allAbsent, nil
}
