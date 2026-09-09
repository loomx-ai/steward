package azure

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const eventHubClusterType = "Microsoft.EventHub/clusters"

// Quota configuration is a singleton settings object, without an ARM resource
// identity or a DELETE. Keep it as cluster properties instead of inventing a
// separate resource or deletion action.
func (c *client) eventHubClusterSettings(ctx context.Context, id string) (map[string]any, error) {
	kind, _ := findType(eventHubClusterType)
	_, parameters, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, _ := metadata.catalog.Operation("Azure.Microsoft.EventHub.Configuration_Get")
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", bound.URL)
	if err != nil {
		return nil, err
	}
	settings, ok := live.data["settings"].(map[string]any)
	if live.status != 200 || live.data["error"] != nil || !ok || settings == nil {
		return nil, fmt.Errorf("invalid Event Hubs cluster quota configuration")
	}
	for _, value := range settings {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("invalid Event Hubs cluster quota setting")
		}
	}
	return object(safePayload(live.data)["settings"]), nil
}

func (c *client) verifyEventHubClusterSettings(ctx context.Context, planned asset.Asset) error {
	settings, err := c.eventHubClusterSettings(ctx, planned.Identity.NativeID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(planned.Normalized["quotaSettings"], settings) {
		return serviceDenied("eventhub_cluster_configuration_changed")
	}
	return nil
}

func eventHubClusterReference(value any, expected string) bool {
	valueID, ok := value.(string)
	id, kind, err := parseID(valueID)
	return ok && err == nil && strings.EqualFold(kind, eventHubClusterType) && strings.EqualFold(id, expected)
}

// ListNamespaces returns full namespace IDs, including other resource groups,
// rather than nested cluster resources. Require each namespace's reciprocal
// clusterArmId and a stable complete member set before planning its deletion.
// https://learn.microsoft.com/rest/api/eventhub/clusters/list-namespaces?view=rest-eventhub-2024-01-01
func (c *client) eventHubClusterNamespaces(ctx context.Context, parent asset.Identity) ([]serviceChild, error) {
	kind, _ := findType(eventHubClusterType)
	_, parameters, err := c.resourceOperation(kind, parent.NativeID, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, _ := metadata.catalog.Operation("Azure.Microsoft.EventHub.Clusters_ListNamespaces")
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	list := func() ([]string, error) {
		records, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		seen := map[string]bool{}
		for _, record := range records {
			value := object(record)
			id, kind, err := parseID(text(value["id"]))
			if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(kind, eventHubNamespaceType) || !validResponseType(eventHubNamespaceType, text(value["type"])) || seen[id] {
				return nil, fmt.Errorf("invalid or duplicate Event Hubs cluster namespace")
			}
			seen[id] = true
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids, nil
	}
	ids, err := list()
	if err != nil {
		return nil, err
	}
	namespaceKind, _ := findType(eventHubNamespaceType)
	children := []serviceChild{}
	for _, id := range ids {
		endpoint, err := c.resourceURL(namespaceKind, id)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, id, eventHubNamespaceType) || !eventHubClusterReference(object(live.data["properties"])["clusterArmId"], parent.NativeID) {
			return nil, serviceDenied("eventhub_cluster_membership_changed")
		}
		children = append(children, serviceChild{kind: eventHubNamespaceType, id: id, data: live.data, direct: true})
	}
	current, err := list()
	if err != nil {
		return nil, err
	}
	if !slices.Equal(ids, current) {
		return nil, serviceDenied("eventhub_cluster_membership_changed")
	}
	// Namespace association changes need not change an ARM ETag. Repeat both
	// identity and backlink checks after the final member-list read.
	for _, child := range children {
		endpoint, _ := c.resourceURL(namespaceKind, child.id)
		live, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, child.id, child.kind) || !eventHubClusterReference(object(live.data["properties"])["clusterArmId"], parent.NativeID) {
			return nil, serviceDenied("eventhub_cluster_membership_changed")
		}
		if err := serviceListedIncarnation(child.data, live.data); err != nil {
			return nil, err
		}
	}
	return children, nil // serviceChildren re-reads the bound cluster itself.
}

func eventHubClusterMinimumAge(raw map[string]any, now time.Time) string {
	// Azure rejects dedicated cluster deletion during its first four hours.
	// https://learn.microsoft.com/azure/event-hubs/event-hubs-dedicated-cluster-create-portal#delete-a-dedicated-cluster
	value := object(raw["properties"])["createdAt"]
	if value == nil || value == "" {
		value = object(raw["systemData"])["createdAt"]
	}
	if value == nil || value == "" {
		return "" // Older responses can omit creation time; Azure enforces it.
	}
	created, err := time.Parse(time.RFC3339Nano, text(value))
	if err != nil {
		return "azure_eventhub_cluster_invalid_creation_time"
	}
	if now.Before(created.Add(4 * time.Hour)) {
		return "azure_eventhub_cluster_minimum_age"
	}
	return ""
}
