package azure

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) deploymentStackMemberExecutionBinding(req contracts.ActionRequest, saved map[string]any) (string, error) {
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	value := maps.Clone(saved)
	delete(value, "binding")
	wire, err := json.Marshal(value)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_member_execution_receipt")
	}
	var canonical map[string]any
	if err := json.Unmarshal(wire, &canonical); err != nil {
		return "", serviceDenied("invalid_deployment_stack_member_execution_receipt")
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-member-execution-1", "request": payload, "receipt": canonical}), nil
}

// Run an independently reviewed member through its actual product lifecycle.
// The complete Stack preflight and dependency ordering precede this call. Persist
// each returned checkpoint before calling again. This never deletes the Stack,
// detaches its membership, changes deny settings or bypasses an out-of-sync error.
func (r *Runtime) deploymentStackExecuteMember(ctx context.Context, req contracts.ActionRequest, id asset.AssetID, saved map[string]any) (out contracts.WaitResult, err error) {
	// A missing member or its dependency is not proof that the Stack is absent.
	defer func() { err = contracts.DependencyReadError(err) }()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	member, err := c.deploymentStackMemberRequest(req, id)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if _, err := deploymentStackRequestPayload(req); err != nil {
		return contracts.WaitResult{}, err
	}
	phase := "execute"
	var result contracts.ActionResult
	if saved != nil {
		phase, result, err = c.deploymentStackMemberExecutionResult(req, id, saved)
		if err != nil {
			return contracts.WaitResult{}, err
		}
	}
	if err := c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return contracts.WaitResult{}, err
	}
	driver, err := r.ResolveAction(ctx, member.Asset.Identity.ConnectionID, member.Asset)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	checkpoint := func(phase, state string, done bool) (contracts.WaitResult, error) {
		next := map[string]any{"member": string(id), "phase": phase, "result": result}
		binding, err := c.deploymentStackMemberExecutionBinding(req, next)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		next["binding"] = binding
		return contracts.WaitResult{Done: done, State: state, RetryAfter: result.RetryAfter, Data: next}, nil
	}
	switch phase {
	case "execute":
		live, err := c.deploymentStackMemberRead(ctx, member.Asset)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if err := c.deploymentStackPreparedMember(member.Asset, live.data, nil); err != nil {
			return contracts.WaitResult{}, err
		}
		result, err = driver.Execute(ctx, member)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		return checkpoint("wait", "member_execution_started", false)
	case "wait":
		wait, err := driver.Wait(ctx, member, result)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if wait.Data != nil {
			result.Data = maps.Clone(wait.Data)
		}
		if wait.RetryAfter > 0 {
			result.RetryAfter = wait.RetryAfter
		}
		if !wait.Done {
			return checkpoint("wait", wait.State, false)
		}
		return checkpoint("readback", "member_execution_finished", false)
	default:
		// Completion is not a cached absence certificate. Re-run the native
		// product readback, including its residual/purge checks, on every resume.
		read, err := c.deploymentStackMemberReadback(ctx, member, driver, result)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if err := c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
			return contracts.WaitResult{}, err
		}
		if read.Exists {
			if result.RetryAfter <= 0 {
				result.RetryAfter = 2 * time.Second
			}
			return checkpoint("readback", read.State, false)
		}
		return checkpoint("complete", "member_absent", true)
	}
}

// Authenticate all checkpoints before resolving or reading any completed member.
func (c *client) deploymentStackMemberExecutionResult(req contracts.ActionRequest, id asset.AssetID, saved map[string]any) (string, contracts.ActionResult, error) {
	binding, err := c.deploymentStackMemberExecutionBinding(req, saved)
	if err != nil {
		return "", contracts.ActionResult{}, err
	}
	phase, _ := saved["phase"].(string)
	if len(saved) != 4 || saved["binding"] != binding || saved["member"] != string(id) || phase != "wait" && phase != "readback" && phase != "complete" {
		return "", contracts.ActionResult{}, serviceDenied("deployment_stack_member_execution_receipt_changed")
	}
	var result contracts.ActionResult
	wire, err := json.Marshal(saved["result"])
	if err != nil || json.Unmarshal(wire, &result) != nil {
		return "", contracts.ActionResult{}, serviceDenied("invalid_deployment_stack_member_execution_receipt")
	}
	return phase, result, nil
}

// The caller verifies the Stack around these product/own reads. This check proves
// only the requested member's result, never absence of its whole controller tree.
func (c *client) deploymentStackMemberReadback(ctx context.Context, member contracts.ActionRequest, driver contracts.ActionDriver, result contracts.ActionResult) (contracts.ReadbackResult, error) {
	member.ExecutionResult = &result
	read, err := driver.Readback(ctx, member)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	live, err := c.deploymentStackMemberRead(ctx, member.Asset)
	if err != nil && !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	absent := isNotFound(err)
	if !absent {
		if err := serviceCreationIdentity(member.Asset, live.data); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	read.Exists = read.Exists || !absent
	return read, nil
}
