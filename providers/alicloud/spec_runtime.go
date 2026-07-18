package alicloud

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type specParameterContext struct {
	region     string
	parentID   string
	nativeID   string
	nativeIDs  []string
	normalized map[string]any
}

func resolveSpecParameters(parameters map[string]any, context specParameterContext) (map[string]any, error) {
	resolved := make(map[string]any, len(parameters))
	for name, value := range parameters {
		text, expression := value.(string)
		if !expression {
			resolved[name] = value
			continue
		}
		switch text {
		case "scope.location":
			if strings.TrimSpace(context.region) == "" {
				return nil, fmt.Errorf("spec parameter %q requires a region location", name)
			}
			resolved[name] = context.region
		case "parent.nativeId":
			if strings.TrimSpace(context.parentID) == "" {
				return nil, fmt.Errorf("spec parameter %q requires a parent native ID", name)
			}
			resolved[name] = context.parentID
		case "resource.nativeId":
			if strings.TrimSpace(context.nativeID) == "" {
				return nil, fmt.Errorf("spec parameter %q requires a resource native ID", name)
			}
			resolved[name] = context.nativeID
		case "resource.nativeIdsJson":
			if strings.TrimSpace(context.nativeID) == "" {
				return nil, fmt.Errorf("spec parameter %q requires a resource native ID", name)
			}
			encoded, err := json.Marshal([]string{context.nativeID})
			if err != nil {
				return nil, fmt.Errorf("encode resource native ID for spec parameter %q: %w", name, err)
			}
			resolved[name] = string(encoded)
		case "resources.nativeId":
			if len(context.nativeIDs) != 1 || strings.TrimSpace(context.nativeIDs[0]) == "" {
				return nil, fmt.Errorf("spec parameter %q requires exactly one resource native ID", name)
			}
			resolved[name] = context.nativeIDs[0]
		case "resources.nativeIds":
			if len(context.nativeIDs) == 0 {
				return nil, fmt.Errorf("spec parameter %q requires resource native IDs", name)
			}
			resolved[name] = append([]string(nil), context.nativeIDs...)
		case "resources.nativeIdsJson":
			if len(context.nativeIDs) == 0 {
				return nil, fmt.Errorf("spec parameter %q requires resource native IDs", name)
			}
			encoded, err := json.Marshal(context.nativeIDs)
			if err != nil {
				return nil, fmt.Errorf("encode resource native IDs for spec parameter %q: %w", name, err)
			}
			resolved[name] = string(encoded)
		case "resources.count":
			if len(context.nativeIDs) == 0 {
				return nil, fmt.Errorf("spec parameter %q requires resource native IDs", name)
			}
			resolved[name] = len(context.nativeIDs)
		default:
			if strings.HasPrefix(text, "resource.normalized.") {
				path := strings.TrimPrefix(text, "resource.normalized.")
				resolvedValue := valueAtPath(context.normalized, path)
				if resolvedValue == nil {
					return nil, fmt.Errorf(
						"spec parameter %q requires normalized resource field %q",
						name,
						path,
					)
				}
				resolved[name] = resolvedValue
				continue
			}
			resolved[name] = value
		}
	}
	return resolved, nil
}

func (r *Runtime) listProductAPI(
	ctx context.Context,
	request contracts.InventoryRequest,
	compiled spec.CompiledSpec,
) (contracts.InventoryBatch, error) {
	list := compiled.Definition.Discovery.List
	if list == nil {
		return contracts.InventoryBatch{}, fmt.Errorf(
			"Alibaba Cloud resource type %q has no product API discovery spec",
			compiled.ResourceKind.NativeType,
		)
	}
	if compiled.Definition.Discovery.Parent != nil {
		return r.listFanoutProductAPI(ctx, request, compiled)
	}
	api := networkScopedProductAPI(*list, compiled.ResourceKind.NativeType, request.NetworkTarget)
	region, err := productAPIRegion(request)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if !productAPISupportsRegion(api, region) {
		return contracts.InventoryBatch{}, NormalizeError(&APIError{
			Code:       "RegionNotAvailable",
			Message:    fmt.Sprintf("%s is not available in region %s", api.Operation, region),
			StatusCode: 400,
		})
	}
	page, err := r.invokeProductAPIPage(
		ctx,
		request,
		api,
		specParameterContext{region: region},
		request.Cursor,
		request.Limit,
	)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	items, err := inventoryItemsFromProductAPI(
		page.items,
		compiled,
		request,
		region,
		"",
	)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	return contracts.InventoryBatch{
		Items: items, NextCursor: page.next, RequestID: page.requestID, Complete: page.next == "",
	}, nil
}

func networkScopedProductAPI(
	api spec.ProductAPISpec,
	nativeType string,
	target *asset.ScanTarget,
) spec.ProductAPISpec {
	if target == nil {
		return api
	}
	parameter := ""
	value := ""
	switch nativeType {
	case vpcNativeType:
		if target.Kind == asset.ScanTargetVPC {
			parameter, value = "VpcId", strings.TrimSpace(target.NativeID)
		}
	case VPCGatewayEndpointNativeType:
		if target.Kind == asset.ScanTargetVPC {
			parameter, value = "VpcId", strings.TrimSpace(target.NativeID)
		}
	case vSwitchNativeType:
		switch target.Kind {
		case asset.ScanTargetVPC:
			parameter, value = "VpcId", strings.TrimSpace(target.NativeID)
		case asset.ScanTargetVSwitch:
			parameter, value = "VSwitchId", strings.TrimSpace(target.NativeID)
		}
	}
	if parameter == "" || value == "" {
		return api
	}
	parameters := make(map[string]any, len(api.Parameters)+1)
	for name, configured := range api.Parameters {
		parameters[name] = configured
	}
	parameters[parameter] = value
	api.Parameters = parameters
	return api
}

func productAPISupportsRegion(api spec.ProductAPISpec, region string) bool {
	if len(api.SupportedRegions) == 0 {
		return true
	}
	for _, supported := range api.SupportedRegions {
		if region == supported {
			return true
		}
	}
	return false
}

type productAPIPage struct {
	items     []any
	next      string
	requestID string
}

func (r *Runtime) invokeProductAPIPage(
	ctx context.Context,
	request contracts.InventoryRequest,
	api spec.ProductAPISpec,
	parameterContext specParameterContext,
	cursor string,
	limit int,
) (productAPIPage, error) {
	parameters, err := resolveSpecParameters(api.Parameters, parameterContext)
	if err != nil {
		return productAPIPage{}, err
	}
	page, pageSize, err := applySpecPagination(parameters, api.Pagination, cursor, limit)
	if err != nil {
		return productAPIPage{}, err
	}
	result, err := r.Invoke(ctx, contracts.Invocation{
		ConnectionID: request.ConnectionID,
		Operation:    api.Operation,
		Scope:        map[string]string{"region": parameterContext.region},
		Parameters:   parameters,
	})
	if err != nil {
		return productAPIPage{}, err
	}
	rawValue := valueAtPath(result.Data, api.ItemsPath)
	rawItems, ok := productAPIListValue(rawValue)
	if !ok {
		if rawObject, objectOK := rawValue.(map[string]any); objectOK {
			if len(rawObject) == 0 {
				rawItems = []any{}
			} else {
				rawItems = []any{rawObject}
			}
			ok = true
		}
	}
	if !ok && explicitlyEmptyPathValue(result.Data, api.ItemsPath) {
		rawItems = []any{}
		ok = true
	}
	if !ok && paginationTotalIsZero(result.Data, api.Pagination) {
		rawItems = []any{}
		ok = true
	}
	if !ok {
		return productAPIPage{}, fmt.Errorf(
			"Alibaba Cloud operation %q response path %q is not an array",
			api.Operation,
			api.ItemsPath,
		)
	}
	next, err := nextSpecCursor(result.Data, api.Pagination, page, pageSize, len(rawItems))
	if err != nil {
		return productAPIPage{}, err
	}
	return productAPIPage{items: rawItems, next: next, requestID: result.RequestID}, nil
}

func productAPIListValue(value any) ([]any, bool) {
	if items, ok := value.([]any); ok {
		return items, true
	}
	if value == nil {
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return []any{}, true
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) == 0 || encoded[0] != '[' {
			return nil, false
		}
		var items []any
		if err := json.Unmarshal(encoded, &items); err != nil {
			return nil, false
		}
		return items, true
	}
	items := make([]any, reflected.Len())
	for index := range items {
		items[index] = reflected.Index(index).Interface()
	}
	return items, true
}

func paginationTotalIsZero(data map[string]any, pagination *spec.PaginationSpec) bool {
	if pagination == nil || strings.TrimSpace(pagination.TotalPath) == "" {
		return false
	}
	total, ok := integerValue(valueAtPath(data, pagination.TotalPath))
	return ok && total == 0
}

func explicitlyEmptyPathValue(value map[string]any, path string) bool {
	current := any(value)
	for _, segment := range strings.Split(strings.TrimSpace(path), ".") {
		if nilLikeValue(current) {
			return true
		}
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		next, exists := object[segment]
		if !exists {
			return len(object) == 0
		}
		current = next
	}
	return nilLikeValue(current)
}

func nilLikeValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func inventoryItemsFromProductAPI(
	rawItems []any,
	compiled spec.CompiledSpec,
	request contracts.InventoryRequest,
	region string,
	parentID string,
) ([]contracts.InventoryItem, error) {
	list := compiled.Definition.Discovery.List
	items := make([]contracts.InventoryItem, 0, len(rawItems))
	for index, raw := range rawItems {
		resource, ok := productAPIResourceMap(raw)
		if !ok {
			return nil, fmt.Errorf(
				"Alibaba Cloud operation %q item %d has type %T",
				list.Operation,
				index,
				raw,
			)
		}
		if strings.TrimSpace(parentID) != "" {
			resource["_parent"] = map[string]any{"nativeId": parentID}
		}
		if request.Scope.Kind == asset.ScopeRegion && !productAPIResourceMatchesRegion(resource, region) {
			continue
		}
		nativeID := strings.TrimSpace(stringValue(valueAtPath(resource, list.IdentityPath)))
		if nativeID == "" {
			if list.Operation == "AlibabaCloud.CloudFirewall.DescribeUserBuyVersion" &&
				cloudFirewallInventoryAbsent(resource) {
				continue
			}
			return nil, fmt.Errorf(
				"Alibaba Cloud operation %q item %d has no identity at %q",
				list.Operation,
				index,
				list.IdentityPath,
			)
		}
		normalized := make(map[string]any, len(compiled.Definition.Fields))
		for field, property := range compiled.Definition.Fields {
			if value := valueAtPath(resource, property.Path); value != nil {
				normalized[field] = value
			}
		}
		normalized[inventorySourceField] = "product-api"
		items = append(items, contracts.InventoryItem{
			NativeType:   compiled.ResourceKind.NativeType,
			NativeID:     nativeID,
			ResourceKind: compiled.ResourceKind,
			Scope: contracts.InventoryScope{
				Kind: request.Scope.Kind, NativeID: request.Scope.NativeID,
				Name: request.Scope.Name, Location: request.Scope.Location,
			},
			Name:       stringValue(normalized["name"]),
			State:      stringValue(normalized["state"]),
			Location:   region,
			Tags:       productTags(resource),
			Normalized: normalized,
			Raw:        resource,
		})
	}
	return items, nil
}

func productAPIResourceMatchesRegion(resource map[string]any, region string) bool {
	resourceRegion := firstString(resource, "RegionId", "RegionID", "regionId", "region_id")
	return strings.TrimSpace(resourceRegion) == "" ||
		strings.EqualFold(strings.TrimSpace(resourceRegion), strings.TrimSpace(region))
}

func productAPIResourceMap(value any) (map[string]any, bool) {
	if resource, ok := value.(map[string]any); ok {
		return resource, true
	}
	if value == nil {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		return nil, false
	}
	var resource map[string]any
	if err := json.Unmarshal(encoded, &resource); err != nil || resource == nil {
		return nil, false
	}
	return resource, true
}

func cloudFirewallInventoryAbsent(resource map[string]any) bool {
	if strings.EqualFold(strings.TrimSpace(stringValue(resource["InstanceStatus"])), "free") {
		return true
	}
	userStatus, hasUserStatus := resource["UserStatus"].(bool)
	return hasUserStatus && !userStatus
}

type fanoutProductAPICursor struct {
	ParentIndex int    `json:"parent_index"`
	ChildCursor string `json:"child_cursor,omitempty"`
}

func (r *Runtime) listFanoutProductAPI(
	ctx context.Context,
	request contracts.InventoryRequest,
	compiled spec.CompiledSpec,
) (contracts.InventoryBatch, error) {
	region, err := productAPIRegion(request)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	parents, err := r.collectFanoutParents(
		ctx,
		request,
		*compiled.Definition.Discovery.Parent,
		region,
	)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	cursor := fanoutProductAPICursor{}
	if strings.TrimSpace(request.Cursor) != "" {
		if err := json.Unmarshal([]byte(request.Cursor), &cursor); err != nil {
			return contracts.InventoryBatch{}, fmt.Errorf(
				"invalid product API fanout cursor %q: %w",
				request.Cursor,
				err,
			)
		}
	}
	if cursor.ParentIndex < 0 || cursor.ParentIndex > len(parents) {
		return contracts.InventoryBatch{}, fmt.Errorf(
			"product API fanout parent cursor %d is out of range",
			cursor.ParentIndex,
		)
	}
	for cursor.ParentIndex < len(parents) {
		parentID := parents[cursor.ParentIndex]
		page, pageErr := r.invokeProductAPIPage(
			ctx,
			request,
			*compiled.Definition.Discovery.List,
			specParameterContext{region: region, parentID: parentID},
			cursor.ChildCursor,
			request.Limit,
		)
		if pageErr != nil {
			return contracts.InventoryBatch{}, pageErr
		}
		items, itemErr := inventoryItemsFromProductAPI(
			page.items,
			compiled,
			request,
			region,
			parentID,
		)
		if itemErr != nil {
			return contracts.InventoryBatch{}, itemErr
		}
		if page.next != "" {
			cursor.ChildCursor = page.next
		} else {
			cursor.ParentIndex++
			cursor.ChildCursor = ""
		}
		if len(items) == 0 && page.next == "" {
			continue
		}
		next, cursorErr := encodeFanoutProductAPICursor(cursor, len(parents))
		if cursorErr != nil {
			return contracts.InventoryBatch{}, cursorErr
		}
		return contracts.InventoryBatch{
			Items: items, NextCursor: next, RequestID: page.requestID, Complete: next == "",
		}, nil
	}
	return contracts.InventoryBatch{Complete: true}, nil
}

func (r *Runtime) collectFanoutParents(
	ctx context.Context,
	request contracts.InventoryRequest,
	parent spec.ParentDiscoverySpec,
	region string,
) ([]string, error) {
	switch strings.TrimSpace(parent.Source) {
	case "":
		return r.collectDirectProductAPIParents(
			ctx,
			request,
			parent.ProductAPISpec,
			region,
		)
	case "resource-center":
		return r.collectResourceCenterParents(
			ctx,
			request,
			parent.NativeType,
			region,
		)
	default:
		return nil, fmt.Errorf(
			"Alibaba Cloud product API fanout parent source %q is unsupported",
			parent.Source,
		)
	}
}

func (r *Runtime) collectDirectProductAPIParents(
	ctx context.Context,
	request contracts.InventoryRequest,
	parent spec.ProductAPISpec,
	region string,
) ([]string, error) {
	var result []string
	cursor := ""
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		page, err := r.invokeProductAPIPage(
			ctx,
			request,
			parent,
			specParameterContext{region: region},
			cursor,
			1000,
		)
		if err != nil {
			return nil, err
		}
		for index, raw := range page.items {
			resource, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf(
					"Alibaba Cloud operation %q parent item %d has type %T",
					parent.Operation,
					index,
					raw,
				)
			}
			nativeID := strings.TrimSpace(stringValue(valueAtPath(resource, parent.IdentityPath)))
			if nativeID == "" {
				return nil, fmt.Errorf(
					"Alibaba Cloud operation %q parent item %d has no identity at %q",
					parent.Operation,
					index,
					parent.IdentityPath,
				)
			}
			result = append(result, nativeID)
		}
		if page.next == "" {
			return result, nil
		}
		if page.next == cursor {
			return nil, fmt.Errorf(
				"Alibaba Cloud operation %q returned a repeated parent cursor %q",
				parent.Operation,
				cursor,
			)
		}
		cursor = page.next
	}
	return nil, fmt.Errorf(
		"Alibaba Cloud operation %q exceeded 1000 parent pages",
		parent.Operation,
	)
}

func (r *Runtime) collectResourceCenterParents(
	ctx context.Context,
	request contracts.InventoryRequest,
	nativeType string,
	region string,
) ([]string, error) {
	nativeType = strings.TrimSpace(nativeType)
	if nativeType == "" {
		return nil, fmt.Errorf(
			"Alibaba Cloud Resource Center fanout parent requires a native type",
		)
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	client, err := r.factory.ResourceCenter(ctx, credential, region)
	if err != nil {
		return nil, NormalizeError(err)
	}
	result := []string{}
	seenIDs := map[string]struct{}{}
	cursor := ""
	regionFilter := resourceCenterRegionFilter(request.Scope)
	for pageNumber := 0; pageNumber < 1000; pageNumber++ {
		searchRequest := SearchRequest{
			NextToken:     cursor,
			MaxResults:    ResourceCenterPageLimit,
			RegionID:      regionFilter,
			ResourceTypes: []string{nativeType},
		}
		filters := []any{
			map[string]any{
				"Key": "ResourceType", "MatchType": "Equals",
				"Value": []string{nativeType},
			},
		}
		if regionFilter != "" {
			filters = append([]any{
				map[string]any{
					"Key": "RegionId", "MatchType": "Equals",
					"Value": []string{regionFilter},
				},
			}, filters...)
		}
		requestPayload := map[string]any{
			"Filter":                  filters,
			"MaxResults":              ResourceCenterPageLimit,
			"IncludeDeletedResources": false,
		}
		if cursor != "" {
			requestPayload["NextToken"] = cursor
		}
		execution.LogCloudAPIRequest(
			ctx,
			"resource-center",
			"SearchResources",
			rawCloudPayload(requestPayload),
		)
		page, searchErr := client.SearchResources(ctx, searchRequest)
		if searchErr != nil {
			LogCloudAPIError(ctx, "resource-center", "SearchResources", searchErr)
			return nil, NormalizeError(searchErr)
		}
		responsePayload := page.RawResponse
		if responsePayload == nil {
			responsePayload = map[string]any{
				"RequestId": page.RequestID,
				"NextToken": page.NextToken,
				"Resources": page.Resources,
			}
		}
		execution.LogCloudAPIResponse(
			ctx,
			"resource-center",
			"SearchResources",
			rawCloudPayload(responsePayload),
		)
		for _, resource := range page.Resources {
			if strings.TrimSpace(resource.ResourceType) != nativeType {
				continue
			}
			if regionFilter != "" &&
				strings.TrimSpace(resource.RegionID) != regionFilter {
				continue
			}
			nativeID := strings.TrimSpace(resource.ResourceID)
			if nativeID == "" {
				continue
			}
			if _, exists := seenIDs[nativeID]; exists {
				continue
			}
			seenIDs[nativeID] = struct{}{}
			result = append(result, nativeID)
		}
		if page.NextToken == "" {
			return result, nil
		}
		if page.NextToken == cursor {
			return nil, fmt.Errorf(
				"Alibaba Cloud Resource Center returned a repeated parent cursor %q",
				cursor,
			)
		}
		cursor = page.NextToken
	}
	return nil, fmt.Errorf(
		"Alibaba Cloud Resource Center exceeded 1000 parent pages for %q",
		nativeType,
	)
}

func encodeFanoutProductAPICursor(
	cursor fanoutProductAPICursor,
	parentCount int,
) (string, error) {
	if cursor.ParentIndex >= parentCount && cursor.ChildCursor == "" {
		return "", nil
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode product API fanout cursor: %w", err)
	}
	return string(payload), nil
}

func applySpecPagination(
	parameters map[string]any,
	pagination *spec.PaginationSpec,
	cursor string,
	limit int,
) (page int, pageSize int, err error) {
	if pagination == nil {
		if strings.TrimSpace(cursor) != "" {
			return 0, 0, fmt.Errorf("product API discovery does not support a cursor")
		}
		return 1, limit, nil
	}
	pageSize = limit
	if pageSize <= 0 {
		pageSize = 100
	}
	if pagination.MaxPageSize > 0 && pageSize > pagination.MaxPageSize {
		pageSize = pagination.MaxPageSize
	}
	switch pagination.Type {
	case "token":
		if strings.TrimSpace(cursor) != "" {
			parameters[pagination.TokenParameter] = cursor
		}
		if pagination.PageSizeParameter != "" {
			parameters[pagination.PageSizeParameter] = pageSize
		}
		return 1, pageSize, nil
	case "page-number":
		page = 1
		if strings.TrimSpace(cursor) != "" {
			parsed, parseErr := strconv.Atoi(cursor)
			if parseErr != nil || parsed < 1 {
				return 0, 0, fmt.Errorf("invalid product API page cursor %q", cursor)
			}
			page = parsed
		}
		parameters[pagination.PageParameter] = page
		parameters[pagination.PageSizeParameter] = pageSize
		return page, pageSize, nil
	case "offset":
		offset := 0
		if strings.TrimSpace(cursor) != "" {
			parsed, parseErr := strconv.Atoi(cursor)
			if parseErr != nil || parsed < 0 {
				return 0, 0, fmt.Errorf("invalid product API offset cursor %q", cursor)
			}
			offset = parsed
		}
		parameters[pagination.OffsetParameter] = offset
		parameters[pagination.PageSizeParameter] = pageSize
		return offset, pageSize, nil
	default:
		return 0, 0, fmt.Errorf("unsupported product API pagination type %q", pagination.Type)
	}
}

func nextSpecCursor(
	data map[string]any,
	pagination *spec.PaginationSpec,
	page int,
	pageSize int,
	itemCount int,
) (string, error) {
	if pagination == nil {
		return "", nil
	}
	switch pagination.Type {
	case "token":
		return strings.TrimSpace(stringValue(valueAtPath(data, pagination.TokenPath))), nil
	case "page-number":
		if itemCount == 0 {
			return "", nil
		}
		total, ok := integerValue(valueAtPath(data, pagination.TotalPath))
		if !ok {
			return "", fmt.Errorf("product API pagination total path %q is not an integer", pagination.TotalPath)
		}
		if page*pageSize >= total {
			return "", nil
		}
		return strconv.Itoa(page + 1), nil
	case "offset":
		if itemCount == 0 {
			return "", nil
		}
		total, ok := integerValue(valueAtPath(data, pagination.TotalPath))
		if !ok {
			return "", fmt.Errorf("product API pagination total path %q is not an integer", pagination.TotalPath)
		}
		if page+itemCount >= total {
			return "", nil
		}
		return strconv.Itoa(page + itemCount), nil
	default:
		return "", fmt.Errorf("unsupported product API pagination type %q", pagination.Type)
	}
}

func valueAtPath(value any, path string) any {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) == "$" {
		return value
	}
	current := value
	for _, segment := range strings.Split(path, ".") {
		next, ok := valueAtPathSegment(current, segment)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func valueAtPathSegment(value any, segment string) (any, bool) {
	if object, ok := value.(map[string]any); ok {
		next, exists := object[segment]
		return next, exists
	}
	if value == nil {
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil, false
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		next := reflected.MapIndex(reflect.ValueOf(segment).Convert(reflected.Type().Key()))
		if !next.IsValid() {
			return nil, false
		}
		return next.Interface(), true
	case reflect.Struct:
		reflectedType := reflected.Type()
		for index := 0; index < reflected.NumField(); index++ {
			field := reflectedType.Field(index)
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.Name != segment && jsonName != segment {
				continue
			}
			fieldValue := reflected.Field(index)
			if !fieldValue.CanInterface() {
				return nil, false
			}
			return fieldValue.Interface(), true
		}
	}
	return nil, false
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func productTags(resource map[string]any) map[string]string {
	raw := valueAtPath(resource, "Tags.Tag")
	if raw == nil {
		raw = resource["Tags"]
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	tags := make(map[string]string, len(items))
	for _, item := range items {
		tag, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := stringValue(firstValue(tag, "Key", "TagKey", "key"))
		if key == "" {
			continue
		}
		tags[key] = stringValue(firstValue(tag, "Value", "TagValue", "value"))
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}
