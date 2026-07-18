package contracts_test

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
	alihooks "github.com/loomx-ai/steward/providers/alicloud/hooks"
	provideraws "github.com/loomx-ai/steward/providers/aws"
	awshooks "github.com/loomx-ai/steward/providers/aws/hooks"
)

func TestInventoryContractAcrossProviders(t *testing.T) {
	tests := []struct {
		name       string
		provider   asset.Provider
		scope      asset.Scope
		adapter    contracts.InventoryAdapter
		wantScopes []asset.ScopeKind
	}{
		{
			name: "Alibaba Cloud", provider: asset.ProviderAliCloud,
			scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
			adapter: alicloud.NewInventory(
				&aliInventoryClient{},
				[]string{"ACS::ECS::Instance", "ACS::VPC::VPC"},
			),
			wantScopes: []asset.ScopeKind{asset.ScopeRegion},
		},
		{name: "AWS", provider: asset.ProviderAWS, scope: asset.Scope{Kind: asset.ScopeAccount, NativeID: "123456789012"}, adapter: provideraws.NewInventory(&awsInventoryClient{}, nil), wantScopes: []asset.ScopeKind{asset.ScopeGlobal, asset.ScopeRegion}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := contracts.InventoryRequest{ConnectionID: "connection", Scope: test.scope, Limit: 100}
			first, err := test.adapter.List(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if first.Complete || first.NextCursor == "" || first.RequestID == "" || len(first.Items) == 0 {
				t.Fatalf("first page lacks coverage provenance: %+v", first)
			}
			repeated, err := test.adapter.List(context.Background(), request)
			if err != nil || repeated.Items[0].NativeID != first.Items[0].NativeID || repeated.Items[0].NativeType != first.Items[0].NativeType {
				t.Fatalf("native identity is unstable: first=%+v repeated=%+v err=%v", first.Items, repeated.Items, err)
			}
			request.Cursor = first.NextCursor
			second, err := test.adapter.List(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !second.Complete || second.NextCursor != "" || second.RequestID == "" || len(second.Items) == 0 {
				t.Fatalf("terminal page lacks complete coverage: %+v", second)
			}
			seenScopes := map[asset.ScopeKind]bool{}
			for _, item := range append(first.Items, second.Items...) {
				if item.ResourceKind.Provider != test.provider || item.ResourceKind.NativeType != item.NativeType || !item.ResourceKind.Capabilities.Has(asset.CapabilityIndexed) || item.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
					t.Fatalf("catalog-only L0 kind = %+v item=%+v", item.ResourceKind, item)
				}
				if item.Raw == nil || item.Normalized == nil || item.Raw["normalized"] != nil {
					t.Fatalf("raw and normalized payloads are not separated: %+v", item)
				}
				scopeKind := item.Scope.Kind
				if scopeKind == "" {
					scopeKind = test.scope.Kind
				}
				seenScopes[scopeKind] = true
			}
			for _, want := range test.wantScopes {
				if !seenScopes[want] {
					t.Fatalf("scope %q missing from %v", want, seenScopes)
				}
			}
		})
	}
}

func TestInventoryContractSupportsOptionalBatchEnrichment(t *testing.T) {
	adapter := inventoryBatchEnricher{}
	var inventory contracts.InventoryAdapter = adapter
	enricher, ok := inventory.(contracts.InventoryBatchEnricher)
	if !ok {
		t.Fatal("inventory adapter does not expose batch enrichment")
	}
	items, err := enricher.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{}, []contracts.InventoryItem{{NativeID: "i-1"}})
	if err != nil || len(items) != 1 || items[0].Normalized["enriched"] != true {
		t.Fatalf("enriched items=%+v err=%v", items, err)
	}
}

func TestInventoryContractNormalizesPermissionAndThrottleFailures(t *testing.T) {
	tests := []struct {
		name       string
		normalize  func(error) error
		permission error
		throttle   error
	}{
		{name: "Alibaba Cloud", normalize: alicloud.NormalizeError, permission: &alicloud.APIError{Code: "Forbidden", Message: "denied", RequestID: "ali-denied", StatusCode: 403}, throttle: &alicloud.APIError{Code: "Throttling", Message: "slow", RequestID: "ali-throttle", StatusCode: 429}},
		{name: "AWS", normalize: provideraws.NormalizeError, permission: &provideraws.APIError{Code: "AccessDeniedException", Message: "denied", RequestID: "aws-denied", StatusCode: 403}, throttle: &provideraws.APIError{Code: "ThrottlingException", Message: "slow", RequestID: "aws-throttle", StatusCode: 429}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertProviderError(t, test.normalize(test.permission), execution.ErrorPermissionDenied)
			assertProviderError(t, test.normalize(test.throttle), execution.ErrorThrottled)
		})
	}
}

func TestLifecycleContractCollapsesACKAndCloudFormationIntoControllerOnlyActions(t *testing.T) {
	tests := []struct {
		name        string
		contributor lifecycleContributor
		controller  asset.Asset
		managed     asset.Asset
	}{
		{
			name: "ACK", contributor: alihooks.NewACK(&ackLifecycleClient{}, "cn-hangzhou"),
			controller: lifecycleAsset("ack", asset.ProviderAliCloud, alicloud.ACKClusterNativeType, "cluster-1"),
			managed:    lifecycleAsset("ack-child", asset.ProviderAliCloud, "ACS::ECS::Instance", "i-1"),
		},
		{
			name: "CloudFormation", contributor: awshooks.NewCloudFormation(&cloudFormationLifecycleClient{}, "us-east-1"),
			controller: lifecycleAsset("stack", asset.ProviderAWS, provideraws.CloudFormationStackNativeType, "stack-1"),
			managed:    lifecycleAsset("stack-child", asset.ProviderAWS, "AWS::EC2::Instance", "i-1"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contribution, err := test.contributor.Contribute(context.Background(), "scope", []asset.Asset{test.controller, test.managed})
			if err != nil {
				t.Fatal(err)
			}
			result, err := plan.Solve(plan.Input{
				ResolvedAssetIDs: []asset.AssetID{test.controller.ID, test.managed.ID}, Assets: []asset.Asset{test.controller, test.managed},
				Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings,
				Revision: plan.RevisionBinding{InventoryRevision: "inventory", GraphRevision: "graph", SpecBundleRevision: "bundle", SpecHash: "spec"},
			})
			if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || len(result.ImpactItems) != 1 || result.ImpactItems[0].AssetID != test.managed.ID {
				t.Fatalf("managed lifecycle result=%+v err=%v", result, err)
			}
			var controllerStep, verificationStep plan.CleanupTaskStep
			for _, step := range result.Steps {
				switch step.AssetID {
				case test.controller.ID:
					controllerStep = step
				case test.managed.ID:
					verificationStep = step
				}
			}
			if controllerStep.Kind != plan.StepController ||
				verificationStep.Kind != plan.StepVerification ||
				verificationStep.Action != plan.ActionVerifyManagedAbsent ||
				len(verificationStep.DependsOn) != 1 ||
				verificationStep.DependsOn[0] != controllerStep.ID {
				t.Fatalf("managed lifecycle steps=%+v", result.Steps)
			}
		})
	}
}

func TestCloudFormationLifecycleContractAllowsWarnedDirectMemberCleanup(t *testing.T) {
	controller := lifecycleAsset("stack", asset.ProviderAWS, provideraws.CloudFormationStackNativeType, "stack-1")
	managed := lifecycleAsset("stack-child", asset.ProviderAWS, "AWS::EC2::Instance", "i-1")
	contribution, err := awshooks.NewCloudFormation(&cloudFormationLifecycleClient{}, "us-east-1").
		Contribute(context.Background(), "scope", []asset.Asset{controller, managed})
	if err != nil {
		t.Fatal(err)
	}

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{managed.ID},
		Assets:            []asset.Asset{controller, managed},
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
		Revision: plan.RevisionBinding{
			InventoryRevision: "inventory", GraphRevision: "graph", SpecBundleRevision: "bundle", SpecHash: "spec",
		},
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 ||
		result.Steps[0].AssetID != managed.ID || result.Steps[0].Kind != plan.StepDirect {
		t.Fatalf("direct CloudFormation member cleanup result=%+v err=%v", result, err)
	}
	if len(result.Warnings) != 1 ||
		result.Warnings[0].Code != plan.WarningManagedResourceDirectCleanup ||
		result.Warnings[0].AssetID != managed.ID ||
		result.Warnings[0].ControllerID != controller.ID {
		t.Fatalf("direct CloudFormation member warnings=%+v", result.Warnings)
	}
}

func TestCoreAndApplicationDoNotImportProviderSDKs(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, directory := range []string{"internal/core", "internal/app"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return walkErr
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				value, _ := strconv.Unquote(imported.Path.Value)
				if strings.Contains(value, "/providers/") || strings.HasPrefix(value, "github.com/aws/") || strings.HasPrefix(value, "github.com/alibabacloud-go/") {
					t.Errorf("%s imports Provider implementation %q", path, value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func assertProviderError(t *testing.T, err error, category execution.ErrorCategory) {
	t.Helper()
	var normalized *contracts.ProviderCallError
	if !errors.As(err, &normalized) || normalized.Provider.Category != category || normalized.Provider.RequestID == "" {
		t.Fatalf("normalized error = %#v", err)
	}
}

type lifecycleContributor interface {
	Contribute(context.Context, asset.ScopeID, []asset.Asset) (governance.Contribution, error)
}

func lifecycleAsset(id asset.AssetID, provider asset.Provider, nativeType, nativeID string) asset.Asset {
	location := "us-east-1"
	if provider == asset.ProviderAliCloud {
		location = "cn-hangzhou"
	}
	return asset.Asset{ID: id, ScopeID: "scope", Location: location, Identity: asset.Identity{Provider: provider, Partition: string(provider), ConnectionID: "connection", NativeType: nativeType, NativeID: nativeID}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
}

type ackLifecycleClient struct{}

func (ackLifecycleClient) DescribeClusterResources(context.Context, string, bool) ([]alicloud.ClusterResource, string, error) {
	autoCreated := 1
	return []alicloud.ClusterResource{{InstanceID: "i-1", ResourceType: "ACS::ECS::Instance", AutoCreate: &autoCreated, CreatorType: "system", DeleteBehavior: alicloud.DeleteBehavior{DeleteByDefault: true}}}, "ack-request", nil
}
func (ackLifecycleClient) DescribeClusterNodes(context.Context, string) ([]alicloud.ClusterNode, string, error) {
	return nil, "ack-node-request", nil
}
func (ackLifecycleClient) DeleteCluster(context.Context, alicloud.DeleteClusterRequest) (alicloud.DeleteClusterResponse, error) {
	panic("lifecycle discovery must not delete")
}

type cloudFormationLifecycleClient struct{}

func (cloudFormationLifecycleClient) ListStackResources(context.Context, provideraws.ListStackResourcesRequest) (provideraws.StackResourcePage, error) {
	return provideraws.StackResourcePage{RequestID: "cfn-request", Resources: []provideraws.StackResource{{LogicalID: "Instance", PhysicalID: "i-1", NativeType: "AWS::EC2::Instance", Status: "CREATE_COMPLETE"}}}, nil
}
func (cloudFormationLifecycleClient) DescribeStack(context.Context, string) (provideraws.StackDescription, string, error) {
	panic("lifecycle discovery must not describe action state")
}
func (cloudFormationLifecycleClient) DeleteStack(context.Context, provideraws.DeleteStackRequest) (string, error) {
	panic("lifecycle discovery must not delete")
}

type aliInventoryClient struct{}

func (*aliInventoryClient) SearchResources(_ context.Context, request alicloud.SearchRequest) (alicloud.ResourcePage, error) {
	if request.NextToken == "" {
		return alicloud.ResourcePage{RequestID: "ali-page-1", NextToken: "next", Resources: []alicloud.ResourceRecord{{ResourceID: "i-1", ResourceType: "ACS::ECS::Instance", ResourceName: "one", RegionID: "cn-hangzhou"}}}, nil
	}
	return alicloud.ResourcePage{RequestID: "ali-page-2", Resources: []alicloud.ResourceRecord{{ResourceID: "vpc-1", ResourceType: "ACS::VPC::VPC", ResourceName: "two", RegionID: "cn-hangzhou"}}}, nil
}

func (*aliInventoryClient) BatchGetResourceConfigurations(
	_ context.Context,
	request alicloud.ResourceConfigurationRequest,
) (alicloud.ResourceConfigurationPage, error) {
	page := alicloud.ResourceConfigurationPage{RequestID: "ali-configurations"}
	for _, resource := range request.Resources {
		page.Resources = append(page.Resources, alicloud.ResourceRecord{
			RegionID: resource.RegionID, ResourceType: resource.ResourceType, ResourceID: resource.ResourceID,
			Configuration: map[string]any{},
		})
	}
	return page, nil
}

type awsInventoryClient struct{}

func (*awsInventoryClient) Search(_ context.Context, request provideraws.SearchRequest) (provideraws.SearchPage, error) {
	if request.NextToken == "" {
		return provideraws.SearchPage{RequestID: "aws-page-1", NextToken: "next", Resources: []provideraws.SearchResource{{ARN: "arn:aws:iam::123456789012:role/Admin", NativeType: "AWS::IAM::Role", AccountID: "123456789012", Properties: map[string]any{}}}}, nil
	}
	return provideraws.SearchPage{RequestID: "aws-page-2", Resources: []provideraws.SearchResource{{ARN: "arn:aws:ec2:us-east-1:123456789012:instance/i-1", NativeType: "AWS::EC2::Instance", AccountID: "123456789012", Region: "us-east-1", Properties: map[string]any{}}}}, nil
}

type inventoryBatchEnricher struct{}

func (inventoryBatchEnricher) List(context.Context, contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	return contracts.InventoryBatch{}, nil
}

func (inventoryBatchEnricher) EnrichInventoryBatch(_ context.Context, _ contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
	items[0].Normalized = map[string]any{"enriched": true}
	return items, nil
}
