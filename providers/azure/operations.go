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
	// Elastic SAN headers and permanent-delete query values are exact wire
	// switches. Generic case-insensitive enum matching must not broaden them.
	if strings.HasPrefix(operation.ID, "Azure.Microsoft.ElasticSan.") {
		for _, key := range []string{"x-ms-access-soft-deleted-resources", "x-ms-delete-snapshots", "x-ms-force-delete", "deleteType"} {
			if value, present := parameters[key]; present {
				wire, ok := value.(string)
				if !ok || key == "deleteType" && wire != "permanent" || key != "deleteType" && wire != "true" && wire != "false" {
					return catalog.RESTRequest{}, serviceDenied("invalid_elastic_san_wire_switch")
				}
			}
		}
	}
	// Azure Local extends a HybridCompute machine. Several native examples omit
	// its /providers segment or pass the VM-instance suffix as the parent.
	// Keep those examples unchanged; accept only the documented parent scope.
	if operation.Call != nil && strings.HasPrefix(operation.Call.Path, "/{resourceUri}/providers/Microsoft.AzureStackHCI/virtualMachineInstances") {
		parent, ok := parameters["resourceUri"].(string)
		id, kind, err := parseID("/" + parent)
		if !ok || parent != strings.TrimSpace(parent) || err != nil || kind != strings.ToLower(hybridMachineType) || len(strings.Split(id, "/")) != 9 {
			return catalog.RESTRequest{}, serviceDenied("invalid_azure_local_machine_scope")
		}
	}
	if operation.Call != nil && strings.HasPrefix(operation.Call.Path, "/{connectedClusterResourceUri}/providers/Microsoft.HybridContainerService/") {
		parent, ok := parameters["connectedClusterResourceUri"].(string)
		id, kind, err := parseID("/" + parent)
		if !ok || parent != strings.TrimSpace(parent) || err != nil || kind != strings.ToLower(fleetArcClusterType) || len(strings.Split(id, "/")) != 9 {
			return catalog.RESTRequest{}, serviceDenied("invalid_azure_local_aks_parent_scope")
		}
	}
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
	if operation.Call.Style == "azure-synapse-rest" {
		if operation.Call.Method != "GET" {
			return contracts.InvocationResult{}, serviceDenied("synapse_data_mutation_not_implemented")
		}
		c, err := r.synapseClient(ctx, invocation.ConnectionID)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		return c.invokeRead(ctx, operation, invocation)
	}
	c, err := r.resolve(ctx, invocation.ConnectionID)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if operation.Call.Style == "azure-batch-rest" {
		return c.invokeBatch(ctx, operation, invocation)
	}
	if operation.Call.Style == "azure-communication-rest" {
		return c.invokeCommunication(ctx, operation, invocation)
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
	if err := c.elasticSanInvocationResponse(operation.ID, request.URL, result); err != nil {
		return contracts.InvocationResult{}, err
	}
	if strings.HasPrefix(operation.ID, "Azure.Microsoft.HybridCompute.") && request.Method == "DELETE" {
		u, _ := url.Parse(request.URL)
		id, _, err := parseID(u.Path)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		receipt, err := c.hybridComputeDeleteReceipt(id, result)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		operationID := text(receipt["status_url"])
		if operationID == "" {
			operationID = text(receipt["result_url"])
		}
		return contracts.InvocationResult{Data: safeAPIPayload(result.data, request.URL), RequestID: result.requestID, OperationID: operationID}, nil
	}
	if strings.HasPrefix(operation.ID, "Azure.Microsoft.Communication.") && request.Method == "DELETE" {
		u, _ := url.Parse(request.URL)
		_, typ, _ := parseID(u.Path)
		operationID, _, err := communicationDeleteReceipt(c.subscription, u.Path, communicationKind(typ), "", result)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		return contracts.InvocationResult{Data: safeAPIPayload(result.data, request.URL), RequestID: result.requestID, OperationID: operationID}, nil
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
	return contracts.InvocationResult{Data: safeAPIPayload(result.data, request.URL), RequestID: result.requestID, NextToken: text(result.data["nextLink"]), OperationID: operationID}, nil
}

func azureRequestID(key string) string {
	hash := sha256.Sum256([]byte(key))
	hash[6] = (hash[6] & 0x0f) | 0x40
	hash[8] = (hash[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

func (c *client) resourceOperation(kind resourceType, nativeID, method string) (catalog.Operation, map[string]any, error) {
	if kind.NativeType == defenderPricingType {
		_, scope, err := c.defenderIdentity(nativeID)
		if err != nil {
			return catalog.Operation{}, nil, err
		}
		return c.defenderOperation(scope, last(nativeID), method)
	}
	if rbacResourceKind(kind.NativeType) != "" {
		id, _, typ, err := rbacResourceID(nativeID)
		wire, wireErr := c.rbacWireID(nativeID)
		if err != nil || wireErr != nil || typ != kind.NativeType || !strings.HasPrefix(id, c.root()+"/") {
			return catalog.Operation{}, nil, serviceDenied("invalid_rbac_resource_operation")
		}
		return c.rbacOperation(typ, wire[:strings.LastIndex(wire, "/providers/")], last(id), method)
	}

	if kind.NativeType == diagnosticSettingsType {
		id, _, typ, err := diagnosticResourceID(nativeID)
		if err != nil || typ != kind.NativeType {
			return catalog.Operation{}, nil, serviceDenied("invalid_diagnostic_resource_operation")
		}
		wire, err := diagnosticWireID(nativeID)
		if err != nil {
			return catalog.Operation{}, nil, err
		}
		return c.diagnosticOperation(diagnosticWireScope(wire), last(id), typ, method)
	}
	if budget, _ := monitorBudgetKind(kind.NativeType); budget != "" {
		return c.monitorBudgetOperation(kind, nativeID, method)
	}
	if insightsLegacyKind(kind.NativeType).kind != "" {
		return c.insightsLegacyOperation(kind, nativeID, method)
	}
	if synapseDataKind(kind.NativeType).kind != "" {
		return c.synapseDataOperation(kind, nativeID, method)
	}
	if isBatchDataType(kind.NativeType) {
		return batchDataOperation(kind, nativeID, method)
	}
	if isCommunicationDataType(kind.NativeType) {
		return communicationDataOperation(kind, nativeID, method)
	}
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
	if isCosmosType(kind.NativeType) || kind.NativeType == dataFactoryNodeType {
		if nativeID != strings.TrimSpace(nativeID) {
			return catalog.Operation{}, nil, fmt.Errorf("invalid case-preserving resource ID")
		}
		parts = strings.Split(nativeID, "/")
	}
	for _, operationID := range ids {
		operation, ok := metadata.catalog.Operation(operationID)
		if !ok || operation.Call == nil || operation.Call.Method != method {
			continue
		}
		template := strings.Split(operation.Call.Path, "/")
		if azureLocalKind(kind.NativeType) != "" && strings.Contains(operation.Call.Path, "{resourceUri}") {
			canonical, err := c.azureLocalIdentity(nativeID, kind.NativeType)
			if err != nil {
				return catalog.Operation{}, nil, err
			}
			parameters := map[string]any{"resourceUri": strings.TrimPrefix(azureLocalMachine(canonical), "/")}
			if _, err := bindAzureREST(operation, parameters); err != nil {
				return catalog.Operation{}, nil, err
			}
			return operation, parameters, nil
		}
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
		// This resolves identity before the caller supplies conditional headers
		// and action options. Validate only the path here; the final BindREST
		// still requires every native header, query parameter and request body.
		identityOperation := operation
		identityOperation.InputSchema = maps.Clone(operation.InputSchema)
		properties := map[string]any{}
		for name, value := range object(operation.InputSchema["properties"]) {
			property := maps.Clone(object(value))
			if property["in"] != "path" {
				delete(property, "required")
			}
			properties[name] = property
		}
		identityOperation.InputSchema["properties"] = properties
		if _, err := bindAzureREST(identityOperation, parameters); err == nil {
			return operation, parameters, nil
		}
	}
	return catalog.Operation{}, nil, fmt.Errorf("Azure resource identity does not match a catalog operation")
}

func (c *client) resourceURL(kind resourceType, nativeID string) (string, error) {
	operation, parameters, err := c.resourceOperation(kind, nativeID, resourceReadMethod(kind.NativeType))
	if err != nil {
		return "", err
	}
	if kind.NativeType == insightsWorkbookType {
		parameters["canFetchContent"] = true
	}
	request, err := bindAzureREST(operation, parameters)
	return request.URL, err
}
