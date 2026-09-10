package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	kustoType                  = "Microsoft.Kusto/clusters"
	kustoAttachmentType        = kustoType + "/attachedDatabaseConfigurations"
	kustoDatabaseType          = kustoType + "/databases"
	kustoDataConnectionType    = kustoDatabaseType + "/dataConnections"
	kustoDatabasePrincipalType = kustoDatabaseType + "/principalAssignments"
	kustoScriptType            = kustoDatabaseType + "/scripts"
	kustoManagedEndpointType   = kustoType + "/managedPrivateEndpoints"
	kustoPrincipalType         = kustoType + "/principalAssignments"
	kustoEndpointType          = kustoType + "/privateEndpointConnections"
	kustoImageType             = kustoType + "/sandboxCustomImages"
)

func kustoKind(kind string) string {
	for _, value := range []string{kustoType, kustoAttachmentType, kustoDatabaseType, kustoDataConnectionType, kustoDatabasePrincipalType, kustoScriptType, kustoManagedEndpointType, kustoPrincipalType, kustoEndpointType, kustoImageType} {
		if strings.EqualFold(value, kind) {
			return value
		}
	}
	return ""
}
func isKustoType(kind string) bool { return kustoKind(kind) != "" }
func kustoOwnedKinds(kind string) []string {
	switch kustoKind(kind) {
	case kustoType:
		return []string{kustoAttachmentType, kustoDatabaseType, kustoManagedEndpointType, kustoPrincipalType, kustoEndpointType, kustoImageType}
	case kustoDatabaseType:
		return []string{kustoDataConnectionType, kustoDatabasePrincipalType, kustoScriptType}
	}
	return nil
}

// Database size changes with ingestion. Native child and follower indexes are
// reconciled separately; retain all policy, identity, script and secret values.
func kustoSnapshot(kind string, raw map[string]any) map[string]any {
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
	if kustoKind(kind) != kustoType {
		delete(result, "location")
	}
	switch kustoKind(kind) {
	case kustoType:
		delete(props, "privateEndpointConnections")
		delete(props, "state")
		delete(props, "stateReason")
	case kustoDatabaseType:
		delete(props, "statistics")
		delete(props, "isFollowed")
	case kustoAttachmentType:
		delete(props, "attachedDatabaseNames")
	}
	return result
}
func kustoConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, kustoSnapshot(kind, raw))
}
func kustoIncarnation(planned asset.Asset, live map[string]any) error {
	if isKustoType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_kusto_configuration"]); expected == "" || expected != kustoConfiguration(planned.Identity.NativeType, live) {
			return serviceDenied("kusto_configuration_changed")
		}
	}
	return nil
}
func (a *action) kustoRequestIdentity(value asset.Asset) error {
	if !isKustoType(a.kind.NativeType) {
		return nil
	}
	id, kind, err := parseID(value.Identity.NativeID)
	if err != nil || value.Identity.Provider != asset.ProviderAzure || id != a.id || !strings.EqualFold(kind, a.kind.NativeType) || !strings.EqualFold(value.Identity.NativeType, a.kind.NativeType) {
		return serviceDenied("kusto_request_identity_changed")
	}
	return nil
}
func kustoClusterID(id string) string {
	id, kind, err := parseID(id)
	if err != nil || !isKustoType(kind) {
		return ""
	}
	return strings.Join(strings.Split(id, "/")[:9], "/")
}
func kustoAncestorIDs(id string) []string {
	_, kind, err := parseID(id)
	if err != nil || !isKustoType(kind) {
		return nil
	}
	var result []string
	for kustoKind(kind) != kustoType {
		id = redisParentID(id)
		_, kind, _ = parseID(id)
		result = append(result, id)
	}
	return result
}
func kustoName(value any) (string, error) {
	name, ok := value.(string)
	if !ok || name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\\?#%") || strings.ContainsAny(name, "\r\n\x00") || name == "." || name == ".." {
		return "", serviceDenied("invalid_kusto_name")
	}
	return name, nil
}
func kustoAttachmentSource(raw map[string]any) (string, string, error) {
	props := object(raw["properties"])
	id, kind, err := parseID(text(props["clusterResourceId"]))
	name, nameErr := kustoName(props["databaseName"])
	if err != nil || kustoKind(kind) != kustoType || id == kustoClusterID(text(raw["id"])) || nameErr != nil {
		return "", "", serviceDenied("invalid_kusto_attachment_source")
	}
	return id, name, nil
}
func kustoFollowingAttachment(raw map[string]any) (string, error) {
	if raw["kind"] != "ReadOnlyFollowing" {
		return "", nil
	}
	props := object(raw["properties"])
	id, kind, err := parseID(text(props["leaderClusterResourceId"]))
	name, nameErr := kustoName(props["attachedDatabaseConfigurationName"])
	_, databaseErr := kustoName(props["originalDatabaseName"])
	if err != nil || kustoKind(kind) != kustoType || id == kustoClusterID(text(raw["id"])) || nameErr != nil || databaseErr != nil {
		return "", serviceDenied("invalid_kusto_following_database")
	}
	return kustoClusterID(text(raw["id"])) + "/attacheddatabaseconfigurations/" + strings.ToLower(name), nil
}
func validateKusto(kind string, raw map[string]any) error {
	if _, ok := raw["properties"].(map[string]any); !ok {
		return serviceDenied("invalid_kusto_properties")
	}
	switch kustoKind(kind) {
	case kustoDatabaseType:
		if raw["kind"] != "ReadWrite" && raw["kind"] != "ReadOnlyFollowing" {
			return serviceDenied("unknown_kusto_database_kind")
		}
		_, err := kustoFollowingAttachment(raw)
		return err
	case kustoDataConnectionType:
		if !slices.Contains([]string{"EventHub", "EventGrid", "IotHub", "CosmosDb", "EventHubWithManagedIdentity", "EventGridWithManagedIdentity"}, text(raw["kind"])) {
			return serviceDenied("unknown_kusto_data_connection_kind")
		}
	case kustoAttachmentType:
		_, _, err := kustoAttachmentSource(raw)
		return err
	}
	return nil
}
func kustoReady(kind string, raw map[string]any) error {
	if err := validateKusto(kind, raw); err != nil {
		return err
	}
	props := object(raw["properties"])
	if state := props["provisioningState"]; state != nil && state != "Succeeded" && state != "Failed" && state != "Canceled" {
		return serviceDenied("kusto_resource_not_terminal")
	}
	if kustoKind(kind) == kustoType {
		if state := props["state"]; state != nil && state != "Running" && state != "Stopped" && state != "Unavailable" && state != "Migrated" {
			return serviceDenied("kusto_cluster_not_terminal")
		}
		if migration := props["migrationCluster"]; migration != nil {
			peer, peerKind, err := parseID(text(object(migration)["id"]))
			if err != nil || kustoKind(peerKind) != kustoType || peer == strings.ToLower(text(raw["id"])) || !slices.Contains([]string{"Source", "Destination"}, text(object(migration)["role"])) {
				return serviceDenied("invalid_kusto_migration")
			}
		}
	}
	return nil
}
func kustoProtection(kind string, raw map[string]any) string {
	if kustoKind(kind) == kustoDatabaseType && raw["kind"] == "ReadOnlyFollowing" {
		return "azure_kusto_following_database"
	}
	return ""
}
func (c *client) kustoResource(ctx context.Context, id string) (map[string]any, error) {
	_, kind, err := parseID(id)
	if err != nil || !isKustoType(kind) {
		return nil, serviceDenied("invalid_kusto_resource")
	}
	raw, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	return raw, validateKusto(kind, raw)
}
func (c *client) kustoInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isKustoType(kind) {
		return nil
	}
	if err := validateKusto(kind, raw); err != nil {
		return err
	}
	normalized["kind"] = raw["kind"]
	normalized["_kusto_configuration"] = kustoConfiguration(kind, raw)
	normalized["_kusto_private_configuration"] = c.privateConfiguration(kustoSnapshot(kind, raw))
	ancestors := map[string]any{}
	for _, ancestor := range kustoAncestorIDs(id) {
		parent, err := c.kustoResource(ctx, ancestor)
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(ancestor)
		ancestors[ancestor] = c.privateConfiguration(kustoSnapshot(parentKind, parent))
		if kustoKind(parentKind) == kustoType {
			// Proxy responses may contain display text such as DummyLocation.
			// Their cluster supplies the actual regional scope and poll location.
			normalized["_kusto_location"] = resourceRegion(parent)
		}
		if kind == kustoImageType && kustoKind(parentKind) == kustoType {
			active, err := kustoActiveImage(parent, last(id))
			if err != nil {
				return err
			}
			normalized["_kusto_active_image"] = active
		}
	}
	normalized["_kusto_ancestors"] = ancestors
	if kustoKind(kind) == kustoManagedEndpointType {
		targetID, err := kustoEndpointTarget(raw)
		if err != nil {
			return err
		}
		target, err := c.linkedResource(ctx, targetID)
		if err != nil {
			return err
		}
		normalized["_kusto_target_configuration"] = c.privateConfiguration(searchTargetSnapshot(target))
	}
	return nil
}
func (c *client) kustoAncestors(ctx context.Context, id string, expected map[string]any, protection bool) error {
	ids := kustoAncestorIDs(id)
	if len(ids) != len(expected) {
		return serviceDenied("kusto_ancestor_set_changed")
	}
	for _, ancestor := range ids {
		parent, err := c.kustoResource(ctx, ancestor)
		if err != nil {
			return err
		}
		_, kind, _ := parseID(ancestor)
		if text(expected[ancestor]) == "" || text(expected[ancestor]) != c.privateConfiguration(kustoSnapshot(kind, parent)) {
			return serviceDenied("kusto_ancestor_changed")
		}
		if protection {
			if err := kustoReady(kind, parent); err != nil {
				return err
			}
			mapping, _ := findType(kind)
			if reason := protectionReason(mapping, parent); reason != "" && reason != "azure_kusto_following_database" {
				return serviceDenied(reason)
			}
		}
	}
	return nil
}
func (a *action) kustoPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if !isKustoType(a.kind.NativeType) {
		return nil
	}
	if err := kustoIncarnation(planned, raw); err != nil {
		return err
	}
	if err := kustoReady(a.kind.NativeType, raw); err != nil {
		return err
	}
	if err := a.client.kustoAncestors(ctx, a.id, object(planned.Normalized["_kusto_ancestors"]), true); err != nil {
		return err
	}
	if a.kind.NativeType == kustoImageType {
		parent, err := a.client.kustoResource(ctx, kustoClusterID(a.id))
		if err != nil {
			return err
		}
		active, err := kustoActiveImage(parent, last(a.id))
		if err != nil {
			return err
		}
		if active {
			return serviceDenied("azure_kusto_active_image")
		}
	}
	return a.kustoTargetPreflight(ctx, planned, raw)
}

func (a *action) kustoTargetPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if a.kind.NativeType == kustoManagedEndpointType {
		targetID, err := kustoEndpointTarget(raw)
		if err != nil {
			return err
		}
		target, err := a.client.linkedResource(ctx, targetID)
		if err != nil {
			return err
		}
		expected := text(planned.Normalized["_kusto_target_configuration"])
		if expected == "" || expected != a.client.privateConfiguration(searchTargetSnapshot(target)) {
			return serviceDenied("kusto_link_target_changed")
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return err
		}
		if err := a.client.linkedResourceProtection(ctx, targetID, target, locks); err != nil {
			return err
		}
		current, err := a.client.linkedResource(ctx, targetID)
		if err != nil {
			return err
		}
		if expected != a.client.privateConfiguration(searchTargetSnapshot(current)) {
			return serviceDenied("kusto_link_target_changed")
		}
	}
	return nil
}

func kustoEndpointTarget(raw map[string]any) (string, error) {
	props := object(raw["properties"])
	value, ok := props["privateLinkResourceId"].(string)
	id, _, err := parseID(value)
	_, groupErr := kustoName(props["groupId"])
	if !ok || value != strings.TrimSpace(value) || err != nil || groupErr != nil {
		return "", serviceDenied("invalid_kusto_link_target")
	}
	return id, nil
}
func kustoActiveImage(raw map[string]any, name string) (bool, error) {
	value := object(raw["properties"])["languageExtensions"]
	if value == nil {
		return false, nil
	}
	list, ok := value.(map[string]any)
	if !ok || list["nextLink"] != nil && list["nextLink"] != "" {
		return false, serviceDenied("incomplete_kusto_language_extensions")
	}
	items, ok := list["value"].([]any)
	if !ok {
		return false, serviceDenied("invalid_kusto_language_extensions")
	}
	for _, item := range items {
		extension, ok := item.(map[string]any)
		if !ok {
			return false, serviceDenied("invalid_kusto_language_extension")
		}
		if strings.EqualFold(text(extension["languageExtensionCustomImageName"]), name) {
			return true, nil
		}
	}
	return false, nil
}

func kustoSharedPrerequisite(parent, child asset.Asset) bool {
	if child.Identity.NativeType != kustoAttachmentType || (parent.Identity.NativeType != kustoType && parent.Identity.NativeType != kustoDatabaseType) {
		return false
	}
	source, database, err := kustoAttachmentSource(map[string]any{"id": child.Identity.NativeID, "properties": child.Normalized})
	return err == nil && source == kustoClusterID(parent.Identity.NativeID) && (parent.Identity.NativeType == kustoType || database == "*" || strings.EqualFold(database, last(parent.Identity.NativeID)))
}
func kustoControlledDatabase(parent, child asset.Asset) bool {
	if parent.Identity.NativeType != kustoAttachmentType || child.Identity.NativeType != kustoDatabaseType {
		return false
	}
	attachment, err := kustoFollowingAttachment(map[string]any{"id": child.Identity.NativeID, "kind": child.Normalized["kind"], "properties": child.Normalized})
	return err == nil && strings.EqualFold(attachment, parent.Identity.NativeID)
}

// A native GET index identifies the follower's attachment, not ownership of
// its cluster or source database. Read that actual attachment before planning
// its independent DELETE as a prerequisite of the source resource.
func (c *client) kustoFollowerAttachments(ctx context.Context, parent asset.Identity) ([]serviceChild, error) {
	root := kustoClusterID(parent.NativeID)
	mapping, _ := findType(kustoType)
	_, parameters, err := c.resourceOperation(mapping, root, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, _ := metadata.catalog.Operation("Azure.Microsoft.Kusto.Clusters_ListFollowerDatabasesGet")
	bound, err := bindAzureREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	values, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var children []serviceChild
	for _, value := range values {
		properties := object(object(value)["properties"])
		follower, kind, err := parseID(text(properties["clusterResourceId"]))
		name, nameErr := kustoName(properties["attachedDatabaseConfigurationName"])
		database, databaseErr := kustoName(properties["databaseName"])
		if err != nil || kustoKind(kind) != kustoType || follower == root || nameErr != nil || databaseErr != nil {
			return nil, serviceDenied("invalid_kusto_follower_index")
		}
		id := follower + "/attacheddatabaseconfigurations/" + strings.ToLower(name)
		if seen[id] {
			return nil, serviceDenied("duplicate_kusto_follower")
		}
		seen[id] = true
		live, err := c.kustoResource(ctx, id)
		if err != nil {
			return nil, err
		}
		source, selector, err := kustoAttachmentSource(live)
		if err != nil || source != root || !strings.EqualFold(selector, database) {
			return nil, serviceDenied("kusto_follower_indexes_disagree")
		}
		if sharing := properties["tableLevelSharingProperties"]; sharing != nil && !reflect.DeepEqual(sharing, object(live["properties"])["tableLevelSharingProperties"]) {
			return nil, serviceDenied("kusto_follower_sharing_changed")
		}
		if parent.NativeType == kustoType || database == "*" || strings.EqualFold(database, last(parent.NativeID)) {
			children = append(children, serviceChild{kind: kustoAttachmentType, id: id, data: live})
		}
	}
	return children, nil
}

// Attached databases cannot be deleted directly. The attachment controls only
// the matching local read-only views. Its native names and each database's
// source, original name and attachment name must agree before delegation.
func (c *client) kustoAttachedDatabases(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	source, selector, err := kustoAttachmentSource(raw)
	if err != nil {
		return nil, err
	}
	clusterID := kustoClusterID(parent.NativeID)
	cluster, err := c.kustoResource(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	all, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: clusterID, NativeType: kustoType}, cluster, []string{kustoDatabaseType})
	if err != nil {
		return nil, err
	}
	var children []serviceChild
	for _, child := range all {
		if err := validateKusto(child.kind, child.data); err != nil {
			return nil, err
		}
		attachment, err := kustoFollowingAttachment(child.data)
		if err != nil {
			return nil, err
		}
		if attachment != strings.ToLower(parent.NativeID) {
			continue
		}
		props := object(child.data["properties"])
		if !strings.EqualFold(text(props["leaderClusterResourceId"]), source) || selector != "*" && !strings.EqualFold(selector, text(props["originalDatabaseName"])) {
			return nil, serviceDenied("kusto_attachment_database_changed")
		}
		name := text(props["originalDatabaseName"])
		if override := object(raw["properties"])["databaseNameOverride"]; override != nil && override != "" {
			if selector == "*" {
				return nil, serviceDenied("invalid_kusto_database_override")
			}
			name = text(override)
		}
		name = text(object(raw["properties"])["databaseNamePrefix"]) + name
		if !strings.EqualFold(name, last(child.id)) {
			return nil, serviceDenied("kusto_attachment_database_name_changed")
		}
		children = append(children, child)
	}
	return children, kustoAttachedDatabaseIndex(raw, children)
}

func kustoAttachedDatabaseIndex(raw map[string]any, children []serviceChild) error {
	if index := object(raw["properties"])["attachedDatabaseNames"]; index != nil {
		values, ok := index.([]any)
		if !ok {
			return serviceDenied("invalid_kusto_attached_database_index")
		}
		names := map[string]bool{}
		for _, child := range children {
			names[strings.ToLower(text(object(child.data["properties"])["originalDatabaseName"]))] = true
		}
		for _, value := range values {
			name, err := kustoName(value)
			if err != nil || !names[strings.ToLower(name)] {
				return serviceDenied("kusto_attached_database_indexes_disagree")
			}
			delete(names, strings.ToLower(name))
		}
		if len(names) != 0 {
			return serviceDenied("kusto_attached_database_indexes_disagree")
		}
	}
	return nil
}

func kustoDatabaseFollowerIndex(raw map[string]any, children []serviceChild) error {
	if value := object(raw["properties"])["isFollowed"]; value != nil {
		followed, ok := value.(bool)
		actual := slices.ContainsFunc(children, func(child serviceChild) bool { return child.kind == kustoAttachmentType })
		if !ok || followed != actual {
			return serviceDenied("kusto_database_follower_indexes_disagree")
		}
	}
	return nil
}
func kustoPrivateEndpointIndex(raw map[string]any, children []serviceChild) error {
	value := object(raw["properties"])["privateEndpointConnections"]
	if value == nil {
		return nil
	}
	index, ok := value.([]any)
	if !ok {
		return serviceDenied("invalid_kusto_endpoint_index")
	}
	actual := map[string]bool{}
	for _, child := range children {
		if child.kind == kustoEndpointType {
			actual[child.id] = true
		}
	}
	for _, value := range index {
		id := strings.ToLower(text(object(value)["id"]))
		if !actual[id] {
			return serviceDenied("kusto_endpoint_indexes_disagree")
		}
		delete(actual, id)
	}
	if len(actual) != 0 {
		return serviceDenied("kusto_endpoint_indexes_disagree")
	}
	return nil
}
func (c *client) kustoChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		var children []serviceChild
		var err error
		if parent.NativeType == kustoAttachmentType {
			children, err = c.kustoAttachedDatabases(ctx, parent, raw)
		} else {
			children, err = c.nativeServiceChildren(ctx, parent, raw, kustoOwnedKinds(parent.NativeType))
		}
		if err != nil {
			return nil, err
		}
		if parent.NativeType == kustoType {
			if err := kustoPrivateEndpointIndex(raw, children); err != nil {
				return nil, err
			}
			// The attachment is the sole lifecycle controller of a followed view.
			var ordinary []serviceChild
			for _, child := range children {
				if child.kind == kustoDatabaseType && child.data["kind"] == "ReadOnlyFollowing" {
					id, err := kustoFollowingAttachment(child.data)
					if err != nil {
						return nil, err
					}
					if !slices.ContainsFunc(children, func(candidate serviceChild) bool { return candidate.kind == kustoAttachmentType && candidate.id == id }) {
						return nil, serviceDenied("kusto_following_database_has_no_attachment")
					}
					continue
				}
				ordinary = append(ordinary, child)
			}
			children = ordinary
		}
		if parent.NativeType == kustoType || parent.NativeType == kustoDatabaseType && raw["kind"] == "ReadWrite" {
			followers, err := c.kustoFollowerAttachments(ctx, parent)
			if err != nil {
				return nil, err
			}
			if parent.NativeType == kustoDatabaseType {
				if err := kustoDatabaseFollowerIndex(raw, followers); err != nil {
					return nil, err
				}
			}
			children = append(children, followers...)
		}
		current, err := c.kustoResource(ctx, parent.NativeID)
		if err != nil {
			return nil, err
		}
		if err := kustoReady(parent.NativeType, current); err != nil {
			return nil, err
		}
		if parent.NativeType == kustoType {
			if err := kustoPrivateEndpointIndex(current, children); err != nil {
				return nil, err
			}
		}
		if parent.NativeType == kustoAttachmentType {
			if err := kustoAttachedDatabaseIndex(current, children); err != nil {
				return nil, err
			}
		}
		if parent.NativeType == kustoDatabaseType && raw["kind"] == "ReadWrite" {
			if err := kustoDatabaseFollowerIndex(current, children); err != nil {
				return nil, err
			}
		}
		if c.privateConfiguration(kustoSnapshot(parent.NativeType, raw)) != c.privateConfiguration(kustoSnapshot(parent.NativeType, current)) {
			return nil, serviceDenied("kusto_parent_changed")
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		for _, child := range children {
			if err := kustoReady(child.kind, child.data); err != nil {
				return nil, err
			}
		}
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(kustoSnapshot(a.kind, a.data)) == c.privateConfiguration(kustoSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("kusto_children_changed")
	}
	return second, nil
}

// Native examples return earlier, still published operation API versions, and
// the CLI uses display-region segments such as West%20US%202. Permit only those
// exact versions and the single operationResults collection, never other URLs.
func validateKustoOperationURL(subscription, location, version, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32*1024 || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid Kusto operation endpoint")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || !slices.Contains([]string{version, "2022-02-01", "2022-12-29", "2023-05-02"}, query.Get("api-version")) {
		return fmt.Errorf("invalid Kusto operation version")
	}
	for key, values := range query {
		if key != "api-version" && (key != "operationResultResponseType" || len(values) != 1 || values[0] != "Location") {
			return fmt.Errorf("invalid Kusto operation query")
		}
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if len(parts) != 9 || parts[1] != "subscriptions" || parts[2] != strings.ToLower(subscription) || parts[3] != "providers" || parts[4] != "microsoft.kusto" || parts[5] != "locations" || !cosmosOperationRegion.MatchString(strings.ReplaceAll(parts[6], " ", "")) || parts[7] != "operationresults" || !uuidPattern.MatchString(parts[8]) {
		return fmt.Errorf("invalid Kusto operation path")
	}
	// Escapes are allowed only for spaces within the native display-region.
	if strings.ReplaceAll(strings.ToLower(u.EscapedPath()), "%20", " ") != strings.ToLower(u.Path) {
		return fmt.Errorf("invalid Kusto operation path encoding")
	}
	if location != "" && location != "global" && strings.ReplaceAll(parts[6], " ", "") != strings.ReplaceAll(strings.ToLower(location), " ", "") {
		return fmt.Errorf("Kusto operation belongs to another region")
	}
	return nil
}
