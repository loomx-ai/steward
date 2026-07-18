package alicloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	ResourceCenterPageLimit         = 500
	ResourceConfigurationBatchLimit = 100
)

type SearchRequest struct {
	NextToken        string
	MaxResults       int
	RegionID         string
	ResourceTypes    []string
	SearchExpression string
	VpcID            string
	VSwitchID        string
}

type ResourceTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type ResourceRecord struct {
	AccountID           string               `json:"AccountId"`
	ResourceGroupID     string               `json:"ResourceGroupId"`
	ResourceID          string               `json:"ResourceId"`
	ResourceName        string               `json:"ResourceName"`
	ResourceType        string               `json:"ResourceType"`
	RegionID            string               `json:"RegionId"`
	ZoneID              string               `json:"ZoneId"`
	CreateTime          string               `json:"CreateTime"`
	ExpireTime          string               `json:"ExpireTime"`
	Deleted             bool                 `json:"Deleted"`
	Tags                []ResourceTag        `json:"Tags"`
	Configuration       map[string]any       `json:"Configuration,omitempty"`
	IPAddresses         []string             `json:"IpAddresses,omitempty"`
	IPAddressAttributes []IPAddressAttribute `json:"IpAddressAttributes,omitempty"`
}

type IPAddressAttribute struct {
	IPAddress   string `json:"IpAddress"`
	NetworkType string `json:"NetworkType"`
	Version     string `json:"Version"`
}

type ResourcePage struct {
	RequestID   string           `json:"RequestId"`
	NextToken   string           `json:"NextToken"`
	Resources   []ResourceRecord `json:"Resources"`
	RawResponse map[string]any   `json:"-"`
}

type ResourceConfigurationRequest struct {
	Resources []ResourceConfigurationReference `json:"Resources"`
}

type ResourceConfigurationReference struct {
	RegionID     string `json:"RegionId"`
	ResourceID   string `json:"ResourceId"`
	ResourceType string `json:"ResourceType"`
}

type ResourceConfigurationPage struct {
	RequestID   string           `json:"RequestId"`
	Resources   []ResourceRecord `json:"Resources"`
	RawResponse map[string]any   `json:"-"`
}

type ResourceCenterClient interface {
	SearchResources(context.Context, SearchRequest) (ResourcePage, error)
}

type ResourceConfigurationClient interface {
	BatchGetResourceConfigurations(context.Context, ResourceConfigurationRequest) (ResourceConfigurationPage, error)
}

type Inventory struct {
	client        ResourceCenterClient
	resourceTypes []string
	allowedTypes  map[string]struct{}
}

func NewInventory(client ResourceCenterClient, resourceTypes []string) *Inventory {
	allowed := make(map[string]struct{}, len(resourceTypes))
	for _, resourceType := range resourceTypes {
		resourceType = strings.TrimSpace(resourceType)
		if resourceType != "" {
			allowed[resourceType] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(allowed))
	for resourceType := range allowed {
		normalized = append(normalized, resourceType)
	}
	sort.Strings(normalized)
	return &Inventory{client: client, resourceTypes: normalized, allowedTypes: allowed}
}

func (i *Inventory) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if i.client == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud Resource Center client is required")
	}
	limit := ResourceCenterPageLimit
	resourceTypes, err := requestedNativeTypes(request.ResourceKind, i.resourceTypes, i.allowedTypes)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	filters := make([]any, 0, 3)
	regionFilter := resourceCenterRegionFilter(request.Scope)
	if regionFilter != "" {
		filters = append(filters, map[string]any{
			"Key": "RegionId", "MatchType": "Equals", "Value": []string{regionFilter},
		})
	}
	filters = append(filters, map[string]any{
		"Key": "ResourceType", "MatchType": "Equals", "Value": resourceTypes,
	})
	requestPayload := map[string]any{
		"Filter": filters, "MaxResults": limit, "IncludeDeletedResources": false,
	}
	vpcID := stringOption(request.Options, "vpc_id")
	vSwitchID := stringOption(request.Options, "vswitch_id")
	if target := request.NetworkTarget; target != nil {
		switch target.Kind {
		case asset.ScanTargetVPC:
			vpcID = strings.TrimSpace(target.NativeID)
		case asset.ScanTargetVSwitch:
			vSwitchID = strings.TrimSpace(target.NativeID)
		}
	}
	if vpcID != "" {
		filters = append(filters, map[string]any{
			"Key": "VpcId", "MatchType": "Equals", "Value": []string{vpcID},
		})
	}
	if vSwitchID != "" {
		filters = append(filters, map[string]any{
			"Key": "VSwitchId", "MatchType": "Equals", "Value": []string{vSwitchID},
		})
	}
	if vpcID != "" || vSwitchID != "" {
		requestPayload["Filter"] = filters
	}
	if expression, _ := request.Options["search_expression"].(string); strings.TrimSpace(expression) != "" {
		requestPayload["SearchExpression"] = strings.TrimSpace(expression)
	}
	if request.Cursor != "" {
		requestPayload["NextToken"] = request.Cursor
	}
	execution.LogCloudAPIRequest(ctx, "resource-center", "SearchResources", rawCloudPayload(requestPayload))
	page, err := i.client.SearchResources(ctx, SearchRequest{
		NextToken: request.Cursor, MaxResults: limit, RegionID: resourceCenterRegionFilter(request.Scope),
		ResourceTypes:    resourceTypes,
		SearchExpression: stringOption(request.Options, "search_expression"),
		VpcID:            vpcID,
		VSwitchID:        vSwitchID,
	})
	if err != nil {
		LogCloudAPIError(ctx, "resource-center", "SearchResources", err)
		normalized := NormalizeError(err)
		return contracts.InventoryBatch{}, normalized
	}
	responsePayload := page.RawResponse
	if responsePayload == nil {
		responsePayload = map[string]any{
			"RequestId": page.RequestID, "NextToken": page.NextToken, "Resources": page.Resources,
		}
	}
	batch := contracts.InventoryBatch{
		Items: make([]contracts.InventoryItem, 0, len(page.Resources)), NextCursor: page.NextToken,
		RequestID: page.RequestID, Complete: page.NextToken == "",
	}
	ignoredTypes := make(map[string]struct{})
	indexedResources := make([]ResourceRecord, 0, len(page.Resources))
	for _, record := range page.Resources {
		if strings.TrimSpace(record.ResourceType) == "" || strings.TrimSpace(record.ResourceID) == "" {
			continue
		}
		if _, allowed := i.allowedTypes[record.ResourceType]; !allowed {
			ignoredTypes[record.ResourceType] = struct{}{}
			continue
		}
		indexedResources = append(indexedResources, record)
	}
	if len(ignoredTypes) > 0 {
		ignored := make([]string, 0, len(ignoredTypes))
		for resourceType := range ignoredTypes {
			ignored = append(ignored, resourceType)
		}
		sort.Strings(ignored)
		responsePayload["ignored_resource_count"] = len(ignoredTypes)
		responsePayload["ignored_resource_types"] = ignored
	}
	execution.LogCloudAPIResponse(ctx, "resource-center", "SearchResources", rawCloudPayload(responsePayload))
	configurations, err := i.resourceConfigurations(ctx, indexedResources)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	for _, indexed := range indexedResources {
		configurationKey := resourceConfigurationKey(
			indexed.RegionID,
			indexed.ResourceType,
			indexed.ResourceID,
		)
		configured, exists := configurations[configurationKey]
		if !exists {
			continue
		}
		record := mergeResourceConfiguration(
			indexed,
			configured,
		)
		tags := make(map[string]string, len(record.Tags))
		for _, tag := range record.Tags {
			tags[tag.Key] = tag.Value
		}
		raw, err := recordMap(record)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		normalized := map[string]any{
			"accountId": record.AccountID, "resourceGroupId": record.ResourceGroupID,
			"createTime": record.CreateTime, "expireTime": record.ExpireTime,
			"zoneId": record.ZoneID, "deleted": record.Deleted,
			"configuration": record.Configuration, "_inventory_source": "resource-center",
		}
		if record.ResourceType == "ACS::VPC::VPC" {
			normalized["vpc_id"] = record.ResourceID
		}
		location := record.RegionID
		if request.Scope.Kind == asset.ScopeGlobal {
			location = request.Scope.Location
		}
		batch.Items = append(batch.Items, contracts.InventoryItem{
			NativeType: record.ResourceType, NativeID: record.ResourceID, Name: record.ResourceName,
			Scope: contracts.InventoryScope{
				Kind: request.Scope.Kind, NativeID: request.Scope.NativeID,
				Name: request.Scope.Name, Location: request.Scope.Location,
			},
			Location: location, Tags: tags, Normalized: normalized, Raw: raw,
			NativeAliases: []string{record.ResourceID},
			NetworkReferences: appendUniqueReferences(
				tagReferences(record.Tags),
				scalarConfigurationReferences(record.Configuration, nil),
			),
			ResourceKind: asset.ResourceKind{
				ID:       asset.ResourceKindID(string(asset.ProviderAliCloud) + ":" + record.ResourceType),
				Provider: asset.ProviderAliCloud, NativeType: record.ResourceType, DisplayName: record.ResourceType,
				ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
				BundleRevision: "resource-center",
			},
		})
	}
	return batch, nil
}

func (i *Inventory) resourceConfigurations(
	ctx context.Context,
	resources []ResourceRecord,
) (map[string]ResourceRecord, error) {
	if len(resources) == 0 {
		return map[string]ResourceRecord{}, nil
	}
	client, ok := i.client.(ResourceConfigurationClient)
	if !ok {
		return nil, fmt.Errorf("Alibaba Cloud Resource Center client does not support BatchGetResourceConfigurations")
	}
	result := make(map[string]ResourceRecord, len(resources))
	for start := 0; start < len(resources); start += ResourceConfigurationBatchLimit {
		end := start + ResourceConfigurationBatchLimit
		if end > len(resources) {
			end = len(resources)
		}
		request := ResourceConfigurationRequest{
			Resources: make([]ResourceConfigurationReference, 0, end-start),
		}
		for _, resource := range resources[start:end] {
			request.Resources = append(request.Resources, ResourceConfigurationReference{
				RegionID: resource.RegionID, ResourceID: resource.ResourceID, ResourceType: resource.ResourceType,
			})
		}
		execution.LogCloudAPIRequest(ctx, "resource-center", "BatchGetResourceConfigurations", rawCloudPayload(request))
		page, err := client.BatchGetResourceConfigurations(ctx, request)
		if err != nil {
			LogCloudAPIError(ctx, "resource-center", "BatchGetResourceConfigurations", err)
			return nil, NormalizeError(err)
		}
		responsePayload := page.RawResponse
		if responsePayload == nil {
			responsePayload = map[string]any{"RequestId": page.RequestID, "Resources": page.Resources}
		}
		execution.LogCloudAPIResponse(ctx, "resource-center", "BatchGetResourceConfigurations", rawCloudPayload(responsePayload))
		for _, resource := range page.Resources {
			result[resourceConfigurationKey(resource.RegionID, resource.ResourceType, resource.ResourceID)] = resource
		}
	}
	for _, resource := range resources {
		key := resourceConfigurationKey(resource.RegionID, resource.ResourceType, resource.ResourceID)
		if _, exists := result[key]; !exists {
			execution.LogJob(ctx, "warn", fmt.Sprintf(
				"resource-center BatchGetResourceConfigurations omitted %s %s in %s; treating the resource as deleted during the scan",
				resource.ResourceType,
				resource.ResourceID,
				resource.RegionID,
			))
		}
	}
	return result, nil
}

func resourceConfigurationKey(regionID, resourceType, resourceID string) string {
	return strings.TrimSpace(regionID) + "\x00" + strings.TrimSpace(resourceType) + "\x00" + strings.TrimSpace(resourceID)
}

func mergeResourceConfiguration(indexed, configured ResourceRecord) ResourceRecord {
	result := configured
	if result.AccountID == "" {
		result.AccountID = indexed.AccountID
	}
	if result.ResourceGroupID == "" {
		result.ResourceGroupID = indexed.ResourceGroupID
	}
	if result.ResourceID == "" {
		result.ResourceID = indexed.ResourceID
	}
	if result.ResourceName == "" {
		result.ResourceName = indexed.ResourceName
	}
	if result.ResourceType == "" {
		result.ResourceType = indexed.ResourceType
	}
	if result.RegionID == "" {
		result.RegionID = indexed.RegionID
	}
	if result.ZoneID == "" {
		result.ZoneID = indexed.ZoneID
	}
	if result.CreateTime == "" {
		result.CreateTime = indexed.CreateTime
	}
	if result.ExpireTime == "" {
		result.ExpireTime = indexed.ExpireTime
	}
	if len(result.Tags) == 0 {
		result.Tags = indexed.Tags
	}
	result.Deleted = indexed.Deleted
	return result
}

func resourceCenterRegionFilter(scope asset.Scope) string {
	if scope.Kind == asset.ScopeGlobal {
		return ""
	}
	return strings.TrimSpace(scope.NativeID)
}

func stringOption(options map[string]any, key string) string {
	value, _ := options[key].(string)
	return strings.TrimSpace(value)
}

func scalarConfigurationReferences(value any, result []string) []string {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			result = append(result, text)
		}
	case []string:
		for _, item := range typed {
			result = scalarConfigurationReferences(item, result)
		}
	case []any:
		for _, item := range typed {
			result = scalarConfigurationReferences(item, result)
		}
	case map[string]string:
		for _, item := range typed {
			result = scalarConfigurationReferences(item, result)
		}
	case map[string]any:
		for _, item := range typed {
			result = scalarConfigurationReferences(item, result)
		}
	}
	return result
}

func appendUniqueReferences(groups ...[]string) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, value := range group {
			if value = strings.TrimSpace(value); value != "" {
				seen[value] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func tagReferences(tags []ResourceTag) []string {
	seen := map[string]struct{}{}
	for _, tag := range tags {
		value := strings.TrimSpace(tag.Value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func requestedNativeTypes(
	kind *asset.ResourceKind,
	all []string,
	allowed map[string]struct{},
) ([]string, error) {
	if kind == nil {
		if len(all) == 0 {
			return nil, fmt.Errorf("Alibaba Cloud instance resource catalog is empty")
		}
		return append([]string(nil), all...), nil
	}
	nativeType := strings.TrimSpace(kind.NativeType)
	if _, exists := allowed[nativeType]; !exists {
		return nil, fmt.Errorf("Alibaba Cloud resource type %q is not an instance resource", nativeType)
	}
	return []string{nativeType}, nil
}

type APIError struct {
	Code       string
	Message    string
	RequestID  string
	StatusCode int
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e == nil {
		return "Alibaba Cloud API call failed"
	}
	return e.Code + ": " + e.Message
}

func NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var normalized *contracts.ProviderCallError
	if errors.As(err, &normalized) {
		return err
	}
	providerError := execution.ProviderError{
		Category: execution.ErrorProviderFailure,
		Message:  sanitizeProviderMessage(err.Error()),
	}
	retryAfter := time.Duration(0)
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsNotFound {
		// DNS lookup failures are transport failures, not authoritative proof
		// that a regional product endpoint is unsupported. Classifying them as
		// retryable lets cleanup readback recover when resolution returns.
		providerError.Category = execution.ErrorRetryable
		return &contracts.ProviderCallError{Provider: providerError, Cause: err}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		providerError.Category = execution.ErrorRetryable
	}
	var apiError *APIError
	if !errors.As(err, &apiError) {
		apiError = sdkAPIError(err)
	}
	if apiError != nil {
		providerError.Code = apiError.Code
		providerError.Message = sanitizeProviderMessage(apiError.Message)
		providerError.RequestID = apiError.RequestID
		retryAfter = apiError.RetryAfter
		code := strings.ToLower(apiError.Code)
		message := strings.ToLower(apiError.Message)
		switch {
		case code == "operationunsupported" || code == "unsupportedoperation" ||
			code == "unsupportedhttpmethod" || code == "invalidaction" ||
			strings.HasPrefix(code, "invalidaction.") ||
			code == "dcdnipaservicenotfound" ||
			(code == "illegal_request" && strings.Contains(message, "tenant id is empty")) ||
			apiError.StatusCode == http.StatusMethodNotAllowed:
			providerError.Category = execution.ErrorUnsupported
			providerError.Summary = map[string]any{"skip_reason": string(asset.SkipProductUnsupported)}
		case code == "invalidregionid.notfound" || code == "regionnotavailable" ||
			code == "invalidregion" || code == "invalidregionid" ||
			code == "unauthorizedregion" || code == "regionnotsupporterror" ||
			code == "ddosbgp.checkerror.invalidregion" ||
			code == "invalidoperation.notsupportedendpoint" ||
			strings.Contains(message, "filterregionids is illegal or not belong") ||
			(code == "invalidparameter" && strings.Contains(message, "parameter regionid:") &&
				(strings.Contains(message, "not valid") || strings.Contains(message, "invalid"))):
			providerError.Category = execution.ErrorUnsupported
			providerError.Summary = map[string]any{"skip_reason": string(asset.SkipProviderRegionUnavailable)}
		case code == "invalidoperation.resourcemanagedbycloudproduct" &&
			strings.Contains(message, `product "natgw"`):
			providerError.Category = execution.ErrorUnsupported
			providerError.Summary = map[string]any{
				"skip_reason": "delegated_to_nat_gateway",
			}
		case apiError.StatusCode == 429 || strings.Contains(code, "throttl"):
			providerError.Category = execution.ErrorThrottled
		case (code == "704203" && strings.Contains(message, "resource group status is deleted")) ||
			code == "odps-0420061" ||
			apiError.StatusCode == 404 || strings.Contains(code, "notfound") || strings.Contains(code, "not_found") ||
			(apiError.StatusCode == http.StatusBadRequest &&
				(strings.Contains(message, "does not exist") || strings.Contains(message, "不存在"))):
			providerError.Category = execution.ErrorNotFound
		case code == "operationdenied.vpcpeerexists" ||
			strings.Contains(code, "dependency") || strings.Contains(code, "inuse") || strings.Contains(code, "in_use"):
			providerError.Category = execution.ErrorDependencyViolation
		case code == "projectdeletionnotallowed" ||
			strings.Contains(code, "protected") ||
			strings.Contains(code, "deletionprotection") ||
			strings.Contains(code, "releaseprotection") ||
			strings.Contains(code, "deletionlock") ||
			strings.Contains(message, "protected from deletion") ||
			strings.Contains(message, "删除保护") ||
			strings.Contains(message, "释放保护") ||
			strings.Contains(message, "保护锁"):
			providerError.Category = execution.ErrorProtected
		case apiError.StatusCode == 403 || strings.Contains(code, "permission") || strings.Contains(code, "forbidden") || strings.Contains(code, "nopermission"):
			providerError.Category = execution.ErrorPermissionDenied
		case apiError.StatusCode == 409 || strings.Contains(code, "conflict"):
			providerError.Category = execution.ErrorConflict
		case strings.HasPrefix(code, "invalid") ||
			strings.HasPrefix(code, "illegal") ||
			strings.Contains(code, ".invalid") ||
			strings.Contains(code, ".illegal"):
			// Alibaba Cloud occasionally returns HTTP 5xx for a deterministic
			// request rejection. The semantic error code takes precedence over
			// the transport status so destructive actions do not loop forever.
			providerError.Category = execution.ErrorInvalidRequest
		case apiError.StatusCode >= 500:
			providerError.Category = execution.ErrorRetryable
		case apiError.StatusCode >= 400:
			providerError.Category = execution.ErrorInvalidRequest
		}
	}
	return &contracts.ProviderCallError{Provider: providerError, RetryAfter: retryAfter, Cause: err}
}

func sdkAPIError(err error) *APIError {
	var teaError *tea.SDKError
	if errors.As(err, &teaError) {
		data := tea.StringValue(teaError.Data)
		code := tea.StringValue(teaError.Code)
		if providerCode := codeFromErrorData(data); providerCode != "" &&
			(strings.TrimSpace(code) == "" || strings.TrimSpace(code) == "<nil>") {
			code = providerCode
		}
		message := tea.StringValue(teaError.Message)
		if providerMessage := messageFromErrorData(data); providerMessage != "" {
			message = providerMessage
		}
		return &APIError{
			Code: code, Message: message,
			StatusCode: tea.IntValue(teaError.StatusCode), RequestID: requestIDFromErrorData(data),
		}
	}
	var daraError *dara.SDKError
	if errors.As(err, &daraError) {
		data := dara.StringValue(daraError.Data)
		code := dara.StringValue(daraError.Code)
		if providerCode := codeFromErrorData(data); providerCode != "" &&
			(strings.TrimSpace(code) == "" || strings.TrimSpace(code) == "<nil>") {
			code = providerCode
		}
		message := dara.StringValue(daraError.Message)
		if providerMessage := messageFromErrorData(data); providerMessage != "" {
			message = providerMessage
		}
		return &APIError{
			Code: code, Message: message,
			StatusCode: dara.IntValue(daraError.StatusCode), RequestID: requestIDFromErrorData(data),
		}
	}
	return nil
}

func normalizeConnectionValidationError(err error) error {
	normalized := NormalizeError(err)
	var providerError *contracts.ProviderCallError
	if !errors.As(normalized, &providerError) {
		return normalized
	}
	message, requestID, ok := originalSDKValidationMessage(err)
	if !ok {
		return normalized
	}
	result := *providerError
	result.Provider.Message = sanitizeProviderMessage(message)
	if requestID != "" {
		result.Provider.RequestID = requestID
	}
	return &result
}

func originalSDKValidationMessage(err error) (string, string, bool) {
	var teaError *tea.SDKError
	if errors.As(err, &teaError) {
		return normalizedSDKMessage(
			tea.StringValue(teaError.Message),
			tea.StringValue(teaError.Data),
			tea.IntValue(teaError.StatusCode),
		)
	}
	var daraError *dara.SDKError
	if errors.As(err, &daraError) {
		return normalizedSDKMessage(
			dara.StringValue(daraError.Message),
			dara.StringValue(daraError.Data),
			dara.IntValue(daraError.StatusCode),
		)
	}
	return "", "", false
}

func normalizedSDKMessage(message, data string, statusCode int) (string, string, bool) {
	requestID := requestIDFromErrorData(data)
	if original := messageFromErrorData(data); original != "" {
		return sanitizeProviderMessage(original), requestID, true
	}
	prefix := fmt.Sprintf("code: %d, ", statusCode)
	const requestIDMarker = " request id: "
	remainder, found := strings.CutPrefix(strings.TrimSpace(message), prefix)
	if !found {
		return sanitizeProviderMessage(message), requestID, false
	}
	markerIndex := strings.LastIndex(remainder, requestIDMarker)
	if markerIndex < 0 {
		return sanitizeProviderMessage(message), requestID, false
	}
	original := strings.TrimSpace(remainder[:markerIndex])
	embeddedRequestID := strings.TrimSpace(remainder[markerIndex+len(requestIDMarker):])
	if original == "" {
		return sanitizeProviderMessage(message), requestID, false
	}
	if requestID == "" && embeddedRequestID != "" && embeddedRequestID != "<nil>" {
		requestID = embeddedRequestID
	}
	return sanitizeProviderMessage(original), requestID, true
}

func sanitizeProviderMessage(message string) string {
	for _, marker := range []string{
		"authorization",
		"x-acs-accesskey-id",
		"x-acs-security-token",
		"x-fc-security-token",
		"securitytoken",
		"accesskeysecret",
	} {
		message = redactProviderMessageValue(message, marker)
	}
	return message
}

func redactProviderMessageValue(message, marker string) string {
	const redacted = "[REDACTED]"
	cursor := 0
	for cursor < len(message) {
		index := strings.Index(strings.ToLower(message[cursor:]), marker)
		if index < 0 {
			break
		}
		index += cursor
		valueStart := index + len(marker)
		for valueStart < len(message) && (message[valueStart] == ' ' || message[valueStart] == '\t') {
			valueStart++
		}
		hasDelimiter := false
		if valueStart < len(message) && (message[valueStart] == ':' || message[valueStart] == '=') {
			hasDelimiter = true
			valueStart++
			for valueStart < len(message) && (message[valueStart] == ' ' || message[valueStart] == '\t') {
				valueStart++
			}
		}
		if valueStart < len(message) && (message[valueStart] == '\'' || message[valueStart] == '"') {
			quote := message[valueStart]
			valueEnd := strings.IndexByte(message[valueStart+1:], quote)
			if valueEnd >= 0 {
				valueEnd += valueStart + 1
				message = message[:valueStart+1] + redacted + message[valueEnd:]
				cursor = valueStart + 1 + len(redacted) + 1
				continue
			}
		}
		if hasDelimiter && valueStart < len(message) {
			valueEnd := len(message)
			lowerRemainder := strings.ToLower(message[valueStart:])
			for _, boundary := range []string{"\r", "\n", ",", ";", " request id:"} {
				if boundaryIndex := strings.Index(lowerRemainder, boundary); boundaryIndex >= 0 &&
					valueStart+boundaryIndex < valueEnd {
					valueEnd = valueStart + boundaryIndex
				}
			}
			message = message[:valueStart] + redacted + message[valueEnd:]
			cursor = valueStart + len(redacted)
			continue
		}
		cursor = valueStart
	}
	return message
}

func messageFromErrorData(data string) string {
	value, ok := decodeErrorData(data).(map[string]any)
	if !ok {
		return ""
	}
	for key, item := range value {
		if strings.EqualFold(key, "message") || strings.EqualFold(key, "errorMessage") {
			if text, ok := item.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func codeFromErrorData(data string) string {
	value, ok := decodeErrorData(data).(map[string]any)
	if !ok {
		return ""
	}
	for key, item := range value {
		if strings.EqualFold(key, "code") || strings.EqualFold(key, "errorCode") {
			if text, ok := item.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func requestIDFromErrorData(data string) string {
	return findRequestID(decodeErrorData(data))
}

func decodeErrorData(data string) any {
	var value any = strings.TrimSpace(data)
	for range 2 {
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			break
		}
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			break
		}
		value = decoded
	}
	return value
}

func findRequestID(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if normalized == "requestid" {
				if text, ok := item.(string); ok {
					return strings.TrimSpace(text)
				}
			}
		}
		for _, item := range typed {
			if requestID := findRequestID(item); requestID != "" {
				return requestID
			}
		}
	case []any:
		for _, item := range typed {
			if requestID := findRequestID(item); requestID != "" {
				return requestID
			}
		}
	}
	return ""
}

func recordMap(record ResourceRecord) (map[string]any, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode Resource Center record: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode Resource Center record: %w", err)
	}
	return result, nil
}
