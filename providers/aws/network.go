package aws

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type NetworkListRequest struct {
	ParentNativeID string
	Cursor         string
	Limit          int
}

// Use the CloudFormation model's network fields, never arbitrary strings in
// tags or IAM policies, to establish network placement and deletion edges.
func normalizeCloudControlNetwork(model map[string]any) {
	fields := map[string][]string{
		"vpc_id":             {"VpcId", "VpcConfig.VpcId", "ResourcesVpcConfig.VpcId", "VPCOptions.VPCId"},
		"subnet_ids":         {"SubnetId", "SubnetIds", "Subnets", "VPCZoneIdentifier", "VpcConfig.SubnetIds", "ResourcesVpcConfig.SubnetIds", "VPCOptions.SubnetIds", "SubnetMappings.SubnetId", "NetworkInterfaces.SubnetId"},
		"security_group_ids": {"SecurityGroupIds", "SecurityGroups", "GroupSet", "VpcSecurityGroupIds", "VPCSecurityGroups", "VpcConfig.SecurityGroupIds", "ResourcesVpcConfig.SecurityGroupIds", "VPCOptions.SecurityGroupIds", "NetworkInterfaces.GroupSet"},
		"zone_id":            {"AvailabilityZone"},
	}
	for field, paths := range fields {
		set := map[string]bool{}
		for _, path := range paths {
			for _, value := range cloudControlNetworkValues(model, strings.Split(path, ".")) {
				if value = strings.TrimSpace(value); value != "" {
					set[value] = true
				}
			}
		}
		values := make([]string, 0, len(set))
		for value := range set {
			values = append(values, value)
		}
		sort.Strings(values)
		if field == "vpc_id" || field == "zone_id" {
			if len(values) == 1 {
				model[field] = values[0]
			}
		} else if len(values) > 0 {
			model[field] = values
		}
		if field == "subnet_ids" && len(values) == 1 {
			model["vswitch_id"] = values[0]
		}
	}
}

func cloudControlNetworkValues(value any, path []string) []string {
	if items, ok := value.([]any); ok {
		var result []string
		for _, item := range items {
			result = append(result, cloudControlNetworkValues(item, path)...)
		}
		return result
	}
	if len(path) > 0 {
		object, _ := value.(map[string]any)
		return cloudControlNetworkValues(object[path[0]], path[1:])
	}
	if text, ok := value.(string); ok {
		return strings.Split(text, ",")
	}
	return nil
}

func enrichCloudControlNetwork(ctx context.Context, client CloudControlClient, model map[string]any, subnetVPCs map[string]string) error {
	if stringValue(model["vpc_id"]) != "" {
		return nil
	}
	subnets, _ := model["subnet_ids"].([]string)
	parentType := "AWS::EC2::Subnet"
	if len(subnets) == 0 {
		subnets, _ = model["security_group_ids"].([]string)
		parentType = "AWS::EC2::SecurityGroup"
	}
	vpcID := ""
	for _, subnetID := range subnets {
		parent, known := subnetVPCs[subnetID]
		if !known {
			resource, _, err := client.GetResource(ctx, parentType, subnetID)
			if err != nil {
				if cloudControlNotFound(err) {
					return nil
				}
				return NormalizeError(err)
			}
			properties, err := cloudControlModel(resource.Properties)
			if err != nil {
				return err
			}
			parent = stringValue(properties["VpcId"])
			subnetVPCs[subnetID] = parent
		}
		if parent == "" || (vpcID != "" && parent != vpcID) {
			return nil
		}
		vpcID = parent
	}
	if vpcID != "" {
		model["vpc_id"] = vpcID
	}
	return nil
}

func cloudControlNetworkReferences(model map[string]any) []string {
	var result []string
	if vpcID := stringValue(model["vpc_id"]); vpcID != "" {
		result = append(result, vpcID)
	}
	for _, field := range []string{"subnet_ids", "security_group_ids"} {
		values, _ := model[field].([]string)
		result = append(result, values...)
	}
	return result
}

type NetworkItem struct {
	NativeID       string
	Name           string
	ParentNativeID string
}

type NetworkPage struct {
	Items     []NetworkItem
	NextToken string
	RequestID string
}

type NetworkClient interface {
	ListVPCs(context.Context, NetworkListRequest) (NetworkPage, error)
	ListVSwitches(context.Context, NetworkListRequest) (NetworkPage, error)
	InternetGatewayVPCs(context.Context, string) ([]string, error)
	DetachInternetGateway(context.Context, string, string) error
}

type internetGatewayAction struct {
	*CloudControlAction
	network NetworkClient
}

func (a *internetGatewayAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ActionResult{}, err
	}
	id := cloudControlIdentifier(request.Asset)
	vpcs, err := a.network.InternetGatewayVPCs(ctx, id)
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	for _, vpcID := range vpcs {
		if err := a.network.DetachInternetGateway(ctx, id, vpcID); err != nil {
			return contracts.ActionResult{}, NormalizeError(err)
		}
	}
	return a.CloudControlAction.Execute(ctx, request)
}

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	if query.Kind != asset.ScanTargetVPC && query.Kind != asset.ScanTargetVSwitch {
		return contracts.NetworkTargetPage{}, fmt.Errorf("AWS network target kind %q is unsupported", query.Kind)
	}
	region := strings.TrimSpace(query.RegionID)
	if region == "" {
		return contracts.NetworkTargetPage{}, fmt.Errorf("AWS network target region is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	credential, err := r.resolveCredential(ctx, query.ConnectionID)
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	client, err := r.factory.Network(ctx, credential, region)
	if err != nil {
		return contracts.NetworkTargetPage{}, NormalizeError(err)
	}
	request := NetworkListRequest{ParentNativeID: strings.TrimSpace(query.ParentNativeID), Cursor: strings.TrimSpace(query.Cursor), Limit: limit}
	var providerPage NetworkPage
	if query.Kind == asset.ScanTargetVPC {
		providerPage, err = client.ListVPCs(ctx, request)
	} else {
		providerPage, err = client.ListVSwitches(ctx, request)
	}
	if err != nil {
		return contracts.NetworkTargetPage{}, NormalizeError(err)
	}
	needle := strings.ToLower(strings.TrimSpace(query.Query))
	page := contracts.NetworkTargetPage{NextCursor: providerPage.NextToken, RequestID: providerPage.RequestID}
	for _, item := range providerPage.Items {
		if !networkOptionMatches(needle, item.NativeID, item.Name) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{
			Kind: query.Kind, RegionID: region, NativeID: item.NativeID, Name: item.Name, ParentNativeID: item.ParentNativeID,
		})
	}
	return page, nil
}

func networkOptionMatches(needle string, values ...string) bool {
	if needle == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}
