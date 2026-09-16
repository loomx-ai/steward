package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Project the reviewed controller tree; never reconstruct ownership from ARM
// name prefixes or give a member the Stack's category-wide retention parameters.
func (c *client) deploymentStackMemberRequest(req contracts.ActionRequest, id asset.AssetID) (contracts.ActionRequest, error) {
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		return contracts.ActionRequest{}, err
	}
	if req.IdempotencyKey == "" {
		return contracts.ActionRequest{}, serviceDenied("deployment_stack_execution_identity_missing")
	}
	impacts, err := deploymentStackProductImpacts(req)
	if err != nil {
		return contracts.ActionRequest{}, err
	}
	byID := map[asset.AssetID]contracts.ActionImpact{}
	for _, impact := range impacts {
		byID[impact.Asset.ID] = impact
	}
	member, found := byID[id]
	if !found || !member.Delete {
		return contracts.ActionRequest{}, serviceDenied("invalid_deployment_stack_execution_member")
	}
	out := contracts.ActionRequest{Asset: member.Asset, Action: "delete", IdempotencyKey: req.IdempotencyKey + ":member:" + string(id)}
	for _, impact := range impacts {
		if impact.Asset.ID == id {
			continue
		}
		for current := impact; current.ControllerID != req.Asset.ID; current = byID[current.ControllerID] {
			if current.ControllerID == id {
				out.LifecycleImpacts = append(out.LifecycleImpacts, impact)
				break
			}
		}
	}
	for _, prerequisite := range req.PrerequisiteDeletions {
		if prerequisite.ControllerID == id {
			out.PrerequisiteDeletions = append(out.PrerequisiteDeletions, prerequisite)
		}
	}
	// Native product receipts must remain stable under the same impact-order
	// canonicalization used by the outer Stack request binding.
	compare := func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) }
	slices.SortFunc(out.LifecycleImpacts, compare)
	slices.SortFunc(out.PrerequisiteDeletions, compare)
	return out, nil
}

func (c *client) deploymentStackPreparationBinding(req contracts.ActionRequest, saved map[string]any) (string, error) {
	payload, err := deploymentStackRequestPayload(req)
	if err != nil {
		return "", err
	}
	value := maps.Clone(saved)
	delete(value, "binding")
	// Marshal first so an invalid value cannot fall through privateConfiguration's
	// historical error-free API and accidentally hash an empty payload.
	wire, err := json.Marshal(value)
	if err != nil {
		return "", serviceDenied("invalid_deployment_stack_preparation_receipt")
	}
	var canonical map[string]any
	if err := json.Unmarshal(wire, &canonical); err != nil {
		return "", serviceDenied("invalid_deployment_stack_preparation_receipt")
	}
	return c.privateConfiguration(map[string]any{"protocol": "deployment-stack-preparation-1", "request": payload, "receipt": canonical}), nil
}

// Called after the Stack's complete preflight. Performs at most one retention
// mutation per call and never invokes a member DELETE. The returned checkpoint
// must be persisted before calling again; Done means only preparation is ready.
func (c *client) deploymentStackPrepareMember(ctx context.Context, req contracts.ActionRequest, id asset.AssetID, saved map[string]any) (contracts.WaitResult, error) {
	member, err := c.deploymentStackMemberRequest(req, id)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if member.Asset.Identity.NativeType != vmType && member.Asset.Identity.NativeType != nicType {
		return contracts.WaitResult{}, serviceDenied("invalid_deployment_stack_preparation_member")
	}
	if _, err := deploymentStackRequestPayload(req); err != nil {
		return contracts.WaitResult{}, err
	}
	var result contracts.ActionResult
	configurations := map[string]any{}
	if saved != nil {
		binding, err := c.deploymentStackPreparationBinding(req, saved)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(saved) != 5 || saved["binding"] != binding || saved["member"] != string(id) || saved["phase"] != "prepare_attachments" && saved["phase"] != "attachments_prepared" {
			return contracts.WaitResult{}, serviceDenied("deployment_stack_preparation_receipt_changed")
		}
		configurations = maps.Clone(object(saved["configurations"]))
		if configurations == nil {
			return contracts.WaitResult{}, serviceDenied("invalid_deployment_stack_preparation_configurations")
		}
		wire, err := json.Marshal(saved["result"])
		if err != nil || json.Unmarshal(wire, &result) != nil {
			return contracts.WaitResult{}, serviceDenied("invalid_deployment_stack_preparation_receipt")
		}
	}
	// These root reads verify the signed Stack review without rejecting ETag
	// changes caused by a preceding reviewed member retention update.
	if err := c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return contracts.WaitResult{}, err
	}
	kind, _ := findType(member.Asset.Identity.NativeType)
	endpoint, err := c.plannedResourceURL(member.Asset)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	a := &action{client: c, kind: kind, endpoint: endpoint, id: strings.ToLower(member.Asset.Identity.NativeID), location: strings.ToLower(member.Asset.Location)}

	// Retention updates can change ETags, but must not cross a recorded resource
	// incarnation or continue against a missing member after an async write.
	targets := []asset.Asset{member.Asset}
	for _, impact := range member.LifecycleImpacts {
		if impact.Delete && impact.Asset.Identity.NativeType == nicType {
			targets = append(targets, impact.Asset)
		}
	}
	for _, target := range targets {
		live, err := c.deploymentStackMemberRead(ctx, target)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if err := serviceCreationIdentity(target, live.data); err != nil {
			return contracts.WaitResult{}, err
		}
	}
	if saved != nil && saved["phase"] == "prepare_attachments" {
		ready, err := a.attachmentPreparationReady(ctx, member, result)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if !ready.Done {
			ready.Data = maps.Clone(saved)
			return ready, nil
		}
	}
	for _, target := range targets {
		if configuration := object(configurations[strings.ToLower(target.Identity.NativeID)]); configuration != nil {
			live, err := c.deploymentStackMemberRead(ctx, target)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if err := c.deploymentStackPreparedMember(target, live.data, configuration); err != nil {
				return contracts.WaitResult{}, err
			}
		}
	}
	// Re-evaluate even a completed preparation checkpoint; it is not permission
	// to skip the live attachment settings before the subsequent native delete.
	result, err = a.prepareAttachmentMutation(ctx, member)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	phase := text(result.Data["phase"])
	if phase != "attachments_prepared" && phase != "prepare_attachments" {
		return contracts.WaitResult{}, serviceDenied("deployment_stack_preparation_member_missing")
	}
	if phase == "prepare_attachments" {
		configurations[text(result.Data["target"])] = map[string]any{"configuration": result.Data["expected_configuration"], "creation": result.Data["creation_generation"]}
	}
	if phase == "attachments_prepared" {
		// Record even targets that needed no write. Aggregate completed stages
		// must detect later drift without re-running a mutation-capable helper.
		for _, target := range targets {
			key := strings.ToLower(target.Identity.NativeID)
			live, err := c.deploymentStackMemberRead(ctx, target)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if prior := object(configurations[key]); prior != nil {
				if err := c.deploymentStackPreparedMember(target, live.data, prior); err != nil {
					return contracts.WaitResult{}, err
				}
			}
			snapshot, err := attachmentPreparedConfiguration(target.Identity.NativeType, live.data, nil)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			configurations[key] = map[string]any{"configuration": c.privateConfiguration(snapshot), "creation": creationGeneration(live.data)}
		}
	}
	next := map[string]any{"member": string(id), "phase": phase, "result": result, "configurations": configurations}
	next["binding"], err = c.deploymentStackPreparationBinding(req, next)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: phase == "attachments_prepared", State: phase, Data: next, RetryAfter: 2 * time.Second}, nil
}
