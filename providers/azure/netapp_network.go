package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// This is a read-only POST with a required body, not a pageable GET resource.
// NIC information supplies primary mount IPs and volume IDs, not NIC ARM IDs.
func (c *client) netappNetworkSet(ctx context.Context, region, subnet, set string) (map[string]any, error) {
	canonical, kind, err := parseID(subnet)
	if err != nil || canonical != subnet || !strings.EqualFold(kind, subnetType) || len(strings.Split(subnet, "/")) != 11 || !uuidPattern.MatchString(set) || region == "" || strings.Trim(region, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return nil, serviceDenied("invalid_netapp_network_scope")
	}
	wire, _ := json.Marshal(map[string]any{"networkSiblingSetId": set, "subnetId": subnet})
	res, err := c.requestBody(ctx, "POST", apiURL(c.root()+"/providers/Microsoft.NetApp/locations/"+region+"/queryNetworkSiblingSet", netappVersion), wire, nil)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	raw := res.data
	if res.status != 200 || operationLocation(res.header) != "" || raw["error"] != nil || raw["nextLink"] != nil || !strings.EqualFold(text(raw["networkSiblingSetId"]), set) || !strings.EqualFold(text(raw["subnetId"]), subnet) {
		return nil, serviceDenied("invalid_netapp_network_response")
	}
	state, ok := raw["networkSiblingSetStateId"].(string)
	if !ok || state == "" || len(state) > 1024 || state != strings.TrimSpace(state) || !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Updating"}, text(raw["provisioningState"])) || !slices.Contains([]string{"Basic", "Standard", "Basic_Standard", "Standard_Basic"}, text(raw["networkFeatures"])) {
		return nil, serviceDenied("invalid_netapp_network_state")
	}
	rows, ok := raw["nicInfoList"].([]any)
	if !ok {
		return nil, serviceDenied("incomplete_netapp_network_members")
	}
	nics := map[string]any{}
	seen := map[string]bool{}
	for _, value := range rows {
		row := object(value)
		ip, err := netip.ParseAddr(text(row["ipAddress"]))
		if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
			return nil, serviceDenied("invalid_netapp_network_ip")
		}
		address := ip.Unmap().String()
		volumes, ok := row["volumeResourceIds"].([]any)
		if !ok || len(volumes) == 0 || nics[address] != nil {
			return nil, serviceDenied("invalid_netapp_network_nic")
		}
		ids := []string{}
		for _, value := range volumes {
			id, ok := value.(string)
			id = strings.ToLower(id)
			if !ok || c.netappIdentity(id, netappVolumeType) != nil || seen[id] {
				return nil, serviceDenied("invalid_netapp_network_volume")
			}
			seen[id] = true
			ids = append(ids, id)
		}
		slices.Sort(ids)
		nics[address] = ids
	}
	return map[string]any{"set": strings.ToLower(set), "subnet": subnet, "state": state, "provisioning_state": raw["provisioningState"], "features": raw["networkFeatures"], "nics": nics}, nil
}

func (c *client) netappNetworkBoundary(ctx context.Context, id string, raw map[string]any) (map[string]any, bool, error) {
	p := object(raw["properties"])
	if p["networkSiblingSetId"] == nil {
		return map[string]any{}, true, nil
	}
	set, subnet := strings.ToLower(text(p["networkSiblingSetId"])), strings.ToLower(text(p["subnetId"]))
	region := resourceRegion(raw)
	first, err := c.netappNetworkSet(ctx, region, subnet, set)
	if err != nil {
		return nil, false, err
	}
	members := map[string]any{}
	ready := first["provisioning_state"] == "Succeeded" && (first["features"] == "Basic" || first["features"] == "Standard")
	for _, ip := range slices.Sorted(maps.Keys(object(first["nics"]))) {
		for _, peer := range object(first["nics"])[ip].([]string) {
			own, err := c.netappRead(ctx, peer, netappVolumeType)
			if err != nil {
				return nil, false, contracts.DependencyReadError(err)
			}
			props := object(own.data["properties"])
			uid, validType := props["fileSystemId"].(string)
			if props["fileSystemId"] != nil && (!validType || uid != "" && !uuidPattern.MatchString(uid)) {
				return nil, false, serviceDenied("invalid_netapp_network_volume_uuid")
			}
			if resourceRegion(own.data) != region || !strings.EqualFold(text(props["networkSiblingSetId"]), set) || !strings.EqualFold(text(props["subnetId"]), subnet) {
				return nil, false, serviceDenied("netapp_network_volume_changed")
			}
			matched := false
			for _, value := range array(props["mountTargets"]) {
				target := object(value)
				address, parseErr := netip.ParseAddr(text(target["ipAddress"]))
				if parseErr == nil && address.Zone() == "" && address.Unmap().String() == ip && (target["fileSystemId"] == nil || target["fileSystemId"] == props["fileSystemId"]) {
					matched = true
				}
			}
			if props["mountTargets"] != nil && !matched {
				return nil, false, serviceDenied("netapp_network_mount_changed")
			}
			if peer == id && c.privateConfiguration(own.data) != c.privateConfiguration(raw) {
				return nil, false, serviceDenied("netapp_network_owner_changed")
			}
			members[peer] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": uid, "ip": ip}
			ready = ready && props["provisioningState"] == "Succeeded" && uuidPattern.MatchString(text(props["fileSystemId"]))
		}
	}
	if members[id] == nil {
		return nil, false, serviceDenied("netapp_network_owner_omitted")
	}
	second, err := c.netappNetworkSet(ctx, region, subnet, set)
	if err != nil {
		return nil, false, err
	}
	if c.privateConfiguration(first) != c.privateConfiguration(second) {
		return nil, false, serviceDenied("netapp_network_set_changed")
	}
	for _, peer := range slices.Sorted(maps.Keys(members)) {
		own, err := c.netappRead(ctx, peer, netappVolumeType)
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(own.data) != object(members[peer])["configuration"] {
			return nil, false, serviceDenied("netapp_network_peer_changed")
		}
	}
	first["members"] = members
	return first, ready, nil
}

// Reviewed siblings may have been deleted by earlier volume prerequisites. Only
// their independent own 404 permits a smaller set; live omitted peers and new
// members always require another review.
func (c *client) netappNetworkMatches(ctx context.Context, old, current map[string]any) error {
	for _, key := range []string{"set", "subnet", "features"} {
		if old[key] != current[key] {
			return serviceDenied("netapp_volume_network_changed")
		}
	}
	before, after := object(old["members"]), object(current["members"])
	for id, value := range after {
		if before[id] == nil || c.privateConfiguration(object(value)) != c.privateConfiguration(object(before[id])) {
			return serviceDenied("netapp_volume_network_changed")
		}
	}
	removed := false
	for id := range before {
		if after[id] != nil {
			continue
		}
		_, err := c.netappRead(ctx, id, netappVolumeType)
		if !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("netapp_network_live_peer_omitted")
		}
		removed = true
	}
	if !removed && old["state"] != current["state"] {
		return serviceDenied("netapp_volume_network_changed")
	}
	return nil
}
