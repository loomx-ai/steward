package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const routerReview = "_router_configuration"
const routerBaseReview = "_router_base_configuration"

// Read the complete parent before recording deletion impact. A LIST record can
// be stale or omit configuration; neither may silently replace a reviewed GET.
func (c *client) routerInventoryData(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	kind, _ := findType(routerType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	if listed["name"] != last(id) || !firewallNumericID(text(listed["id"])) {
		return nil, groupDenied("router_list_identity_invalid")
	}
	if link, ok := listed["selfLink"]; ok && c.canonicalName(text(link)) != id {
		return nil, groupDenied("router_list_scope_changed")
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if err := c.routerData(id, text(listed["id"]), live); err != nil {
		return nil, err
	}
	return live, nil
}

func (c *client) routerData(id, incarnation string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	if _, err := c.cloudNatRouter(data, id, incarnation); err != nil {
		return err
	}
	if region, present := data["region"]; present && c.canonicalName(text(region)) != strings.Split(id, "/routers/")[0] {
		return groupDenied("router_region_changed")
	}
	if zone, present := data["zone"]; present && zone != "" {
		return groupDenied("router_region_changed")
	}
	if err := cloudNatScalars(data, []string{"description", "network", "nccGateway"}, []string{"encryptedInterconnectRouter"}, nil, nil); err != nil {
		return err
	}
	if bgp, present := data["bgp"]; present && object(bgp) == nil {
		return groupDenied("router_bgp_invalid")
	}
	if _, err := routePolicyBGPPeers(data); err != nil {
		return err
	}
	interfaces, err := cloudNatObjects(data, "interfaces")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, value := range interfaces {
		name := text(value["name"])
		if !routePolicySegment.MatchString(name) || seen[name] {
			return groupDenied("router_interface_invalid")
		}
		seen[name] = true
		if err := cloudNatScalars(value, []string{"name", "ipRange", "ipVersion", "linkedVpnTunnel", "linkedInterconnectAttachment", "managementType", "privateIpAddress", "redundantInterface", "subnetwork"}, nil, nil, nil); err != nil {
			return err
		}
		for field, collection := range map[string]string{"linkedVpnTunnel": "vpnTunnels", "linkedInterconnectAttachment": "interconnectAttachments", "subnetwork": "subnetworks"} {
			if value, present := value[field]; present {
				target := c.canonicalName(text(value))
				parts := strings.Split(strings.TrimPrefix(target, "//compute.googleapis.com/"), "/")
				region := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")[3]
				if !strings.HasPrefix(target, "//compute.googleapis.com/") || len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "regions" || parts[3] != region || parts[4] != collection || !routePolicySegment.MatchString(parts[5]) {
					return groupDenied("router_interface_scope_changed")
				}
			}
		}
	}
	keys, err := cloudNatObjects(data, "md5AuthenticationKeys")
	if err != nil {
		return err
	}
	seen = map[string]bool{}
	for _, key := range keys {
		name, ok := key["name"].(string)
		if !ok || name == "" || seen[name] {
			return groupDenied("router_authentication_key_invalid")
		}
		seen[name] = true
		if err := cloudNatScalars(key, []string{"name", "key"}, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// Full review binds embedded NATs and ordered BGP policy references. The base
// review lets controller cleanup verify the remaining router configuration after
// separately reviewed child removals. Unknown native fields stay in both proofs.
func routerConfiguration(data map[string]any, base bool) string {
	value := cloneParameters(data)
	for _, field := range []string{"creationTimestamp", "kind", "region", "selfLink", "params"} {
		delete(value, field)
	}
	for _, field := range []string{"nats", "bgpPeers", "interfaces", "md5AuthenticationKeys"} {
		if base && (field == "nats" || field == "bgpPeers") {
			delete(value, field)
			continue
		}
		values := append([]any{}, array(value[field])...)
		if field == "nats" {
			for i, item := range values {
				values[i] = cloudNatConfiguration(object(item))
			}
		} else if field == "bgpPeers" {
			values, _ = routePolicyBGPPeers(data) // routerData validates before inventory calls this.
		}
		slices.SortFunc(values, func(a, b any) int { return strings.Compare(text(object(a)["name"]), text(object(b)["name"])) })
		value[field] = values
	}
	return firewallDigest(value)
}
