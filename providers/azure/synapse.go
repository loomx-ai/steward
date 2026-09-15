package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
)

const (
	synapseSource    = "synapse"
	synapseType      = "Microsoft.Synapse/workspaces"
	synapseSparkType = synapseType + "/bigDataPools"
	synapseSQLType   = synapseType + "/sqlPools"
	synapseVersion   = "2021-06-01"
)

func synapseKind(kind string) string {
	for _, candidate := range []string{synapseType, synapseSparkType, synapseSQLType} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}

func (c *client) synapseMetadata(raw map[string]any, kind string) error {
	id, typ, err := parseID(text(raw["id"]))
	segments := 9
	if kind != synapseType {
		segments = 11
	}
	if synapseKind(kind) == "" || err != nil || !strings.EqualFold(typ, kind) || !strings.EqualFold(text(raw["type"]), kind) || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != segments || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("invalid_synapse_identity")
	}
	location, ok := raw["location"].(string)
	if !ok || strings.TrimSpace(location) == "" {
		return serviceDenied("synapse_location_missing")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok {
		return serviceDenied("synapse_properties_missing")
	}
	for _, key := range []string{"provisioningState", "status", "creationDate", "workspaceUID", "managedResourceGroupName", "sparkVersion", "nodeSize", "collation"} {
		if value, present := props[key]; present && value != nil {
			if value, ok := value.(string); !ok || strings.TrimSpace(value) == "" {
				return serviceDenied("invalid_synapse_metadata")
			}
		}
	}
	for _, key := range []string{"defaultDataLakeStorage", "connectivityEndpoints", "autoScale", "autoPause"} {
		if value, present := props[key]; present && value != nil {
			if _, ok := value.(map[string]any); !ok {
				return serviceDenied("invalid_synapse_configuration")
			}
		}
	}
	for _, key := range []string{"tags", "identity", "sku", "systemData"} {
		if value, present := raw[key]; present && value != nil {
			if _, ok := value.(map[string]any); !ok {
				return serviceDenied("invalid_synapse_resource_metadata")
			}
		}
	}
	return nil
}

func (c *client) synapseReadResponse(res response, id, kind string) error {
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" || !strings.EqualFold(text(res.data["id"]), id) {
		return serviceDenied("invalid_synapse_read_response")
	}
	return c.synapseMetadata(res.data, kind)
}

func (c *client) synapseListQuery(endpoint, collection string) error {
	if err := c.validateURL(endpoint); err != nil {
		return err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.RawPath != "" || !strings.EqualFold(u.Path, collection) {
		return serviceDenied("synapse_collection_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Get("api-version") != synapseVersion {
		return serviceDenied("synapse_version_changed")
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken"}, key) {
			return serviceDenied("filtered_synapse_inventory")
		}
	}
	return nil
}

func (c *client) synapsePage(ctx context.Context, endpoint, collection, kind string) ([]any, string, response, error) {
	if err := c.synapseListQuery(endpoint, collection); err != nil {
		return nil, "", response{}, err
	}
	rows, next, res, err := c.listPageResult(ctx, endpoint, collection)
	if err != nil {
		return nil, "", response{}, err
	}
	if operationLocation(res.header) != "" {
		return nil, "", response{}, serviceDenied("incomplete_synapse_index")
	}
	if next != "" {
		if err := c.synapseListQuery(next, collection); err != nil {
			return nil, "", response{}, err
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		raw := object(row)
		if err := c.synapseMetadata(raw, kind); err != nil {
			return nil, "", response{}, err
		}
		id := strings.ToLower(text(raw["id"]))
		if seen[id] || kind != synapseType && !strings.EqualFold(id, collection+"/"+last(id)) {
			return nil, "", response{}, serviceDenied("invalid_synapse_index_member")
		}
		seen[id] = true
	}
	return rows, next, res, nil
}

// Compare authored configuration across parent discovery and child pagination.
// Provisioning/activity state can advance during a scan. The private digest is
// bound to the connection and does not persist unknown private configuration.
func synapseSnapshot(raw map[string]any) map[string]any {
	result := maps.Clone(raw)
	for _, key := range []string{"id", "name", "type"} {
		result[key] = strings.ToLower(text(raw[key]))
	}
	props := maps.Clone(object(raw["properties"]))
	delete(props, "provisioningState")
	delete(props, "status")
	result["properties"] = props
	return result
}

func (c *client) synapseReferences(ctx context.Context, kind string, raw, normalized map[string]any, refs map[string][]string) (err error) {
	if kind = synapseKind(kind); kind == "" {
		return nil
	}
	if err := c.synapseMetadata(raw, kind); err != nil {
		return err
	}
	normalized["_synapse_private_configuration"] = c.privateConfiguration(synapseSnapshot(raw))
	normalized["state"] = object(raw["properties"])["provisioningState"]
	if kind == synapseSQLType && text(object(raw["properties"])["status"]) != "" {
		normalized["state"] = object(raw["properties"])["status"]
	}
	// Native DELETE contracts exist, but reviewed runtime cleanup must also
	// account for code artifacts, running work and production LRO receipts.
	normalized["cleanup_protected"] = true
	if normalized["cleanup_protection_reason"] == nil {
		normalized["cleanup_protection_reason"] = "synapse_cleanup_not_implemented"
	}
	if kind != synapseType {
		if kind == synapseSQLType && normalized["cleanup_protection_reason"] == "synapse_cleanup_not_implemented" {
			normalized["cleanup_protected"] = false
			normalized["cleanup_controller_only"] = true
		}
		id := strings.ToLower(text(raw["id"]))
		addReference(refs, synapseType, strings.Join(strings.Split(id, "/")[:9], "/"))
		return nil
	}
	defer func() {
		if err != nil {
			return
		}
		id := strings.ToLower(text(raw["id"]))
		current, readErr := c.request(ctx, "GET", apiURL(id, synapseVersion))
		if readErr != nil {
			err = readErr
			return
		}
		if err = c.synapseReadResponse(current, id, kind); err != nil {
			return
		}
		if c.privateConfiguration(synapseSnapshot(current.data)) != normalized["_synapse_private_configuration"] {
			err = serviceDenied("synapse_workspace_changed_during_dependency_read")
		}
	}()
	if identity := object(raw["identity"]); identity["userAssignedIdentities"] != nil {
		identities, ok := identity["userAssignedIdentities"].(map[string]any)
		if !ok {
			return serviceDenied("invalid_synapse_managed_identities")
		}
		for wire := range identities {
			id, typ, err := parseID(wire)
			if err != nil || typ != "microsoft.managedidentity/userassignedidentities" || len(strings.Split(id, "/")) != 9 {
				return serviceDenied("invalid_synapse_managed_identity")
			}
			addReference(refs, "Microsoft.ManagedIdentity/userAssignedIdentities", id)
		}
	}
	lake := object(object(raw["properties"])["defaultDataLakeStorage"])
	if len(lake) == 0 {
		normalized["_synapse_unresolved_storage"] = "default_storage_metadata_missing"
		return nil
	}
	endpoint, ok := lake["accountUrl"].(string)
	u, err := url.Parse(endpoint)
	filesystem, hasFilesystem := lake["filesystem"].(string)
	if !ok || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || !hasFilesystem || filesystem == "" || filesystem != strings.TrimSpace(filesystem) || strings.ContainsAny(filesystem, "/\\%?#") || filesystem == "." || filesystem == ".." {
		return serviceDenied("invalid_synapse_default_storage")
	}
	values, err := c.subscriptionReferenceIndex(ctx, storageType)
	if err != nil {
		return err
	}
	found := ""
	for _, listed := range values {
		if batchReferenceEndpoint(storageType, listed, u.Host, "") != "dfs" {
			continue
		}
		id := strings.ToLower(text(listed["id"]))
		current, err := c.linkedResource(ctx, id)
		if err != nil {
			return err
		}
		if found != "" || batchReferenceEndpoint(storageType, current, u.Host, "") != "dfs" || serviceListedIncarnation(listed, current) != nil {
			return serviceDenied("synapse_default_storage_changed_or_ambiguous")
		}
		found = id
	}
	if found == "" {
		// No fabricated ARM ID for storage in another subscription or a
		// nonstandard endpoint. Keep the unresolved reference visible.
		normalized["_synapse_unresolved_storage"] = map[string]any{"accountUrl": endpoint, "filesystem": filesystem}
		return nil
	}
	addReference(refs, storageType, found)
	addReference(refs, containerType, found+"/blobservices/default/containers/"+strings.ToLower(filesystem))
	return nil
}
