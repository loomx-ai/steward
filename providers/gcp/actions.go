package gcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type action struct {
	client   *client
	kind     resourceType
	endpoint string
}

func (r *Runtime) ResolveAction(ctx context.Context, id asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok || value.Identity.Provider != asset.ProviderGCP || kind.NativeType == "container.googleapis.com/Cluster" {
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
	return &action{client: c, kind: kind, endpoint: endpoint}, nil
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
	query := url.Values{}
	if strings.HasPrefix(a.kind.NativeType, "compute.googleapis.com/") && request.IdempotencyKey != "" {
		hash := sha256.Sum256([]byte(request.IdempotencyKey))
		hash[6] = (hash[6] & 0x0f) | 0x40
		hash[8] = (hash[8] & 0x3f) | 0x80
		query.Set("requestId", fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16]))
	}
	data, err := a.client.request(ctx, "DELETE", a.endpoint, query)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := a.operationURL(data)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if operationError(data) != nil {
		return contracts.ActionResult{}, operationError(data)
	}
	return contracts.ActionResult{ProviderOperationID: operation, Data: map[string]any{"operation": operation}, RetryAfter: 2 * time.Second}, nil
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
		version := strings.Split(strings.TrimPrefix(endpoint.Path, "/"), "/")[0]
		return "https://" + endpoint.Host + "/" + version + "/" + name, nil
	}
	return "", nil
}
func operationError(data map[string]any) error {
	detail := object(data["error"])
	if len(detail) == 0 {
		return nil
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage}}
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
		data, err := a.client.request(ctx, "GET", expected, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if failure := operationError(data); failure != nil {
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
