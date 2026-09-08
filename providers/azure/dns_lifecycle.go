package azure

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const publicDNSZoneType = "Microsoft.Network/dnsZones"
const privateDNSZoneType = "Microsoft.Network/privateDnsZones"
const privateDNSLinkType = "Microsoft.Network/privateDnsZones/virtualNetworkLinks"
const privateDNSZoneGroupType = "Microsoft.Network/privateEndpoints/privateDnsZoneGroups"

func dnsChildTypes(zone string) []string {
	types := []string{"A", "AAAA", "CNAME", "MX", "PTR", "SOA", "SRV", "TXT"}
	if strings.EqualFold(zone, publicDNSZoneType) {
		types = append(types, "CAA", "NS")
	}
	sort.Strings(types)
	for i := range types {
		types[i] = zone + "/" + types[i]
	}
	return types
}
func isDNSZoneType(kind string) bool {
	return strings.EqualFold(kind, publicDNSZoneType) || strings.EqualFold(kind, privateDNSZoneType)
}
func isDNSRecordType(kind string) bool {
	parts := strings.Split(kind, "/")
	if len(parts) != 3 {
		return false
	}
	zone := strings.Join(parts[:2], "/")
	return isDNSZoneType(zone) && slices.ContainsFunc(dnsChildTypes(zone), func(expected string) bool { return strings.EqualFold(expected, kind) })
}
func isDNSSystemRecord(kind, id string) bool {
	return isDNSRecordType(kind) && (strings.EqualFold(last(kind), "SOA") || (strings.EqualFold(kind, publicDNSZoneType+"/NS") && last(id) == "@"))
}
func dnsExternalController(kind string) bool {
	return strings.EqualFold(kind, privateDNSZoneGroupType) || strings.EqualFold(kind, privateDNSLinkType)
}

func (c *client) privateDNSLinks(ctx context.Context, zoneID string, raw map[string]any) ([]serviceChild, error) {
	return c.nativeServiceChildren(ctx, asset.Identity{NativeID: zoneID, NativeType: privateDNSZoneType}, raw, []string{privateDNSLinkType})
}

type dnsGroupRecord struct {
	id, kind, fqdn string
	ips            []string
	ttl            any
}

// These identities come from the Network resource provider's read-only
// recordSets field. DNS labels, IP equality or naming conventions alone never
// establish a private endpoint's ownership of an arbitrary DNS record.
func privateDNSGroupRecords(properties map[string]any) ([]dnsGroupRecord, error) {
	configs, ok := properties["privateDnsZoneConfigs"].([]any)
	if !ok {
		return nil, fmt.Errorf("Azure DNS zone group omitted its configuration collection")
	}
	result := []dnsGroupRecord{}
	seen := map[string]bool{}
	for _, entry := range configs {
		config := object(object(entry)["properties"])
		zone, kind, err := parseID(text(config["privateDnsZoneId"]))
		if err != nil || !strings.EqualFold(kind, privateDNSZoneType) {
			return nil, fmt.Errorf("invalid private DNS zone group target")
		}
		records, ok := config["recordSets"].([]any)
		if !ok {
			return nil, fmt.Errorf("Azure DNS zone group omitted its managed record set collection")
		}
		for _, entry := range records {
			record := object(entry)
			recordType := strings.ToUpper(text(record["recordType"]))
			if recordType != "A" && recordType != "AAAA" {
				return nil, fmt.Errorf("unsupported private endpoint DNS record type")
			}
			name := text(record["recordSetName"])
			if name == "" || strings.Contains(name, "/") {
				return nil, fmt.Errorf("invalid private endpoint DNS record name")
			}
			id, kind, err := parseID(zone + "/" + recordType + "/" + name)
			if err != nil || seen[id] || !strings.EqualFold(kind, privateDNSZoneType+"/"+recordType) {
				return nil, fmt.Errorf("invalid or duplicate private endpoint DNS record")
			}
			ips, err := dnsAddresses(record["ipAddresses"], recordType)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("private endpoint DNS record has no complete address set")
			}
			seen[id] = true
			result = append(result, dnsGroupRecord{id: id, kind: privateDNSZoneType + "/" + recordType, fqdn: text(record["fqdn"]), ips: ips, ttl: record["ttl"]})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

func dnsAddresses(raw any, recordType string) ([]string, error) {
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid DNS address array")
	}
	result := []string{}
	for _, value := range values {
		ip, err := netip.ParseAddr(text(value))
		if err != nil || ip.Is4() != (recordType == "A") {
			return nil, fmt.Errorf("invalid DNS address family")
		}
		result = append(result, ip.String())
	}
	sort.Strings(result)
	if len(slices.Compact(slices.Clone(result))) != len(result) {
		return nil, fmt.Errorf("duplicate DNS address")
	}
	return result, nil
}

func privateDNSRecordAddresses(recordType string, raw map[string]any) ([]string, error) {
	field, address := "aRecords", "ipv4Address"
	if recordType == "AAAA" {
		field, address = "aaaaRecords", "ipv6Address"
	}
	records, ok := object(raw["properties"])[field].([]any)
	if !ok {
		return nil, fmt.Errorf("private DNS record omitted its native values")
	}
	ips := []any{}
	for _, record := range records {
		ips = append(ips, object(record)[address])
	}
	return dnsAddresses(ips, recordType)
}

func (c *client) privateDNSGroupChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	records, err := privateDNSGroupRecords(object(raw["properties"]))
	if err != nil {
		return nil, err
	}
	result := []serviceChild{}
	for _, record := range records {
		kind, _ := findType(record.kind)
		endpoint, err := c.resourceURL(kind, record.id)
		if err != nil {
			return nil, err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(current, record.id, record.kind) {
			return nil, fmt.Errorf("private endpoint DNS read identity mismatch")
		}
		actual, err := privateDNSRecordAddresses(last(record.kind), current.data)
		if err != nil || !slices.Equal(actual, record.ips) {
			return nil, fmt.Errorf("private endpoint DNS record values changed or include another owner")
		}
		properties := object(current.data["properties"])
		if properties["isAutoRegistered"] == true {
			return nil, fmt.Errorf("private endpoint record is owned by VM auto-registration")
		}
		if record.fqdn != "" && !strings.EqualFold(strings.TrimSuffix(record.fqdn, "."), strings.TrimSuffix(text(properties["fqdn"]), ".")) {
			return nil, fmt.Errorf("private endpoint DNS record FQDN changed")
		}
		if record.ttl != nil && fmt.Sprint(record.ttl) != fmt.Sprint(properties["ttl"]) {
			return nil, fmt.Errorf("private endpoint DNS record TTL changed")
		}
		result = append(result, serviceChild{kind: record.kind, id: record.id, data: current.data})
	}
	return result, nil
}

// Auto-registration is a native DNS property. With one registration link it
// has one owner. With multiple links, every address must uniquely belong to a
// linked VNet's authoritative address space; overlapping/unknown spaces cannot
// establish an exclusive deletion impact.
func (c *client) privateDNSRegistrationChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	if object(raw["properties"])["registrationEnabled"] != true {
		return nil, nil
	}
	parentID, kind, err := parseID(parent.NativeID)
	if err != nil || !strings.EqualFold(kind, privateDNSLinkType) {
		return nil, fmt.Errorf("invalid DNS registration link identity")
	}
	zoneID := strings.Join(strings.Split(parentID, "/")[:9], "/")
	zoneKind, _ := findType(privateDNSZoneType)
	endpoint, err := c.resourceURL(zoneKind, zoneID)
	if err != nil {
		return nil, err
	}
	zone, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(zone, zoneID, privateDNSZoneType) {
		return nil, fmt.Errorf("DNS registration zone identity mismatch")
	}
	links, err := c.privateDNSLinks(ctx, zoneID, zone.data)
	if err != nil {
		return nil, err
	}
	registrations := []serviceChild{}
	found := false
	for _, link := range links {
		if object(link.data["properties"])["registrationEnabled"] == true {
			registrations = append(registrations, link)
			if link.id == parentID {
				found = true
				if productGeneration(link.data) != productGeneration(raw) {
					return nil, fmt.Errorf("DNS registration link changed")
				}
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("DNS registration link missing from its zone")
	}
	records, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: zoneID, NativeType: privateDNSZoneType}, zone.data, []string{privateDNSZoneType + "/A", privateDNSZoneType + "/AAAA"})
	if err != nil {
		return nil, err
	}
	prefixes := map[string][]netip.Prefix{}
	if len(registrations) > 1 {
		for _, link := range registrations {
			networkID := text(object(object(link.data["properties"])["virtualNetwork"])["id"])
			vnetKind, _ := findType(vnetType)
			endpoint, err := c.resourceURL(vnetKind, networkID)
			if err != nil {
				return nil, err
			}
			network, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return nil, err
			}
			if !validResourceResponse(network, networkID, vnetType) {
				return nil, fmt.Errorf("DNS registration VNet identity mismatch")
			}
			values, ok := object(object(network.data["properties"])["addressSpace"])["addressPrefixes"].([]any)
			if !ok || len(values) == 0 {
				return nil, fmt.Errorf("DNS registration VNet has no address space")
			}
			for _, value := range values {
				prefix, err := netip.ParsePrefix(text(value))
				if err != nil {
					return nil, fmt.Errorf("invalid DNS registration VNet prefix")
				}
				prefixes[link.id] = append(prefixes[link.id], prefix)
			}
		}
	}
	result := []serviceChild{}
	for _, record := range records {
		if object(record.data["properties"])["isAutoRegistered"] != true {
			continue
		}
		ips, err := privateDNSRecordAddresses(last(record.kind), record.data)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("auto-registered record has no address identity")
		}
		owner := ""
		for _, link := range registrations {
			matches := true
			if len(registrations) > 1 {
				for _, value := range ips {
					ip, _ := netip.ParseAddr(value)
					if !slices.ContainsFunc(prefixes[link.id], func(prefix netip.Prefix) bool { return prefix.Contains(ip) }) {
						matches = false
						break
					}
				}
			}
			if matches {
				if owner != "" {
					return nil, fmt.Errorf("ambiguous DNS auto-registration ownership")
				}
				owner = link.id
			}
		}
		if owner == "" {
			return nil, fmt.Errorf("DNS auto-registration ownership is unresolved")
		}
		if owner == parentID {
			result = append(result, record)
		}
	}
	return result, nil
}

// VNet deletion also deletes DNS links and auto-registered records. Require the
// independently planned link actions to finish first, including links in zones
// outside the VNet's own resource group.
func (c *client) virtualNetworkHasDNSLinks(ctx context.Context, id string) (bool, error) {
	links, err := c.virtualNetworkDNSLinks(ctx, id)
	return len(links) != 0, err
}

func (c *client) virtualNetworkDNSLinks(ctx context.Context, id string) ([]serviceChild, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	kind := runtime.resourceKind(privateDNSLinkType)
	request := contracts.InventoryRequest{Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: c.subscription}}
	result := []serviceChild{}
	for {
		batch, err := runtime.listProduct(ctx, c, request, nil)
		if err != nil {
			return nil, err
		}
		for _, link := range batch.Items {
			if strings.EqualFold(text(object(link.Normalized["virtualNetwork"])["id"]), id) {
				rule, _ := findType(privateDNSLinkType)
				endpoint, err := c.resourceURL(rule, link.NativeID)
				if err != nil {
					return nil, err
				}
				live, err := c.request(ctx, "GET", endpoint)
				if err != nil {
					return nil, err
				}
				if !validResourceResponse(live, link.NativeID, privateDNSLinkType) || !strings.EqualFold(text(object(object(live.data["properties"])["virtualNetwork"])["id"]), id) {
					return nil, fmt.Errorf("VNet DNS link membership changed")
				}
				if err := serviceIncarnation(asset.Asset{Normalized: link.Normalized}, live.data); err != nil {
					return nil, err
				}
				result = append(result, serviceChild{id: link.NativeID, kind: privateDNSLinkType, data: live.data})
			}
		}
		if batch.Complete {
			return result, nil
		}
		request.Cursor = batch.NextCursor
	}
}
