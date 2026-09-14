package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	discoveryHost           = "discoveryengine.googleapis.com"
	discoveryCollectionType = discoveryHost + "/Collection"
	discoveryDataStoreType  = discoveryHost + "/DataStore"
	discoveryEngineType     = discoveryHost + "/Engine"
	discoverySiteType       = discoveryHost + "/SiteSearchEngine"
	discoveryProof          = "_discoveryengine_configuration"
	discoveryParentProof    = "_discoveryengine_parent_configuration"
)

func isDiscovery(kind string) bool { return strings.HasPrefix(kind, discoveryHost+"/") }
func discoveryLocation(location string) bool {
	return slices.Contains([]string{"global", "us", "eu"}, location)
}
func discoveryAPIHost(host string) bool {
	return host == discoveryHost || host == "us-"+discoveryHost || host == "eu-"+discoveryHost
}

// Discovery Engine publishes one Discovery origin but requires the US/EU
// endpoint for those multi-regions. All readers, Invoke, writers and LRO polls
// pass this boundary; resource names cannot select an arbitrary regional host.
func (c *client) discoveryEndpoint(u *url.URL) error {
	if !discoveryAPIHost(u.Host) {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 5 || !slices.Contains([]string{"v1", "v1alpha"}, parts[0]) || parts[1] != "projects" || (parts[2] != c.project && parts[2] != c.number) || parts[3] != "locations" || !discoveryLocation(parts[4]) {
		return groupDenied("discoveryengine_endpoint_scope_invalid")
	}
	for _, part := range parts {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return groupDenied("discoveryengine_endpoint_path_invalid")
		}
	}
	host := discoveryHost
	if parts[4] != "global" {
		host = parts[4] + "-" + host
	}
	if u.Host != discoveryHost && u.Host != host {
		return groupDenied("discoveryengine_endpoint_region_changed")
	}
	u.Host = host
	return nil
}

func discoveryCanonical(value string) string {
	for _, host := range []string{discoveryHost, "us-" + discoveryHost, "eu-" + discoveryHost} {
		for _, version := range []string{"v1", "v1alpha"} {
			prefix := "https://" + host + "/" + version + "/"
			if strings.HasPrefix(value, prefix) {
				value = "//" + discoveryHost + "/" + strings.TrimPrefix(value, prefix)
			}
		}
	}
	// The older DataStoreService resource-name alias denotes default_collection.
	prefix := "//" + discoveryHost + "/"
	if strings.HasPrefix(value, prefix) {
		parts := strings.Split(strings.TrimPrefix(value, prefix), "/")
		if len(parts) >= 6 && parts[0] == "projects" && parts[2] == "locations" && parts[4] == "dataStores" {
			parts = append(parts[:4], append([]string{"collections", "default_collection"}, parts[4:]...)...)
			value = prefix + strings.Join(parts, "/")
		}
	}
	return value
}

func discoveryConfiguration(raw map[string]any) string {
	value := cloneParameters(raw)
	for key := range value {
		if key == "name" || key == "updateTime" || key == "state" || key == "indexTime" || key == "indexStatus" || key == "derivedStructData" || key == "billingEstimation" || key == "configurableBillingApproachUpdateTime" || key == "project_id" || key == "project_number" || strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") {
			delete(value, key)
		}
	}
	// Connector synchronization progress changes without changing its connection.
	if raw["dataConnector"] != nil {
		connector := cloneParameters(object(raw["dataConnector"]))
		for _, key := range []string{"name", "state", "errors", "warnings", "lastSyncTime", "lastSyncStatus", "syncStatus", "updateTime", "entities"} {
			delete(connector, key)
		}
		// Collection.get returns only a summary. discoveryRead replaces it with
		// GetDataConnector before hashing, including each entity's ingestion
		// parameters. Only the output DataStore link is reconciled separately.
		var entities []map[string]any
		for _, raw := range array(object(value["dataConnector"])["entities"]) {
			entity := cloneParameters(object(raw))
			delete(entity, "dataStore")
			entities = append(entities, entity)
		}
		slices.SortFunc(entities, func(a, b map[string]any) int { return strings.Compare(text(a["entityName"]), text(b["entityName"])) })
		if len(entities) > 0 {
			connector["entities"] = entities
		}
		value["dataConnector"] = connector
	}
	payload, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func discoverySameResource(kind string, planned, live map[string]any) error {
	if !isDiscovery(kind) {
		return nil
	}
	expected := text(planned[discoveryProof])
	if expected == "" {
		expected = discoveryConfiguration(planned)
	}
	if expected != discoveryConfiguration(live) {
		return groupDenied("discoveryengine_configuration_changed")
	}
	return serviceIncarnation(planned, live)
}

func (c *client) discoveryID(kind, value string) (string, error) {
	if strings.HasPrefix(value, "https://") {
		u, err := url.Parse(value)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !discoveryAPIHost(u.Host) {
			return "", groupDenied("discoveryengine_identity_invalid")
		}
		if err := c.discoveryEndpoint(u); err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(value, "projects/") {
		value = "//" + discoveryHost + "/" + value
	}
	id := c.canonicalName(value)
	native, ok := findType(kind)
	if !ok {
		return "", groupDenied("discoveryengine_kind_invalid")
	}
	if _, err := c.resourceURL(native, id); err != nil {
		return "", err
	}
	return id, nil
}

func (c *client) discoveryIdentity(kind, id string, data map[string]any) error {
	actual, err := c.discoveryID(kind, text(data["name"]))
	if err != nil || actual != id {
		return groupDenied("discoveryengine_identity_changed")
	}
	if kind == discoveryCollectionType || kind == discoveryDataStoreType || kind == discoveryEngineType {
		if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
			return groupDenied("discoveryengine_creation_time_missing")
		}
	}
	return nil
}

func (c *client) discoveryRead(ctx context.Context, kind, id string) (map[string]any, error) {
	native, _ := findType(kind)
	endpoint, err := c.resourceURL(native, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if _, present := data["error"]; present {
		return nil, groupDenied("discoveryengine_resource_error_response")
	}
	if err := c.discoveryIdentity(kind, id, data); err != nil {
		return nil, err
	}
	if kind == discoveryCollectionType && data["dataConnector"] != nil {
		name := strings.TrimPrefix(id, "//"+discoveryHost+"/") + "/dataConnector"
		response, err := c.discoveryCall(ctx, "discoveryengine.projects.locations.collections.getDataConnector", map[string]any{"name": name})
		if err != nil {
			return nil, err
		}
		if c.canonicalName("//"+discoveryHost+"/"+text(response["name"])) != id+"/dataConnector" {
			return nil, groupDenied("discoveryengine_connector_identity_changed")
		}
		if _, err := productRecords(response, "entities"); err != nil {
			return nil, err
		}
		data["dataConnector"] = response
	}

	if kind == discoverySiteType {
		response, err := c.discoveryCall(ctx, "discoveryengine.projects.locations.collections.dataStores.siteSearchEngine.sitemaps.fetch", map[string]any{"parent": strings.TrimPrefix(id, "//"+discoveryHost+"/")})
		if err != nil {
			return nil, err
		}
		if err := checkListCompleteness(response); err != nil {
			return nil, err
		}
		if response["nextPageToken"] != nil {
			return nil, groupDenied("discoveryengine_sitemaps_paging_invalid")
		}
		records, err := productRecords(response, "sitemapsMetadata")
		if err != nil {
			return nil, err
		}
		var sitemaps []map[string]any
		seen := map[string]bool{}
		for _, record := range records {
			sitemap := cloneParameters(object(record.Data["sitemap"]))
			name := c.canonicalName("//" + discoveryHost + "/" + text(sitemap["name"]))
			prefix := id + "/sitemaps/"
			if !strings.HasPrefix(name, prefix) || strings.Contains(strings.TrimPrefix(name, prefix), "/") || !segmentPattern.MatchString(last(name)) || last(name) == "." || last(name) == ".." || seen[name] {
				return nil, groupDenied("discoveryengine_sitemap_identity_invalid")
			}
			if _, err := time.Parse(time.RFC3339Nano, text(sitemap["createTime"])); err != nil {
				return nil, groupDenied("discoveryengine_sitemap_creation_time_missing")
			}
			seen[name] = true
			sitemap["name"] = name
			sitemaps = append(sitemaps, sitemap)
		}
		slices.SortFunc(sitemaps, func(a, b map[string]any) int { return strings.Compare(text(a["name"]), text(b["name"])) })
		data["sitemaps"] = sitemaps
		data["_discoveryengine_sitemap_count"] = len(sitemaps)
	}
	return data, nil
}

func (c *client) discoveryCall(ctx context.Context, id string, parameters map[string]any) (map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation(id)
	if !ok {
		return nil, groupDenied("discoveryengine_method_missing")
	}
	request, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	response, err := c.requestResult(ctx, request.Method, request.URL, nil, request.Body)
	if _, present := response.Data["error"]; err == nil && present {
		err = groupDenied("discoveryengine_resource_error_response")
	}
	return response.Data, err
}

func discoveryParentKind(operation string) string {
	if strings.HasSuffix(operation, ".collections.dataStores.list") || strings.HasSuffix(operation, ".collections.engines.list") {
		return discoveryCollectionType
	}
	for _, entry := range []struct{ suffix, kind string }{
		{".dataStores.siteSearchEngine.targetSites.list", discoverySiteType},
		{".dataStores.branches.documents.list", discoveryHost + "/Branch"},
		{".engines.assistants.list", discoveryEngineType},
	} {
		if strings.HasSuffix(operation, entry.suffix) {
			return entry.kind
		}
	}
	if strings.Contains(operation, ".dataStores.") {
		return discoveryDataStoreType
	}
	if strings.Contains(operation, ".engines.") {
		return discoveryEngineType
	}
	if strings.HasSuffix(operation, ".collections.list") {
		return ""
	}
	return discoveryCollectionType
}

func (r *Runtime) discoveryTargets(ctx context.Context, c *client, request contracts.InventoryRequest, definition spec.ResourceKindSpec, ancestors []string) ([]productTarget, error) {
	kind, _ := findType(definition.Metadata.NativeType)
	if request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeGlobal {
		return nil, groupDenied("discoveryengine_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeRegion && !discoveryLocation(request.Scope.NativeID) {
		return nil, nil
	}
	var result []productTarget
	for _, operation := range kind.ListOperations {
		api := *definition.Discovery.List
		api.Operation = operation
		parentKind := discoveryParentKind(operation)
		if parentKind == "" {
			locations := []string{"global", "us", "eu"}
			if request.Scope.Kind == asset.ScopeRegion {
				locations = []string{request.Scope.NativeID}
			}
			if request.Scope.Kind == asset.ScopeGlobal {
				locations = []string{"global"}
			}
			for _, location := range locations {
				result = append(result, productTarget{API: api, Parameters: map[string]any{"parent": "projects/" + c.project + "/locations/" + location}})
			}
			continue
		}
		parentRequest := request
		parent := r.resourceKind(parentKind)
		parentRequest.ResourceKind = &parent
		parentRequest.Cursor = ""
		seen := map[string]bool{}
		for {
			page, err := r.listProduct(ctx, c, parentRequest, ancestors)
			if err != nil {
				return nil, err
			}
			for _, item := range page.Items {
				if seen[item.NativeID] {
					return nil, groupDenied("discoveryengine_parent_duplicate")
				}
				seen[item.NativeID] = true
				if kind.NativeType == discoverySiteType && item.Normalized["contentConfig"] != "NO_CONTENT" {
					continue
				}
				parameters := map[string]any{"parent": strings.TrimPrefix(item.NativeID, "//"+discoveryHost+"/")}
				if kind.NativeType == discoverySiteType {
					parameters = map[string]any{"name": strings.TrimPrefix(item.NativeID, "//"+discoveryHost+"/") + "/siteSearchEngine"}
				}
				chain, err := discoveryAncestors(item)
				if err != nil {
					return nil, err
				}
				result = append(result, productTarget{API: api, Parameters: parameters, ParentType: parentKind, ParentID: item.NativeID, ParentUID: text(item.Normalized["createTime"]), ParentConfiguration: text(item.Normalized[discoveryProof]), ParentContainerChain: chain})
			}
			if page.Complete {
				break
			}
			parentRequest.Cursor = page.NextCursor
		}
	}
	slices.SortFunc(result, func(a, b productTarget) int {
		if n := strings.Compare(a.API.Operation, b.API.Operation); n != 0 {
			return n
		}
		return strings.Compare(fmt.Sprint(a.Parameters), fmt.Sprint(b.Parameters))
	})
	return result, nil
}

func (c *client) verifyDiscoveryParent(ctx context.Context, target productTarget) error {
	if !isDiscovery(target.ParentType) {
		return nil
	}
	return c.verifyDiscoveryAncestors(ctx, target)
}

func (c *client) discoveryReferences(kind, id string, data map[string]any) (map[string][]string, error) {
	refs := references(c, map[string]any{"kmsKeyName": data["kmsKeyName"], "cmekConfig": data["cmekConfig"]})
	add := func(target, value string) error {
		native, err := c.discoveryID(target, value)
		if err != nil {
			return err
		}
		if !slices.Contains(refs[target], native) {
			refs[target] = append(refs[target], native)
		}
		return nil
	}
	parent := id[:strings.LastIndex(id, "/")]
	parent = parent[:strings.LastIndex(parent, "/")]
	if kind == discoveryEngineType {
		values, err := discoveryStrings(data["dataStoreIds"])
		if err != nil {
			return nil, groupDenied("discoveryengine_datastore_reference_invalid")
		}
		for _, value := range values {
			if !segmentPattern.MatchString(value) || value == "." || value == ".." {
				return nil, groupDenied("discoveryengine_datastore_reference_invalid")
			}
			if err := add(discoveryDataStoreType, parent+"/dataStores/"+value); err != nil {
				return nil, err
			}
		}
	}
	if kind == discoveryHost+"/Document" && data["schemaId"] != nil {
		value := text(data["schemaId"])
		if !segmentPattern.MatchString(value) || value == "." || value == ".." {
			return nil, groupDenied("discoveryengine_schema_reference_invalid")
		}
		store := parent[:strings.LastIndex(parent, "/branches/")]
		if err := add(discoveryHost+"/Schema", store+"/schemas/"+value); err != nil {
			return nil, err
		}
	}
	if kind == discoveryHost+"/ServingConfig" {
		for _, field := range []string{"boostControlIds", "filterControlIds", "redirectControlIds", "synonymsControlIds", "onewaySynonymsControlIds", "dissociateControlIds", "replacementControlIds", "ignoreControlIds", "promoteControlIds"} {
			values, err := discoveryStrings(data[field])
			if err != nil {
				return nil, groupDenied("discoveryengine_control_reference_invalid")
			}
			for _, value := range values {
				if err := add(discoveryHost+"/Control", parent+"/controls/"+value); err != nil {
					return nil, err
				}
			}
		}
	}
	return refs, nil
}

func discoveryStrings(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	if values, ok := raw.([]string); ok {
		return values, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, groupDenied("discoveryengine_list_invalid")
	}
	var result []string
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok || value == "" || strings.TrimSpace(value) != value {
			return nil, groupDenied("discoveryengine_list_invalid")
		}
		result = append(result, value)
	}
	return result, nil
}

// Customer document text, schemas, prompts, conversation turns and connector
// configuration may carry sensitive values under ordinary keys. The proof is
// taken from the original response before this metadata-only view is produced.
func safeDiscoveryPayload(raw map[string]any) map[string]any {
	cleaned := safePayload(raw)
	// Product-specific filtering also covers malformed records that omit name;
	// error paths must not log customer content before identity validation fails.
	keep := []string{"name", "id", "createTime", "updateTime", "displayName", "solutionType", "solutionTypes", "industryVertical", "contentConfig", "aclEnabled", "dataStoreIds", "schemaId", "defaultSchemaId", "startTime", "endTime", "state", "labels", "type", "indexTime", "project_id", "project_number", "cleanup_protected", "cleanup_protection_reason"}
	containers := []string{"body", "resource", "data", "collections", "dataStores", "engines", "schemas", "controls", "servingConfigs", "sessions", "conversations", "assistants", "branches", "documents", "targetSites", "sitemapsMetadata", "sitemap"}
	var redact func(map[string]any)
	redact = func(value map[string]any) {
		for key, child := range value {
			if key == "indexingStatus" || key == "isDefault" || key == "lastDocumentImportTime" {
				valid := false
				switch key {
				case "indexingStatus":
					valid = slices.Contains([]string{"INDEXING_STATUS_UNSPECIFIED", "PENDING", "FAILED", "SUCCEEDED", "DELETING", "CANCELLABLE", "CANCELLED"}, text(child))
				case "isDefault":
					_, valid = child.(bool)
				case "lastDocumentImportTime":
					_, err := time.Parse(time.RFC3339Nano, text(child))
					valid = err == nil
				}
				if !valid {
					value[key] = "[REDACTED]"
				}
				continue
			}
			if key == "indexStatus" {
				// Preserve only the typed timestamp, never indexing messages,
				// error samples or malformed customer content.
				status := map[string]any{}
				if stamp := text(object(child)["indexTime"]); stamp != "" {
					if _, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
						status["indexTime"] = stamp
					}
				}
				value[key] = status
				continue
			}
			if slices.Contains(keep, key) || strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") {
				continue
			}
			if slices.Contains(containers, key) {
				switch nested := child.(type) {
				case map[string]any:
					redact(nested)
				case []any:
					for i, item := range nested {
						if record, ok := item.(map[string]any); ok {
							redact(record)
						} else {
							nested[i] = "[REDACTED]"
						}
					}
				default:
					value[key] = "[REDACTED]"
				}
			} else if slices.Contains([]string{"method", "path", "request_id", "status_code", "assetType", "location", "nextPageToken", "done", "error"}, key) {
				continue
			} else {
				value[key] = "[REDACTED]"
			}
		}
	}
	redact(cleaned)
	return cleaned
}

type discoveryAncestor struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Configuration string `json:"configuration"`
}

func discoveryAncestors(parent contracts.InventoryItem) (string, error) {
	var chain []discoveryAncestor
	if raw := text(parent.Normalized["_discoveryengine_ancestors"]); raw != "" {
		if json.Unmarshal([]byte(raw), &chain) != nil {
			return "", groupDenied("discoveryengine_ancestor_proof_invalid")
		}
	}
	chain = append(chain, discoveryAncestor{parent.NativeType, parent.NativeID, text(parent.Normalized[discoveryProof])})
	raw, _ := json.Marshal(chain)
	return string(raw), nil
}

func (c *client) verifyDiscoveryAncestors(ctx context.Context, target productTarget) error {
	var chain []discoveryAncestor
	if len(target.ParentContainerChain) > 32*1024 || json.Unmarshal([]byte(target.ParentContainerChain), &chain) != nil || len(chain) == 0 || len(chain) > 5 {
		return groupDenied("discoveryengine_ancestor_proof_missing")
	}
	previous, previousKind := "", ""
	for i, ancestor := range chain {
		parentKind, parentID, err := c.discoveryParent(ancestor.Kind, ancestor.ID)
		if err != nil || parentKind != previousKind || parentID != previous {
			return groupDenied("discoveryengine_ancestor_relation_invalid")
		}
		if !isDiscovery(ancestor.Kind) || ancestor.Configuration == "" || (i == 0 && ancestor.Kind != discoveryCollectionType) || (i > 0 && !strings.HasPrefix(ancestor.ID, previous+"/")) {
			return groupDenied("discoveryengine_ancestor_proof_invalid")
		}
		live, err := c.discoveryRead(ctx, ancestor.Kind, ancestor.ID)
		if err != nil {
			return err
		}
		if discoveryConfiguration(live) != ancestor.Configuration {
			return groupDenied("discoveryengine_ancestor_changed")
		}
		previous, previousKind = ancestor.ID, ancestor.Kind
	}
	parent := chain[len(chain)-1]
	if parent.Kind != target.ParentType || parent.ID != target.ParentID || parent.Configuration != target.ParentConfiguration {
		return groupDenied("discoveryengine_ancestor_parent_changed")
	}
	return nil
}

func (c *client) discoveryParent(kind, id string) (string, string, error) {
	native, ok := findType(kind)
	if !ok || !isDiscovery(kind) {
		return "", "", groupDenied("discoveryengine_parent_kind_invalid")
	}
	if _, err := c.resourceURL(native, id); err != nil {
		return "", "", err
	}
	if kind == discoveryCollectionType {
		return "", "", nil
	}
	parent := strings.TrimSuffix(id, "/siteSearchEngine")
	if kind != discoverySiteType {
		parent = id[:strings.LastIndex(id, "/")]
		parent = parent[:strings.LastIndex(parent, "/")]
	}
	for _, native := range allTypes() {
		if !isDiscovery(native.NativeType) {
			continue
		}
		if _, err := c.resourceURL(native, parent); err == nil {
			return native.NativeType, parent, nil
		}
	}
	return "", "", groupDenied("discoveryengine_parent_identity_invalid")
}
