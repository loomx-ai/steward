package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackPreparationState struct {
	Completed []map[string]any `json:"completed"`
	Active    map[string]any   `json:"active"`
}

// A VM preparation already checks and prepares its reviewed deleted NICs. Only
// NICs outside those VM projections need an independent preparation step.
func (c *client) deploymentStackPreparationOrder(req contracts.ActionRequest) ([]asset.AssetID, error) {
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		return nil, err
	}
	selected, covered := map[asset.AssetID]bool{}, map[asset.AssetID]bool{}
	for _, impact := range req.LifecycleImpacts {
		if !impact.Delete || impact.Asset.Identity.NativeType != vmType {
			continue
		}
		member, err := c.deploymentStackMemberRequest(req, impact.Asset.ID)
		if err != nil {
			return nil, err
		}
		selected[impact.Asset.ID] = true
		for _, child := range member.LifecycleImpacts {
			if child.Delete && child.Asset.Identity.NativeType == nicType {
				covered[child.Asset.ID] = true
			}
		}
	}
	for _, impact := range req.LifecycleImpacts {
		if impact.Delete && impact.Asset.Identity.NativeType == nicType && !covered[impact.Asset.ID] {
			selected[impact.Asset.ID] = true
		}
	}
	order := make([]asset.AssetID, 0, len(selected))
	for id := range selected {
		order = append(order, id)
	}
	slices.Sort(order)
	return order, nil
}

func (c *client) deploymentStackPreparationStateBinding(req contracts.ActionRequest, state deploymentStackPreparationState) (string, error) {
	if req.IdempotencyKey == "" {
		return "", serviceDenied("deployment_stack_execution_identity_missing")
	}
	order, err := c.deploymentStackPreparationOrder(req)
	if err != nil {
		return "", err
	}
	if _, err = c.deploymentStackPreparedConfigurations(req, state.Completed); err != nil {
		return "", err
	}
	seen := map[asset.AssetID]bool{}
	for _, receipt := range state.Completed {
		id := asset.AssetID(text(receipt["member"]))
		if seen[id] || !slices.Contains(order, id) {
			return "", serviceDenied("invalid_deployment_stack_preparation_stage_member")
		}
		seen[id] = true
	}
	if state.Active != nil {
		id := asset.AssetID(text(state.Active["member"]))
		if seen[id] || !slices.Contains(order, id) || state.Active["phase"] != "prepare_attachments" {
			return "", serviceDenied("invalid_deployment_stack_active_preparation")
		}
		if _, err = c.deploymentStackPreparationConfigurations(req, []map[string]any{state.Active}, true); err != nil {
			return "", err
		}
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	wire, err := json.Marshal(state)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_preparation_state")
	}
	var canonical map[string]any
	if err = json.Unmarshal(wire, &canonical); err != nil {
		return "", err
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-preparation-stage-1", "request": payload, "state": canonical}), nil
}

// Called after complete scope review. Preparation never DELETEs a member, and
// stage Done only permits subsequent preflight; it is not cleanup completion.
func (c *client) deploymentStackAdvancePreparations(ctx context.Context, req contracts.ActionRequest, saved map[string]any) (out contracts.WaitResult, err error) {
	defer func() {
		if err != nil {
			out = contracts.WaitResult{}
			err = contracts.DependencyReadError(err)
		}
	}()
	state, err := c.deploymentStackReadPreparationState(req, saved)
	if err != nil {
		return out, err
	}
	checkpoint := func(done bool, status string, retry time.Duration) (contracts.WaitResult, error) {
		binding, err := c.deploymentStackPreparationStateBinding(req, state)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		return contracts.WaitResult{Done: done, State: status, RetryAfter: retry, Data: map[string]any{"state": state, "binding": binding}}, nil
	}
	var result contracts.WaitResult
	if state.Active != nil {
		result, err = c.deploymentStackPrepareMember(ctx, req, asset.AssetID(text(state.Active["member"])), state.Active)
	} else {
		if err = c.deploymentStackObservePreparedMembers(ctx, req, state.Completed...); err != nil {
			return out, err
		}
		order, failure := c.deploymentStackPreparationOrder(req)
		if failure != nil {
			return out, failure
		}
		completed := map[asset.AssetID]bool{}
		for _, receipt := range state.Completed {
			completed[asset.AssetID(text(receipt["member"]))] = true
		}
		var next asset.AssetID
		for _, id := range order {
			if !completed[id] {
				next = id
				break
			}
		}
		if next == "" {
			return checkpoint(true, "retention_prepared", 0)
		}
		result, err = c.deploymentStackPrepareMember(ctx, req, next, nil)
	}
	if err != nil {
		return out, err
	}
	if result.Done {
		state.Completed = append(state.Completed, result.Data)
		state.Active = nil
		return checkpoint(false, "member_retention_prepared", result.RetryAfter)
	}
	state.Active = result.Data
	return checkpoint(false, result.State, result.RetryAfter)
}

func (c *client) deploymentStackReadPreparationState(req contracts.ActionRequest, saved map[string]any) (deploymentStackPreparationState, error) {
	var state deploymentStackPreparationState
	if saved != nil {
		if len(saved) != 2 || saved["state"] == nil {
			return deploymentStackPreparationState{}, serviceDenied("invalid_deployment_stack_preparation_state")
		}
		wire, err := json.Marshal(saved["state"])
		if err != nil {
			return deploymentStackPreparationState{}, err
		}
		decoder := json.NewDecoder(bytes.NewReader(wire))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&state); err != nil {
			return deploymentStackPreparationState{}, serviceDenied("invalid_deployment_stack_preparation_state")
		}
	}
	binding, err := c.deploymentStackPreparationStateBinding(req, state)
	if err != nil {
		return deploymentStackPreparationState{}, err
	}
	if saved != nil && saved["binding"] != binding {
		return deploymentStackPreparationState{}, serviceDenied("deployment_stack_preparation_state_changed")
	}
	return state, nil
}
