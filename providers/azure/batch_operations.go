package azure

import (
	"context"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// An operation's endpoint parameter is only a lookup key. The native ARM
// subscription list and account GET establish the authority for Batch OAuth.
func (c *client) batchAccountForEndpoint(ctx context.Context, endpoint string) (batchAccountContext, error) {
	if !batchEndpointPattern.MatchString(endpoint) {
		return batchAccountContext{}, serviceDenied("invalid_batch_account_endpoint")
	}
	values, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Batch/batchAccounts", batchVersion)
	if err != nil {
		return batchAccountContext{}, err
	}
	seen := map[string]bool{}
	var match batchAccountContext
	for _, value := range values {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || batchKind(kind) != batchAccountType || !strings.HasPrefix(id, c.root()+"/") || seen[id] || !validResponseType(batchAccountType, text(raw["type"])) {
			return batchAccountContext{}, serviceDenied("invalid_batch_account_list_identity")
		}
		seen[id] = true
		current, err := c.batchAccount(ctx, id)
		if err != nil {
			return batchAccountContext{}, err
		}
		if !nativeConfigurationContains(batchSnapshot(batchAccountType, raw), batchSnapshot(batchAccountType, current.raw)) {
			return batchAccountContext{}, serviceDenied("batch_account_list_changed")
		}
		if current.endpoint == endpoint {
			if match.id != "" {
				return batchAccountContext{}, serviceDenied("ambiguous_batch_account_endpoint")
			}
			match = current
		}
	}
	if match.id == "" {
		return batchAccountContext{}, serviceDenied("batch_account_outside_subscription")
	}
	return match, nil
}

func (c *client) invokeBatch(ctx context.Context, operation catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	request, err := bindAzureREST(operation, invocation.Parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	account, err := c.batchAccountForEndpoint(ctx, text(invocation.Parameters["endpoint"]))
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	if invocation.IdempotencyKey != "" {
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["client-request-id"] = azureRequestID(invocation.IdempotencyKey)
	}
	result, err := c.batchRequest(ctx, account, request)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	// These native data-plane operations poll the resource itself. The Swagger
	// declares no LRO endpoint; never follow an unsolicited Location header.
	if operationLocation(result.header) != "" || result.data["code"] != nil || result.data["error"] != nil {
		return contracts.InvocationResult{}, serviceDenied("invalid_batch_operation_response")
	}
	next := ""
	if value := result.data["odata.nextLink"]; value != nil {
		var ok bool
		next, ok = value.(string)
		if !ok {
			return contracts.InvocationResult{}, serviceDenied("invalid_batch_list_continuation")
		}
		if next != "" {
			initial, _ := url.Parse(request.URL)
			u, err := url.Parse(next)
			if err != nil || u.Scheme+"://"+u.Host != account.endpoint || u.Path != initial.Path || u.RawPath != "" || u.Fragment != "" || u.User != nil || u.Port() != "" {
				return contracts.InvocationResult{}, serviceDenied("batch_list_collection_changed")
			}
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != batchVersion {
				return contracts.InvocationResult{}, serviceDenied("batch_list_version_changed")
			}
		}
	}
	return contracts.InvocationResult{Data: safePayload(object(batchSafeValue(result.data))), RequestID: result.requestID, NextToken: next}, nil
}

func (c *client) batchPoolData(ctx context.Context, account batchAccountContext, poolID string) (response, error) {
	if !batchNamePattern.MatchString(poolID) {
		return response{}, serviceDenied("invalid_batch_pool_id")
	}
	metadata, err := providerData()
	if err != nil {
		return response{}, err
	}
	op, _ := metadata.catalog.Operation(batchDataPrefix + "Pools_GetPool")
	request, err := bindAzureREST(op, map[string]any{"endpoint": account.endpoint, "poolId": poolID})
	if err != nil {
		return response{}, err
	}
	result, err := c.batchRequest(ctx, account, request)
	if err != nil {
		return result, err
	}
	if result.status != 200 || !strings.EqualFold(text(result.data["id"]), poolID) || !strings.EqualFold(text(result.data["url"]), account.endpoint+"/pools/"+poolID) || result.data["error"] != nil || result.data["code"] != nil {
		return result, serviceDenied("invalid_batch_pool_response")
	}
	if etag := result.header.Get("ETag"); etag != "" && text(result.data["eTag"]) != "" && etag != text(result.data["eTag"]) {
		return result, serviceDenied("batch_pool_etag_disagrees")
	}
	return result, nil
}

func batchPoolDataSnapshot(raw map[string]any) map[string]any {
	copy := batchClone(raw)
	copy["id"], copy["url"] = strings.ToLower(text(copy["id"])), strings.ToLower(text(copy["url"]))
	for _, key := range []string{"eTag", "lastModified", "state", "stateTransitionTime", "allocationState", "allocationStateTransitionTime", "currentDedicatedNodes", "currentLowPriorityNodes", "resizeErrors", "resizeOperationStatus", "autoScaleRun", "stats"} {
		delete(copy, key)
	}
	return copy
}

// Reconcile both native indexes, including auto pools. A complete ARM page
// alone must not conceal a pool which the Batch service still reports.
func (c *client) batchPools(ctx context.Context, account batchAccountContext) ([]serviceChild, string, error) {
	pools, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: account.id, NativeType: batchAccountType}, account.raw, []string{batchPoolType})
	if err != nil {
		return nil, "", err
	}
	values, requestID, err := c.batchDataList(ctx, account, "Pools_ListPools", nil)
	if err != nil {
		return nil, requestID, err
	}
	index := map[string]bool{}
	for _, pool := range pools {
		index[strings.ToLower(last(pool.id))] = true
	}
	for _, listed := range values {
		name := text(listed["id"])
		if !batchNamePattern.MatchString(name) || !index[strings.ToLower(name)] || !strings.EqualFold(text(listed["url"]), account.endpoint+"/pools/"+name) {
			return nil, requestID, serviceDenied("batch_pool_indexes_disagree")
		}
		delete(index, strings.ToLower(name))
		current, err := c.batchPoolData(ctx, account, name)
		if err != nil {
			return nil, requestID, err
		}
		if !nativeConfigurationContains(batchPoolDataSnapshot(listed), batchPoolDataSnapshot(current.data)) {
			return nil, requestID, serviceDenied("batch_data_pool_configuration_changed")
		}
	}
	if len(index) != 0 {
		return nil, requestID, serviceDenied("batch_pool_indexes_disagree")
	}
	return pools, requestID, nil
}
