package azure

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// SiteCertificates' 2025-05-01 Swagger contradicts App Service naming rules by
// rejecting hyphens and leading digits in the parent site name. Keep the source
// unchanged and correct only this known runtime constraint. DNS names returned
// by ARM use their ASCII/Punycode form.
// https://learn.microsoft.com/azure/azure-resource-manager/management/resource-name-rules#microsoftweb
func bindAzureREST(operation catalog.Operation, parameters map[string]any) (catalog.RESTRequest, error) {
	if operation.Call != nil && operation.Call.Version == "2025-05-01" && strings.HasPrefix(operation.ID, "Azure.Microsoft.Web.SiteCertificates_") {
		properties := object(operation.InputSchema["properties"])
		if object(properties["name"])["pattern"] == "^[A-z][A-z0-9]*$" {
			operation.InputSchema = maps.Clone(operation.InputSchema)
			properties = maps.Clone(properties)
			name := maps.Clone(object(properties["name"]))
			name["pattern"] = "^[A-Za-z0-9][A-Za-z0-9-]{0,58}[A-Za-z0-9]$"
			properties["name"] = name
			operation.InputSchema["properties"] = properties
		}
	}
	return catalog.BindREST(operation, parameters)
}

func (r *Runtime) Invoke(ctx context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	operation, ok := metadata.catalog.Operation(invocation.Operation)
	if !ok || operation.Call == nil {
		return contracts.InvocationResult{}, fmt.Errorf("unknown Azure operation %q", invocation.Operation)
	}
	c, err := r.resolve(ctx, invocation.ConnectionID)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	parameters := map[string]any{}
	for name, value := range invocation.Parameters {
		parameters[name] = value
	}
	properties := object(operation.InputSchema["properties"])
	for name := range properties {
		if !strings.EqualFold(name, "subscriptionId") {
			continue
		}
		if supplied, exists := parameters[name]; exists {
			value, ok := supplied.(string)
			if !ok || !strings.EqualFold(value, c.subscription) {
				return contracts.InvocationResult{}, fmt.Errorf("Azure invocation belongs to another subscription")
			}
		}
		parameters[name] = c.subscription
	}
	request, err := bindAzureREST(operation, parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if invocation.IdempotencyKey != "" {
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["x-ms-client-request-id"] = azureRequestID(invocation.IdempotencyKey)
	}
	result, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	operationID := operationLocation(result.header)
	if operationID != "" {
		validate := c.validateURL
		u, _ := url.Parse(request.URL)
		_, kind, _ := parseID(u.Path)
		if isCosmosType(kind) {
			validate = func(endpoint string) error {
				return validateCosmosOperationURL(c.subscription, u.Path, operation.Call.Version, endpoint)
			}
		}
		if strings.HasPrefix(invocation.Operation, "Azure.Microsoft.StreamAnalytics.") {
			owner := u.Path
			if strings.EqualFold(last(owner), "stop") {
				owner = strings.TrimSuffix(owner, "/"+last(owner))
			}
			validate = func(endpoint string) error {
				return validateStreamAnalyticsOperationURL(c.subscription, owner, operation.Call.Version, endpoint)
			}
		}
		if strings.HasPrefix(invocation.Operation, "Azure.Microsoft.Kusto.") {
			validate = func(endpoint string) error {
				return validateKustoOperationURL(c.subscription, "", operation.Call.Version, endpoint)
			}
		}
		if isMongoClusterType(kind) {
			validate = func(endpoint string) error {
				return validateMongoClusterOperationURL(c.subscription, "", operation.Call.Version, endpoint)
			}
		}
		if strings.HasPrefix(invocation.Operation, "Azure.Microsoft.Dashboard.") && grafanaGlobalOperation(operationID) {
			validate = func(endpoint string) error { return validateGrafanaGlobalOperation(endpoint, "") }
		}
		if err := validate(operationID); err != nil {
			return contracts.InvocationResult{}, err
		}
	}
	return contracts.InvocationResult{Data: safePayload(result.data), RequestID: result.requestID, NextToken: text(result.data["nextLink"]), OperationID: operationID}, nil
}

func azureRequestID(key string) string {
	hash := sha256.Sum256([]byte(key))
	hash[6] = (hash[6] & 0x0f) | 0x40
	hash[8] = (hash[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

func (c *client) resourceOperation(kind resourceType, nativeID, method string) (catalog.Operation, map[string]any, error) {
	id, nativeType, err := parseID(nativeID)
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(nativeType, kind.NativeType) {
		return catalog.Operation{}, nil, fmt.Errorf("Azure resource identity does not match its subscription and type")
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	ids := kind.ReadOperations
	if method == "DELETE" {
		ids = kind.DeleteOperations
	}
	parts := strings.Split(id, "/")
	if isCosmosType(kind.NativeType) {
		if nativeID != strings.TrimSpace(nativeID) {
			return catalog.Operation{}, nil, fmt.Errorf("invalid Cosmos DB resource ID")
		}
		parts = strings.Split(nativeID, "/")
	}
	for _, operationID := range ids {
		operation, ok := metadata.catalog.Operation(operationID)
		if !ok || operation.Call == nil || operation.Call.Method != method {
			continue
		}
		template := strings.Split(operation.Call.Path, "/")
		// Monitor associations are extension resources. Their native resourceUri
		// consumes the complete, already validated ARM parent path.
		if kind.NativeType == dataCollectionAssociationType && operation.Call.Path == "/{resourceUri}/providers/Microsoft.Insights/dataCollectionRuleAssociations/{associationName}" {
			parent, err := dataCollectionMonitoredResource(nativeID)
			if err != nil {
				return catalog.Operation{}, nil, err
			}
			parameters := map[string]any{"resourceUri": strings.TrimPrefix(parent, "/"), "associationName": last(id)}
			if _, err := bindAzureREST(operation, parameters); err != nil {
				return catalog.Operation{}, nil, err
			}
			return operation, parameters, nil
		}
		if len(template) != len(parts) {
			continue
		}
		parameters := map[string]any{}
		matched := true
		for i, part := range template {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				parameters[strings.Trim(part, "{}")] = parts[i]
			} else if !strings.EqualFold(part, parts[i]) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		// ARM IDs are case-insensitive, but native enum values retain their
		// declared spelling (for example DNS recordType A and AAAA).
		for name, value := range parameters {
			for _, allowed := range array(object(object(operation.InputSchema["properties"])[name])["enum"]) {
				if strings.EqualFold(text(value), text(allowed)) {
					parameters[name] = allowed
					break
				}
			}
		}
		if _, err := bindAzureREST(operation, parameters); err == nil {
			return operation, parameters, nil
		}
	}
	return catalog.Operation{}, nil, fmt.Errorf("Azure resource identity does not match a catalog operation")
}

func (c *client) resourceURL(kind resourceType, nativeID string) (string, error) {
	operation, parameters, err := c.resourceOperation(kind, nativeID, "GET")
	if err != nil {
		return "", err
	}
	request, err := bindAzureREST(operation, parameters)
	return request.URL, err
}
