package aws

import (
	"context"
	"errors"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsdrs "github.com/aws/aws-sdk-go-v2/service/drs"
	drstypes "github.com/aws/aws-sdk-go-v2/service/drs/types"
	awsorganizations "github.com/aws/aws-sdk-go-v2/service/organizations"
	awspinpoint "github.com/aws/aws-sdk-go-v2/service/pinpoint"
	awsdomains "github.com/aws/aws-sdk-go-v2/service/route53domains"
	domaintypes "github.com/aws/aws-sdk-go-v2/service/route53domains/types"
	awsstoragegateway "github.com/aws/aws-sdk-go-v2/service/storagegateway"
	sgtypes "github.com/aws/aws-sdk-go-v2/service/storagegateway/types"
)

const organizationTreeSource = "organization-tree"

func init() {
	for _, kind := range []nativeKind{storageGatewayKind, registeredDomainKind, drsSourceServerKind, pinpointSMSTemplateKind} {
		nativeKinds[kind.nativeType] = kind
	}
}

// Storage Gateway reports a missing gateway as InvalidGatewayRequestException
// with the detailed error code GatewayNotFound.
func storageGatewayMissing(err error) bool {
	var invalid *sgtypes.InvalidGatewayRequestException
	return errors.As(err, &invalid) && invalid.Error_ != nil && invalid.Error_.ErrorCode == sgtypes.ErrorCodeGatewayNotFound
}

var storageGatewayKind = nativeKind{
	nativeType: "AWS::StorageGateway::Gateway", nameField: "GatewayName", service: "storagegateway", identity: "GatewayARN", statePath: "GatewayState",
	listOperation: "com.amazonaws.storagegateway#ListGateways", readOperation: "com.amazonaws.storagegateway#DescribeGatewayInformation", deleteOperation: "com.amazonaws.storagegateway#DeleteGateway",
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsstoragegateway.ListGatewaysInput{Limit: awssdk.Int32(100)}
		if token != "" {
			input.Marker = awssdk.String(token)
		}
		output, err := c.StorageGateway.ListGateways(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.Gateways)
		return nativePage{Items: items, NextToken: awssdk.ToString(output.Marker), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.StorageGateway.DescribeGatewayInformation(ctx, &awsstoragegateway.DescribeGatewayInformationInput{GatewayARN: awssdk.String(id)})
		if storageGatewayMissing(err) {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		if awssdk.ToString(output.GatewayARN) != id {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		document, err := nativeDocument(output)
		delete(document, "ResultMetadata")
		return document, requestIDOf(output.ResultMetadata), err
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.StorageGateway.DeleteGateway(ctx, &awsstoragegateway.DeleteGatewayInput{GatewayARN: awssdk.String(id)})
		if err != nil {
			return requestIDFromNormalized(NormalizeError(err)), err
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

var registeredDomainKind = nativeKind{
	nativeType: "AWS::Route53Domains::Domain", nameField: "DomainName", service: "route53domains", identity: "DomainName",
	listOperation: "com.amazonaws.route53domains#ListDomains", readOperation: "com.amazonaws.route53domains#ListDomains", deleteOperation: "com.amazonaws.route53domains#DeleteDomain",
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsdomains.ListDomainsInput{MaxItems: awssdk.Int32(100)}
		if token != "" {
			input.Marker = awssdk.String(token)
		}
		output, err := c.Domains.ListDomains(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.Domains)
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextPageMarker), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	// Absence is an exact-name miss in a filtered account listing, not an
	// interpretation of GetDomainDetail error text.
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.Domains.ListDomains(ctx, &awsdomains.ListDomainsInput{FilterConditions: []domaintypes.FilterCondition{{
			Name: domaintypes.ListDomainsAttributeNameDomainName, Operator: domaintypes.OperatorBeginsWith, Values: []string{id},
		}}, MaxItems: awssdk.Int32(100)})
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		return singleNative(output.Domains, id, func(domain domaintypes.DomainSummary) string { return awssdk.ToString(domain.DomainName) }, requestIDOf(output.ResultMetadata))
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.Domains.DeleteDomain(ctx, &awsdomains.DeleteDomainInput{DomainName: awssdk.String(id)})
		if err != nil {
			return requestIDFromNormalized(NormalizeError(err)), err
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

var drsSourceServerKind = nativeKind{
	nativeType: "AWS::DRS::SourceServer", service: "drs", identity: "SourceServerID", statePath: "DataReplicationInfo.DataReplicationState",
	listOperation: "com.amazonaws.drs#DescribeSourceServers", readOperation: "com.amazonaws.drs#DescribeSourceServers", deleteOperation: "com.amazonaws.drs#DeleteSourceServer",
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awsdrs.DescribeSourceServersInput{MaxResults: awssdk.Int32(200)}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.DRS.DescribeSourceServers(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		items, err := nativeDocuments(output.Items)
		for _, item := range items {
			if hostname := stringValue(nestedValue(item, "SourceProperties", "IdentificationHints", "Hostname")); hostname != "" {
				item["Name"] = hostname
			}
		}
		return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.DRS.DescribeSourceServers(ctx, &awsdrs.DescribeSourceServersInput{Filters: &drstypes.DescribeSourceServersRequestFilters{SourceServerIDs: []string{id}}})
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		return singleNative(output.Items, id, func(server drstypes.SourceServer) string { return awssdk.ToString(server.SourceServerID) }, requestIDOf(output.ResultMetadata))
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.DRS.DeleteSourceServer(ctx, &awsdrs.DeleteSourceServerInput{SourceServerID: awssdk.String(id)})
		if err != nil {
			return requestIDFromNormalized(NormalizeError(err)), err
		}
		return requestIDOf(output.ResultMetadata), nil
	},
	// DeleteSourceServer is only accepted after replication is disconnected.
	precondition: func(model map[string]any) (bool, string) {
		if strings.EqualFold(stringValue(nestedValue(model, "DataReplicationInfo", "DataReplicationState")), "DISCONNECTED") {
			return true, ""
		}
		return false, "Elastic Disaster Recovery source server must be disconnected before deletion"
	},
}

var pinpointSMSTemplateKind = nativeKind{
	nativeType: "AWS::Pinpoint::SmsTemplate", nameField: "TemplateName", service: "pinpoint", identity: "TemplateName",
	listOperation: "com.amazonaws.pinpoint#ListTemplates", readOperation: "com.amazonaws.pinpoint#GetSmsTemplate", deleteOperation: "com.amazonaws.pinpoint#DeleteSmsTemplate",
	list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
		input := &awspinpoint.ListTemplatesInput{TemplateType: awssdk.String("SMS"), PageSize: awssdk.String("100")}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := c.Pinpoint.ListTemplates(ctx, input)
		if err != nil {
			return nativePage{}, err
		}
		page := nativePage{RequestID: requestIDOf(output.ResultMetadata)}
		if output.TemplatesResponse != nil {
			page.NextToken = awssdk.ToString(output.TemplatesResponse.NextToken)
			items, err := nativeDocuments(output.TemplatesResponse.Item)
			if err != nil {
				return nativePage{}, err
			}
			page.Items = items
		}
		return page, nil
	},
	read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
		output, err := c.Pinpoint.GetSmsTemplate(ctx, &awspinpoint.GetSmsTemplateInput{TemplateName: awssdk.String(id)})
		if nativeNotFound(err, "NotFoundException") {
			return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
		}
		if err != nil {
			return nil, requestIDFromNormalized(NormalizeError(err)), err
		}
		if output.SMSTemplateResponse == nil || awssdk.ToString(output.SMSTemplateResponse.TemplateName) != id {
			return nil, requestIDOf(output.ResultMetadata), errNativeAbsent
		}
		document, err := nativeDocument(output.SMSTemplateResponse)
		return document, requestIDOf(output.ResultMetadata), err
	},
	remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
		output, err := c.Pinpoint.DeleteSmsTemplate(ctx, &awspinpoint.DeleteSmsTemplateInput{TemplateName: awssdk.String(id)})
		if err != nil {
			return requestIDFromNormalized(NormalizeError(err)), err
		}
		return requestIDOf(output.ResultMetadata), nil
	},
}

// organizationTreeParents returns every root and organizational unit ID. An
// account outside an organization has an authoritative empty tree.
func organizationTreeParents(ctx context.Context, client OrganizationsNativeAPI) ([]cloudControlParent, error) {
	var parents []cloudControlParent
	var queue []string
	token := ""
	for {
		input := &awsorganizations.ListRootsInput{}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		output, err := client.ListRoots(ctx, input)
		if nativeNotFound(err, "AWSOrganizationsNotInUseException") {
			return nil, nil
		}
		if err != nil {
			return nil, NormalizeError(err)
		}
		for _, root := range output.Roots {
			queue = append(queue, awssdk.ToString(root.Id))
		}
		if token = awssdk.ToString(output.NextToken); token == "" {
			break
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		if parent == "" || seen[parent] {
			continue
		}
		seen[parent] = true
		parents = append(parents, cloudControlParent{Identifier: parent, Properties: map[string]any{"Id": parent}})
		if len(seen) > 5000 {
			return nil, errors.New("AWS organization tree exceeds 5000 parents")
		}
		token := ""
		for {
			input := &awsorganizations.ListOrganizationalUnitsForParentInput{ParentId: awssdk.String(parent)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := client.ListOrganizationalUnitsForParent(ctx, input)
			if err != nil {
				return nil, NormalizeError(err)
			}
			for _, unit := range output.OrganizationalUnits {
				queue = append(queue, awssdk.ToString(unit.Id))
			}
			if token = awssdk.ToString(output.NextToken); token == "" {
				break
			}
		}
	}
	return parents, nil
}
