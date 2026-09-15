package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Bind the native receipt to the entire frozen action, not only the category
// flags. Two plans can use identical native flags while affecting different
// members or retaining different implicit children.
func (c *client) deploymentStackExecutionBinding(req contracts.ActionRequest, region string, native map[string]any) (string, error) {
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		return "", err
	}
	if req.IdempotencyKey == "" {
		return "", serviceDenied("deployment_stack_execution_identity_missing")
	}
	if _, err := c.deploymentStackVerifyReceipt(req.Asset.Identity.NativeID, region, native); err != nil {
		return "", err
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-execution-1", "request": payload, "region": region, "native": native}), nil
}

func deploymentStackRequestPayload(req contracts.ActionRequest) (string, error) {
	canonical := req
	canonical.ExecutionResult = nil
	canonical.LifecycleImpacts = slices.Clone(req.LifecycleImpacts)
	canonical.PrerequisiteDeletions = slices.Clone(req.PrerequisiteDeletions)
	compare := func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) }
	slices.SortFunc(canonical.LifecycleImpacts, compare)
	slices.SortFunc(canonical.PrerequisiteDeletions, compare)
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_execution_request")
	}
	return string(payload), nil
}

func (c *client) deploymentStackExecutionReceipt(req contracts.ActionRequest, region string, res response) (map[string]any, error) {
	_, parameters, err := c.deploymentStackDeletePlan(req)
	if err != nil {
		return nil, err
	}
	native, err := c.deploymentStackDeleteReceipt(req.Asset.Identity.NativeID, region, parameters, res)
	if err != nil {
		return nil, err
	}
	binding, err := c.deploymentStackExecutionBinding(req, region, native)
	if err != nil {
		return nil, err
	}
	return map[string]any{"native": native, "binding": binding}, nil
}

// Resume only the native operation. Its completion must still be followed by
// member, retained-resource and product-consequence verification in the action.
func (c *client) deploymentStackResumeOperation(ctx context.Context, req contracts.ActionRequest, region string, saved map[string]any) (contracts.WaitResult, error) {
	native := object(saved["native"])
	binding, err := c.deploymentStackExecutionBinding(req, region, native)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if len(saved) != 2 || saved["binding"] != binding {
		return contracts.WaitResult{}, serviceDenied("deployment_stack_execution_receipt_changed")
	}
	out, err := c.deploymentStackPoll(ctx, req.Asset.Identity.NativeID, region, native)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	nextBinding, err := c.deploymentStackExecutionBinding(req, region, out.Data)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	out.Data = map[string]any{"native": maps.Clone(out.Data), "binding": nextBinding}
	return out, nil
}
