package hooks_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	provideraws "github.com/loomx-ai/steward/providers/aws"
	"github.com/loomx-ai/steward/providers/aws/hooks"
)

func TestCloudFormationContributesAuthoritativeDelegatedLifecycle(t *testing.T) {
	page := readStackResourcePage(t)
	client := &cloudFormationClient{resourcePages: []provideraws.StackResourcePage{page}}
	stack := awsAsset("stack", provideraws.CloudFormationStackNativeType, "arn:aws:cloudformation:us-east-1:123456789012:stack/application/id")
	stack.Normalized = map[string]any{"accountId": "123456789012"}
	instance := awsAsset("instance", "AWS::EC2::Instance", "arn:aws:ec2:us-east-1:123456789012:instance/i-123")
	instance.Normalized = map[string]any{"physicalId": "i-123", "accountId": "123456789012"}
	bucket := awsAsset("bucket", "AWS::S3::Bucket", "arn:aws:s3:::app-data-123456789012")
	bucket.Normalized = map[string]any{"physicalId": "app-data-123456789012", "accountId": "123456789012"}

	contribution, err := hooks.NewCloudFormation(client, "us-east-1").Contribute(context.Background(), "scope-root", []asset.Asset{stack, instance, bucket})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 2 || len(contribution.Relationships) != 2 || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution = %+v", contribution)
	}
	for _, binding := range contribution.Bindings {
		if binding.ControllerAssetID != stack.ID || binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDelegate || !binding.DirectCleanupAllowed || binding.Confidence != 1 {
			t.Fatalf("binding = %+v", binding)
		}
		if binding.Evidence["request_id"] != "cfn-list-request-1" || binding.Evidence["logical_id"] == "" {
			t.Fatalf("evidence = %+v", binding.Evidence)
		}
	}
	if len(client.listCalls) != 1 || client.listCalls[0].StackID != stack.Identity.NativeID {
		t.Fatalf("list calls = %+v", client.listCalls)
	}
}

func TestCloudFormationUsesReservedSystemTagsToResolveInventoryIdentity(t *testing.T) {
	stackID := "arn:aws:cloudformation:us-east-1:123456789012:stack/application/id"
	page := provideraws.StackResourcePage{
		RequestID: "request",
		Resources: []provideraws.StackResource{{
			NativeType: "AWS::EC2::Instance", PhysicalID: "i-123", LogicalID: "ApplicationServer",
		}},
	}
	client := &cloudFormationClient{resourcePages: []provideraws.StackResourcePage{page}}
	stack := awsAsset("stack", provideraws.CloudFormationStackNativeType, stackID)
	stack.Normalized = map[string]any{"accountId": "123456789012"}
	instance := awsAsset("instance", "AWS::EC2::Instance", "arn:aws:ec2:us-east-1:123456789012:instance/i-123")
	instance.Normalized = map[string]any{"accountId": "123456789012"}
	instance.Tags = map[string]string{
		provideraws.CloudFormationStackIDTagKey:   stackID,
		provideraws.CloudFormationStackNameTagKey: "application",
		provideraws.CloudFormationLogicalIDTagKey: "ApplicationServer",
	}

	contribution, err := hooks.NewCloudFormation(client, "us-east-1").Contribute(context.Background(), "scope-root", []asset.Asset{stack, instance})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 || contribution.Bindings[0].ManagedAssetID != instance.ID || len(contribution.Unresolved) != 0 {
		t.Fatalf("system tag identity resolution = %+v", contribution)
	}
	evidence := contribution.Bindings[0].Evidence
	if evidence["identity_resolution"] != "cloudformation_system_tags" ||
		evidence["system_tag_stack_id_matches"] != true ||
		evidence["system_tag_logical_id_matches"] != true {
		t.Fatalf("system tag evidence = %+v", evidence)
	}
}

func TestCloudFormationDoesNotTreatTagsAsCurrentMembershipWithoutStackAPIEvidence(t *testing.T) {
	stackID := "arn:aws:cloudformation:us-east-1:123456789012:stack/application/id"
	client := &cloudFormationClient{resourcePages: []provideraws.StackResourcePage{{RequestID: "request"}}}
	stack := awsAsset("stack", provideraws.CloudFormationStackNativeType, stackID)
	instance := awsAsset("instance", "AWS::EC2::Instance", "i-123")
	instance.Tags = map[string]string{
		provideraws.CloudFormationStackIDTagKey:   stackID,
		provideraws.CloudFormationLogicalIDTagKey: "ApplicationServer",
	}

	contribution, err := hooks.NewCloudFormation(client, "us-east-1").Contribute(context.Background(), "scope-root", []asset.Asset{stack, instance})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Relationships) != 0 || len(contribution.Unresolved) != 0 {
		t.Fatalf("tags without ListStackResources membership must not bind: %+v", contribution)
	}
}

func TestCloudFormationPhysicalIDsCannotCrossAccounts(t *testing.T) {
	page := provideraws.StackResourcePage{RequestID: "request", Resources: []provideraws.StackResource{{NativeType: "AWS::EC2::Instance", PhysicalID: "i-shared"}}}
	client := &cloudFormationClient{resourcePages: []provideraws.StackResourcePage{page}}
	stack := awsAsset("stack", provideraws.CloudFormationStackNativeType, "arn:aws:cloudformation:us-east-1:111111111111:stack/application/id")
	stack.Normalized = map[string]any{"accountId": "111111111111"}
	wanted := awsAsset("wanted", "AWS::EC2::Instance", "arn:aws:ec2:us-east-1:111111111111:instance/i-shared")
	wanted.Normalized = map[string]any{"physicalId": "i-shared", "accountId": "111111111111"}
	foreign := awsAsset("foreign", "AWS::EC2::Instance", "arn:aws:ec2:us-east-1:222222222222:instance/i-shared")
	foreign.Normalized = map[string]any{"physicalId": "i-shared", "accountId": "222222222222"}

	contribution, err := hooks.NewCloudFormation(client, "us-east-1").Contribute(context.Background(), "scope-root", []asset.Asset{stack, foreign, wanted})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 || contribution.Bindings[0].ManagedAssetID != wanted.ID || len(contribution.Unresolved) != 0 {
		t.Fatalf("cross-account physical ID resolution = %+v", contribution)
	}

	stack.Normalized = map[string]any{}
	client.listCalls = nil
	contribution, err = hooks.NewCloudFormation(client, "us-east-1").Contribute(context.Background(), "scope-root", []asset.Asset{stack, foreign, wanted})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Unresolved) != 1 {
		t.Fatalf("ambiguous physical ID must remain unresolved: %+v", contribution)
	}
}

type cloudFormationClient struct {
	resourcePages []provideraws.StackResourcePage
	listCalls     []provideraws.ListStackResourcesRequest
}

func (c *cloudFormationClient) ListStackResources(_ context.Context, request provideraws.ListStackResourcesRequest) (provideraws.StackResourcePage, error) {
	c.listCalls = append(c.listCalls, request)
	return c.resourcePages[len(c.listCalls)-1], nil
}

func (c *cloudFormationClient) DescribeStack(context.Context, string) (provideraws.StackDescription, string, error) {
	return provideraws.StackDescription{}, "", nil
}

func (c *cloudFormationClient) DeleteStack(context.Context, provideraws.DeleteStackRequest) (string, error) {
	return "", nil
}

func readStackResourcePage(t *testing.T) provideraws.StackResourcePage {
	t.Helper()
	payload, err := os.ReadFile("../fixtures/cloudformation-stack-resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var page provideraws.StackResourcePage
	if err := json.Unmarshal(payload, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func awsAsset(id asset.AssetID, nativeType, nativeID string) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope-region", Location: "us-east-1",
		Identity: asset.Identity{Provider: asset.ProviderAWS, Partition: "aws", ConnectionID: "connection-aws", NativeType: nativeType, NativeID: nativeID},
	}
}
