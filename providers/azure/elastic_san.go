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
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return res, serviceDenied("incomplete_elastic_san_read")
	}
	actual, err := c.elasticSanRecord(res.data, kind)
	if err != nil || actual != id {
		return res, serviceDenied("invalid_elastic_san_read_response")
	}
	return res, nil
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
		if err := c.validateURL(next); err != nil {
			return nil, "", err
		}
		u, err := url.Parse(next)
		if err != nil || u.RawPath != "" || !strings.EqualFold(u.Path, collection.Path) {
			return nil, "", serviceDenied("invalid_elastic_san_collection_cursor")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != elasticSanVersion {
			return nil, "", serviceDenied("invalid_elastic_san_cursor_version")
		}
		for name, values := range query {
			if len(values) != 1 || values[0] == "" || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken"}, name) {
				return nil, "", serviceDenied("filtered_elastic_san_collection")
			}
		}
		pageKey := strings.ToLower(u.Path) + "?" + query.Encode()
		if pages[pageKey] {
			return nil, "", serviceDenied("repeated_elastic_san_page")
		}
		pages[pageKey] = true
		res, err := c.requestBody(ctx, bound.Method, next, nil, bound.Headers)
		if err != nil {
			return nil, "", err
		}
		if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
			return nil, "", serviceDenied("incomplete_elastic_san_collection")
		}
		values, ok := res.data["value"].([]any)
		if !ok {
			return nil, "", serviceDenied("invalid_elastic_san_collection_value")
		}
		for _, value := range values {
			id, err := c.elasticSanRecord(object(value), kind)
			if err != nil || identities[id] || elasticSanParent(id, kind) != parent {
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
	if value := props["availabilityZones"]; value != nil {
		zones, ok := value.([]any)
		if !ok {
			return "", serviceDenied("invalid_elastic_san_zones")
		}
		for _, zone := range zones {
			if _, ok := zone.(string); !ok {
				return "", serviceDenied("invalid_elastic_san_zone_value")
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
