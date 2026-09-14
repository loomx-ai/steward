package gcp

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const billingBudgetDelete = "billingbudgets.billingAccounts.budgets.delete"

func (a *action) billingBudgetActionIdentity(request contracts.ActionRequest) error {
	if request.Asset.ID == "" || request.IdempotencyKey == "" || request.Action != "delete" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || len(request.Parameters)+len(request.PrerequisiteDeletions)+len(request.LifecycleImpacts) != 0 {
		return groupDenied("billing_budget_action_review_changed")
	}
	parentKey := billingBudgetAccountReview
	if _, project := request.Asset.Normalized[billingBudgetProjectReview]; project {
		if _, account := request.Asset.Normalized[billingBudgetAccountReview]; account || request.Asset.Normalized[billingBudgetProject] != "projects/"+a.client.project {
			return groupDenied("billing_budget_project_review_changed")
		}
		parentKey = billingBudgetProjectReview
	} else if _, project := request.Asset.Normalized[billingBudgetProject]; project {
		return groupDenied("billing_budget_project_review_changed")
	}
	for _, key := range []string{billingBudgetReview, parentKey} {
		proof, err := hex.DecodeString(text(request.Asset.Normalized[key]))
		if err != nil || len(proof) != 32 {
			return groupDenied("billing_budget_action_review_missing")
		}
	}
	_, parent, err := billingBudgetName(a.identity.NativeID)
	if err != nil {
		return err
	}
	if request.Asset.Normalized["billing_account"] != parent {
		return groupDenied("billing_budget_account_review_changed")
	}
	return nil
}

func (a *action) billingBudgetReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := ctx.Err(); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.billingBudgetActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	name, parent, err := billingBudgetName(a.identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	project := text(request.Asset.Normalized[billingBudgetProjectReview]) != ""
	parentKey := billingBudgetAccountReview
	if project {
		parentKey = billingBudgetProjectReview
	}
	accountRead := func() error {
		data, err := a.client.billingBudgetParent(ctx, parent, project)
		if err != nil {
			return err
		}
		if firewallDigest(data) != text(request.Asset.Normalized[parentKey]) {
			return groupDenied("billing_budget_account_configuration_changed")
		}
		return nil
	}
	if err := accountRead(); err != nil {
		return contracts.ReadbackResult{}, err
	}
	result, readErr := a.client.billingReadResult(ctx, "billingbudgets.billingAccounts.budgets.get", name)
	if readErr != nil && !isNotFound(readErr) {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(readErr)
	}
	if err := accountRead(); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if isNotFound(readErr) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if _, err := a.client.billingBudgetData(parent, result.Data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if project {
		if err := a.client.billingSingleProject(result.Data); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	if firewallDigest(result.Data) != text(request.Asset.Normalized[billingBudgetReview]) {
		return contracts.ReadbackResult{}, groupDenied("billing_budget_configuration_changed")
	}
	return contracts.ReadbackResult{Exists: true}, nil
}

func billingBudgetPhase(request contracts.ActionRequest) map[string]any {
	review := map[string]any{"asset_id": request.Asset.ID, "identity": request.Asset.Identity, "configuration": request.Asset.Normalized[billingBudgetReview], "account": request.Asset.Normalized[billingBudgetAccountReview], "action": request.Action, "idempotency_key": request.IdempotencyKey}
	if project, ok := request.Asset.Normalized[billingBudgetProjectReview]; ok {
		review["project_configuration"] = project
		review["project"] = request.Asset.Normalized[billingBudgetProject]
	}
	return map[string]any{"phase": "billing_budget_delete", "review": firewallDigest(review)}
}

func (a *action) executeBillingBudget(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	read, err := a.billingBudgetReadback(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !read.Exists {
		return contracts.ActionResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, "DELETE", a.endpoint, nil, nil)
	if isNotFound(err) {
		live, readErr := a.billingBudgetReadback(ctx, request)
		if readErr != nil {
			return contracts.ActionResult{}, readErr
		}
		if live.Exists {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
	} else if err != nil {
		return contracts.ActionResult{}, err
	} else if len(response.Data) != 0 {
		return contracts.ActionResult{}, groupDenied("billing_budget_delete_response_invalid")
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, Data: billingBudgetPhase(request), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitBillingBudget(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.billingBudgetActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if result.ProviderOperationID != "" || len(result.Data) != 0 && firewallDigest(result.Data) != firewallDigest(billingBudgetPhase(request)) {
		return contracts.WaitResult{}, groupDenied("billing_budget_delete_receipt_changed")
	}
	read, err := a.billingBudgetReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
}
