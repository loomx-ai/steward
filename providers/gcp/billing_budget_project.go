package gcp

import (
	"context"
	"slices"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const billingProjectGet = "cloudbilling.projects.getBillingInfo"
const billingBudgetProjectReview = "_billing_budget_project_configuration"
const billingBudgetProject = "_billing_budget_project"

// ProjectBillingInfo has a different response name from its request parameter.
func (c *client) billingProjectInfo(ctx context.Context) (map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := metadata.catalog.Operation(billingProjectGet)
	if !ok {
		return nil, groupDenied("billing_project_operation_missing")
	}
	bound, err := catalog.BindREST(op, map[string]any{"name": "projects/" + c.project})
	if err != nil {
		return nil, err
	}
	result, err := c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	data := result.Data
	if err := checkListCompleteness(data); err != nil {
		return nil, err
	}
	if err := cloudNatScalars(data, []string{"name", "projectId", "billingAccountName"}, []string{"billingEnabled"}, nil, nil); err != nil {
		return nil, err
	}
	if data["name"] != "projects/"+c.project+"/billingInfo" || data["projectId"] != c.project {
		return nil, groupDenied("billing_project_identity_changed")
	}
	parent := text(data["billingAccountName"])
	if parent != "" && !billingAccountName.MatchString(parent) || parent == "" && data["billingEnabled"] == true {
		return nil, groupDenied("billing_project_account_invalid")
	}
	return data, nil
}

func (c *client) billingSingleProject(data map[string]any) error {
	filter := object(data["budgetFilter"])
	if err := cloudNatScalars(filter, nil, nil, nil, []string{"projects"}); err != nil {
		return err
	}
	projects := array(filter["projects"])
	if len(projects) != 1 || !slices.Contains([]string{"projects/" + c.project, "projects/" + c.number}, text(projects[0])) {
		return groupDenied("billing_budget_single_project_required")
	}
	return nil
}

// This is positive project inventory, never complete channel-consumer coverage.
func (c *client) projectBillingBudgets(ctx context.Context) (map[string]map[string]any, map[string]any, error) {
	info, err := c.billingProjectInfo(ctx)
	if err != nil {
		return nil, nil, err
	}
	parent := text(info["billingAccountName"])
	values := map[string]map[string]any{}
	if parent != "" {
		rows, err := c.billingBudgets(ctx, parent, "projects/"+c.project)
		if err != nil {
			return nil, nil, err
		}
		names := make([]string, 0, len(rows))
		for name := range rows {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			data, err := c.billingRead(ctx, "billingbudgets.billingAccounts.budgets.get", name)
			if err != nil {
				return nil, nil, err
			}
			if _, err := c.billingBudgetData(parent, data); err != nil {
				return nil, nil, err
			}
			if err := c.billingSingleProject(data); err != nil {
				return nil, nil, err
			}
			if firewallDigest(data) != firewallDigest(rows[name]) {
				return nil, nil, groupDenied("billing_budget_changed")
			}
			values["//billingbudgets.googleapis.com/"+name] = data
		}
		again, err := c.billingBudgets(ctx, parent, "projects/"+c.project)
		if err != nil {
			return nil, nil, err
		}
		if firewallDigest(rows) != firewallDigest(again) {
			return nil, nil, groupDenied("billing_budget_set_changed")
		}
	}
	again, err := c.billingProjectInfo(ctx)
	if err != nil {
		return nil, nil, err
	}
	if firewallDigest(info) != firewallDigest(again) {
		return nil, nil, groupDenied("billing_project_configuration_changed")
	}
	return values, info, nil
}

// A project review must never silently acquire account-wide authority. Both
// inventory and cleanup bracket a budget read with the same native parent proof.
func (c *client) billingBudgetParent(ctx context.Context, parent string, project bool) (map[string]any, error) {
	if project {
		info, err := c.billingProjectInfo(ctx)
		if err != nil {
			return nil, err
		}
		if info["billingAccountName"] != parent {
			return nil, groupDenied("billing_budget_project_account_changed")
		}
		return info, nil
	}
	data, err := c.billingRead(ctx, "cloudbilling.billingAccounts.get", parent)
	if err != nil {
		return nil, err
	}
	if err := billingAccountData(data); err != nil {
		return nil, err
	}
	return data, nil
}
