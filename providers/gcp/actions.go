package gcp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type action struct {
	client           *client
	kind             resourceType
	endpoint         string
	deleteOperation  catalog.Operation
	deleteParameters map[string]any
}

func (r *Runtime) ResolveAction(ctx context.Context, id asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok || value.Identity.Provider != asset.ProviderGCP || len(kind.DeleteOperations) == 0 {
		return nil, fmt.Errorf("GCP resource has no action driver")
	}
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	endpoint, err := c.resourceURL(kind, value.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	operation, parameters, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	return &action{client: c, kind: kind, endpoint: endpoint, deleteOperation: operation, deleteParameters: parameters}, nil
}
func (*action) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *action) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if request.Action != "delete" {
		return contracts.PreflightResult{Reason: "unsupported_action"}, nil
	}
	data, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if reason := protectionReason(a.kind.NativeType, data); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	if a.kind.NativeType == "storage.googleapis.com/Bucket" {
		// A bucket's globally unique name has no project component.
		number := fmt.Sprint(data["projectNumber"])
		if number != a.client.number {
			return contracts.PreflightResult{Reason: "bucket_project_not_verified"}, nil
		}
		objects, err := a.client.request(ctx, "GET", a.endpoint+"/o", url.Values{"maxResults": {"1"}, "versions": {"true"}})
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(array(objects["items"])) > 0 {
			return contracts.PreflightResult{Reason: "bucket_not_empty"}, nil
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *action) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: check.Reason, Message: contracts.SafeProviderValidationMessage}}
	}
	if check.Absent {
		return contracts.ActionResult{}, nil
	}
	parameters := map[string]any{}
	for key, value := range a.deleteParameters {
		parameters[key] = value
	}
	if a.deleteOperation.Call == nil {
		return contracts.ActionResult{}, fmt.Errorf("GCP deletion has no catalog operation")
	}
	if token := a.deleteOperation.Call.IdempotencyParameter; token != "" && request.IdempotencyKey != "" {
		parameters[token] = googleRequestID(request.IdempotencyKey)
	}
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := response.Data
	if failure := operationError(data, response.RequestID); failure != nil {
		return contracts.ActionResult{}, failure
	}
	operation, err := a.operationURL(data)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: map[string]any{"operation": operation}, RetryAfter: 2 * time.Second}, nil
}
func (a *action) operationURL(data map[string]any) (string, error) {
	nativeType := a.kind.NativeType
	if strings.HasPrefix(nativeType, "compute.googleapis.com/") || nativeType == "sqladmin.googleapis.com/Instance" {
		name := text(data["name"])
		if !segmentPattern.MatchString(name) || name == "." || name == ".." {
			return "", fmt.Errorf("Google API omitted a valid deletion operation")
		}
		parts := strings.Split(a.endpoint, "/")
		return strings.Join(parts[:len(parts)-2], "/") + "/operations/" + name, nil
	}
	if nativeType == "run.googleapis.com/Service" || nativeType == "artifactregistry.googleapis.com/Repository" {
		name := text(data["name"])
		parts := strings.Split(name, "/")
		if len(parts) != 6 || parts[0] != "projects" || (parts[1] != a.client.project && parts[1] != a.client.number) || parts[2] != "locations" || parts[4] != "operations" {
			return "", fmt.Errorf("Google API omitted a valid deletion operation")
		}
		for _, part := range parts {
			if !segmentPattern.MatchString(part) || part == "." || part == ".." {
				return "", fmt.Errorf("invalid Google deletion operation")
			}
		}
		endpoint, _ := url.Parse(a.endpoint)
		resourceScope := strings.Split(strings.Trim(endpoint.Path, "/"), "/")
		if len(resourceScope) < 5 || resourceScope[3] != "locations" || resourceScope[4] != parts[3] {
			return "", fmt.Errorf("Google deletion operation belongs to another location")
		}
		version := strings.Split(strings.TrimPrefix(endpoint.Path, "/"), "/")[0]
		return "https://" + endpoint.Host + "/" + version + "/" + name, nil
	}
	return "", nil
}
func operationError(data map[string]any, requestID string) error {
	detail := object(data["error"])
	if len(detail) == 0 {
		return nil
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage, RequestID: requestID}}
}
func (a *action) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if result.ProviderOperationID != "" {
		// Rebuild the operation from its name and the validated resource endpoint.
		// A persisted waiter URL cannot redirect credentials to another project/API.
		endpoint, _ := url.Parse(result.ProviderOperationID)
		if endpoint == nil {
			return contracts.WaitResult{}, fmt.Errorf("invalid GCP operation")
		}
		name := last(endpoint.Path)
		if a.kind.NativeType == "run.googleapis.com/Service" || a.kind.NativeType == "artifactregistry.googleapis.com/Repository" {
			name = strings.TrimPrefix(strings.TrimPrefix(endpoint.Path, "/"), strings.Split(strings.TrimPrefix(endpoint.Path, "/"), "/")[0]+"/")
		}
		expected, err := a.operationURL(map[string]any{"name": name})
		if err != nil || expected != result.ProviderOperationID {
			return contracts.WaitResult{}, fmt.Errorf("GCP operation identity mismatch")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			data := response.Data
			if failure := operationError(data, response.RequestID); failure != nil {
				return contracts.WaitResult{}, failure
			}
			done := data["done"] == true || text(data["status"]) == "DONE"
			if !done {
				return contracts.WaitResult{RetryAfter: 2 * time.Second, State: text(data["status"])}, nil
			}
		}
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
func (a *action) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	data, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: text(data["status"])}, nil
}

func protectionReason(nativeType string, data map[string]any) string {
	if data["deletionProtection"] == true || object(data["settings"])["deletionProtectionEnabled"] == true {
		return "deletion_protection_enabled"
	}
	if nativeType == "compute.googleapis.com/Instance" {
		for _, disk := range array(data["disks"]) {
			if object(disk)["autoDelete"] == true {
				return "attached_disk_auto_delete_enabled"
			}
		}
	}
	return ""
}
