package aws

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awsemr "github.com/aws/aws-sdk-go-v2/service/emr"
	emrtypes "github.com/aws/aws-sdk-go-v2/service/emr/types"
	awswafv2 "github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
	awsworkspaces "github.com/aws/aws-sdk-go-v2/service/workspaces"
)

// Client VPN, EMR, WorkSpaces directories and WAF web ACL associations have
// no Cloud Control list, read and delete handlers in the official
// CloudFormation schemas, so they use their product APIs.

type ClientVPNNativeAPI interface {
	DescribeClientVpnEndpoints(context.Context, *awsec2.DescribeClientVpnEndpointsInput, ...func(*awsec2.Options)) (*awsec2.DescribeClientVpnEndpointsOutput, error)
	DeleteClientVpnEndpoint(context.Context, *awsec2.DeleteClientVpnEndpointInput, ...func(*awsec2.Options)) (*awsec2.DeleteClientVpnEndpointOutput, error)
	DescribeClientVpnTargetNetworks(context.Context, *awsec2.DescribeClientVpnTargetNetworksInput, ...func(*awsec2.Options)) (*awsec2.DescribeClientVpnTargetNetworksOutput, error)
	DisassociateClientVpnTargetNetwork(context.Context, *awsec2.DisassociateClientVpnTargetNetworkInput, ...func(*awsec2.Options)) (*awsec2.DisassociateClientVpnTargetNetworkOutput, error)
	DescribeClientVpnAuthorizationRules(context.Context, *awsec2.DescribeClientVpnAuthorizationRulesInput, ...func(*awsec2.Options)) (*awsec2.DescribeClientVpnAuthorizationRulesOutput, error)
	RevokeClientVpnIngress(context.Context, *awsec2.RevokeClientVpnIngressInput, ...func(*awsec2.Options)) (*awsec2.RevokeClientVpnIngressOutput, error)
	DescribeClientVpnRoutes(context.Context, *awsec2.DescribeClientVpnRoutesInput, ...func(*awsec2.Options)) (*awsec2.DescribeClientVpnRoutesOutput, error)
	DeleteClientVpnRoute(context.Context, *awsec2.DeleteClientVpnRouteInput, ...func(*awsec2.Options)) (*awsec2.DeleteClientVpnRouteOutput, error)
}

type EMRNativeAPI interface {
	ListClusters(context.Context, *awsemr.ListClustersInput, ...func(*awsemr.Options)) (*awsemr.ListClustersOutput, error)
	DescribeCluster(context.Context, *awsemr.DescribeClusterInput, ...func(*awsemr.Options)) (*awsemr.DescribeClusterOutput, error)
	SetTerminationProtection(context.Context, *awsemr.SetTerminationProtectionInput, ...func(*awsemr.Options)) (*awsemr.SetTerminationProtectionOutput, error)
	TerminateJobFlows(context.Context, *awsemr.TerminateJobFlowsInput, ...func(*awsemr.Options)) (*awsemr.TerminateJobFlowsOutput, error)
}

type WorkSpacesNativeAPI interface {
	DescribeWorkspaceDirectories(context.Context, *awsworkspaces.DescribeWorkspaceDirectoriesInput, ...func(*awsworkspaces.Options)) (*awsworkspaces.DescribeWorkspaceDirectoriesOutput, error)
	DeregisterWorkspaceDirectory(context.Context, *awsworkspaces.DeregisterWorkspaceDirectoryInput, ...func(*awsworkspaces.Options)) (*awsworkspaces.DeregisterWorkspaceDirectoryOutput, error)
}

type WAFNativeAPI interface {
	ListWebACLs(context.Context, *awswafv2.ListWebACLsInput, ...func(*awswafv2.Options)) (*awswafv2.ListWebACLsOutput, error)
	GetWebACL(context.Context, *awswafv2.GetWebACLInput, ...func(*awswafv2.Options)) (*awswafv2.GetWebACLOutput, error)
	ListResourcesForWebACL(context.Context, *awswafv2.ListResourcesForWebACLInput, ...func(*awswafv2.Options)) (*awswafv2.ListResourcesForWebACLOutput, error)
	GetWebACLForResource(context.Context, *awswafv2.GetWebACLForResourceInput, ...func(*awswafv2.Options)) (*awswafv2.GetWebACLForResourceOutput, error)
	DisassociateWebACL(context.Context, *awswafv2.DisassociateWebACLInput, ...func(*awswafv2.Options)) (*awswafv2.DisassociateWebACLOutput, error)
}

const (
	clientVPNEndpointType    = "AWS::EC2::ClientVpnEndpoint"
	clientVPNAssociationType = "AWS::EC2::ClientVpnTargetNetworkAssociation"
	clientVPNRuleType        = "AWS::EC2::ClientVpnAuthorizationRule"
	clientVPNRouteType       = "AWS::EC2::ClientVpnRoute"
	emrClusterType           = "AWS::EMR::Cluster"
	workspaceDirectoryType   = "AWS::WorkSpaces::WorkspaceDirectory"
	webACLAssociationType    = "AWS::WAFv2::WebACLAssociation"
	// Child listings read every parent page first; the bound stops a
	// misbehaving paginator.
	nativeMaxParentPages = 1000
	// Routes the service adds when a subnet is associated can only be removed
	// by disassociating that subnet.
	clientVPNRouteOriginAssociate = "associate"
)

func init() {
	for _, kind := range []nativeKind{clientVPNEndpointKind, clientVPNAssociationKind, clientVPNRuleKind, clientVPNRouteKind, emrClusterKind, workspaceDirectoryKind, webACLAssociationKind} {
		nativeKinds[kind.nativeType] = kind
	}
}

// clientVPNKey joins the endpoint ID with the fields that identify a child
// inside it. Child reads and deletes need the endpoint ID, and rules and routes
// have no service identifier of their own.
func clientVPNKey(parts ...string) string {
	return strings.Join(parts, "/")
}

// splitClientVPNKey returns the endpoint ID, the middle field and the last
// field. The middle field of rules and routes is a CIDR, which contains a
// slash; endpoint, group, subnet and association IDs do not.
func splitClientVPNKey(id string, parts int) ([]string, error) {
	values := strings.Split(id, "/")
	if len(values) < parts || (parts == 2 && len(values) != 2) || !strings.HasPrefix(values[0], "cvpn-endpoint-") {
		return nil, fmt.Errorf("AWS Client VPN child identifier %q is malformed", id)
	}
	if parts == 3 {
		values = []string{values[0], strings.Join(values[1:len(values)-1], "/"), values[len(values)-1]}
	}
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("AWS Client VPN child identifier %q is malformed", id)
		}
	}
	return values, nil
}

func clientVPNRuleKey(document map[string]any) string {
	group := stringValue(document["GroupId"])
	if document["AccessAll"] == true || group == "" {
		group = "*"
	}
	return clientVPNKey(stringValue(document["ClientVpnEndpointId"]), stringValue(document["DestinationCidr"]), group)
}

func clientVPNRouteKey(document map[string]any) string {
	return clientVPNKey(stringValue(document["ClientVpnEndpointId"]), stringValue(document["DestinationCidr"]), stringValue(document["TargetSubnet"]))
}

func clientVPNEndpointIDs(ctx context.Context, c *NativeClients) ([]string, error) {
	var ids []string
	token := ""
	for pages := 0; ; pages++ {
		if pages >= nativeMaxParentPages {
			return nil, fmt.Errorf("AWS Client VPN endpoint listing exceeded %d pages", nativeMaxParentPages)
		}
		input := &awsec2.DescribeClientVpnEndpointsInput{MaxResults: awssdk.Int32(1000)}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.ClientVPN.DescribeClientVpnEndpoints(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, endpoint := range output.ClientVpnEndpoints {
			if id := awssdk.ToString(endpoint.ClientVpnEndpointId); id != "" {
				ids = append(ids, id)
			}
		}
		next := awssdk.ToString(output.NextToken)
		if next == "" {
			break
		}
		if next == token {
			return nil, fmt.Errorf("AWS DescribeClientVpnEndpoints repeated its page token")
		}
		token = next
	}
	sort.Strings(ids)
	return ids, nil
}

// clientVPNChildren lists one child collection of every endpoint and returns
// the complete set as one page, so a missing endpoint read fails the listing
// rather than returning a partial set.
func clientVPNChildren(ctx context.Context, c *NativeClients, list func(string, string) ([]map[string]any, string, string, error)) (nativePage, error) {
	endpoints, err := clientVPNEndpointIDs(ctx, c)
	if err != nil {
		return nativePage{}, err
	}
	var items []map[string]any
	requestID := ""
	for _, endpoint := range endpoints {
		token := ""
		for pages := 0; ; pages++ {
			if pages >= nativeMaxParentPages {
				return nativePage{}, fmt.Errorf("AWS Client VPN child listing of %s exceeded %d pages", endpoint, nativeMaxParentPages)
			}
			documents, next, id, err := list(endpoint, token)
			if nativeNotFound(err, "InvalidClientVpnEndpointId.NotFound") {
				break // Deleted after the endpoint listing.
			}
			if err != nil {
				return nativePage{}, err
			}
			requestID = id
			items = append(items, documents...)
			if next == "" {
				break
			}
			if next == token {
				return nativePage{}, fmt.Errorf("AWS Client VPN child listing repeated its page token")
			}
			token = next
		}
	}
	return nativePage{Items: items, RequestID: requestID}, nil
}

func removed(requestID string, err error) (string, error) {
	if err != nil {
		return requestIDFromNormalized(NormalizeError(err)), err
	}
	return requestID, nil
}

var clientVPNEndpointKind = nativeKind{
	nativeType: clientVPNEndpointType, service: "ec2", identity: "ClientVpnEndpointId", statePath: "Status.Code",
	listOperation: "com.amazonaws.ec2#DescribeClientVpnEndpoints", readOperation: "com.amazonaws.ec2#DescribeClientVpnEndpoints", deleteOperation: "com.amazonaws.ec2#DeleteClientVpnEndpoint",
	deletingStates: []string{"deleting"}, absentStates: []string{"deleted"},
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsec2.DescribeClientVpnEndpointsInput{MaxResults: awssdk.Int32(1000)}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.ClientVPN.DescribeClientVpnEndpoints(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.ClientVpnEndpoints)
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.ClientVPN.DescribeClientVpnEndpoints(ctx, &awsec2.DescribeClientVpnEndpointsInput{ClientVpnEndpointIds: []string{id}})
		if nativeNotFound(err, "InvalidClientVpnEndpointId.NotFound") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		return singleNativeDocument(output.ClientVpnEndpoints, id, "ClientVpnEndpointId", requestIDOf(output.ResultMetadata))
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.ClientVPN.DeleteClientVpnEndpoint(ctx, &awsec2.DeleteClientVpnEndpointInput{ClientVpnEndpointId: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

var clientVPNAssociationKind = nativeKind{
	nativeType: clientVPNAssociationType, service: "ec2", identity: "AssociationKey", nameField: "TargetNetworkId", statePath: "Status.Code",
	listOperation: "com.amazonaws.ec2#DescribeClientVpnTargetNetworks", readOperation: "com.amazonaws.ec2#DescribeClientVpnTargetNetworks", deleteOperation: "com.amazonaws.ec2#DisassociateClientVpnTargetNetwork",
	deletingStates: []string{"disassociating"}, absentStates: []string{"disassociated"}, failedStates: []string{"association-failed"},
	list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
		return clientVPNChildren(ctx, c, func(endpoint, token string) ([]map[string]any, string, string, error) {
			input := &awsec2.DescribeClientVpnTargetNetworksInput{ClientVpnEndpointId: awssdk.String(endpoint), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.ClientVPN.DescribeClientVpnTargetNetworks(ctx, input)
			if err != nil {
				return nil, "", "", err
			}
			documents, err := clientVPNAssociationDocuments(output)
			return documents, awssdk.ToString(output.NextToken), requestIDOf(output.ResultMetadata), err
		})
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		parts, err := splitClientVPNKey(id, 2)
		if err != nil {
			return nil, "", err
		}
		output, err := c.ClientVPN.DescribeClientVpnTargetNetworks(ctx, &awsec2.DescribeClientVpnTargetNetworksInput{ClientVpnEndpointId: awssdk.String(parts[0]), AssociationIds: []string{parts[1]}})
		if nativeNotFound(err, "InvalidClientVpnEndpointId.NotFound", "InvalidClientVpnAssociationId.NotFound") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		documents, err := clientVPNAssociationDocuments(output)
		if err != nil {
			return nil, "", err
		}
		return singleDocument(documents, "AssociationKey", id, requestIDOf(output.ResultMetadata))
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		parts, err := splitClientVPNKey(id, 2)
		if err != nil {
			return "", err
		}
		output, err := c.ClientVPN.DisassociateClientVpnTargetNetwork(ctx, &awsec2.DisassociateClientVpnTargetNetworkInput{ClientVpnEndpointId: awssdk.String(parts[0]), AssociationId: awssdk.String(parts[1])})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

func clientVPNAssociationDocuments(output *awsec2.DescribeClientVpnTargetNetworksOutput) ([]map[string]any, error) {
	documents, err := nativeDocuments(output.ClientVpnTargetNetworks)
	for _, document := range documents {
		document["AssociationKey"] = clientVPNKey(stringValue(document["ClientVpnEndpointId"]), stringValue(document["AssociationId"]))
		// The association's subnet is its target network.
		document["SubnetId"] = document["TargetNetworkId"]
	}
	return documents, err
}

var clientVPNRuleKind = nativeKind{
	nativeType: clientVPNRuleType, service: "ec2", identity: "RuleKey", nameField: "DestinationCidr", statePath: "Status.Code",
	listOperation: "com.amazonaws.ec2#DescribeClientVpnAuthorizationRules", readOperation: "com.amazonaws.ec2#DescribeClientVpnAuthorizationRules", deleteOperation: "com.amazonaws.ec2#RevokeClientVpnIngress",
	deletingStates: []string{"revoking"}, failedStates: []string{"failed"},
	list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
		return clientVPNChildren(ctx, c, func(endpoint, token string) ([]map[string]any, string, string, error) {
			input := &awsec2.DescribeClientVpnAuthorizationRulesInput{ClientVpnEndpointId: awssdk.String(endpoint), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.ClientVPN.DescribeClientVpnAuthorizationRules(ctx, input)
			if err != nil {
				return nil, "", "", err
			}
			documents, err := clientVPNRuleDocuments(output)
			return documents, awssdk.ToString(output.NextToken), requestIDOf(output.ResultMetadata), err
		})
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		parts, err := splitClientVPNKey(id, 3)
		if err != nil {
			return nil, "", err
		}
		// The rule set of one endpoint is small; the filter API matches on
		// description and group only, so the rule is selected by its key.
		var documents []map[string]any
		requestID, token := "", ""
		for pages := 0; ; pages++ {
			if pages >= nativeMaxParentPages {
				return nil, requestID, fmt.Errorf("AWS Client VPN authorization rules of %s exceeded %d pages", parts[0], nativeMaxParentPages)
			}
			input := &awsec2.DescribeClientVpnAuthorizationRulesInput{ClientVpnEndpointId: awssdk.String(parts[0]), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.ClientVPN.DescribeClientVpnAuthorizationRules(ctx, input)
			if nativeNotFound(err, "InvalidClientVpnEndpointId.NotFound") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			page, err := clientVPNRuleDocuments(output)
			if err != nil {
				return nil, "", err
			}
			documents, requestID = append(documents, page...), requestIDOf(output.ResultMetadata)
			next := awssdk.ToString(output.NextToken)
			if next == "" || next == token {
				break
			}
			token = next
		}
		return singleDocument(documents, "RuleKey", id, requestID)
	},
	remove: func(ctx context.Context, c *NativeClients, id string, model map[string]any, _ string) (string, error) {
		parts, err := splitClientVPNKey(id, 3)
		if err != nil {
			return "", err
		}
		input := &awsec2.RevokeClientVpnIngressInput{ClientVpnEndpointId: awssdk.String(parts[0]), TargetNetworkCidr: awssdk.String(parts[1])}
		if parts[2] == "*" {
			input.RevokeAllGroups = awssdk.Bool(true)
		} else {
			input.AccessGroupId = awssdk.String(parts[2])
		}
		output, err := c.ClientVPN.RevokeClientVpnIngress(ctx, input)
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

func clientVPNRuleDocuments(output *awsec2.DescribeClientVpnAuthorizationRulesOutput) ([]map[string]any, error) {
	documents, err := nativeDocuments(output.AuthorizationRules)
	for _, document := range documents {
		document["RuleKey"] = clientVPNRuleKey(document)
	}
	return documents, err
}

var clientVPNRouteKind = nativeKind{
	nativeType: clientVPNRouteType, service: "ec2", identity: "RouteKey", nameField: "DestinationCidr", statePath: "Status.Code",
	listOperation: "com.amazonaws.ec2#DescribeClientVpnRoutes", readOperation: "com.amazonaws.ec2#DescribeClientVpnRoutes", deleteOperation: "com.amazonaws.ec2#DeleteClientVpnRoute",
	deletingStates: []string{"deleting"}, failedStates: []string{"failed"},
	list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
		return clientVPNChildren(ctx, c, func(endpoint, token string) ([]map[string]any, string, string, error) {
			input := &awsec2.DescribeClientVpnRoutesInput{ClientVpnEndpointId: awssdk.String(endpoint), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.ClientVPN.DescribeClientVpnRoutes(ctx, input)
			if err != nil {
				return nil, "", "", err
			}
			documents, err := clientVPNRouteDocuments(output)
			return documents, awssdk.ToString(output.NextToken), requestIDOf(output.ResultMetadata), err
		})
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		parts, err := splitClientVPNKey(id, 3)
		if err != nil {
			return nil, "", err
		}
		var documents []map[string]any
		requestID, token := "", ""
		for pages := 0; ; pages++ {
			if pages >= nativeMaxParentPages {
				return nil, requestID, fmt.Errorf("AWS Client VPN routes of %s exceeded %d pages", parts[0], nativeMaxParentPages)
			}
			input := &awsec2.DescribeClientVpnRoutesInput{ClientVpnEndpointId: awssdk.String(parts[0]), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.ClientVPN.DescribeClientVpnRoutes(ctx, input)
			if nativeNotFound(err, "InvalidClientVpnEndpointId.NotFound") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			page, err := clientVPNRouteDocuments(output)
			if err != nil {
				return nil, "", err
			}
			documents, requestID = append(documents, page...), requestIDOf(output.ResultMetadata)
			next := awssdk.ToString(output.NextToken)
			if next == "" || next == token {
				break
			}
			token = next
		}
		return singleDocument(documents, "RouteKey", id, requestID)
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		parts, err := splitClientVPNKey(id, 3)
		if err != nil {
			return "", err
		}
		output, err := c.ClientVPN.DeleteClientVpnRoute(ctx, &awsec2.DeleteClientVpnRouteInput{
			ClientVpnEndpointId: awssdk.String(parts[0]), DestinationCidrBlock: awssdk.String(parts[1]), TargetVpcSubnetId: awssdk.String(parts[2]),
		})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	precondition: func(model map[string]any) (bool, string) {
		if strings.EqualFold(stringValue(model["Origin"]), clientVPNRouteOriginAssociate) {
			return false, "The route was added when its target subnet was associated; it is removed by disassociating that subnet."
		}
		return true, ""
	},
}

func clientVPNRouteDocuments(output *awsec2.DescribeClientVpnRoutesOutput) ([]map[string]any, error) {
	documents, err := nativeDocuments(output.Routes)
	for _, document := range documents {
		document["RouteKey"] = clientVPNRouteKey(document)
		document["SubnetId"] = document["TargetSubnet"]
	}
	return documents, err
}

func singleDocument(documents []map[string]any, field, id, requestID string) (map[string]any, string, error) {
	for _, document := range documents {
		if stringValue(document[field]) == id {
			return document, requestID, nil
		}
	}
	return nil, requestID, errNativeAbsent
}

// Terminated clusters stay listable for about two months but no longer run
// or bill; only clusters that can still be terminated are inventoried.
var emrActiveStates = []string{"STARTING", "BOOTSTRAPPING", "RUNNING", "WAITING", "TERMINATING"}

func emrClusterMissing(err error) bool {
	if nativeNotFound(err) {
		return true
	}
	var providerError interface{ ErrorCode() string }
	if errors.As(err, &providerError) && providerError.ErrorCode() == "InvalidRequestException" {
		return strings.Contains(strings.ToLower(err.Error()), "is not valid")
	}
	return false
}

func describeEMRCluster(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
	output, err := c.EMR.DescribeCluster(ctx, &awsemr.DescribeClusterInput{ClusterId: awssdk.String(id)})
	if emrClusterMissing(err) {
		return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
	}
	if err != nil {
		return nil, requestIDFromNormalized(NormalizeError(err)), err
	}
	if output.Cluster == nil || awssdk.ToString(output.Cluster.Id) != id {
		return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
	}
	document, err := nativeDocument(output.Cluster)
	return document, requestIDOf(output.ResultMetadata), err
}

var emrClusterKind = nativeKind{
	nativeType: emrClusterType, service: "elasticmapreduce", identity: "Id", nameField: "Name", statePath: "Status.State",
	listOperation: "com.amazonaws.emr#ListClusters", readOperation: "com.amazonaws.emr#DescribeCluster", deleteOperation: "com.amazonaws.emr#TerminateJobFlows",
	deletingStates: []string{"TERMINATING"}, absentStates: []string{"TERMINATED", "TERMINATED_WITH_ERRORS"},
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		states := make([]emrtypes.ClusterState, 0, len(emrActiveStates))
		for _, state := range emrActiveStates {
			states = append(states, emrtypes.ClusterState(state))
		}
		input := &awsemr.ListClustersInput{ClusterStates: states}
		if token != "" {
			input.Marker = awssdk.String(token)
		}
		output, err := c.EMR.ListClusters(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		// Summaries omit the network, roles and protection; read each cluster
		// so relationships and protection are known at scan time.
		items := make([]map[string]any, 0, len(output.Clusters))
		for _, summary := range output.Clusters {
			document, _, err := describeEMRCluster(ctx, c, awssdk.ToString(summary.Id))
			if errors.Is(err, errNativeAbsent) {
				continue
			}
			if err != nil {
				return nativePage{}, err
			}
			items = append(items, document)
		}
		return nativePage{Items: items, NextToken: awssdk.ToString(output.Marker), RequestID: requestIDOf(output.ResultMetadata)}, nil
	},
	read: describeEMRCluster,
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.EMR.TerminateJobFlows(ctx, &awsemr.TerminateJobFlowsInput{JobFlowIds: []string{id}})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	protection: &nativeProtection{
		operation: "com.amazonaws.emr#SetTerminationProtection",
		enabled:   func(model map[string]any) bool { return model["TerminationProtected"] == true },
		disable: func(ctx context.Context, c *NativeClients, id string) (string, error) {
			output, err := c.EMR.SetTerminationProtection(ctx, &awsemr.SetTerminationProtectionInput{JobFlowIds: []string{id}, TerminationProtected: awssdk.Bool(false)})
			if err != nil {
				return removed("", err)
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
}

var workspaceDirectoryKind = nativeKind{
	nativeType: workspaceDirectoryType, service: "workspaces", identity: "DirectoryId", nameField: "DirectoryName", statePath: "State",
	listOperation: "com.amazonaws.workspaces#DescribeWorkspaceDirectories", readOperation: "com.amazonaws.workspaces#DescribeWorkspaceDirectories", deleteOperation: "com.amazonaws.workspaces#DeregisterWorkspaceDirectory",
	deletingStates: []string{"DEREGISTERING"}, absentStates: []string{"DEREGISTERED"}, failedStates: []string{"ERROR"},
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsworkspaces.DescribeWorkspaceDirectoriesInput{}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.WorkSpaces.DescribeWorkspaceDirectories(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.Directories)
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.WorkSpaces.DescribeWorkspaceDirectories(ctx, &awsworkspaces.DescribeWorkspaceDirectoriesInput{DirectoryIds: []string{id}})
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		return singleNativeDocument(output.Directories, id, "DirectoryId", requestIDOf(output.ResultMetadata))
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.WorkSpaces.DeregisterWorkspaceDirectory(ctx, &awsworkspaces.DeregisterWorkspaceDirectoryInput{DirectoryId: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

// Regional web ACLs protect resources through associations; CloudFront
// distributions carry their web ACL as a distribution property instead.
var wafRegionalResourceTypes = []waftypes.ResourceType{
	waftypes.ResourceTypeApplicationLoadBalancer, waftypes.ResourceTypeApiGateway, waftypes.ResourceTypeAppsync,
	waftypes.ResourceTypeCognitioUserPool, waftypes.ResourceTypeAppRunnerService, waftypes.ResourceTypeVerifiedAccessInstance,
	waftypes.ResourceTypeAmplify, waftypes.ResourceTypeAgentcoreGateway,
}

func webACLAssociationDocument(resourceARN string, acl *waftypes.WebACL) map[string]any {
	name, id := awssdk.ToString(acl.Name), awssdk.ToString(acl.Id)
	return map[string]any{
		"ResourceArn": resourceARN, "WebACLArn": awssdk.ToString(acl.ARN), "WebACLName": name, "WebACLId": id,
		"ManagedByFirewallManager": acl.ManagedByFirewallManager,
		// The Cloud Control identifier of the web ACL.
		"web_acl_id": name + "|" + id + "|REGIONAL",
	}
}

var webACLAssociationKind = nativeKind{
	nativeType: webACLAssociationType, service: "wafv2", identity: "ResourceArn", nameField: "ResourceArn",
	listOperation: "com.amazonaws.wafv2#ListResourcesForWebACL", readOperation: "com.amazonaws.wafv2#GetWebACLForResource", deleteOperation: "com.amazonaws.wafv2#DisassociateWebACL",
	list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
		var items []map[string]any
		seen := map[string]bool{}
		requestID, marker := "", ""
		for pages := 0; ; pages++ {
			if pages >= nativeMaxParentPages {
				return nativePage{}, fmt.Errorf("AWS WAF web ACL listing exceeded %d pages", nativeMaxParentPages)
			}
			input := &awswafv2.ListWebACLsInput{Scope: waftypes.ScopeRegional, Limit: awssdk.Int32(100)}
			if marker != "" {
				input.NextMarker = awssdk.String(marker)
			}
			output, err := c.WAF.ListWebACLs(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			requestID = requestIDOf(output.ResultMetadata)
			for _, summary := range output.WebACLs {
				detail, err := c.WAF.GetWebACL(ctx, &awswafv2.GetWebACLInput{Name: summary.Name, Id: summary.Id, Scope: waftypes.ScopeRegional})
				if nativeNotFound(err, "WAFNonexistentItemException") {
					continue
				}
				if err != nil {
					return nativePage{}, err
				}
				if detail.WebACL == nil {
					continue
				}
				for _, resourceType := range wafRegionalResourceTypes {
					resources, err := c.WAF.ListResourcesForWebACL(ctx, &awswafv2.ListResourcesForWebACLInput{WebACLArn: detail.WebACL.ARN, ResourceType: resourceType})
					if nativeNotFound(err, "WAFNonexistentItemException") {
						break
					}
					if err != nil {
						return nativePage{}, err
					}
					for _, arn := range resources.ResourceArns {
						if seen[arn] {
							continue // A resource has at most one web ACL.
						}
						seen[arn] = true
						document := webACLAssociationDocument(arn, detail.WebACL)
						document["ResourceType"] = string(resourceType)
						items = append(items, document)
					}
				}
			}
			next := awssdk.ToString(output.NextMarker)
			// WAF returns a marker even on the last page; an empty page ends it.
			if next == "" || next == marker || len(output.WebACLs) == 0 {
				break
			}
			marker = next
		}
		return nativePage{Items: items, RequestID: requestID}, nil
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.WAF.GetWebACLForResource(ctx, &awswafv2.GetWebACLForResourceInput{ResourceArn: awssdk.String(id)})
		if nativeNotFound(err, "WAFNonexistentItemException") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		if output.WebACL == nil {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		return webACLAssociationDocument(id, output.WebACL), requestIDOf(output.ResultMetadata), nil
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.WAF.DisassociateWebACL(ctx, &awswafv2.DisassociateWebACLInput{ResourceArn: awssdk.String(id)})
		if err != nil {
			return removed("", err)
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	precondition: func(model map[string]any) (bool, string) {
		if model["ManagedByFirewallManager"] == true {
			return false, "A Firewall Manager policy manages this web ACL and reapplies its associations; change the policy scope instead."
		}
		return true, ""
	},
}
