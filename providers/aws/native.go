package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsautoscaling "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	awsdms "github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	dmstypes "github.com/aws/aws-sdk-go-v2/service/databasemigrationservice/types"
	awsdocdb "github.com/aws/aws-sdk-go-v2/service/docdb"
	docdbtypes "github.com/aws/aws-sdk-go-v2/service/docdb/types"
	awsdrs "github.com/aws/aws-sdk-go-v2/service/drs"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awseks "github.com/aws/aws-sdk-go-v2/service/eks"
	awsfsx "github.com/aws/aws-sdk-go-v2/service/fsx"
	awsopensearch "github.com/aws/aws-sdk-go-v2/service/opensearch"
	awsorganizations "github.com/aws/aws-sdk-go-v2/service/organizations"
	awspinpoint "github.com/aws/aws-sdk-go-v2/service/pinpoint"
	awsdomains "github.com/aws/aws-sdk-go-v2/service/route53domains"
	awsstoragegateway "github.com/aws/aws-sdk-go-v2/service/storagegateway"
	"github.com/aws/smithy-go/middleware"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	productAPISource = "product-api"
	productAPIHook   = "aws.product.resource"
	nativeWait       = 15 * time.Second
)

// Each interface is the subset of an official SDK client used by Steward, so
// the SDK clients satisfy them directly and fakes cannot drift in shape.
type EC2NativeAPI interface {
	DescribeImages(context.Context, *awsec2.DescribeImagesInput, ...func(*awsec2.Options)) (*awsec2.DescribeImagesOutput, error)
	DeregisterImage(context.Context, *awsec2.DeregisterImageInput, ...func(*awsec2.Options)) (*awsec2.DeregisterImageOutput, error)
	DisableImageDeregistrationProtection(context.Context, *awsec2.DisableImageDeregistrationProtectionInput, ...func(*awsec2.Options)) (*awsec2.DisableImageDeregistrationProtectionOutput, error)
	DescribeSnapshots(context.Context, *awsec2.DescribeSnapshotsInput, ...func(*awsec2.Options)) (*awsec2.DescribeSnapshotsOutput, error)
	DeleteSnapshot(context.Context, *awsec2.DeleteSnapshotInput, ...func(*awsec2.Options)) (*awsec2.DeleteSnapshotOutput, error)
}

type OpenSearchNativeAPI interface {
	ListDomainNames(context.Context, *awsopensearch.ListDomainNamesInput, ...func(*awsopensearch.Options)) (*awsopensearch.ListDomainNamesOutput, error)
	DescribeDomains(context.Context, *awsopensearch.DescribeDomainsInput, ...func(*awsopensearch.Options)) (*awsopensearch.DescribeDomainsOutput, error)
	DeleteDomain(context.Context, *awsopensearch.DeleteDomainInput, ...func(*awsopensearch.Options)) (*awsopensearch.DeleteDomainOutput, error)
}

type DocDBNativeAPI interface {
	DescribeDBClusters(context.Context, *awsdocdb.DescribeDBClustersInput, ...func(*awsdocdb.Options)) (*awsdocdb.DescribeDBClustersOutput, error)
	DescribeDBInstances(context.Context, *awsdocdb.DescribeDBInstancesInput, ...func(*awsdocdb.Options)) (*awsdocdb.DescribeDBInstancesOutput, error)
	DeleteDBCluster(context.Context, *awsdocdb.DeleteDBClusterInput, ...func(*awsdocdb.Options)) (*awsdocdb.DeleteDBClusterOutput, error)
	DeleteDBInstance(context.Context, *awsdocdb.DeleteDBInstanceInput, ...func(*awsdocdb.Options)) (*awsdocdb.DeleteDBInstanceOutput, error)
	ModifyDBCluster(context.Context, *awsdocdb.ModifyDBClusterInput, ...func(*awsdocdb.Options)) (*awsdocdb.ModifyDBClusterOutput, error)
}

type DMSNativeAPI interface {
	DescribeReplicationInstances(context.Context, *awsdms.DescribeReplicationInstancesInput, ...func(*awsdms.Options)) (*awsdms.DescribeReplicationInstancesOutput, error)
	DeleteReplicationInstance(context.Context, *awsdms.DeleteReplicationInstanceInput, ...func(*awsdms.Options)) (*awsdms.DeleteReplicationInstanceOutput, error)
}

type FSxNativeAPI interface {
	DescribeFileSystems(context.Context, *awsfsx.DescribeFileSystemsInput, ...func(*awsfsx.Options)) (*awsfsx.DescribeFileSystemsOutput, error)
	DeleteFileSystem(context.Context, *awsfsx.DeleteFileSystemInput, ...func(*awsfsx.Options)) (*awsfsx.DeleteFileSystemOutput, error)
}

type StorageGatewayNativeAPI interface {
	ListGateways(context.Context, *awsstoragegateway.ListGatewaysInput, ...func(*awsstoragegateway.Options)) (*awsstoragegateway.ListGatewaysOutput, error)
	DescribeGatewayInformation(context.Context, *awsstoragegateway.DescribeGatewayInformationInput, ...func(*awsstoragegateway.Options)) (*awsstoragegateway.DescribeGatewayInformationOutput, error)
	DeleteGateway(context.Context, *awsstoragegateway.DeleteGatewayInput, ...func(*awsstoragegateway.Options)) (*awsstoragegateway.DeleteGatewayOutput, error)
}

type DomainsNativeAPI interface {
	ListDomains(context.Context, *awsdomains.ListDomainsInput, ...func(*awsdomains.Options)) (*awsdomains.ListDomainsOutput, error)
	GetDomainDetail(context.Context, *awsdomains.GetDomainDetailInput, ...func(*awsdomains.Options)) (*awsdomains.GetDomainDetailOutput, error)
	DeleteDomain(context.Context, *awsdomains.DeleteDomainInput, ...func(*awsdomains.Options)) (*awsdomains.DeleteDomainOutput, error)
}

type DRSNativeAPI interface {
	DescribeSourceServers(context.Context, *awsdrs.DescribeSourceServersInput, ...func(*awsdrs.Options)) (*awsdrs.DescribeSourceServersOutput, error)
	DeleteSourceServer(context.Context, *awsdrs.DeleteSourceServerInput, ...func(*awsdrs.Options)) (*awsdrs.DeleteSourceServerOutput, error)
}

type PinpointNativeAPI interface {
	ListTemplates(context.Context, *awspinpoint.ListTemplatesInput, ...func(*awspinpoint.Options)) (*awspinpoint.ListTemplatesOutput, error)
	GetSmsTemplate(context.Context, *awspinpoint.GetSmsTemplateInput, ...func(*awspinpoint.Options)) (*awspinpoint.GetSmsTemplateOutput, error)
	DeleteSmsTemplate(context.Context, *awspinpoint.DeleteSmsTemplateInput, ...func(*awspinpoint.Options)) (*awspinpoint.DeleteSmsTemplateOutput, error)
}

type OrganizationsNativeAPI interface {
	ListRoots(context.Context, *awsorganizations.ListRootsInput, ...func(*awsorganizations.Options)) (*awsorganizations.ListRootsOutput, error)
	ListOrganizationalUnitsForParent(context.Context, *awsorganizations.ListOrganizationalUnitsForParentInput, ...func(*awsorganizations.Options)) (*awsorganizations.ListOrganizationalUnitsForParentOutput, error)
}

// NativeClients are regional product API clients for kinds without Cloud
// Control list/read/delete handlers.
type NativeClients struct {
	EC2            EC2NativeAPI
	OpenSearch     OpenSearchNativeAPI
	DocDB          DocDBNativeAPI
	DMS            DMSNativeAPI
	FSx            FSxNativeAPI
	StorageGateway StorageGatewayNativeAPI
	Domains        DomainsNativeAPI
	DRS            DRSNativeAPI
	Pinpoint       PinpointNativeAPI
	Organizations  OrganizationsNativeAPI
	Lifecycle      LifecycleEC2API
	AutoScaling    AutoScalingNativeAPI
	EKS            EKSNativeAPI
}

func newNativeClients(config awssdk.Config) *NativeClients {
	return &NativeClients{
		EC2: awsec2.NewFromConfig(config), OpenSearch: awsopensearch.NewFromConfig(config), DocDB: awsdocdb.NewFromConfig(config),
		DMS: awsdms.NewFromConfig(config), FSx: awsfsx.NewFromConfig(config), StorageGateway: awsstoragegateway.NewFromConfig(config),
		Domains: awsdomains.NewFromConfig(config), DRS: awsdrs.NewFromConfig(config), Pinpoint: awspinpoint.NewFromConfig(config),
		Organizations: awsorganizations.NewFromConfig(config), Lifecycle: awsec2.NewFromConfig(config),
		AutoScaling: awsautoscaling.NewFromConfig(config), EKS: awseks.NewFromConfig(config),
	}
}

type nativePage struct {
	Items     []map[string]any
	NextToken string
	RequestID string
}

// nativeKind binds one resource type to its official operations. The operation
// IDs are asserted against the specification and pinned Smithy catalog.
type nativeKind struct {
	nativeType      string
	service         string
	listOperation   string
	readOperation   string
	deleteOperation string
	identity        string
	nameField       string
	list            func(context.Context, *NativeClients, string) (nativePage, error)
	read            func(context.Context, *NativeClients, string) (map[string]any, string, error)
	remove          func(context.Context, *NativeClients, string, map[string]any, string) (string, error)
	statePath       string
	deletingStates  []string
	failedStates    []string
	protection      *nativeProtection
	precondition    func(map[string]any) (bool, string)
}

type nativeProtection struct {
	operation string
	enabled   func(map[string]any) bool
	disable   func(context.Context, *NativeClients, string) (string, error)
}

var errNativeAbsent = errors.New("native resource is absent")

func requestIDOf(metadata middleware.Metadata) string {
	value, _ := awsRequestID(metadata)
	return value
}

func nativeDocument(value any) (map[string]any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	pruneNulls(result)
	return result, nil
}

// SDK structs marshal every unset pointer as null; drop them so normalized
// properties contain only fields the service returned.
func pruneNulls(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if item == nil {
				delete(typed, key)
				continue
			}
			pruneNulls(item)
		}
	case []any:
		for _, item := range typed {
			pruneNulls(item)
		}
	}
}

func nativeDocuments[T any](values []T) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		document, err := nativeDocument(value)
		if err != nil {
			return nil, err
		}
		result = append(result, document)
	}
	return result, nil
}

func nativeNotFound(err error, codes ...string) bool {
	if err == nil {
		return false
	}
	var providerError *contracts.ProviderCallError
	if !errors.As(NormalizeError(err), &providerError) {
		return false
	}
	for _, code := range codes {
		if strings.EqualFold(providerError.Provider.Code, code) {
			return true
		}
	}
	return providerError.Provider.Category == execution.ErrorNotFound
}

var nativeKinds = map[string]nativeKind{
	"AWS::EC2::Image": {
		nativeType: "AWS::EC2::Image", nameField: "Name", service: "ec2", identity: "ImageId", statePath: "State",
		listOperation: "com.amazonaws.ec2#DescribeImages", readOperation: "com.amazonaws.ec2#DescribeImages", deleteOperation: "com.amazonaws.ec2#DeregisterImage",
		deletingStates: []string{"deregistered"}, failedStates: []string{"failed", "error"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsec2.DescribeImagesInput{Owners: []string{"self"}, IncludeDeprecated: awssdk.Bool(true), IncludeDisabled: awssdk.Bool(true), MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.EC2.DescribeImages(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.Images)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.EC2.DescribeImages(ctx, &awsec2.DescribeImagesInput{ImageIds: []string{id}, IncludeDeprecated: awssdk.Bool(true), IncludeDisabled: awssdk.Bool(true)})
			if nativeNotFound(err, "InvalidAMIID.NotFound", "InvalidAMIID.Unavailable") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNative(output.Images, id, func(image ec2types.Image) string { return awssdk.ToString(image.ImageId) }, requestIDOf(output.ResultMetadata))
		},
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.EC2.DeregisterImage(ctx, &awsec2.DeregisterImageInput{ImageId: awssdk.String(id)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
		protection: &nativeProtection{
			operation: "com.amazonaws.ec2#DisableImageDeregistrationProtection",
			enabled: func(model map[string]any) bool {
				value := strings.ToLower(stringValue(model["DeregistrationProtection"]))
				return strings.HasPrefix(value, "enabled")
			},
			disable: func(ctx context.Context, c *NativeClients, id string) (string, error) {
				output, err := c.EC2.DisableImageDeregistrationProtection(ctx, &awsec2.DisableImageDeregistrationProtectionInput{ImageId: awssdk.String(id)})
				if err != nil {
					return requestIDFromNormalized(NormalizeError(err)), err
				}
				return requestIDOf(output.ResultMetadata), nil
			},
		},
	},
	"AWS::EC2::Snapshot": {
		nativeType: "AWS::EC2::Snapshot", service: "ec2", identity: "SnapshotId", statePath: "State",
		listOperation: "com.amazonaws.ec2#DescribeSnapshots", readOperation: "com.amazonaws.ec2#DescribeSnapshots", deleteOperation: "com.amazonaws.ec2#DeleteSnapshot",
		failedStates: []string{"error"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}, MaxResults: awssdk.Int32(1000)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.EC2.DescribeSnapshots(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.Snapshots)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.EC2.DescribeSnapshots(ctx, &awsec2.DescribeSnapshotsInput{SnapshotIds: []string{id}})
			if nativeNotFound(err, "InvalidSnapshot.NotFound") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNative(output.Snapshots, id, func(snapshot ec2types.Snapshot) string { return awssdk.ToString(snapshot.SnapshotId) }, requestIDOf(output.ResultMetadata))
		},
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.EC2.DeleteSnapshot(ctx, &awsec2.DeleteSnapshotInput{SnapshotId: awssdk.String(id)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
	"AWS::OpenSearchService::Domain": {
		nativeType: "AWS::OpenSearchService::Domain", nameField: "DomainName", service: "opensearch", identity: "DomainName",
		listOperation: "com.amazonaws.opensearch#ListDomainNames", readOperation: "com.amazonaws.opensearch#DescribeDomains", deleteOperation: "com.amazonaws.opensearch#DeleteDomain",
		list: func(ctx context.Context, c *NativeClients, _ string) (nativePage, error) {
			output, err := c.OpenSearch.ListDomainNames(ctx, &awsopensearch.ListDomainNamesInput{})
			if err != nil {
				return nativePage{}, err
			}
			page := nativePage{RequestID: requestIDOf(output.ResultMetadata)}
			names := make([]string, 0, len(output.DomainNames))
			for _, domain := range output.DomainNames {
				names = append(names, awssdk.ToString(domain.DomainName))
			}
			// DescribeDomains accepts at most five names per request.
			for start := 0; start < len(names); start += 5 {
				end := min(start+5, len(names))
				described, err := c.OpenSearch.DescribeDomains(ctx, &awsopensearch.DescribeDomainsInput{DomainNames: names[start:end]})
				if err != nil {
					return nativePage{}, err
				}
				items, err := nativeDocuments(described.DomainStatusList)
				if err != nil {
					return nativePage{}, err
				}
				page.Items = append(page.Items, items...)
			}
			return page, nil
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.OpenSearch.DescribeDomains(ctx, &awsopensearch.DescribeDomainsInput{DomainNames: []string{id}})
			if nativeNotFound(err, "ResourceNotFoundException") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			model, requestID, err := singleNativeDocument(output.DomainStatusList, id, "DomainName", requestIDOf(output.ResultMetadata))
			if err == nil && model["Deleted"] == true {
				model["State"] = "deleting"
			}
			return model, requestID, err
		},
		statePath: "State", deletingStates: []string{"deleting"},
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.OpenSearch.DeleteDomain(ctx, &awsopensearch.DeleteDomainInput{DomainName: awssdk.String(id)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
	"AWS::DocDB::DBCluster": {
		nativeType: "AWS::DocDB::DBCluster", nameField: "DBClusterIdentifier", service: "docdb", identity: "DBClusterIdentifier", statePath: "Status",
		listOperation: "com.amazonaws.docdb#DescribeDBClusters", readOperation: "com.amazonaws.docdb#DescribeDBClusters", deleteOperation: "com.amazonaws.docdb#DeleteDBCluster",
		deletingStates: []string{"deleting"}, failedStates: []string{"failed", "inaccessible-encryption-credentials"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsdocdb.DescribeDBClustersInput{Filters: []docdbtypes.Filter{{Name: awssdk.String("engine"), Values: []string{"docdb"}}}, MaxRecords: awssdk.Int32(100)}
			if token != "" {
				input.Marker = awssdk.String(token)
			}
			output, err := c.DocDB.DescribeDBClusters(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.DBClusters)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.Marker), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.DocDB.DescribeDBClusters(ctx, &awsdocdb.DescribeDBClustersInput{DBClusterIdentifier: awssdk.String(id)})
			if nativeNotFound(err, "DBClusterNotFoundFault") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNativeDocument(output.DBClusters, id, "DBClusterIdentifier", requestIDOf(output.ResultMetadata))
		},
		// Member instances are separate assets ordered before the cluster; the
		// cluster itself is deleted without a final snapshot, as on Alibaba Cloud.
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.DocDB.DeleteDBCluster(ctx, &awsdocdb.DeleteDBClusterInput{DBClusterIdentifier: awssdk.String(id), SkipFinalSnapshot: awssdk.Bool(true)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
		protection: &nativeProtection{
			operation: "com.amazonaws.docdb#ModifyDBCluster",
			enabled:   func(model map[string]any) bool { return model["DeletionProtection"] == true },
			disable: func(ctx context.Context, c *NativeClients, id string) (string, error) {
				output, err := c.DocDB.ModifyDBCluster(ctx, &awsdocdb.ModifyDBClusterInput{DBClusterIdentifier: awssdk.String(id), DeletionProtection: awssdk.Bool(false), ApplyImmediately: awssdk.Bool(true)})
				if err != nil {
					return requestIDFromNormalized(NormalizeError(err)), err
				}
				return requestIDOf(output.ResultMetadata), nil
			},
		},
		precondition: func(model map[string]any) (bool, string) {
			members, _ := model["DBClusterMembers"].([]any)
			if len(members) > 0 {
				return false, "DocumentDB cluster still has member instances; delete them first"
			}
			return true, ""
		},
	},
	"AWS::DocDB::DBInstance": {
		nativeType: "AWS::DocDB::DBInstance", nameField: "DBInstanceIdentifier", service: "docdb", identity: "DBInstanceIdentifier", statePath: "DBInstanceStatus",
		listOperation: "com.amazonaws.docdb#DescribeDBInstances", readOperation: "com.amazonaws.docdb#DescribeDBInstances", deleteOperation: "com.amazonaws.docdb#DeleteDBInstance",
		deletingStates: []string{"deleting"}, failedStates: []string{"failed"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsdocdb.DescribeDBInstancesInput{Filters: []docdbtypes.Filter{{Name: awssdk.String("engine"), Values: []string{"docdb"}}}, MaxRecords: awssdk.Int32(100)}
			if token != "" {
				input.Marker = awssdk.String(token)
			}
			output, err := c.DocDB.DescribeDBInstances(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.DBInstances)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.Marker), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.DocDB.DescribeDBInstances(ctx, &awsdocdb.DescribeDBInstancesInput{DBInstanceIdentifier: awssdk.String(id)})
			if nativeNotFound(err, "DBInstanceNotFoundFault") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNativeDocument(output.DBInstances, id, "DBInstanceIdentifier", requestIDOf(output.ResultMetadata))
		},
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.DocDB.DeleteDBInstance(ctx, &awsdocdb.DeleteDBInstanceInput{DBInstanceIdentifier: awssdk.String(id)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
	"AWS::DMS::ReplicationInstance": {
		nativeType: "AWS::DMS::ReplicationInstance", nameField: "ReplicationInstanceIdentifier", service: "dms", identity: "ReplicationInstanceArn", statePath: "ReplicationInstanceStatus",
		listOperation: "com.amazonaws.databasemigrationservice#DescribeReplicationInstances", readOperation: "com.amazonaws.databasemigrationservice#DescribeReplicationInstances", deleteOperation: "com.amazonaws.databasemigrationservice#DeleteReplicationInstance",
		deletingStates: []string{"deleting"}, failedStates: []string{"failed"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsdms.DescribeReplicationInstancesInput{MaxRecords: awssdk.Int32(100)}
			if token != "" {
				input.Marker = awssdk.String(token)
			}
			output, err := c.DMS.DescribeReplicationInstances(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.ReplicationInstances)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.Marker), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.DMS.DescribeReplicationInstances(ctx, &awsdms.DescribeReplicationInstancesInput{Filters: []dmstypes.Filter{{Name: awssdk.String("replication-instance-arn"), Values: []string{id}}}})
			if nativeNotFound(err, "ResourceNotFoundFault") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNativeDocument(output.ReplicationInstances, id, "ReplicationInstanceArn", requestIDOf(output.ResultMetadata))
		},
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, _ string) (string, error) {
			output, err := c.DMS.DeleteReplicationInstance(ctx, &awsdms.DeleteReplicationInstanceInput{ReplicationInstanceArn: awssdk.String(id)})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
	"AWS::FSx::FileSystem": {
		nativeType: "AWS::FSx::FileSystem", service: "fsx", identity: "FileSystemId", statePath: "Lifecycle",
		listOperation: "com.amazonaws.fsx#DescribeFileSystems", readOperation: "com.amazonaws.fsx#DescribeFileSystems", deleteOperation: "com.amazonaws.fsx#DeleteFileSystem",
		deletingStates: []string{"DELETING"}, failedStates: []string{"FAILED", "MISCONFIGURED_UNAVAILABLE"},
		list: func(ctx context.Context, c *NativeClients, token string) (nativePage, error) {
			input := &awsfsx.DescribeFileSystemsInput{MaxResults: awssdk.Int32(100)}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			output, err := c.FSx.DescribeFileSystems(ctx, input)
			if err != nil {
				return nativePage{}, err
			}
			items, err := nativeDocuments(output.FileSystems)
			return nativePage{Items: items, NextToken: awssdk.ToString(output.NextToken), RequestID: requestIDOf(output.ResultMetadata)}, err
		},
		read: func(ctx context.Context, c *NativeClients, id string) (map[string]any, string, error) {
			output, err := c.FSx.DescribeFileSystems(ctx, &awsfsx.DescribeFileSystemsInput{FileSystemIds: []string{id}})
			if nativeNotFound(err, "FileSystemNotFound") {
				return nil, requestIDFromNormalized(NormalizeError(err)), errNativeAbsent
			}
			if err != nil {
				return nil, requestIDFromNormalized(NormalizeError(err)), err
			}
			return singleNativeDocument(output.FileSystems, id, "FileSystemId", requestIDOf(output.ResultMetadata))
		},
		// Omitting the per-type configuration keeps each file system type's
		// documented default final backup behavior.
		remove: func(ctx context.Context, c *NativeClients, id string, _ map[string]any, token string) (string, error) {
			output, err := c.FSx.DeleteFileSystem(ctx, &awsfsx.DeleteFileSystemInput{FileSystemId: awssdk.String(id), ClientRequestToken: awssdk.String(clientRequestToken(token))})
			if err != nil {
				return requestIDFromNormalized(NormalizeError(err)), err
			}
			return requestIDOf(output.ResultMetadata), nil
		},
	},
}

// FSx client request tokens are limited to 63 characters.
func clientRequestToken(token string) string {
	if len(token) <= 63 {
		return token
	}
	return token[len(token)-63:]
}

func singleNative[T any](values []T, id string, identity func(T) string, requestID string) (map[string]any, string, error) {
	for _, value := range values {
		if identity(value) == id {
			document, err := nativeDocument(value)
			return document, requestID, err
		}
	}
	return nil, requestID, errNativeAbsent
}

func singleNativeDocument[T any](values []T, id, identityField, requestID string) (map[string]any, string, error) {
	documents, err := nativeDocuments(values)
	if err != nil {
		return nil, requestID, err
	}
	for _, document := range documents {
		if stringValue(document[identityField]) == id {
			return document, requestID, nil
		}
	}
	return nil, requestID, errNativeAbsent
}

// NativeInventory lists one product API kind.
type NativeInventory struct {
	clients *NativeClients
	kind    nativeKind
}

func (i *NativeInventory) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.ResourceKind == nil || request.ResourceKind.NativeType != i.kind.nativeType {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS product API inventory kind mismatch")
	}
	operation := i.kind.listOperation[strings.Index(i.kind.listOperation, "#")+1:]
	execution.LogCloudAPIRequest(ctx, i.kind.service, operation, rawCloudPayload(map[string]any{"NextToken": request.Cursor}))
	page, err := i.kind.list(ctx, i.clients, request.Cursor)
	if err != nil {
		execution.LogCloudAPIFailure(ctx, i.kind.service, operation, err)
		return contracts.InventoryBatch{}, NormalizeError(err)
	}
	if page.NextToken != "" && page.NextToken == request.Cursor {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS %s repeated its page token", operation)
	}
	execution.LogCloudAPIResponse(ctx, i.kind.service, operation, rawCloudPayload(map[string]any{"RequestId": page.RequestID, "NextToken": page.NextToken, "Items": page.Items}))
	batch := contracts.InventoryBatch{Items: make([]contracts.InventoryItem, 0, len(page.Items)), NextCursor: page.NextToken, RequestID: page.RequestID, Complete: page.NextToken == ""}
	for _, model := range page.Items {
		identifier := strings.TrimSpace(stringValue(model[i.kind.identity]))
		if identifier == "" {
			return contracts.InventoryBatch{}, fmt.Errorf("AWS %s returned a %s without %s", operation, i.kind.nativeType, i.kind.identity)
		}
		batch.Items = append(batch.Items, nativeItem(i.kind, identifier, model, *request.ResourceKind, request.Scope))
	}
	return batch, nil
}

func nativeItem(kind nativeKind, identifier string, model map[string]any, resourceKind asset.ResourceKind, scope asset.Scope) contracts.InventoryItem {
	location := scope.Location
	if scope.Kind == asset.ScopeGlobal {
		location = ""
	} else if location == "" {
		location = scope.NativeID
	}
	normalized := cloneAnyMap(model)
	normalized["nativeIdentifier"] = identifier
	state := ""
	if kind.statePath != "" {
		if value, ok := valueAtDottedPath(model, kind.statePath); ok {
			state = fmt.Sprint(value)
			normalized["state"] = state
		}
	}
	name := ""
	if kind.nameField != "" {
		name = strings.TrimSpace(stringValue(model[kind.nameField]))
	}
	if name == "" {
		name = cloudControlName(model, identifier)
	}
	normalized["name"] = name
	normalizeCloudControlNetwork(normalized)
	deriveNativeReferences(kind.nativeType, normalized)
	tags := cloudControlTags(model)
	if len(tags) == 0 {
		tags = cloudControlTags(map[string]any{"Tags": model["TagList"]})
	}
	return contracts.InventoryItem{
		NativeType: kind.nativeType, NativeID: identifier, ResourceKind: resourceKind,
		Scope: contracts.InventoryScope{Kind: scope.Kind, NativeID: scope.NativeID, Name: scope.Name, Location: location},
		Name:  name, State: state, Location: location, Tags: tags, Normalized: normalized,
		Raw:           map[string]any{"TypeName": kind.nativeType, "Identifier": identifier, "Properties": model},
		NativeAliases: cloudControlAliases(model, identifier), NetworkReferences: cloudControlNetworkReferences(normalized),
	}
}

func deriveNativeReferences(nativeType string, model map[string]any) {
	switch nativeType {
	case "AWS::EC2::Image":
		values := cloudControlNetworkValues(model, []string{"BlockDeviceMappings", "Ebs", "SnapshotId"})
		if len(values) > 0 {
			model["snapshot_ids"] = values
		}
	case "AWS::DocDB::DBInstance", "AWS::DocDB::DBCluster":
		values := cloudControlNetworkValues(model, []string{"VpcSecurityGroups", "VpcSecurityGroupId"})
		if len(values) > 0 {
			model["security_group_ids"] = values
		}
		if vpc := stringValue(nestedValue(model, "DBSubnetGroup", "VpcId")); vpc != "" {
			model["vpc_id"] = vpc
		}
	case "AWS::DMS::ReplicationInstance":
		if vpc := stringValue(nestedValue(model, "ReplicationSubnetGroup", "VpcId")); vpc != "" {
			model["vpc_id"] = vpc
		}
		values := cloudControlNetworkValues(model, []string{"VpcSecurityGroups", "VpcSecurityGroupId"})
		if len(values) > 0 {
			model["security_group_ids"] = values
		}
	}
}

func nestedValue(model map[string]any, path ...string) any {
	value, _ := valueAtDottedPath(model, strings.Join(path, "."))
	return value
}

// NativeAction deletes one product API resource with live readback.
type NativeAction struct {
	clients *NativeClients
	kind    nativeKind
}

func (*NativeAction) DeletionCheckTimeout() time.Duration { return 2 * time.Hour }

func (a *NativeAction) identifier(request contracts.ActionRequest) (string, error) {
	if request.Action != "delete" || request.Asset.Identity.NativeType != a.kind.nativeType || strings.TrimSpace(request.IdempotencyKey) == "" {
		return "", fmt.Errorf("AWS product API delete requires a %s asset and idempotency key", a.kind.nativeType)
	}
	id := strings.TrimSpace(request.Asset.Identity.NativeID)
	if value := strings.TrimSpace(stringValue(request.Asset.Normalized["nativeIdentifier"])); value != "" {
		id = value
	}
	if id == "" {
		return "", fmt.Errorf("AWS product API delete requires a native identifier")
	}
	return id, nil
}

func (a *NativeAction) state(model map[string]any) string {
	value, _ := valueAtDottedPath(model, a.kind.statePath)
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func matchesState(state string, states []string) bool {
	for _, candidate := range states {
		if strings.EqualFold(state, candidate) {
			return true
		}
	}
	return false
}

func (a *NativeAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	id, err := a.identifier(request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	model, requestID, err := a.kind.read(ctx, a.clients, id)
	if errors.Is(err, errNativeAbsent) {
		return contracts.PreflightResult{Absent: true, Reason: "resource no longer exists", Evidence: map[string]any{"provider_request_id": requestID}}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, NormalizeError(err)
	}
	state := a.state(model)
	evidence := map[string]any{"provider_request_id": requestID, "identifier": id, "state": state, "read_operation": a.kind.readOperation}
	if matchesState(state, a.kind.deletingStates) {
		return contracts.PreflightResult{Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence}, nil
	}
	if a.kind.precondition != nil {
		if allowed, reason := a.kind.precondition(model); !allowed {
			return contracts.PreflightResult{Allowed: false, Reason: reason, Evidence: evidence}, nil
		}
	}
	if a.kind.protection != nil {
		enabled := a.kind.protection.enabled(model)
		evidence["deletion_protection"] = enabled
		if enabled {
			evidence["pre_delete_action"] = "disable_deletion_protection"
		}
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *NativeAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	id, err := a.identifier(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	model, _, err := a.kind.read(ctx, a.clients, id)
	if errors.Is(err, errNativeAbsent) {
		return contracts.ActionResult{Data: map[string]any{"phase": "absent"}, RetryAfter: time.Second}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	if matchesState(a.state(model), a.kind.deletingStates) {
		return contracts.ActionResult{Data: map[string]any{"phase": "deleting"}, RetryAfter: nativeWait}, nil
	}
	if a.kind.precondition != nil {
		if allowed, reason := a.kind.precondition(model); !allowed {
			return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorConflict, Code: "PreconditionFailed", Message: reason}}
		}
	}
	data := map[string]any{"phase": "delete"}
	if a.kind.protection != nil && a.kind.protection.enabled(model) {
		requestID, err := a.kind.protection.disable(ctx, a.clients, id)
		if err != nil {
			return contracts.ActionResult{}, NormalizeError(err)
		}
		data["protection_request_id"] = requestID
		model, _, err = a.kind.read(ctx, a.clients, id)
		if err != nil && !errors.Is(err, errNativeAbsent) {
			return contracts.ActionResult{}, NormalizeError(err)
		}
		if err == nil && a.kind.protection.enabled(model) {
			return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
				Category: execution.ErrorRetryable, Code: "DeletionProtectionStillEnabled", Message: "AWS resource still reports deletion protection",
			}, RetryAfter: nativeWait}
		}
	}
	requestID, err := a.kind.remove(ctx, a.clients, id, model, request.IdempotencyKey)
	if err != nil {
		if nativeNotFound(err) {
			return contracts.ActionResult{ProviderRequestID: requestID, Data: map[string]any{"phase": "absent"}, RetryAfter: time.Second}, nil
		}
		return contracts.ActionResult{}, NormalizeError(err)
	}
	data["delete_operation"] = a.kind.deleteOperation
	return contracts.ActionResult{ProviderRequestID: requestID, ProviderOperationID: request.IdempotencyKey, Data: data, RetryAfter: nativeWait}, nil
}

func (a *NativeAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: "absent", Data: result.Data}, nil
	}
	if matchesState(readback.State, a.kind.failedStates) {
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProviderFailure, Code: "DeleteFailed", Message: fmt.Sprintf("AWS %s entered state %s", a.kind.nativeType, readback.State),
		}}
	}
	return contracts.WaitResult{Done: false, RetryAfter: nativeWait, State: readback.State, Data: result.Data}, nil
}

func (a *NativeAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	id, err := a.identifier(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	model, requestID, err := a.kind.read(ctx, a.clients, id)
	if errors.Is(err, errNativeAbsent) {
		return contracts.ReadbackResult{Exists: false, State: "absent", Data: map[string]any{"provider_request_id": requestID}}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, NormalizeError(err)
	}
	state := a.state(model)
	// An AMI in the deregistered state is no longer a registered image.
	if a.kind.nativeType == "AWS::EC2::Image" && strings.EqualFold(state, "deregistered") {
		return contracts.ReadbackResult{Exists: false, State: state, Data: map[string]any{"provider_request_id": requestID}}, nil
	}
	return contracts.ReadbackResult{Exists: true, State: state, Data: map[string]any{"provider_request_id": requestID, "properties": model}}, nil
}

func awsRequestID(metadata middleware.Metadata) (string, bool) {
	return awsmiddleware.GetRequestIDMetadata(metadata)
}
