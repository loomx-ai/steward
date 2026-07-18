package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type Registry struct {
	mu         sync.RWMutex
	fragments  map[asset.Provider][]Catalog
	types      map[asset.Provider]map[string]struct{}
	operations map[asset.Provider]map[string]struct{}
}

func NewRegistry() *Registry {
	return &Registry{
		fragments:  make(map[asset.Provider][]Catalog),
		types:      make(map[asset.Provider]map[string]struct{}),
		operations: make(map[asset.Provider]map[string]struct{}),
	}
}

func (r *Registry) Register(c Catalog) error {
	cloned, err := cloneCatalog(c)
	if err != nil {
		return err
	}
	c = cloned
	r.mu.Lock()
	defer r.mu.Unlock()

	if c.Provider == "" {
		return fmt.Errorf("catalog provider is required")
	}
	localTypes := make(map[string]struct{}, len(c.ResourceTypes))
	for _, resourceType := range c.ResourceTypes {
		if resourceType.NativeType == "" {
			return fmt.Errorf("catalog resource native type is required")
		}
		if _, exists := localTypes[resourceType.NativeType]; exists {
			return fmt.Errorf("duplicate native type %q for provider %q", resourceType.NativeType, c.Provider)
		}
		if _, exists := r.types[c.Provider][resourceType.NativeType]; exists {
			return fmt.Errorf("duplicate native type %q for provider %q", resourceType.NativeType, c.Provider)
		}
		localTypes[resourceType.NativeType] = struct{}{}
	}
	localOperations := make(map[string]struct{}, len(c.Operations))
	for _, operation := range c.Operations {
		if operation.Name == "" {
			return fmt.Errorf("catalog operation name is required")
		}
		key := operation.Key()
		if _, exists := localOperations[key]; exists {
			return fmt.Errorf("duplicate operation %q for provider %q", key, c.Provider)
		}
		if _, exists := r.operations[c.Provider][key]; exists {
			return fmt.Errorf("duplicate operation %q for provider %q", key, c.Provider)
		}
		localOperations[key] = struct{}{}
	}
	if r.types[c.Provider] == nil {
		r.types[c.Provider] = make(map[string]struct{})
	}
	if r.operations[c.Provider] == nil {
		r.operations[c.Provider] = make(map[string]struct{})
	}
	for nativeType := range localTypes {
		r.types[c.Provider][nativeType] = struct{}{}
	}
	for operation := range localOperations {
		r.operations[c.Provider][operation] = struct{}{}
	}
	r.fragments[c.Provider] = append(r.fragments[c.Provider], c)
	return nil
}

func (r *Registry) Resolve(provider asset.Provider) (Catalog, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fragments, ok := r.fragments[provider]
	if !ok || len(fragments) == 0 {
		return Catalog{}, fmt.Errorf("catalog for provider %q is not registered", provider)
	}
	merged := Catalog{
		Provider: provider,
		Source: Source{
			Format:    "composite",
			URI:       "catalog://" + string(provider),
			Generator: "steward/catalog-registry",
		},
	}
	provenance := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		merged.Operations = append(merged.Operations, fragment.Operations...)
		merged.ResourceTypes = append(merged.ResourceTypes, fragment.ResourceTypes...)
		provenance = append(provenance, fragment.Source.URI+"\x00"+fragment.Source.Checksum)
	}
	sort.Strings(provenance)
	digest := sha256.Sum256([]byte(strings.Join(provenance, "\n")))
	merged.Source.Checksum = hex.EncodeToString(digest[:])
	normalize(&merged)
	return cloneCatalog(merged)
}

func cloneCatalog(c Catalog) (Catalog, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return Catalog{}, fmt.Errorf("copy catalog: %w", err)
	}
	var clone Catalog
	if err := json.Unmarshal(payload, &clone); err != nil {
		return Catalog{}, fmt.Errorf("copy catalog: %w", err)
	}
	return clone, nil
}
