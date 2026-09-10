package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	streamAnalyticsJobType            = "Microsoft.StreamAnalytics/streamingjobs"
	streamAnalyticsInputType          = streamAnalyticsJobType + "/inputs"
	streamAnalyticsOutputType         = streamAnalyticsJobType + "/outputs"
	streamAnalyticsFunctionType       = streamAnalyticsJobType + "/functions"
	streamAnalyticsTransformationType = streamAnalyticsJobType + "/transformations"
	streamAnalyticsClusterType        = "Microsoft.StreamAnalytics/clusters"
	streamAnalyticsEndpointType       = streamAnalyticsClusterType + "/privateEndpoints"
)

func streamAnalyticsKind(kind string) string {
	for _, value := range []string{streamAnalyticsJobType, streamAnalyticsInputType, streamAnalyticsOutputType, streamAnalyticsFunctionType, streamAnalyticsTransformationType, streamAnalyticsClusterType, streamAnalyticsEndpointType} {
		if strings.EqualFold(kind, value) {
			return value
		}
	}
	return ""
}
func isStreamAnalyticsType(kind string) bool { return streamAnalyticsKind(kind) != "" }
func streamAnalyticsOwnedKinds(kind string) []string {
	switch streamAnalyticsKind(kind) {
	case streamAnalyticsJobType:
		return []string{streamAnalyticsInputType, streamAnalyticsOutputType, streamAnalyticsFunctionType, streamAnalyticsTransformationType}
	case streamAnalyticsClusterType:
		return []string{streamAnalyticsEndpointType}
	}
	return nil
}

// Keep query text, connection credentials and all authored properties in the
// private digest. Runtime progress and indexes are verified separately.
func streamAnalyticsSnapshot(kind string, raw map[string]any) map[string]any {
	encoded, _ := json.Marshal(raw)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.Decode(&result)
	result["id"] = strings.ToLower(text(raw["id"]))
	result["name"] = last(text(result["id"]))
	delete(result, "type")
	delete(result, "etag")
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	delete(props, "etag")
	delete(props, "diagnostics")
	switch streamAnalyticsKind(kind) {
	case streamAnalyticsJobType:
		for _, key := range []string{"inputs", "outputs", "functions", "transformation", "jobState", "lastOutputEventTime"} {
			delete(props, key)
		}
	case streamAnalyticsClusterType:
		delete(props, "capacityAllocated")
		delete(props, "capacityAssigned")
	default:
		delete(result, "location") // Proxy resources inherit their owning root.
	}
	return result
}
func streamAnalyticsConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, streamAnalyticsSnapshot(kind, raw))
}
func streamAnalyticsIncarnation(planned asset.Asset, live map[string]any) error {
	if isStreamAnalyticsType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_stream_analytics_configuration"]); expected == "" || expected != streamAnalyticsConfiguration(planned.Identity.NativeType, live) {
			return serviceDenied("stream_analytics_configuration_changed")
		}
	}
	return nil
}

// A list may omit properties present in GET. Every field it does provide must
// still match, including credentials and embedded code omitted from public data.
func streamAnalyticsListedIncarnation(kind string, listed, live map[string]any) error {
	var contains func(any, any) bool
	contains = func(expected, actual any) bool {
		switch expected := expected.(type) {
		case map[string]any:
			current, ok := actual.(map[string]any)
			if !ok {
				return false
			}
			for key, value := range expected {
				if !contains(value, current[key]) {
					return false
				}
			}
			return true
		case []any:
			current, ok := actual.([]any)
			if !ok || len(expected) != len(current) {
				return false
			}
			for i, value := range expected {
				if !contains(value, current[i]) {
					return false
				}
			}
			return true
		default:
			return reflect.DeepEqual(expected, actual)
		}
	}
	if !contains(streamAnalyticsSnapshot(kind, listed), streamAnalyticsSnapshot(kind, live)) {
		return serviceDenied("stream_analytics_listed_configuration_changed")
	}
	return nil
}

func streamAnalyticsListQuery(u *url.URL) error {
	if !strings.Contains(strings.ToLower(u.Path), "/providers/microsoft.streamanalytics/") {
		return nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != "2020-03-01" || u.RawPath != "" && u.RawPath != u.Path {
		return serviceDenied("invalid_stream_analytics_list_query")
	}
	for name, values := range query {
		if name != "api-version" && (name != "$skiptoken" || len(values) != 1 || values[0] == "") {
			return serviceDenied("filtered_stream_analytics_list_query")
		}
	}
	return nil
}
func (a *action) streamAnalyticsRequestIdentity(value asset.Asset) error {
	if !isStreamAnalyticsType(a.kind.NativeType) {
		return nil
	}
	id, kind, err := parseID(value.Identity.NativeID)
	if err != nil || value.Identity.Provider != asset.ProviderAzure || id != a.id || !strings.EqualFold(kind, a.kind.NativeType) || !strings.EqualFold(value.Identity.NativeType, a.kind.NativeType) {
		return serviceDenied("stream_analytics_request_identity_changed")
	}
	return nil
}
func streamAnalyticsParentID(id, kind string) string {
	if isStreamAnalyticsType(kind) && kind != streamAnalyticsJobType && kind != streamAnalyticsClusterType {
		return redisParentID(id)
	}
	return ""
}
func streamAnalyticsJobCluster(raw map[string]any) (string, error) {
	value := object(raw["properties"])["cluster"]
	if value == nil {
		return "", nil
	}
	cluster, ok := value.(map[string]any)
	id, kind, err := parseID(text(cluster["id"]))
	if !ok || err != nil || !strings.EqualFold(kind, streamAnalyticsClusterType) {
		return "", serviceDenied("invalid_stream_analytics_job_cluster")
	}
	return id, nil
}
func validateStreamAnalytics(kind string, raw map[string]any) error {
	props, ok := raw["properties"].(map[string]any)
	if !ok || props == nil {
		return serviceDenied("invalid_stream_analytics_properties")
	}
	switch streamAnalyticsKind(kind) {
	case streamAnalyticsJobType:
		_, err := streamAnalyticsJobCluster(raw)
		return err
	case streamAnalyticsInputType:
		if props["type"] != "Stream" && props["type"] != "Reference" {
			return serviceDenied("unknown_stream_analytics_input_type")
		}
	case streamAnalyticsFunctionType:
		if props["type"] != "Scalar" && props["type"] != "Aggregate" {
			return serviceDenied("unknown_stream_analytics_function_type")
		}
	case streamAnalyticsEndpointType:
		_, err := streamAnalyticsEndpointTargets(raw)
		return err
	}
	return nil
}
func streamAnalyticsReady(kind string, raw map[string]any) error {
	if err := validateStreamAnalytics(kind, raw); err != nil {
		return err
	}
	props := object(raw["properties"])
	if state := props["provisioningState"]; state != nil && state != "Succeeded" && state != "Failed" && state != "Canceled" {
		return serviceDenied("stream_analytics_resource_not_terminal")
	}
	if streamAnalyticsKind(kind) == streamAnalyticsJobType && !slices.Contains([]string{"Created", "Running", "Stopped", "Failed", "Degraded"}, text(props["jobState"])) {
		return serviceDenied("stream_analytics_job_not_terminal")
	}
	return nil
}
func streamAnalyticsProtection(kind string) string {
	if kind == streamAnalyticsTransformationType {
		return "azure_stream_analytics_transformation"
	}
	return ""
}
func (c *client) streamAnalyticsResource(ctx context.Context, id string) (map[string]any, error) {
	_, kind, err := parseID(id)
	if err != nil || !isStreamAnalyticsType(kind) {
		return nil, serviceDenied("invalid_stream_analytics_resource")
	}
	raw, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	return raw, validateStreamAnalytics(kind, raw)
}
func (c *client) streamAnalyticsInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isStreamAnalyticsType(kind) {
		return nil
	}
	if err := validateStreamAnalytics(kind, raw); err != nil {
		return err
	}
	normalized["_stream_analytics_configuration"] = streamAnalyticsConfiguration(kind, raw)
	normalized["_stream_analytics_private_configuration"] = c.privateConfiguration(streamAnalyticsSnapshot(kind, raw))
	if parentID := streamAnalyticsParentID(id, kind); parentID != "" {
		parent, err := c.streamAnalyticsResource(ctx, parentID)
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(parentID)
		normalized["_stream_analytics_parent_configuration"] = c.privateConfiguration(streamAnalyticsSnapshot(parentKind, parent))
		normalized["_stream_analytics_location"] = resourceRegion(parent)
	}
	if kind == streamAnalyticsEndpointType {
		targets, err := streamAnalyticsEndpointTargets(raw)
		if err != nil {
			return err
		}
		bindings := map[string]any{}
		for _, id := range targets {
			target, err := c.linkedResource(ctx, id)
			if err != nil {
				return err
			}
			bindings[id] = c.privateConfiguration(searchTargetSnapshot(target))
		}
		normalized["_stream_analytics_target_configurations"] = bindings
	}
	refs, err := c.streamAnalyticsReferences(ctx, kind, raw)
	if err != nil {
		return err
	}
	normalized["_stream_analytics_references"] = refs
	return nil
}
func (a *action) streamAnalyticsPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if !isStreamAnalyticsType(a.kind.NativeType) {
		return nil
	}
	if err := streamAnalyticsReady(a.kind.NativeType, raw); err != nil {
		return err
	}
	if err := streamAnalyticsIncarnation(planned, raw); err != nil {
		return err
	}
	if parentID := streamAnalyticsParentID(a.id, a.kind.NativeType); parentID != "" {
		parent, err := a.client.streamAnalyticsResource(ctx, parentID)
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(parentID)
		if err := streamAnalyticsReady(parentKind, parent); err != nil {
			return err
		}
		if expected := text(planned.Normalized["_stream_analytics_parent_configuration"]); expected == "" || expected != a.client.privateConfiguration(streamAnalyticsSnapshot(parentKind, parent)) {
			return serviceDenied("stream_analytics_parent_changed")
		}
		// Editing a job's definition requires a stopped job. Deleting the job
		// itself is supported while Running/Degraded and uses its native cascade.
		if strings.EqualFold(parentKind, streamAnalyticsJobType) && !slices.Contains([]string{"Created", "Stopped", "Failed"}, text(object(parent["properties"])["jobState"])) {
			return serviceDenied("stream_analytics_parent_job_must_be_stopped")
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return err
		}
		if locked(parentID, locks) {
			return serviceDenied("azure_management_lock")
		}
		if err := a.client.linkedResourceProtection(ctx, parentID, parent, locks); err != nil {
			return err
		}
		current, err := a.client.streamAnalyticsResource(ctx, parentID)
		if err != nil {
			return err
		}
		if err := streamAnalyticsReady(parentKind, current); err != nil {
			return err
		}
		if object(parent["properties"])["jobState"] != object(current["properties"])["jobState"] || a.client.privateConfiguration(streamAnalyticsSnapshot(parentKind, parent)) != a.client.privateConfiguration(streamAnalyticsSnapshot(parentKind, current)) {
			return serviceDenied("stream_analytics_parent_changed")
		}
	}
	return a.streamAnalyticsTargetPreflight(ctx, planned, raw)
}
func streamAnalyticsEndpointTargets(raw map[string]any) ([]string, error) {
	values, ok := object(raw["properties"])["manualPrivateLinkServiceConnections"].([]any)
	if !ok || len(values) == 0 {
		return nil, serviceDenied("invalid_stream_analytics_endpoint_connections")
	}
	seen := map[string]bool{}
	var targets []string
	for _, value := range values {
		props := object(object(value)["properties"])
		id, _, err := parseID(text(props["privateLinkServiceId"]))
		groups, ok := props["groupIds"].([]any)
		if err != nil || seen[id] || !ok || len(groups) == 0 {
			return nil, serviceDenied("invalid_stream_analytics_endpoint_target")
		}
		seenGroups := map[string]bool{}
		for _, group := range groups {
			name, err := kustoName(group)
			if err != nil || seenGroups[name] {
				return nil, serviceDenied("invalid_stream_analytics_endpoint_group")
			}
			seenGroups[name] = true
		}
		seen[id] = true
		targets = append(targets, id)
	}
	slices.Sort(targets)
	return targets, nil
}
func (a *action) streamAnalyticsTargetPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if a.kind.NativeType != streamAnalyticsEndpointType {
		return nil
	}
	targets, err := streamAnalyticsEndpointTargets(raw)
	if err != nil {
		return err
	}
	bindings := object(planned.Normalized["_stream_analytics_target_configurations"])
	if len(bindings) != len(targets) {
		return serviceDenied("stream_analytics_endpoint_targets_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	for _, id := range targets {
		target, err := a.client.linkedResource(ctx, id)
		if err != nil {
			return err
		}
		expected := text(bindings[id])
		if expected == "" || expected != a.client.privateConfiguration(searchTargetSnapshot(target)) {
			return serviceDenied("stream_analytics_endpoint_target_changed")
		}
		if locked(id, locks) {
			return serviceDenied("azure_management_lock")
		}
		if err := a.client.linkedResourceProtection(ctx, id, target, locks); err != nil {
			return err
		}
		current, err := a.client.linkedResource(ctx, id)
		if err != nil {
			return err
		}
		if expected != a.client.privateConfiguration(searchTargetSnapshot(current)) {
			return serviceDenied("stream_analytics_endpoint_target_changed")
		}
	}
	return nil
}

// The transformation is a named singleton embedded in an explicitly expanded
// native job GET. Its name is supplied by Azure; there is no list or DELETE API.
func (c *client) streamAnalyticsTransformationPage(ctx context.Context, endpoint, parentID string) ([]any, string, response, error) {
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Path, parentID) || u.Query().Get("$expand") != "transformation" || len(u.Query()) != 2 || len(u.Query()["$expand"]) != 1 || len(u.Query()["api-version"]) != 1 || u.Query().Get("api-version") != "2020-03-01" {
		return nil, "", response{}, serviceDenied("invalid_stream_analytics_transformation_expansion")
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, "", response{}, err
	}
	if !validResourceResponse(res, parentID, streamAnalyticsJobType) || res.data["nextLink"] != nil {
		return nil, "", response{}, serviceDenied("incomplete_stream_analytics_transformation_parent")
	}
	if err := validateStreamAnalytics(streamAnalyticsJobType, res.data); err != nil {
		return nil, "", response{}, err
	}
	value := object(res.data["properties"])["transformation"]
	if value == nil {
		return nil, "", res, nil
	}
	transformation, ok := value.(map[string]any)
	id, kind, err := parseID(text(transformation["id"]))
	if !ok || err != nil || !strings.EqualFold(kind, streamAnalyticsTransformationType) || !strings.EqualFold(redisParentID(id), parentID) || !validResponseType(streamAnalyticsTransformationType, text(transformation["type"])) || !strings.EqualFold(text(transformation["name"]), last(id)) {
		return nil, "", response{}, serviceDenied("invalid_stream_analytics_transformation_identity")
	}
	return []any{transformation}, "", res, nil
}
func (c *client) streamAnalyticsTransformation(ctx context.Context, parent asset.Identity) ([]serviceChild, error) {
	kind, _ := findType(streamAnalyticsJobType)
	endpoint, err := c.resourceURL(kind, parent.NativeID)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	q := u.Query()
	q.Set("$expand", "transformation")
	u.RawQuery = q.Encode()
	values, _, _, err := c.streamAnalyticsTransformationPage(ctx, u.String(), parent.NativeID)
	if err != nil {
		return nil, err
	}
	var result []serviceChild
	for _, value := range values {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		current, err := c.streamAnalyticsResource(ctx, id)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(streamAnalyticsSnapshot(streamAnalyticsTransformationType, raw)) != c.privateConfiguration(streamAnalyticsSnapshot(streamAnalyticsTransformationType, current)) {
			return nil, serviceDenied("stream_analytics_transformation_changed")
		}
		result = append(result, serviceChild{kind: streamAnalyticsTransformationType, id: id, data: current})
	}
	return result, nil
}

func streamAnalyticsClusterPrerequisite(parent, child asset.Asset) bool {
	if parent.Identity.NativeType != streamAnalyticsClusterType || child.Identity.NativeType != streamAnalyticsJobType {
		return false
	}
	id, err := streamAnalyticsJobCluster(map[string]any{"properties": child.Normalized})
	return err == nil && strings.EqualFold(id, parent.Identity.NativeID)
}

// Native Location URLs bind the complete resource and carry a timestamp/GUID
// operation name and four signing parameters. Never use their query in logs.
var streamAnalyticsOperationName = regexp.MustCompile(`^[0-9]{19}\.[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// The official private-endpoint DELETE recording ends its Location poll with
// HTTP 200 and {status: InProgress, error: null}. Allow resource readback for
// this exact envelope; Wait still requires an independent resource GET 404.
func (a *action) streamAnalyticsLocationReadback(result contracts.ActionResult, res response) bool {
	_, hasError := res.data["error"]
	return a.kind.NativeType == streamAnalyticsEndpointType && text(result.Data["polling"]) == "location" && res.status == 200 && len(res.data) == 2 && res.data["status"] == "InProgress" && hasError && res.data["error"] == nil
}

func (a *action) streamAnalyticsPollReceipt(result *contracts.ActionResult) error {
	if !isStreamAnalyticsType(a.kind.NativeType) {
		return nil
	}
	if text(result.Data["stream_analytics_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
		return fmt.Errorf("Stream Analytics polling receipt does not match its resource")
	}
	next, exists := result.Data["stream_analytics_poll_operation"]
	if !exists {
		if result.Data["stream_analytics_poll_binding"] != nil {
			return fmt.Errorf("incomplete Stream Analytics polling receipt")
		}
		return nil
	}
	if text(result.Data["polling"]) != "location" || a.validateStreamAnalyticsNextPoll(result.ProviderOperationID, text(next)) != nil || text(result.Data["stream_analytics_poll_binding"]) != a.operationBinding(text(next)) {
		return fmt.Errorf("invalid Stream Analytics resumed polling receipt")
	}
	result.ProviderOperationID = text(next)
	return nil
}

func (a *action) validateStreamAnalyticsNextPoll(previous, next string) error {
	if err := a.validateOperationURL(next); err != nil {
		return err
	}
	before, _ := url.Parse(previous)
	after, _ := url.Parse(next)
	if !strings.EqualFold(before.Path, after.Path) {
		return fmt.Errorf("Stream Analytics polling operation changed")
	}
	return nil
}

// Azure refreshes the signed Location on each HTTP 202. Freeze each validated
// successor in Wait.Data so the existing execution journal can resume it.
func (a *action) streamAnalyticsNextPoll(result contracts.ActionResult, res response) (map[string]any, error) {
	if !isStreamAnalyticsType(a.kind.NativeType) || len(res.header.Values("Location")) == 0 {
		return nil, nil
	}
	if len(res.header.Values("Location")) != 1 || res.header.Get("Azure-AsyncOperation") != "" || text(result.Data["polling"]) != "location" {
		return nil, fmt.Errorf("Stream Analytics polling protocol changed")
	}
	next := res.header.Get("Location")
	if err := a.validateStreamAnalyticsNextPoll(result.ProviderOperationID, next); err != nil {
		return nil, err
	}
	data := maps.Clone(result.Data)
	data["stream_analytics_poll_operation"] = next
	data["stream_analytics_poll_binding"] = a.operationBinding(next)
	return data, nil
}

func validateStreamAnalyticsOperationURL(subscription, id, version, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32*1024 || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" && u.RawPath != u.Path {
		return fmt.Errorf("invalid Stream Analytics operation endpoint")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) < 3 || !strings.EqualFold(parts[len(parts)-2], "operationResults") || !streamAnalyticsOperationName.MatchString(parts[len(parts)-1]) {
		return fmt.Errorf("invalid Stream Analytics operation path")
	}
	owner, kind, err := parseID(strings.Join(parts[:len(parts)-2], "/"))
	if err != nil || !strings.HasPrefix(owner, "/subscriptions/"+strings.ToLower(subscription)+"/") || !slices.Contains([]string{streamAnalyticsJobType, streamAnalyticsClusterType, streamAnalyticsEndpointType}, streamAnalyticsKind(kind)) || id != "" && !strings.EqualFold(owner, id) {
		return fmt.Errorf("Stream Analytics operation belongs to another resource")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 5 || len(query["api-version"]) != 1 || query.Get("api-version") != version {
		return fmt.Errorf("invalid Stream Analytics operation version")
	}
	for _, name := range []string{"t", "c", "s", "h"} {
		if len(query[name]) != 1 || query.Get(name) == "" || strings.ContainsAny(query.Get(name), "\r\n\x00") {
			return fmt.Errorf("invalid Stream Analytics signed polling parameters")
		}
	}
	return nil
}

func streamAnalyticsSafeProperties(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			switch strings.ToLower(key) {
			case "query", "fullsnapshotquery", "deltasnapshotquery", "script", "accountkey", "sharedaccesspolicykey", "refreshtoken", "accesstoken", "apikey", "functionkey", "clientsecret", "certificate", "privatekey":
				continue
			}
			if strings.HasSuffix(strings.ToLower(key), "url") || strings.HasSuffix(strings.ToLower(key), "uri") || strings.EqualFold(key, "endpoint") {
				if u, err := url.Parse(text(value)); err == nil && u.Scheme != "" {
					u.User, u.RawQuery, u.Fragment = nil, "", ""
					result[key] = u.String()
					continue
				}
			}
			result[key] = streamAnalyticsSafeProperties(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, value := range typed {
			result[i] = streamAnalyticsSafeProperties(value)
		}
		return result
	default:
		return value
	}
}
