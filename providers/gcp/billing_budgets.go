package gcp

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const billingBudgetType = "billingbudgets.googleapis.com/Budget"
const billingAccountsList = "cloudbilling.billingAccounts.list"
const billingBudgetsList = "billingbudgets.billingAccounts.budgets.list"

var errBillingAccountIndexDenied = errors.New("billing_account_index_denied")

var billingAccountName = regexp.MustCompile(`^billingAccounts/[A-Z0-9]{6}-[A-Z0-9]{6}-[A-Z0-9]{6}$`)

func billingAccountData(data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "parent", "masterBillingAccount", "currencyCode"}, []string{"open"}, nil, nil); err != nil {
		return err
	}
	if !billingAccountName.MatchString(text(data["name"])) {
		return groupDenied("billing_account_identity_invalid")
	}
	for _, key := range []string{"parent", "masterBillingAccount"} {
		value := text(data[key])
		if value != "" && !billingAccountName.MatchString(value) && !(key == "parent" && strings.HasPrefix(value, "organizations/") && firewallContainerName(value)) {
			return groupDenied("billing_account_parent_invalid")
		}
	}
	return nil
}

func (c *client) billingBudgetData(parent string, data map[string]any) ([]string, error) {
	if err := checkListCompleteness(data); err != nil {
		return nil, err
	}
	if !billingAccountName.MatchString(parent) {
		return nil, groupDenied("billing_budget_parent_invalid")
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "etag", "ownershipScope"}, nil, nil, nil); err != nil {
		return nil, err
	}
	name := text(data["name"])
	prefix := parent + "/budgets/"
	if !strings.HasPrefix(name, prefix) || !uptimeSegment(strings.TrimPrefix(name, prefix)) {
		return nil, groupDenied("billing_budget_identity_invalid")
	}
	for _, key := range []string{"budgetFilter", "amount", "notificationsRule"} {
		if raw, ok := data[key]; ok && object(raw) == nil {
			return nil, groupDenied("billing_budget_object_invalid")
		}
	}
	if _, err := cloudNatObjects(data, "thresholdRules"); err != nil {
		return nil, err
	}
	rule := object(data["notificationsRule"])
	if err := cloudNatScalars(rule, []string{"pubsubTopic", "schemaVersion"}, []string{"disableDefaultIamRecipients", "enableProjectLevelRecipients"}, nil, []string{"monitoringNotificationChannels"}); err != nil {
		return nil, err
	}
	// The native reference syntax is identical to AlertPolicy channel names.
	return c.alertPolicyChannels(map[string]any{"notificationChannels": append([]any{}, array(rule["monitoringNotificationChannels"])...)})
}

func (c *client) billingRead(ctx context.Context, operation, name string) (map[string]any, error) {
	result, err := c.billingReadResult(ctx, operation, name)
	return result.Data, contracts.DependencyReadError(err)
}

func (c *client) billingReadResult(ctx context.Context, operation, name string) (contracts.InvocationResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	op, ok := metadata.catalog.Operation(operation)
	if !ok {
		return contracts.InvocationResult{}, groupDenied("billing_operation_missing")
	}
	bound, err := catalog.BindREST(op, map[string]any{"name": name})
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	result, err := c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
	if err != nil {
		return result, err
	}
	if text(result.Data["name"]) != name {
		return contracts.InvocationResult{}, groupDenied("billing_identity_changed")
	}
	return result, nil
}

func (c *client) billingAccounts(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := c.batchList(ctx, billingAccountsList, map[string]any{"pageSize": 100}, "billingAccounts")
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	result := map[string]map[string]any{}
	for _, row := range rows {
		if err := billingAccountData(row); err != nil {
			return nil, err
		}
		id := text(row["name"])
		if _, exists := result[id]; exists {
			return nil, groupDenied("billing_account_duplicate")
		}
		result[id] = row
	}
	return result, nil
}

func (c *client) billingBudgets(ctx context.Context, parent string, projectScope ...string) (map[string]map[string]any, error) {
	if !billingAccountName.MatchString(parent) {
		return nil, groupDenied("billing_budget_parent_invalid")
	}
	// Channel consumer discovery leaves scope empty; project inventory opts in.
	parameters := map[string]any{"parent": parent, "pageSize": 100}
	if len(projectScope) != 0 {
		if len(projectScope) != 1 || projectScope[0] != "projects/"+c.project {
			return nil, groupDenied("billing_budget_project_scope_invalid")
		}
		parameters["scope"] = projectScope[0]
	}
	rows, err := c.batchList(ctx, billingBudgetsList, parameters, "budgets")
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	result := map[string]map[string]any{}
	for _, row := range rows {
		if _, err := c.billingBudgetData(parent, row); err != nil {
			return nil, err
		}
		id := text(row["name"])
		if _, exists := result[id]; exists {
			return nil, groupDenied("billing_budget_duplicate")
		}
		result[id] = row
	}
	return result, nil
}

// Visibility-filtered accounts only establish positive references. Even a stable
// empty result cannot prove global budget absence or authorize email deletion.
func (c *client) visibleBillingBudgets(ctx context.Context) (map[string]map[string]any, error) {
	accounts, err := c.billingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	return c.visibleBillingBudgetsFrom(ctx, accounts)
}

func (c *client) visibleBillingBudgetsFrom(ctx context.Context, accounts map[string]map[string]any) (map[string]map[string]any, error) {
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	result := map[string]map[string]any{}
	for _, account := range ids {
		live, err := c.billingRead(ctx, "cloudbilling.billingAccounts.get", account)
		if err != nil {
			return nil, err
		}
		if err := billingAccountData(live); err != nil {
			return nil, err
		}
		if firewallDigest(accounts[account]) != firewallDigest(live) {
			return nil, groupDenied("billing_account_changed")
		}
		budgets, err := c.billingBudgets(ctx, account)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(budgets))
		for name := range budgets {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			budget, err := c.billingRead(ctx, "billingbudgets.billingAccounts.budgets.get", name)
			if err != nil {
				return nil, err
			}
			_, err = c.billingBudgetData(account, budget)
			if err != nil {
				return nil, err
			}
			if firewallDigest(budgets[name]) != firewallDigest(budget) {
				return nil, groupDenied("billing_budget_changed")
			}
			result["//billingbudgets.googleapis.com/"+name] = budget
		}
		again, err := c.billingBudgets(ctx, account)
		if err != nil {
			return nil, err
		}
		if firewallDigest(budgets) != firewallDigest(again) {
			return nil, groupDenied("billing_budget_set_changed")
		}
		live, err = c.billingRead(ctx, "cloudbilling.billingAccounts.get", account)
		if err != nil {
			return nil, err
		}
		if firewallDigest(accounts[account]) != firewallDigest(live) {
			return nil, groupDenied("billing_account_changed")
		}
	}
	again, err := c.billingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if firewallDigest(accounts) != firewallDigest(again) {
		return nil, groupDenied("billing_account_visibility_changed")
	}
	return result, nil
}
