package azure

import (
	"context"
	"encoding/json"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// The persisted deletion context retains the exact preparation and independent
// member receipts after the Stack disappears. It is not a cleanup certificate.
func (c *client) deploymentStackDeletionCheckpoint(req contracts.ActionRequest, setup map[string]any, region string, execution map[string]any) (map[string]any, error) {
	state, err := c.deploymentStackReadSetupState(req, setup)
	if err != nil {
		return nil, err
	}
	if setup == nil || state.Phase != "prerequisites" || state.Prerequisites == nil {
		return nil, serviceDenied("deployment_stack_setup_not_complete")
	}
	prerequisites, err := c.deploymentStackReadPrerequisiteState(req, deploymentStackProgress{}, state.Prerequisites)
	if err != nil {
		return nil, err
	}
	if prerequisites.Active != nil {
		return nil, serviceDenied("deployment_stack_setup_not_complete")
	}
	nativeBinding, err := c.deploymentStackExecutionBinding(req, region, object(execution["native"]))
	if err != nil {
		return nil, err
	}
	if len(execution) != 2 || execution["binding"] != nativeBinding {
		return nil, serviceDenied("deployment_stack_execution_receipt_changed")
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return nil, err
	}
	// Clone through a fresh JSON object: callers must not share nested checkpoint
	// maps or slices with either input, including typed in-memory setup state.
	wire, err := json.Marshal(map[string]any{"setup": setup, "region": region, "execution": execution})
	if err != nil {
		return nil, err
	}
	var checkpoint map[string]any
	if err = json.Unmarshal(wire, &checkpoint); err != nil {
		return nil, err
	}
	checkpoint["binding"] = c.privateConfiguration(map[string]any{"protocol": "deployment-stack-deletion-1", "request": payload, "checkpoint": checkpoint})
	return checkpoint, nil
}

type deploymentStackDeletionObservation struct {
	Products deploymentStackProductObservation
	Data     map[string]any
}

// Resume is read-only, even for a pending operation or a recreated resource.
// Product reconciliation does not establish deny ownership/release or complete
// family acceptance. Keep that evidence separate from the native operation.
func (r *Runtime) deploymentStackResumeDeletion(ctx context.Context, req contracts.ActionRequest, saved map[string]any) (out deploymentStackDeletionObservation, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackDeletionObservation{}
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	if len(saved) != 4 {
		return out, serviceDenied("invalid_deployment_stack_deletion_checkpoint")
	}
	checkpoint, err := c.deploymentStackDeletionCheckpoint(req, object(saved["setup"]), text(saved["region"]), object(saved["execution"]))
	if err != nil {
		return out, err
	}
	if saved["binding"] != checkpoint["binding"] {
		return out, serviceDenied("deployment_stack_deletion_checkpoint_changed")
	}
	state, err := c.deploymentStackReadSetupState(req, object(checkpoint["setup"]))
	if err != nil {
		return out, err
	}
	prerequisites, err := c.deploymentStackReadPrerequisiteState(req, deploymentStackProgress{}, state.Prerequisites)
	if err != nil {
		return out, err
	}
	out.Products, err = r.deploymentStackObserveProductOutcome(ctx, req, text(checkpoint["region"]), object(checkpoint["execution"]), prerequisites.Progress.Executions...)
	if err != nil {
		return out, err
	}
	out.Data, err = c.deploymentStackDeletionCheckpoint(req, object(checkpoint["setup"]), text(checkpoint["region"]), out.Products.Execution.Operation.Data)
	return out, err
}
