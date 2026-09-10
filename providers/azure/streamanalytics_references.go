package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Stream Analytics connectors often return service names rather than ARM IDs.
// Resolve names against the native subscription collection. Never assume that
// the connector and its data store share a resource group or subscription.
func (c *client) streamAnalyticsNamedResource(ctx context.Context, kind string, value any) (string, error) {
	if supplied, ok := value.(string); ok && strings.HasPrefix(supplied, "/") {
		id, typ, err := parseID(supplied)
		if err != nil || supplied != strings.TrimSpace(supplied) || !strings.EqualFold(typ, kind) {
			return "", serviceDenied("invalid_stream_analytics_reference_id")
		}
		return id, nil
	}
	name, err := kustoName(value)
	if err != nil {
		return "", serviceDenied("invalid_stream_analytics_reference_name")
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	r := &Runtime{bundle: metadata.bundle}
	definition, ok := r.productDefinition(kind)
	if !ok || definition.Discovery.Parent != nil {
		return "", serviceDenied("unknown_stream_analytics_reference_type")
	}
	bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(bound.URL)
	values, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	match := ""
	for _, value := range values {
		raw := object(value)
		id, typ, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || !validResponseType(kind, text(raw["type"])) || seen[id] || !strings.EqualFold(text(raw["name"]), last(id)) {
			return "", serviceDenied("invalid_stream_analytics_reference_index")
		}
		seen[id] = true
		if strings.EqualFold(name, last(id)) {
			if match != "" {
				return "", serviceDenied("ambiguous_stream_analytics_reference_name")
			}
			match = id
		}
	}
	// An absent match can be a deleted data store or one in another subscription.
	// Neither justifies inventing an ARM identity inside the current subscription.
	return match, nil
}

func (c *client) streamAnalyticsReferences(ctx context.Context, kind string, raw map[string]any) ([]string, error) {
	if !isStreamAnalyticsType(kind) {
		return nil, nil
	}
	props := object(raw["properties"])
	refs := []string{}
	add := func(id string) {
		if id != "" {
			refs = append(refs, id)
		}
	}
	named := func(kind string, name any) (string, error) {
		id, err := c.streamAnalyticsNamedResource(ctx, kind, name)
		if err == nil {
			add(id)
		}
		return id, err
	}
	child := func(parent, collection string, name any) (string, error) {
		if parent == "" || name == nil {
			return "", nil
		}
		id, err := cognitiveNameID(parent, collection, text(name))
		if err == nil {
			add(id)
		}
		return id, err
	}
	if kind == streamAnalyticsJobType {
		if account := props["jobStorageAccount"]; account != nil {
			if _, err := named(storageType, object(account)["accountName"]); err != nil {
				return nil, err
			}
		}
		return refs, nil
	}
	if kind != streamAnalyticsInputType && kind != streamAnalyticsOutputType {
		return nil, nil
	}
	source := object(props["datasource"])
	settings := object(source["properties"])
	switch source["type"] {
	case "Microsoft.Storage/Blob":
		accounts, ok := settings["storageAccounts"].([]any)
		if !ok {
			return nil, serviceDenied("invalid_stream_analytics_storage_accounts")
		}
		for _, account := range accounts {
			id, err := named(storageType, object(account)["accountName"])
			if err != nil {
				return nil, err
			}
			if _, err := child(id, "blobServices/default/containers", settings["container"]); err != nil {
				return nil, err
			}
		}
	case "Microsoft.Storage/Table":
		if _, err := named(storageType, settings["accountName"]); err != nil {
			return nil, err
		}
	case "Microsoft.ServiceBus/EventHub", "Microsoft.EventHub/EventHub":
		id, err := named(eventHubNamespaceType, settings["serviceBusNamespace"])
		if err != nil {
			return nil, err
		}
		id, err = child(id, "eventhubs", settings["eventHubName"])
		if err != nil {
			return nil, err
		}
		if _, err := child(id, "consumergroups", settings["consumerGroupName"]); err != nil {
			return nil, err
		}
	case "Microsoft.ServiceBus/Queue", "Microsoft.ServiceBus/Topic":
		id, err := named(serviceBusNamespaceType, settings["serviceBusNamespace"])
		if err != nil {
			return nil, err
		}
		collection, key := "queues", "queueName"
		if source["type"] == "Microsoft.ServiceBus/Topic" {
			collection, key = "topics", "topicName"
		}
		if _, err := child(id, collection, settings[key]); err != nil {
			return nil, err
		}
	case "Microsoft.Sql/Server/Database", "Microsoft.Sql/Server/DataWarehouse":
		id, err := named(sqlServerType, settings["server"])
		if err != nil {
			return nil, err
		}
		if _, err := child(id, "databases", settings["database"]); err != nil {
			return nil, err
		}
	case "Microsoft.Storage/DocumentDB":
		if _, err := named(cosmosType, settings["accountId"]); err != nil {
			return nil, err
		}
	case "Microsoft.AzureFunction":
		id, err := named(appSiteType, settings["functionAppName"])
		if err != nil {
			return nil, err
		}
		if _, err := child(id, "functions", settings["functionName"]); err != nil {
			return nil, err
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}
