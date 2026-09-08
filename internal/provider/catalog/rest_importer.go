package catalog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// RESTDocumentSet stores versioned upstream documents together with Steward's
// resource classification. SourceSHA256 identifies the original upstream file;
// the catalog checksum covers the complete checked-in input, including mapping
// changes. Documents may contain selected, unchanged upstream API fragments.
type RESTDocumentSet struct {
	Documents     []RESTSourceDocument  `json:"documents"`
	ResourceTypes []openAPIResourceType `json:"x-resource-types"`
}

type RESTSourceDocument struct {
	SourceURI    string          `json:"source_uri"`
	SourceSHA256 string          `json:"source_sha256"`
	SourceFormat string          `json:"source_format,omitempty"`
	Dependency   bool            `json:"dependency,omitempty"`
	Document     json.RawMessage `json:"document"`
}

type discoveryMethod struct {
	ID         string                    `json:"id"`
	HTTPMethod string                    `json:"httpMethod"`
	Path       string                    `json:"path"`
	Parameters map[string]map[string]any `json:"parameters"`
	Request    map[string]any            `json:"request"`
	Response   map[string]any            `json:"response"`
}

type discoveryResource struct {
	Methods   map[string]discoveryMethod   `json:"methods"`
	Resources map[string]discoveryResource `json:"resources"`
}

type discoveryDocument struct {
	Name        string                    `json:"name"`
	Version     string                    `json:"version"`
	RootURL     string                    `json:"rootUrl"`
	ServicePath string                    `json:"servicePath"`
	Schemas     map[string]map[string]any `json:"schemas"`
	discoveryResource
}

// GoogleDiscoveryImporter imports native Discovery method IDs and transport
// metadata. No synthetic GetResource/DeleteResource operations are invented.
type GoogleDiscoveryImporter struct{}

func (GoogleDiscoveryImporter) Import(provider asset.Provider, sourceURI string, source []byte) (Catalog, error) {
	if provider != asset.ProviderGCP {
		return Catalog{}, fmt.Errorf("Google Discovery requires the gcp provider")
	}
	set, err := decodeRESTDocumentSet(source)
	if err != nil {
		return Catalog{}, err
	}
	c := newCatalog(provider, "google-discovery", sourceURI, source)
	for _, upstream := range set.Documents {
		var document discoveryDocument
		if err := json.Unmarshal(upstream.Document, &document); err != nil {
			return Catalog{}, fmt.Errorf("decode Google Discovery document: %w", err)
		}
		endpoint, err := trustedRESTOrigin(document.RootURL, "googleapis.com")
		if err != nil || document.Name == "" || document.Version == "" {
			return Catalog{}, fmt.Errorf("Google Discovery document has invalid API identity or endpoint")
		}
		var walk func(discoveryResource) error
		walk = func(resource discoveryResource) error {
			for _, method := range resource.Methods {
				if method.ID == "" || !isHTTPMethod(method.HTTPMethod) || method.Path == "" {
					return fmt.Errorf("Google Discovery method has no valid ID, HTTP method, or path")
				}
				path := "/" + strings.TrimLeft(document.ServicePath+method.Path, "/")
				rawParameters := []string{}
				for _, parameter := range restPathParameter.FindAllStringSubmatch(path, -1) {
					if parameter[1] == "+" {
						rawParameters = append(rawParameters, parameter[2])
					}
				}
				path = strings.ReplaceAll(path, "{+", "{")
				properties := map[string]any{}
				for name, value := range method.Parameters {
					properties[name] = value
				}
				if len(method.Request) != 0 {
					properties["body"] = discoverySchema(method.Request, document.Schemas)
				}
				call := &OperationCall{Product: document.Name, Version: document.Version, Style: "google-rest", Protocol: "HTTPS", Method: strings.ToUpper(method.HTTPMethod), Path: path, Endpoint: endpoint, RawPathParameters: rawParameters, ParameterPosition: "query", BodyType: "json"}
				if _, ok := method.Parameters["requestId"]; ok {
					call.IdempotencyParameter = "requestId"
				}
				output := discoverySchema(method.Response, document.Schemas)
				c.Operations = append(c.Operations, Operation{ID: method.ID, Name: lastRESTSegment(method.ID, "."), Service: document.Name, Method: call.Method, Path: path, Destructive: isDestructiveOperation(lastRESTSegment(method.ID, "."), call.Method), InputSchema: map[string]any{"type": "object", "properties": properties}, OutputSchema: output, Pagination: discoveryPagination(method, output), Call: call, SourceURI: upstream.SourceURI, SourceFormat: upstream.SourceFormat})
			}
			for _, child := range resource.Resources {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(document.discoveryResource); err != nil {
			return Catalog{}, err
		}
	}
	appendResourceTypes(&c, set.ResourceTypes)
	return finishRESTCatalog(c)
}

func discoverySchema(reference map[string]any, schemas map[string]map[string]any) map[string]any {
	if name, ok := reference["$ref"].(string); ok {
		if value, ok := schemas[name]; ok {
			return value
		}
	}
	return reference
}

func discoveryPagination(method discoveryMethod, output map[string]any) *Pagination {
	if _, ok := method.Parameters["pageToken"]; !ok {
		return nil
	}
	properties, _ := output["properties"].(map[string]any)
	if _, ok := properties["nextPageToken"]; !ok {
		return nil
	}
	items := []string{}
	for key, value := range properties {
		property, _ := value.(map[string]any)
		if property["type"] == "array" || (key == "items" && property["type"] == "object") {
			items = append(items, key)
		}
	}
	sort.Strings(items)
	if len(items) != 1 {
		return nil // Ambiguous response collections require an explicit spec.
	}
	return &Pagination{InputTokenPath: "pageToken", OutputTokenPath: "nextPageToken", ItemsPath: items[0]}
}

type azureSwagger struct {
	Swagger  string `json:"swagger"`
	Host     string `json:"host"`
	BasePath string `json:"basePath"`
	Info     struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Paths       map[string]json.RawMessage `json:"paths"`
	ExtraPaths  map[string]json.RawMessage `json:"x-ms-paths"`
	Parameters  map[string]map[string]any  `json:"parameters"`
	Definitions map[string]map[string]any  `json:"definitions"`
}

type azureSwaggerOperation struct {
	ID         string           `json:"operationId"`
	Parameters []map[string]any `json:"parameters"`
	Responses  map[string]struct {
		Schema map[string]any `json:"schema"`
	} `json:"responses"`
	Pageable *struct {
		NextLinkName string `json:"nextLinkName"`
		ItemName     string `json:"itemName"`
	} `json:"x-ms-pageable"`
}

type AzureOpenAPIImporter struct{}

func (AzureOpenAPIImporter) Import(provider asset.Provider, sourceURI string, source []byte) (Catalog, error) {
	if provider != asset.ProviderAzure {
		return Catalog{}, fmt.Errorf("Azure OpenAPI requires the azure provider")
	}
	set, err := decodeRESTDocumentSet(source)
	if err != nil {
		return Catalog{}, err
	}
	c := newCatalog(provider, "azure-openapi", sourceURI, source)
	resolver, err := newAzureReferenceResolver(set)
	if err != nil {
		return Catalog{}, err
	}
	for _, upstream := range set.Documents {
		if upstream.Dependency {
			continue
		}
		var document azureSwagger
		if err := json.Unmarshal(upstream.Document, &document); err != nil {
			return Catalog{}, fmt.Errorf("decode Azure OpenAPI document: %w", err)
		}
		if document.Swagger != "2.0" || document.Info.Version == "" || document.Host != "management.azure.com" {
			return Catalog{}, fmt.Errorf("Azure OpenAPI document must describe a versioned ARM API")
		}
		paths := map[string]json.RawMessage{}
		for key, value := range document.Paths {
			paths[key] = value
		}
		for key, value := range document.ExtraPaths {
			if _, exists := paths[key]; exists {
				return Catalog{}, fmt.Errorf("duplicate Azure API path %q", key)
			}
			paths[key] = value
		}
		for path, rawItem := range paths {
			var item map[string]json.RawMessage
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return Catalog{}, err
			}
			var common []map[string]any
			if raw := item["parameters"]; raw != nil {
				if err := json.Unmarshal(raw, &common); err != nil {
					return Catalog{}, err
				}
			}
			for method, rawOperation := range item {
				if !isHTTPMethod(method) {
					continue
				}
				var operation azureSwaggerOperation
				if err := json.Unmarshal(rawOperation, &operation); err != nil {
					return Catalog{}, err
				}
				if operation.ID == "" {
					return Catalog{}, fmt.Errorf("Azure API method has no operationId")
				}
				service := azureService(path, document.Info.Title)
				properties := map[string]any{}
				rawParameters := []string{}
				for _, parameter := range append(common, operation.Parameters...) {
					parameter, err = resolver.resolve(parameter, upstream.SourceURI)
					if err != nil {
						return Catalog{}, err
					}
					if name, ok := parameter["name"].(string); ok {
						properties[name] = parameter
						if parameter["in"] == "path" && parameter["x-ms-skip-url-encoding"] == true {
							rawParameters = append(rawParameters, name)
						}
					} else {
						return Catalog{}, fmt.Errorf("Azure operation %q has an unnamed parameter", operation.ID)
					}
				}
				for _, parameter := range restPathParameter.FindAllStringSubmatch(path, -1) {
					if _, exists := properties[parameter[2]]; !exists {
						properties[parameter[2]] = map[string]any{"type": "string", "in": "path", "required": true}
					}
				}
				properties["api-version"] = map[string]any{"type": "string", "in": "query", "enum": []string{document.Info.Version}}
				var output map[string]any
				for _, status := range []string{"200", "201", "202", "204"} {
					if response, exists := operation.Responses[status]; exists {
						output = response.Schema
						output, err = resolver.resolve(output, upstream.SourceURI)
						if err != nil {
							return Catalog{}, err
						}
						break
					}
				}
				var pagination *Pagination
				if operation.Pageable != nil && operation.Pageable.NextLinkName != "" {
					items := operation.Pageable.ItemName
					if items == "" {
						items = "value"
					}
					pagination = &Pagination{OutputTokenPath: operation.Pageable.NextLinkName, ItemsPath: items}
				}
				fullPath := strings.TrimRight(document.BasePath, "/") + path
				call := &OperationCall{Product: service, Version: document.Info.Version, Style: "azure-rest", Protocol: "HTTPS", Method: strings.ToUpper(method), Path: fullPath, Endpoint: "https://management.azure.com", ParameterPosition: "query", BodyType: "json"}
				call.RawPathParameters = rawParameters
				c.Operations = append(c.Operations, Operation{ID: "Azure." + service + "." + operation.ID, Name: operation.ID, Service: service, Method: call.Method, Path: fullPath, Destructive: isDestructiveOperation(operation.ID, method), InputSchema: map[string]any{"type": "object", "properties": properties}, OutputSchema: output, Pagination: pagination, Call: call, SourceURI: upstream.SourceURI})
			}
		}
	}
	appendResourceTypes(&c, set.ResourceTypes)
	return finishRESTCatalog(c)
}

// Azure splits parameters and schemas across versioned files. Resolve only
// references supplied in the checked-in document set; generation never fetches
// URLs or silently drops a required parameter when a dependency is missing.
type azureReferenceResolver map[string]map[string]any

func newAzureReferenceResolver(set RESTDocumentSet) (azureReferenceResolver, error) {
	result := azureReferenceResolver{}
	for _, source := range set.Documents {
		if _, exists := result[source.SourceURI]; exists {
			return nil, fmt.Errorf("duplicate Azure source document %q", source.SourceURI)
		}
		var value map[string]any
		if err := json.Unmarshal(source.Document, &value); err != nil {
			return nil, err
		}
		result[source.SourceURI] = value
	}
	return result, nil
}

func (r azureReferenceResolver) resolve(value map[string]any, baseURI string) (map[string]any, error) {
	seen := map[string]bool{}
	for {
		ref, ok := value["$ref"].(string)
		if !ok {
			return value, nil
		}
		base, _ := url.Parse(baseURI)
		relative, err := url.Parse(ref)
		if err != nil {
			return nil, fmt.Errorf("invalid Azure reference")
		}
		absolute := base.ResolveReference(relative)
		if seen[absolute.String()] {
			return nil, fmt.Errorf("cyclic Azure root reference")
		}
		seen[absolute.String()] = true
		fragment := absolute.Fragment
		absolute.Fragment = ""
		baseURI = absolute.String()
		var target any = r[baseURI]
		for _, part := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
			object, ok := target.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unresolved Azure reference %q", ref)
			}
			target = object[strings.NewReplacer("~1", "/", "~0", "~").Replace(part)]
		}
		value, ok = target.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolved Azure reference %q", ref)
		}
	}
}

var restPathParameter = regexp.MustCompile(`\{(\+?)([A-Za-z0-9_]+)\}`)

func azureService(path, fallback string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.EqualFold(part, "providers") && i+1 < len(parts) && !strings.Contains(parts[i+1], "{") {
			return parts[i+1]
		}
	}
	return fallback
}

func lastRESTSegment(value, separator string) string {
	parts := strings.Split(value, separator)
	return parts[len(parts)-1]
}

func trustedRESTOrigin(value, domain string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || (u.Path != "" && u.Path != "/") || (u.Host != domain && !strings.HasSuffix(u.Host, "."+domain)) {
		return "", fmt.Errorf("invalid REST API origin")
	}
	return "https://" + u.Host, nil
}

func decodeRESTDocumentSet(source []byte) (RESTDocumentSet, error) {
	var set RESTDocumentSet
	if err := json.Unmarshal(source, &set); err != nil {
		return set, fmt.Errorf("decode REST source documents: %w", err)
	}
	if len(set.Documents) == 0 {
		return set, fmt.Errorf("REST source documents are empty")
	}
	for _, document := range set.Documents {
		u, err := url.Parse(document.SourceURI)
		digest, hashErr := hex.DecodeString(document.SourceSHA256)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(document.Document) == 0 || hashErr != nil || len(digest) != 32 {
			return set, fmt.Errorf("REST source document lacks source URL, SHA-256, or API content")
		}
	}
	return set, nil
}

func finishRESTCatalog(c Catalog) (Catalog, error) {
	if len(c.Operations) == 0 {
		return Catalog{}, fmt.Errorf("REST source contains no operations")
	}
	normalize(&c)
	for i := 1; i < len(c.Operations); i++ {
		if c.Operations[i-1].ID == c.Operations[i].ID {
			return Catalog{}, fmt.Errorf("duplicate REST operation %q", c.Operations[i].ID)
		}
	}
	for i := 1; i < len(c.ResourceTypes); i++ {
		if c.ResourceTypes[i-1].NativeType == c.ResourceTypes[i].NativeType {
			return Catalog{}, fmt.Errorf("duplicate REST resource type %q", c.ResourceTypes[i].NativeType)
		}
	}
	return c, nil
}
