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
	"github.com/loomx-ai/steward/internal/provider/spec"
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
	// ResourceModel is the canonical JSON model required by list handlers
	// whose schema declares handlerSchema inputs, for example a parent ID.
	ResourceModel string
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
	UpdateResource(context.Context, CloudControlUpdateRequest) (CloudControlProgress, string, error)
}

type CloudControlUpdateRequest struct {
	TypeName      string
	Identifier    string
	PatchDocument string
	ClientToken   string
}

type CloudControlInventory struct {
	client    CloudControlClient
	plan      *CloudControlListPlan
	parents   func(context.Context) ([]cloudControlParent, error)
	accountID func(context.Context) (string, error)
}

func NewCloudControlInventory(client CloudControlClient) *CloudControlInventory {
	return &CloudControlInventory{client: client}
}

// WithPlan applies the spec-declared list request. Parent and account lookups
// are only invoked when the plan references them.
func (i *CloudControlInventory) WithPlan(plan CloudControlListPlan, parents func(context.Context) ([]cloudControlParent, error), accountID func(context.Context) (string, error)) *CloudControlInventory {
	i.plan, i.parents, i.accountID = &plan, parents, accountID
	return i
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
	plan := CloudControlListPlan{TypeName: typeName}
	if i.plan != nil {
		plan = *i.plan
	}
	if plan.TypeName != typeName {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control list plan %s does not match kind %s", plan.TypeName, typeName)
	}
	var parents []cloudControlParent
	if plan.usesParent() {
		if i.parents == nil {
			return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control kind %s requires parent discovery", typeName)
		}
		var err error
		if parents, err = i.parents(ctx); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	accountID := ""
	for _, value := range plan.Model {
		if value == "scope.accountId" {
			if i.accountID == nil {
				return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control kind %s requires the account ID", typeName)
			}
			var err error
			if accountID, err = i.accountID(ctx); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
	}
	variants, err := plan.variants(parents, request.Scope, accountID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if len(variants) == 0 {
		return contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}, nil
	}
	fingerprint := variantFingerprint(typeName, variants)
	cursor, err := decodeCloudControlCursor(request.Cursor, variants, fingerprint)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	variant := variants[cursor.Variant]
	requestPayload := map[string]any{"TypeName": typeName, "MaxResults": limit}
	if cursor.Token != "" {
		requestPayload["NextToken"] = cursor.Token
	}
	if variant.Model != "" {
		requestPayload["ResourceModel"] = variant.Model
	}
	execution.LogCloudAPIRequest(ctx, "cloudcontrol", "ListResources", rawCloudPayload(requestPayload))
	page, err := i.client.ListResources(ctx, CloudControlListRequest{TypeName: typeName, NextToken: cursor.Token, Limit: limit, ResourceModel: variant.Model})
	if err != nil {
		execution.LogCloudAPIFailure(ctx, "cloudcontrol", "ListResources", err)
		return contracts.InventoryBatch{}, NormalizeError(err)
	}
	if page.NextToken != "" && page.NextToken == cursor.Token {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control repeated the %s page token", typeName)
	}
	responseResources := make([]any, 0, len(page.Resources))
	batch := contracts.InventoryBatch{Items: make([]contracts.InventoryItem, 0, len(page.Resources)), RequestID: page.RequestID}
	if page.NextToken != "" {
		cursor.Token = page.NextToken
	} else {
		cursor.Variant, cursor.Token = cursor.Variant+1, ""
	}
	if cursor.Variant < len(variants) {
		batch.NextCursor = encodeCloudControlCursor(cursor, variants)
	}
	batch.Complete = batch.NextCursor == ""
	for _, resource := range page.Resources {
		identifier := strings.TrimSpace(resource.Identifier)
		if identifier == "" {
			return contracts.InventoryBatch{}, fmt.Errorf("AWS Cloud Control ListResources returned an empty identifier")
		}
		item, err := cloudControlItem(resource, *request.ResourceKind, request.Scope)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		responseResources = append(responseResources, map[string]any{"Identifier": identifier, "Properties": item.Raw["Properties"]})
		batch.Items = append(batch.Items, item)
	}
	execution.LogCloudAPIResponse(ctx, "cloudcontrol", "ListResources", rawCloudPayload(map[string]any{
		"RequestId": page.RequestID, "NextToken": page.NextToken, "TypeName": typeName, "ResourceDescriptions": responseResources,
	}))
	return batch, nil
}

func cloudControlItem(resource CloudControlResource, kind asset.ResourceKind, scope asset.Scope) (contracts.InventoryItem, error) {
	identifier := strings.TrimSpace(resource.Identifier)
	properties, err := cloudControlModel(resource.Properties)
	if err != nil {
		return contracts.InventoryItem{}, fmt.Errorf("decode AWS Cloud Control resource %q: %w", identifier, err)
	}
	location := scope.Location
	if scope.Kind == asset.ScopeGlobal {
		location = ""
	} else if location == "" {
		location = scope.NativeID
	}
	name, state := cloudControlName(properties, identifier), cloudControlState(properties)
	normalized := cloneAnyMap(properties)
	normalized["cloudControlIdentifier"] = identifier
	if name != "" {
		normalized["name"] = name
	}
	if state != "" {
		normalized["state"] = state
	}
	normalizeCloudControlNetwork(normalized)
	deriveCloudControlReferences(kind.NativeType, normalized)
	item := contracts.InventoryItem{
		NativeType: kind.NativeType, NativeID: identifier, ResourceKind: kind,
		Scope: contracts.InventoryScope{Kind: scope.Kind, NativeID: scope.NativeID, Name: scope.Name, Location: location},
		Name:  name, State: state, Location: location, Tags: cloudControlTags(properties),
		Normalized: normalized, Raw: map[string]any{"TypeName": kind.NativeType, "Identifier": identifier, "Properties": properties},
		NativeAliases: cloudControlAliases(properties, identifier), NetworkReferences: cloudControlNetworkReferences(normalized),
	}
	if reason := cloudControlServiceManaged(kind.NativeType, identifier, properties); reason != "" {
		actionable := false
		item.Actionable = &actionable
		normalized["cleanup_protection_reason"] = reason
	}
	return item, nil
}

// cloudControlServiceManaged names resources that an AWS service creates and
// owns, which only that service can remove.
func cloudControlServiceManaged(nativeType, identifier string, properties map[string]any) string {
	switch nativeType {
	case "AWS::EC2::PrefixList":
		// AWS-managed prefix lists, such as those of S3 or CloudFront, are
		// owned by AWS rather than the account.
		if stringValue(properties["OwnerId"]) == "AWS" {
			return "aws_managed_prefix_list"
		}
	case "AWS::EC2::IPAMScope":
		// Default scopes are created and deleted with their IPAM.
		if properties["IsDefault"] == true {
			return "ipam_default_scope"
		}
	case "AWS::Scheduler::ScheduleGroup":
		if identifier == "default" {
			return "scheduler_default_group"
		}
	case "AWS::MSK::Topic":
		// Kafka and MSK own internal topics such as __consumer_offsets and
		// __amazon_msk_canary; the topic name is the ARN's last segment.
		if strings.HasPrefix(identifier[strings.LastIndex(identifier, "/")+1:], "__") {
			return "kafka_internal_topic"
		}
	case "AWS::ResourceGroups::Group":
		// Group names beginning with "AWS" or "aws" are reserved for groups
		// that AWS services create, such as AppRegistry application groups.
		if strings.HasPrefix(strings.ToLower(identifier), "aws") {
			return "service_managed_resource_group"
		}
	}
	return ""
}

// ListResources may return only primary identifiers. Fetch the full model before
// projecting tags, network relationships, or detailed resource capabilities.
// The inventory worker preserves the base batch if enrichment fails.
func (r *Runtime) EnrichInventoryBatch(ctx context.Context, request contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
	if request.Source != cloudControlSource || len(items) == 0 {
		return items, nil
	}
	region := cloudControlRegion(request.Scope)
	if request.ResourceKind != nil {
		region = r.cloudControlInventoryRegion(request.ResourceKind.NativeType, request.Scope)
	}
	client, err := r.CloudControl(ctx, request.ConnectionID, region)
	if err != nil {
		return nil, err
	}
	result := make([]contracts.InventoryItem, 0, len(items))
	subnetVPCs := map[string]string{}
	for _, item := range items {
		execution.LogCloudAPIRequest(ctx, "cloudcontrol", "GetResource", rawCloudPayload(map[string]any{"TypeName": item.NativeType, "Identifier": item.NativeID}))
		resource, requestID, err := client.GetResource(ctx, item.NativeType, item.NativeID)
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "cloudcontrol", "GetResource", err)
			if cloudControlNotFound(err) {
				continue // Resource was deleted between list and detail requests.
			}
			return nil, NormalizeError(err)
		}
		if resource.Identifier != item.NativeID {
			return nil, fmt.Errorf("AWS Cloud Control GetResource identifier %q does not match %q", resource.Identifier, item.NativeID)
		}
		detail, err := cloudControlItem(resource, item.ResourceKind, request.Scope)
		if err != nil {
			return nil, err
		}
		if err := enrichCloudControlNetwork(ctx, client, detail.Normalized, subnetVPCs); err != nil {
			return nil, err
		}
		if item.NativeType == "AWS::EC2::InternetGateway" {
			network, err := r.networkClient(ctx, request.ConnectionID, region)
			if err != nil {
				return nil, err
			}
			vpcs, err := network.InternetGatewayVPCs(ctx, item.NativeID)
			if err != nil {
				return nil, NormalizeError(err)
			}
			if len(vpcs) == 1 {
				detail.Normalized["vpc_id"] = vpcs[0]
			}
		}
		detail.NetworkReferences = cloudControlNetworkReferences(detail.Normalized)
		execution.LogCloudAPIResponse(ctx, "cloudcontrol", "GetResource", rawCloudPayload(map[string]any{"RequestId": requestID, "ResourceDescription": detail.Raw}))
		result = append(result, detail)
	}
	if request.ResourceKind != nil && lifecycleFactKind(request.ResourceKind.NativeType) && len(result) > 0 {
		credential, err := r.resolveCredential(ctx, request.ConnectionID)
		if err != nil {
			return nil, err
		}
		clients, err := r.factory.Native(ctx, credential, region)
		if err != nil {
			return nil, NormalizeError(err)
		}
		if err := enrichLifecycleFacts(ctx, clients, result); err != nil {
			return nil, err
		}
	}
	return result, nil
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
	if name := strings.TrimSpace(cloudControlTags(properties)["Name"]); name != "" {
		return name
	}
	for _, key := range []string{
		"Name", "AlarmName", "AutoScalingGroupName", "BucketName", "ClusterName", "DBClusterIdentifier",
		"DBInstanceIdentifier", "DashboardName", "DomainName", "EventBusName", "FileSystemId", "FunctionName",
		"GroupName", "LogGroupName", "PipelineName", "QueueName", "RepositoryName", "RoleName", "StreamName",
		"TableName", "TopicName", "UserName",
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
	client     CloudControlClient
	protection *cloudControlProtection
}

func NewCloudControlAction(client CloudControlClient) *CloudControlAction {
	return &CloudControlAction{client: client}
}

// NewCloudControlActionForSpec applies the kind's reviewed delete action rules.
func NewCloudControlActionForSpec(client CloudControlClient, nativeType string, action spec.ActionSpec) (*CloudControlAction, error) {
	protection, err := cloudControlProtectionFromSpec(nativeType, action)
	if err != nil {
		return nil, err
	}
	return &CloudControlAction{client: client, protection: protection}, nil
}

func (*CloudControlAction) DeletionCheckTimeout() time.Duration { return time.Hour }

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
	evidence := map[string]any{
		"provider_request_id": requestID, "identifier": resource.Identifier, "type_name": request.Asset.Identity.NativeType,
	}
	if a.protection != nil {
		model, err := cloudControlModel(resource.Properties)
		if err != nil {
			return contracts.PreflightResult{}, fmt.Errorf("decode AWS Cloud Control preflight model: %w", err)
		}
		enabled, _, err := a.protection.evaluate(model)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		evidence["deletion_protection"] = enabled
		if enabled {
			evidence["pre_delete_action"] = cloudControlPhaseDisableProtection
		}
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *CloudControlAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if a.protection != nil {
		resource, requestID, err := a.client.GetResource(ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset))
		if err != nil {
			if cloudControlNotFound(err) {
				return contracts.ActionResult{ProviderRequestID: requestID, Data: map[string]any{"phase": cloudControlPhaseDelete, "status": "FAILED", "error_code": "NotFound"}}, nil
			}
			return contracts.ActionResult{}, NormalizeError(err)
		}
		model, err := cloudControlModel(resource.Properties)
		if err != nil {
			return contracts.ActionResult{}, fmt.Errorf("decode AWS Cloud Control model: %w", err)
		}
		enabled, patch, err := a.protection.evaluate(model)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if enabled {
			return a.disableProtection(ctx, request, patch)
		}
	}
	return a.submitDelete(ctx, request)
}

func (a *CloudControlAction) disableProtection(ctx context.Context, request contracts.ActionRequest, patch string) (contracts.ActionResult, error) {
	payload := map[string]any{"TypeName": request.Asset.Identity.NativeType, "Identifier": cloudControlIdentifier(request.Asset), "PatchDocument": patch}
	execution.LogCloudAPIRequest(ctx, "cloudcontrol", "UpdateResource", rawCloudPayload(payload))
	progress, requestID, err := a.client.UpdateResource(ctx, CloudControlUpdateRequest{
		TypeName: request.Asset.Identity.NativeType, Identifier: cloudControlIdentifier(request.Asset),
		PatchDocument: patch, ClientToken: request.IdempotencyKey + ":disable-deletion-protection",
	})
	if err != nil {
		execution.LogCloudAPIFailure(ctx, "cloudcontrol", "UpdateResource", err)
		return contracts.ActionResult{}, NormalizeError(err)
	}
	if cloudControlFailed(progress.Status) {
		return contracts.ActionResult{}, cloudControlProgressError(progress, requestID)
	}
	data := cloudControlProgressData(progress)
	data["phase"] = cloudControlPhaseDisableProtection
	data["patch_document"] = patch
	return contracts.ActionResult{ProviderRequestID: requestID, ProviderOperationID: progress.RequestToken, RetryAfter: cloudControlRetryAfter(progress), Data: data}, nil
}

func (a *CloudControlAction) submitDelete(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	progress, requestID, err := a.client.DeleteResource(
		ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset), request.IdempotencyKey,
	)
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	if cloudControlFailed(progress.Status) {
		if strings.EqualFold(progress.ErrorCode, "NotFound") {
			data := cloudControlProgressData(progress)
			data["phase"] = cloudControlPhaseDelete
			return contracts.ActionResult{ProviderRequestID: requestID, ProviderOperationID: progress.RequestToken, Data: data}, nil
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
	data := cloudControlProgressData(progress)
	data["phase"] = cloudControlPhaseDelete
	return contracts.ActionResult{
		ProviderRequestID: requestID, ProviderOperationID: progress.RequestToken,
		RetryAfter: retryAfter, Data: data,
	}, nil
}

func (a *CloudControlAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.WaitResult{}, err
	}
	phase := stringValue(result.Data["phase"])
	status := stringValue(result.Data["status"])
	token := strings.TrimSpace(stringValue(result.Data["request_token"]))
	if token == "" {
		token = strings.TrimSpace(result.ProviderOperationID)
	}
	if phase != cloudControlPhaseDisableProtection {
		if strings.EqualFold(status, "SUCCESS") {
			return contracts.WaitResult{Done: true, State: status, Data: result.Data}, nil
		}
		if strings.EqualFold(status, "FAILED") && strings.EqualFold(stringValue(result.Data["error_code"]), "NotFound") {
			return contracts.WaitResult{Done: true, State: "absent", Data: result.Data}, nil
		}
	}
	if token == "" {
		return contracts.WaitResult{}, fmt.Errorf("AWS Cloud Control wait requires an operation token")
	}
	progress, requestID, err := a.client.GetResourceRequestStatus(ctx, token)
	if err != nil {
		return contracts.WaitResult{}, NormalizeError(err)
	}
	if progress.RequestToken != "" && progress.RequestToken != token {
		return contracts.WaitResult{}, fmt.Errorf("AWS Cloud Control returned status for another operation")
	}
	data := cloudControlProgressData(progress)
	data["provider_request_id"] = requestID
	data["request_token"] = token
	if phase == cloudControlPhaseDisableProtection {
		data["phase"] = phase
		switch strings.ToUpper(strings.TrimSpace(progress.Status)) {
		case "SUCCESS":
			return a.afterProtectionDisabled(ctx, request, data)
		case "FAILED", "CANCEL_COMPLETE":
			if strings.EqualFold(progress.ErrorCode, "NotFound") {
				return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
			}
			return contracts.WaitResult{}, cloudControlProgressError(progress, requestID)
		default:
			return contracts.WaitResult{Done: false, RetryAfter: cloudControlRetryAfter(progress), State: "disabling_deletion_protection", Data: data}, nil
		}
	}
	data["phase"] = cloudControlPhaseDelete
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

// The protection update is only a preparation step: read the model again and
// submit the delete only after the live resource reports protection disabled.
func (a *CloudControlAction) afterProtectionDisabled(ctx context.Context, request contracts.ActionRequest, data map[string]any) (contracts.WaitResult, error) {
	resource, _, err := a.client.GetResource(ctx, request.Asset.Identity.NativeType, cloudControlIdentifier(request.Asset))
	if err != nil {
		if cloudControlNotFound(err) {
			return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
		}
		return contracts.WaitResult{}, NormalizeError(err)
	}
	model, err := cloudControlModel(resource.Properties)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if enabled, _, err := a.protection.evaluate(model); err != nil {
		return contracts.WaitResult{}, err
	} else if enabled {
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorConflict, Code: "DeletionProtectionStillEnabled",
			Message: "AWS resource still reports deletion protection after the update completed",
		}}
	}
	deleted, err := a.submitDelete(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	deleted.Data["protection_request_token"] = data["request_token"]
	deleted.Data["provider_request_id"] = deleted.ProviderRequestID
	retry := deleted.RetryAfter
	if retry <= 0 {
		retry = cloudControlWaitInterval
	}
	return contracts.WaitResult{Done: false, RetryAfter: retry, State: "deleting", Data: deleted.Data}, nil
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
		Category: cloudControlHandlerCategory(code, message), Code: code, Message: message, RequestID: requestID,
	}}
}

// Cloud Control reports handler failures on a finished request. A NotFound
// delete lets the worker confirm absence by readback; the other codes are
// final for this request token, so none of them is classified as retryable.
func cloudControlHandlerCategory(code, message string) execution.ErrorCategory {
	lower := strings.ToLower(message)
	switch code {
	case "NotFound":
		return execution.ErrorNotFound
	case "AccessDenied", "InvalidCredentials":
		return execution.ErrorPermissionDenied
	case "InvalidRequest", "NotUpdatable":
		return execution.ErrorInvalidRequest
	case "ResourceConflict":
		if strings.Contains(lower, "dependencyviolation") || strings.Contains(lower, "dependent object") || strings.Contains(lower, "in use") || strings.Contains(lower, "has dependencies") {
			return execution.ErrorDependencyViolation
		}
		return execution.ErrorConflict
	}
	return execution.ErrorProviderFailure
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
