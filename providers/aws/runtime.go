package aws

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

//go:embed catalog/generated/catalog.json specs/*.yaml
var providerFiles embed.FS

type clientFactory interface {
	CallerIdentity(context.Context, contracts.Credential, string) (string, string, error)
	DiscoverRegions(context.Context, contracts.Credential, string) ([]providerRegion, error)
	ResourceExplorer(context.Context, contracts.Credential, string) (ResourceExplorerClient, error)
	CloudFormation(context.Context, contracts.Credential, string) (CloudFormationClient, error)
	CloudControl(context.Context, contracts.Credential, string) (CloudControlClient, error)
	Network(context.Context, contracts.Credential, string) (NetworkClient, error)
}

const awsRegionBootstrap = "us-east-1"

type providerRegion struct {
	RegionID    string
	Name        string
	Endpoint    string
	OptInStatus string
}

type Runtime struct {
	credentials contracts.CredentialSource
	factory     clientFactory
	catalog     catalog.Catalog
	bundle      spec.Bundle
}

func NewRuntime(credentials contracts.CredentialSource) (*Runtime, error) {
	return newRuntime(credentials, sdkClientFactory{})
}

func newRuntime(credentials contracts.CredentialSource, factory clientFactory) (*Runtime, error) {
	if credentials == nil || factory == nil {
		return nil, fmt.Errorf("AWS credential source and client factory are required")
	}
	providerCatalog, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	bundle, err := compileEmbeddedSpecs(providerCatalog)
	if err != nil {
		return nil, err
	}
	return &Runtime{credentials: credentials, factory: factory, catalog: providerCatalog, bundle: bundle}, nil
}

func (r *Runtime) Provider() asset.Provider { return asset.ProviderAWS }

func (r *Runtime) CredentialSchemas() []contracts.CredentialSchema {
	accessKey := []contracts.CredentialField{
		{Key: "access_key_id", LabelKey: "credentials.accessKeyId", InputType: "text", Secret: true, Required: true},
		{Key: "secret_access_key", LabelKey: "credentials.secretAccessKey", InputType: "password", Secret: true, Required: true},
	}
	return []contracts.CredentialSchema{
		{Type: asset.CredentialAWSAccessKey, LabelKey: "credentials.awsAccessKey", Fields: append([]contracts.CredentialField(nil), accessKey...)},
		{Type: asset.CredentialAWSSession, LabelKey: "credentials.awsSession", Fields: append(accessKey,
			contracts.CredentialField{Key: "session_token", LabelKey: "credentials.sessionToken", InputType: "password", Secret: true, Required: true},
			contracts.CredentialField{Key: "expires_at", LabelKey: "credentials.expiresAt", InputType: "datetime-local", Required: true},
		)},
	}
}

func (r *Runtime) ValidateConnection(ctx context.Context, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	if credential.Type != asset.CredentialAWSAccessKey && credential.Type != asset.CredentialAWSSession {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"credential_type_unsupported",
			"The credential type is not supported by AWS.",
			nil,
		)
	}
	if credential.Type == asset.CredentialAWSSession && credential.ExpiresAt == nil {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"credential_expiration_required",
			"The temporary credential must include an expiration time.",
			nil,
		)
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"credential_expired",
			"The cloud credential has expired.",
			nil,
		)
	}
	if _, err := loadSDKConfig(ctx, credential, awsRegionBootstrap); err != nil {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"credential_fields_invalid",
			"The AWS credential fields are incomplete or invalid.",
			err,
		)
	}
	accountID, arn, err := r.factory.CallerIdentity(ctx, credential, awsRegionBootstrap)
	if err != nil {
		return contracts.ConnectionIdentity{}, NormalizeError(err)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"provider_identity_incomplete",
			"AWS did not return a usable account identity.",
			nil,
		)
	}
	principal := strings.TrimSpace(arn)
	if principal == "" {
		principal = accountID
	}
	return contracts.ConnectionIdentity{
		Partition: "aws", TenantID: accountID, Principal: principal,
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: accountID, Name: accountID}},
	}, nil
}

func (r *Runtime) DiscoverRegions(ctx context.Context, connectionID asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	credential, err := r.resolveCredential(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	regions, err := r.factory.DiscoverRegions(ctx, credential, awsRegionBootstrap)
	if err != nil {
		return nil, NormalizeError(err)
	}
	result := make([]contracts.DiscoveredRegion, 0, len(regions))
	for _, region := range regions {
		regionID := strings.TrimSpace(region.RegionID)
		status := strings.ToLower(strings.TrimSpace(region.OptInStatus))
		if regionID == "" || status == "not-opted-in" {
			continue
		}
		metadata := map[string]string{}
		if endpoint := strings.TrimSpace(region.Endpoint); endpoint != "" {
			metadata["endpoint"] = endpoint
		}
		if status != "" {
			metadata["opt_in_status"] = status
		}
		result = append(result, contracts.DiscoveredRegion{RegionID: regionID, Name: strings.TrimSpace(region.Name), Metadata: metadata})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}

func (r *Runtime) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{
		{Name: "resource-explorer", RootScopeKinds: []asset.ScopeKind{asset.ScopeAccount, asset.ScopeOrganization, asset.ScopeRegion, asset.ScopeGlobal}, AuthoritativeDefault: false},
		{Name: cloudControlSource, RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal}, AuthoritativeDefault: true, KindSpecific: true},
	}
}

func (r *Runtime) Bundle() spec.Bundle {
	payload, err := json.Marshal(r.bundle)
	if err != nil {
		return spec.Bundle{}
	}
	var cloned spec.Bundle
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return spec.Bundle{}
	}
	return cloned
}

func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if request.Source == cloudControlSource {
		client, err := r.factory.CloudControl(ctx, credential, cloudControlRegion(request.Scope))
		if err != nil {
			return contracts.InventoryBatch{}, NormalizeError(err)
		}
		return NewCloudControlInventory(client).List(ctx, request)
	}
	if request.Source != "" && request.Source != "resource-explorer" {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS inventory source %q is not supported", request.Source)
	}
	client, err := r.factory.ResourceExplorer(ctx, credential, request.Scope.Location)
	if err != nil {
		return contracts.InventoryBatch{}, NormalizeError(err)
	}
	batch, err := NewInventory(client, r.resourceKind).List(ctx, request)
	if err != nil || request.Source == "" || request.ResourceKind != nil {
		return batch, err
	}
	batch.Items = r.withoutCloudControlItems(batch.Items)
	return batch, nil
}

func (r *Runtime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	return contracts.InvocationResult{}, fmt.Errorf("AWS runtime exposes typed Provider clients; no generic operation invocation is available")
}

func (r *Runtime) ResolveAction(ctx context.Context, connectionID asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	if value.Identity.Provider != asset.ProviderAWS {
		return nil, fmt.Errorf("AWS native type %q has no action driver", value.Identity.NativeType)
	}
	if value.Identity.NativeType == CloudFormationStackNativeType {
		client, err := r.CloudFormation(ctx, connectionID, value.Location)
		if err != nil {
			return nil, err
		}
		return NewCloudFormationAction(client), nil
	}
	if !r.cloudControlKind(value.Identity.NativeType) {
		return nil, fmt.Errorf("AWS native type %q has no action driver", value.Identity.NativeType)
	}
	client, err := r.CloudControl(ctx, connectionID, value.Location)
	if err != nil {
		return nil, err
	}
	return NewCloudControlAction(client), nil
}

func (r *Runtime) CloudFormation(ctx context.Context, connectionID asset.ConnectionID, region string) (CloudFormationClient, error) {
	if strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("AWS CloudFormation region is required")
	}
	credential, err := r.resolveCredential(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	client, err := r.factory.CloudFormation(ctx, credential, strings.TrimSpace(region))
	if err != nil {
		return nil, NormalizeError(err)
	}
	return client, nil
}

func (r *Runtime) CloudControl(ctx context.Context, connectionID asset.ConnectionID, region string) (CloudControlClient, error) {
	region = strings.TrimSpace(region)
	if region == "" || strings.EqualFold(region, "global") {
		region = awsRegionBootstrap
	}
	credential, err := r.resolveCredential(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	client, err := r.factory.CloudControl(ctx, credential, region)
	if err != nil {
		return nil, NormalizeError(err)
	}
	return client, nil
}

func cloudControlRegion(scope asset.Scope) string {
	if region := strings.TrimSpace(scope.Location); region != "" && !strings.EqualFold(region, "global") {
		return region
	}
	if scope.Kind == asset.ScopeRegion {
		if region := strings.TrimSpace(scope.NativeID); region != "" {
			return region
		}
	}
	return awsRegionBootstrap
}

func (r *Runtime) cloudControlKind(nativeType string) bool {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType && compiled.Definition.Extensions.Hook == cloudControlHook {
			return true
		}
	}
	return false
}

func (r *Runtime) withoutCloudControlItems(items []contracts.InventoryItem) []contracts.InventoryItem {
	result := make([]contracts.InventoryItem, 0, len(items))
	for _, item := range items {
		if !r.cloudControlKind(item.NativeType) {
			result = append(result, item)
		}
	}
	return result
}

func (r *Runtime) resourceKind(nativeType string, scopeKind asset.ScopeKind) asset.ResourceKind {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled.ResourceKind
		}
	}
	if kind, err := r.catalog.ResourceKind(nativeType, r.catalog.Source.Checksum, nil); err == nil {
		return kind
	}
	return asset.ResourceKind{
		ID: asset.ResourceKindID(string(asset.ProviderAWS) + ":" + nativeType), Provider: asset.ProviderAWS,
		NativeType: nativeType, DisplayName: nativeType, ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal, asset.ScopeRegion},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: r.catalog.Source.Checksum,
	}
}

func (r *Runtime) resolveCredential(ctx context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	if connectionID == "" {
		return contracts.Credential{}, fmt.Errorf("AWS connection ID is required")
	}
	credential, err := r.credentials.Resolve(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("resolve AWS connection credential: %w", err)
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		return contracts.Credential{}, fmt.Errorf("AWS credential has expired")
	}
	if len(credential.Values) == 0 {
		return contracts.Credential{}, fmt.Errorf("AWS credential is empty")
	}
	return credential, nil
}

func loadCatalog() (catalog.Catalog, error) {
	payload, err := providerFiles.ReadFile("catalog/generated/catalog.json")
	if err != nil {
		return catalog.Catalog{}, err
	}
	value, err := catalog.UnmarshalGenerated(payload)
	if err != nil {
		return catalog.Catalog{}, fmt.Errorf("load embedded AWS catalog: %w", err)
	}
	return value, nil
}

func compileEmbeddedSpecs(providerCatalog catalog.Catalog) (spec.Bundle, error) {
	entries, err := fs.ReadDir(providerFiles, "specs")
	if err != nil {
		return spec.Bundle{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	sources := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		payload, err := providerFiles.ReadFile("specs/" + entry.Name())
		if err != nil {
			return spec.Bundle{}, err
		}
		sources = append(sources, payload)
	}
	return spec.CompileBundle(sources, providerCatalog, spec.HookRegistry{
		"aws.cloudformation.stack": {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback, spec.HookLifecycle},
		cloudControlHook:           {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback},
	})
}
