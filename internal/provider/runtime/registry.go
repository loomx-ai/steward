package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type Registry struct {
	mu            sync.RWMutex
	providers     map[asset.Provider]contracts.Provider
	bundles       map[asset.Provider]spec.Bundle
	sortedBundles []spec.Bundle
	resourceKinds map[asset.Provider]resourceKindSnapshot
}

// resourceKindSnapshot is a provider's resource kind metadata captured once
// at registration; provider metadata is static after construction.
type resourceKindSnapshot struct {
	kinds    []asset.ResourceKind
	revision string
}

func NewRegistry() *Registry {
	return &Registry{
		providers:     make(map[asset.Provider]contracts.Provider),
		bundles:       make(map[asset.Provider]spec.Bundle),
		resourceKinds: make(map[asset.Provider]resourceKindSnapshot),
	}
}

func (r *Registry) Register(provider contracts.Provider) error {
	if provider == nil {
		return fmt.Errorf("provider runtime is required")
	}
	name := provider.Provider()
	if name == "" {
		return fmt.Errorf("provider runtime name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider runtime %q is already registered", name)
	}
	if metadata, ok := provider.(contracts.ResourceKindMetadata); ok {
		kinds, revision := metadata.ResourceKinds()
		var snapshot []asset.ResourceKind
		if payload, err := json.Marshal(kinds); err == nil && json.Unmarshal(payload, &snapshot) == nil {
			r.resourceKinds[name] = resourceKindSnapshot{kinds: snapshot, revision: revision}
		}
	}
	r.providers[name] = provider
	return nil
}

func (r *Registry) Resolve(provider asset.Provider) (contracts.Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resolved, ok := r.providers[provider]
	if !ok {
		return nil, fmt.Errorf("provider runtime %q is not registered", provider)
	}
	return resolved, nil
}

func (r *Registry) ResolveAction(ctx context.Context, connectionID asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	provider, err := r.Resolve(value.Identity.Provider)
	if err != nil {
		return nil, err
	}
	actions, ok := provider.(contracts.ActionProvider)
	if !ok {
		return nil, fmt.Errorf("provider runtime %q does not expose action drivers", value.Identity.Provider)
	}
	return actions.ResolveAction(ctx, connectionID, value)
}

func (r *Registry) ResolveInventory(providerName asset.Provider) (contracts.InventoryAdapter, error) {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return nil, err
	}
	inventory, ok := provider.(contracts.InventoryAdapter)
	if !ok {
		return nil, fmt.Errorf("provider runtime %q does not expose inventory", providerName)
	}
	return inventory, nil
}

func (r *Registry) ResolveInventorySource(
	providerName asset.Provider,
	kind asset.ResourceKind,
	declared string,
) (string, error) {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return "", err
	}
	selector, ok := provider.(contracts.InventorySourceSelector)
	if !ok {
		return declared, nil
	}
	selected := selector.InventorySourceForResourceKind(kind, declared)
	if selected == "" {
		return "", fmt.Errorf("provider runtime %q selected an empty inventory source for %q", providerName, kind.ID)
	}
	return selected, nil
}

func (r *Registry) ResolveRegionDiscoverer(providerName asset.Provider) (contracts.RegionDiscoverer, error) {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return nil, err
	}
	discoverer, ok := provider.(contracts.RegionDiscoverer)
	if !ok {
		return nil, fmt.Errorf("provider runtime %q does not expose region discovery", providerName)
	}
	return discoverer, nil
}

func (r *Registry) ResolveNetworkTargetDiscoverer(providerName asset.Provider) (contracts.NetworkTargetDiscoverer, error) {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return nil, err
	}
	discoverer, ok := provider.(contracts.NetworkTargetDiscoverer)
	if !ok {
		return nil, fmt.Errorf("provider runtime %q does not expose network target discovery", providerName)
	}
	return discoverer, nil
}

func (r *Registry) ValidateConnection(ctx context.Context, providerName asset.Provider, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"provider_validation_unsupported",
			"The selected cloud provider does not support credential validation.",
			err,
		)
	}
	bootstrapper, ok := provider.(contracts.ConnectionBootstrapper)
	if !ok {
		return contracts.ConnectionIdentity{}, contracts.NewCredentialValidationError(
			"provider_validation_unsupported",
			"The selected cloud provider does not support credential validation.",
			nil,
		)
	}
	return bootstrapper.ValidateConnection(ctx, credential)
}

func (r *Registry) ValidateConnectionSite(providerName asset.Provider, site asset.ConnectionSite) error {
	provider, err := r.Resolve(providerName)
	if err != nil {
		return err
	}
	metadata, ok := provider.(contracts.ConnectionSiteMetadata)
	if !ok {
		if site == "" {
			return nil
		}
		return fmt.Errorf("provider runtime %q does not accept connection site %q", providerName, site)
	}
	sites := metadata.ConnectionSites()
	if len(sites) == 0 {
		if site == "" {
			return nil
		}
		return fmt.Errorf("provider runtime %q does not accept connection site %q", providerName, site)
	}
	for _, candidate := range sites {
		if candidate.Value == site {
			return nil
		}
	}
	return fmt.Errorf("provider runtime %q does not accept connection site %q", providerName, site)
}

// RegisterBundle accepts only compiler output: the non-empty revision and hash
// are the boundary that prevents runtime YAML interpretation.
func (r *Registry) RegisterBundle(bundle spec.Bundle) error {
	if bundle.Provider == "" || bundle.Hash == "" || bundle.Revision == "" {
		return fmt.Errorf("compiled bundle provider, hash, and revision are required")
	}
	copy, err := cloneBundle(bundle)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.bundles[bundle.Provider]; exists {
		return fmt.Errorf("compiled bundle for provider %q is already registered", bundle.Provider)
	}
	r.bundles[bundle.Provider] = copy
	r.sortedBundles = append(r.sortedBundles, copy)
	sort.Slice(r.sortedBundles, func(i, j int) bool {
		return r.sortedBundles[i].Provider < r.sortedBundles[j].Provider
	})
	return nil
}

func (r *Registry) Bundle(provider asset.Provider) (spec.Bundle, error) {
	r.mu.RLock()
	bundle, ok := r.bundles[provider]
	r.mu.RUnlock()
	if !ok {
		return spec.Bundle{}, fmt.Errorf("compiled bundle for provider %q is not registered", provider)
	}
	return cloneBundle(bundle)
}

// Bundles returns every registered bundle sorted by provider. The bundles
// share the registry's registration-time copy: callers must treat them as
// read-only. Use Bundle for a copy that may be mutated.
func (r *Registry) Bundles() []spec.Bundle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]spec.Bundle(nil), r.sortedBundles...)
}

func (r *Registry) ResourceKinds(provider asset.Provider) ([]asset.ResourceKind, string, bool) {
	r.mu.RLock()
	snapshot, ok := r.resourceKinds[provider]
	r.mu.RUnlock()
	if !ok {
		return nil, "", false
	}
	return cloneResourceKinds(snapshot.kinds), snapshot.revision, true
}

func (r *Registry) ProviderDescriptors() []contracts.ProviderDescriptor {
	r.mu.RLock()
	providers := make([]asset.Provider, 0, len(r.providers))
	for provider := range r.providers {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	result := make([]contracts.ProviderDescriptor, 0, len(providers))
	for _, provider := range providers {
		descriptor := contracts.ProviderDescriptor{Provider: provider, Sites: []contracts.ProviderSite{}}
		if metadata, ok := r.providers[provider].(contracts.ConnectionSiteMetadata); ok {
			descriptor.Sites = append([]contracts.ProviderSite(nil), metadata.ConnectionSites()...)
		}
		if metadata, ok := r.providers[provider].(contracts.InventoryMetadata); ok {
			descriptor.InventorySources = metadata.InventorySources()
		}
		if metadata, ok := r.providers[provider].(contracts.CredentialMetadata); ok {
			descriptor.CredentialSchemas = metadata.CredentialSchemas()
		}
		result = append(result, descriptor)
	}
	r.mu.RUnlock()
	return result
}

func cloneBundle(bundle spec.Bundle) (spec.Bundle, error) {
	payload, err := json.Marshal(bundle)
	if err != nil {
		return spec.Bundle{}, fmt.Errorf("copy compiled bundle: %w", err)
	}
	var clone spec.Bundle
	if err := json.Unmarshal(payload, &clone); err != nil {
		return spec.Bundle{}, fmt.Errorf("copy compiled bundle: %w", err)
	}
	return clone, nil
}

func cloneResourceKinds(values []asset.ResourceKind) []asset.ResourceKind {
	if values == nil {
		return nil
	}
	cloned := make([]asset.ResourceKind, len(values))
	for index, value := range values {
		value.ScopeKinds = slices.Clone(value.ScopeKinds)
		value.Capabilities = slices.Clone(value.Capabilities)
		value.DisplayNames = maps.Clone(value.DisplayNames)
		value.FieldDisplayNames = cloneLocalizedFields(value.FieldDisplayNames)
		value.SummaryFields = slices.Clone(value.SummaryFields)
		if value.Properties != nil {
			properties := make([]asset.ResourceProperty, len(value.Properties))
			for position, property := range value.Properties {
				property.DisplayNames = maps.Clone(property.DisplayNames)
				property.Enum = slices.Clone(property.Enum)
				property.Operators = slices.Clone(property.Operators)
				properties[position] = property
			}
			value.Properties = properties
		}
		cloned[index] = value
	}
	return cloned
}

func cloneLocalizedFields(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]map[string]string, len(values))
	for field, labels := range values {
		cloned[field] = make(map[string]string, len(labels))
		for locale, label := range labels {
			cloned[field][locale] = label
		}
	}
	return cloned
}
