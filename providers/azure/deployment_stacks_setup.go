package azure

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackSetupState struct {
	Phase         string         `json:"phase"`
	Preparation   map[string]any `json:"preparation"`
	Prerequisites map[string]any `json:"prerequisites"`
}

func (c *client) deploymentStackSetupBinding(req contracts.ActionRequest, state deploymentStackSetupState) (string, error) {
	preparation, err := c.deploymentStackReadPreparationState(req, state.Preparation)
	if err != nil {
		return "", err
	}
	switch state.Phase {
	case "prepare":
		if state.Prerequisites != nil {
			return "", serviceDenied("deployment_stack_setup_phase_changed")
		}
	case "prerequisites":
		order, err := c.deploymentStackPreparationOrder(req)
		if err != nil {
			return "", err
		}
		if state.Preparation == nil || preparation.Active != nil || len(preparation.Completed) != len(order) {
			return "", serviceDenied("deployment_stack_setup_preparation_incomplete")
		}
		initial := deploymentStackProgress{}
		if state.Prerequisites == nil {
			initial.Preparations = preparation.Completed
		}
		prerequisites, err := c.deploymentStackReadPrerequisiteState(req, initial, state.Prerequisites)
		if err != nil {
			return "", err
		}
		if c.privateConfiguration(map[string]any{"preparations": preparation.Completed}) != c.privateConfiguration(map[string]any{"preparations": prerequisites.Progress.Preparations}) {
			return "", serviceDenied("deployment_stack_setup_preparation_changed")
		}
	default:
		return "", serviceDenied("invalid_deployment_stack_setup_phase")
	}
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	wire, err := json.Marshal(state)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_setup_state")
	}
	var canonical map[string]any
	if err = json.Unmarshal(wire, &canonical); err != nil {
		return "", err
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-setup-1", "request": payload, "state": canonical}), nil
}

// Advance preparation, then independently executed prerequisites. Each result
// must be persisted before the next call. Done covers setup only: the complete
// Stack scope review precedes it, and native deletion/final checks still follow.
func (r *Runtime) deploymentStackAdvanceSetup(ctx context.Context, req contracts.ActionRequest, saved map[string]any) (out contracts.WaitResult, err error) {
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
	state, err := c.deploymentStackReadSetupState(req, saved)
	if err != nil {
		return out, err
	}
	if _, err = c.deploymentStackProtectedRead(ctx, req); err != nil {
		return out, err
	}
	if saved == nil {
		if err = r.deploymentStackPreflightSetup(ctx, req, deploymentStackProgress{}); err != nil {
			return out, err
		}
	}
	var result contracts.WaitResult
	if state.Phase == "prepare" {
		var guard func(context.Context) error
		if saved != nil {
			preparation, failure := c.deploymentStackReadPreparationState(req, state.Preparation)
			if failure != nil {
				return out, failure
			}
			guard = func(ctx context.Context) error {
				progress := deploymentStackProgress{Preparations: preparation.Completed}
				if preparation.Active != nil {
					progress.Preparations = append(progress.Preparations, preparation.Active)
					progress.preparationReady = true
				}
				return r.deploymentStackPreflightSetup(ctx, req, progress)
			}
		}
		result, err = c.deploymentStackAdvancePreparationsWithGuard(ctx, req, state.Preparation, guard)
		if err != nil {
			return out, err
		}
		state.Preparation = result.Data
		if result.Done {
			state.Phase = "prerequisites"
			result.Done = false
			result.State = "retention_prepared"
		}
	} else {
		initial := deploymentStackProgress{}
		if state.Prerequisites == nil {
			preparation, err := c.deploymentStackReadPreparationState(req, state.Preparation)
			if err != nil {
				return out, err
			}
			initial.Preparations = preparation.Completed
		}
		prerequisites, failure := c.deploymentStackReadPrerequisiteState(req, initial, state.Prerequisites)
		if failure != nil {
			return out, failure
		}
		if prerequisites.Active == nil {
			if err = r.deploymentStackPreflightSetup(ctx, req, prerequisites.Progress); err != nil {
				return out, err
			}
		}
		result, err = r.deploymentStackAdvancePrerequisites(ctx, req, initial, state.Prerequisites)
		if err != nil {
			return out, err
		}
		state.Prerequisites = result.Data
		if result.Done {
			result.State = "setup_ready"
		}
	}
	binding, err := c.deploymentStackSetupBinding(req, state)
	if err != nil {
		return out, err
	}
	result.Data = map[string]any{"state": state, "binding": binding}
	return result, nil
}

func (c *client) deploymentStackReadSetupState(req contracts.ActionRequest, saved map[string]any) (deploymentStackSetupState, error) {
	state := deploymentStackSetupState{Phase: "prepare"}
	if saved != nil {
		if len(saved) != 2 || saved["state"] == nil {
			return deploymentStackSetupState{}, serviceDenied("invalid_deployment_stack_setup_state")
		}
		wire, err := json.Marshal(saved["state"])
		if err != nil {
			return deploymentStackSetupState{}, err
		}
		decoder := json.NewDecoder(bytes.NewReader(wire))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&state); err != nil {
			return deploymentStackSetupState{}, serviceDenied("invalid_deployment_stack_setup_state")
		}
	}
	binding, err := c.deploymentStackSetupBinding(req, state)
	if err != nil {
		return deploymentStackSetupState{}, err
	}
	if saved != nil && saved["binding"] != binding {
		return deploymentStackSetupState{}, serviceDenied("deployment_stack_setup_state_changed")
	}
	return state, nil
}
