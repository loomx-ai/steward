package azure

import (
	"context"
	"maps"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Resolve the operation region from fresh native state, matching the inventory
// contract for resource-group Stacks whose GET omits location.
func (c *client) deploymentStackDeleteRegion(ctx context.Context, req contracts.ActionRequest) (string, error) {
	current, err := c.deploymentStackProtectedRead(ctx, req)
	if err != nil {
		return "", err
	}
	region := strings.ToLower(text(current.data["location"]))
	scope, _, err := deploymentStackParameters(req.Asset.Identity.NativeID)
	if err != nil {
		return "", err
	}
	if region == "" && scope == "ResourceGroup" {
		parent := req.Asset.Identity.NativeID[:strings.LastIndex(strings.ToLower(req.Asset.Identity.NativeID), "/providers/microsoft.resources/deploymentstacks/")]
		group, err := c.request(ctx, "GET", apiURL(parent, resourcesVersion))
		if err != nil {
			return "", err
		}
		if group.status != 200 || !strings.EqualFold(text(group.data["id"]), parent) || !strings.EqualFold(text(group.data["type"]), groupType) || group.data["error"] != nil || operationLocation(group.header) != "" {
			return "", serviceDenied("invalid_deployment_stack_group_read")
		}
		region = strings.ToLower(text(group.data["location"]))
	}
	if err = c.deploymentStackReceiptOwner(req.Asset.Identity.NativeID, region); err != nil {
		return "", err
	}
	if !strings.EqualFold(req.Asset.Location, region) {
		return "", serviceDenied("deployment_stack_operation_region_changed")
	}
	return region, nil
}

// Submit once after setup and fresh native/product scope checks. Persist the
// returned execution receipt and use ResumeOperation/ProductOutcome afterwards;
// a saved operation must never be resumed by calling this submission helper.
// This does not register a Stack action or certify complete family acceptance.
func (r *Runtime) deploymentStackStartDelete(ctx context.Context, req contracts.ActionRequest, setup map[string]any) (out contracts.ActionResult, err error) {
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
	state, err := c.deploymentStackReadSetupState(req, setup)
	if err != nil {
		return out, err
	}
	if setup == nil || state.Phase != "prerequisites" || state.Prerequisites == nil {
		return out, serviceDenied("deployment_stack_setup_not_complete")
	}
	prerequisites, err := c.deploymentStackReadPrerequisiteState(req, deploymentStackProgress{}, state.Prerequisites)
	if err != nil {
		return out, err
	}
	if prerequisites.Active != nil {
		return out, serviceDenied("deployment_stack_setup_not_complete")
	}
	bound, _, err := c.deploymentStackDeletePlan(req)
	if err != nil {
		return out, err
	}
	order, err := r.deploymentStackOrderPrerequisites(ctx, req, prerequisites.Progress)
	if err != nil {
		return out, err
	}
	if len(order) != 0 {
		return out, serviceDenied("deployment_stack_prerequisites_remaining")
	}
	if _, err = r.deploymentStackObserveGroupClosureWithProgress(ctx, req, prerequisites.Progress); err != nil {
		return out, err
	}
	checks, err := r.deploymentStackPreflightProductsWithProgress(ctx, req, prerequisites.Progress)
	if err != nil {
		return out, err
	}
	for _, check := range checks {
		if !check.Check.Allowed || check.Check.Absent {
			reason := check.Check.Reason
			if reason == "" {
				reason = "deployment_stack_product_preflight_blocked"
			}
			return out, serviceDenied(reason)
		}
	}
	region, err := c.deploymentStackDeleteRegion(ctx, req)
	if err != nil {
		return out, err
	}
	if err = c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return out, err
	}
	headers := maps.Clone(bound.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["x-ms-client-request-id"] = azureRequestID(req.IdempotencyKey + ":stack-delete")
	response, err := c.requestBody(ctx, bound.Method, bound.URL, bound.Body, headers)
	if err != nil {
		return out, err
	}
	receipt, err := c.deploymentStackExecutionReceipt(req, region, response)
	if err != nil {
		return out, err
	}
	checkpoint, err := c.deploymentStackDeletionCheckpoint(req, setup, region, receipt)
	if err != nil {
		return out, err
	}
	return contracts.ActionResult{ProviderRequestID: response.requestID, ProviderOperationID: operationLocation(response.header), RetryAfter: retryAfter(response.header), Data: checkpoint}, nil
}

// Recheck root protection before setup work as well as native submission. A
// reviewed member operation must not modify a protected or locked Stack plan.
func (c *client) deploymentStackProtectedRead(ctx context.Context, req contracts.ActionRequest) (response, error) {
	current, err := c.deploymentStackRead(ctx, req.Asset.Identity.NativeID)
	if err != nil {
		return response{}, err
	}
	review := object(req.Asset.Normalized[deploymentStackReviewKey])
	if review["configuration"] != c.privateConfiguration(current.data) {
		return response{}, serviceDenied("deployment_stack_live_configuration_changed")
	}
	if protectedAzureTags(object(current.data["tags"])) {
		return response{}, serviceDenied("azure_protected_tag")
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return response{}, err
	}
	if locked(strings.ToLower(req.Asset.Identity.NativeID), locks) {
		return response{}, serviceDenied("azure_management_lock")
	}
	return current, nil
}
