package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func batchInteger(value any, bits int) (int64, error) {
	switch value := value.(type) {
	case float64:
		// Action requests restored from JSON may contain float64. Keep large
		// integral values in decimal form instead of fmt's exponent notation.
		return strconv.ParseInt(strconv.FormatFloat(value, 'f', -1, 64), 10, bits)
	case int, int32, int64, json.Number:
		return strconv.ParseInt(fmt.Sprint(value), 10, bits)
	default:
		return 0, serviceDenied("invalid_batch_integer")
	}
}

const (
	batchVersion         = "2025-06-01"
	batchAccountType     = "Microsoft.Batch/batchAccounts"
	batchPoolType        = batchAccountType + "/pools"
	batchApplicationType = batchAccountType + "/applications"
	batchPackageType     = batchApplicationType + "/versions"
	batchPECType         = batchAccountType + "/privateEndpointConnections"
	batchPerimeterType   = batchAccountType + "/networkSecurityPerimeterConfigurations"
	batchScheduleType    = batchAccountType + "/jobSchedules"
	batchJobType         = batchAccountType + "/jobs"
	batchTaskType        = batchJobType + "/tasks"
	batchNodeType        = batchPoolType + "/nodes"
	batchDataPrefix      = "Azure.Microsoft.Batch.DataPlane."
)

func batchKind(kind string) string {
	for _, value := range []string{batchAccountType, batchPoolType, batchApplicationType, batchPackageType, batchPECType, batchPerimeterType, batchScheduleType, batchJobType, batchTaskType, batchNodeType} {
		if strings.EqualFold(value, kind) {
			return value
		}
	}
	return ""
}
func isBatchType(kind string) bool { return batchKind(kind) != "" }
func isBatchDataType(kind string) bool {
	return slices.Contains([]string{batchScheduleType, batchJobType, batchTaskType, batchNodeType}, batchKind(kind))
}

var batchEndpointPattern = regexp.MustCompile(`^https://[a-z0-9]{3,24}\.[a-z0-9-]+\.batch\.azure\.com$`)
var batchNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var batchJobNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}(:job-[0-9]+)?$`)
var batchNodeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type batchAccountContext struct {
	id, endpoint, location string
	raw                    map[string]any
}

func batchAccountID(id string) string {
	parsed, kind, err := parseID(id)
	if err != nil || !isBatchType(kind) || isBatchDataType(kind) {
		return ""
	}
	parts := strings.Split(parsed, "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}

func batchAccountEndpoint(raw map[string]any) (string, error) {
	id, kind, err := parseID(text(raw["id"]))
	endpoint := text(object(raw["properties"])["accountEndpoint"])
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	if err != nil || !strings.EqualFold(kind, batchAccountType) || !batchEndpointPattern.MatchString(endpoint) || endpoint != "https://"+last(id)+"."+resourceRegion(raw)+".batch.azure.com" {
		return "", serviceDenied("invalid_batch_account_endpoint")
	}
	return endpoint, nil
}

func (c *client) batchAccount(ctx context.Context, id string) (batchAccountContext, error) {
	parsed, kind, err := parseID(id)
	if err != nil || !strings.EqualFold(kind, batchAccountType) || !strings.HasPrefix(parsed, c.root()+"/") {
		return batchAccountContext{}, serviceDenied("invalid_batch_account")
	}
	raw, err := c.linkedResource(ctx, parsed)
	if err != nil {
		return batchAccountContext{}, err
	}
	endpoint, err := batchAccountEndpoint(raw)
	if err != nil {
		return batchAccountContext{}, err
	}
	return batchAccountContext{id: parsed, endpoint: endpoint, location: resourceRegion(raw), raw: raw}, nil
}

// Batch jobs, schedules, tasks and nodes have native data-plane URLs. They are
// not ARM resource IDs; retain those URLs instead of inventing ARM paths.
func batchDataIdentity(value string) (id, kind, endpoint string, parameters map[string]any, err error) {
	invalid := func() (string, string, string, map[string]any, error) {
		return "", "", "", nil, serviceDenied("invalid_batch_data_identity")
	}
	u, e := url.Parse(value)
	if e != nil || value != strings.TrimSpace(value) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Port() != "" || !batchEndpointPattern.MatchString(u.Scheme+"://"+u.Host) {
		return invalid()
	}
	parts := strings.Split(u.Path, "/")
	parameters = map[string]any{"endpoint": u.Scheme + "://" + u.Host}
	if len(parts) == 3 && parts[1] == "jobschedules" && batchNamePattern.MatchString(parts[2]) {
		kind = batchScheduleType
		parameters["jobScheduleId"] = parts[2]
	} else if len(parts) == 3 && parts[1] == "jobs" && batchJobNamePattern.MatchString(parts[2]) {
		kind = batchJobType
		parameters["jobId"] = parts[2]
	} else if len(parts) == 5 && parts[1] == "jobs" && parts[3] == "tasks" && batchJobNamePattern.MatchString(parts[2]) && batchNamePattern.MatchString(parts[4]) {
		kind = batchTaskType
		parameters["jobId"], parameters["taskId"] = parts[2], parts[4]
	} else if len(parts) == 5 && parts[1] == "pools" && parts[3] == "nodes" && batchNamePattern.MatchString(parts[2]) && batchNodeNamePattern.MatchString(parts[4]) {
		kind = batchNodeType
		parameters["poolId"], parameters["nodeId"] = parts[2], parts[4]
	} else {
		return invalid()
	}
	return strings.ToLower(value), kind, text(parameters["endpoint"]), parameters, nil
}

func batchDataOperation(kind resourceType, id, method string) (catalog.Operation, map[string]any, error) {
	_, typ, _, params, err := batchDataIdentity(id)
	if err != nil || !strings.EqualFold(kind.NativeType, typ) {
		return catalog.Operation{}, nil, serviceDenied("batch_data_type_mismatch")
	}
	ids := kind.ReadOperations
	if method == "DELETE" {
		ids = kind.DeleteOperations
	}
	if len(ids) != 1 {
		return catalog.Operation{}, nil, serviceDenied("invalid_batch_data_binding")
	}
	data, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := data.catalog.Operation(ids[0])
	if !ok || op.Call == nil || op.Call.Style != "azure-batch-rest" {
		return catalog.Operation{}, nil, serviceDenied("invalid_batch_data_operation")
	}
	if method == "DELETE" && typ == batchNodeType {
		params["removeOptions"] = map[string]any{"nodeList": []string{text(params["nodeId"])}, "nodeDeallocationOption": "requeue"}
		delete(params, "nodeId")
	} else if op.Call.Method != method {
		return catalog.Operation{}, nil, serviceDenied("invalid_batch_data_method")
	}
	if _, err := bindAzureREST(op, params); err != nil {
		return catalog.Operation{}, nil, err
	}
	return op, params, nil
}

func (c *client) batchRequest(ctx context.Context, account batchAccountContext, request catalog.RESTRequest) (response, error) {
	// Keep the three OAuth audiences separate. A URL is authorized only by a
	// current ARM account read in the selected subscription, never by its suffix.
	if account.id == "" || !strings.HasPrefix(account.id, c.root()+"/") {
		return response{}, serviceDenied("batch_account_context_missing")
	}
	if modes := object(account.raw["properties"])["allowedAuthenticationModes"]; modes != nil {
		values, ok := modes.([]any)
		if !ok || !slices.ContainsFunc(values, func(v any) bool { return v == "AAD" }) {
			return response{}, serviceDenied("batch_account_aad_disabled")
		}
	}
	headers := maps.Clone(request.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["ocp-date"] = time.Now().UTC().Format(http.TimeFormat)
	validate := func(endpoint string) error {
		u, err := url.Parse(endpoint)
		if err != nil || !batchEndpointPattern.MatchString(u.Scheme+"://"+u.Host) || u.Scheme+"://"+u.Host != account.endpoint || u.User != nil || u.Port() != "" || u.Fragment != "" {
			return serviceDenied("batch_request_changed_account")
		}
		if request.Method == "HEAD" {
			if err := batchFileURL(u); err != nil {
				return err
			}
		} else if u.RawPath != "" {
			return serviceDenied("batch_request_encoded_path")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != batchVersion {
			return serviceDenied("batch_request_changed_version")
		}
		return nil
	}
	return c.requestUsing(ctx, request.Method, request.URL, request.Body, headers, validate, c.batchHTTP, false)
}

func (c *client) batchRead(ctx context.Context, account batchAccountContext, id, kind string) (response, error) {
	if !isBatchDataType(kind) {
		mapping, ok := findType(kind)
		if !ok || batchAccountID(id) != account.id {
			return response{}, serviceDenied("batch_resource_changed_account")
		}
		endpoint, err := c.resourceURL(mapping, id)
		if err != nil {
			return response{}, err
		}
		result, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return result, err
		}
		if !validResourceResponse(result, id, kind) {
			return result, serviceDenied("invalid_batch_resource_response")
		}
		return result, nil
	}
	mapping, _ := findType(kind)
	op, params, err := batchDataOperation(mapping, id, "GET")
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return response{}, err
	}
	result, err := c.batchRequest(ctx, account, request)
	if err != nil {
		return result, err
	}
	actual, typ, _, _, err := batchDataIdentity(text(result.data["url"]))
	if err != nil || !strings.EqualFold(actual, id) || typ != kind || !strings.EqualFold(last(id), text(result.data["id"])) || result.status != 200 || result.data["code"] != nil || result.data["error"] != nil {
		return result, serviceDenied("invalid_batch_data_response")
	}
	if etag := result.header.Get("ETag"); etag != "" && text(result.data["eTag"]) != "" && etag != text(result.data["eTag"]) {
		return result, serviceDenied("batch_response_etag_disagrees")
	}
	return result, nil
}

func batchClone(raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.Decode(&result)
	return result
}

// Public diagnostics omit command text, arbitrary environment/metadata values,
// credentials and URL signing material. Private configuration digests retain
// the complete authored values, so redaction never weakens drift detection.
func batchSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, entry := range value {
			switch strings.ToLower(key) {
			case "commandline", "coordinationcommandline", "environmentsettings", "commonenvironmentsettings", "metadata", "sshprivatekey", "sshpublickey", "starttaskinfo", "errors", "resizeerrors", "autoscalerun":
				continue
			}
			result[key] = batchSafeValue(entry)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = batchSafeValue(entry)
		}
		return result
	case string:
		if u, err := url.Parse(value); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
			return u.String()
		}
	}
	return value
}
func batchRaw(raw map[string]any) bool {
	if isBatchType(text(raw["type"])) || strings.Contains(strings.ToLower(text(raw["id"])), "/providers/microsoft.batch/batchaccounts/") {
		return true
	}
	_, _, _, _, err := batchDataIdentity(text(raw["url"]))
	return err == nil
}
func batchSnapshot(kind string, raw map[string]any) map[string]any {
	result := batchClone(raw)
	delete(result, "etag")
	delete(result, "eTag")
	delete(result, "type")
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	if isBatchDataType(kind) {
		result["id"] = strings.ToLower(text(result["id"]))
		result["url"] = strings.ToLower(text(result["url"]))
		for _, key := range []string{"lastModified", "state", "stateTransitionTime", "previousState", "previousStateTransitionTime", "stats"} {
			delete(result, key)
		}
		if kind == batchJobType {
			if info, exists := result["executionInfo"]; exists {
				stable := map[string]any{}
				if value, exists := object(info)["poolId"]; exists {
					stable["poolId"] = strings.ToLower(text(value))
				}
				result["executionInfo"] = stable
			}
		}
		if kind == batchScheduleType {
			delete(result, "executionInfo")
		}
		if kind == batchTaskType {
			delete(result, "executionInfo")
			delete(result, "nodeInfo")
		}
		if kind == batchNodeType {
			for _, key := range []string{"lastBootTime", "totalTasksRun", "runningTasksCount", "runningTaskSlotsCount", "totalTasksSucceeded", "recentTasks", "startTaskInfo", "errors", "nodeAgentInfo"} {
				delete(result, key)
			}
		}
		return result
	}
	result["id"] = strings.ToLower(text(raw["id"]))
	result["name"] = last(text(result["id"]))
	if kind != batchAccountType {
		delete(result, "location")
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	switch kind {
	case batchAccountType:
		delete(props, "privateEndpointConnections")
		delete(object(props["autoStorage"]), "lastKeySync")
	case batchPoolType:
		for _, key := range []string{"lastModified", "provisioningStateTransitionTime", "allocationState", "allocationStateTransitionTime", "currentDedicatedNodes", "currentLowPriorityNodes", "autoScaleRun", "resizeOperationStatus"} {
			delete(props, key)
		}
	case batchPackageType:
		// GET/Activate omit these upload-only fields during the May 2026 rollout.
		// Older responses contain ephemeral SAS values. The native package ID,
		// activation time, format and state bind the actual package version.
		delete(props, "storageUrl")
		delete(props, "storageUrlExpiry")
	}
	return result
}

func batchNodePoolSnapshot(raw map[string]any) map[string]any {
	result := batchSnapshot(batchPoolType, raw)
	fixed := object(object(object(result["properties"])["scaleSettings"])["fixedScale"])
	// Removing an exact node changes desired capacity. Capacity is not that
	// node's incarnation; keep every other pool setting bound and condition the
	// native RemoveNodes request on the latest data-plane pool ETag.
	delete(fixed, "targetDedicatedNodes")
	delete(fixed, "targetLowPriorityNodes")
	return result
}
func batchConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, batchSnapshot(kind, raw))
}

func batchReady(kind string, raw map[string]any) error {
	if isBatchDataType(kind) {
		states := map[string][]string{batchScheduleType: {"active", "completed", "disabled"}, batchJobType: {"active", "completed", "disabled"}, batchTaskType: {"active", "preparing", "running", "completed"}, batchNodeType: {"idle", "running", "unusable", "starttaskfailed", "offline", "preempted", "deallocated"}}
		if !slices.Contains(states[kind], text(raw["state"])) {
			return serviceDenied("batch_resource_not_terminal")
		}
		return nil
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || props == nil {
		return serviceDenied("invalid_batch_properties")
	}
	if state := props["provisioningState"]; state != nil && !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Invalid"}, text(state)) {
		return serviceDenied("batch_resource_not_terminal")
	}
	if kind == batchPackageType && !slices.Contains([]string{"Active", "Pending"}, text(props["state"])) {
		return serviceDenied("batch_package_state_unknown")
	}
	return nil
}

func batchListQuery(endpoint string, dataPlane bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.RawPath != "" {
		return serviceDenied("invalid_batch_list_url")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != batchVersion {
		return serviceDenied("invalid_batch_list_version")
	}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return serviceDenied("invalid_batch_list_query")
		}
		switch name {
		case "api-version", "$skiptoken":
		case "maxresults":
			if values[0] != "1000" {
				return serviceDenied("invalid_batch_page_size")
			}
		default:
			return serviceDenied("filtered_batch_list_query")
		}
	}
	if dataPlane && !batchEndpointPattern.MatchString(u.Scheme+"://"+u.Host) {
		return serviceDenied("invalid_batch_list_endpoint")
	}
	return nil
}

func (c *client) batchDataList(ctx context.Context, account batchAccountContext, operation string, parameters map[string]any) ([]map[string]any, string, error) {
	requestID := ""
	data, err := providerData()
	if err != nil {
		return nil, requestID, err
	}
	op, ok := data.catalog.Operation(batchDataPrefix + operation)
	if !ok || op.Call == nil || op.Call.Method != "GET" || op.Pagination == nil && operation != "Tasks_ListSubTasks" {
		return nil, requestID, serviceDenied("invalid_batch_list_operation")
	}
	params := maps.Clone(parameters)
	if params == nil {
		params = map[string]any{}
	}
	params["endpoint"] = account.endpoint
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, requestID, err
	}
	initial, _ := url.Parse(request.URL)
	next := request.URL
	seen := map[string]bool{}
	var result []map[string]any
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || u.Path != initial.Path || seen[next] {
			return nil, requestID, serviceDenied("batch_list_collection_changed")
		}
		if err := batchListQuery(next, true); err != nil {
			return nil, requestID, err
		}
		seen[next] = true
		page, err := c.batchRequest(ctx, account, catalog.RESTRequest{Method: "GET", URL: next})
		if err != nil {
			return nil, requestID, err
		}
		requestID = page.requestID
		items, ok := page.data["value"].([]any)
		if !ok || page.status != 200 || page.data["error"] != nil || page.data["code"] != nil || page.data["nextLink"] != nil {
			return nil, requestID, serviceDenied("incomplete_batch_data_list")
		}
		for _, item := range items {
			raw, ok := item.(map[string]any)
			if !ok || raw == nil {
				return nil, requestID, serviceDenied("invalid_batch_list_item")
			}
			result = append(result, raw)
		}
		next = ""
		if value := page.data["odata.nextLink"]; value != nil {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return nil, requestID, serviceDenied("invalid_batch_list_continuation")
			}
		}
	}
	return result, requestID, nil
}

func batchState(kind string, raw map[string]any) string {
	if isBatchDataType(kind) {
		return text(raw["state"])
	}
	props := object(raw["properties"])
	if kind == batchPackageType {
		return text(props["state"])
	}
	return text(props["provisioningState"])
}

func batchIncarnation(c *client, planned asset.Asset, live map[string]any) error {
	kind := planned.Identity.NativeType
	if !isBatchType(kind) {
		return nil
	}
	if expected := text(planned.Normalized["_batch_configuration"]); expected == "" || expected != batchConfiguration(kind, live) {
		return serviceDenied("batch_configuration_changed")
	}
	if expected := text(planned.Normalized["_batch_private_configuration"]); expected == "" || expected != c.privateConfiguration(batchSnapshot(kind, live)) {
		return serviceDenied("batch_private_configuration_changed")
	}
	return nil
}

func batchAccountBinding(c *client, account batchAccountContext) string {
	return c.privateConfiguration(map[string]any{"id": account.id, "endpoint": account.endpoint, "location": account.location, "configuration": batchSnapshot(batchAccountType, account.raw)})
}
