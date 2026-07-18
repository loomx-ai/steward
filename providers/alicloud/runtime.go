package alicloud

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	cloudcredentials "github.com/aliyun/credentials-go/credentials"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

//go:embed catalog/generated/catalog.json specs/*.yaml resourcecenter/resources.json
var providerFiles embed.FS

type clientFactory interface {
	CallerIdentity(context.Context, contracts.Credential) (string, string, error)
	DiscoverRegions(context.Context, contracts.Credential) ([]providerRegion, error)
	ResourceCenter(context.Context, contracts.Credential, string) (ResourceCenterClient, error)
	Invoke(context.Context, contracts.Credential, string, catalog.Operation, contracts.Invocation) (contracts.InvocationResult, error)
	ACK(context.Context, contracts.Credential, string) (ACKClient, error)
}

type providerRegion struct {
	RegionID string
	Name     string
	Endpoint string
}

// Runtime owns Alibaba Cloud credentials and SDK construction. Neither SDK
// models nor secrets cross the provider contract.
type Runtime struct {
	credentials              contracts.CredentialSource
	factory                  clientFactory
	oauth                    *oauthMaterializer
	oauthAPI                 oauthAPI
	oauthNow                 func() time.Time
	catalog                  catalog.Catalog
	resourceCatalog          resourceCatalog
	resourceKinds            []asset.ResourceKind
	resourceKindByNativeType map[string]asset.ResourceKind
	resourceKindRevision     string
	bundle                   spec.Bundle
}

var _ contracts.InventoryBatchEnricher = (*Runtime)(nil)

type runtimeOption func(*Runtime)

func withOAuthAPI(client oauthAPI) runtimeOption {
	return func(runtime *Runtime) {
		runtime.oauthAPI = client
	}
}

func withOAuthClock(now func() time.Time) runtimeOption {
	return func(runtime *Runtime) {
		runtime.oauthNow = now
	}
}

func NewRuntime(credentials contracts.CredentialSource) (*Runtime, error) {
	return newRuntime(credentials, sdkClientFactory{})
}

func newRuntime(credentials contracts.CredentialSource, factory clientFactory, options ...runtimeOption) (*Runtime, error) {
	if credentials == nil {
		return nil, fmt.Errorf("Alibaba Cloud credential source is required")
	}
	if factory == nil {
		return nil, fmt.Errorf("Alibaba Cloud client factory is required")
	}
	providerCatalog, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	bundle, err := compileEmbeddedSpecs(providerCatalog)
	if err != nil {
		return nil, err
	}
	instanceResources, err := loadResourceCatalog(providerFiles)
	if err != nil {
		return nil, err
	}
	resourceKinds, resourceKindRevision := instanceResources.ResourceKinds(bundle, providerCatalog)
	resourceKindByNativeType := make(map[string]asset.ResourceKind, len(resourceKinds))
	for _, kind := range resourceKinds {
		resourceKindByNativeType[kind.NativeType] = kind
	}
	runtime := &Runtime{
		credentials:              credentials,
		factory:                  factory,
		oauthNow:                 time.Now,
		catalog:                  providerCatalog,
		resourceCatalog:          instanceResources,
		resourceKinds:            resourceKinds,
		resourceKindByNativeType: resourceKindByNativeType,
		resourceKindRevision:     resourceKindRevision,
		bundle:                   bundle,
	}
	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}
	if runtime.oauthNow == nil {
		runtime.oauthNow = time.Now
	}
	if runtime.oauthAPI == nil {
		runtime.oauthAPI = newOAuthClient(&http.Client{Timeout: 30 * time.Second}, runtime.oauthNow)
	}
	updater, _ := credentials.(contracts.CredentialUpdater)
	runtime.oauth = newOAuthMaterializer(runtime.oauthAPI, credentials, updater, runtime.oauthNow)
	return runtime, nil
}

func (r *Runtime) Provider() asset.Provider { return asset.ProviderAliCloud }

func (r *Runtime) ConnectionSites() []contracts.ProviderSite {
	return []contracts.ProviderSite{
		{Value: asset.ConnectionSiteCN, LabelKey: "sites.alicloudCN"},
		{Value: asset.ConnectionSiteINTL, LabelKey: "sites.alicloudINTL"},
	}
}

func (r *Runtime) CredentialSchemas() []contracts.CredentialSchema {
	accessKey := []contracts.CredentialField{
		{Key: "access_key_id", LabelKey: "credentials.accessKeyId", InputType: "text", Secret: true, Required: true},
		{Key: "access_key_secret", LabelKey: "credentials.accessSecret", InputType: "password", Secret: true, Required: true},
	}
	return []contracts.CredentialSchema{
		{Type: asset.CredentialAliCloudAccessKey, LabelKey: "credentials.alicloudAccessKey", Fields: append([]contracts.CredentialField(nil), accessKey...)},
		{Type: asset.CredentialAliCloudSTS, LabelKey: "credentials.alicloudSTS", Fields: append(accessKey,
			contracts.CredentialField{Key: "security_token", LabelKey: "credentials.securityToken", InputType: "password", Secret: true, Required: true},
			contracts.CredentialField{Key: "expires_at", LabelKey: "credentials.expiresAt", InputType: "datetime-local", Required: true},
		)},
		{Type: asset.CredentialAliCloudOAuth, LabelKey: "credentials.alicloudOAuth", Flow: "browser_oauth", Fields: []contracts.CredentialField{}},
	}
}

func (r *Runtime) ValidateConnection(ctx context.Context, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	if credential.Type != asset.CredentialAliCloudAccessKey &&
		credential.Type != asset.CredentialAliCloudSTS &&
		credential.Type != asset.CredentialAliCloudOAuth {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"credential_type_unsupported",
			"The credential type is not supported by Alibaba Cloud.",
			nil,
		)
	}
	materialized, err := r.materializeCredential(ctx, credential)
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	failureIdentity := contracts.ConnectionIdentity{CredentialVersion: materialized.Version}
	if materialized.Type == asset.CredentialAliCloudSTS && materialized.ExpiresAt == nil {
		return failureIdentity, contracts.NewCredentialValidationError(
			"credential_expiration_required",
			"The temporary credential must include an expiration time.",
			nil,
		)
	}
	if materialized.ExpiresAt != nil && !materialized.ExpiresAt.After(r.oauthNow()) {
		return failureIdentity, contracts.NewCredentialValidationError(
			"credential_expired",
			"The cloud credential has expired.",
			nil,
		)
	}
	if _, err := cloudCredential(materialized); err != nil {
		return failureIdentity, contracts.NewCredentialValidationError(
			"credential_fields_invalid",
			"The Alibaba Cloud credential fields are incomplete or invalid.",
			err,
		)
	}
	accountID, identity, err := r.factory.CallerIdentity(ctx, materialized)
	if err != nil {
		return failureIdentity, normalizeConnectionValidationError(err)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return failureIdentity, contracts.NewCredentialValidationError(
			"provider_identity_incomplete",
			"Alibaba Cloud did not return a usable account identity.",
			nil,
		)
	}
	principal := strings.TrimSpace(identity)
	if principal == "" {
		principal = accountID
	}
	return contracts.ConnectionIdentity{
		Partition: "public", TenantID: accountID, Principal: principal,
		RootScopes:        []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: accountID, Name: accountID}},
		CredentialVersion: materialized.Version,
	}, nil
}

func (r *Runtime) DiscoverRegions(ctx context.Context, connectionID asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	credential, err := r.resolveCredential(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	regions, err := r.factory.DiscoverRegions(ctx, credential)
	if err != nil {
		return nil, NormalizeError(err)
	}
	result := make([]contracts.DiscoveredRegion, 0, len(regions))
	for _, region := range regions {
		regionID := strings.TrimSpace(region.RegionID)
		if regionID == "" {
			continue
		}
		metadata := map[string]string{}
		if endpoint := strings.TrimSpace(region.Endpoint); endpoint != "" {
			metadata["endpoint"] = endpoint
		}
		result = append(result, contracts.DiscoveredRegion{RegionID: regionID, Name: strings.TrimSpace(region.Name), Metadata: metadata})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}

func (r *Runtime) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{
		{
			Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			AuthoritativeDefault: true, KindSpecific: true, NetworkClosure: true,
		},
		{
			Name:                 "product-api",
			RootScopeKinds:       []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			AuthoritativeDefault: true,
			KindSpecific:         true,
		},
		{
			Name:                 "cen-topology",
			RootScopeKinds:       []asset.ScopeKind{asset.ScopeRegion},
			AuthoritativeDefault: true,
			NetworkClosure:       true,
		},
	}
}

func (r *Runtime) InventorySourceForResourceKind(kind asset.ResourceKind, declared string) string {
	if r.resourceCatalog.IsInstance(kind.NativeType) {
		return "resource-center"
	}
	return strings.TrimSpace(declared)
}

func (r *Runtime) Bundle() spec.Bundle {
	payload, err := json.Marshal(r.bundle)
	if err != nil {
		return spec.Bundle{}
	}
	var clone spec.Bundle
	if err := json.Unmarshal(payload, &clone); err != nil {
		return spec.Bundle{}
	}
	return clone
}

func (r *Runtime) ResourceKinds() ([]asset.ResourceKind, string) {
	payload, err := json.Marshal(r.resourceKinds)
	if err != nil {
		return nil, ""
	}
	var cloned []asset.ResourceKind
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, ""
	}
	return cloned, r.resourceKindRevision
}

func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	source := strings.TrimSpace(request.Source)
	var compiled *spec.CompiledSpec
	if request.ResourceKind != nil {
		for index := range r.bundle.Specs {
			if r.bundle.Specs[index].ResourceKind.NativeType != request.ResourceKind.NativeType {
				continue
			}
			compiled = &r.bundle.Specs[index]
			if source == "" {
				source = strings.TrimSpace(compiled.Definition.Discovery.Source)
			}
			break
		}
	}
	if source == "" {
		source = "resource-center"
	}
	switch source {
	case "cen-topology":
		return r.listCENTopology(ctx, request)
	case "product-api":
		if compiled == nil {
			return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud product API inventory requires a compiled resource spec")
		}
		return r.listProductAPI(ctx, request, *compiled)
	case "resource-center":
	default:
		return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud inventory source %q is unsupported", source)
	}
	region, err := inventoryRegion(request)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	client, err := r.factory.ResourceCenter(ctx, credential, region)
	if err != nil {
		return contracts.InventoryBatch{}, NormalizeError(err)
	}
	batch, err := NewInventory(client, r.resourceCenterInventoryNativeTypes()).List(ctx, request)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	for index := range batch.Items {
		batch.Items[index].ResourceKind = r.resourceKind(batch.Items[index].NativeType)
		if err := r.projectResourceCenterConfiguration(&batch.Items[index]); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	return batch, nil
}

func (r *Runtime) resourceCenterInventoryNativeTypes() []string {
	return r.resourceCatalog.InstanceNativeTypes()
}

func (r *Runtime) resourceKind(nativeType string) asset.ResourceKind {
	if kind, exists := r.resourceKindByNativeType[nativeType]; exists {
		kind.FieldDisplayNames = cloneLocalizedFields(kind.FieldDisplayNames)
		kind.Properties = asset.CloneResourceProperties(kind.Properties)
		return kind
	}
	return asset.ResourceKind{}
}

func (r *Runtime) Invoke(ctx context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	operation, ok := r.catalog.Operation(strings.TrimSpace(invocation.Operation))
	if !ok {
		return contracts.InvocationResult{}, fmt.Errorf("Alibaba Cloud operation %q is not in the generated catalog", invocation.Operation)
	}
	region, err := invocationRegion(invocation)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	credential, err := r.resolveCredential(ctx, invocation.ConnectionID)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	canonical := invocation
	canonical.Operation = operation.Key()
	service := cloudAPIService(operation)
	operationName := strings.TrimSpace(operation.Name)
	if operationName == "" {
		operationName = operation.Key()
	}
	execution.LogCloudAPIRequest(ctx, service, operationName, rawCloudPayload(canonical.Parameters))
	result, err := r.factory.Invoke(ctx, credential, region, operation, canonical)
	if err != nil {
		normalized := NormalizeError(err)
		annotateProviderErrorOperation(normalized, canonical.Operation)
		LogCloudAPIError(ctx, service, operationName, normalized)
		return contracts.InvocationResult{}, normalized
	}
	responsePayload := rawCloudPayload(result.Data)
	responsePayload["RequestId"] = result.RequestID
	responsePayload["OperationId"] = result.OperationID
	execution.LogCloudAPIResponse(ctx, service, operationName, responsePayload)
	return result, nil
}

func annotateProviderErrorOperation(err error, operation string) {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) {
		return
	}
	if providerCall.Provider.Summary == nil {
		providerCall.Provider.Summary = map[string]any{}
	}
	providerCall.Provider.Summary["operation"] = strings.TrimSpace(operation)
}

func cloudAPIService(operation catalog.Operation) string {
	if operation.Call != nil {
		if product := strings.TrimSpace(operation.Call.Product); product != "" {
			return strings.ToLower(product)
		}
	}
	if service := strings.TrimSpace(operation.Service); service != "" {
		return strings.ToLower(service)
	}
	return "alicloud"
}

func (r *Runtime) ResolveAction(ctx context.Context, connectionID asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	if value.Identity.Provider != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud runtime cannot resolve action for provider %q", value.Identity.Provider)
	}
	region := strings.TrimSpace(value.Location)
	if region == "" {
		return nil, fmt.Errorf("Alibaba Cloud action asset %q requires a region location", value.ID)
	}
	if value.Identity.NativeType == ACKClusterNativeType {
		client, err := r.ACK(ctx, connectionID, region)
		if err != nil {
			return nil, err
		}
		return NewACKHook(client), nil
	}
	if value.Identity.NativeType == AliKafkaInstanceNativeType {
		return NewAliKafkaHook(r, connectionID, region)
	}
	if value.Identity.NativeType == BPStudioApplicationNativeType {
		return NewBPStudioHook(r, connectionID, region)
	}
	if value.Identity.NativeType == CloudFirewallInstanceNativeType {
		return NewCloudFirewallHook(r, connectionID, region)
	}
	if value.Identity.NativeType == endpointNativeType {
		return NewPrivateLinkEndpointHook(r, connectionID, region)
	}
	if value.Identity.NativeType == VPCGatewayEndpointNativeType {
		return NewVPCGatewayEndpointHook(r, connectionID, region)
	}
	if value.Identity.NativeType == DdosBgpInstanceNativeType {
		return NewDdosBgpHook(r, connectionID, region)
	}
	if value.Identity.NativeType == ENSInstanceNativeType {
		return NewENSHook(r, connectionID, region)
	}
	if value.Identity.NativeType == SSLCertificateNativeType {
		return NewSSLCertificateHook(r, connectionID, region)
	}
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == value.Identity.NativeType {
			return newSpecAction(r, connectionID, region, compiled)
		}
	}
	return nil, fmt.Errorf("Alibaba Cloud native type %q has no resource spec", value.Identity.NativeType)
}

// ACK constructs the narrow ACK client used by the lifecycle contributor and
// cluster action hook after resolving the connection credential.
func (r *Runtime) ACK(ctx context.Context, connectionID asset.ConnectionID, region string) (ACKClient, error) {
	if strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud ACK region is required")
	}
	credential, err := r.resolveCredential(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	client, err := r.factory.ACK(ctx, credential, strings.TrimSpace(region))
	if err != nil {
		return nil, NormalizeError(err)
	}
	return client, nil
}

func (r *Runtime) resolveCredential(ctx context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	if connectionID == "" {
		return contracts.Credential{}, fmt.Errorf("Alibaba Cloud connection ID is required")
	}
	credential, err := r.credentials.Resolve(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("resolve Alibaba Cloud connection credential: %w", err)
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		return contracts.Credential{}, fmt.Errorf("Alibaba Cloud credential has expired")
	}
	if len(credential.Values) == 0 {
		return contracts.Credential{}, fmt.Errorf("Alibaba Cloud credential is empty")
	}
	return r.materializeCredential(ctx, credential)
}

func (r *Runtime) materializeCredential(ctx context.Context, credential contracts.Credential) (contracts.Credential, error) {
	if credential.Type != asset.CredentialAliCloudOAuth {
		return credential, nil
	}
	if r.oauth == nil {
		return contracts.Credential{}, oauthRefreshUnavailable(nil)
	}
	return r.oauth.materialize(ctx, credential)
}

func LoadBundle() (spec.Bundle, error) {
	providerCatalog, err := loadCatalog()
	if err != nil {
		return spec.Bundle{}, err
	}
	return compileEmbeddedSpecs(providerCatalog)
}

func loadCatalog() (catalog.Catalog, error) {
	source, err := providerFiles.ReadFile("catalog/generated/catalog.json")
	if err != nil {
		return catalog.Catalog{}, fmt.Errorf("read embedded Alibaba Cloud catalog: %w", err)
	}
	providerCatalog, err := catalog.UnmarshalGenerated(source)
	if err != nil {
		return catalog.Catalog{}, fmt.Errorf("load embedded Alibaba Cloud catalog: %w", err)
	}
	if providerCatalog.Provider != asset.ProviderAliCloud {
		return catalog.Catalog{}, fmt.Errorf("embedded catalog provider is %q", providerCatalog.Provider)
	}
	return providerCatalog, nil
}

func compileEmbeddedSpecs(providerCatalog catalog.Catalog) (spec.Bundle, error) {
	entries, err := fs.ReadDir(providerFiles, "specs")
	if err != nil {
		return spec.Bundle{}, fmt.Errorf("read embedded Alibaba Cloud specs: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	sources := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		source, err := providerFiles.ReadFile("specs/" + entry.Name())
		if err != nil {
			return spec.Bundle{}, fmt.Errorf("read embedded Alibaba Cloud spec %s: %w", entry.Name(), err)
		}
		sources = append(sources, source)
	}
	bundle, err := spec.CompileBundle(sources, providerCatalog, spec.HookRegistry{
		"alicloud.ecs.instance": {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback},
		"alicloud.vpc":          {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback},
		"alicloud.prometheus":   {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback},
		"alicloud.vpc.gateway_endpoint": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.ack.cluster": {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback, spec.HookLifecycle},
		"alicloud.alikafka.instance": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.bpstudio.application": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.cloud_firewall.instance": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.ddos_bgp.instance": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.ens.instance": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
		"alicloud.ssl_certificate": {
			spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback,
		},
	})
	if err != nil {
		return spec.Bundle{}, fmt.Errorf("compile embedded Alibaba Cloud specs: %w", err)
	}
	return bundle, nil
}

func inventoryRegion(request contracts.InventoryRequest) (string, error) {
	switch request.Scope.Kind {
	case asset.ScopeRegion:
		if region := strings.TrimSpace(request.Scope.NativeID); region != "" {
			return region, nil
		}
	case asset.ScopeGlobal:
		if region := strings.TrimSpace(request.Scope.Location); region != "" {
			return region, nil
		}
	}
	return "", fmt.Errorf("Alibaba Cloud Resource Center inventory requires a region scope or a global scope with an endpoint region")
}

func productAPIRegion(request contracts.InventoryRequest) (string, error) {
	switch request.Scope.Kind {
	case asset.ScopeRegion:
		if region := strings.TrimSpace(request.Scope.NativeID); region != "" {
			return region, nil
		}
	case asset.ScopeGlobal:
		if region := strings.TrimSpace(request.Scope.Location); region != "" {
			return region, nil
		}
	}
	return "", fmt.Errorf(
		"Alibaba Cloud product API inventory requires a region scope or a global scope with an endpoint region",
	)
}

func invocationRegion(invocation contracts.Invocation) (string, error) {
	for _, key := range []string{"region", "region_id", "location"} {
		if region := strings.TrimSpace(invocation.Scope[key]); region != "" {
			return region, nil
		}
	}
	return "", fmt.Errorf("Alibaba Cloud invocation requires a region scope")
}

func cloudCredential(contract contracts.Credential) (cloudcredentials.Credential, error) {
	values := contract.Values
	credentialType := strings.TrimSpace(string(contract.Type))
	if credentialType == "" {
		credentialType = strings.TrimSpace(values["type"])
	}
	if credentialType == "" {
		credentialType = strings.TrimSpace(values["mode"])
	}
	if credentialType == "" {
		credentialType = "access_key"
	}
	config := &cloudcredentials.Config{Type: tea.String(credentialType)}
	set := func(value string) *string {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return tea.String(strings.TrimSpace(value))
	}
	config.AccessKeyId = set(values["access_key_id"])
	config.AccessKeySecret = set(values["access_key_secret"])
	config.SecurityToken = set(values["security_token"])
	config.RoleArn = set(values["role_arn"])
	config.RoleSessionName = set(values["role_session_name"])
	config.Policy = set(values["policy"])
	config.ExternalId = set(values["external_id"])
	config.RoleName = set(values["role_name"])
	config.PublicKeyId = set(values["public_key_id"])
	config.PrivateKeyFile = set(values["private_key_file"])
	config.OIDCProviderArn = set(values["oidc_provider_arn"])
	config.OIDCTokenFilePath = set(values["oidc_token_file"])
	config.Url = set(values["credentials_uri"])
	if err := validateCredentialFields(credentialType, values); err != nil {
		return nil, err
	}
	credential, err := cloudcredentials.NewCredential(config)
	if err != nil {
		return nil, fmt.Errorf("build Alibaba Cloud SDK credential: %w", err)
	}
	return credential, nil
}

func validateCredentialFields(credentialType string, values map[string]string) error {
	require := func(names ...string) error {
		for _, name := range names {
			if strings.TrimSpace(values[name]) == "" {
				return fmt.Errorf("Alibaba Cloud %s credential requires %s", credentialType, name)
			}
		}
		return nil
	}
	switch credentialType {
	case "access_key":
		return require("access_key_id", "access_key_secret")
	case "sts":
		return require("access_key_id", "access_key_secret", "security_token")
	case "ram_role_arn":
		return require("access_key_id", "access_key_secret", "role_arn")
	case "ecs_ram_role":
		return nil
	case "rsa_key_pair":
		return require("public_key_id", "private_key_file")
	case "oidc_role_arn":
		return require("role_arn", "oidc_provider_arn", "oidc_token_file")
	case "credentials_uri":
		return require("credentials_uri")
	default:
		return fmt.Errorf("unsupported Alibaba Cloud credential type %q", credentialType)
	}
}
