package gcp

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) metricsActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || a.kind.NativeType != monitoredProjectType || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("metrics_scope_action_changed")
	}
	if _, _, err := a.client.metricsOperation(a.kind.NativeType, a.identity.NativeID, "DELETE"); err != nil {
		return err
	}
	if err := a.client.metricsIdentity(a.kind.NativeType, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return err
	}
	for _, key := range []string{"createTime", metricsParentTime} {
		if _, err := time.Parse(time.RFC3339Nano, text(request.Asset.Normalized[key])); err != nil {
			return groupDenied("metrics_scope_plan_time_missing")
		}
	}
	if value, present := request.Asset.Normalized["isTombstoned"]; present {
		if _, ok := value.(bool); !ok {
			return groupDenied("metrics_scope_plan_tombstone_invalid")
		}
	}
	return nil
}

func (a *action) metricsReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.metricsActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	parent := strings.Join(strings.Split(a.identity.NativeID, "/")[:7], "/")
	var exists bool
	for pass := 0; pass < 2; pass++ {
		response, err := a.client.metricsRead(ctx, parent)
		// Scope failure/404 is not evidence that its monitored-project link is gone.
		if err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
		if response.Data["createTime"] != request.Asset.Normalized[metricsParentTime] {
			return contracts.ReadbackResult{}, groupDenied("metrics_scope_parent_recreated")
		}
		members, _ := a.client.metricsMembers(parent, response.Data)
		live := members[a.identity.NativeID]
		if pass == 1 && exists != (live != nil) {
			return contracts.ReadbackResult{}, groupDenied("metrics_scope_link_visibility_changed")
		}
		exists = live != nil
		if live != nil && (live["createTime"] != request.Asset.Normalized["createTime"] || (live["isTombstoned"] == true) != (request.Asset.Normalized["isTombstoned"] == true)) {
			return contracts.ReadbackResult{}, groupDenied("metrics_scope_link_recreated_or_changed")
		}
	}
	return contracts.ReadbackResult{Exists: exists}, nil
}

func (a *action) metricsPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.metricsReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func metricsOperationURL(data map[string]any, requestID string) (string, error) {
	name, ok := data["name"].(string)
	p := strings.Split(name, "/")
	if !ok || len(p) != 2 || p[0] != "operations" || !segmentPattern.MatchString(p[1]) || p[1] == "." || p[1] == ".." {
		return "", groupDenied("metrics_scope_operation_name_invalid")
	}
	if done, present := data["done"]; present {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("metrics_scope_operation_done_invalid")
		}
	}
	if _, present := data["error"]; present {
		if failure := operationError(data, requestID); failure != nil {
			return "", failure
		}
		return "", groupDenied("metrics_scope_operation_error_invalid")
	}
	if value, present := data["metadata"]; present {
		metadata, ok := value.(map[string]any)
		if !ok {
			return "", groupDenied("metrics_scope_operation_metadata_invalid")
		}
		if value, present := metadata["@type"]; present && value != "type.googleapis.com/google.monitoring.metricsscope.v1.OperationMetadata" {
			return "", groupDenied("metrics_scope_operation_type_changed")
		}
		if state, present := metadata["state"]; present {
			if !slices.Contains([]string{"CREATED", "RUNNING", "DONE"}, text(state)) || (state == "DONE" && data["done"] != true) || (state != "DONE" && data["done"] == true) {
				return "", groupDenied("metrics_scope_operation_state_invalid")
			}
		}
	}
	if value, present := data["response"]; present {
		response, ok := value.(map[string]any)
		if !ok || data["done"] != true {
			return "", groupDenied("metrics_scope_operation_response_invalid")
		}
		if value, present := response["@type"]; present && value != "type.googleapis.com/google.protobuf.Empty" {
			return "", groupDenied("metrics_scope_operation_response_changed")
		}
	}
	return "https://" + metricsHost + "/v1/" + name, nil
}

func (a *action) executeMetricsScope(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	bound, err := catalog.BindREST(a.deleteOperation, a.deleteParameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, checkErr := a.metricsReadback(ctx, request)
		if checkErr != nil {
			return contracts.ActionResult{}, checkErr
		}
		if !read.Exists {
			return contracts.ActionResult{}, nil
		}
		return contracts.ActionResult{}, groupDenied("metrics_scope_delete_not_observed")
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := metricsOperationURL(response.Data, response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: response.RequestID, RetryAfter: 2 * time.Second, Data: map[string]any{
		"phase": "metrics_scope_unlink", "resource": a.identity.NativeID, "create_time": request.Asset.Normalized["createTime"], "parent_create_time": request.Asset.Normalized[metricsParentTime], "tombstoned": request.Asset.Normalized["isTombstoned"] == true, "operation": operation,
	}}, nil
}

func (a *action) waitMetricsScope(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.metricsActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if len(result.Data) == 0 && result.ProviderOperationID == "" {
		read, err := a.metricsReadback(ctx, request)
		return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
	}
	data := result.Data
	if data["phase"] != "metrics_scope_unlink" || data["resource"] != a.identity.NativeID || data["create_time"] != request.Asset.Normalized["createTime"] || data["parent_create_time"] != request.Asset.Normalized[metricsParentTime] || data["tombstoned"] != (request.Asset.Normalized["isTombstoned"] == true) || data["operation"] != result.ProviderOperationID || result.ProviderOperationID == "" {
		return contracts.WaitResult{}, groupDenied("metrics_scope_phase_changed")
	}
	u, err := url.Parse(result.ProviderOperationID)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return contracts.WaitResult{}, groupDenied("metrics_scope_operation_url_invalid")
	}
	name := strings.TrimPrefix(u.Path, "/v1/")
	expected, err := metricsOperationURL(map[string]any{"name": name}, "")
	if err != nil || expected != result.ProviderOperationID {
		return contracts.WaitResult{}, groupDenied("metrics_scope_operation_url_changed")
	}
	response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
	if err != nil && !isNotFound(err) {
		return contracts.WaitResult{}, err
	}
	pending := false
	if err == nil {
		actual, err := metricsOperationURL(response.Data, response.RequestID)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if actual != expected {
			return contracts.WaitResult{}, groupDenied("metrics_scope_operation_changed")
		}
		pending = response.Data["done"] != true
	}
	read, err := a.metricsReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !pending && !read.Exists, State: "metrics_scope_unlink", RetryAfter: 2 * time.Second}, err
}
