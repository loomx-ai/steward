package azure

import (
	"context"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) deploymentStackProgressConfigurations(req contracts.ActionRequest, progress deploymentStackProgress) (map[string]any, error) {
	if progress.preparationReady && len(progress.Executions) != 0 {
		return nil, serviceDenied("deployment_stack_pending_preparation_with_execution")
	}
	return c.deploymentStackPreparationConfigurations(req, progress.Preparations, progress.preparationReady)
}

func (r *Runtime) deploymentStackPreflightSetup(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress) error {
	if _, err := r.deploymentStackObserveGroupClosureWithProgress(ctx, req, progress); err != nil {
		return err
	}
	checks, err := r.deploymentStackCheckProducts(ctx, req, progress, true)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if !check.Check.Allowed || check.Check.Absent {
			return serviceDenied("deployment_stack_setup_product_not_ready")
		}
	}
	return nil
}
