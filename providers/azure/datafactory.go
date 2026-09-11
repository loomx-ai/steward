package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	dataFactoryType            = "Microsoft.DataFactory/factories"
	dataFactoryCDCType         = dataFactoryType + "/adfcdcs"
	dataFactoryCredentialType  = dataFactoryType + "/credentials"
	dataFactoryFlowType        = dataFactoryType + "/dataflows"
	dataFactoryDatasetType     = dataFactoryType + "/datasets"
	dataFactoryParametersType  = dataFactoryType + "/globalParameters"
	dataFactoryIRType          = dataFactoryType + "/integrationRuntimes"
	dataFactoryNodeType        = dataFactoryIRType + "/nodes"
	dataFactoryLinkedType      = dataFactoryType + "/linkedservices"
	dataFactoryNetworkType     = dataFactoryType + "/managedVirtualNetworks"
	dataFactoryEndpointType    = dataFactoryNetworkType + "/managedPrivateEndpoints"
	dataFactoryPipelineType    = dataFactoryType + "/pipelines"
	dataFactoryPECType         = dataFactoryType + "/privateEndpointConnections"
	dataFactoryTriggerType     = dataFactoryType + "/triggers"
	dataFactoryVersion         = "2018-06-01"
	dataFactoryPrefix          = "Azure.Microsoft.DataFactory."
	dataFactoryInventorySource = "data-factory"
)

func dataFactoryChildKinds(kind string) []string {
	switch kind {
	case dataFactoryType:
		return []string{dataFactoryCDCType, dataFactoryCredentialType, dataFactoryFlowType, dataFactoryDatasetType, dataFactoryParametersType, dataFactoryIRType, dataFactoryLinkedType, dataFactoryNetworkType, dataFactoryPipelineType, dataFactoryPECType, dataFactoryTriggerType}
	case dataFactoryIRType:
		return []string{dataFactoryNodeType}
	case dataFactoryNetworkType:
		return []string{dataFactoryEndpointType}
	}
	return nil
}

func dataFactoryKind(kind string) string {
	for _, candidate := range append(dataFactoryChildKinds(dataFactoryType), dataFactoryType, dataFactoryNodeType, dataFactoryEndpointType) {
		if strings.EqualFold(candidate, kind) {
			return candidate
		}
	}
	return ""
}

func dataFactoryRoot(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}

func dataFactoryParent(id, kind string) string {
	if kind == dataFactoryType {
		return ""
	}
	parts := strings.Split(id, "/")
	if len(parts) < 11 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "/")
}

func (c *client) dataFactoryIdentity(id, kind string) error {
	canonical, typ, err := parseID(id)
	if err != nil || canonical != id || dataFactoryKind(typ) != kind || kind == "" || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 7+2*(len(strings.Split(kind, "/"))-1) {
		return serviceDenied("invalid_datafactory_identity")
	}
	return nil
}

// Node GET returns the node itself, with no ARM id/name/type. The stable
// internal ARM identity is case-insensitive; the native nodeName stays intact
// in its signed request parameters and every native request.
func dataFactoryWireID(id, kind, nodeName string) (string, error) {
	if kind != dataFactoryNodeType {
		if nodeName != "" {
			return "", serviceDenied("unexpected_datafactory_node_name")
		}
		return id, nil
	}
	wire := dataFactoryParent(id, kind) + "/nodes/" + nodeName
	parsed, typ, err := parseID(wire)
	if err != nil || parsed != id || !strings.EqualFold(typ, kind) || nodeName == "" || nodeName != strings.TrimSpace(nodeName) || strings.ContainsAny(nodeName, "/\t") {
		return "", serviceDenied("invalid_datafactory_node_name")
	}
	return wire, nil
}

func dataFactoryMetadata(id, kind, nodeName string, raw map[string]any) error {
	if raw == nil || raw["error"] != nil {
		return serviceDenied("invalid_datafactory_resource_response")
	}
	if kind == dataFactoryNodeType {
		if raw["nodeName"] != nodeName || raw["id"] != nil || raw["type"] != nil {
			return serviceDenied("datafactory_node_identity_changed")
		}
		return nil
	}
	if !strings.EqualFold(text(raw["id"]), id) || !validResponseType(kind, text(raw["type"])) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("datafactory_resource_identity_changed")
	}
	if props, ok := raw["properties"].(map[string]any); !ok || props == nil {
		return serviceDenied("datafactory_properties_missing")
	}
	if kind == dataFactoryType && (text(raw["location"]) == "" || resourceRegion(raw) == "global") {
		return serviceDenied("invalid_datafactory_location")
	}
	if kind == dataFactoryIRType && !slices.Contains([]string{"Managed", "SelfHosted"}, text(object(raw["properties"])["type"])) {
		return serviceDenied("unknown_datafactory_runtime_type")
	}
	return nil
}

func dataFactorySnapshot(kind string, raw map[string]any) map[string]any {
	result := batchClone(raw)
	delete(result, "_datafactory_header_etag")
	if kind == dataFactoryNodeType {
		for _, key := range []string{"status", "lastConnectTime", "lastStartTime", "lastStopTime", "lastUpdateResult", "lastStartUpdateTime", "lastEndUpdateTime", "isActiveDispatcher", "version", "versionStatus", "expiryTime"} {
			delete(result, key)
		}
		return result // registerTime, machine/host and authored limits bind this node.
	}
	result["id"] = strings.ToLower(text(result["id"]))
	result["name"] = last(text(result["id"]))
	delete(result, "type")
	delete(result, "etag")
	delete(result, "eTag")
	if kind != dataFactoryType {
		delete(result, "location")
	}
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	if len(object(result["systemData"])) == 0 {
		delete(result, "systemData")
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	if kind == dataFactoryIRType {
		delete(props, "state")
	}
	if kind == dataFactoryTriggerType {
		delete(props, "runtimeState")
	}
	if kind == dataFactoryCDCType {
		delete(props, "status")
	}
	return result
}

// Authored pipelines, connection strings, parameters, scripts, node hosts and
// future fields remain private, even when their keys do not resemble secrets.
func dataFactorySafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "location", "tags", "nodeName", "status", "request_id", "status_code"} {
			if entry, ok := value[key]; ok {
				result[key] = entry
			}
		}
		if props, ok := value["properties"].(map[string]any); ok {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "createTime", "version", "publicNetworkAccess", "runtimeState", "status", "type", "state"} {
				if entry, ok := props[key].(string); ok {
					public[key] = entry
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if entry, ok := value[key]; ok {
				result[key] = dataFactorySafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = dataFactorySafeValue(entry)
		}
		return result
	default:
		return nil
	}
}

// This single native GET returns a JSON string. Do not relax the object
// contract for ordinary ARM resource reads or unrelated action responses.
func dataFactoryStringResponse(method string, u *url.URL) bool {
	if method != http.MethodGet || u.Host != "management.azure.com" || u.Query().Get("api-version") != dataFactoryVersion || len(u.Query()) != 1 || len(u.Query()["api-version"]) != 1 || u.RawPath != "" || !strings.HasSuffix(u.Path, "/status") {
		return false
	}
	id, kind, err := parseID(strings.TrimSuffix(u.Path, "/status"))
	return err == nil && strings.EqualFold(kind, dataFactoryCDCType) && len(strings.Split(id, "/")) == 11
}

// Azure CLI's native PipelineRuns_Cancel recording returns JSON "" with 200.
// Restrict that empty-string success response to this exact operation shape.
func dataFactoryCancelStringResponse(method string, u *url.URL) bool {
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || method != "POST" || u.Host != "management.azure.com" || u.RawPath != "" || len(query["api-version"]) != 1 || query.Get("api-version") != dataFactoryVersion {
		return false
	}
	for key, values := range query {
		if key == "api-version" {
			continue
		}
		if key != "isRecursive" || len(values) != 1 || !slices.Contains([]string{"true", "false"}, values[0]) {
			return false
		}
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 12 || !strings.EqualFold(parts[9], "pipelineruns") || !uuidPattern.MatchString(parts[10]) || parts[11] != "cancel" {
		return false
	}
	_, kind, err := parseID(strings.Join(parts[:9], "/"))
	return err == nil && strings.EqualFold(kind, dataFactoryType)
}

func dataFactoryETag(res response) (string, error) {
	values := res.header.Values("ETag")
	if len(values) > 1 {
		return "", serviceDenied("ambiguous_datafactory_etag")
	}
	for _, key := range []string{"etag", "eTag"} {
		if value, exists := res.data[key]; exists {
			stamp, ok := value.(string)
			if !ok {
				return "", serviceDenied("invalid_datafactory_etag")
			}
			values = append(values, stamp)
		}
	}
	stamp := ""
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || value == "*" || strings.ContainsAny(value, "\r\n\x00,") || stamp != "" && stamp != value {
			return "", serviceDenied("inconsistent_datafactory_etag")
		}
		stamp = value
	}
	return stamp, nil
}

func (c *client) dataFactoryRead(ctx context.Context, id, kind, nodeName string) (map[string]any, error) {
	if err := c.dataFactoryIdentity(id, kind); err != nil {
		return nil, err
	}
	wire, err := dataFactoryWireID(id, kind, nodeName)
	if err != nil {
		return nil, err
	}
	mapping, ok := findType(kind)
	if !ok {
		return nil, serviceDenied("datafactory_kind_unavailable")
	}
	op, params, err := c.resourceOperation(mapping, wire, "GET")
	if err != nil {
		return nil, err
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	response, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, err
	}
	if response.status != 200 || operationLocation(response.header) != "" {
		return nil, serviceDenied("incomplete_datafactory_read")
	}
	if err := dataFactoryMetadata(id, kind, nodeName, response.data); err != nil {
		return nil, err
	}
	stamp, err := dataFactoryETag(response)
	if err != nil {
		return nil, err
	}
	if response.header.Get("ETag") != "" {
		response.data["_datafactory_header_etag"] = stamp
	}
	return response.data, nil
}

func (c *client) dataFactoryOperation(id, kind, operation string, extra map[string]any) (catalog.RESTRequest, error) {
	if err := c.dataFactoryIdentity(id, kind); err != nil {
		return catalog.RESTRequest{}, err
	}
	mapping, ok := findType(kind)
	if !ok {
		return catalog.RESTRequest{}, serviceDenied("datafactory_kind_unavailable")
	}
	_, params, err := c.resourceOperation(mapping, id, "GET")
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	op, ok := metadata.catalog.Operation(dataFactoryPrefix + operation)
	if !ok || op.Call == nil || op.Call.Version != dataFactoryVersion || op.Call.Style != "azure-rest" {
		return catalog.RESTRequest{}, serviceDenied("invalid_datafactory_operation")
	}
	for key := range extra {
		if _, exists := params[key]; exists {
			return catalog.RESTRequest{}, serviceDenied("datafactory_operation_identity_changed")
		}
	}
	maps.Copy(params, extra)
	request, err := bindAzureREST(op, params)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	u, _ := url.Parse(request.URL)
	if !strings.EqualFold(u.Path, id) && !strings.HasPrefix(strings.ToLower(u.Path), id+"/") {
		return catalog.RESTRequest{}, serviceDenied("datafactory_operation_scope_changed")
	}
	return request, nil
}

func (c *client) dataFactoryStatus(ctx context.Context, id string, raw map[string]any) (map[string]any, error) {
	request, err := c.dataFactoryOperation(id, dataFactoryIRType, "IntegrationRuntimes_GetStatus", nil)
	if err != nil {
		return nil, err
	}
	result, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, err
	}
	props := object(result.data["properties"])
	typ := text(object(raw["properties"])["type"])
	if result.status != 200 || operationLocation(result.header) != "" || result.data["error"] != nil || !strings.EqualFold(text(result.data["name"]), last(id)) || props == nil || props["type"] != typ || (props["dataFactoryName"] != nil && !strings.EqualFold(text(props["dataFactoryName"]), last(dataFactoryRoot(id)))) {
		return nil, serviceDenied("invalid_datafactory_runtime_status")
	}
	if _, ok := props["typeProperties"].(map[string]any); !ok {
		return nil, serviceDenied("datafactory_runtime_status_properties_missing")
	}
	return result.data, nil
}
