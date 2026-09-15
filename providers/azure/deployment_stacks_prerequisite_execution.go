package azure

import (
	"context"
	"encoding/json"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackPrerequisiteState struct {
	Progress deploymentStackProgress `json:"progress"`
	Active   map[string]any          `json:"active"`
}

func (c *client) deploymentStackPrerequisiteBinding(req contracts.ActionRequest, state deploymentStackPrerequisiteState) (string, error) {
	if req.IdempotencyKey == "" {
		return "", serviceDenied("deployment_stack_execution_identity_missing")
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	if _, err := c.deploymentStackPreparedConfigurations(req, state.Progress.Preparations); err != nil {
		return "", err
	}
	seen := map[asset.AssetID]bool{}
	for _, receipt := range state.Progress.Executions {
		id := asset.AssetID(text(receipt["member"]))
		_, phase, _, err := c.deploymentStackMemberExecutionResult(req, id, receipt)
		if err != nil {
			return "", err
		}
		if phase != "complete" || seen[id] {
			return "", serviceDenied("deployment_stack_member_completion_not_unique_or_final")
		}
		seen[id] = true
	}
	if state.Active != nil {
		id := asset.AssetID(text(state.Active["member"]))
		_, phase, _, err := c.deploymentStackMemberExecutionResult(req, id, state.Active)
		if err != nil {
			return "", err
		}
		if phase == "complete" || seen[id] {
			return "", serviceDenied("invalid_deployment_stack_active_prerequisite")
		}
	}
	wire, err := json.Marshal(state)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_prerequisite_state")
	}
	var canonical map[string]any
	if err = json.Unmarshal(wire, &canonical); err != nil {
		return "", err
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-prerequisite-execution-1", "request": payload, "state": canonical}), nil
}

// Advance at most one member lifecycle phase after the caller's complete scope
// review and retention preparation. Persist each result before calling again.
// Done covers this prerequisite stage only, never the complete Stack action.
func (r *Runtime) deploymentStackAdvancePrerequisites(ctx context.Context, req contracts.ActionRequest, initial deploymentStackProgress, saved map[string]any) (out contracts.WaitResult, err error) {
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
	if saved != nil && len(initial.Preparations)+len(initial.Executions) != 0 {
		return out, serviceDenied("deployment_stack_prerequisite_progress_changed_during_resume")
	}
	state := deploymentStackPrerequisiteState{Progress: initial}
	if saved != nil {
		if len(saved) != 2 || saved["state"] == nil {
			return out, serviceDenied("invalid_deployment_stack_prerequisite_state")
		}
		wire, err := json.Marshal(saved["state"])
		if err != nil {
			return out, serviceDenied("invalid_deployment_stack_prerequisite_state")
		}
		if err = json.Unmarshal(wire, &state); err != nil {
			return out, serviceDenied("invalid_deployment_stack_prerequisite_state")
		}
		// Canonical round-trip comparison rejects ignored or unknown state fields.
		var decoded map[string]any
		canonical, err := json.Marshal(state)
		if err != nil {
			return out, err
		}
		if err = json.Unmarshal(canonical, &decoded); err != nil {
			return out, err
		}
		var original map[string]any
		if err = json.Unmarshal(wire, &original); err != nil {
			return out, err
		}
		if c.privateConfiguration(original) != c.privateConfiguration(decoded) {
			return out, serviceDenied("invalid_deployment_stack_prerequisite_state")
		}
	}
	binding, err := c.deploymentStackPrerequisiteBinding(req, state)
	if err != nil {
		return out, err
	}
	if saved != nil && saved["binding"] != binding {
		return out, serviceDenied("deployment_stack_prerequisite_state_changed")
	}
	// Decode into a fresh object; unmarshalling into the populated state would
	// reuse its maps and slice capacity, including caller-owned receipt maps.
	wire, err := json.Marshal(state)
	if err != nil {
		return out, err
	}
	var owned deploymentStackPrerequisiteState
	if err = json.Unmarshal(wire, &owned); err != nil {
		return out, err
	}
	state = owned
	checkpoint := func(done bool, status string, retry time.Duration) (contracts.WaitResult, error) {
		binding, err := c.deploymentStackPrerequisiteBinding(req, state)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		return contracts.WaitResult{Done: done, State: status, RetryAfter: retry, Data: map[string]any{"state": state, "binding": binding}}, nil
	}
	var result contracts.WaitResult
	if state.Active != nil {
		// The active target may already return 404 after an accepted DELETE. Resume
		// its exact persisted native phase before enumerating remaining membership.
		id := asset.AssetID(text(state.Active["member"]))
		result, err = r.deploymentStackExecuteMember(ctx, req, id, state.Active)
	} else {
		var order []contracts.ActionImpact
		order, err = r.deploymentStackOrderPrerequisites(ctx, req, state.Progress)
		if err != nil {
			return out, err
		}
		if len(order) == 0 {
			return checkpoint(true, "prerequisites_ready", 0)
		}
		result, err = r.deploymentStackExecuteMemberWithProgress(ctx, req, order[0].Asset.ID, nil, state.Progress)
	}
	if err != nil {
		return out, err
	}
	if result.Done {
		state.Progress.Executions = append(state.Progress.Executions, result.Data)
		state.Active = nil
		// Re-enumeration happens on the next persisted turn, including on an otherwise
		// completed stage resume. This never chains two member mutations in one call.
		return checkpoint(false, "prerequisite_verified", result.RetryAfter)
	}
	state.Active = result.Data
	return checkpoint(false, result.State, result.RetryAfter)
}
