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
	accountID       string
	principal       string
	bootstrapRegion string
	regions         []providerRegion
	network         NetworkClient
	networkRegion   string
}

func (*runtimeFactory) ResourceExplorer(context.Context, contracts.Credential, string) (ResourceExplorerClient, error) {
	return nil, errors.New("not used")
}

func (*runtimeFactory) CloudFormation(context.Context, contracts.Credential, string) (CloudFormationClient, error) {
	return nil, errors.New("not used")
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
