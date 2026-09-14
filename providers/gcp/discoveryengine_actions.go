package gcp

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) readResource(ctx context.Context) (map[string]any, error) {
	if a.kind.NativeType == routePolicyType {
		return a.client.routePolicyRead(ctx, a.identity.NativeID)
	}
	if isFusion(a.kind.NativeType) {
		return a.client.fusionRead(ctx, a.kind.NativeType, a.identity.NativeID)
	}
	if isTPU(a.kind.NativeType) {
		return a.client.tpuRead(ctx, a.kind.NativeType, a.identity.NativeID)
	}
	if isDiscovery(a.kind.NativeType) {
		return a.client.discoveryRead(ctx, a.kind.NativeType, a.identity.NativeID)
	}
	data, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if err == nil && a.kind.NativeType == "bigtableadmin.googleapis.com/Table" && a.client.canonicalName("//bigtableadmin.googleapis.com/"+text(data["name"])) != a.client.canonicalName(a.identity.NativeID) {
		return nil, groupDenied("bigtable_identity_changed")
	}
	return data, err
}

func discoveryPhase(request contracts.ActionRequest, operation string) map[string]any {
	return map[string]any{"phase": "discoveryengine_delete", "resource": request.Asset.Identity.NativeID, "configuration": request.Asset.Normalized[discoveryProof], "operation": operation, "review": serviceReview(request)}
}

func (a *action) discoveryOperation(data map[string]any, requestIDs ...string) (string, error) {
	if !a.regionalOperation() {
		if len(data) > 0 {
			return "", groupDenied("discoveryengine_delete_response_invalid")
		}
		return "", nil
	}
	name := text(data["name"])
	parent := a.client.canonicalName("//" + discoveryHost + "/" + name)
	resource := a.identity.NativeID
	if a.kind.NativeType == discoveryHost+"/TargetSite" {
		resource = resource[:strings.LastIndex(resource, "/")]
	}
	if !strings.HasPrefix(parent, resource+"/operations/") || strings.Contains(strings.TrimPrefix(parent, resource+"/operations/"), "/") || !segmentPattern.MatchString(last(parent)) || last(parent) == "." || last(parent) == ".." {
		return "", groupDenied("discoveryengine_operation_scope_changed")
	}
	if done, present := data["done"]; present {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("discoveryengine_operation_state_invalid")
		}
	}
	if raw, present := data["error"]; present {
		failure, ok := raw.(map[string]any)
		if !ok || len(failure) == 0 {
			return "", groupDenied("discoveryengine_operation_error_invalid")
		}
		requestID := ""
		if len(requestIDs) > 0 {
			requestID = requestIDs[0]
		}
		return "", operationError(data, requestID)
	}
	if metadata, ok := data["metadata"].(map[string]any); ok {
		if kind := text(metadata["@type"]); kind != "" && kind != "type.googleapis.com/google.cloud.discoveryengine."+a.deleteOperation.Call.Version+".Delete"+last(a.kind.NativeType)+"Metadata" {
			return "", groupDenied("discoveryengine_operation_type_changed")
		}
	} else if data["metadata"] != nil {
		return "", groupDenied("discoveryengine_operation_metadata_invalid")
	}
	return a.operationURL(data)
}

func (a *action) waitDiscovery(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.discoveryActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.Readback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
	}
	if result.Data["phase"] != "discoveryengine_delete" || result.Data["resource"] != request.Asset.Identity.NativeID || result.Data["configuration"] != request.Asset.Normalized[discoveryProof] || result.Data["review"] != serviceReview(request) || text(result.Data["operation"]) != result.ProviderOperationID {
		return contracts.WaitResult{}, groupDenied("discoveryengine_phase_changed")
	}
	if operation := result.ProviderOperationID; operation != "" {
		endpoint, err := url.Parse(operation)
		if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return contracts.WaitResult{}, groupDenied("discoveryengine_operation_invalid")
		}
		name := strings.TrimPrefix(endpoint.Path, "/"+a.deleteOperation.Call.Version+"/")
		expected, err := a.discoveryOperation(map[string]any{"name": name})
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("discoveryengine_operation_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.discoveryOperation(response.Data, response.RequestID)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != operation {
				return contracts.WaitResult{}, groupDenied("discoveryengine_operation_changed")
			}
			// Check recreation even while the operation is still pending.
			if _, err := a.Readback(ctx, request); err != nil {
				return contracts.WaitResult{}, err
			}
			if response.Data["done"] != true {
				return contracts.WaitResult{State: "deleting", RetryAfter: 2 * time.Second}, nil
			}
		}
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
}

func (a *action) discoveryReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.discoveryActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	exists := false
	for _, impact := range request.LifecycleImpacts {
		live, err := a.client.discoveryRead(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if err := discoverySameResource(impact.Asset.Identity.NativeType, impact.Asset.Normalized, live); err != nil {
			return contracts.ReadbackResult{}, err
		}
		exists = true
	}
	if exists {
		return contracts.ReadbackResult{Exists: true, State: "service_children_deleting"}, nil
	}
	// Re-read the root after the last member check so a recreated root cannot be
	// hidden by an earlier 404 while its old operation is finishing.
	live, err := a.client.discoveryRead(ctx, a.kind.NativeType, request.Asset.Identity.NativeID)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := discoverySameResource(a.kind.NativeType, request.Asset.Normalized, live); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}
