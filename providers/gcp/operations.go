package gcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (r *Runtime) Invoke(ctx context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	operation, ok := metadata.catalog.Operation(invocation.Operation)
	if !ok || operation.Call == nil {
		return contracts.InvocationResult{}, fmt.Errorf("unknown GCP operation %q", invocation.Operation)
	}
	c, err := r.resolve(ctx, invocation.ConnectionID)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	parameters := map[string]any{}
	for name, value := range invocation.Parameters {
		parameters[name] = value
	}
	properties, _ := operation.InputSchema["properties"].(map[string]any)
	for name, raw := range properties {
		property := object(raw)
		if name == "project" || name == "projectId" || name == "userProject" {
			value, present := parameters[name]
			if !present && property["required"] == true {
				parameters[name] = c.project
				continue
			}
			if present && value != c.project && value != c.number {
				return contracts.InvocationResult{}, fmt.Errorf("GCP invocation belongs to another project")
			}
		}
	}
	for _, name := range operation.Call.RawPathParameters {
		value, _ := parameters[name].(string)
		parts := strings.Split(value, "/")
		if len(parts) < 2 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number) {
			return contracts.InvocationResult{}, fmt.Errorf("GCP invocation resource belongs to another project")
		}
	}
	if name := operation.Call.IdempotencyParameter; name != "" && invocation.IdempotencyKey != "" {
		parameters[name] = googleRequestID(invocation.IdempotencyKey)
	}
	request, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	result, err := c.requestResult(ctx, request.Method, request.URL, nil, request.Body)
	if err != nil {
		return result, err
	}
	result.Data = safePayload(result.Data)
	return result, nil
}

func googleRequestID(key string) string {
	hash := sha256.Sum256([]byte(key))
	hash[6] = (hash[6] & 0x0f) | 0x40
	hash[8] = (hash[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

// resourceOperation binds an identity to one of the resource's exact official
// methods, including zonal/regional/global variants. Resource names never select
// arbitrary origins, API versions, collections, or action methods.
func (c *client) resourceOperation(kind resourceType, nativeID, method string) (catalog.Operation, map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	host := strings.Split(kind.NativeType, "/")[0]
	prefix := "//" + host + "/"
	if !strings.HasPrefix(nativeID, prefix) {
		return catalog.Operation{}, nil, fmt.Errorf("invalid GCP resource identity")
	}
	name := strings.TrimPrefix(nativeID, prefix)
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return catalog.Operation{}, nil, fmt.Errorf("invalid GCP resource path")
		}
	}
	if host != "storage.googleapis.com" && (len(parts) < 4 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number)) {
		return catalog.Operation{}, nil, fmt.Errorf("GCP resource belongs to another project")
	}
	ids := kind.ReadOperations
	if method == "DELETE" {
		ids = kind.DeleteOperations
	}
	for _, id := range ids {
		operation, ok := metadata.catalog.Operation(id)
		if !ok || operation.Call == nil || operation.Call.Method != method {
			continue
		}
		parameters := map[string]any{}
		call := operation.Call
		switch {
		case host == "storage.googleapis.com":
			if len(parts) != 1 {
				continue
			}
			parameters["bucket"] = name
		case len(call.RawPathParameters) == 1:
			parameters[call.RawPathParameters[0]] = name
		default:
			index := strings.Index(call.Path, "/projects/")
			if index < 0 {
				continue
			}
			template := strings.Split(call.Path[index+1:], "/")
			if len(template) != len(parts) {
				continue
			}
			matched := true
			for i, part := range template {
				if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
					parameters[strings.Trim(part, "{}")] = parts[i]
				} else if part != parts[i] {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		if _, err := catalog.BindREST(operation, parameters); err == nil {
			return operation, parameters, nil
		}
	}
	return catalog.Operation{}, nil, fmt.Errorf("GCP resource identity does not match a catalog operation")
}

func (c *client) resourceURL(kind resourceType, nativeID string) (string, error) {
	operation, parameters, err := c.resourceOperation(kind, nativeID, "GET")
	if err != nil {
		return "", err
	}
	request, err := catalog.BindREST(operation, parameters)
	return request.URL, err
}
