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

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
	"github.com/loomx-ai/steward/providers/alicloud"
	provideraws "github.com/loomx-ai/steward/providers/aws"
	"github.com/loomx-ai/steward/providers/gcp"
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
	if want := []asset.Provider{asset.ProviderAliCloud, asset.ProviderAWS, asset.ProviderAzure, asset.ProviderGCP}; !reflect.DeepEqual(providers, want) {
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

func TestNativeCloudAttachmentContributorsAreWiredIntoServer(t *testing.T) {
	for _, provider := range []asset.Provider{asset.ProviderGCP, asset.ProviderAzure} {
		t.Run(string(provider), func(t *testing.T) {
			controller := asset.Asset{ID: "vm", Identity: asset.Identity{Provider: provider, ConnectionID: "connection"}}
			child := asset.Asset{ID: "disk", Identity: asset.Identity{Provider: provider, ConnectionID: "connection"}}
			if provider == asset.ProviderGCP {
				controller.Identity.NativeType = "compute.googleapis.com/Instance"
				child.Identity.NativeType = "compute.googleapis.com/Disk"
				child.Identity.NativeID = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/boot"
				controller.Normalized = map[string]any{"project_id": "sample-project", "disks": []any{map[string]any{"source": child.Identity.NativeID, "deviceName": "boot", "autoDelete": true}}}
			} else {
				controller.Identity.NativeType = "Microsoft.Compute/virtualMachines"
				child.Identity.NativeType = "Microsoft.Compute/disks"
				child.Identity.NativeID = "/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/test/providers/Microsoft.Compute/disks/boot"
				controller.Normalized = map[string]any{"subscription_id": "11111111-1111-4111-8111-111111111111", "storageProfile": map[string]any{"osDisk": map[string]any{"managedDisk": map[string]any{"id": child.Identity.NativeID}, "deleteOption": "Delete"}}}
			}
			assets := []asset.Asset{controller, child}
			runtime := &azureServiceContributorRuntime{}
			directory := contributorRuntimeDirectory{}
			if provider == asset.ProviderAzure {
				directory.runtime = runtime
			}
			contributors, err := newLifecycleContributorResolver(directory).ResolveContributors(context.Background(), asset.CloudConnection{ID: "connection", Provider: provider}, assets)
			if err != nil {
				t.Fatal(err)
			}
			bindings := 0
			for _, contributor := range contributors {
				result, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil {
					t.Fatal(err)
				}
				for _, binding := range result.Bindings {
					if binding.ControllerAssetID == "vm" && binding.ManagedAssetID == "disk" {
						bindings++
					}
				}
			}
			if bindings != 1 {
				t.Fatalf("server omitted native attachment lifecycle: %d", bindings)
			}
			if provider == asset.ProviderAzure && !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{"connection"}) {
				t.Fatalf("Azure VM extension discovery missing: %v", runtime.connections)
			}
		})
	}
}

type contributorRuntimeDirectory struct {
	runtime contracts.Provider
}

type clusterContributorRuntime struct {
	connections []asset.ConnectionID
}

func (*clusterContributorRuntime) Provider() asset.Provider { return asset.ProviderAzure }
func (*clusterContributorRuntime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	panic("contributor discovery cannot mutate resources")
}
func (r *clusterContributorRuntime) ClusterLifecycle(_ context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	r.connections = append(r.connections, id)
	return emptyClusterContributor{}, nil
}

func (*clusterContributorRuntime) ServiceLifecycle(context.Context, asset.ConnectionID) (governance.Contributor, error) {
	return emptyClusterContributor{}, nil
}

type emptyClusterContributor struct{}

func (emptyClusterContributor) Contribute(context.Context, asset.ScopeID, []asset.Asset) (governance.Contribution, error) {
	return governance.Contribution{}, nil
}

func TestAzureClusterContributorUsesExplicitConnectionOnlyWhenRequired(t *testing.T) {
	runtime := &clusterContributorRuntime{}
	resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure}
	if _, err := resolver.ResolveContributors(context.Background(), connection, nil); err != nil || len(runtime.connections) != 0 {
		t.Fatalf("unneeded cluster API credentials requested: %+v %v", runtime.connections, err)
	}
	cluster := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeType: "Microsoft.ContainerService/managedClusters", ConnectionID: connection.ID}}
	contributors, err := resolver.ResolveContributors(context.Background(), connection, []asset.Asset{cluster, cluster})
	if err != nil || len(contributors) != 3 || !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{connection.ID}) {
		t.Fatalf("cluster discovery not wired exactly once: contributors=%d connections=%+v err=%v", len(contributors), runtime.connections, err)
	}
	if _, err := newLifecycleContributorResolver(contributorRuntimeDirectory{}).ResolveContributors(context.Background(), connection, []asset.Asset{cluster}); err == nil {
		t.Fatal("missing cluster lifecycle implementation silently accepted")
	}
}

type computeContributorRuntime struct{ connections []asset.ConnectionID }

func (*computeContributorRuntime) Provider() asset.Provider { return asset.ProviderGCP }
func (*computeContributorRuntime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	panic("contributor discovery cannot mutate resources")
}
func (r *computeContributorRuntime) ComputeLifecycle(_ context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	r.connections = append(r.connections, id)
	return emptyClusterContributor{}, nil
}

func TestGCPComputeContributorUsesExplicitConnectionAndReplacesStaticAttachments(t *testing.T) {
	for _, nativeType := range []string{"compute.googleapis.com/StoragePool", "compute.googleapis.com/InstanceGroupManager", "container.googleapis.com/Cluster", "container.googleapis.com/NodePool"} {
		t.Run(nativeType, func(t *testing.T) {
			runtime := &computeContributorRuntime{}
			resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
			connection := asset.CloudConnection{ID: "gcp-connection", Provider: asset.ProviderGCP}
			if _, err := resolver.ResolveContributors(context.Background(), connection, nil); err != nil || len(runtime.connections) != 0 {
				t.Fatalf("unneeded Compute credentials requested: %+v %v", runtime.connections, err)
			}
			group := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, NativeType: nativeType, ConnectionID: connection.ID}}
			contributors, err := resolver.ResolveContributors(context.Background(), connection, []asset.Asset{group, group})
			if err != nil || len(contributors) != 1 || !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{connection.ID}) {
				t.Fatalf("Compute lifecycle not wired once: contributors=%d connections=%+v err=%v", len(contributors), runtime.connections, err)
			}
			if _, err := newLifecycleContributorResolver(contributorRuntimeDirectory{}).ResolveContributors(context.Background(), connection, []asset.Asset{group}); err == nil {
				t.Fatal("missing Compute lifecycle implementation silently accepted")
			}
		})
	}
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

type serviceContributorRuntime struct{ connections []asset.ConnectionID }

func (*serviceContributorRuntime) Provider() asset.Provider { return asset.ProviderGCP }
func (*serviceContributorRuntime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	panic("service contributor cannot mutate resources")
}
func (r *serviceContributorRuntime) ServiceLifecycle(_ context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	r.connections = append(r.connections, id)
	return emptyClusterContributor{}, nil
}
func TestGCPServiceContributorUsesExplicitConnectionOnce(t *testing.T) {
	runtime := &serviceContributorRuntime{}
	resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
	connection := asset.CloudConnection{ID: "gcp-connection", Provider: asset.ProviderGCP}
	if _, err := resolver.ResolveContributors(context.Background(), connection, nil); err != nil || len(runtime.connections) != 0 {
		t.Fatalf("unneeded service lookup: %v", err)
	}
	parent := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: connection.ID, NativeType: "servicedirectory.googleapis.com/Namespace"}}
	contributors, err := resolver.ResolveContributors(context.Background(), connection, []asset.Asset{parent, parent})
	if err != nil || len(contributors) != 2 || !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{connection.ID}) {
		t.Fatalf("service lifecycle not connected: %d %v %v", len(contributors), runtime.connections, err)
	}
	if _, err := newLifecycleContributorResolver(contributorRuntimeDirectory{}).ResolveContributors(context.Background(), connection, []asset.Asset{parent}); err == nil {
		t.Fatal("missing service lifecycle silently accepted")
	}
}

type azureServiceContributorRuntime struct{ serviceContributorRuntime }

func (*azureServiceContributorRuntime) Provider() asset.Provider { return asset.ProviderAzure }
func TestAzureServiceContributorUsesExplicitConnectionOnce(t *testing.T) {
	runtime := &azureServiceContributorRuntime{}
	resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
	connection := asset.CloudConnection{ID: "azure-connection", Provider: asset.ProviderAzure}
	if _, err := resolver.ResolveContributors(context.Background(), connection, nil); err != nil || len(runtime.connections) != 0 {
		t.Fatalf("unneeded service lookup: %v", err)
	}
	parent := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: connection.ID, NativeType: "Microsoft.Network/networkWatchers"}}
	contributors, err := resolver.ResolveContributors(context.Background(), connection, []asset.Asset{parent, parent})
	if err != nil || len(contributors) != 2 || !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{connection.ID}) {
		t.Fatalf("service lifecycle not connected: %d %v %v", len(contributors), runtime.connections, err)
	}
	if _, err := newLifecycleContributorResolver(contributorRuntimeDirectory{}).ResolveContributors(context.Background(), connection, []asset.Asset{parent}); err == nil {
		t.Fatal("missing service lifecycle silently accepted")
	}
}

func TestAzureIndependentResourcesReceiveNativeReferenceDiscovery(t *testing.T) {
	for _, kind := range []string{
		"Microsoft.Insights/diagnosticSettings", "Microsoft.Insights/metricAlerts", "Microsoft.Insights/actionGroups",
		"Microsoft.Consumption/budgets", "Microsoft.Insights/workbooks", "Microsoft.Insights/components",
		"Microsoft.Storage/storageAccounts", "Microsoft.Compute/disks", "Microsoft.Example/unregistered",
	} {
		t.Run(kind, func(t *testing.T) {
			runtime := &azureServiceContributorRuntime{}
			resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
			connection := asset.CloudConnection{ID: "azure-connection", Provider: asset.ProviderAzure}
			value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: connection.ID, NativeType: kind}}
			contributors, err := resolver.ResolveContributors(t.Context(), connection, []asset.Asset{value, value})
			if err != nil || len(contributors) != 2 || !reflect.DeepEqual(runtime.connections, []asset.ConnectionID{connection.ID}) {
				t.Fatal("independent Azure source/target lost native dependency discovery", len(contributors), runtime.connections, err)
			}
			if _, err := newLifecycleContributorResolver(contributorRuntimeDirectory{}).ResolveContributors(t.Context(), connection, []asset.Asset{value}); err == nil {
				t.Fatal("missing native dependency discovery was silently accepted")
			}
		})
	}
}

func TestGCPCloudNatHubContributorIsConnectedWithoutCascade(t *testing.T) {
	runtime := &serviceContributorRuntime{}
	resolver := newLifecycleContributorResolver(contributorRuntimeDirectory{runtime: runtime})
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP}
	nat := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: connection.ID, NativeType: "compute.googleapis.com/RouterNat"}}
	contributors, err := resolver.ResolveContributors(t.Context(), connection, []asset.Asset{nat, nat})
	if err != nil || len(contributors) != 2 || len(runtime.connections) != 0 {
		t.Fatal(contributors, err, runtime.connections)
	}
	if _, ok := contributors[1].(*gcp.CloudNatHubs); !ok {
		t.Fatalf("missing NAT Hub contributor: %T", contributors[1])
	}
}
