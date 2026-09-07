package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	cloudControlSource       = "cloud-control"
	cloudControlHook         = "aws.cloudcontrol.resource"
	cloudControlPageLimit    = 100
	cloudControlWaitInterval = 5 * time.Second
)

type CloudControlListRequest struct {
	TypeName  string
	NextToken string
	Limit     int
}

type CloudControlResource struct {
	Identifier string `json:"identifier"`
	Properties string `json:"properties,omitempty"`
}

type CloudControlPage struct {
	RequestID string                 `json:"request_id"`
	NextToken string                 `json:"next_token,omitempty"`
	Resources []CloudControlResource `json:"resources"`
}

type CloudControlProgress struct {
	RequestToken string    `json:"request_token,omitempty"`
	Identifier   string    `json:"identifier,omitempty"`
	Status       string    `json:"status,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
	Message      string    `json:"message,omitempty"`
	RetryAfter   time.Time `json:"retry_after,omitempty"`
}

type CloudControlClient interface {
	ListResources(context.Context, CloudControlListRequest) (CloudControlPage, error)
	GetResource(context.Context, string, string) (CloudControlResource, string, error)
	DeleteResource(context.Context, string, string, string) (CloudControlProgress, string, error)
	GetResourceRequestStatus(context.Context, string) (CloudControlProgress, string, error)
}

type CloudControlInventory struct {
	client CloudControlClient
}

func NewCloudControlInventory(client CloudControlClient) *CloudControlInventory {
	return &CloudControlInventory{client: client}
}

func (i *CloudControlInventory) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if i == nil || i.client == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control client is required")
	}
	if request.ResourceKind == nil || strings.TrimSpace(request.ResourceKind.NativeType) == "" {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control inventory requires a resource kind")
	}
	if request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeGlobal {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control inventory requires a region or global scope")
	}
	limit := request.Limit
	if limit <= 0 || limit > cloudControlPageLimit {
		limit = cloudControlPageLimit
	}
	typeName := strings.TrimSpace(request.ResourceKind.NativeType)
	requestPayload := map[string]any{"TypeName": typeName, "MaxResults": limit}
	if request.Cursor != "" {
		requestPayload["NextToken"] = request.Cursor
	}
	execution.LogCloudAPIRequest(ctx, "cloudcontrol", "ListResources", rawCloudPayload(requestPayload))
	page, err := i.client.ListResources(ctx, CloudControlListRequest{TypeName: typeName, NextToken: request.Cursor, Limit: limit})
	if err != nil {
		execution.LogCloudAPIFailure(ctx, "cloudcontrol", "ListResources", err)
		return contracts.InventoryBatch{}, NormalizeError(err)
	}
	responseResources := make([]any, 0, len(page.Resources))
	batch := contracts.InventoryBatch{
		Items: make([]contracts.InventoryItem, 0, len(page.Resources)), NextCursor: page.NextToken,
		RequestID: page.RequestID, Complete: page.NextToken == "",
	}
	for _, resource := range page.Resources {
		identifier := strings.TrimSpace(resource.Identifier)
		if identifier == "" {
			continue
		}
		properties, err := cloudControlModel(resource.Properties)
		if err != nil {
			return contracts.InventoryBatch{}, fmt.Errorf("decode AWS Cloud Control resource %q: %w", identifier, err)
		}
		responseResources = append(responseResources, map[string]any{"Identifier": identifier, "Properties": properties})
		normalized := cloneAnyMap(properties)
		normalized["cloudControlIdentifier"] = identifier
		name := cloudControlName(properties, identifier)
		state := cloudControlState(properties)
		if name != "" {
			normalized["name"] = name
		}
		if state != "" {
			normalized["state"] = state
		}
		batch.Items = append(batch.Items, contracts.InventoryItem{
			NativeType: typeName, NativeID: identifier, ResourceKind: *request.ResourceKind,
			Scope: contracts.InventoryScope{
				Kind: request.Scope.Kind, NativeID: request.Scope.NativeID, Name: request.Scope.Name, Location: request.Scope.Location,
			},
			Name: name, State: state, Location: request.Scope.Location, Tags: cloudControlTags(properties),
			Normalized: normalized, Raw: map[string]any{"TypeName": typeName, "Identifier": identifier, "Properties": properties},
			NativeAliases: cloudControlAliases(properties, identifier), NetworkReferences: scalarStrings(properties),
		})
	}
	execution.LogCloudAPIResponse(ctx, "cloudcontrol", "ListResources", rawCloudPayload(map[string]any{
		"RequestId": page.RequestID, "NextToken": page.NextToken, "TypeName": typeName, "ResourceDescriptions": responseResources,
	}))
	return batch, nil
}

func cloudControlAliases(properties map[string]any, identifier string) []string {
	seen := map[string]struct{}{identifier: {}}
	for _, key := range []string{"Arn", "ARN", "ResourceArn", "ResourceARN"} {
		if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
			seen[strings.TrimSpace(value)] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloudControlModel(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]any{}, nil
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func cloneAnyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+3)
	for key, item := range value {
		result[key] = item
	}
	return result
}

func cloudControlName(properties map[string]any, identifier string) string {
	for _, key := range []string{
		"Name", "AlarmName", "AutoScalingGroupName", "BucketName", "ClusterName", "DBClusterIdentifier",
		"DBInstanceIdentifier", "DashboardName", "DomainName", "EventBusName", "FileSystemId", "FunctionName",
		"GroupName", "LogGroupName", "PipelineName", "QueueName", "RepositoryName", "RoleName", "StreamName",
		"TableName", "TopicName", "UserName", "VpcId",
	} {
		if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.HasSuffix(key, "Name") {
			continue
		}
		if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return identifier
}

func cloudControlState(properties map[string]any) string {
	for _, key := range []string{"State", "Status", "ResourceStatus", "DBClusterStatus", "DBInstanceStatus"} {
		if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !strings.HasSuffix(key, "Status") && !strings.HasSuffix(key, "State") {
			continue
		}
		if value, ok := properties[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloudControlTags(properties map[string]any) map[string]string {
	result := stringMap(properties["Tags"])
	if len(result) > 0 {
		return result
	}
	items, _ := properties["Tags"].([]any)
	for _, item := range items {
		entry, _ := item.(map[string]any)
		key, _ := entry["Key"].(string)
		value, _ := entry["Value"].(string)
		if strings.TrimSpace(key) != "" {
			result[strings.TrimSpace(key)] = value
		}
	}
	return result
}

type CloudControlAction struct {
	client CloudControlClient
}

func NewCloudControlAction(client CloudControlClient) *CloudControlAction {
	return &CloudControlAction{client: client}
}

func (a *CloudControlAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	resource, requestID, err := a.client.GetResource(ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset))
	if err != nil {
		if cloudControlNotFound(err) {
			return contracts.PreflightResult{Absent: true, Reason: "resource no longer exists", Evidence: map[string]any{"provider_request_id": requestID}}, nil
		}
		return contracts.PreflightResult{}, NormalizeError(err)
	}
	return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{
		"provider_request_id": requestID, "identifier": resource.Identifier, "type_name": request.Asset.Identity.NativeType,
	}}, nil
}

func (a *CloudControlAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ActionResult{}, err
	}
	progress, requestID, err := a.client.DeleteResource(
		ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset), request.IdempotencyKey,
	)
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	if cloudControlFailed(progress.Status) {
		if strings.EqualFold(progress.ErrorCode, "NotFound") {
			return contracts.ActionResult{
				ProviderRequestID: requestID, ProviderOperationID: progress.RequestToken,
				Data: cloudControlProgressData(progress),
			}, nil
		}
		return contracts.ActionResult{}, cloudControlProgressError(progress, requestID)
	}
	if strings.TrimSpace(progress.RequestToken) == "" && !strings.EqualFold(progress.Status, "SUCCESS") {
		return contracts.ActionResult{}, fmt.Errorf("AWS Cloud Control delete returned no operation token")
	}
	retryAfter := time.Duration(0)
	if !strings.EqualFold(progress.Status, "SUCCESS") {
		retryAfter = cloudControlRetryAfter(progress)
	}
	return contracts.ActionResult{
		ProviderRequestID: requestID, ProviderOperationID: progress.RequestToken,
		RetryAfter: retryAfter, Data: cloudControlProgressData(progress),
	}, nil
}

func (a *CloudControlAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.WaitResult{}, err
	}
	status := stringValue(result.Data["status"])
	if strings.EqualFold(status, "SUCCESS") {
		return contracts.WaitResult{Done: true, State: status, Data: result.Data}, nil
	}
	if strings.EqualFold(status, "FAILED") && strings.EqualFold(stringValue(result.Data["error_code"]), "NotFound") {
		return contracts.WaitResult{Done: true, State: "absent", Data: result.Data}, nil
	}
	token := strings.TrimSpace(result.ProviderOperationID)
	if token == "" {
		return contracts.WaitResult{}, fmt.Errorf("AWS Cloud Control wait requires an operation token")
	}
	progress, requestID, err := a.client.GetResourceRequestStatus(ctx, token)
	if err != nil {
		return contracts.WaitResult{}, NormalizeError(err)
	}
	data := cloudControlProgressData(progress)
	data["provider_request_id"] = requestID
	switch strings.ToUpper(strings.TrimSpace(progress.Status)) {
	case "SUCCESS":
		return contracts.WaitResult{Done: true, State: progress.Status, Data: data}, nil
	case "FAILED", "CANCEL_COMPLETE":
		if strings.EqualFold(progress.ErrorCode, "NotFound") {
			return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
		}
		return contracts.WaitResult{}, cloudControlProgressError(progress, requestID)
	default:
		return contracts.WaitResult{Done: false, RetryAfter: cloudControlRetryAfter(progress), State: progress.Status, Data: data}, nil
	}
}

func (a *CloudControlAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	resource, requestID, err := a.client.GetResource(ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset))
	if err != nil {
		if cloudControlNotFound(err) {
			return contracts.ReadbackResult{Exists: false, State: "absent", Data: map[string]any{"provider_request_id": requestID}}, nil
		}
		return contracts.ReadbackResult{}, NormalizeError(err)
	}
	model, modelErr := cloudControlModel(resource.Properties)
	if modelErr != nil {
		return contracts.ReadbackResult{}, fmt.Errorf("decode AWS Cloud Control readback: %w", modelErr)
	}
	return contracts.ReadbackResult{Exists: true, State: cloudControlState(model), Data: map[string]any{
		"provider_request_id": requestID, "identifier": resource.Identifier, "properties": model,
	}}, nil
}

func (a *CloudControlAction) validate(request contracts.ActionRequest) error {
	if a == nil || a.client == nil {
		return fmt.Errorf("AWS Cloud Control client is required")
	}
	if request.Asset.Identity.Provider != asset.ProviderAWS || !strings.HasPrefix(request.Asset.Identity.NativeType, "AWS::") {
		return fmt.Errorf("AWS Cloud Control action requires an AWS resource asset")
	}
	if request.Action != "delete" || cloudControlIdentifier(request.Asset) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("AWS Cloud Control delete requires a resource identifier and idempotency key")
	}
	return nil
}

func cloudControlIdentifier(value asset.Asset) string {
	if identifier, ok := value.Normalized["cloudControlIdentifier"].(string); ok && strings.TrimSpace(identifier) != "" {
		return strings.TrimSpace(identifier)
	}
	identifier := strings.TrimSpace(value.Identity.NativeID)
	if strings.HasPrefix(strings.ToLower(identifier), "arn:") {
		return ""
	}
	return identifier
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func cloudControlNotFound(err error) bool {
	normalized := NormalizeError(err)
	var providerError *contracts.ProviderCallError
	return errors.As(normalized, &providerError) && providerError.Provider.Category == execution.ErrorNotFound
}

func cloudControlFailed(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FAILED", "CANCEL_COMPLETE":
		return true
	default:
		return false
	}
}

func cloudControlProgressError(progress CloudControlProgress, requestID string) error {
	code := strings.TrimSpace(progress.ErrorCode)
	if code == "" {
		code = "ResourceOperationFailed"
	}
	message := strings.TrimSpace(progress.Message)
	if message == "" {
		message = "AWS Cloud Control resource operation failed"
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorProviderFailure, Code: code, Message: message, RequestID: requestID,
	}}
}

func cloudControlRetryAfter(progress CloudControlProgress) time.Duration {
	if progress.RetryAfter.IsZero() {
		return cloudControlWaitInterval
	}
	delay := time.Until(progress.RetryAfter)
	if delay <= 0 {
		return cloudControlWaitInterval
	}
	return delay
}

func cloudControlProgressData(progress CloudControlProgress) map[string]any {
	return map[string]any{
		"identifier": progress.Identifier, "status": progress.Status, "error_code": progress.ErrorCode,
		"status_message": progress.Message, "request_token": progress.RequestToken,
	}
}
