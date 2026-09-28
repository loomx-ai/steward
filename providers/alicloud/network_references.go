package alicloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	ECSPrefixListNativeType = "ACS::ECS::PrefixList"
	ALBAclNativeType        = "ACS::ALB::Acl"
	VPCPrefixListNativeType = "ACS::VPC::PrefixList"
	vpcFlowLogNativeType    = "ACS::VPC::FlowLog"
	resolverEndpointType    = "ACS::PrivateZone::ResolverEndpoint"
	resolverRuleType        = "ACS::PrivateZone::ResolverRule"
	// NormalizedPrefixListAssociationsField lists the resources that still
	// reference a prefix list, each as {resourceId, resourceType}.
	NormalizedPrefixListAssociationsField = "associations"
	prefixListAssociationPageLimit        = 1000
)

type prefixListAssociationLookup struct {
	operation, itemsPath string
	pageSize             int
	// idsParameter names a list parameter taking the resource ID, for APIs
	// that report the associations of several resources at once.
	idsParameter string
}

// DeletePrefixList fails with NotAllowed.AssociationExist and
// DeleteVpcPrefixList with DependencyViolation.PrefixListRelation while
// another resource references the list, so the references are read at scan
// time and the referencing resources are ordered first.
var prefixListAssociationLookups = map[string]prefixListAssociationLookup{
	ECSPrefixListNativeType: {operation: "DescribePrefixListAssociations", itemsPath: "PrefixListAssociations.PrefixListAssociation", pageSize: 100},
	VPCPrefixListNativeType: {operation: "GetVpcPrefixListAssociations", itemsPath: "PrefixListAssociation", pageSize: 100},
	// DeleteAcl fails with ResourceInUse.Acl while a listener uses the ACL.
	ALBAclNativeType: {operation: "AlibabaCloud.ALB.ListAclRelations", itemsPath: "AclRelations.RelatedListeners", idsParameter: "AclIds"},
}

func (r *Runtime) enrichPrefixListAssociations(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	for index := range items {
		lookup, ok := prefixListAssociationLookups[items[index].NativeType]
		if !ok || strings.TrimSpace(request.Source) == "resource-center" {
			continue
		}
		region, err := inventoryRegion(request)
		if err != nil {
			return nil, err
		}
		associations := []any{}
		token := ""
		for page := 0; ; page++ {
			if page >= prefixListAssociationPageLimit {
				return nil, fmt.Errorf("Alibaba Cloud %s exceeded %d pages", lookup.operation, prefixListAssociationPageLimit)
			}
			parameters := map[string]any{"RegionId": region, "PrefixListId": items[index].NativeID, "MaxResults": lookup.pageSize}
			if lookup.idsParameter != "" {
				parameters = map[string]any{lookup.idsParameter: []string{items[index].NativeID}}
			}
			if token != "" {
				parameters["NextToken"] = token
			}
			result, err := r.Invoke(ctx, contracts.Invocation{
				ConnectionID: request.ConnectionID, Operation: lookup.operation,
				Scope: map[string]string{"region": region}, Parameters: parameters,
			})
			if err != nil {
				return nil, err
			}
			for _, raw := range recordsAtPath(result.Data, lookup.itemsPath) {
				record, _ := raw.(map[string]any)
				if id := strings.TrimSpace(stringValue(record["ResourceId"])); id != "" {
					associations = append(associations, map[string]any{
						"resourceId": id, "resourceType": strings.TrimSpace(stringValue(record["ResourceType"])),
					})
				}
				if id := strings.TrimSpace(stringValue(record["ListenerId"])); id != "" {
					associations = append(associations, map[string]any{"resourceId": id, "resourceType": "listener"})
				}
			}
			next := strings.TrimSpace(stringValue(result.Data["NextToken"]))
			if next == "" || next == token {
				break
			}
			token = next
		}
		if items[index].Normalized == nil {
			items[index].Normalized = map[string]any{}
		}
		items[index].Normalized[NormalizedPrefixListAssociationsField] = associations
	}
	return items, nil
}

// enrichVPCFlowLogs records the logstore a VPC flow log writes to as a
// logstore native ID. DeleteFlowLog fails with ProjectOrLogstoreNotExist once
// the logstore is gone, so the flow log is deleted first.
func enrichVPCFlowLogs(items []contracts.InventoryItem) []contracts.InventoryItem {
	for index := range items {
		if items[index].NativeType != vpcFlowLogNativeType {
			continue
		}
		project := strings.TrimSpace(stringValue(items[index].Normalized["projectName"]))
		logStore := strings.TrimSpace(stringValue(items[index].Normalized["logStoreName"]))
		if project != "" && logStore != "" {
			items[index].Normalized["logStoreRef"] = project + "/" + logStore
		}
	}
	return items
}

// enrichResolverNetworks records the vSwitches a resolver endpoint or gateway
// load balancer places its addresses in and the VPCs a forwarding rule is
// bound to, all nested in arrays a field path cannot address.
func enrichResolverNetworks(items []contracts.InventoryItem) []contracts.InventoryItem {
	collect := func(values []any, field string) []any {
		ids := []any{}
		seen := map[string]bool{}
		for _, raw := range values {
			record, _ := raw.(map[string]any)
			if id := strings.TrimSpace(stringValue(record[field])); id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return ids
	}
	for index := range items {
		switch items[index].NativeType {
		case resolverEndpointType:
			items[index].Normalized["vSwitchIds"] = collect(anySlice(items[index].Raw["IpConfigs"]), "VSwitchId")
		case resolverRuleType:
			items[index].Normalized["boundVpcIds"] = collect(anySlice(items[index].Raw["BindVpcs"]), "VpcId")
		case gwlbLoadBalancerNativeType:
			// Its zone mappings place service-managed interfaces in vSwitches.
			items[index].Normalized["vSwitchIds"] = collect(anySlice(items[index].Raw["ZoneMappings"]), "VSwitchId")
		}
	}
	return items
}

// recordsAtPath returns the values at a dotted path, flattening any arrays
// met along the way (AclRelations[].RelatedListeners[]).
func recordsAtPath(value any, path string) []any {
	current := []any{value}
	for _, segment := range strings.Split(path, ".") {
		var next []any
		for _, item := range current {
			object, _ := item.(map[string]any)
			child := object[segment]
			if values := anySlice(child); values != nil {
				next = append(next, values...)
			} else if child != nil {
				next = append(next, child)
			}
		}
		current = next
	}
	var result []any
	for _, item := range current {
		if values := anySlice(item); values != nil {
			result = append(result, values...)
		} else {
			result = append(result, item)
		}
	}
	return result
}
