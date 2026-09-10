package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func cosmosParentID(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) <= 9 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "/")
}
func cosmosRootID(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}
func cosmosAncestorIDs(id string) []string {
	var ids []string
	for id = cosmosParentID(id); id != ""; id = cosmosParentID(id) {
		ids = append(ids, id)
	}
	return ids
}

// Non-API capabilities (serverless, backup, encryption, and future features)
// do not select a different API. Contradictory API markers never imply NoSQL.
func cosmosAPI(raw map[string]any) (string, map[string]bool, error) {
	kind := "GlobalDocumentDB"
	if value := raw["kind"]; value != nil {
		var ok bool
		kind, ok = value.(string)
		if !ok {
			return "", nil, serviceDenied("cosmos_unknown_account_api")
		}
	}
	if kind != "GlobalDocumentDB" && kind != "MongoDB" {
		return "", nil, serviceDenied("cosmos_unknown_account_api")
	}
	capabilities := map[string]bool{}
	value := object(raw["properties"])["capabilities"]
	if value != nil {
		rows, ok := value.([]any)
		if !ok {
			return "", nil, serviceDenied("cosmos_invalid_capabilities")
		}
		for _, row := range rows {
			name, ok := object(row)["name"].(string)
			if !ok || name == "" || name != strings.TrimSpace(name) || capabilities[strings.ToLower(name)] {
				return "", nil, serviceDenied("cosmos_invalid_capabilities")
			}
			capabilities[strings.ToLower(name)] = true
		}
	}
	api := ""
	for marker, selected := range map[string]string{"enablemongo": "mongo", "enablecassandra": "cassandra", "enablegremlin": "gremlin", "enabletable": "table"} {
		if capabilities[marker] {
			if api != "" {
				return "", nil, serviceDenied("cosmos_conflicting_account_apis")
			}
			api = selected
		}
	}
	if kind == "MongoDB" {
		if api != "" && api != "mongo" {
			return "", nil, serviceDenied("cosmos_conflicting_account_apis")
		}
		api = "mongo"
	}
	if api == "" {
		api = "sql"
	}
	if capabilities["enablemongorolebasedaccesscontrol"] && api != "mongo" {
		return "", nil, serviceDenied("cosmos_conflicting_account_apis")
	}
	return api, capabilities, nil
}
func cosmosChildApplies(child string, parent map[string]any) (bool, error) {
	_, kind, err := parseID(text(parent["id"]))
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(kind, cosmosType) {
		return true, nil
	}
	api, caps, err := cosmosAPI(parent)
	if err != nil {
		return false, err
	}
	child = cosmosKind(child)
	if child == cosmosMongoRoleType || child == cosmosMongoUserType {
		return api == "mongo" && caps["enablemongorolebasedaccesscontrol"], nil
	}
	for prefix, expected := range map[string]string{"sql": "sql", "cassandra": "cassandra", "gremlin": "gremlin", "table": "table", "mongodb": "mongo", "mongoMI": "mongo"} {
		if strings.HasPrefix(child, cosmosType+"/"+prefix) {
			return api == expected, nil
		}
	}
	return true, nil
}

// Preserve immutable resource IDs and all configuration. Timestamps/ETags for
// data writes, service-state clocks and controller-maintained indexes are not
// configuration; their live state and membership are checked separately.
func cosmosSnapshot(kind string, raw map[string]any) map[string]any {
	encoded, _ := json.Marshal(raw)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.Decode(&result)
	kind = cosmosKind(kind)
	result["id"] = cosmosWireSignature(responseID(kind, text(raw["id"])))
	result["name"] = last(text(result["id"]))
	result["location"] = strings.ReplaceAll(strings.ToLower(text(raw["location"])), " ", "")
	delete(result, "type")
	delete(result, "etag")
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	resource := object(props["resource"])
	for _, key := range []string{"_etag", "_ts", "_self"} {
		delete(resource, key)
	}
	if kind == cosmosType {
		delete(props, "privateEndpointConnections")
		for _, key := range []string{"locations", "readLocations", "writeLocations"} {
			for _, row := range array(props[key]) {
				delete(object(row), "provisioningState")
			}
		}
	}
	if kind == cosmosServiceType || kind == cosmosNotebookType {
		delete(props, "status")
		if kind == cosmosServiceType {
			delete(props, "creationTime")
		}
		for _, row := range array(props["locations"]) {
			delete(object(row), "status")
		}
	}
	return result
}
func cosmosConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, cosmosSnapshot(kind, raw))
}
func cosmosIncarnation(value asset.Asset, raw map[string]any) error {
	if !isCosmosType(value.Identity.NativeType) {
		return nil
	}
	wire := text(value.Normalized["_cosmos_wire_id"])
	if !cosmosSameWireID(wire, responseID(value.Identity.NativeType, text(raw["id"]))) {
		return serviceDenied("cosmos_resource_name_changed")
	}
	if expected := text(value.Normalized["_cosmos_configuration"]); expected == "" || expected != cosmosConfiguration(value.Identity.NativeType, raw) {
		return serviceDenied("cosmos_configuration_changed")
	}
	return cosmosPECIndexIncarnation(value.Identity.NativeType, value.Normalized["_cosmos_private_endpoints"], raw)
}

func validateCosmosResource(kind string, raw map[string]any) error {
	props := object(raw["properties"])
	if props == nil {
		return serviceDenied("cosmos_missing_resource_properties")
	}
	wire := responseID(kind, text(raw["id"]))
	switch kind {
	case cosmosSQLDatabaseType, cosmosContainerType, cosmosKeyType, cosmosStoredProcedureType, cosmosTriggerType, cosmosFunctionType, cosmosMongoDatabaseType, cosmosCollectionType, cosmosKeyspaceType, cosmosCassandraTableType, cosmosGremlinDatabaseType, cosmosGraphType, cosmosTableType:
		resource := object(props["resource"])
		name, ok := resource["id"].(string)
		if !ok || name == "" || name != last(wire) {
			return serviceDenied("cosmos_data_resource_name_mismatch")
		}
		if value := resource["_rid"]; value != nil {
			if id, ok := value.(string); !ok || id == "" {
				return serviceDenied("cosmos_invalid_resource_incarnation")
			}
		}
		if kind == cosmosTriggerType {
			for field, allowed := range map[string][]string{"triggerType": {"Pre", "Post"}, "triggerOperation": {"All", "Create", "Update", "Delete", "Replace"}} {
				if value, present := resource[field]; present && !slices.Contains(allowed, text(value)) {
					return serviceDenied("cosmos_invalid_trigger_configuration")
				}
			}
		}
	case cosmosDataCenterType:
		value, ok := props["dataCenterLocation"].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return serviceDenied("cosmos_invalid_data_center_location")
		}
	}
	return nil
}
func (c *client) cosmosResource(ctx context.Context, wire string) (map[string]any, error) {
	id, kind, err := parseID(wire)
	if err != nil || !isCosmosType(kind) {
		return nil, serviceDenied("invalid_cosmos_resource")
	}
	mapping, _ := findType(kind)
	endpoint, err := c.resourceURL(mapping, wire)
	if err != nil {
		return nil, err
	}
	response, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(response, id, mapping.NativeType) || !cosmosSameWireID(responseID(mapping.NativeType, text(response.data["id"])), wire) {
		return nil, serviceDenied("cosmos_resource_identity_mismatch")
	}
	raw := response.data
	raw["id"], raw["type"] = responseID(mapping.NativeType, text(raw["id"])), mapping.NativeType
	return raw, validateCosmosResource(mapping.NativeType, raw)
}

func (c *client) cosmosInventory(ctx context.Context, kind string, raw, normalized map[string]any) error {
	if !isCosmosType(kind) {
		return nil
	}
	if err := validateCosmosResource(kind, raw); err != nil {
		return err
	}
	wire := responseID(kind, text(raw["id"]))
	normalized["_cosmos_configuration"] = cosmosConfiguration(kind, raw)
	normalized["_cosmos_private_configuration"] = c.privateConfiguration(cosmosSnapshot(kind, raw))
	indexes, err := cosmosPECIndexes(kind, raw)
	if err != nil {
		return err
	}
	normalized["_cosmos_private_endpoints"] = indexes
	throughput, err := c.cosmosThroughput(ctx, kind, wire, raw)
	if err != nil {
		return err
	}
	normalized["_cosmos_throughput"] = throughput
	normalized["_cosmos_throughput_binding"] = c.privateConfiguration(throughput)
	ancestors := map[string]any{}
	for _, ancestor := range cosmosAncestorIDs(wire) {
		parent, err := c.cosmosResource(ctx, ancestor)
		if err != nil {
			return err
		}
		parentKind := text(parent["type"])
		applicable, err := cosmosChildApplies(kind, parent)
		if err != nil {
			return err
		}
		if !applicable {
			return serviceDenied("cosmos_child_api_mismatch")
		}
		settings, err := c.cosmosThroughput(ctx, parentKind, ancestor, parent)
		if err != nil {
			return err
		}
		indexes, err := cosmosPECIndexes(parentKind, parent)
		if err != nil {
			return err
		}
		ancestors[strings.ToLower(ancestor)] = map[string]any{"configuration": c.privateConfiguration(cosmosSnapshot(parentKind, parent)), "throughput": c.privateConfiguration(settings), "private_endpoints": indexes}
	}
	normalized["_cosmos_ancestors"] = ancestors
	refs, err := cosmosReferenceIDs(kind, raw)
	if err != nil {
		return err
	}
	normalized["_cosmos_references"] = refs
	if kind == cosmosFleetAccountType {
		targetID, err := cosmosFleetTarget(raw)
		if err != nil {
			return err
		}
		target, err := c.cosmosResource(ctx, targetID)
		if err != nil {
			return err
		}
		normalized["_cosmos_fleet_target"] = c.privateConfiguration(cosmosSnapshot(cosmosType, target))
	}
	return nil
}
func (c *client) cosmosAncestors(ctx context.Context, wire string, bindings map[string]any, protect bool) error {
	ids := cosmosAncestorIDs(wire)
	if len(ids) != len(bindings) {
		return serviceDenied("cosmos_ancestor_bindings_changed")
	}
	_, targetKind, _ := parseID(wire)
	for _, ancestor := range ids {
		binding := object(bindings[strings.ToLower(ancestor)])
		parent, err := c.cosmosResource(ctx, ancestor)
		if err != nil {
			return err
		}
		kind := text(parent["type"])
		if expected := text(binding["configuration"]); expected == "" || expected != c.privateConfiguration(cosmosSnapshot(kind, parent)) {
			return serviceDenied("cosmos_ancestor_changed")
		}
		if err := cosmosPECIndexIncarnation(kind, binding["private_endpoints"], parent); err != nil {
			return err
		}
		applicable, err := cosmosChildApplies(targetKind, parent)
		if err != nil {
			return err
		}
		if !applicable {
			return serviceDenied("cosmos_child_api_mismatch")
		}
		settings, err := c.cosmosThroughput(ctx, kind, ancestor, parent)
		if err != nil {
			return err
		}
		if text(binding["throughput"]) != c.privateConfiguration(settings) {
			return serviceDenied("cosmos_ancestor_throughput_changed")
		}
		if protect {
			mapping, _ := findType(kind)
			if reason := protectionReason(mapping, parent); reason != "" {
				return serviceDenied(reason)
			}
			if reason := cosmosThroughputProtection(settings); reason != "" {
				return serviceDenied(reason)
			}
		}
	}
	return nil
}
func (a *action) cosmosPreflight(ctx context.Context, value asset.Asset, raw map[string]any) error {
	if !isCosmosType(a.kind.NativeType) {
		return nil
	}
	if err := validateCosmosResource(a.kind.NativeType, raw); err != nil {
		return err
	}
	if err := cosmosIncarnation(value, raw); err != nil {
		return err
	}
	if err := a.client.cosmosAncestors(ctx, a.wireID, object(value.Normalized["_cosmos_ancestors"]), true); err != nil {
		return err
	}
	settings, err := a.client.cosmosThroughput(ctx, a.kind.NativeType, a.wireID, raw)
	if err != nil {
		return err
	}
	if text(value.Normalized["_cosmos_throughput_binding"]) != a.client.privateConfiguration(settings) {
		return serviceDenied("cosmos_throughput_changed")
	}
	if reason := cosmosThroughputProtection(settings); reason != "" {
		return serviceDenied(reason)
	}
	if a.kind.NativeType == cosmosFleetAccountType {
		targetID, err := cosmosFleetTarget(raw)
		if err != nil {
			return err
		}
		target, err := a.client.cosmosResource(ctx, targetID)
		if err != nil {
			return err
		}
		expected := text(value.Normalized["_cosmos_fleet_target"])
		if expected == "" || expected != a.client.privateConfiguration(cosmosSnapshot(cosmosType, target)) {
			return serviceDenied("cosmos_fleet_account_changed")
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return err
		}
		if err := a.client.linkedResourceProtection(ctx, targetID, target, locks); err != nil {
			return err
		}
		current, err := a.client.cosmosResource(ctx, targetID)
		if err != nil {
			return err
		}
		if expected != a.client.privateConfiguration(cosmosSnapshot(cosmosType, current)) {
			return serviceDenied("cosmos_fleet_account_changed")
		}
	}
	return nil
}

func cosmosProtection(kind string, raw map[string]any) string {
	if !isCosmosType(kind) {
		return ""
	}
	props := object(raw["properties"])
	if kind == cosmosType {
		if _, _, err := cosmosAPI(raw); err != nil {
			return "azure_cosmos_unknown_account_api"
		}
	}
	if kind == cosmosKeyType {
		return "azure_cosmos_managed_encryption_key"
	}
	if strings.HasSuffix(kind, "RoleDefinitions") {
		switch props["type"] {
		case "BuiltInRole":
			return "azure_cosmos_builtin_role"
		case "CustomRole":
		default:
			return "azure_cosmos_unknown_role_type"
		}
	}
	if value := props["provisioningState"]; value != nil {
		switch value {
		case "Succeeded", "Failed", "Canceled", "Cancelled":
		default:
			return "azure_cosmos_resource_transitioning"
		}
	} else if kind == cosmosType || kind == cosmosCassandraType || kind == cosmosDataCenterType || kind == cosmosFleetType || kind == cosmosFleetspaceType || kind == cosmosFleetAccountType {
		return "azure_cosmos_missing_provisioning_state"
	}
	if kind == cosmosServiceType {
		switch props["status"] {
		case "Running", "Stopped", "Error":
		default:
			return "azure_cosmos_service_transitioning"
		}
	}
	if kind == cosmosNotebookType {
		switch props["status"] {
		case "Online", "Failed":
		default:
			return "azure_cosmos_notebook_transitioning"
		}
	}
	if state := object(object(props["backupPolicy"])["migrationState"])["status"]; state != nil && state != "Completed" && state != "Failed" {
		return "azure_cosmos_backup_migrating"
	}
	return ""
}

func cosmosPECIndexes(kind string, raw map[string]any) ([]string, error) {
	indexes := []string{}
	if kind != cosmosType {
		return indexes, nil
	}
	value := object(raw["properties"])["privateEndpointConnections"]
	if value == nil {
		return indexes, nil
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, serviceDenied("cosmos_invalid_private_endpoint_index")
	}
	for _, row := range rows {
		id, typ, err := parseID(text(object(row)["id"]))
		if err != nil || !strings.EqualFold(typ, cosmosPECType) || !strings.EqualFold(cosmosParentID(id), text(raw["id"])) || slices.Contains(indexes, id) {
			return nil, serviceDenied("cosmos_invalid_private_endpoint_index")
		}
		indexes = append(indexes, id)
	}
	slices.Sort(indexes)
	return indexes, nil
}
func cosmosPECIndexIncarnation(kind string, expected any, raw map[string]any) error {
	if kind != cosmosType {
		return nil
	}
	current, err := cosmosPECIndexes(kind, raw)
	if err != nil {
		return err
	}
	planned := stringValues(expected)
	if expected == nil || len(planned) < len(current) {
		return serviceDenied("cosmos_private_endpoint_index_changed")
	}
	for _, id := range current {
		if !slices.Contains(planned, id) {
			return serviceDenied("cosmos_private_endpoint_index_changed")
		}
	}
	return nil
}

// Database accounts, fleets and Cassandra clusters describe global controllers;
// their top-level location is the resource group's region. A managed Cassandra
// data center has its own deployment region in properties.dataCenterLocation.
func cosmosRegion(kind string, raw map[string]any) string {
	if kind == cosmosDataCenterType {
		return strings.ReplaceAll(strings.ToLower(text(object(raw["properties"])["dataCenterLocation"])), " ", "")
	}
	return "global"
}

func cosmosCreation(raw map[string]any) string {
	values := map[string]any{}
	for key, value := range map[string]any{"instanceId": object(raw["properties"])["instanceId"], "resourceRid": object(object(raw["properties"])["resource"])["_rid"], "createdAt": object(raw["systemData"])["createdAt"]} {
		if value != nil {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func cosmosListedIncarnation(kind string, listed, live map[string]any) error {
	if !cosmosSameWireID(responseID(kind, text(listed["id"])), responseID(kind, text(live["id"]))) {
		return serviceDenied("cosmos_listed_name_changed")
	}
	lp, rp := object(listed["properties"]), object(live["properties"])
	if id := lp["instanceId"]; id != nil && !reflect.DeepEqual(id, rp["instanceId"]) {
		return serviceDenied("cosmos_account_recreated")
	}
	for _, key := range []string{"id", "_rid"} {
		if id := object(lp["resource"])[key]; id != nil && !reflect.DeepEqual(id, object(rp["resource"])[key]) {
			return serviceDenied("cosmos_data_resource_recreated")
		}
	}
	return nil
}
