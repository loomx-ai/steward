package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const azureLocalAKSType = "Microsoft.HybridContainerService/provisionedClusterInstances"
const azureLocalAKSSuffix = "/providers/microsoft.hybridcontainerservice/provisionedclusterinstances/default"

func (c *client) azureLocalAKSIdentity(wire, kind string) (string, error) {
	id, typ, err := parseID(wire)
	if err != nil || wire != strings.TrimSpace(wire) || !strings.HasPrefix(id, c.root()+"/resourcegroups/") || !strings.EqualFold(typ, kind) {
		return "", serviceDenied("invalid_azure_local_aks_identity")
	}
	parent := id
	if kind == azureLocalAKSType {
		if !strings.HasSuffix(id, azureLocalAKSSuffix) {
			return "", serviceDenied("invalid_azure_local_aks_singleton")
		}
		parent = strings.TrimSuffix(id, azureLocalAKSSuffix)
	} else if kind != fleetArcClusterType {
		return "", serviceDenied("invalid_azure_local_aks_kind")
	}
	_, typ, err = parseID(parent)
	if err != nil || typ != strings.ToLower(fleetArcClusterType) || len(strings.Split(parent, "/")) != 9 {
		return "", serviceDenied("invalid_azure_local_aks_parent")
	}
	return id, nil
}

func (c *client) azureLocalAKSRequest(kind, id string, list bool) (catalog.RESTRequest, error) {
	operation := "Azure.Microsoft.Kubernetes.ConnectedCluster_Get"
	parameters := map[string]any{"subscriptionId": c.subscription}
	if kind == fleetArcClusterType && list {
		operation = "Azure.Microsoft.Kubernetes.ConnectedCluster_ListBySubscription"
		if id != "" {
			return catalog.RESTRequest{}, serviceDenied("invalid_azure_local_aks_list_scope")
		}
	} else {
		canonical, err := c.azureLocalAKSIdentity(id, kind)
		if err != nil || canonical != id {
			return catalog.RESTRequest{}, serviceDenied("invalid_azure_local_aks_read_scope")
		}
		if kind == azureLocalAKSType {
			operation = "Azure.Microsoft.HybridContainerService.provisionedClusterInstances_Get"
			if list {
				operation = "Azure.Microsoft.HybridContainerService.provisionedClusterInstances_List"
			}
			parameters = map[string]any{"connectedClusterResourceUri": strings.TrimPrefix(strings.TrimSuffix(id, azureLocalAKSSuffix), "/")}
		} else {
			parameters["resourceGroupName"], parameters["clusterName"] = strings.Split(id, "/")[4], last(id)
		}
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	op, ok := metadata.catalog.Operation(operation)
	if !ok || op.Call == nil || op.Call.Version != azureLocalVersion || op.Call.Method != "GET" {
		return catalog.RESTRequest{}, serviceDenied("azure_local_aks_contract_changed")
	}
	return bindAzureREST(op, parameters)
}

func (c *client) azureLocalAKSRead(ctx context.Context, id, kind string) (response, error) {
	request, err := c.azureLocalAKSRequest(kind, id, false)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return res, err
	}
	actual, e := c.azureLocalAKSIdentity(text(res.data["id"]), kind)
	name := text(res.data["name"])
	// The original 2024 GET returns the connected-cluster name for its default
	// provisioned instance. The singleton ID and native type remain authoritative.
	validName := strings.EqualFold(name, last(id)) || kind == azureLocalAKSType && strings.EqualFold(name, last(strings.TrimSuffix(id, azureLocalAKSSuffix)))
	if res.status != 200 || operationLocation(res.header) != "" || e != nil || actual != id || !strings.EqualFold(text(res.data["type"]), kind) || !validName || object(res.data["properties"]) == nil {
		return res, serviceDenied("invalid_azure_local_aks_response")
	}
	if kind == fleetArcClusterType && text(res.data["location"]) == "" {
		return res, serviceDenied("azure_local_aks_parent_location_missing")
	}
	if kind == azureLocalAKSType {
		if _, err := azureLocalAKSReferences(res.data); err != nil {
			return res, err
		}
	}
	return res, nil
}

func azureLocalAKSReferences(raw map[string]any) (map[string][]string, error) {
	refs, err := azureLocalReferences(text(raw["id"]), azureLocalAKSType, raw)
	if err != nil {
		return nil, err
	}
	props := object(raw["properties"])
	for _, field := range []string{"cloudProviderProfile", "infraNetworkProfile"} {
		value := props[field]
		if value == nil {
			return refs, nil
		}
		props = object(value)
		if props == nil {
			return nil, serviceDenied("invalid_azure_local_aks_network_profile")
		}
	}
	if props["vnetSubnetIds"] == nil {
		return refs, nil
	}
	ids, ok := props["vnetSubnetIds"].([]any)
	if !ok {
		return nil, serviceDenied("invalid_azure_local_aks_networks")
	}
	seen := map[string]bool{}
	for _, value := range ids {
		wire, ok := value.(string)
		id, kind, e := parseID(wire)
		if !ok || e != nil || wire != strings.TrimSpace(wire) || kind != strings.ToLower(azureLocalNetworkType) || len(strings.Split(id, "/")) != 9 || seen[id] {
			return nil, serviceDenied("invalid_azure_local_aks_network_reference")
		}
		seen[id] = true
		refs[azureLocalNetworkType] = append(refs[azureLocalNetworkType], id)
	}
	slices.Sort(refs[azureLocalNetworkType])
	return refs, nil
}

// Every connected cluster is probed for the native default instance, including
// clusters whose instance index is missing. Known instance IDs are reread even
// after their Arc parent disappears; parent/index absence cannot hide a consumer.
func (c *client) azureLocalAKSResources(ctx context.Context, known []string) (map[string]map[string]any, error) {
	instances := map[string]bool{}
	for _, id := range known {
		canonical, e := c.azureLocalAKSIdentity(id, azureLocalAKSType)
		if e != nil || canonical != id || instances[id] {
			return nil, serviceDenied("invalid_azure_local_aks_history")
		}
		instances[id] = true
	}
	request, err := c.azureLocalAKSRequest(fleetArcClusterType, "", true)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(request.URL)
	rows, err := c.unfilteredARMIndex(ctx, u.Path, azureLocalVersion)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		raw := object(row)
		parent, e := c.azureLocalAKSIdentity(text(raw["id"]), fleetArcClusterType)
		if e != nil || seen[parent] || !strings.EqualFold(text(raw["type"]), fleetArcClusterType) {
			return nil, serviceDenied("invalid_azure_local_aks_parent_index")
		}
		seen[parent] = true
		live, e := c.azureLocalAKSRead(ctx, parent, fleetArcClusterType)
		if e != nil {
			return nil, e
		}
		if serviceListedIncarnation(raw, live.data) != nil || !nativeConfigurationContains(raw, live.data) {
			return nil, serviceDenied("azure_local_aks_parent_index_changed")
		}
		instances[parent+azureLocalAKSSuffix] = true
	}
	resources := map[string]map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(instances)) {
		req, e := c.azureLocalAKSRequest(azureLocalAKSType, id, true)
		if e != nil {
			return nil, e
		}
		u, _ := url.Parse(req.URL)
		rows, e := c.unfilteredARMIndex(ctx, u.Path, azureLocalVersion)
		if e != nil && !isNotFound(e) {
			return nil, e
		}
		if len(rows) > 1 {
			return nil, serviceDenied("ambiguous_azure_local_aks_singleton")
		}
		var listed map[string]any
		if len(rows) == 1 {
			listed = object(rows[0])
			member, e := c.azureLocalAKSIdentity(text(listed["id"]), azureLocalAKSType)
			if e != nil || member != id || !strings.EqualFold(text(listed["type"]), azureLocalAKSType) {
				return nil, serviceDenied("invalid_azure_local_aks_index")
			}
		}
		live, e := c.azureLocalAKSRead(ctx, id, azureLocalAKSType)
		if isNotFound(e) && listed == nil {
			continue
		}
		if e != nil {
			return nil, e
		}
		if listed != nil && (serviceListedIncarnation(listed, live.data) != nil || !nativeConfigurationContains(listed, live.data)) {
			return nil, serviceDenied("azure_local_aks_index_changed")
		}
		resources[id] = live.data
	}
	return resources, nil
}
