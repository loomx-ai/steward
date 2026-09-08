package catalog

import (
	"fmt"
	"sort"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type Source struct {
	Format    string `json:"format"`
	URI       string `json:"uri"`
	Checksum  string `json:"checksum"`
	Generator string `json:"generator"`
}

type Pagination struct {
	InputTokenPath  string `json:"input_token_path,omitempty"`
	OutputTokenPath string `json:"output_token_path,omitempty"`
	ItemsPath       string `json:"items_path,omitempty"`
}

// OperationCall describes the stable transport contract needed to invoke one
// cloud product API operation. Resource specs reference operations by ID and
// never need to know SDK-specific request types.
type OperationCall struct {
	Product              string                          `json:"product"`
	Version              string                          `json:"version"`
	Style                string                          `json:"style"`
	Protocol             string                          `json:"protocol,omitempty"`
	Method               string                          `json:"method"`
	Path                 string                          `json:"path"`
	Endpoint             string                          `json:"endpoint"`
	EndpointOverrides    map[string]string               `json:"endpoint_overrides,omitempty"`
	SiteEndpoints        map[asset.ConnectionSite]string `json:"site_endpoints,omitempty"`
	EndpointParameters   []string                        `json:"endpoint_parameters,omitempty"`
	HostParameters       []string                        `json:"host_parameters,omitempty"`
	HeaderParameters     []string                        `json:"header_parameters,omitempty"`
	RawPathParameters    []string                        `json:"raw_path_parameters,omitempty"`
	RequestBodyType      string                          `json:"request_body_type,omitempty"`
	BodyType             string                          `json:"body_type,omitempty"`
	ParameterPosition    string                          `json:"parameter_position,omitempty"`
	IdempotencyParameter string                          `json:"idempotency_parameter,omitempty"`
}

type Operation struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Service      string         `json:"service,omitempty"`
	Method       string         `json:"method,omitempty"`
	Path         string         `json:"path,omitempty"`
	Destructive  bool           `json:"destructive"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Pagination   *Pagination    `json:"pagination,omitempty"`
	Call         *OperationCall `json:"call,omitempty"`
	SourceURI    string         `json:"source_uri,omitempty"`
}

type ResourceType struct {
	NativeType  string            `json:"native_type"`
	Class       string            `json:"class,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	ScopeKinds  []asset.ScopeKind `json:"scope_kinds"`
	REST        *RESTResource     `json:"rest,omitempty"`
}

// RESTResource enumerates the official methods for a resource family. Some
// Google asset types share several regional/global API methods; recording those
// bindings in the catalog makes routing changes part of the bundle revision.
type RESTResource struct {
	Collection       string   `json:"collection,omitempty"`
	ReadOperations   []string `json:"read_operations"`
	DeleteOperations []string `json:"delete_operations,omitempty"`
}

type Catalog struct {
	Provider        asset.Provider `json:"provider"`
	Source          Source         `json:"source"`
	ContentChecksum string         `json:"content_checksum,omitempty"`
	Operations      []Operation    `json:"operations"`
	ResourceTypes   []ResourceType `json:"resource_types"`
}

func (c Catalog) Operation(name string) (Operation, bool) {
	for _, operation := range c.Operations {
		if operation.Key() == name {
			return operation, true
		}
	}
	var matched Operation
	matches := 0
	for _, operation := range c.Operations {
		if operation.Name == name {
			matched = operation
			matches++
		}
	}
	return matched, matches == 1
}

func (o Operation) Key() string {
	if o.ID != "" {
		return o.ID
	}
	if o.Service != "" {
		return o.Service + "." + o.Name
	}
	return o.Name
}

func (c Catalog) ResourceType(nativeType string) (ResourceType, bool) {
	for _, resourceType := range c.ResourceTypes {
		if resourceType.NativeType == nativeType {
			return resourceType, true
		}
	}
	return ResourceType{}, false
}

func (c Catalog) ResourceKind(nativeType, revision string, capabilities asset.CapabilitySet) (asset.ResourceKind, error) {
	resourceType, ok := c.ResourceType(nativeType)
	if !ok {
		return asset.ResourceKind{}, fmt.Errorf("catalog resource type %q not found", nativeType)
	}
	all := append(asset.CapabilitySet{asset.CapabilityIndexed}, capabilities...)
	all = normalizedCapabilities(all)
	return asset.ResourceKind{
		ID:             asset.ResourceKindID(string(c.Provider) + ":" + nativeType),
		Provider:       c.Provider,
		NativeType:     nativeType,
		Class:          resourceType.Class,
		ScopeKinds:     append([]asset.ScopeKind(nil), resourceType.ScopeKinds...),
		Capabilities:   all,
		DisplayName:    resourceType.DisplayName,
		BundleRevision: revision,
	}, nil
}

func normalizedCapabilities(capabilities asset.CapabilitySet) asset.CapabilitySet {
	seen := make(map[asset.Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		seen[capability] = struct{}{}
	}
	result := make(asset.CapabilitySet, 0, len(seen))
	for capability := range seen {
		result = append(result, capability)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
