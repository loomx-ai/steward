package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type Runtime struct {
	credentials contracts.CredentialSource
	transport   http.RoundTripper
	bundle      spec.Bundle
	mu          sync.Mutex
	clients     map[asset.ConnectionID]*client
}

func NewRuntime(credentials contracts.CredentialSource) (*Runtime, error) {
	if credentials == nil {
		return nil, fmt.Errorf("Azure credential source is required")
	}
	bundle, err := compileBundle()
	if err != nil {
		return nil, err
	}
	return &Runtime{credentials: credentials, transport: http.DefaultTransport, bundle: bundle, clients: map[asset.ConnectionID]*client{}}, nil
}
func (r *Runtime) Provider() asset.Provider { return asset.ProviderAzure }
func (r *Runtime) Bundle() spec.Bundle {
	payload, _ := json.Marshal(r.bundle)
	var cloned spec.Bundle
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}
func (r *Runtime) CredentialSchemas() []contracts.CredentialSchema {
	return []contracts.CredentialSchema{{Type: asset.CredentialAzureServicePrincipal, LabelKey: "credentials.azureServicePrincipal", Fields: []contracts.CredentialField{
		{Key: "subscription_id", LabelKey: "credentials.subscriptionId", InputType: "text", Required: true},
		{Key: "tenant_id", LabelKey: "credentials.tenantId", InputType: "text", Required: true},
		{Key: "client_id", LabelKey: "credentials.clientId", InputType: "text", Required: true},
		{Key: "client_secret", LabelKey: "credentials.clientSecret", InputType: "password", Required: true, Secret: true},
	}}}
}
func (r *Runtime) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{{Name: inventorySource, RootScopeKinds: []asset.ScopeKind{asset.ScopeSubscription, asset.ScopeRegion, asset.ScopeGlobal}, AuthoritativeDefault: true, NetworkClosure: true}}
}
func (r *Runtime) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	return contracts.InvocationResult{}, fmt.Errorf("Azure uses provider-owned REST operations")
}
func (c *client) subscriptionIdentity(ctx context.Context) (map[string]any, error) {
	res, err := c.request(ctx, "GET", apiURL(c.root(), "2022-12-01"))
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(text(res.data["subscriptionId"]), c.subscription) || !strings.EqualFold(text(res.data["state"]), "Enabled") ||
		!strings.EqualFold(text(res.data["tenantId"]), c.tenant) {
		return nil, fmt.Errorf("Azure subscription is not enabled for the supplied tenant")
	}
	return res.data, nil
}
func (r *Runtime) ValidateConnection(ctx context.Context, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	c, err := newClient(credential, r.transport)
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	data, err := c.subscriptionIdentity(ctx)
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	path := c.root() + "/resources"
	if _, _, err := c.listPage(ctx, apiURL(path, resourcesVersion), path); err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	if _, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Authorization/locks", locksVersion); err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	return contracts.ConnectionIdentity{Partition: "azure", TenantID: c.tenant, Principal: c.application,
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeSubscription, NativeID: c.subscription, Name: text(data["displayName"])}}}, nil
}
func (r *Runtime) resolve(ctx context.Context, id asset.ConnectionID) (*client, error) {
	if id == "" {
		return nil, fmt.Errorf("Azure connection ID is required")
	}
	credential, err := r.credentials.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	candidate, err := newClient(credential, r.transport)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	existing := r.clients[id]
	r.mu.Unlock()
	if existing != nil && existing.fingerprint == candidate.fingerprint {
		return existing, nil
	}
	if _, err := candidate.subscriptionIdentity(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.clients[id] = candidate
	r.mu.Unlock()
	return candidate, nil
}
func (r *Runtime) DiscoverRegions(ctx context.Context, id asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	items, err := c.listAll(ctx, c.root()+"/locations", "2022-12-01")
	if err != nil {
		return nil, err
	}
	result := []contracts.DiscoveredRegion{}
	seen := map[string]bool{}
	for _, value := range items {
		data := object(value)
		name := strings.ToLower(text(data["name"]))
		if name == "" || name == "global" || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, contracts.DiscoveredRegion{RegionID: name, Name: text(data["displayName"])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}
func (r *Runtime) resourceKind(nativeType string) asset.ResourceKind {
	for _, compiled := range r.bundle.Specs {
		if strings.EqualFold(compiled.ResourceKind.NativeType, nativeType) {
			return compiled.ResourceKind
		}
	}
	return asset.ResourceKind{ID: asset.ResourceKindID("azure:" + strings.ToLower(nativeType)), Provider: asset.ProviderAzure, NativeType: strings.ToLower(nativeType), DisplayName: last(nativeType), ScopeKinds: both, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: r.bundle.Revision}
}
