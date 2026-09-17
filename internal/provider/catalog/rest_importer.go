package catalog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
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
	// Embedded components can be removed by native merge-patch. Only explicit
	// resource deletion bindings classify PATCH as destructive; reads never do.
	for _, resource := range set.ResourceTypes {
		if resource.REST == nil {
			continue
		}
		for _, id := range resource.REST.DeleteOperations {
			for i := range c.Operations {
				if c.Operations[i].ID == id && c.Operations[i].Method == "PATCH" {
					c.Operations[i].Destructive = true
				}
			}
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
	Swagger           string `json:"swagger"`
	Host              string `json:"host"`
	BasePath          string `json:"basePath"`
	ParameterizedHost *struct {
		Template        string           `json:"hostTemplate"`
		UseSchemePrefix bool             `json:"useSchemePrefix"`
		Parameters      []map[string]any `json:"parameters"`
	} `json:"x-ms-parameterized-host"`
	Info struct {
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
	titles := map[string]string{}
	for _, upstream := range set.Documents {
		if upstream.Dependency {
			continue
		}
		if upstream.SourceFormat == msgraphSourceFormat {
			if err := importMicrosoftGraph(&c, upstream); err != nil {
				return Catalog{}, err
			}
			continue
		}
		var document azureSwagger
		if err := json.Unmarshal(upstream.Document, &document); err != nil {
			return Catalog{}, fmt.Errorf("decode Azure OpenAPI document: %w", err)
		}
		batch := document.Host == "" && document.Info.Title == "Azure Batch" && document.ParameterizedHost != nil
		communication := document.Host == "" && (document.Info.Title == "PhoneNumbersClient" || document.Info.Title == "Azure Communication Room Service") && document.ParameterizedHost != nil
		synapse := document.Host == "" && (document.Info.Title == "SparkClient" || document.Info.Title == "ArtifactsClient") && document.ParameterizedHost != nil && strings.Contains(upstream.SourceURI, "/specification/synapse/data-plane/Microsoft.Synapse/")
		keyvault := document.Host == "" && document.Info.Title == "KeyVaultClient" && document.ParameterizedHost != nil && strings.Contains(upstream.SourceURI, "/specification/keyvault/data-plane/")
		var endpointParameter map[string]any
		if keyvault {
			host := document.ParameterizedHost
			if len(host.Parameters) == 1 {
				endpointParameter, err = resolver.resolve(host.Parameters[0], upstream.SourceURI)
				if err != nil {
					return Catalog{}, err
				}
			}
			if host.Template != "{vaultBaseUrl}" || host.UseSchemePrefix || len(host.Parameters) != 1 || endpointParameter["name"] != "vaultBaseUrl" || endpointParameter["in"] != "path" || endpointParameter["type"] != "string" || endpointParameter["required"] != true || endpointParameter["x-ms-skip-url-encoding"] != true || document.BasePath != "" {
				return Catalog{}, fmt.Errorf("unsupported Azure Key Vault parameterized host")
			}
		}
		if batch || communication || synapse {
			host := document.ParameterizedHost
			if len(host.Parameters) == 1 {
				endpointParameter, err = resolver.resolve(host.Parameters[0], upstream.SourceURI)
				if err != nil {
					return Catalog{}, err
				}
			}
			if host.Template != "{endpoint}" || host.UseSchemePrefix || len(host.Parameters) != 1 || endpointParameter["name"] != "endpoint" || endpointParameter["in"] != "path" || endpointParameter["type"] != "string" || endpointParameter["required"] != true || endpointParameter["x-ms-skip-url-encoding"] != true || document.BasePath != "" {
				return Catalog{}, fmt.Errorf("unsupported Azure data-plane parameterized host")
			}
		}
		if document.Swagger != "2.0" || document.Info.Version == "" || (!batch && !communication && !synapse && !keyvault && (document.Host != "management.azure.com" || document.ParameterizedHost != nil)) {
			return Catalog{}, fmt.Errorf("Azure OpenAPI document must describe a supported versioned Azure API")
		}
		titles[upstream.SourceURI] = document.Info.Title
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
				if batch {
					service = "Microsoft.Batch.DataPlane"
				}
				if communication {
					service = "Microsoft.Communication.DataPlane"
				}
				if synapse {
					service = "Microsoft.Synapse.DataPlane"
				}
				if keyvault {
					service = "Microsoft.KeyVault.DataPlane"
				}
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
				if synapse && document.Info.Title == "SparkClient" {
					if p, ok := properties["livyApiVersion"].(map[string]any); !ok || p["in"] != "path" || p["required"] != true || !strings.Contains(path, "/versions/{livyApiVersion}/") {
						return Catalog{}, fmt.Errorf("unsupported Synapse Livy version contract")
					}
				} else {
					properties["api-version"] = map[string]any{"type": "string", "in": "query", "enum": []string{document.Info.Version}}
				}
				if batch || communication || synapse {
					properties["endpoint"] = endpointParameter
				}
				if keyvault {
					properties["vaultBaseUrl"] = endpointParameter
					// Key Vault names path parameters with hyphens, which are not
					// template identifiers. Bind them by their camel-case client names.
					path, properties, err = keyVaultPathParameters(path, properties)
					if err != nil {
						return Catalog{}, err
					}
				}
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
				if batch {
					call.Style, call.Endpoint, call.EndpointParameters = "azure-batch-rest", "{endpoint}", []string{"endpoint"}
				}
				if communication {
					call.Style, call.Endpoint, call.EndpointParameters = "azure-communication-rest", "{endpoint}", []string{"endpoint"}
				}
				if synapse {
					call.Style, call.Endpoint, call.EndpointParameters = "azure-synapse-rest", "{endpoint}", []string{"endpoint"}
					// Native names are individual URL segments, even when Swagger
					// asks its generated clients to skip encoding.
					call.RawPathParameters = nil
				}
				if keyvault {
					call.Style, call.Endpoint, call.EndpointParameters, call.RawPathParameters = "azure-keyvault-rest", "{vaultBaseUrl}", []string{"vaultBaseUrl"}, nil
				}
				destructive := isDestructiveOperation(operation.ID, method) || (batch && operation.ID == "Pools_RemoveNodes" && method == "post" && path == "/pools/{poolId}/removenodes")
				c.Operations = append(c.Operations, Operation{ID: "Azure." + service + "." + operation.ID, Name: operation.ID, Service: service, Method: call.Method, Path: fullPath, Destructive: destructive, InputSchema: map[string]any{"type": "object", "properties": properties}, OutputSchema: output, Pagination: pagination, Call: call, SourceURI: upstream.SourceURI})
			}
		}
	}
	// Public and private DNS use the same native RecordSets operation IDs in
	// distinct API documents. Qualify only collisions by the official document
	// title; retain the native operationId in Name and all transport metadata.
	// Official titles such as "Cosmos DB" use spaces; encode those as underscores.
	counts := map[string]int{}
	for _, operation := range c.Operations {
		counts[operation.ID]++
	}
	titlePattern := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	for i := range c.Operations {
		operation := &c.Operations[i]
		if counts[operation.ID] > 1 {
			title := strings.ReplaceAll(titles[operation.SourceURI], " ", "_")
			if !titlePattern.MatchString(title) {
				return Catalog{}, fmt.Errorf("ambiguous Azure operation %q has no usable document title", operation.Name)
			}
			operation.ID = "Azure." + operation.Service + "." + title + "." + operation.Name
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

var keyVaultPathParameter = regexp.MustCompile(`\{([a-z]+(?:-[a-z]+)+)\}`)

func keyVaultPathParameters(path string, properties map[string]any) (string, map[string]any, error) {
	for _, match := range keyVaultPathParameter.FindAllStringSubmatch(path, -1) {
		words := strings.Split(match[1], "-")
		for i := 1; i < len(words); i++ {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
		name := strings.Join(words, "")
		parameter, ok := properties[match[1]].(map[string]any)
		if _, exists := properties[name]; !ok || exists || parameter["in"] != "path" {
			return "", nil, fmt.Errorf("unsupported Azure Key Vault path parameter %q", match[1])
		}
		parameter = maps.Clone(parameter)
		parameter["name"] = name
		// An empty certificate version selects the current version.
		if name == "certificateVersion" {
			parameter["required"] = false
		}
		delete(properties, match[1])
		properties[name] = parameter
		path = strings.ReplaceAll(path, match[0], "{"+name+"}")
	}
	return path, properties, nil
}

var restPathParameter = regexp.MustCompile(`\{(\+?)([A-Za-z0-9_]+)\}`)

func azureService(path, fallback string) string {
	parts := strings.Split(path, "/")
	// An extension resource belongs to the innermost provider, even when
	// its scope is a resource owned by a different provider.
	for i := len(parts) - 2; i >= 0; i-- {
		if strings.EqualFold(parts[i], "providers") && !strings.Contains(parts[i+1], "{") {
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
