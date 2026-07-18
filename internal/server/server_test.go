package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
	"github.com/loomx-ai/steward/providers/alicloud"
	provideraws "github.com/loomx-ai/steward/providers/aws"
)

func TestServerSupportsOnlyTerminalDatabases(t *testing.T) {
	for _, driver := range []string{"memory", "mysql"} {
		if _, err := openRepositories(Config{DBDriver: driver, MigrationsDir: filepath.Join("..", "..", "migrations")}); err == nil {
			t.Fatalf("legacy database driver %q was accepted", driver)
		}
	}
	repositories, err := openRepositories(Config{DBDriver: "sqlite", DSN: filepath.Join(t.TempDir(), "server.db"), MigrationsDir: filepath.Join("..", "..", "migrations")})
	if err != nil || repositories == nil {
		t.Fatalf("open terminal sqlite repositories: %v", err)
	}
}

type failingCredentials struct{}

func (failingCredentials) Resolve(context.Context, asset.ConnectionID) (contracts.Credential, error) {
	return contracts.Credential{}, errors.New("not used during startup")
}

type providerCatalogHTTPBundle struct {
	Provider      asset.Provider       `json:"provider"`
	Revision      string               `json:"revision"`
	Hash          string               `json:"hash"`
	KindsRevision string               `json:"kinds_revision"`
	Kinds         []asset.ResourceKind `json:"kinds"`
	Specs         []spec.CompiledSpec  `json:"specs"`
}

type sharedProviderCatalog struct {
	bundles []spec.Bundle
	kinds   []asset.ResourceKind
}

func (c *sharedProviderCatalog) Bundles() []spec.Bundle {
	return c.bundles
}

func (c *sharedProviderCatalog) ResourceKinds(asset.Provider) ([]asset.ResourceKind, string, bool) {
	return c.kinds, "runtime-a", true
}

func TestProviderRegistryCompilesWithoutResolvingCredentials(t *testing.T) {
	registry, err := providerRegistry(failingCredentials{})
	if err != nil {
		t.Fatalf("registry=%+v err=%v", registry, err)
	}
	descriptors := registry.ProviderDescriptors()
	providers := make([]asset.Provider, 0, len(descriptors))
	for _, descriptor := range descriptors {
		providers = append(providers, descriptor.Provider)
	}
	if want := []asset.Provider{asset.ProviderAliCloud, asset.ProviderAWS}; !reflect.DeepEqual(providers, want) {
		t.Fatalf("registered providers = %v, want %v", providers, want)
	}
	if len(registry.Bundles()) != len(providers) {
		t.Fatalf("bundle count = %d, provider count = %d", len(registry.Bundles()), len(providers))
	}
	var alicloudDescriptor *contracts.ProviderDescriptor
	for index := range descriptors {
		if descriptors[index].Provider == asset.ProviderAliCloud {
			alicloudDescriptor = &descriptors[index]
		}
	}
	if alicloudDescriptor == nil || len(alicloudDescriptor.Sites) != 2 {
		t.Fatalf("Alibaba Cloud descriptor = %#v", alicloudDescriptor)
	}
	foundOAuth := false
	for _, schema := range alicloudDescriptor.CredentialSchemas {
		foundOAuth = foundOAuth || (schema.Type == asset.CredentialAliCloudOAuth && schema.Flow == "browser_oauth")
	}
	if !foundOAuth {
		t.Fatalf("Alibaba Cloud descriptor has no browser OAuth schema: %#v", alicloudDescriptor)
	}
}

func TestProviderCatalogHTTPDoesNotMutateBundleSource(t *testing.T) {
	compiledKind := asset.ResourceKind{
		ID:         "kind-a",
		Provider:   asset.ProviderAliCloud,
		NativeType: "ACS::ECS::Instance",
		FieldDisplayNames: map[string]map[string]string{
			"accountId": {"zh-CN": "Compiled label"},
		},
	}
	runtimeKind := compiledKind
	runtimeKind.FieldDisplayNames = map[string]map[string]string{
		"accountId": {"zh-CN": "Runtime label"},
	}
	catalog := &sharedProviderCatalog{
		bundles: []spec.Bundle{{
			Provider: asset.ProviderAliCloud,
			Revision: "compiled-a",
			Hash:     "hash-a",
			Specs: []spec.CompiledSpec{{
				ResourceKind: compiledKind,
			}},
		}},
		kinds: []asset.ResourceKind{runtimeKind},
	}
	router := httptransport.NewRouter(httptransport.Dependencies{
		Bundles: catalog,
		Authenticator: httptransport.NewStaticBearerAuthenticator([]httptransport.TokenBinding{{
			Token: "viewer-token",
			Principal: httptransport.Principal{
				Subject: "viewer",
				Roles:   []httptransport.Role{httptransport.RoleViewer},
			},
		}}),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/providers/catalog", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	got := catalog.bundles[0].Specs[0].ResourceKind.
		FieldDisplayNames["accountId"]["zh-CN"]
	if got != "Compiled label" {
		t.Fatalf("GET mutated compiled bundle source label to %q", got)
	}
}

func TestProviderCatalogHTTPExposesRuntimeResourceKinds(t *testing.T) {
	registry, err := providerRegistry(failingCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	router := httptransport.NewRouter(httptransport.Dependencies{
		Bundles: registry,
		Authenticator: httptransport.NewStaticBearerAuthenticator([]httptransport.TokenBinding{{
			Token: "viewer-token",
			Principal: httptransport.Principal{
				Subject: "viewer",
				Roles:   []httptransport.Role{httptransport.RoleViewer},
			},
		}}),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/providers/catalog", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var bundles []providerCatalogHTTPBundle
	if err := json.Unmarshal(response.Body.Bytes(), &bundles); err != nil {
		t.Fatal(err)
	}
	var alicloudBundle *providerCatalogHTTPBundle
	for index := range bundles {
		if bundles[index].Provider == asset.ProviderAliCloud {
			alicloudBundle = &bundles[index]
			break
		}
	}
	if alicloudBundle == nil {
		t.Fatalf("Alibaba Cloud bundle missing: %+v", bundles)
	}
	if alicloudBundle.Revision == "" || alicloudBundle.Hash == "" ||
		alicloudBundle.KindsRevision == "" {
		t.Fatalf("catalog revisions missing: %+v", alicloudBundle)
	}
	runtimeOnlyKind := findResourceKind(
		alicloudBundle.Kinds,
		"ACS::OSS::Bucket",
	)
	if runtimeOnlyKind.ID == "" {
		t.Fatalf("runtime-only OSS bucket kind missing from HTTP catalog")
	}

	runtimeInstance := findResourceKind(
		alicloudBundle.Kinds,
		"ACS::ECS::Instance",
	)
	if got := runtimeInstance.FieldDisplayNames["accountId"]["zh-CN"]; got != "账号 ID" {
		t.Fatalf("runtime accountId label = %q", got)
	}
	var compiledInstance asset.ResourceKind
	for _, compiled := range alicloudBundle.Specs {
		if compiled.ResourceKind.NativeType == "ACS::ECS::Instance" {
			compiledInstance = compiled.ResourceKind
			break
		}
	}
	if got := compiledInstance.FieldDisplayNames["accountId"]["zh-CN"]; got != "账号 ID" {
		t.Fatalf("compatible compiled-spec accountId label = %q", got)
	}
}

func findResourceKind(kinds []asset.ResourceKind, nativeType string) asset.ResourceKind {
	for _, kind := range kinds {
		if kind.NativeType == nativeType {
			return kind
		}
	}
	return asset.ResourceKind{}
}

func TestControllerLocationsAreUniqueSortedAndProviderScoped(t *testing.T) {
	assets := []asset.Asset{
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType}, Location: "cn-shanghai"},
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType}, Location: "cn-hangzhou"},
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType}, Location: "cn-hangzhou"},
		{Identity: asset.Identity{Provider: asset.ProviderAWS, NativeType: provideraws.CloudFormationStackNativeType}, Location: "us-east-1"},
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance"}, Location: "cn-beijing"},
	}
	got, err := controllerLocations(asset.ProviderAliCloud, assets)
	if err != nil || !reflect.DeepEqual(got, []string{"cn-hangzhou", "cn-shanghai"}) {
		t.Fatalf("controller locations = %v", got)
	}
	got, err = controllerLocations(asset.Provider("unsupported"), assets)
	if err != nil || len(got) != 0 {
		t.Fatalf("unsupported provider unexpectedly exposed lifecycle contributors: %v", got)
	}
	assets = append(assets, asset.Asset{ID: "cluster-without-location", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType}})
	if _, err := controllerLocations(asset.ProviderAliCloud, assets); err == nil {
		t.Fatal("lifecycle controller without a location was silently skipped")
	}
}

type contributorRuntimeDirectory struct {
	runtime contracts.Provider
}

func (d contributorRuntimeDirectory) Resolve(asset.Provider) (contracts.Provider, error) {
	return d.runtime, nil
}

type contributorRuntime struct {
	ackRegions []string
}

func (*contributorRuntime) Provider() asset.Provider {
	return asset.ProviderAliCloud
}

func (*contributorRuntime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	panic("contributor resolution must not invoke product actions")
}

func (r *contributorRuntime) ACK(_ context.Context, _ asset.ConnectionID, region string) (alicloud.ACKClient, error) {
	r.ackRegions = append(r.ackRegions, region)
	return emptyACKClient{}, nil
}

type emptyACKClient struct{}

func (emptyACKClient) DescribeClusterResources(context.Context, string, bool) ([]alicloud.ClusterResource, string, error) {
	return nil, "ack-resources", nil
}

func (emptyACKClient) DescribeClusterNodes(context.Context, string) ([]alicloud.ClusterNode, string, error) {
	return nil, "ack-nodes", nil
}

func (emptyACKClient) DeleteCluster(context.Context, alicloud.DeleteClusterRequest) (alicloud.DeleteClusterResponse, error) {
	panic("graph contributor must not delete clusters")
}

func TestAlibabaContributorResolverAddsStaticAndACKContributors(t *testing.T) {
	t.Parallel()

	runtime := &contributorRuntime{}
	resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
	assets := []asset.Asset{
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Disk"}, Location: "cn-beijing"},
		{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType}, Location: "cn-shanghai"},
	}
	contributors, err := resolver.ResolveContributors(context.Background(), asset.CloudConnection{
		ID: "connection-a", Provider: asset.ProviderAliCloud,
	}, assets)
	if err != nil {
		t.Fatalf("resolve Alibaba Cloud contributors: %v", err)
	}
	if !reflect.DeepEqual(runtime.ackRegions, []string{"cn-shanghai"}) {
		t.Fatalf("ACK regions = %v", runtime.ackRegions)
	}
	if len(contributors) != 13 {
		t.Fatalf("contributors = %d, want twelve Alibaba static contributors and one ACK", len(contributors))
	}
}
