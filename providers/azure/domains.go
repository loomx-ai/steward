package azure

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	domainType          = "Microsoft.DomainRegistration/domains"
	domainOwnershipType = domainType + "/domainOwnershipIdentifiers"
	domainConfiguration = "_domain_configuration"
	domainDependencies  = "_domain_dependencies"
	domainProof         = "_domain_binding"
)

type domainReadContextKey struct{}

func isDomainType(kind string) bool { return kind == domainType || kind == domainOwnershipType }

func domainPath(path string) bool {
	path = strings.ToLower(path)
	return armPathProvider(path) == "microsoft.domainregistration" && strings.Contains(path, "/providers/microsoft.domainregistration/domains")
}

// Contacts, transfer authorization, ownership tokens, consent and future fields
// are retained only in the keyed configuration, including in API diagnostics.
func domainSafeValue(value any) any {
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
			for _, key := range []string{"provisioningState", "registrationStatus", "createdTime", "expirationTime", "lastRenewedTime", "autoRenew", "privacy", "dnsType", "dnsZoneId", "targetDnsType", "readyForDnsRecordManagement"} {
				switch entry := props[key].(type) {
				case string, bool:
					public[key] = entry
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if entry, ok := value[key]; ok {
				result[key] = domainSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = domainSafeValue(entry)
		}
		return result
	default:
		return nil
	}
}

func domainSnapshot(kind string, raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var snapshot map[string]any
	json.Unmarshal(payload, &snapshot)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	delete(snapshot, "eTag")
	if kind == domainOwnershipType {
		delete(snapshot, "location") // Native proxy resources have no location.
	}
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	if len(object(snapshot["systemData"])) == 0 {
		delete(snapshot, "systemData")
	}
	for _, field := range []string{"provisioningState", "registrationStatus", "readyForDnsRecordManagement", "domainNotRenewableReasons", "managedHostNames"} {
		delete(object(snapshot["properties"]), field)
	}
	return snapshot
}

func domainMetadata(kind string, raw map[string]any) error {
	id, typ, err := parseID(text(raw["id"]))
	if err != nil || !strings.EqualFold(typ, kind) || !validResponseType(kind, text(raw["type"])) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("invalid_domain_identity")
	}
	props := object(raw["properties"])
	if kind == domainOwnershipType {
		if text(props["ownershipId"]) == "" {
			return serviceDenied("domain_ownership_identity_missing")
		}
		return nil
	}
	if resourceRegion(raw) != "global" || text(raw["location"]) == "" {
		return serviceDenied("invalid_domain_location")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(props["createdTime"])); err != nil {
		return serviceDenied("domain_creation_missing")
	}
	if !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "InProgress", "Deleting"}, text(props["provisioningState"])) {
		return serviceDenied("domain_provisioning_unknown")
	}
	if _, ok := props["managedHostNames"].([]any); !ok {
		return serviceDenied("domain_hostname_index_missing")
	}
	if zone := props["dnsZoneId"]; zone != nil && zone != "" {
		_, kind, err := parseID(text(zone))
		if err != nil || !strings.EqualFold(kind, publicDNSZoneType) {
			return serviceDenied("invalid_domain_dns_zone")
		}
	}
	return nil
}

func domainHostname(name, domain string) bool {
	name, domain = strings.ToLower(strings.TrimSuffix(name, ".")), strings.ToLower(domain)
	return name == domain || strings.HasSuffix(name, "."+domain)
}

// A hostname binding consumes the registered name; this is a deletion-order
// dependency, never ownership of its app or of a DNS zone.
func domainBindingReference(domainID string, raw map[string]any) (bool, error) {
	id, kind, err := parseID(text(raw["id"]))
	if err != nil || (!strings.EqualFold(kind, appBindingType) && !strings.EqualFold(kind, appSlotBindingType)) {
		return false, serviceDenied("invalid_domain_hostname_binding")
	}
	ref := object(raw["properties"])["domainId"]
	if ref != nil && ref != "" {
		registered, kind, err := parseID(text(ref))
		if err != nil || !strings.EqualFold(kind, domainType) {
			return false, serviceDenied("invalid_domain_registration_reference")
		}
		if registered == domainID && !domainHostname(last(id), last(domainID)) {
			return false, serviceDenied("domain_hostname_reference_disagrees")
		}
		if registered != domainID && domainHostname(last(id), last(domainID)) {
			return false, serviceDenied("domain_hostname_registration_disagrees")
		}
		return registered == domainID, nil
	}
	return domainHostname(last(id), last(domainID)), nil
}

func domainPrerequisite(parent, child asset.Asset) bool {
	if parent.Identity.NativeType == publicDNSZoneType && child.Identity.NativeType == domainType {
		return text(child.Normalized["_domain_dns_zone"]) == parent.Identity.NativeID
	}
	if parent.Identity.NativeType != domainType || !isAppBinding(child.Identity.NativeType) {
		return false
	}
	linked, err := domainBindingReference(parent.Identity.NativeID, map[string]any{"id": child.Identity.NativeID, "properties": child.Normalized})
	return err == nil && linked
}

func domainChildSnapshot(kind string, raw map[string]any) map[string]any {
	if isAppBinding(kind) {
		return appServiceSnapshot(kind, raw)
	}
	return domainSnapshot(kind, raw)
}

// Enumerate native ownership identifiers and web/slot bindings twice. Known
// names also receive their own GET so an omitted list row cannot hide a survivor.
func (c *client) domainChildren(ctx context.Context, parent asset.Identity, raw map[string]any, known map[string]any) ([]serviceChild, error) {
	ctx = context.WithValue(ctx, domainReadContextKey{}, true)
	observe := func() ([]serviceChild, string, error) {
		children, err := c.nativeServiceChildren(ctx, parent, raw, []string{domainOwnershipType})
		if err != nil {
			return nil, "", err
		}
		observed := map[string]any{}
		var walk func(string, string, map[string]any) error
		walk = func(id, kind string, site map[string]any) error {
			observed[id] = c.privateConfiguration(appServiceSnapshot(kind, site))
			kinds := []string{appBindingType, appSlotType}
			if kind == appSlotType {
				kinds = []string{appSlotBindingType}
			}
			entries, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: id, NativeType: kind}, site, kinds)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.kind == appSlotType {
					if err := walk(entry.id, entry.kind, entry.data); err != nil {
						return err
					}
					continue
				}
				observed[entry.id] = c.privateConfiguration(appServiceSnapshot(entry.kind, entry.data))
				linked, err := domainBindingReference(parent.NativeID, entry.data)
				if err != nil {
					return err
				}
				if linked {
					children = append(children, entry)
				}
			}
			return nil
		}
		sites, err := c.subscriptionReferenceIndex(ctx, appSiteType)
		if err != nil {
			return nil, "", err
		}
		for _, site := range sites {
			id, _, _ := parseID(text(site["id"]))
			kind, _ := findType(appSiteType)
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return nil, "", err
			}
			current, err := c.readResource(ctx, endpoint)
			if err != nil {
				return nil, "", err
			}
			if !insightsARMReadValid(current, id, appSiteType) || !nativeConfigurationContains(appServiceSnapshot(appSiteType, site), appServiceSnapshot(appSiteType, current.data)) {
				return nil, "", serviceDenied("domain_app_index_changed")
			}
			if err := walk(id, appSiteType, current.data); err != nil {
				return nil, "", err
			}
		}
		for id, entry := range known {
			if slices.ContainsFunc(children, func(child serviceChild) bool { return child.id == id }) {
				continue
			}
			kind, _ := findType(text(object(entry)["kind"]))
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return nil, "", err
			}
			current, err := c.readResource(ctx, endpoint)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return nil, "", err
			}
			if !insightsARMReadValid(current, id, kind.NativeType) {
				return nil, "", serviceDenied("domain_known_dependency_changed")
			}
			children = append(children, serviceChild{id: id, kind: kind.NativeType, data: current.data})
		}
		for i := range children {
			children[i].direct = true
			observed[children[i].id] = c.privateConfiguration(domainChildSnapshot(children[i].kind, children[i].data))
		}
		kind, _ := findType(domainType)
		endpoint, err := c.resourceURL(kind, parent.NativeID)
		if err != nil {
			return nil, "", err
		}
		latest, err := c.readResource(ctx, endpoint)
		if err != nil {
			return nil, "", err
		}
		if !insightsARMReadValid(latest, parent.NativeID, domainType) || c.privateConfiguration(domainSnapshot(domainType, raw)) != c.privateConfiguration(domainSnapshot(domainType, latest.data)) || c.privateConfiguration(map[string]any{"hostnames": object(raw["properties"])["managedHostNames"]}) != c.privateConfiguration(map[string]any{"hostnames": object(latest.data["properties"])["managedHostNames"]}) {
			return nil, "", serviceDenied("domain_configuration_changed_during_read")
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, c.privateConfiguration(observed), nil
	}
	first, before, err := observe()
	if err != nil {
		return nil, err
	}
	second, after, err := observe()
	if err != nil {
		return nil, err
	}
	if before != after || !slices.EqualFunc(first, second, func(a, b serviceChild) bool { return a.id == b.id && a.kind == b.kind }) {
		return nil, serviceDenied("domain_dependencies_changed_during_read")
	}
	return second, nil
}

func (c *client) domainBinding(id, kind string, normalized map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "configuration": normalized[domainConfiguration], "parent": normalized["_domain_parent_configuration"], "dependencies": normalized[domainDependencies], "dns_zone": normalized["_domain_dns_zone"]})
}

func (c *client) domainPlan(value asset.Asset) (map[string]any, error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok || !isDomainType(kind.NativeType) {
		return nil, serviceDenied("invalid_domain_review_kind")
	}
	_, parameters, err := c.resourceOperation(kind, value.Identity.NativeID, "GET")
	if err != nil || c.privateConfiguration(parameters) != c.privateConfiguration(object(value.Normalized["arm_parameters"])) {
		return nil, serviceDenied("domain_review_parameters_changed")
	}
	if text(value.Normalized[domainConfiguration]) == "" || text(value.Normalized[domainProof]) != c.domainBinding(value.Identity.NativeID, value.Identity.NativeType, value.Normalized) {
		return nil, serviceDenied("domain_review_changed")
	}
	dependencies, ok := value.Normalized[domainDependencies].(map[string]any)
	if !ok {
		return nil, serviceDenied("domain_review_dependencies_missing")
	}
	if value.Identity.NativeType == domainOwnershipType {
		if len(dependencies) != 0 || text(value.Normalized["_domain_parent_configuration"]) == "" {
			return nil, serviceDenied("domain_ownership_parent_missing")
		}
		return dependencies, nil
	}
	if zone := text(value.Normalized["_domain_dns_zone"]); zone != "" {
		id, kind, err := parseID(zone)
		if err != nil || id != zone || !strings.EqualFold(kind, publicDNSZoneType) {
			return nil, serviceDenied("invalid_domain_review_dns_zone")
		}
	}
	for id, dependency := range dependencies {
		entry := object(dependency)
		canonical, kind, err := parseID(id)
		if err != nil || id != canonical || !strings.EqualFold(kind, text(entry["kind"])) || text(entry["configuration"]) == "" || len(entry) != 2 || !strings.HasPrefix(id, c.root()+"/") {
			return nil, serviceDenied("invalid_domain_review_dependency")
		}
		if text(entry["kind"]) == domainOwnershipType && redisParentID(id) == value.Identity.NativeID {
			continue
		}
		if !isAppBinding(text(entry["kind"])) || !domainHostname(last(id), last(value.Identity.NativeID)) {
			return nil, serviceDenied("domain_dependency_scope_changed")
		}
	}
	return dependencies, nil
}

func (c *client) domainInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isDomainType(kind) {
		return nil
	}
	if err := domainMetadata(kind, raw); err != nil {
		return err
	}
	normalized[domainConfiguration] = c.privateConfiguration(domainSnapshot(kind, raw))
	dependencies := map[string]any{}
	if kind == domainType {
		zone, _, _ := parseID(text(object(raw["properties"])["dnsZoneId"]))
		normalized["_domain_dns_zone"] = zone
		children, err := c.domainChildren(ctx, asset.Identity{NativeID: id, NativeType: kind}, raw, nil)
		if err != nil {
			return err
		}
		for _, child := range children {
			dependencies[child.id] = map[string]any{"kind": child.kind, "configuration": c.privateConfiguration(domainChildSnapshot(child.kind, child.data))}
		}
	} else {
		parent, err := c.domainParent(ctx, id)
		if err != nil {
			return err
		}
		normalized["_domain_parent_configuration"] = c.privateConfiguration(domainSnapshot(domainType, parent))
	}
	normalized[domainDependencies] = dependencies
	normalized[domainProof] = c.domainBinding(id, kind, normalized)
	return nil
}

func (c *client) domainParent(ctx context.Context, childID string) (map[string]any, error) {
	id := redisParentID(childID)
	kind, _ := findType(domainType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	parent, err := c.readResource(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if !insightsARMReadValid(parent, id, domainType) {
		return nil, serviceDenied("invalid_domain_parent")
	}
	if err := domainMetadata(domainType, parent.data); err != nil {
		return nil, err
	}
	return parent.data, nil
}

func (c *client) domainIncarnation(value asset.Asset, raw map[string]any) error {
	if !isDomainType(value.Identity.NativeType) {
		return nil
	}
	if _, err := c.domainPlan(value); err != nil {
		return err
	}
	if err := domainMetadata(value.Identity.NativeType, raw); err != nil {
		return err
	}
	if text(value.Normalized[domainConfiguration]) != c.privateConfiguration(domainSnapshot(value.Identity.NativeType, raw)) {
		return serviceDenied("domain_configuration_changed")
	}
	return nil
}

func (c *client) domainZoneUnused(ctx context.Context, zoneID string) error {
	var before string
	for pass := 0; pass < 2; pass++ {
		domains, err := c.subscriptionReferenceIndex(ctx, domainType)
		if err != nil {
			return err
		}
		observed := map[string]any{}
		for _, listed := range domains {
			id, _, _ := parseID(text(listed["id"]))
			kind, _ := findType(domainType)
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return err
			}
			current, err := c.readResource(ctx, endpoint)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !insightsARMReadValid(current, id, domainType) || !nativeConfigurationContains(domainSnapshot(domainType, listed), domainSnapshot(domainType, current.data)) {
				return serviceDenied("domain_dns_reference_index_changed")
			}
			if err := domainMetadata(domainType, current.data); err != nil {
				return err
			}
			if strings.EqualFold(text(object(current.data["properties"])["dnsZoneId"]), zoneID) {
				return serviceDenied("dns_zone_has_registered_domain")
			}
			observed[id] = c.privateConfiguration(domainSnapshot(domainType, current.data))
		}
		after := c.privateConfiguration(observed)
		if pass != 0 && before != after {
			return serviceDenied("domain_dns_reference_index_changed")
		}
		before = after
	}
	return nil
}
