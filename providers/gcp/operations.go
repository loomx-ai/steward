package gcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
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
	if operation.ID == securitySubscriptionGet {
		if _, err := catalog.BindREST(operation, parameters); err != nil {
			return contracts.InvocationResult{}, err
		}
		return c.invokeSecuritySubscription(ctx, parameters)
	}
	properties, _ := operation.InputSchema["properties"].(map[string]any)
	for name, raw := range properties {
		property := object(raw)
		if name == "project" || name == "projectId" || name == "userProject" {
			expected, alternate := c.project, c.number
			if slices.Contains(operation.Call.RawPathParameters, name) && strings.HasPrefix(text(property["pattern"]), "^projects/") {
				expected, alternate = "projects/"+c.project, "projects/"+c.number
			}
			value, present := parameters[name]
			if !present && property["required"] == true {
				parameters[name] = expected
				continue
			}
			if present && value != expected && value != alternate {
				return contracts.InvocationResult{}, fmt.Errorf("GCP invocation belongs to another project")
			}
		}
	}
	if operation.ID == routePolicyGet || operation.ID == routePolicyList || operation.ID == routePolicyDelete || operation.ID == namedSetGet || operation.ID == namedSetList || operation.ID == namedSetDelete {
		for _, key := range []string{"region", "router"} {
			value, ok := parameters[key].(string)
			if !ok || !routePolicySegment.MatchString(value) {
				return contracts.InvocationResult{}, groupDenied("route_policy_parameter_invalid")
			}
		}
		if operation.ID == routePolicyGet || operation.ID == routePolicyDelete || operation.ID == namedSetGet || operation.ID == namedSetDelete {
			parameter := "policy"
			if operation.ID == namedSetGet || operation.ID == namedSetDelete {
				parameter = "namedSet"
			}
			value, ok := parameters[parameter].(string)
			if !ok || !routePolicySegment.MatchString(value) {
				return contracts.InvocationResult{}, groupDenied("route_policy_parameter_invalid")
			}
		}
	}
	if err := c.identityInvocation(ctx, operation, parameters); err != nil {
		return contracts.InvocationResult{}, err
	}
	if err := c.firewallInvocation(ctx, operation, parameters); err != nil {
		return contracts.InvocationResult{}, err
	}
	for _, name := range operation.Call.RawPathParameters {
		value, _ := parameters[name].(string)
		if firewallContainerInvocation(operation.ID) || operation.Call.Product == "cloudidentity" {
			continue
		}
		if operation.Call.Product == "config" {
			for _, part := range strings.Split(value, "/") {
				if !segmentPattern.MatchString(part) || part == "." || part == ".." || part == "-" {
					return contracts.InvocationResult{}, groupDenied("infra_invocation_path_invalid")
				}
			}
		}
		if strings.HasPrefix(operation.ID, "monitoring.locations.global.metricsScopes.") {
			kind := metricsScopeType
			if operation.ID == metricsDelete {
				kind = monitoredProjectType
			}
			id, err := c.metricsID(kind, value, true)
			if err != nil {
				return contracts.InvocationResult{}, err
			}
			if operation.ID == metricsDelete && last(id) == c.number {
				return contracts.InvocationResult{}, groupDenied("metrics_scope_self_protected")
			}
			parameters[name] = strings.TrimPrefix(id, "//"+metricsHost+"/")
			continue
		}
		if operation.ID == "monitoring.operations.get" {
			if _, err := metricsOperationURL(map[string]any{"name": value}, ""); err != nil {
				return contracts.InvocationResult{}, err
			}
			continue
		}
		// Discovery's {+parameter} controls URI expansion. BigQuery also uses
		// it for scalar project/dataset IDs; it does not always mean a full name.
		if !strings.Contains(value, "/") && !strings.HasPrefix(text(object(properties[name])["pattern"]), "^projects/") {
			continue
		}
		parts := strings.Split(value, "/")
		if len(parts) < 2 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number) {
			return contracts.InvocationResult{}, fmt.Errorf("GCP invocation resource belongs to another project")
		}
	}
	if operation.ID == metricsReverse {
		value := parameters["monitoredResourceContainer"]
		if value != "projects/"+c.project && value != "projects/"+c.number {
			return contracts.InvocationResult{}, groupDenied("metrics_scope_reverse_project_changed")
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
	if operation.ID == routePolicyGet || operation.ID == namedSetGet {
		kind, parameter := routePolicyType, "policy"
		if operation.ID == namedSetGet {
			kind, parameter = namedSetType, "namedSet"
		}
		if err := routerComponentData(kind, object(result.Data["resource"]), text(parameters[parameter])); err != nil {
			return contracts.InvocationResult{}, err
		}
	}
	if operation.ID == securityBillingGet {
		if err := c.securityBillingMetadata(result.Data, text(parameters["name"])); err != nil {
			return contracts.InvocationResult{}, err
		}
	}
	result.Data = safePayload(result.Data)
	if operation.Call.Product == "config" {
		result.Data = safeInfraPayload(result.Data)
	}
	if operation.Call.Product == "datafusion" {
		result.Data = safeFusionPayload(result.Data)
	}
	if operation.Call.Product == "tpu" {
		result.Data = safeTPUPayload(result.Data)
	}
	if operation.Call.Product == "discoveryengine" {
		result.Data = safeDiscoveryPayload(result.Data)
	}
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
	if isIdentityGroup(kind.NativeType) {
		return identityResourceOperation(metadata, kind.NativeType, nativeID, method)
	}
	if isRouterComponent(kind.NativeType) {
		return c.routerComponentOperation(kind.NativeType, nativeID, method)
	}
	if kind.NativeType == securitySubscriptionType {
		return securitySubscriptionOperation(metadata, nativeID, method)
	}
	if kind.NativeType == organizationType {
		return organizationOperation(metadata, nativeID, method)
	}
	if isInfra(kind.NativeType) {
		if _, err := c.infraName(kind.NativeType, nativeID); err != nil {
			return catalog.Operation{}, nil, err
		}
	}
	if isMetricsScope(kind.NativeType) {
		return c.metricsOperation(kind.NativeType, nativeID, method)
	}
	if isFirewall(kind.NativeType) {
		return c.firewallResourceOperation(kind.NativeType, nativeID, method)
	}
	if isFusion(kind.NativeType) {
		if _, err := c.fusionName(kind.NativeType, nativeID); err != nil {
			return catalog.Operation{}, nil, err
		}
		if kind.NativeType != fusionInstanceType && method == "GET" {
			method, _, parameters := fusionListParameters(kind.NativeType, strings.Join(strings.Split(name, "/")[:6], "/"))
			op, ok := metadata.catalog.Operation(method)
			if !ok {
				return catalog.Operation{}, nil, groupDenied("datafusion_child_method_missing")
			}
			return op, parameters, nil
		}
	}
	if isTPU(kind.NativeType) {
		if _, err := c.tpuName(kind.NativeType, nativeID); err != nil {
			return catalog.Operation{}, nil, err
		}
		if kind.NativeType == tpuReservationType && method == "GET" {
			op, ok := metadata.catalog.Operation("tpu.projects.locations.reservations.list")
			if !ok {
				return catalog.Operation{}, nil, groupDenied("tpu_reservation_method_missing")
			}
			return op, map[string]any{"parent": strings.Join(strings.Split(name, "/")[:4], "/")}, nil
		}
	}
	if isDiscovery(kind.NativeType) {
		parts := strings.Split(name, "/")
		if len(parts) < 6 || parts[0] != "projects" || parts[2] != "locations" || !discoveryLocation(parts[3]) || parts[4] != "collections" {
			return catalog.Operation{}, nil, groupDenied("discoveryengine_resource_scope_invalid")
		}
	}
	if host == "iap.googleapis.com" {
		name = strings.Replace(name, "projects/"+c.project+"/", "projects/"+c.number+"/", 1)
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		validRecordName := kind.NativeType == dnsRecordSetType && i == len(parts)-2 && dnsRecordName(part)
		if (!segmentPattern.MatchString(part) && !validRecordName && !(isTPU(kind.NativeType) && i == len(parts)-1 && tpuSegment(part))) || part == "." || part == ".." {
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
		if !ok || operation.Call == nil || (operation.Call.Method != method && !(method == "DELETE" && operation.Call.Method == "POST" && operation.Destructive)) {
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
		if kind.NativeType == "bigtableadmin.googleapis.com/Table" && method == "GET" {
			// LIST cannot use FULL; every table detail read needs both schema
			// and replication metadata, including cleanup dependency reads.
			parameters["view"] = "FULL"
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
