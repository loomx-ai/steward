package catalog

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
)

const msgraphSourceFormat = "msgraph-openapi3"
const msgraphOrigin = "https://graph.microsoft.com"

type msgraphDocument struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Version string `json:"version"`
	} `json:"info"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Parameters map[string]map[string]any `json:"parameters"`
	} `json:"components"`
}

type msgraphOperation struct {
	ID         string           `json:"operationId"`
	Parameters []map[string]any `json:"parameters"`
	Responses  map[string]any   `json:"responses"`
}

// importMicrosoftGraph reads the pinned Microsoft Graph OpenAPI 3 subset. Graph
// is a separate tenant-level API with its own token audience, so its operations
// use a dedicated call style bound to the public Graph origin.
func importMicrosoftGraph(c *Catalog, upstream RESTSourceDocument) error {
	if !strings.HasPrefix(upstream.SourceURI, "https://raw.githubusercontent.com/microsoftgraph/msgraph-metadata/") {
		return fmt.Errorf("Microsoft Graph metadata must come from the official msgraph-metadata repository")
	}
	var document msgraphDocument
	if err := json.Unmarshal(upstream.Document, &document); err != nil {
		return fmt.Errorf("decode Microsoft Graph OpenAPI document: %w", err)
	}
	version := document.Info.Version
	if !strings.HasPrefix(document.OpenAPI, "3.") || version != "v1.0" || len(document.Servers) != 1 || document.Servers[0].URL != msgraphOrigin+"/"+version {
		return fmt.Errorf("Microsoft Graph document must describe the public v1.0 service")
	}
	resolve := func(parameter map[string]any) (map[string]any, error) {
		if reference, ok := parameter["$ref"].(string); ok {
			name, found := strings.CutPrefix(reference, "#/components/parameters/")
			resolved, exists := document.Components.Parameters[name]
			if !found || !exists {
				return nil, fmt.Errorf("unresolved Microsoft Graph parameter %q", reference)
			}
			return resolved, nil
		}
		return parameter, nil
	}
	paths := make([]string, 0, len(document.Paths))
	for path := range document.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		item := document.Paths[path]
		var common []map[string]any
		if raw, ok := item["parameters"]; ok {
			if err := json.Unmarshal(raw, &common); err != nil {
				return err
			}
		}
		for method, raw := range item {
			if !isHTTPMethod(method) {
				continue
			}
			var operation msgraphOperation
			if err := json.Unmarshal(raw, &operation); err != nil {
				return err
			}
			if operation.ID == "" {
				return fmt.Errorf("Microsoft Graph method has no operationId")
			}
			properties := map[string]any{}
			for _, value := range append(append([]map[string]any{}, common...), operation.Parameters...) {
				parameter, err := resolve(value)
				if err != nil {
					return err
				}
				name, _ := parameter["name"].(string)
				if name == "" {
					return fmt.Errorf("Microsoft Graph operation %q has an unnamed parameter", operation.ID)
				}
				property := map[string]any{"name": name, "in": parameter["in"]}
				if parameter["required"] == true {
					property["required"] = true
				}
				if schema, ok := parameter["schema"].(map[string]any); ok {
					if kind, ok := schema["type"].(string); ok && kind != "array" {
						property["type"] = kind
					}
				}
				properties[name] = property
			}
			boundPath, bound, err := keyVaultPathParameters(path, properties)
			if err != nil {
				return err
			}
			for _, match := range restPathParameter.FindAllStringSubmatch(boundPath, -1) {
				if _, exists := bound[match[2]]; !exists {
					return fmt.Errorf("Microsoft Graph operation %q lacks path parameter %q", operation.ID, match[2])
				}
			}
			var pagination *Pagination
			if success, _ := json.Marshal(operation.Responses["2XX"]); strings.Contains(string(success), "CollectionResponse") {
				pagination = &Pagination{OutputTokenPath: "@odata.nextLink", ItemsPath: "value"}
			}
			fullPath := "/" + version + boundPath
			call := &OperationCall{Product: "Microsoft.Graph", Version: version, Style: "azure-graph-rest", Protocol: "HTTPS", Method: strings.ToUpper(method), Path: fullPath, Endpoint: msgraphOrigin, ParameterPosition: "query", BodyType: "json"}
			c.Operations = append(c.Operations, Operation{
				ID: "Azure.Microsoft.Graph." + operation.ID, Name: operation.ID, Service: "Microsoft.Graph", Method: call.Method, Path: fullPath,
				Destructive: isDestructiveOperation(operation.ID[strings.LastIndex(operation.ID, ".")+1:], method), InputSchema: map[string]any{"type": "object", "properties": maps.Clone(bound)},
				Pagination: pagination, Call: call, SourceURI: upstream.SourceURI, SourceFormat: msgraphSourceFormat,
			})
		}
	}
	return nil
}
