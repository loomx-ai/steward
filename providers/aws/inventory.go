package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const ResourceExplorerPageLimit = 100

type SearchRequest struct {
	Query      string
	NextToken  string
	MaxResults int
}

type SearchResource struct {
	ARN          string         `json:"arn"`
	NativeType   string         `json:"native_type"`
	ResourceType string         `json:"resource_type"`
	Service      string         `json:"service"`
	Region       string         `json:"region,omitempty"`
	AccountID    string         `json:"account_id"`
	Name         string         `json:"name,omitempty"`
	LastReported string         `json:"last_reported_at,omitempty"`
	Properties   map[string]any `json:"properties,omitempty"`
}

type SearchPage struct {
	RequestID   string           `json:"request_id"`
	NextToken   string           `json:"next_token,omitempty"`
	Resources   []SearchResource `json:"resources"`
	RawResponse map[string]any   `json:"-"`
}

type ResourceExplorerClient interface {
	Search(context.Context, SearchRequest) (SearchPage, error)
}

type KindResolver func(nativeType string, scopeKind asset.ScopeKind) asset.ResourceKind

type Inventory struct {
	client      ResourceExplorerClient
	resolveKind KindResolver
}

func NewInventory(client ResourceExplorerClient, resolver KindResolver) *Inventory {
	if resolver == nil {
		resolver = defaultKind
	}
	return &Inventory{client: client, resolveKind: resolver}
}

func (i *Inventory) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if i == nil || i.client == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Resource Explorer client is required")
	}
	if request.Scope.Kind != asset.ScopeAccount && request.Scope.Kind != asset.ScopeOrganization && request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeGlobal {
		return contracts.InventoryBatch{}, fmt.Errorf("AWS Resource Explorer inventory requires account, organization, region, or global scope")
	}
	limit := request.Limit
	if limit <= 0 || limit > ResourceExplorerPageLimit {
		limit = ResourceExplorerPageLimit
	}
	queryParts := make([]string, 0, 2)
	if request.Scope.Kind == asset.ScopeRegion {
		queryParts = append(queryParts, "region:"+strings.TrimSpace(request.Scope.NativeID))
	} else if request.Scope.Kind == asset.ScopeGlobal {
		queryParts = append(queryParts, "region:global")
	}
	if request.ResourceKind != nil {
		queryParts = append(queryParts, "resourcetype:"+resourceExplorerType(request.ResourceKind.NativeType))
	}
	query := strings.Join(queryParts, " ")
	requestPayload := map[string]any{
		"MaxResults": limit,
	}
	if query != "" {
		requestPayload["Filters"] = map[string]any{"FilterString": query}
	}
	if request.Cursor != "" {
		requestPayload["NextToken"] = request.Cursor
	}
	execution.LogCloudAPIRequest(ctx, "resource-explorer-2", "Search", rawCloudPayload(requestPayload))
	page, err := i.client.Search(ctx, SearchRequest{Query: query, NextToken: request.Cursor, MaxResults: limit})
	if err != nil {
		execution.LogCloudAPIFailure(ctx, "resource-explorer-2", "Search", err)
		normalized := NormalizeError(err)
		return contracts.InventoryBatch{}, normalized
	}
	responsePayload := page.RawResponse
	if responsePayload == nil {
		resources := make([]any, 0, len(page.Resources))
		for _, record := range page.Resources {
			resources = append(resources, map[string]any{
				"Arn": record.ARN, "CfnResourceType": record.NativeType,
				"ResourceType": record.ResourceType, "Service": record.Service,
				"Region": record.Region, "OwningAccountId": record.AccountID,
				"Name": record.Name, "LastReportedAt": record.LastReported,
				"Properties": record.Properties,
			})
		}
		responsePayload = map[string]any{
			"RequestId": page.RequestID, "NextToken": page.NextToken, "Resources": resources,
		}
	}
	execution.LogCloudAPIResponse(ctx, "resource-explorer-2", "Search", rawCloudPayload(responsePayload))
	batch := contracts.InventoryBatch{
		Items: make([]contracts.InventoryItem, 0, len(page.Resources)), NextCursor: page.NextToken,
		RequestID: page.RequestID, Complete: page.NextToken == "",
	}
	for _, record := range page.Resources {
		nativeType := strings.TrimSpace(record.NativeType)
		if nativeType == "" {
			nativeType = strings.TrimSpace(record.ResourceType)
		}
		if nativeType == "" || strings.TrimSpace(record.ARN) == "" {
			continue
		}
		scope := resourceScope(record)
		raw, err := snapshot(record)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		state, _ := record.Properties["state"].(string)
		batch.Items = append(batch.Items, contracts.InventoryItem{
			NativeType: nativeType, NativeID: record.ARN, ResourceKind: i.resolveKind(nativeType, scope.Kind), Scope: scope,
			Name: record.Name, State: state, Location: record.Region, Tags: stringMap(record.Properties["tags"]),
			NativeAliases: []string{physicalResourceID(record.ARN)}, NetworkReferences: scalarStrings(record.Properties),
			Normalized: map[string]any{
				"arn": record.ARN, "accountId": record.AccountID, "service": record.Service,
				"resourceType": record.ResourceType, "physicalId": physicalResourceID(record.ARN), "lastReportedAt": record.LastReported,
			},
			Raw: raw,
		})
	}
	return batch, nil
}

func rawCloudPayload(value any) map[string]any {
	payload, err := contracts.CloudRawPayload(value)
	if err != nil {
		payload = map[string]any{"StewardLogError": err.Error()}
	}
	return payload
}

func scalarStrings(value any) []string {
	seen := map[string]struct{}{}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				seen[text] = struct{}{}
			}
		case []string:
			for _, item := range typed {
				visit(item)
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]string:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	visit(value)
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func resourceExplorerType(nativeType string) string {
	parts := strings.Split(strings.TrimSpace(nativeType), "::")
	if len(parts) == 3 && strings.EqualFold(parts[0], "AWS") {
		return strings.ToLower(parts[1] + ":" + parts[2])
	}
	return strings.ToLower(strings.TrimSpace(nativeType))
}

func physicalResourceID(arn string) string {
	parts := strings.SplitN(strings.TrimSpace(arn), ":", 6)
	if len(parts) != 6 {
		return strings.TrimSpace(arn)
	}
	resource := strings.Trim(parts[5], "/")
	if separator := strings.LastIndexAny(resource, "/:"); separator >= 0 && separator+1 < len(resource) {
		return resource[separator+1:]
	}
	return resource
}

func resourceScope(record SearchResource) contracts.InventoryScope {
	region := strings.TrimSpace(record.Region)
	if region == "" || strings.EqualFold(region, "global") {
		return contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: record.AccountID + "/global", Name: "Global"}
	}
	return contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
}

func defaultKind(nativeType string, scopeKind asset.ScopeKind) asset.ResourceKind {
	return asset.ResourceKind{
		ID: asset.ResourceKindID(string(asset.ProviderAWS) + ":" + nativeType), Provider: asset.ProviderAWS,
		NativeType: nativeType, DisplayName: nativeType, ScopeKinds: []asset.ScopeKind{scopeKind},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "resource-explorer",
	}
}

func snapshot(value any) (map[string]any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode AWS inventory record: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode AWS inventory record: %w", err)
	}
	return result, nil
}

func stringMap(value any) map[string]string {
	result := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			result[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok {
				result[key] = text
			}
		}
	}
	return result
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
		return "AWS API call failed"
	}
	return e.Code + ": " + e.Message
}

func NormalizeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var existing *contracts.ProviderCallError
	if errors.As(err, &existing) {
		return err
	}
	apiError := &APIError{Message: err.Error()}
	var local *APIError
	if errors.As(err, &local) {
		apiError = local
	} else {
		var smithyError smithy.APIError
		if errors.As(err, &smithyError) {
			apiError.Code = smithyError.ErrorCode()
			apiError.Message = smithyError.ErrorMessage()
		}
		var responseError *awshttp.ResponseError
		if errors.As(err, &responseError) {
			apiError.StatusCode = responseError.HTTPStatusCode()
			apiError.RequestID = responseError.ServiceRequestID()
		}
	}
	providerError := execution.ProviderError{
		Category: errorCategory(apiError.Code, apiError.StatusCode), Code: apiError.Code,
		Message: apiError.Message, RequestID: apiError.RequestID, Summary: unsupportedSummary(apiError.Code),
	}
	return &contracts.ProviderCallError{Provider: providerError, RetryAfter: apiError.RetryAfter, Cause: err}
}

func errorCategory(code string, status int) execution.ErrorCategory {
	normalized := strings.ToLower(code)
	switch {
	case normalized == "unsupportedoperation" || normalized == "unsupportedactionexception" ||
		normalized == "typenotfoundexception" || normalized == "optinrequired":
		return execution.ErrorUnsupported
	case status == 429 || strings.Contains(normalized, "throttl") || strings.Contains(normalized, "toomanyrequests"):
		return execution.ErrorThrottled
	case status == 403 || status == 401 || strings.Contains(normalized, "accessdenied") || strings.Contains(normalized, "unauthorized"):
		return execution.ErrorPermissionDenied
	case status == 404 || strings.Contains(normalized, "notfound"):
		return execution.ErrorNotFound
	case status == 409 || strings.Contains(normalized, "conflict"):
		return execution.ErrorConflict
	case status >= 500:
		return execution.ErrorRetryable
	case status >= 400:
		return execution.ErrorInvalidRequest
	default:
		return execution.ErrorProviderFailure
	}
}

func unsupportedSummary(code string) map[string]any {
	switch strings.ToLower(code) {
	case "optinrequired":
		return map[string]any{"skip_reason": string(asset.SkipProviderRegionUnavailable)}
	case "unsupportedoperation", "unsupportedactionexception", "typenotfoundexception":
		return map[string]any{"skip_reason": string(asset.SkipProductUnsupported)}
	default:
		return nil
	}
}
