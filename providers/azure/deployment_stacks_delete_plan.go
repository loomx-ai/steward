package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Compile reviewed consequences into the native category-wide operation. This
// does not establish live membership, incarnation, permissions or child closure;
// the action must verify those before sending the returned request.
func (c *client) deploymentStackDeletePlan(req contracts.ActionRequest) (catalog.RESTRequest, map[string]any, error) {
	fail := func(reason string) (catalog.RESTRequest, map[string]any, error) {
		return catalog.RESTRequest{}, nil, serviceDenied(reason)
	}
	root := req.Asset
	scope, parameters, err := deploymentStackParameters(root.Identity.NativeID)
	review := object(root.Normalized[deploymentStackReviewKey])
	if err != nil || scope == "ManagementGroup" || !strings.EqualFold(text(parameters["subscriptionId"]), c.subscription) || req.Action != "delete" || root.ID == "" || root.Identity.Provider != asset.ProviderAzure || root.Identity.Partition != "azure" || root.Identity.ConnectionID == "" || !strings.EqualFold(root.Identity.NativeType, deploymentStackType) || len(review) != 9 || text(review["incarnation"]) == "" || review["arm_members_complete"] != true || text(review["configuration"]) == "" || object(review["members"]) == nil || root.Normalized[deploymentStackProofKey] != c.deploymentStackProof(root.Identity.NativeID, root.Identity.ConnectionID, review) {
		return fail("deployment_stack_delete_plan_requires_refresh")
	}
	members := object(review["members"])
	impacts := map[string]contracts.ActionImpact{}
	byAsset := map[asset.AssetID]contracts.ActionImpact{}
	modes := map[string]string{}
	for _, impact := range req.LifecycleImpacts {
		value := impact.Asset
		id, kind, err := deploymentStackMemberID(value.Identity.NativeID)
		if err != nil || value.ID == "" || value.ID == root.ID || byAsset[value.ID].Asset.ID != "" || impacts[id].Asset.ID != "" || value.Identity.Provider != root.Identity.Provider || value.Identity.Partition != root.Identity.Partition || value.Identity.ConnectionID != root.Identity.ConnectionID || !strings.EqualFold(kind, value.Identity.NativeType) || !strings.HasPrefix(id, strings.ToLower(c.root())+"/") || strings.EqualFold(id, root.Identity.NativeID) || impact.ControllerID == "" {
			return fail("invalid_deployment_stack_delete_impact")
		}
		impacts[id], byAsset[value.ID] = impact, impact
		if impact.ControllerID != root.ID {
			continue
		}
		member := object(members[id])
		if member == nil || !strings.EqualFold(text(member["type"]), kind) || member["subscription_local"] != true || member["status"] != "managed" || member["deny_status"] == "unknown" {
			return fail("deployment_stack_delete_member_changed")
		}
		category := "Resources"
		if strings.EqualFold(kind, groupType) {
			category = "ResourceGroups"
		}
		if strings.EqualFold(kind, "Microsoft.Management/managementGroups") {
			category = "ManagementGroups"
		}
		mode := "detach"
		if impact.Delete {
			mode = "delete"
		}
		if previous := modes[category]; previous != "" && previous != mode {
			return fail("deployment_stack_partial_category_retention_unsupported")
		}
		modes[category] = mode
	}
	for id := range members {
		impact, found := impacts[id]
		if !found || impact.ControllerID != root.ID {
			return fail("deployment_stack_member_missing_from_delete_plan")
		}
	}
	// Nested impacts must reach a reviewed direct member. Their own native
	// controller remains responsible for authenticating the nested membership.
	for _, impact := range req.LifecycleImpacts {
		seen := map[asset.AssetID]bool{impact.Asset.ID: true}
		for current := impact; current.ControllerID != root.ID; {
			parent, found := byAsset[current.ControllerID]
			if !found || seen[parent.Asset.ID] || !parent.Delete && current.Delete {
				return fail("invalid_deployment_stack_nested_impact")
			}
			seen[parent.Asset.ID] = true
			current = parent
		}
	}
	// A category-level detach cannot retain an ARM descendant when another
	// category deletes its containing group. Match path segments, not prefixes.
	for id, impact := range impacts {
		if impact.Delete {
			continue
		}
		for parentID, parent := range impacts {
			if parent.Delete && strings.HasPrefix(id, parentID+"/") {
				return fail("deployment_stack_retained_descendant_would_be_deleted")
			}
		}
	}
	if err := deploymentStackRetentionOptions(req); err != nil {
		return catalog.RESTRequest{}, nil, err
	}
	saved := map[string]any{"api-version": deploymentStackVersion, "bypassStackOutOfSyncError": "false", "unmanageAction.ResourcesWithoutDeleteSupport": "fail"}
	for _, category := range []string{"Resources", "ResourceGroups", "ManagementGroups"} {
		mode := modes[category]
		if mode == "" {
			mode = "detach"
		} // Unreviewed categories never imply deletion.
		saved["unmanageAction."+category] = mode
		parameters["unmanageAction."+category] = mode
	}
	parameters["bypassStackOutOfSyncError"] = false
	parameters["unmanageAction.ResourcesWithoutDeleteSupport"] = "fail"
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, nil, err
	}
	operation, found := metadata.catalog.Operation(deploymentStackOperation + "DeleteAt" + scope)
	if !found {
		return fail("missing_deployment_stack_delete_operation")
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return catalog.RESTRequest{}, nil, err
	}
	return bound, saved, nil
}

func deploymentStackRetentionOptions(req contracts.ActionRequest) error {
	invalid := func() error { return serviceDenied("invalid_deployment_stack_retention_option") }
	for key, value := range req.Parameters {
		switch key {
		case "retain_all_resources":
			retain, ok := value.(bool)
			if !ok {
				return invalid()
			}
			if retain {
				for _, impact := range req.LifecycleImpacts {
					if impact.Delete {
						return invalid()
					}
				}
			}
		case "retain_resources":
			var ids []string
			switch values := value.(type) {
			case []string:
				ids = values
			case []any:
				for _, value := range values {
					id, ok := value.(string)
					if !ok {
						return invalid()
					}
					ids = append(ids, id)
				}
			default:
				return invalid()
			}
			for _, id := range ids {
				found := false
				for _, impact := range req.LifecycleImpacts {
					if id != "" && (id == string(impact.Asset.ID) || strings.EqualFold(id, impact.Asset.Identity.NativeID)) {
						if impact.Delete {
							return invalid()
						}
						found = true
					}
				}
				if !found {
					return invalid()
				}
			}
		case "delete_options":
			rows, ok := value.([]any)
			if !ok {
				return invalid()
			}
			seen := map[string]bool{}
			for _, value := range rows {
				row := object(value)
				kind, ok := row["resource_type"].(string)
				if !ok || kind == "" || len(row) != 2 || (row["delete_mode"] != "retain" && row["delete_mode"] != "delete") || seen[strings.ToLower(kind)] {
					return invalid()
				}
				seen[strings.ToLower(kind)] = true
				found := false
				for _, impact := range req.LifecycleImpacts {
					if strings.EqualFold(impact.Asset.Identity.NativeType, kind) {
						if impact.Delete != (row["delete_mode"] == "delete") {
							return invalid()
						}
						found = true
					}
				}
				if !found {
					return invalid()
				}
			}
		default:
			return invalid()
		}
	}
	return nil
}
