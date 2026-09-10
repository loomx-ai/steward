package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	mongoClusterType         = "Microsoft.DocumentDB/mongoClusters"
	mongoClusterFirewallType = mongoClusterType + "/firewallRules"
	mongoClusterPECType      = mongoClusterType + "/privateEndpointConnections"
	mongoClusterUserType     = mongoClusterType + "/users"
)

func isMongoClusterType(kind string) bool {
	return slices.ContainsFunc([]string{mongoClusterType, mongoClusterFirewallType, mongoClusterPECType, mongoClusterUserType}, func(value string) bool { return strings.EqualFold(value, kind) })
}

func mongoClusterOwnedKinds() []string {
	return []string{mongoClusterFirewallType, mongoClusterPECType, mongoClusterUserType}
}

// The restore window advances while an active cluster runs. Preserve backup
// settings, credentials, creation metadata and the current replication role;
// reconcile the private-endpoint index through its native collection instead.
func mongoClusterSnapshot(kind string, raw map[string]any) map[string]any {
	encoded, _ := json.Marshal(raw)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.Decode(&result)
	result["id"] = strings.ToLower(text(raw["id"]))
	result["name"] = last(text(result["id"]))
	delete(result, "type")
	delete(result, "etag")
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), field)
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	if strings.EqualFold(kind, mongoClusterType) {
		delete(props, "clusterStatus")
		delete(props, "privateEndpointConnections")
		delete(object(props["backup"]), "earliestRestoreTime")
		delete(object(props["replica"]), "replicationState")
	} else {
		delete(result, "location") // ARM proxy resources inherit a display region.
	}
	return result
}

func mongoClusterConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, mongoClusterSnapshot(kind, raw))
}

func mongoClusterIncarnation(planned asset.Asset, live map[string]any) error {
	if isMongoClusterType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_mongocluster_configuration"]); expected == "" || expected != mongoClusterConfiguration(planned.Identity.NativeType, live) {
			return serviceDenied("mongocluster_configuration_changed")
		}
	}
	return nil
}

func (a *action) mongoClusterRequestIdentity(value asset.Asset) error {
	if !isMongoClusterType(a.kind.NativeType) {
		return nil
	}
	id, kind, err := parseID(value.Identity.NativeID)
	if err != nil || value.Identity.Provider != asset.ProviderAzure || id != a.id || !strings.EqualFold(kind, a.kind.NativeType) || !strings.EqualFold(value.Identity.NativeType, a.kind.NativeType) {
		return serviceDenied("mongocluster_request_identity_changed")
	}
	return nil
}

// A restored cluster is independent. Only the current replica.sourceResourceId
// establishes a live dependency; restoreParameters/replicaParameters are history.
func mongoClusterSource(raw map[string]any) (string, error) {
	value := object(raw["properties"])["replica"]
	if value == nil {
		return "", nil
	}
	replica, ok := value.(map[string]any)
	if !ok {
		return "", serviceDenied("invalid_mongocluster_replication")
	}
	if replica["role"] == "Primary" {
		if source := replica["sourceResourceId"]; source != nil && source != "" {
			return "", serviceDenied("invalid_mongocluster_primary_source")
		}
		return "", nil
	}
	if replica["role"] != "AsyncReplica" && replica["role"] != "GeoAsyncReplica" {
		return "", serviceDenied("unknown_mongocluster_replication_role")
	}
	value, err := cognitiveExactString(replica["sourceResourceId"])
	id, kind, parseErr := parseID(text(value))
	if err != nil || parseErr != nil || !strings.EqualFold(kind, mongoClusterType) || strings.EqualFold(id, text(raw["id"])) {
		return "", serviceDenied("invalid_mongocluster_replication_source")
	}
	return id, nil
}

func validateMongoCluster(kind string, raw map[string]any) error {
	if _, ok := raw["properties"].(map[string]any); !ok {
		return serviceDenied("invalid_mongocluster_properties")
	}
	if strings.EqualFold(kind, mongoClusterType) {
		_, err := mongoClusterSource(raw)
		return err
	}
	return nil
}

func mongoClusterReady(kind string, raw map[string]any) error {
	if err := validateMongoCluster(kind, raw); err != nil {
		return err
	}
	props := object(raw["properties"])
	if state := props["provisioningState"]; state != nil && state != "Succeeded" && state != "Failed" && state != "Canceled" {
		return serviceDenied("mongocluster_resource_not_terminal")
	}
	if kind == mongoClusterType {
		if state := props["clusterStatus"]; state != nil && state != "Ready" && state != "Stopped" {
			return serviceDenied("mongocluster_not_terminal")
		}
		// Catch-up does not change cluster roles. Promotions and topology edits
		// must finish before a reviewed primary/replica relationship is used.
		if state := object(props["replica"])["replicationState"]; state != nil && state != "Active" && state != "Broken" && state != "Catchup" {
			return serviceDenied("mongocluster_replication_not_terminal")
		}
	}
	return nil
}

func (c *client) mongoClusterResource(ctx context.Context, id string) (map[string]any, error) {
	_, kind, err := parseID(id)
	if err != nil || !isMongoClusterType(kind) {
		return nil, serviceDenied("invalid_mongocluster_resource")
	}
	raw, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	return raw, validateMongoCluster(kind, raw)
}

func (c *client) mongoClusterInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isMongoClusterType(kind) {
		return nil
	}
	if err := validateMongoCluster(kind, raw); err != nil {
		return err
	}
	normalized["_mongocluster_configuration"] = mongoClusterConfiguration(kind, raw)
	normalized["_mongocluster_private_configuration"] = c.privateConfiguration(mongoClusterSnapshot(kind, raw))
	if kind != mongoClusterType {
		parent, err := c.mongoClusterResource(ctx, redisParentID(id))
		if err != nil {
			return err
		}
		normalized["_mongocluster_parent_configuration"] = c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, parent))
	}
	return nil
}

func (a *action) mongoClusterPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	kind := a.kind.NativeType
	if !isMongoClusterType(kind) {
		return nil
	}
	if err := mongoClusterIncarnation(planned, raw); err != nil {
		return err
	}
	if err := mongoClusterReady(kind, raw); err != nil {
		return err
	}
	if kind != mongoClusterType {
		parent, err := a.client.mongoClusterResource(ctx, redisParentID(a.id))
		if err != nil {
			return err
		}
		if expected := text(planned.Normalized["_mongocluster_parent_configuration"]); expected == "" || expected != a.client.privateConfiguration(mongoClusterSnapshot(mongoClusterType, parent)) {
			return serviceDenied("mongocluster_parent_changed")
		}
		if err := mongoClusterReady(mongoClusterType, parent); err != nil {
			return err
		}
		mapping, _ := findType(mongoClusterType)
		if reason := protectionReason(mapping, parent); reason != "" {
			return serviceDenied(reason)
		}
	}
	return nil
}

// Replicas are separate clusters. Microsoft's deletion order requires their
// own reviewed deletes before the source; deleting one never owns its source.
// https://learn.microsoft.com/azure/documentdb/troubleshoot-replication
func mongoClusterReplicaPrerequisite(parent, child asset.Asset) bool {
	if parent.Identity.NativeType != mongoClusterType || child.Identity.NativeType != mongoClusterType || object(parent.Normalized["replica"])["role"] != "Primary" {
		return false
	}
	source, err := mongoClusterSource(map[string]any{"id": child.Identity.NativeID, "properties": child.Normalized})
	return err == nil && source != "" && strings.EqualFold(source, parent.Identity.NativeID)
}

func (c *client) mongoClusterChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		children, err := c.nativeServiceChildren(ctx, parent, raw, mongoClusterOwnedKinds())
		if err != nil {
			return nil, err
		}
		if err := mongoClusterPECIndex(raw, children); err != nil {
			return nil, err
		}
		mapping, _ := findType(mongoClusterType)
		_, parameters, err := c.resourceOperation(mapping, parent.NativeID, "GET")
		if err != nil {
			return nil, err
		}
		metadata, err := providerData()
		if err != nil {
			return nil, err
		}
		operation, _ := metadata.catalog.Operation("Azure.Microsoft.DocumentDB.Replicas_ListByParent")
		bound, err := bindAzureREST(operation, parameters)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(bound.URL)
		replicas, err := c.listAllURL(ctx, u.String(), u.Path)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{strings.ToLower(parent.NativeID): true}
		for _, value := range replicas {
			listed := object(value)
			id, kind, err := parseID(text(listed["id"]))
			if err != nil || !strings.EqualFold(kind, mongoClusterType) || !validResponseType(mongoClusterType, text(listed["type"])) || seen[id] || object(object(raw["properties"])["replica"])["role"] != "Primary" {
				return nil, serviceDenied("invalid_mongocluster_replica_index")
			}
			seen[id] = true
			live, err := c.mongoClusterResource(ctx, id)
			if err != nil {
				return nil, err
			}
			source, err := mongoClusterSource(live)
			if err != nil || !strings.EqualFold(source, parent.NativeID) {
				return nil, serviceDenied("mongocluster_replica_indexes_disagree")
			}
			if err := serviceListedIncarnation(listed, live); err != nil {
				return nil, err
			}
			if name := listed["name"]; name != nil && !strings.EqualFold(text(name), last(id)) {
				return nil, serviceDenied("invalid_mongocluster_replica_name")
			}
			if location := listed["location"]; location != nil && resourceRegion(listed) != resourceRegion(live) {
				return nil, serviceDenied("mongocluster_replica_location_changed")
			}
			if object(listed["properties"])["replica"] != nil {
				listedSource, err := mongoClusterSource(listed)
				if err != nil || listedSource != source || object(object(listed["properties"])["replica"])["role"] != object(object(live["properties"])["replica"])["role"] {
					return nil, serviceDenied("mongocluster_replica_indexes_disagree")
				}
			}
			children = append(children, serviceChild{kind: mongoClusterType, id: id, data: live})
		}
		current, err := c.mongoClusterResource(ctx, parent.NativeID)
		if err != nil {
			return nil, err
		}
		if err := mongoClusterReady(mongoClusterType, current); err != nil {
			return nil, err
		}
		if err := mongoClusterPECIndex(current, children); err != nil {
			return nil, err
		}
		if c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, raw)) != c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, current)) {
			return nil, serviceDenied("mongocluster_parent_changed")
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
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
		return a.id == b.id && c.privateConfiguration(mongoClusterSnapshot(a.kind, a.data)) == c.privateConfiguration(mongoClusterSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("mongocluster_children_changed")
	}
	return second, nil
}

func mongoClusterPECIndex(raw map[string]any, children []serviceChild) error {
	if value := object(raw["properties"])["privateEndpointConnections"]; value != nil {
		index, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_mongocluster_private_endpoint_index")
		}
		actual := map[string]bool{}
		for _, child := range children {
			if child.kind == mongoClusterPECType {
				actual[child.id] = true
			}
		}
		for _, value := range index {
			id := strings.ToLower(text(object(value)["id"]))
			if !actual[id] {
				return serviceDenied("mongocluster_private_endpoint_indexes_disagree")
			}
			delete(actual, id)
		}
		if len(actual) != 0 {
			return serviceDenied("mongocluster_private_endpoint_indexes_disagree")
		}
	}
	return nil
}

func validateMongoClusterOperationURL(subscription, location, version, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 32*1024 || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || (u.RawPath != "" && u.RawPath != u.Path) {
		return fmt.Errorf("invalid DocumentDB operation endpoint")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || (len(query) != 1 && len(query) != 5) || len(query["api-version"]) != 1 || query.Get("api-version") != version {
		return fmt.Errorf("invalid DocumentDB operation API version")
	}
	if len(query) == 5 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" {
				return fmt.Errorf("invalid DocumentDB signed polling parameters")
			}
		}
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if len(parts) != 9 || parts[1] != "subscriptions" || parts[2] != strings.ToLower(subscription) || parts[3] != "providers" || parts[4] != "microsoft.documentdb" || parts[5] != "locations" || !cosmosOperationRegion.MatchString(parts[6]) || (parts[7] != "mongoclusterazureasyncoperation" && parts[7] != "mongoclusteroperationresults") || !uuidPattern.MatchString(parts[8]) {
		return fmt.Errorf("invalid DocumentDB operation path")
	}
	if location != "" && location != "global" && parts[6] != strings.ReplaceAll(strings.ToLower(location), " ", "") {
		return fmt.Errorf("DocumentDB operation belongs to another region")
	}
	return nil
}
