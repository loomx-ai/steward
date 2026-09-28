package alicloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	ECSPrefixListNativeType = "ACS::ECS::PrefixList"
	VPCPrefixListNativeType = "ACS::VPC::PrefixList"
	vpcFlowLogNativeType    = "ACS::VPC::FlowLog"
	// NormalizedPrefixListAssociationsField lists the resources that still
	// reference a prefix list, each as {resourceId, resourceType}.
	NormalizedPrefixListAssociationsField = "associations"
	prefixListAssociationPageLimit        = 1000
)

type prefixListAssociationLookup struct {
	operation, itemsPath string
	pageSize             int
}

// DeletePrefixList fails with NotAllowed.AssociationExist and
// DeleteVpcPrefixList with DependencyViolation.PrefixListRelation while
// another resource references the list, so the references are read at scan
// time and the referencing resources are ordered first.
var prefixListAssociationLookups = map[string]prefixListAssociationLookup{
	ECSPrefixListNativeType: {operation: "DescribePrefixListAssociations", itemsPath: "PrefixListAssociations.PrefixListAssociation", pageSize: 100},
	VPCPrefixListNativeType: {operation: "GetVpcPrefixListAssociations", itemsPath: "PrefixListAssociation", pageSize: 100},
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
			for _, raw := range anySlice(valueAtPath(result.Data, lookup.itemsPath)) {
				record, _ := raw.(map[string]any)
				if id := strings.TrimSpace(stringValue(record["ResourceId"])); id != "" {
					associations = append(associations, map[string]any{
						"resourceId": id, "resourceType": strings.TrimSpace(stringValue(record["ResourceType"])),
					})
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
