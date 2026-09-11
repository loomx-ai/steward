package azure

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	fleetType           = "Microsoft.ContainerService/fleets"
	fleetMemberType     = fleetType + "/members"
	fleetNamespaceType  = fleetType + "/managedNamespaces"
	fleetRunType        = fleetType + "/updateRuns"
	fleetStrategyType   = fleetType + "/updateStrategies"
	fleetProfileType    = fleetType + "/autoUpgradeProfiles"
	fleetGateType       = fleetType + "/gates"
	fleetVersion        = "2026-06-01"
	fleetArcClusterType = "Microsoft.Kubernetes/connectedClusters"
)

// Fleet enrolls existing AKS or Arc clusters. This reference never grants
// lifecycle ownership of either cluster or its Kubernetes extensions.
// https://learn.microsoft.com/azure/kubernetes-fleet/quickstart-create-fleet-and-members
func fleetClusterKind(kind string) string {
	for _, supported := range []string{aksType, fleetArcClusterType} {
		if strings.EqualFold(kind, supported) {
			return supported
		}
	}
	return ""
}

type fleetResource struct{ kind, prefix, selector string }

func fleetKind(kind string) fleetResource {
	for _, row := range []fleetResource{
		{fleetType, "Fleets", "fleetName"},
		{fleetMemberType, "FleetMembers", "fleetMemberName"},
		{fleetNamespaceType, "FleetManagedNamespaces", "managedNamespaceName"},
		{fleetRunType, "UpdateRuns", "updateRunName"},
		{fleetStrategyType, "FleetUpdateStrategies", "updateStrategyName"},
		{fleetProfileType, "AutoUpgradeProfiles", "autoUpgradeProfileName"},
		{fleetGateType, "Gates", "gateName"},
	} {
		if strings.EqualFold(row.kind, kind) {
			return row
		}
	}
	return fleetResource{}
}

var fleetNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func fleetName(name string, limit int) bool {
	return len(name) <= limit && fleetNamePattern.MatchString(name)
}

func fleetIdentity(wire string) (id, kind string, err error) {
	id, kind, err = parseID(wire)
	row := fleetKind(kind)
	parts := strings.Split(id, "/")
	if err != nil || row.kind == "" || wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "\t") || len(parts) != 9 && len(parts) != 11 || !fleetName(parts[8], 63) {
		return "", "", serviceDenied("invalid_fleet_identity")
	}
	if len(parts) == 11 {
		limit := 50
		if row.kind == fleetNamespaceType {
			limit = 63
		}
		if row.kind == fleetGateType {
			if !uuidPattern.MatchString(parts[10]) {
				return "", "", serviceDenied("invalid_fleet_gate_identity")
			}
		} else if !fleetName(parts[10], limit) {
			return "", "", serviceDenied("invalid_fleet_child_identity")
		}
	}
	return id, row.kind, nil
}

func fleetParent(id, kind string) string {
	if fleetKind(kind).kind != "" && kind != fleetType {
		return redisParentID(id)
	}
	return ""
}

// All routes come from the pinned native operation. Gates have no DELETE;
// stopping a run is the only POST needed by its deletion lifecycle.
func (c *client) fleetRequest(kind, scope, name, method string) (catalog.RESTRequest, error) {
	row := fleetKind(kind)
	if row.kind != kind || kind == "" || method != "GET" && method != "DELETE" && method != "POST" || method == "DELETE" && (name == "" || kind == fleetGateType) || method == "POST" && (name == "" || kind != fleetRunType) {
		return catalog.RESTRequest{}, serviceDenied("invalid_fleet_operation")
	}
	parameters := map[string]any{"subscriptionId": c.subscription}
	operation := "ListByFleet"
	if kind == fleetType && scope == c.root() && name == "" {
		operation = "ListBySubscription"
	} else {
		id, parentKind, err := parseID(scope)
		if err != nil || id != scope || !strings.HasPrefix(id, c.root()+"/") || kind == fleetType && !strings.EqualFold(parentKind, groupType) || kind != fleetType && (fleetKind(parentKind).kind != fleetType || !fleetName(last(id), 63)) {
			return catalog.RESTRequest{}, serviceDenied("invalid_fleet_operation_scope")
		}
		parameters["resourceGroupName"] = strings.Split(id, "/")[4]
		if kind == fleetType {
			operation = "ListByResourceGroup"
		} else {
			parameters["fleetName"] = last(id)
		}
		if name != "" {
			resourceID := scope + "/" + last(kind) + "/" + name
			if kind == fleetType {
				resourceID = scope + "/providers/Microsoft.ContainerService/fleets/" + name
			}
			if _, typ, err := fleetIdentity(resourceID); err != nil || typ != kind || name != strings.ToLower(name) {
				return catalog.RESTRequest{}, serviceDenied("invalid_fleet_operation_name")
			}
			parameters[row.selector], operation = name, "Get"
			if method == "DELETE" {
				operation = "Delete"
			} else if method == "POST" {
				operation = "Stop"
			}
		}
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.ContainerService." + row.prefix + "_" + operation)
	if !ok || op.Call == nil || op.Call.Version != fleetVersion || op.Call.Method != method {
		return catalog.RESTRequest{}, serviceDenied("fleet_native_operation_changed")
	}
	return bindAzureREST(op, parameters)
}

func fleetListQuery(u *url.URL) error {
	path := strings.ToLower(u.Path)
	const provider = "/providers/microsoft.containerservice/"
	index := strings.LastIndex(path, provider)
	if armPathProvider(u.Path) != "microsoft.containerservice" || index < 0 || strings.Split(path[index+len(provider):], "/")[0] != "fleets" {
		return nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != fleetVersion || u.RawPath != "" && u.RawPath != u.Path {
		return serviceDenied("invalid_fleet_list_query")
	}
	for key, values := range query {
		if key != "api-version" && key != "$skiptoken" || len(values) != 1 || values[0] == "" {
			return serviceDenied("filtered_fleet_list_query")
		}
	}
	return nil
}

// Keep every authored field, including arbitrary namespace annotations and
// placement policies, in the private digest. Only native progress is excluded.
func fleetSnapshot(kind string, raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	id, _, _ := fleetIdentity(text(raw["id"]))
	copy["id"], copy["name"], copy["type"] = id, last(id), kind
	delete(copy, "eTag")
	system := maps.Clone(object(raw["systemData"]))
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(system, key)
	}
	if raw["systemData"] != nil {
		copy["systemData"] = system
	}
	props := maps.Clone(object(raw["properties"]))
	delete(props, "provisioningState")
	if kind != fleetGateType {
		delete(props, "status")
	} else {
		// Approval/skip progress does not change the run's ownership of this
		// gate. Keep its target and subtype configuration in the digest.
		delete(props, "state")
	}
	if kind == fleetProfileType {
		delete(props, "autoUpgradeProfileStatus")
	}
	copy["properties"] = props
	return copy
}

func fleetValidate(kind string, raw map[string]any) error {
	if monitorRuleFields(raw, "id", "name", "type", "properties", "location", "tags", "identity", "eTag", "systemData") != nil {
		return serviceDenied("ambiguous_fleet_resource")
	}
	id, typ, err := fleetIdentity(text(raw["id"]))
	if err != nil || typ != kind || raw["id"] != text(raw["id"]) || !strings.EqualFold(text(raw["type"]), kind) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("fleet_response_identity_changed")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || props == nil || monitorRuleFields(props, "provisioningState", "hubProfile", "clusterResourceId", "group", "labels", "managedNamespaceProperties", "adoptionPolicy", "deletePolicy", "propagationPolicy", "strategy", "managedClusterUpdate", "updateStrategyId", "autoUpgradeProfileId", "channel", "disabled", "autoUpgradeProfileStatus", "status", "gateType", "target", "state") != nil {
		return serviceDenied("invalid_fleet_properties")
	}
	if kind == fleetType || kind == fleetNamespaceType {
		if text(raw["location"]) == "" {
			return serviceDenied("fleet_location_missing")
		}
	}
	if value, present := raw["tags"]; present {
		tags, ok := value.(map[string]any)
		if !ok {
			return serviceDenied("invalid_fleet_tags")
		}
		for _, value := range tags {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_fleet_tag")
			}
		}
	}
	for _, field := range []string{"hubProfile", "managedNamespaceProperties", "strategy", "managedClusterUpdate", "autoUpgradeProfileStatus", "status", "target"} {
		if value, present := props[field]; present {
			if _, ok := value.(map[string]any); !ok {
				return serviceDenied("invalid_fleet_configuration_object")
			}
		}
	}
	switch kind {
	case fleetType:
		if _, _, err := rbacNativePrincipal(kind, raw); err != nil {
			return err
		}
		identity := object(raw["identity"])
		if monitorRuleFields(identity, "userAssignedIdentities") != nil {
			return serviceDenied("ambiguous_fleet_identity")
		}
		if value, present := identity["userAssignedIdentities"]; present {
			identities, ok := value.(map[string]any)
			if !ok {
				return serviceDenied("invalid_fleet_user_identities")
			}
			for _, value := range identities {
				if _, ok := value.(map[string]any); !ok {
					return serviceDenied("invalid_fleet_user_identity")
				}
			}
		}
	case fleetMemberType:
		if props["clusterResourceId"] == nil {
			return serviceDenied("fleet_member_cluster_missing")
		}
	case fleetNamespaceType:
		if props["deletePolicy"] != text(props["deletePolicy"]) || props["adoptionPolicy"] != text(props["adoptionPolicy"]) || !slices.Contains([]string{"Keep", "Delete"}, text(props["deletePolicy"])) || !slices.Contains([]string{"Never", "IfIdentical", "Always"}, text(props["adoptionPolicy"])) {
			return serviceDenied("invalid_fleet_namespace_policy")
		}
		if _, _, err := fleetNamespaceMembers(raw); err != nil {
			return err
		}
	case fleetStrategyType:
		if object(props["strategy"]) == nil {
			return serviceDenied("fleet_strategy_missing")
		}
	case fleetRunType:
		if object(props["managedClusterUpdate"]) == nil {
			return serviceDenied("fleet_update_missing")
		}
	case fleetProfileType:
		if !slices.Contains([]string{"Stable", "Rapid", "NodeImage", "TargetKubernetesVersion"}, text(props["channel"])) {
			return serviceDenied("unknown_fleet_upgrade_channel")
		}
		if value, present := props["disabled"]; present {
			if _, ok := value.(bool); !ok {
				return serviceDenied("invalid_fleet_upgrade_disabled")
			}
		}
	case fleetGateType:
		// The native gateType enum is modelAsString. Microsoft recordings
		// already return ScheduledStart. Gates are read-only here: retain
		// opaque subtype configuration without completing or changing gates.
		if text(props["gateType"]) == "" || props["gateType"] != text(props["gateType"]) || !slices.Contains([]string{"Pending", "Skipped", "Completed"}, text(props["state"])) || object(props["target"])["id"] == nil {
			return serviceDenied("invalid_fleet_gate")
		}
	}
	_, err = fleetReferences(kind, raw)
	return err
}

// An omitted propagation policy deploys only to the hub. Within an explicit
// placement policy, omitted selectors mean all joined members. Dynamic
// selectors must be resolved using native placement/member observations;
// returning an empty fixed set would silently hide affected clusters.
func fleetNamespaceMembers(raw map[string]any) (names []string, dynamic bool, err error) {
	value, present := object(raw["properties"])["propagationPolicy"]
	if !present {
		return nil, false, nil
	}
	propagation, ok := value.(map[string]any)
	if !ok || propagation["type"] != "Placement" || monitorRuleFields(propagation, "type", "placementProfile") != nil {
		return nil, false, serviceDenied("invalid_fleet_propagation_policy")
	}
	policy := propagation
	for _, field := range []string{"placementProfile", "defaultClusterResourcePlacement", "policy"} {
		if monitorRuleFields(policy, field) != nil {
			return nil, false, serviceDenied("ambiguous_fleet_placement_policy")
		}
		value, present := policy[field]
		if !present {
			return nil, true, nil
		}
		var ok bool
		policy, ok = value.(map[string]any)
		if !ok {
			return nil, false, serviceDenied("invalid_fleet_placement_policy")
		}
	}
	if monitorRuleFields(policy, "placementType", "clusterNames", "affinity", "tolerations") != nil {
		return nil, false, serviceDenied("ambiguous_fleet_placement_policy")
	}
	typ := text(policy["placementType"])
	if value, present := policy["placementType"]; present && (typ == "" || value != typ) {
		return nil, false, serviceDenied("invalid_fleet_placement_type")
	}
	if typ == "" || typ == "PickAll" || typ == "PickN" {
		if _, present := policy["clusterNames"]; present {
			return nil, false, serviceDenied("ambiguous_fleet_placement_members")
		}
		return nil, true, nil
	}
	values, ok := policy["clusterNames"].([]any)
	if typ != "PickFixed" || !ok || policy["affinity"] != nil {
		return nil, false, serviceDenied("invalid_fleet_fixed_members")
	}
	seen := map[string]bool{}
	for _, value := range values {
		name, ok := value.(string)
		if !ok || !fleetName(name, 50) || seen[name] {
			return nil, false, serviceDenied("invalid_fleet_fixed_member")
		}
		seen[name], names = true, append(names, name)
	}
	slices.Sort(names)
	return names, false, nil
}

func fleetReferences(kind string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	id, _, err := fleetIdentity(text(raw["id"]))
	if err != nil {
		return nil, err
	}
	add := func(value any, typ string, sameFleet bool) error {
		if value == nil {
			return nil
		}
		wire, ok := value.(string)
		target, actual, err := parseID(wire)
		if !ok || wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "\t") || err != nil || !strings.EqualFold(actual, typ) || sameFleet && redisParentID(target) != fleetParent(id, kind) {
			return serviceDenied("invalid_fleet_reference")
		}
		if fleetKind(typ).kind != "" {
			if _, _, err := fleetIdentity(wire); err != nil {
				return err
			}
		}
		if !slices.Contains(refs[typ], target) {
			refs[typ] = append(refs[typ], target)
		}
		return nil
	}
	props := object(raw["properties"])
	switch kind {
	case fleetType:
		hub := object(props["hubProfile"])
		if monitorRuleFields(hub, "agentProfile", "apiServerAccessProfile") != nil {
			return nil, serviceDenied("ambiguous_fleet_hub_profile")
		}
		for _, field := range []string{"agentProfile", "apiServerAccessProfile"} {
			if value, present := hub[field]; present {
				profile, ok := value.(map[string]any)
				if !ok || monitorRuleFields(profile, "subnetId") != nil {
					return nil, serviceDenied("invalid_fleet_hub_network")
				}
				if err := add(profile["subnetId"], subnetType, false); err != nil {
					return nil, err
				}
			}
		}
		for wire := range object(object(raw["identity"])["userAssignedIdentities"]) {
			if err := add(wire, "Microsoft.ManagedIdentity/userAssignedIdentities", false); err != nil {
				return nil, err
			}
		}
	case fleetMemberType:
		target, actual, parseErr := parseID(text(props["clusterResourceId"]))
		clusterKind := fleetClusterKind(actual)
		if parseErr != nil || clusterKind == "" || len(strings.Split(target, "/")) != 9 {
			return nil, serviceDenied("invalid_fleet_member_cluster")
		}
		err = add(props["clusterResourceId"], clusterKind, false)
	case fleetRunType, fleetProfileType:
		err = add(props["updateStrategyId"], fleetStrategyType, true)
		if err == nil && kind == fleetRunType {
			err = add(props["autoUpgradeProfileId"], fleetProfileType, true)
		}
	case fleetGateType:
		target := object(props["target"])
		if monitorRuleFields(target, "id", "updateRunProperties") != nil {
			return nil, serviceDenied("ambiguous_fleet_gate_target")
		}
		if value, present := target["updateRunProperties"]; present {
			details, ok := value.(map[string]any)
			if !ok || monitorRuleFields(details, "name", "stage", "group", "timing") != nil || details["name"] != last(text(target["id"])) || details["timing"] != "Before" && details["timing"] != "After" {
				return nil, serviceDenied("invalid_fleet_gate_target_details")
			}
		}
		err = add(target["id"], fleetRunType, true)
	case fleetNamespaceType:
		names, _, memberErr := fleetNamespaceMembers(raw)
		err = memberErr
		for _, name := range names {
			if err == nil {
				err = add(fleetParent(id, kind)+"/members/"+name, fleetMemberType, true)
			}
		}
	}
	for _, values := range refs {
		slices.Sort(values)
	}
	return refs, err
}

func (c *client) fleetRead(ctx context.Context, kind, id string) (response, error) {
	canonical, typ, err := fleetIdentity(id)
	if err != nil || canonical != id || typ != kind {
		return response{}, serviceDenied("invalid_fleet_read_identity")
	}
	scope := fleetParent(id, kind)
	if kind == fleetType {
		scope = strings.Join(strings.Split(id, "/")[:5], "/")
	}
	request, err := c.fleetRequest(kind, scope, last(id), "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return result, err
	}
	if result.status != 200 || result.data["code"] != nil || result.data["error"] != nil || operationLocation(result.header) != "" || result.data["nextLink"] != nil || result.data["NextLink"] != nil || !strings.EqualFold(text(result.data["id"]), id) {
		return result, serviceDenied("invalid_fleet_get_response")
	}
	return result, fleetValidate(kind, result.data)
}

func (c *client) fleetIndex(ctx context.Context, kind, scope string) (items map[string]map[string]any, requestID string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	request, err := c.fleetRequest(kind, scope, "", "GET")
	if err != nil {
		return nil, "", err
	}
	initial, _ := url.Parse(request.URL)
	seen := map[string]bool{}
	items = map[string]map[string]any{}
	for endpoint := request.URL; endpoint != ""; {
		if seen[endpoint] {
			return nil, "", serviceDenied("repeated_fleet_page")
		}
		seen[endpoint] = true
		values, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
		if err != nil {
			return nil, "", err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil || monitorRuleFields(result.data, "value", "nextLink") != nil {
			return nil, "", serviceDenied("invalid_fleet_list_response")
		}
		for _, value := range values {
			raw := object(value)
			id, typ, err := fleetIdentity(text(raw["id"]))
			if err != nil || typ != kind || !strings.HasPrefix(id, c.root()+"/") || items[id] != nil || fleetValidate(kind, raw) != nil || kind != fleetType && fleetParent(id, kind) != scope || kind == fleetType && scope != c.root() && !inResourceGroup(id, scope) {
				return nil, "", serviceDenied("invalid_fleet_list_identity")
			}
			current, err := c.fleetRead(ctx, kind, id)
			if err != nil {
				return nil, "", err
			}
			if !nativeConfigurationContains(fleetSnapshot(kind, raw), fleetSnapshot(kind, current.data)) {
				return nil, "", serviceDenied("fleet_list_configuration_changed")
			}
			items[id] = current.data
		}
		if result.requestID != "" {
			requestID = result.requestID
		}
		endpoint = next
	}
	return items, requestID, nil
}
