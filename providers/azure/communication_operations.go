package azure

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Suffix validation is only a preliminary check. The current subscription's
// native account index and own GET must uniquely bind the selected endpoint.
func (c *client) communicationAccountForEndpoint(ctx context.Context, endpoint string) (communicationAccountContext, error) {
	endpoint = strings.TrimSuffix(endpoint, "/")
	if !communicationEndpointPattern.MatchString(endpoint) {
		return communicationAccountContext{}, serviceDenied("invalid_communication_account_endpoint")
	}
	values, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Communication/communicationServices", communicationARMVersion)
	if err != nil {
		return communicationAccountContext{}, err
	}
	seen := map[string]bool{}
	var match communicationAccountContext
	for _, value := range values {
		raw := object(value)
		id, typ, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(typ, communicationType) || !strings.HasPrefix(id, c.root()+"/") || seen[id] || !validResponseType(communicationType, text(raw["type"])) {
			return communicationAccountContext{}, serviceDenied("invalid_communication_account_index")
		}
		seen[id] = true
		current, err := c.communicationAccount(ctx, id)
		if err != nil {
			return communicationAccountContext{}, err
		}
		if !nativeConfigurationContains(communicationSnapshot(communicationType, raw), communicationSnapshot(communicationType, current.raw)) {
			return communicationAccountContext{}, serviceDenied("communication_account_index_changed")
		}
		if current.endpoint == endpoint {
			if match.id != "" {
				return communicationAccountContext{}, serviceDenied("ambiguous_communication_account_endpoint")
			}
			match = current
		}
	}
	if match.id == "" {
		return communicationAccountContext{}, serviceDenied("communication_account_outside_subscription")
	}
	return match, nil
}

func communicationNextLink(initial, next string) error {
	start, err := url.Parse(initial)
	if err != nil {
		return serviceDenied("invalid_communication_collection")
	}
	u, err := url.Parse(next)
	if err != nil || !communicationEndpointPattern.MatchString(u.Scheme+"://"+u.Host) || u.Scheme+"://"+u.Host != start.Scheme+"://"+start.Host || u.Path != start.Path || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Port() != "" {
		return serviceDenied("communication_list_collection_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != communicationVersion(start.Path) {
		return serviceDenied("communication_list_version_changed")
	}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return serviceDenied("invalid_communication_list_query")
		}
		switch name {
		case "api-version", "continuationToken", "skipToken":
		case "skip", "top", "maxPageSize":
			n, err := strconv.ParseInt(values[0], 10, 32)
			if err != nil || n < 0 || name != "skip" && n == 0 {
				return serviceDenied("invalid_communication_list_size")
			}
		default:
			return serviceDenied("filtered_communication_list_query")
		}
	}
	return nil
}

func (c *client) communicationDataList(ctx context.Context, account communicationAccountContext, operation string, parameters map[string]any) ([]map[string]any, string, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, "", err
	}
	op, ok := metadata.catalog.Operation(communicationDataPrefix + operation)
	if !ok || op.Call == nil || op.Call.Style != "azure-communication-rest" || op.Call.Method != "GET" || op.Pagination == nil {
		return nil, "", serviceDenied("invalid_communication_list_operation")
	}
	request, err := bindAzureREST(op, communicationParameters(parameters, account.endpoint))
	if err != nil {
		return nil, "", err
	}
	next, requestID := request.URL, ""
	seen := map[string]bool{}
	var result []map[string]any
	for next != "" {
		if seen[next] {
			return nil, requestID, serviceDenied("communication_list_cycle")
		}
		if err := communicationNextLink(request.URL, next); err != nil {
			return nil, requestID, err
		}
		seen[next] = true
		page, err := c.communicationRequest(ctx, account, catalog.RESTRequest{Method: "GET", URL: next})
		if err != nil {
			return nil, requestID, err
		}
		requestID = page.requestID
		values, ok := page.data[op.Pagination.ItemsPath].([]any)
		if !ok || page.status != 200 || page.data["error"] != nil || operationLocation(page.header) != "" {
			return nil, requestID, serviceDenied("incomplete_communication_list")
		}
		for _, value := range values {
			raw, ok := value.(map[string]any)
			if !ok || raw == nil {
				return nil, requestID, serviceDenied("invalid_communication_list_item")
			}
			result = append(result, raw)
		}
		next = ""
		if value, exists := page.data["nextLink"]; exists && value != nil {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return nil, requestID, serviceDenied("invalid_communication_next_link")
			}
		}
	}
	return result, requestID, nil
}

func (c *client) communicationRoomParticipants(ctx context.Context, account communicationAccountContext, roomID string) ([]map[string]any, error) {
	_, typ, endpoint, params, err := communicationDataIdentity(roomID)
	if err != nil || typ != communicationRoomType || endpoint != account.endpoint {
		return nil, serviceDenied("invalid_communication_room_identity")
	}
	values, _, err := c.communicationDataList(ctx, account, "Participants_List", map[string]any{"roomId": params["roomId"]})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, raw := range values {
		id := text(raw["rawId"])
		if id == "" || id != strings.TrimSpace(id) || seen[id] || !slices.Contains([]string{"Presenter", "Attendee", "Consumer", "Collaborator"}, text(raw["role"])) {
			return nil, serviceDenied("invalid_communication_room_participant")
		}
		seen[id] = true
	}
	slices.SortFunc(values, func(a, b map[string]any) int { return strings.Compare(text(a["rawId"]), text(b["rawId"])) })
	return values, nil
}

// Native release receipts use relative Operation-Location without api-version.
// Resolve only this exact account-local operation path and the pinned version.
func communicationReleaseOperation(endpoint, value, operationID string) (string, error) {
	if !communicationEndpointPattern.MatchString(endpoint) || operationID == "" || len(operationID) > 1024 || !communicationOpaqueIDPattern.MatchString(operationID) {
		return "", serviceDenied("invalid_communication_release_operation")
	}
	u, err := url.Parse(value)
	if err != nil || value == "" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Port() != "" || u.ForceQuery {
		return "", serviceDenied("invalid_communication_release_location")
	}
	if u.Scheme == "" && u.Host == "" && strings.HasPrefix(value, "/") {
		u, _ = url.Parse(endpoint + value)
	}
	if u.Scheme+"://"+u.Host != endpoint || u.Path != "/phoneNumbers/operations/"+operationID {
		return "", serviceDenied("communication_release_operation_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) > 1 || len(q) == 1 && (len(q["api-version"]) != 1 || q.Get("api-version") != communicationPhoneVersion) {
		return "", serviceDenied("communication_release_version_changed")
	}
	q.Set("api-version", communicationPhoneVersion)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *client) invokeCommunication(ctx context.Context, operation catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	request, err := bindAzureREST(operation, invocation.Parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	account, err := c.communicationAccountForEndpoint(ctx, text(invocation.Parameters["endpoint"]))
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	if invocation.IdempotencyKey != "" {
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["x-ms-client-request-id"] = azureRequestID(invocation.IdempotencyKey)
	}
	result, err := c.communicationRequest(ctx, account, request)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if result.data["error"] != nil {
		return contracts.InvocationResult{}, serviceDenied("invalid_communication_operation_response")
	}
	next, operationURL := "", ""
	if request.Method == "DELETE" {
		u, _ := url.Parse(request.URL)
		id, kind, _, _, err := communicationDataIdentity(account.endpoint + u.Path)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		operationURL, _, err = communicationDeleteReceipt(c.subscription, id, kind, account.endpoint, result)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
	} else if result.status != 200 || operation.ID != communicationDataPrefix+"PhoneNumbers_GetOperation" && operationLocation(result.header) != "" {
		return contracts.InvocationResult{}, serviceDenied("invalid_communication_read_status")
	}
	if operation.Pagination != nil {
		if _, ok := result.data[operation.Pagination.ItemsPath].([]any); !ok {
			return contracts.InvocationResult{}, serviceDenied("incomplete_communication_list")
		}
		if value := result.data["nextLink"]; value != nil {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return contracts.InvocationResult{}, serviceDenied("invalid_communication_next_link")
			}
			if next != "" {
				if err := communicationNextLink(request.URL, next); err != nil {
					return contracts.InvocationResult{}, err
				}
			}
		}
	}
	return contracts.InvocationResult{Data: safePayload(object(communicationSafeValue(result.data))), RequestID: result.requestID, NextToken: next, OperationID: operationURL}, nil
}
