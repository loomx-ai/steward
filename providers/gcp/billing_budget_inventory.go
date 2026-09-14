package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const billingBudgetSource = "billing-budgets-visible"
const billingBudgetReview = "_billing_budget_configuration"
const billingBudgetAccountReview = "_billing_budget_account_configuration"

func redactBillingBudgetPayload(data map[string]any) {
	name := strings.TrimPrefix(text(data["name"]), "//billingbudgets.googleapis.com/")
	if !strings.HasPrefix(name, "billingAccounts/") || !strings.Contains(name, "/budgets/") {
		return
	}
	for _, field := range []string{"budgetFilter", "notificationsRule"} {
		if _, exists := data[field]; exists {
			data[field] = "[REDACTED]"
		}
	}
}

func billingBudgetName(id string) (string, string, error) {
	prefix := "//billingbudgets.googleapis.com/"
	name := strings.TrimPrefix(id, prefix)
	parts := strings.Split(name, "/")
	if name == id || len(parts) != 4 || parts[2] != "budgets" || !billingAccountName.MatchString(strings.Join(parts[:2], "/")) || !uptimeSegment(parts[3]) {
		return "", "", groupDenied("billing_budget_identity_invalid")
	}
	return name, strings.Join(parts[:2], "/"), nil
}

func (r *Runtime) listBillingBudgets(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	failed := contracts.InventoryBatch{}
	if request.Source != billingBudgetSource || request.ResourceKind == nil || request.ResourceKind.NativeType != billingBudgetType || request.Cursor != "" || request.NetworkTarget != nil || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeGlobal {
		return failed, groupDenied("billing_budget_inventory_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && !slices.Contains([]string{"global", c.project + "/global", c.number + "/global"}, request.Scope.NativeID) {
		return failed, groupDenied("billing_budget_inventory_project_changed")
	}
	known := map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		if _, _, err := billingBudgetName(id); err != nil {
			return failed, err
		}
		if known[id] {
			return failed, groupDenied("billing_budget_known_duplicate")
		}
		known[id] = true
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return failed, groupDenied("billing_budget_known_metadata_unrelated")
		}
	}
	projectValues, projectInfo, err := c.projectBillingBudgets(ctx)
	if err != nil {
		return failed, err
	}
	accounts, err := c.billingAccounts(ctx)
	// Only initial account-index denial permits project-only access. Failures
	// inside a visible account, malformed data and transport errors still fail.
	if err != nil && !(errors.Is(err, errBillingAccountIndexDenied) && text(projectInfo["billingAccountName"]) != "") {
		return failed, err
	}
	values := map[string]map[string]any{}
	if err == nil {
		values, err = c.visibleBillingBudgetsFrom(ctx, accounts)
		if err != nil {
			return failed, err
		}
	}
	projectReviews := map[string]bool{}
	for id, data := range projectValues {
		if accountData := values[id]; accountData != nil {
			if firewallDigest(accountData) != firewallDigest(data) {
				return failed, groupDenied("billing_budget_changed")
			}
		} else {
			values[id] = data
			projectReviews[id] = true
		}
	}
	for id := range known {
		if values[id] == nil && text(request.KnownNativeMetadata[id][billingBudgetProjectReview]) != "" {
			if request.KnownNativeMetadata[id][billingBudgetProject] != "projects/"+c.project {
				return failed, groupDenied("billing_budget_project_review_changed")
			}
			projectReviews[id] = true
		}
	}
	ids := make([]string, 0, len(values)+len(known))
	for id := range values {
		ids = append(ids, id)
	}
	for id := range known {
		if values[id] == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	for _, id := range ids {
		name, parent, err := billingBudgetName(id)
		if err != nil {
			return failed, err
		}
		project := projectReviews[id]
		account, err := c.billingBudgetParent(ctx, parent, project)
		if err != nil {
			return failed, err
		}
		if project && firewallDigest(account) != firewallDigest(projectInfo) {
			return failed, groupDenied("billing_project_configuration_changed")
		}
		live, readErr := c.billingReadResult(ctx, "billingbudgets.billingAccounts.budgets.get", name)
		absent := known[id] && values[id] == nil && isNotFound(readErr)
		if readErr != nil && !absent {
			return failed, contracts.DependencyReadError(readErr)
		}
		again, err := c.billingBudgetParent(ctx, parent, project)
		if err != nil {
			return failed, err
		}
		if firewallDigest(account) != firewallDigest(again) {
			return failed, groupDenied("billing_account_changed")
		}
		if live.RequestID != "" {
			batch.RequestID = live.RequestID
		}
		if absent {
			batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id)
			continue
		}
		if _, err := c.billingBudgetData(parent, live.Data); err != nil {
			return failed, err
		}
		if project {
			if err := c.billingSingleProject(live.Data); err != nil {
				return failed, err
			}
		}
		if previous := values[id]; previous != nil && firewallDigest(previous) != firewallDigest(live.Data) {
			return failed, groupDenied("billing_budget_changed")
		}
		// Retain queryable budget fields; provider-only notification delivery and
		// arbitrary filters remain inside the configuration fingerprint.
		safe := map[string]any{}
		for _, field := range []string{"name", "displayName", "etag", "ownershipScope", "amount", "thresholdRules"} {
			if value, exists := live.Data[field]; exists {
				safe[field] = value
			}
		}
		safe["billing_account"] = parent
		safe[billingBudgetReview] = firewallDigest(live.Data)
		if project {
			safe[billingBudgetProjectReview] = firewallDigest(account)
			safe[billingBudgetProject] = "projects/" + c.project
		} else {
			safe[billingBudgetAccountReview] = firewallDigest(account)
		}
		safe["_inventory_source"] = billingBudgetSource
		safe = safePayload(safe)
		batch.Items = append(batch.Items, contracts.InventoryItem{NativeType: billingBudgetType, NativeID: id, ResourceKind: r.resourceKind(billingBudgetType), Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.project + "/global", Name: "Global", Location: "global"}, Name: text(live.Data["displayName"]), Location: "global", Normalized: safe, Raw: map[string]any{"name": id, "assetType": billingBudgetType, "resource": map[string]any{"data": safePayload(safe), "location": "global"}}})
	}
	return batch, nil
}
