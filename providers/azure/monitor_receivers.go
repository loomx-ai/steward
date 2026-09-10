package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorReceiverName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\%?#\x00\r\n\t")
}

// ITSM's published Create example uses subscription|customerId. A standalone
// customerId has no subscription qualifier. Neither form is an ARM resource ID.
func monitorITSMWorkspace(value string) (subscription, customer string, err error) {
	parts := strings.Split(strings.ToLower(value), "|")
	if len(parts) != 1 && len(parts) != 2 {
		return "", "", serviceDenied("invalid_monitor_itsm_workspace")
	}
	for _, part := range parts {
		if !uuidPattern.MatchString(part) {
			return "", "", serviceDenied("invalid_monitor_itsm_workspace")
		}
	}
	if len(parts) == 2 {
		subscription = parts[0]
	}
	return subscription, parts[len(parts)-1], nil
}

// Unmatched native selectors remain explicit unresolved references. They do
// not acquire a fabricated resource-group path or authorize a foreign read.
func monitorReceiverSelector(kind, value string) bool {
	if value != strings.ToLower(value) {
		return false
	}
	if strings.EqualFold(kind, insightsWorkspaceType) && strings.HasPrefix(value, "workspace-id:") {
		_, _, err := monitorITSMWorkspace(strings.TrimPrefix(value, "workspace-id:"))
		return err == nil
	}
	prefix, count := "eventhub:", 3
	if strings.EqualFold(kind, eventHubNamespaceType) {
		prefix, count = "eventhub-namespace:", 2
	} else if !strings.EqualFold(kind, eventHubType) {
		return false
	}
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), "/")
	if len(parts) != count {
		return false
	}
	scope := strings.Split(parts[0], "@")
	if len(scope) != 1 && len(scope) != 2 {
		return false
	}
	for _, value := range scope {
		if !uuidPattern.MatchString(value) {
			return false
		}
	}
	for _, name := range parts[1:] {
		if !monitorReceiverName(name) {
			return false
		}
	}
	return true
}

func monitorReferenceProjection(refs map[string][]string) map[string]any {
	values := map[string]any{}
	for kind, ids := range refs {
		ids = slices.Clone(ids)
		slices.Sort(ids)
		values[kind] = ids
	}
	return values
}

func (c *client) monitorReferencesUnchanged(value asset.Asset, refs map[string][]string) error {
	recorded, err := c.monitorRecordedReferences(value)
	if err != nil {
		return err
	}
	if c.privateConfiguration(recorded) != c.privateConfiguration(monitorReferenceProjection(refs)) {
		return serviceDenied("monitor_receiver_resolution_changed")
	}
	return nil
}

func monitorReceiverIndexIdentity(raw map[string]any, id, kind string, requireCustomer bool) (map[string]any, error) {
	if err := monitorRuleFields(raw, "id", "type", "name", "location", "properties", "systemData"); err != nil {
		return nil, err
	}
	actual, typ, err := parseID(text(raw["id"]))
	if err != nil || actual != id || !strings.EqualFold(typ, kind) || !validResponseType(kind, text(raw["type"])) || raw["type"] != nil && text(raw["type"]) == "" || !strings.EqualFold(text(raw["name"]), last(id)) || text(raw["location"]) == "" {
		return nil, serviceDenied("invalid_monitor_receiver_index_identity")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || monitorRuleFields(props, "customerId", "createdAt") != nil {
		return nil, serviceDenied("invalid_monitor_receiver_index_properties")
	}
	value := map[string]any{"id": id, "name": last(id), "location": resourceRegion(raw), "createdAt": props["createdAt"], "systemCreatedAt": object(raw["systemData"])["createdAt"]}
	if kind == insightsWorkspaceType {
		customer := text(props["customerId"])
		if (requireCustomer || props["customerId"] != nil) && !uuidPattern.MatchString(customer) {
			return nil, serviceDenied("invalid_monitor_receiver_workspace_identity")
		}
		value["customerId"] = strings.ToLower(customer)
	}
	return value, nil
}

// The selected namespace/workspace APIs return complete subscription indexes.
// Reconcile each row with GET and repeat the full identity index. Only fields
// needed to identify the receiver participate; target configuration is checked
// by the target's own cleanup driver, not inferred from this shared reference.
func (c *client) monitorReceiverIndex(ctx context.Context, kind string) (values map[string]map[string]any, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if kind != eventHubNamespaceType && kind != insightsWorkspaceType {
		return nil, serviceDenied("invalid_monitor_receiver_index_kind")
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	definition, ok := (&Runtime{bundle: metadata.bundle}).productDefinition(kind)
	if !ok || definition.Discovery.List == nil || definition.Discovery.Parent != nil {
		return nil, serviceDenied("monitor_receiver_index_unavailable")
	}
	bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
	if err != nil {
		return nil, err
	}
	initial, _ := url.Parse(bound.URL)
	read := func() (map[string]map[string]any, error) {
		values := map[string]map[string]any{}
		seen := map[string]bool{}
		for endpoint := bound.URL; endpoint != ""; {
			u, err := url.Parse(endpoint)
			if err != nil || seen[endpoint] || strings.Contains(endpoint, "#") {
				return nil, serviceDenied("invalid_monitor_receiver_index_page")
			}
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil || !slices.Equal(query["api-version"], initial.Query()["api-version"]) || len(query["skiptoken"])+len(query["$skiptoken"]) > 1 {
				return nil, serviceDenied("invalid_monitor_receiver_index_query")
			}
			for key, entries := range query {
				if key != "api-version" && key != "skiptoken" && key != "$skiptoken" || len(entries) != 1 || entries[0] == "" {
					return nil, serviceDenied("filtered_monitor_receiver_index")
				}
			}
			seen[endpoint] = true
			rows, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
			if err != nil {
				return nil, err
			}
			if result.data["code"] != nil || operationLocation(result.header) != "" || monitorRuleFields(result.data, "value", "nextLink") != nil {
				return nil, serviceDenied("invalid_monitor_receiver_index_response")
			}
			for _, row := range rows {
				raw := object(row)
				id, typ, err := parseID(text(raw["id"]))
				if err != nil || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || values[id] != nil {
					return nil, serviceDenied("invalid_monitor_receiver_index_resource")
				}
				listed, err := monitorReceiverIndexIdentity(raw, id, kind, false)
				if err != nil {
					return nil, err
				}
				mapping, _ := findType(kind)
				endpoint, err := c.resourceURL(mapping, id)
				if err != nil {
					return nil, err
				}
				current, err := c.request(ctx, "GET", endpoint)
				if err != nil {
					return nil, err
				}
				live, identityErr := monitorReceiverIndexIdentity(current.data, id, kind, true)
				if !insightsARMReadValid(current, id, kind) || current.data["nextLink"] != nil || monitorRuleFields(current.data, "nextLink") != nil || identityErr != nil || serviceListedIncarnation(raw, current.data) != nil || listed["location"] != live["location"] || listed["customerId"] != nil && listed["customerId"] != "" && listed["customerId"] != live["customerId"] {
					return nil, serviceDenied("monitor_receiver_index_disagrees")
				}
				values[id] = live
			}
			endpoint = next
		}
		return values, nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(map[string]any{"index": first}) != c.privateConfiguration(map[string]any{"index": second}) {
		return nil, serviceDenied("monitor_receiver_index_changed")
	}
	return second, nil
}

func (c *client) monitorReferences(ctx context.Context, kind, id string, raw map[string]any) (refs map[string][]string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	refs, err = monitorResourceReferences(kind, id, raw)
	if err != nil || kind != monitorActionGroupType {
		return refs, err
	}
	props := object(raw["properties"])
	if err := monitorRuleFields(props, "eventHubReceivers", "itsmReceivers"); err != nil {
		return nil, err
	}
	for _, field := range []string{"eventHubReceivers", "itsmReceivers"} {
		if value, present := props[field]; present {
			rows, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_monitor_receiver_collection")
			}
			for _, value := range rows {
				if _, ok := value.(map[string]any); !ok {
					return nil, serviceDenied("invalid_monitor_receiver_entry")
				}
			}
		}
	}
	indexes := map[string]map[string]map[string]any{}
	index := func(kind string) (map[string]map[string]any, error) {
		if rows, ok := indexes[kind]; ok {
			return rows, nil
		}
		rows, err := c.monitorReceiverIndex(ctx, kind)
		if err == nil {
			indexes[kind] = rows
		}
		return rows, err
	}
	for _, value := range array(props["eventHubReceivers"]) {
		row := object(value)
		if err := monitorRuleFields(row, "subscriptionId", "tenantId", "eventHubNameSpace", "eventHubName"); err != nil {
			return nil, err
		}
		subscription, tenant := strings.ToLower(text(row["subscriptionId"])), strings.ToLower(text(row["tenantId"]))
		namespace, name := strings.ToLower(text(row["eventHubNameSpace"])), strings.ToLower(text(row["eventHubName"]))
		_, tenantPresent := row["tenantId"]
		if !uuidPattern.MatchString(subscription) || tenantPresent && !uuidPattern.MatchString(tenant) || !monitorReceiverName(namespace) || !monitorReceiverName(name) {
			return nil, serviceDenied("invalid_monitor_event_hub_receiver")
		}
		scope := subscription
		if tenant != "" {
			scope = tenant + "@" + subscription
		}
		found := ""
		if subscription == c.subscription && (tenant == "" || strings.EqualFold(tenant, c.tenant)) {
			rows, err := index(eventHubNamespaceType)
			if err != nil {
				return nil, err
			}
			for _, id := range slices.Sorted(maps.Keys(rows)) {
				if rows[id]["name"] == namespace {
					if found != "" {
						return nil, serviceDenied("ambiguous_monitor_event_hub_receiver")
					}
					found = id
				}
			}
		}
		if found == "" {
			addReference(refs, eventHubNamespaceType, "eventhub-namespace:"+scope+"/"+namespace)
			addReference(refs, eventHubType, "eventhub:"+scope+"/"+namespace+"/"+name)
		} else {
			addReference(refs, eventHubNamespaceType, found)
			addReference(refs, eventHubType, found+"/eventhubs/"+name)
		}
	}
	for _, value := range array(props["itsmReceivers"]) {
		row := object(value)
		if err := monitorRuleFields(row, "workspaceId", "region"); err != nil {
			return nil, err
		}
		selector, ok := row["workspaceId"].(string)
		subscription, customer, err := monitorITSMWorkspace(selector)
		region := strings.ToLower(text(row["region"]))
		if !ok || err != nil || region == "" || region != strings.TrimSpace(region) {
			return nil, serviceDenied("invalid_monitor_itsm_receiver")
		}
		found := ""
		if subscription == "" || subscription == c.subscription {
			rows, err := index(insightsWorkspaceType)
			if err != nil {
				return nil, err
			}
			for _, id := range slices.Sorted(maps.Keys(rows)) {
				if rows[id]["customerId"] == customer {
					if found != "" || rows[id]["location"] != region {
						return nil, serviceDenied("ambiguous_monitor_itsm_receiver")
					}
					found = id
				}
			}
		}
		if found == "" {
			found = "workspace-id:" + strings.ToLower(selector)
		}
		addReference(refs, insightsWorkspaceType, found)
	}
	return refs, nil
}
