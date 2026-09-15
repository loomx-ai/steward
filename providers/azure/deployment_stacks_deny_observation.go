package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These are subscription observations, not Stack ownership or authorization to
// remove assignments. Unrelated assignments can legitimately remain or change.
type deploymentStackDenyObservation struct {
	Execution deploymentStackExecutionObservation
	Removed   []string
	Unchanged []string
	Changed   []string
	Added     []string
}

func (c *client) deploymentStackDenyBinding(req contracts.ActionRequest, saved map[string]any) (string, error) {
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		return "", err
	}
	if req.IdempotencyKey == "" {
		return "", serviceDenied("deployment_stack_execution_identity_missing")
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	if len(saved) != 3 || saved["scope"] != c.root() {
		return "", serviceDenied("deployment_stack_deny_snapshot_scope_changed")
	}
	assignments, ok := saved["assignments"].(map[string]any)
	if !ok {
		return "", serviceDenied("invalid_deployment_stack_deny_snapshot")
	}
	seen := map[string]bool{}
	for wire, value := range assignments {
		id, scope, err := denyAssignmentID(wire)
		if err != nil {
			return "", err
		}
		if _, err = c.denyLocalScope(scope); err != nil {
			return "", err
		}
		hash, ok := value.(string)
		if seen[id] || !ok || len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
			return "", serviceDenied("invalid_deployment_stack_deny_snapshot")
		}
		seen[id] = true
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-deny-observation-1", "request": payload, "scope": saved["scope"], "assignments": assignments}), nil
}

// Capture the entire connection subscription before mutation. This scope remains
// queryable if the Stack's containing resource group is deleted. Store private
// fingerprints, not principal lists, conditions or description text.
func (c *client) deploymentStackCaptureDenies(ctx context.Context, req contracts.ActionRequest) (saved map[string]any, err error) {
	defer func() {
		if err != nil {
			saved = nil
			err = contracts.DependencyReadError(err)
		}
	}()
	saved = map[string]any{"scope": c.root(), "assignments": map[string]any{}, "binding": ""}
	if _, err = c.deploymentStackDenyBinding(req, saved); err != nil {
		return nil, err
	}
	if err = c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return nil, err
	}
	rows, _, err := c.denyAssignmentSnapshot(ctx, c.root(), nil)
	if err != nil {
		return nil, err
	}
	assignments := object(saved["assignments"])
	for _, raw := range rows {
		assignments[text(raw["id"])] = c.privateConfiguration(raw)
	}
	if err = c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return nil, err
	}
	saved["binding"], err = c.deploymentStackDenyBinding(req, saved)
	return saved, err
}

// Use the signed baseline as the known-ID set, so omitted list entries still
// require their own GET. Native completion and deny changes stay distinct: the
// generic schema cannot identify which observed assignments belong to this Stack.
func (c *client) deploymentStackObserveDenyChanges(ctx context.Context, req contracts.ActionRequest, region string, execution, baseline map[string]any) (out deploymentStackDenyObservation, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackDenyObservation{}
			err = contracts.DependencyReadError(err)
		}
	}()
	binding, err := c.deploymentStackDenyBinding(req, baseline)
	if err != nil {
		return out, err
	}
	if baseline["binding"] != binding {
		return out, serviceDenied("deployment_stack_deny_snapshot_changed")
	}
	out.Execution, err = c.deploymentStackObserveExecution(ctx, req, region, execution)
	if err != nil {
		return out, err
	}
	known := []string{}
	before := map[string]string{}
	for wire, hash := range object(baseline["assignments"]) {
		id, _, _ := denyAssignmentID(wire)
		known = append(known, wire)
		before[id] = hash.(string)
	}
	rows, absent, err := c.denyAssignmentSnapshot(ctx, c.root(), known)
	if err != nil {
		return out, err
	}
	out.Removed = absent
	for id, raw := range rows {
		previous, found := before[id]
		switch {
		case !found:
			out.Added = append(out.Added, id)
		case previous == c.privateConfiguration(raw):
			out.Unchanged = append(out.Unchanged, id)
		default:
			out.Changed = append(out.Changed, id)
		}
	}
	out.Execution.Outcome, err = c.deploymentStackObserveOutcome(ctx, req)
	if err != nil {
		return out, err
	}
	out.Execution.ResourcesReconciled = deploymentStackResourcesReconciled(req, out.Execution.Operation, out.Execution.Outcome)
	// Stable output across native page order and JSON checkpoint restoration.
	slices.Sort(out.Removed)
	slices.Sort(out.Unchanged)
	slices.Sort(out.Changed)
	slices.Sort(out.Added)
	return out, nil
}
