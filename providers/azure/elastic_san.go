package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"
)

const elasticSanVersion = "2026-04-01-preview"
const elasticSanType = "Microsoft.ElasticSan/elasticSans"
const elasticSanGroupType = elasticSanType + "/volumegroups"
const elasticSanVolumeType = elasticSanGroupType + "/volumes"
const elasticSanSnapshotType = elasticSanGroupType + "/snapshots"
const elasticSanEndpointType = elasticSanType + "/privateEndpointConnections"

func elasticSanKind(value string) string {
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		if strings.EqualFold(value, kind) {
			return kind
		}
	}
	return ""
}

func elasticSanOperations(kind string) (read, list, parent string) {
	switch kind {
	case elasticSanType:
		return "ElasticSans_Get", "ElasticSans_ListBySubscription", ""
	case elasticSanGroupType:
		return "VolumeGroups_Get", "VolumeGroups_ListByElasticSan", elasticSanType
	case elasticSanVolumeType:
		return "Volumes_Get", "Volumes_ListByVolumeGroup", elasticSanGroupType
	case elasticSanSnapshotType:
		return "VolumeSnapshots_Get", "VolumeSnapshots_ListByVolumeGroup", elasticSanGroupType
	case elasticSanEndpointType:
		return "PrivateEndpointConnections_Get", "PrivateEndpointConnections_List", elasticSanType
	}
	return "", "", ""
}

func elasticSanParent(id, kind string) string {
	if kind == elasticSanType {
		return ""
	}
	return id[:strings.LastIndex(id[:strings.LastIndex(id, "/")], "/")]
}

func (c *client) elasticSanIdentity(wire, kind string) (string, error) {
	id, typ, err := parseID(wire)
	depth := 9
	if kind == elasticSanGroupType || kind == elasticSanEndpointType {
		depth = 11
	} else if kind == elasticSanVolumeType || kind == elasticSanSnapshotType {
		depth = 13
	}
	if err != nil || wire != strings.TrimSpace(wire) || kind == "" || elasticSanKind(typ) != kind || len(strings.Split(id, "/")) != depth || !strings.HasPrefix(id, c.root()+"/resourcegroups/") {
		return "", serviceDenied("invalid_elastic_san_identity")
	}
	// Retained rows may have no usable GET, but their path parameters must
	// still satisfy the native resource contract before becoming inventory.
	read, _, _ := elasticSanOperations(kind)
	if _, _, err := c.resourceOperation(resourceType{NativeType: kind, ReadOperations: []string{"Azure.Microsoft.ElasticSan." + read}}, id, "GET"); err != nil {
		return "", serviceDenied("invalid_elastic_san_native_name")
	}
	return id, nil
}

// A retained LIST is a distinct authority: native GET has no retained selector.
// This helper deliberately returns GET's 404 without interpreting it as absence.
func (c *client) elasticSanRead(ctx context.Context, id, kind string) (response, error) {
	canonical, err := c.elasticSanIdentity(id, kind)
	if err != nil || canonical != id {
		return response{}, serviceDenied("invalid_elastic_san_read_identity")
	}
	read, _, _ := elasticSanOperations(kind)
	endpoint, err := c.resourceURL(resourceType{NativeType: kind, ReadOperations: []string{"Azure.Microsoft.ElasticSan." + read}}, id)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return res, err
	}
	return res, c.elasticSanReadResponse(res, id, kind)
}

// Keep header selection unchanged on every page. An incomplete collection,
// including a missing parent, is an error rather than an empty inventory.
func (c *client) elasticSanIndex(ctx context.Context, kind, parent string, retained bool) ([]any, string, error) {
	_, list, parentKind := elasticSanOperations(kind)
	if list == "" || retained && kind != elasticSanGroupType && kind != elasticSanVolumeType {
		return nil, "", serviceDenied("invalid_elastic_san_collection")
	}
	params := map[string]any{"subscriptionId": c.subscription}
	if parentKind != "" {
		canonical, err := c.elasticSanIdentity(parent, parentKind)
		if err != nil || canonical != parent {
			return nil, "", serviceDenied("invalid_elastic_san_parent")
		}
		parts := strings.Split(parent, "/")
		params["resourceGroupName"], params["elasticSanName"] = parts[4], parts[8]
		if parentKind == elasticSanGroupType {
			params["volumeGroupName"] = parts[10]
		}
	} else if parent != "" {
		return nil, "", serviceDenied("invalid_elastic_san_root_collection")
	}
	if kind == elasticSanGroupType || kind == elasticSanVolumeType {
		params["x-ms-access-soft-deleted-resources"] = "false"
		if retained {
			params["x-ms-access-soft-deleted-resources"] = "true"
		}
	}
	metadata, err := providerData()
	if err != nil {
		return nil, "", err
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.ElasticSan." + list)
	if !ok || op.Call == nil || op.Call.Version != elasticSanVersion || op.Call.Method != "GET" {
		return nil, "", serviceDenied("invalid_elastic_san_list_contract")
	}
	bound, err := bindAzureREST(op, params)
	if err != nil {
		return nil, "", err
	}
	collection, _ := url.Parse(bound.URL)
	rows, pages, identities, requestID := []any{}, map[string]bool{}, map[string]bool{}, ""
	for next := bound.URL; next != ""; {
		pageKey, err := c.elasticSanPageCursor(next, collection)
		if err != nil {
			return nil, "", err
		}
		if pages[pageKey] {
			return nil, "", serviceDenied("repeated_elastic_san_page")
		}
		pages[pageKey] = true
		res, err := c.requestBody(ctx, bound.Method, next, nil, bound.Headers)
		if err != nil {
			return nil, "", err
		}
		values, err := c.elasticSanPageResponse(res, kind, collection)
		if err != nil {
			return nil, "", err
		}
		for _, value := range values {
			id, _ := c.elasticSanRecord(object(value), kind)
			if identities[id] {
				return nil, "", serviceDenied("invalid_elastic_san_collection_record")
			}
			identities[id] = true
		}
		rows = append(rows, values...)
		if res.requestID != "" {
			requestID = res.requestID
		}
		next = ""
		if value, exists := res.data["nextLink"]; exists && value != nil {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return nil, "", serviceDenied("invalid_elastic_san_next_link")
			}
		}
	}
	return rows, requestID, nil
}

func (c *client) elasticSanRecord(raw map[string]any, kind string) (string, error) {
	wire, _ := raw["id"].(string)
	id, err := c.elasticSanIdentity(wire, kind)
	name, nameOK := raw["name"].(string)
	typ, typeOK := raw["type"].(string)
	props := object(raw["properties"])
	if err != nil || !nameOK || !typeOK || !strings.EqualFold(name, last(id)) || !strings.EqualFold(typ, kind) || props == nil {
		return "", serviceDenied("invalid_elastic_san_record")
	}
	for _, key := range []string{"location", "etag", "eTag", "managedBy", "kind"} {
		if value := raw[key]; value != nil {
			v, ok := value.(string)
			if !ok || v != strings.TrimSpace(v) || key == "location" && v == "" {
				return "", serviceDenied("invalid_elastic_san_metadata")
			}
		}
	}
	if kind == elasticSanType && text(raw["location"]) == "" {
		return "", serviceDenied("missing_elastic_san_location")
	}
	if value := raw["tags"]; value != nil {
		tags, ok := value.(map[string]any)
		if !ok {
			return "", serviceDenied("invalid_elastic_san_tags")
		}
		for _, value := range tags {
			if _, ok := value.(string); !ok {
				return "", serviceDenied("invalid_elastic_san_tag_value")
			}
		}
	}
	for _, key := range []string{"provisioningState", "volumeId", "volumeName", "protocolType", "encryption", "publicNetworkAccess"} {
		if value := props[key]; value != nil {
			if _, ok := value.(string); !ok {
				return "", serviceDenied("invalid_elastic_san_property")
			}
		}
	}
	if value := props["volumeId"]; value != nil && !uuidPattern.MatchString(value.(string)) {
		return "", serviceDenied("invalid_elastic_san_volume_identity")
	}
	if value := props["sku"]; value != nil {
		sku := object(value)
		if sku == nil {
			return "", serviceDenied("invalid_elastic_san_sku")
		}
		for _, key := range []string{"name", "tier"} {
			if value := sku[key]; value != nil {
				if _, ok := value.(string); !ok {
					return "", serviceDenied("invalid_elastic_san_sku_value")
				}
			}
		}
	}
	for _, key := range []string{"availabilityZones", "groupIds"} {
		if value := props[key]; value != nil {
			values, ok := value.([]any)
			if !ok {
				return "", serviceDenied("invalid_elastic_san_string_list")
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return "", serviceDenied("invalid_elastic_san_string_list_value")
				}
			}
		}
	}
	if value := props["privateLinkServiceConnectionState"]; value != nil {
		state := object(value)
		if state == nil {
			return "", serviceDenied("invalid_elastic_san_connection_state")
		}
		for _, key := range []string{"status", "actionsRequired"} {
			if value := state[key]; value != nil {
				if _, ok := value.(string); !ok {
					return "", serviceDenied("invalid_elastic_san_connection_state_value")
				}
			}
		}
	}
	for _, key := range []string{"baseSizeTiB", "extendedCapacitySizeTiB", "totalVolumeSizeGiB", "volumeGroupCount", "totalIops", "totalMBps", "totalSizeTiB", "sizeGiB", "sourceVolumeSizeGiB"} {
		if value := props[key]; value != nil {
			if v, err := batchInteger(value, 64); err != nil || v < 0 {
				return "", serviceDenied("invalid_elastic_san_capacity")
			}
		}
	}
	for _, key := range []string{"enforceDataIntegrityCheckForIscsi", "encryptionInTransit"} {
		if value := props[key]; value != nil {
			if _, ok := value.(bool); !ok {
				return "", serviceDenied("invalid_elastic_san_switch")
			}
		}
	}
	if value := props["deleteRetentionPolicy"]; value != nil {
		policy := object(value)
		if policy == nil {
			return "", serviceDenied("invalid_elastic_san_retention")
		}
		if value := policy["policyState"]; value != nil {
			if _, ok := value.(string); !ok {
				return "", serviceDenied("invalid_elastic_san_retention_state")
			}
		}
		if value := policy["retentionPeriodDays"]; value != nil {
			if v, err := batchInteger(value, 32); err != nil || v < 0 {
				return "", serviceDenied("invalid_elastic_san_retention_days")
			}
		}
	}
	return id, nil
}

func (c *client) elasticSanReadResponse(res response, id, kind string) error {
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return serviceDenied("incomplete_elastic_san_read")
	}
	actual, err := c.elasticSanRecord(res.data, kind)
	if err != nil || actual != id {
		return serviceDenied("invalid_elastic_san_read_response")
	}
	return nil
}

func (c *client) elasticSanPageCursor(next string, collection *url.URL) (string, error) {
	if err := c.validateURL(next); err != nil {
		return "", err
	}
	u, err := url.Parse(next)
	if err != nil || u.RawPath != "" || !strings.EqualFold(u.Path, collection.Path) {
		return "", serviceDenied("invalid_elastic_san_collection_cursor")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Get("api-version") != elasticSanVersion {
		return "", serviceDenied("invalid_elastic_san_cursor_version")
	}
	original := collection.Query()
	cursorFields := []string{"$skiptoken", "$skipToken", "skipToken", "continuationToken"}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" || !slices.Contains(cursorFields, name) && !slices.Equal(values, original[name]) {
			return "", serviceDenied("filtered_elastic_san_collection")
		}
	}
	// Public snapshot lists can explicitly filter by volume. Inventory supplies
	// no filter; in both cases the next page must preserve the original query.
	for name, values := range original {
		if !slices.Contains(cursorFields, name) && !slices.Equal(values, query[name]) {
			return "", serviceDenied("elastic_san_cursor_query_changed")
		}
	}
	return strings.ToLower(u.Path) + "?" + query.Encode(), nil
}

func (c *client) elasticSanPageResponse(res response, kind string, collection *url.URL) ([]any, error) {
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return nil, serviceDenied("incomplete_elastic_san_collection")
	}
	values, ok := res.data["value"].([]any)
	if !ok {
		return nil, serviceDenied("invalid_elastic_san_collection_value")
	}
	subscriptionList := kind == elasticSanType && strings.EqualFold(collection.Path, c.root()+"/providers/"+elasticSanType)
	seen := map[string]bool{}
	for _, value := range values {
		id, err := c.elasticSanRecord(object(value), kind)
		if err != nil || seen[id] {
			return nil, serviceDenied("invalid_elastic_san_collection_record")
		}
		if !subscriptionList && !strings.EqualFold(id[:strings.LastIndex(id, "/")], collection.Path) {
			return nil, serviceDenied("invalid_elastic_san_collection_record")
		}
		seen[id] = true
	}
	if value, exists := res.data["nextLink"]; exists && value != nil {
		next, ok := value.(string)
		if !ok {
			return nil, serviceDenied("invalid_elastic_san_next_link")
		}
		if next != "" {
			key, err := c.elasticSanPageCursor(next, collection)
			if err != nil {
				return nil, err
			}
			first, err := c.elasticSanPageCursor(collection.String(), collection)
			if err != nil {
				return nil, err
			}
			if key == first {
				return nil, serviceDenied("repeated_elastic_san_page")
			}
		}
	}
	return values, nil
}

// Public reads use the same resource/page validation as native inventory. Invoke
// returns one page; following its cursor remains the caller's responsibility.
func (c *client) elasticSanInvocationResponse(operation, endpoint string, res response) error {
	if !strings.HasPrefix(operation, "Azure.Microsoft.ElasticSan.") {
		return nil
	}
	name := strings.TrimPrefix(operation, "Azure.Microsoft.ElasticSan.")
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		read, list, _ := elasticSanOperations(kind)
		if name != read && name != list && !(kind == elasticSanType && name == "ElasticSans_ListByResourceGroup") {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		if name == read {
			id, err := c.elasticSanIdentity(u.Path, kind)
			if err != nil {
				return err
			}
			return c.elasticSanReadResponse(res, id, kind)
		}
		_, err = c.elasticSanPageResponse(res, kind, u)
		return err
	}
	return nil
}
