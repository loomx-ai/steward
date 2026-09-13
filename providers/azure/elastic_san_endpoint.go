package azure

import "strings"

// Elastic SAN groupIds are native volume-group IDs in the reviewed REST
// examples, unlike the short group labels used by many other Private Link APIs.
func (c *client) elasticSanEndpointGroups(raw map[string]any) ([]string, error) {
	id, err := c.elasticSanRecord(raw, elasticSanEndpointType)
	if err != nil {
		return nil, err
	}
	values, ok := object(raw["properties"])["groupIds"].([]any)
	if !ok || len(values) == 0 {
		return nil, serviceDenied("elastic_san_endpoint_groups_missing")
	}
	ids, seen := []string{}, map[string]bool{}
	for _, value := range values {
		wire, ok := value.(string)
		group, err := c.elasticSanIdentity(wire, elasticSanGroupType)
		if !ok || err != nil || elasticSanRoot(group) != elasticSanRoot(id) || seen[group] {
			return nil, serviceDenied("elastic_san_endpoint_group_changed")
		}
		seen[group], ids = true, append(ids, group)
	}
	return ids, nil
}

func elasticSanChildSnapshot(kind string, raw map[string]any) map[string]any {
	snapshot := hybridComputeChildSnapshot(raw)
	if kind == elasticSanEndpointType {
		state := object(object(snapshot["properties"])["privateLinkServiceConnectionState"])
		// Disconnection can change operational status while DELETE is outstanding.
		// Preflight still binds these fields through the separate version record.
		delete(state, "status")
		delete(state, "actionsRequired")
	}
	return snapshot
}

func elasticSanChildVersion(kind string, raw map[string]any) map[string]any {
	version := map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}
	if strings.EqualFold(kind, elasticSanEndpointType) {
		version["connectionState"] = object(raw["properties"])["privateLinkServiceConnectionState"]
	}
	return version
}
