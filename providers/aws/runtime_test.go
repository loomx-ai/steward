package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestValidateConnectionReturnsTypedExpiredCredentialError(t *testing.T) {
	runtime, err := newRuntime(&runtimeCredentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := time.Now().Add(-time.Minute)

	_, err = runtime.ValidateConnection(context.Background(), contracts.Credential{
		Type: asset.CredentialAWSSession, ExpiresAt: &expiredAt,
		Values: map[string]string{
			"access_key_id": "id", "secret_access_key": "secret", "session_token": "token",
		},
	})
	var validationErr *contracts.CredentialValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != "credential_expired" {
		t.Fatalf("ValidateConnection() error = %#v", err)
	}
}

type runtimeCredentialSource struct {
	want  asset.ConnectionID
	value contracts.Credential
	calls int
}

func (s *runtimeCredentialSource) Resolve(_ context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	s.calls++
	if connectionID != s.want {
		return contracts.Credential{}, errors.New("unexpected connection ID")
	}
	return s.value, nil
}

type runtimeFactory struct {
	accountID        string
	principal        string
	bootstrapRegion  string
	regions          []providerRegion
	network          NetworkClient
	networkRegion    string
	cloudControl     CloudControlClient
	cloudRegion      string
	resourceExplorer ResourceExplorerClient
	explorerRegion   string
}

func (f *runtimeFactory) ResourceExplorer(_ context.Context, _ contracts.Credential, region string) (ResourceExplorerClient, error) {
	f.explorerRegion = region
	if f.resourceExplorer == nil {
		return nil, errors.New("not used")
	}
	return f.resourceExplorer, nil
}

func (*runtimeFactory) CloudFormation(context.Context, contracts.Credential, string) (CloudFormationClient, error) {
	return nil, errors.New("not used")
}

func (f *runtimeFactory) CloudControl(_ context.Context, _ contracts.Credential, region string) (CloudControlClient, error) {
	f.cloudRegion = region
	if f.cloudControl == nil {
		return nil, errors.New("not used")
	}
	return f.cloudControl, nil
}

func (f *runtimeFactory) Network(_ context.Context, _ contracts.Credential, region string) (NetworkClient, error) {
	f.networkRegion = region
	if f.network == nil {
		return nil, errors.New("not used")
	}
	return f.network, nil
}

func (f *runtimeFactory) CallerIdentity(_ context.Context, _ contracts.Credential, region string) (string, string, error) {
	f.bootstrapRegion = region
	return f.accountID, f.principal, nil
}

func (f *runtimeFactory) DiscoverRegions(context.Context, contracts.Credential, string) ([]providerRegion, error) {
	return append([]providerRegion(nil), f.regions...), nil
}

func TestCredentialValidationAndRegionDiscoveryAreIndependent(t *testing.T) {
	t.Parallel()

	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{
		Type:   asset.CredentialAWSAccessKey,
		Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"},
	}}
	factory := &runtimeFactory{
		accountID: "123456789012", principal: "arn:aws:iam::123456789012:user/operator",
		regions: []providerRegion{
			{RegionID: " us-east-1 ", Name: "US East (N. Virginia)", Endpoint: "ec2.us-east-1.amazonaws.com", OptInStatus: "opt-in-not-required"},
			{RegionID: "ap-east-1", Name: "Asia Pacific (Hong Kong)", Endpoint: "ec2.ap-east-1.amazonaws.com", OptInStatus: "opted-in"},
			{RegionID: "me-central-1", Endpoint: "ec2.me-central-1.amazonaws.com", OptInStatus: "not-opted-in"},
		},
	}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range runtime.CredentialSchemas() {
		for _, field := range schema.Fields {
			if field.Key == "region" {
				t.Fatalf("credential schema still contains region: %#v", schema)
			}
		}
	}
	identity, err := runtime.ValidateConnection(context.Background(), source.value)
	if err != nil {
		t.Fatal(err)
	}
	if factory.bootstrapRegion != "us-east-1" || identity.TenantID != factory.accountID || identity.Principal != factory.principal || len(identity.RootScopes) != 1 || identity.RootScopes[0].Kind != asset.ScopeAccount || identity.RootScopes[0].NativeID != factory.accountID || identity.RootScopes[0].Location != "" {
		t.Fatalf("identity = %#v, bootstrap region = %q", identity, factory.bootstrapRegion)
	}
	regions, err := runtime.DiscoverRegions(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || len(regions) != 2 || regions[0].RegionID != "ap-east-1" || regions[0].Metadata["opt_in_status"] != "opted-in" || regions[1].RegionID != "us-east-1" || regions[1].Metadata["endpoint"] != "ec2.us-east-1.amazonaws.com" || regions[1].Metadata["opt_in_status"] != "opt-in-not-required" {
		t.Fatalf("regions = %#v, credential calls = %d", regions, source.calls)
	}
}

type runtimeNetworkClient struct {
	vpcs       NetworkPage
	vswitches  NetworkPage
	lastQuery  NetworkListRequest
	listedKind asset.ScanTargetKind
}

func (c *runtimeNetworkClient) ListVPCs(_ context.Context, request NetworkListRequest) (NetworkPage, error) {
	c.lastQuery, c.listedKind = request, asset.ScanTargetVPC
	return c.vpcs, nil
}

func (c *runtimeNetworkClient) ListVSwitches(_ context.Context, request NetworkListRequest) (NetworkPage, error) {
	c.lastQuery, c.listedKind = request, asset.ScanTargetVSwitch
	return c.vswitches, nil
}

func TestRuntimeSearchesVPCsAndVSwitchesThroughEC2(t *testing.T) {
	t.Parallel()

	client := &runtimeNetworkClient{vpcs: NetworkPage{RequestID: "req-vpc", NextToken: "next", Items: []NetworkItem{
		{NativeID: "vpc-1", Name: "production"}, {NativeID: "vpc-2", Name: "sandbox"},
	}}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	factory := &runtimeFactory{network: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	page, err := runtime.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{
		ConnectionID: "connection-a", Kind: asset.ScanTargetVPC, RegionID: "us-east-1", Query: "prod", Cursor: "cursor", Limit: 25,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "vpc-1" || page.RequestID != "req-vpc" || page.NextCursor != "next" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if factory.networkRegion != "us-east-1" || client.listedKind != asset.ScanTargetVPC || client.lastQuery.Cursor != "cursor" || client.lastQuery.Limit != 25 {
		t.Fatalf("factory region=%q kind=%q query=%#v", factory.networkRegion, client.listedKind, client.lastQuery)
	}

	client.vswitches = NetworkPage{Items: []NetworkItem{{NativeID: "subnet-1", Name: "app", ParentNativeID: "vpc-1"}}}
	page, err = runtime.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{
		ConnectionID: "connection-a", Kind: asset.ScanTargetVSwitch, RegionID: "us-east-1", ParentNativeID: "vpc-1", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "subnet-1" || page.Items[0].ParentNativeID != "vpc-1" || client.lastQuery.ParentNativeID != "vpc-1" {
		t.Fatalf("page=%#v query=%#v err=%v", page, client.lastQuery, err)
	}
}

type runtimeCloudControlClient struct {
	page CloudControlPage
}

func (c *runtimeCloudControlClient) ListResources(context.Context, CloudControlListRequest) (CloudControlPage, error) {
	return c.page, nil
}

func (*runtimeCloudControlClient) GetResource(context.Context, string, string) (CloudControlResource, string, error) {
	return CloudControlResource{}, "", errors.New("not used")
}

func (*runtimeCloudControlClient) DeleteResource(context.Context, string, string, string) (CloudControlProgress, string, error) {
	return CloudControlProgress{}, "", errors.New("not used")
}

func (*runtimeCloudControlClient) GetResourceRequestStatus(context.Context, string) (CloudControlProgress, string, error) {
	return CloudControlProgress{}, "", errors.New("not used")
}

type runtimeResourceExplorerClient struct {
	page SearchPage
}

func (c *runtimeResourceExplorerClient) Search(context.Context, SearchRequest) (SearchPage, error) {
	return c.page, nil
}

func TestRuntimeRoutesAuthoritativeCloudControlInventoryAndDeduplicatesBroadIndex(t *testing.T) {
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	cloudClient := &runtimeCloudControlClient{page: CloudControlPage{Resources: []CloudControlResource{{Identifier: "i-1", Properties: `{"InstanceId":"i-1"}`}}}}
	explorerClient := &runtimeResourceExplorerClient{page: SearchPage{Resources: []SearchResource{
		{ARN: "arn:aws:ec2:us-east-1:123456789012:instance/i-1", NativeType: "AWS::EC2::Instance", Region: "us-east-1", AccountID: "123456789012"},
		{ARN: "arn:aws:cloudformation:us-east-1:123456789012:stack/app/id", NativeType: CloudFormationStackNativeType, Region: "us-east-1", AccountID: "123456789012"},
	}}}
	factory := &runtimeFactory{cloudControl: cloudClient, resourceExplorer: explorerClient}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	var instanceKind *asset.ResourceKind
	for _, compiled := range runtime.Bundle().Specs {
		if compiled.ResourceKind.NativeType == "AWS::EC2::Instance" {
			kind := compiled.ResourceKind
			instanceKind = &kind
			break
		}
	}
	if instanceKind == nil || !instanceKind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("EC2 Cloud Control kind = %+v", instanceKind)
	}
	scope := asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"}
	cloudBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, Source: cloudControlSource, ResourceKind: instanceKind,
	})
	if err != nil || len(cloudBatch.Items) != 1 || cloudBatch.Items[0].NativeID != "i-1" || factory.cloudRegion != "us-east-1" {
		t.Fatalf("cloud batch=%+v region=%q err=%v", cloudBatch, factory.cloudRegion, err)
	}
	indexBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, Source: "resource-explorer",
	})
	if err != nil || len(indexBatch.Items) != 1 || indexBatch.Items[0].NativeType != CloudFormationStackNativeType {
		t.Fatalf("index batch=%+v err=%v", indexBatch, err)
	}
}
