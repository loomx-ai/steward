package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func firewallContainerInvocation(operation string) bool {
	return operation == "cloudresourcemanager.organizations.get" || operation == "cloudresourcemanager.folders.get" || operation == "cloudresourcemanager.folders.list"
}

// Hierarchical policy IDs and organization LRO IDs are global scalar paths. The
// ordinary project path guard cannot authorize them; read native ownership first.
func (c *client) firewallInvocation(ctx context.Context, operation catalog.Operation, parameters map[string]any) error {
	if firewallContainerInvocation(operation.ID) {
		key := "name"
		if strings.HasSuffix(operation.ID, ".list") {
			key = "parent"
		}
		_, err := c.firewallContainerChain(ctx, text(parameters[key]))
		if err == nil || operation.ID != "cloudresourcemanager.organizations.get" {
			return err
		}
		// Organization inventory also allows its selected project's actual
		// ancestor. This read authority never extends to firewall mutations.
		ancestry, err := c.organizationAncestry(ctx)
		if err != nil {
			return err
		}
		if ancestry.Organization == nil || ancestry.Organization["name"] != parameters[key] {
			return groupDenied("organization_invocation_outside_ancestry")
		}
		return nil
	}
	if operation.ID == "compute.globalOrganizationOperations.get" {
		name := text(parameters["operation"])
		if !firewallContainerName(c.firewallParent) || !segmentPattern.MatchString(name) || name == "." || name == ".." {
			return groupDenied("firewall_operation_invocation_invalid")
		}
		if parent, present := parameters["parentId"]; present {
			if _, err := c.firewallContainerChain(ctx, text(parent)); err != nil {
				return err
			}
		}
		bound, err := catalog.BindREST(operation, parameters)
		if err != nil {
			return err
		}
		data, err := c.request(ctx, "GET", bound.URL, nil)
		if err != nil {
			return err
		}
		if data["name"] != name {
			return groupDenied("firewall_operation_invocation_changed")
		}
		live, err := c.firewallReadPolicy(ctx, firewallPolicyType, c.canonicalName(text(data["targetLink"])))
		if err != nil {
			return err
		}
		if data["targetId"] != live["id"] {
			return groupDenied("firewall_operation_invocation_target_changed")
		}
		return nil
	}
	hierarchical := strings.HasPrefix(operation.ID, "compute.firewallPolicies.")
	network := strings.HasPrefix(operation.ID, "compute.networkFirewallPolicies.") || strings.HasPrefix(operation.ID, "compute.regionNetworkFirewallPolicies.")
	if !hierarchical && !network {
		return nil
	}
	method := last(strings.ReplaceAll(operation.ID, ".", "/"))
	if hierarchical && (method == "list" || method == "listAssociations") {
		key := "parentId"
		if method == "listAssociations" {
			key = "targetResource"
		}
		_, err := c.firewallContainerChain(ctx, text(parameters[key]))
		return err
	}
	if network && (method == "list" || method == "aggregatedList") {
		return nil
	}
	name := text(parameters["firewallPolicy"])
	kind, id := firewallPolicyType, "//compute.googleapis.com/locations/global/firewallPolicies/"+name
	if network {
		kind = networkFirewallPolicyType
		scope := "global"
		if strings.HasPrefix(operation.ID, "compute.regionNetworkFirewallPolicies.") {
			scope = "regions/" + text(parameters["region"])
		}
		id = "//compute.googleapis.com/projects/" + text(parameters["project"]) + "/" + scope + "/firewallPolicies/" + name
	}
	id = c.canonicalName(id)
	live, err := c.firewallReadPolicy(ctx, kind, id)
	if err != nil {
		return err
	}
	if method == "getAssociation" || method == "removeAssociation" {
		association := text(parameters["name"])
		if !firewallAssociationName(association) {
			return groupDenied("firewall_invocation_association_invalid")
		}
		row, err := c.firewallAssociationGET(ctx, firewallChildType(kind), firewallAssociationID(id, association))
		if err != nil {
			return err
		}
		actual, err := c.firewallAssociationValue(firewallChildType(kind), id, live, row)
		if err != nil || actual != firewallAssociationID(id, association) {
			return groupDenied("firewall_invocation_association_changed")
		}
		_, err = c.firewallTarget(ctx, firewallChildType(kind), row)
		return err
	}
	if method == "delete" && len(array(live["associations"])) != 0 {
		return groupDenied("firewall_invocation_policy_has_associations")
	}
	return nil
}
