package gcp

import (
	"context"
	"reflect"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func securityServiceAncestors(chain organizationAncestry) []string {
	parents := []string{}
	for _, folder := range chain.Folders {
		parents = append(parents, folder.Name)
	}
	if chain.Organization != nil {
		parents = append(parents, text(chain.Organization["name"]))
	}
	return parents
}

// Ancestors use the locations visible to the connected project. The SDK exposes
// no organization/folder locations LIST. This is contextual, non-authoritative
// discovery, not an inventory of every private location in an organization.
func securityServiceTargets(targets []productTarget, chain organizationAncestry) []productTarget {
	result := append([]productTarget{}, targets...)
	for _, parent := range securityServiceAncestors(chain) {
		for _, target := range targets {
			target.Parameters = cloneParameters(target.Parameters)
			if target.API.Operation == securityBillingGet {
				// BillingMetadata has organization/project GETs, no folder API.
				if !strings.HasPrefix(parent, "organizations/") {
					continue
				}
				parts := strings.Split(text(target.Parameters["name"]), "/")
				target.Parameters["name"] = parent + "/locations/" + parts[3] + "/billingMetadata"
				target.API.Operation = securityOrganizationBillingGet
			} else {
				target.Parameters["parent"] = parent + "/locations/" + last(text(target.Parameters["parent"]))
				target.API.Operation = "securitycentermanagement." + strings.Split(parent, "/")[0] + ".locations.securityCenterServices.list"
			}
			result = append(result, target)
		}
	}
	return result
}

func (c *client) verifySecurityServiceAncestry(ctx context.Context, first *organizationAncestry) error {
	if first == nil {
		return nil
	}
	second, err := c.organizationAncestry(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*first, second) {
		return groupDenied("security_service_ancestry_changed")
	}
	return nil
}

func securityServiceAncestorOperation(id string) bool {
	if id == securityOrganizationBillingGet {
		return true
	}
	for _, parent := range []string{"folders", "organizations"} {
		for _, method := range []string{"get", "list"} {
			if id == "securitycentermanagement."+parent+".locations.securityCenterServices."+method {
				return true
			}
		}
	}
	return false
}

// Binding a native address does not grant ancestor authority. Inventory verifies
// the full chain around the read; public Invoke uses the same boundary below.
func (c *client) securitySettingsOperation(metadata providerMetadata, nativeType, name, method string) (catalog.Operation, map[string]any, error) {
	parts := strings.Split(name, "/")
	billing := nativeType == securityBillingType
	valid := len(parts) == 6 && parts[4] == "securityCenterServices" && nativeType == securityServiceType
	cluster := len(parts) == 8 && parts[0] == "projects" && parts[4] == "clusters" && parts[6] == "securityCenterServices" && nativeType == securityServiceType
	valid = valid || cluster
	if billing {
		valid = len(parts) == 5 && parts[4] == "billingMetadata" && (parts[0] == "projects" || parts[0] == "organizations")
	}
	if method != "GET" || !valid || parts[2] != "locations" {
		return catalog.Operation{}, nil, groupDenied("security_service_identity_invalid")
	}
	for _, part := range parts {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." || part == "-" {
			return catalog.Operation{}, nil, groupDenied("security_service_identity_invalid")
		}
	}
	if parts[0] == "projects" {
		if parts[1] != c.project && parts[1] != c.number {
			return catalog.Operation{}, nil, groupDenied("security_service_project_changed")
		}
	} else if !firewallContainerName(strings.Join(parts[:2], "/")) {
		return catalog.Operation{}, nil, groupDenied("security_service_container_invalid")
	}
	operationID := "securitycentermanagement." + parts[0] + ".locations.securityCenterServices.get"
	if cluster {
		operationID = securityClusterServiceGet
	}
	if billing {
		operationID = "securitycentermanagement." + parts[0] + ".locations.getBillingMetadata"
	}
	op, ok := metadata.catalog.Operation(operationID)
	if !ok {
		return catalog.Operation{}, nil, groupDenied("security_service_method_missing")
	}
	return op, map[string]any{"name": name}, nil
}

func (c *client) invokeAncestorSecurityService(ctx context.Context, operation catalog.Operation, parameters map[string]any) (contracts.InvocationResult, error) {
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	nativeType := securityServiceType
	if operation.ID == securityOrganizationBillingGet {
		nativeType = securityBillingType
	}
	key := "parent"
	if nativeType == securityBillingType || strings.HasSuffix(operation.ID, ".get") {
		key = "name"
	}
	name := text(parameters[key])
	parts := strings.Split(name, "/")
	if len(parts) < 4 || !firewallContainerName(strings.Join(parts[:2], "/")) {
		return contracts.InvocationResult{}, groupDenied("security_service_container_invalid")
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	resourceName := name
	if key == "parent" {
		resourceName += "/securityCenterServices/scope-validation"
	}
	if _, _, err := c.securitySettingsOperation(metadata, nativeType, resourceName, "GET"); err != nil {
		return contracts.InvocationResult{}, err
	}
	first, err := c.organizationAncestry(ctx)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	allowed := false
	for _, parent := range securityServiceAncestors(first) {
		if parent == strings.Join(parts[:2], "/") {
			allowed = true
		}
	}
	if !allowed {
		return contracts.InvocationResult{}, groupDenied("security_service_outside_ancestry")
	}
	result, err := c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
	if err != nil {
		return result, err
	}
	if err = checkListCompleteness(result.Data); err != nil {
		return contracts.InvocationResult{}, err
	}
	if key == "parent" {
		err = c.securityServiceListMetadata(result.Data, name)
	} else if nativeType == securityBillingType {
		err = c.securityBillingMetadata(result.Data, name)
	} else {
		err = c.securityServiceMetadata(result.Data, name)
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if err = c.verifySecurityServiceAncestry(ctx, &first); err != nil {
		return contracts.InvocationResult{}, err
	}
	result.Data = safeSecurityServicePayload(result.Data)
	return result, nil
}
