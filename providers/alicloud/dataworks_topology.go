package alicloud

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	DataWorksResourceGroupNativeType = "ACS::DataWorks::DwResourceGroup"
	DataWorksProjectNativeType       = "ACS::DataWorks::Project"

	NormalizedDataWorksProjectIDsField       = "_dataworks_associated_project_ids"
	NormalizedDataWorksNetworksField         = "_dataworks_networks"
	NormalizedDataWorksProjectRequestIDField = "_dataworks_project_request_id"
	NormalizedServiceManagedField            = "_service_managed"
	NormalizedServiceIDField                 = "_service_id"
	NormalizedSecurityGroupIDsField          = "security_group_ids"

	dataWorksNetworkPageSize = 100
)

func (r *Runtime) enrichDataWorksResourceGroups(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	indices := make([]int, 0)
	for index := range items {
		if items[index].NativeType == DataWorksResourceGroupNativeType &&
			strings.TrimSpace(items[index].NativeID) != "" {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return items, nil
	}
	region, err := dataWorksInventoryRegion(request)
	if err != nil {
		return nil, err
	}
	for _, index := range indices {
		resourceGroupID := strings.TrimSpace(items[index].NativeID)
		projectIDs, projectRequestID, err := r.dataWorksAssociatedProjectIDs(
			ctx,
			request.ConnectionID,
			region,
			resourceGroupID,
		)
		if err != nil {
			return nil, err
		}
		networks, err := r.dataWorksNetworks(
			ctx,
			request.ConnectionID,
			region,
			resourceGroupID,
		)
		if err != nil {
			return nil, err
		}
		if items[index].Normalized == nil {
			items[index].Normalized = make(map[string]any)
		}
		items[index].Normalized[NormalizedDataWorksProjectIDsField] = projectIDs
		items[index].Normalized[NormalizedDataWorksNetworksField] = networks
		if projectRequestID != "" {
			items[index].Normalized[NormalizedDataWorksProjectRequestIDField] = projectRequestID
		}
		if vpcID, unique := uniqueDataWorksNetworkValue(networks, "vpc_id"); unique {
			items[index].Normalized["vpc_id"] = vpcID
		}
		if vSwitchID, unique := uniqueDataWorksNetworkValue(networks, "vswitch_id"); unique {
			items[index].Normalized["vswitch_id"] = vSwitchID
		}
	}
	return items, nil
}

func dataWorksInventoryRegion(request contracts.InventoryRequest) (string, error) {
	switch request.Scope.Kind {
	case asset.ScopeRegion:
		if region := strings.TrimSpace(request.Scope.NativeID); region != "" {
			return region, nil
		}
	case asset.ScopeGlobal:
		if region := strings.TrimSpace(request.Scope.Location); region != "" {
			return region, nil
		}
	}
	return "", fmt.Errorf("DataWorks topology enrichment requires a region scope")
}

func (r *Runtime) dataWorksAssociatedProjectIDs(
	ctx context.Context,
	connectionID asset.ConnectionID,
	region string,
	resourceGroupID string,
) ([]any, string, error) {
	result, err := r.Invoke(ctx, contracts.Invocation{
		ConnectionID: connectionID,
		Operation:    "AlibabaCloud.DataWorks.ListResourceGroupAssociateProjects",
		Scope:        map[string]string{"region": region},
		Parameters:   map[string]any{"ResourceGroupId": resourceGroupID},
	})
	if err != nil {
		return nil, "", fmt.Errorf(
			"discover DataWorks workspaces for resource group %s: %w",
			resourceGroupID,
			err,
		)
	}
	values, ok := productAPIListValue(result.Data["ProjectIdList"])
	if !ok {
		if explicitlyEmptyPathValue(result.Data, "ProjectIdList") {
			return []any{}, result.RequestID, nil
		}
		return nil, "", fmt.Errorf(
			"DataWorks ListResourceGroupAssociateProjects response has no ProjectIdList for %s",
			resourceGroupID,
		)
	}
	seen := make(map[string]struct{}, len(values))
	projectIDs := make([]string, 0, len(values))
	for _, value := range values {
		projectID := strings.TrimSpace(stringValue(value))
		if projectID == "" {
			return nil, "", fmt.Errorf(
				"DataWorks ListResourceGroupAssociateProjects returned an empty project ID for %s",
				resourceGroupID,
			)
		}
		if _, duplicate := seen[projectID]; duplicate {
			continue
		}
		seen[projectID] = struct{}{}
		projectIDs = append(projectIDs, projectID)
	}
	sort.Strings(projectIDs)
	resultIDs := make([]any, len(projectIDs))
	for index := range projectIDs {
		resultIDs[index] = projectIDs[index]
	}
	return resultIDs, result.RequestID, nil
}

func (r *Runtime) dataWorksNetworks(
	ctx context.Context,
	connectionID asset.ConnectionID,
	region string,
	resourceGroupID string,
) ([]any, error) {
	networks := make([]any, 0)
	for pageNumber := 1; pageNumber <= 1000; pageNumber++ {
		result, err := r.Invoke(ctx, contracts.Invocation{
			ConnectionID: connectionID,
			Operation:    "AlibabaCloud.DataWorks.ListNetworks",
			Scope:        map[string]string{"region": region},
			Parameters: map[string]any{
				"ResourceGroupId": resourceGroupID,
				"PageNumber":      pageNumber,
				"PageSize":        dataWorksNetworkPageSize,
			},
		})
		if err != nil {
			return nil, fmt.Errorf(
				"discover DataWorks networks for resource group %s: %w",
				resourceGroupID,
				err,
			)
		}
		rawNetworks, ok := productAPIListValue(valueAtPath(result.Data, "PagingInfo.NetworkList"))
		if !ok {
			total, totalOK := integerValue(valueAtPath(result.Data, "PagingInfo.TotalCount"))
			if explicitlyEmptyPathValue(result.Data, "PagingInfo.NetworkList") ||
				totalOK && total == 0 {
				return networks, nil
			}
			return nil, fmt.Errorf(
				"DataWorks ListNetworks response has no PagingInfo.NetworkList for %s",
				resourceGroupID,
			)
		}
		for index, raw := range rawNetworks {
			network, ok := productAPIResourceMap(raw)
			if !ok {
				return nil, fmt.Errorf(
					"DataWorks ListNetworks item %d on page %d has type %T",
					index,
					pageNumber,
					raw,
				)
			}
			returnedResourceGroupID := strings.TrimSpace(stringValue(network["ResourceGroupId"]))
			if returnedResourceGroupID != resourceGroupID {
				return nil, fmt.Errorf(
					"DataWorks ListNetworks returned resource group %q for requested %q",
					returnedResourceGroupID,
					resourceGroupID,
				)
			}
			networks = append(networks, normalizedDataWorksNetwork(network, result.RequestID))
		}
		total, totalOK := integerValue(valueAtPath(result.Data, "PagingInfo.TotalCount"))
		if totalOK && len(networks) >= total {
			return networks, nil
		}
		if len(rawNetworks) < dataWorksNetworkPageSize {
			return networks, nil
		}
	}
	return nil, fmt.Errorf(
		"DataWorks ListNetworks exceeded 1000 pages for resource group %s",
		resourceGroupID,
	)
}

func normalizedDataWorksNetwork(network map[string]any, requestID string) map[string]any {
	vSwitchID := strings.TrimSpace(stringValue(network["VswitchId"]))
	if vSwitchID == "" {
		vSwitchID = strings.TrimSpace(stringValue(network["VSwitchId"]))
	}
	result := map[string]any{
		"network_id":        strings.TrimSpace(stringValue(network["Id"])),
		"resource_group_id": strings.TrimSpace(stringValue(network["ResourceGroupId"])),
		"vpc_id":            strings.TrimSpace(stringValue(network["VpcId"])),
		"vswitch_id":        vSwitchID,
		"security_group_id": strings.TrimSpace(stringValue(network["SecurityGroupId"])),
		"status":            strings.TrimSpace(stringValue(network["Status"])),
		"request_id":        strings.TrimSpace(requestID),
	}
	return result
}

func uniqueDataWorksNetworkValue(networks []any, key string) (string, bool) {
	values := make(map[string]struct{})
	for _, raw := range networks {
		network, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if value := strings.TrimSpace(stringValue(network[key])); value != "" {
			values[value] = struct{}{}
		}
	}
	if len(values) != 1 {
		return "", false
	}
	for value := range values {
		return value, true
	}
	return "", false
}
