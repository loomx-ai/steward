package azure

import (
	"context"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Keep operation completion separate from resource reconciliation. Neither is
// alone a certificate that all product-specific cleanup consequences succeeded.
type deploymentStackExecutionObservation struct {
	Operation           contracts.WaitResult
	Outcome             deploymentStackOutcome
	ResourcesReconciled bool
}

// Authenticate the persisted execution before any HTTP, resume its native
// operation and observe every reviewed resource even while deletion is pending.
// This detects a missing retained member without waiting for the operation to
// finish. Completed operation receipts still require fresh resource reads.
func (c *client) deploymentStackObserveExecution(ctx context.Context, req contracts.ActionRequest, region string, saved map[string]any) (out deploymentStackExecutionObservation, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackExecutionObservation{}
			err = contracts.DependencyReadError(err)
		}
	}()
	out.Operation, err = c.deploymentStackResumeOperation(ctx, req, region, saved)
	if err != nil {
		return out, err
	}
	out.Outcome, err = c.deploymentStackObserveOutcome(ctx, req)
	if err != nil {
		return out, err
	}
	out.ResourcesReconciled = out.Operation.Done && out.Outcome.StackAbsent
	for _, impact := range req.LifecycleImpacts {
		absent, observed := out.Outcome.MembersAbsent[impact.Asset.ID]
		if !observed || impact.Delete != absent {
			out.ResourcesReconciled = false
		}
	}
	return out, nil
}
