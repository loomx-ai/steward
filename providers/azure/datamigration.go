package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	dataMigrationServiceType      = "Microsoft.DataMigration/services"
	dataMigrationProjectType      = dataMigrationServiceType + "/projects"
	dataMigrationTaskType         = dataMigrationProjectType + "/tasks"
	dataMigrationFileType         = dataMigrationProjectType + "/files"
	dataMigrationServiceTaskType  = dataMigrationServiceType + "/serviceTasks"
	dataMigrationMongoServiceType = "Microsoft.DataMigration/migrationServices"
	dataMigrationSQLServiceType   = "Microsoft.DataMigration/sqlMigrationServices"
	dataMigrationType             = "Microsoft.DataMigration/databaseMigrations"
	dataMigrationVersion          = "2025-06-30"
	dataMigrationPrefix           = "Azure.Microsoft.DataMigration."
	dataMigrationInventorySource  = "data-migration"
)

func dataMigrationKind(kind string) string {
	for _, candidate := range []string{dataMigrationServiceType, dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType, dataMigrationMongoServiceType, dataMigrationSQLServiceType, dataMigrationType} {
		if strings.EqualFold(candidate, kind) {
			return candidate
		}
	}
	return ""
}

func dataMigrationChildKinds(kind string) []string {
	switch kind {
	case dataMigrationServiceType:
		return []string{dataMigrationProjectType, dataMigrationServiceTaskType}
	case dataMigrationProjectType:
		return []string{dataMigrationTaskType, dataMigrationFileType}
	}
	return nil
}

func dataMigrationParent(id, kind string) string {
	switch kind {
	case dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType:
		return id[:strings.LastIndex(id[:strings.LastIndex(id, "/")], "/")]
	}
	return ""
}

func dataMigrationTarget(id string) (string, string) {
	parts := strings.Split(id, "/")
	if len(parts) != 13 || !strings.EqualFold(parts[9], "providers") || !strings.EqualFold(parts[10], "Microsoft.DataMigration") || !strings.EqualFold(parts[11], "databaseMigrations") {
		return "", ""
	}
	target, typ, err := parseID(strings.Join(parts[:9], "/"))
	if err != nil {
		return "", ""
	}
	switch typ {
	case "microsoft.sql/servers":
		return target, "SqlDb"
	case "microsoft.sql/managedinstances":
		return target, "SqlMi"
	case "microsoft.sqlvirtualmachine/sqlvirtualmachines":
		return target, "SqlVm"
	case "microsoft.documentdb/databaseaccounts", "microsoft.documentdb/mongoclusters":
		return target, "MongoToCosmosDbMongo"
	}
	return "", ""
}

func (c *client) dataMigrationIdentity(id, kind string) error {
	canonical, typ, err := parseID(id)
	if err != nil || canonical != id || kind == "" || dataMigrationKind(typ) != kind || !strings.HasPrefix(id, c.root()+"/") {
		return serviceDenied("invalid_datamigration_identity")
	}
	if kind == dataMigrationType {
		if target, _ := dataMigrationTarget(id); target == "" {
			return serviceDenied("invalid_datamigration_target")
		}
	} else if len(strings.Split(id, "/")) != 7+2*(len(strings.Split(kind, "/"))-1) {
		return serviceDenied("invalid_datamigration_resource_depth")
	}
	return nil
}

func dataMigrationMetadata(id, kind string, raw map[string]any) error {
	if raw == nil || raw["error"] != nil || !strings.EqualFold(text(raw["id"]), id) || text(raw["type"]) == "" || !validResponseType(kind, text(raw["type"])) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("datamigration_resource_identity_changed")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || props == nil {
		return serviceDenied("datamigration_properties_missing")
	}
	switch kind {
	case dataMigrationServiceType, dataMigrationMongoServiceType, dataMigrationSQLServiceType:
		if text(raw["location"]) == "" || resourceRegion(raw) == "global" {
			return serviceDenied("datamigration_location_missing")
		}
	case dataMigrationTaskType, dataMigrationServiceTaskType:
		if text(props["taskType"]) == "" || text(props["state"]) == "" {
			return serviceDenied("datamigration_task_state_missing")
		}
	case dataMigrationType:
		target, migrationKind := dataMigrationTarget(id)
		if target == "" || props["kind"] != migrationKind || !strings.EqualFold(text(props["scope"]), target) {
			return serviceDenied("datamigration_target_changed")
		}
		if service := text(props["migrationService"]); service != "" {
			_, serviceKind, err := parseID(service)
			if err != nil || !slices.Contains([]string{dataMigrationMongoServiceType, dataMigrationSQLServiceType}, dataMigrationKind(serviceKind)) {
				return serviceDenied("invalid_datamigration_service_reference")
			}
		}
	}
	return nil
}

func dataMigrationSnapshot(kind string, raw map[string]any) map[string]any {
	result := batchClone(raw)
	result["id"] = strings.ToLower(text(result["id"]))
	result["name"] = last(text(result["id"]))
	for _, key := range []string{"type", "etag", "eTag"} {
		delete(result, key)
	}
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	if len(object(result["systemData"])) == 0 {
		delete(result, "systemData")
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	for _, key := range []string{"scope", "migrationService", "virtualSubnetId", "virtualNicId"} {
		if value, ok := props[key].(string); ok {
			props[key] = strings.ToLower(value)
		}
	}
	switch kind {
	case dataMigrationTaskType, dataMigrationServiceTaskType:
		for _, key := range []string{"state", "errors", "output"} {
			delete(props, key)
		}
		for _, value := range array(props["commands"]) {
			for _, key := range []string{"state", "errors", "output"} {
				delete(object(value), key)
			}
		}
	case dataMigrationMongoServiceType, dataMigrationSQLServiceType:
		delete(props, "integrationRuntimeState")
	case dataMigrationType:
		for _, key := range []string{"migrationStatus", "migrationStatusDetails", "endedOn", "migrationFailureError", "provisioningError"} {
			delete(props, key)
		}
		for _, value := range array(props["collectionList"]) {
			delete(object(value), "migrationProgressDetails")
		}
	}
	// File lastModified/size and migration startedOn/operationId remain bound.
	// Authored inputs, connection information and unknown fields stay private.
	return result
}

func dataMigrationState(kind string, raw map[string]any) string {
	props := object(raw["properties"])
	switch kind {
	case dataMigrationType:
		return text(props["migrationStatus"])
	case dataMigrationTaskType, dataMigrationServiceTaskType:
		return text(props["state"])
	case dataMigrationFileType:
		return "Available"
	}
	return text(props["provisioningState"])
}

func dataMigrationSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "location", "tags", "request_id", "status_code"} {
			if entry, ok := value[key]; ok {
				result[key] = entry
			}
		}
		if props, ok := value["properties"].(map[string]any); ok {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "migrationStatus", "state", "taskType", "kind", "sourcePlatform", "targetPlatform", "creationTime", "startedOn", "endedOn", "integrationRuntimeState"} {
				if entry, ok := props[key].(string); ok {
					public[key] = entry
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if entry, ok := value[key]; ok {
				result[key] = dataMigrationSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = dataMigrationSafeValue(entry)
		}
		return result
	default:
		return nil
	}
}

func (c *client) dataMigrationRead(ctx context.Context, id, kind string) (map[string]any, error) {
	if err := c.dataMigrationIdentity(id, kind); err != nil {
		return nil, err
	}
	mapping, ok := findType(kind)
	if !ok {
		return nil, serviceDenied("datamigration_kind_missing")
	}
	operation, parameters, err := c.resourceOperation(mapping, id, "GET")
	if err != nil {
		return nil, err
	}
	request, err := bindAzureREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	result, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, err
	}
	if result.status != 200 || operationLocation(result.header) != "" {
		return nil, serviceDenied("invalid_datamigration_read_status")
	}
	if err := dataMigrationMetadata(id, kind, result.data); err != nil {
		return nil, err
	}
	return result.data, nil
}

func (c *client) dataMigrationOperation(id, kind, name string, extra map[string]any) (catalog.RESTRequest, error) {
	if c.dataMigrationIdentity(id, kind) != nil {
		return catalog.RESTRequest{}, serviceDenied("invalid_datamigration_operation_identity")
	}
	mapping, _ := findType(kind)
	_, parameters, err := c.resourceOperation(mapping, id, "GET")
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	operation, ok := metadata.catalog.Operation(dataMigrationPrefix + name)
	if !ok || operation.Call == nil || operation.Call.Version != dataMigrationVersion {
		return catalog.RESTRequest{}, serviceDenied("invalid_datamigration_operation")
	}
	maps.Copy(parameters, extra)
	return bindAzureREST(operation, parameters)
}

func dataMigrationListQuery(u *url.URL) error {
	if armPathProvider(u.Path) != "microsoft.datamigration" {
		return nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != dataMigrationVersion {
		return serviceDenied("datamigration_list_version_changed")
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken"}, key) {
			return serviceDenied("filtered_datamigration_list")
		}
	}
	return nil
}
