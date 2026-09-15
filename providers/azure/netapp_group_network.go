package azure

import (
	"context"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// This records interface correlation, not ownership or permission to delete.
// The sibling query only describes primary IPs; own mount targets can add IPs.
func (c *client) netappGroupNetwork(ctx context.Context, region string, members, known map[string]any) (map[string]any, error) {
	addresses := map[string]any{}
	volumes := map[string]any{}
	complete := len(members) > 0
	for _, id := range slices.Sorted(maps.Keys(members)) {
		own, err := c.netappRead(ctx, id, netappVolumeType)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(own.data) != object(members[id])["configuration"] {
			return nil, serviceDenied("netapp_group_network_volume_changed")
		}
		network, ready, err := c.netappNetworkBoundary(ctx, id, own.data)
		if err != nil {
			return nil, err
		}
		volumes[id] = network
		complete = complete && ready && len(network) > 0
		props := object(own.data["properties"])
		subnet, typ, err := parseID(text(props["subnetId"]))
		if err != nil || !strings.EqualFold(typ, subnetType) {
			return nil, serviceDenied("invalid_netapp_group_subnet")
		}
		add := func(ip string) {
			key := subnet + "|" + ip
			ids, _ := addresses[key].([]string)
			if !slices.Contains(ids, id) {
				addresses[key] = append(ids, id)
			}
		}
		primary := text(object(object(network["members"])[id])["ip"])
		if primary != "" {
			add(primary)
		}
		mounts, valid := props["mountTargets"].([]any)
		if props["mountTargets"] != nil && !valid {
			return nil, serviceDenied("invalid_netapp_group_mounts")
		}
		complete = complete && len(mounts) > 0
		seen := map[string]bool{}
		for _, value := range mounts {
			mount := object(value)
			ip, err := netip.ParseAddr(text(mount["ipAddress"]))
			uid := text(mount["fileSystemId"])
			if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || !uuidPattern.MatchString(uid) || !strings.EqualFold(uid, text(props["fileSystemId"])) || seen[ip.String()] {
				return nil, serviceDenied("invalid_netapp_group_mount")
			}
			if mount["mountTargetId"] != nil && !uuidPattern.MatchString(text(mount["mountTargetId"])) {
				return nil, serviceDenied("invalid_netapp_group_mount_identity")
			}
			seen[ip.String()] = true
			add(ip.String())
		}
	}
	kind, _ := findType(nicType)
	listed, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Network/networkInterfaces", kind.Version)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	ids := map[string]bool{}
	for _, value := range listed {
		raw := object(value)
		id, typ, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(typ, nicType) || !strings.HasPrefix(id, c.root()+"/") || ids[id] {
			return nil, serviceDenied("invalid_netapp_group_nic_index")
		}
		ids[id] = true
	}
	for id := range known {
		canonical, typ, err := parseID(id)
		if err != nil || canonical != id || !strings.EqualFold(typ, nicType) || !strings.HasPrefix(id, c.root()+"/") {
			return nil, serviceDenied("invalid_netapp_group_nic_hint")
		}
		if _, exists := ids[id]; !exists {
			ids[id] = false
		}
	}
	interfaces := map[string]any{}
	found := map[string]string{}
	identities := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		own, err := c.request(ctx, "GET", endpoint)
		if isNotFound(err) && !ids[id] {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if !validResourceResponse(own, id, nicType) || own.status != 200 || own.data["error"] != nil || operationLocation(own.header) != "" {
			return nil, serviceDenied("invalid_netapp_group_nic_response")
		}
		if resourceRegion(own.data) != region {
			if known[id] != nil {
				return nil, serviceDenied("netapp_group_nic_region_changed")
			}
			continue
		}
		p := object(own.data["properties"])
		configs, ok := p["ipConfigurations"].([]any)
		if !ok {
			return nil, serviceDenied("invalid_netapp_group_nic_addresses")
		}
		matched := []string{}
		for _, value := range configs {
			config := object(object(value)["properties"])
			subnet, typ, err := parseID(text(object(config["subnet"])["id"]))
			ip, ipErr := netip.ParseAddr(text(config["privateIPAddress"]))
			if err != nil || !strings.EqualFold(typ, subnetType) || ipErr != nil || ip.Zone() != "" {
				return nil, serviceDenied("invalid_netapp_group_nic_address")
			}
			key := subnet + "|" + ip.Unmap().String()
			if addresses[key] != nil {
				if slices.Contains(matched, key) || found[key] != "" && found[key] != id {
					return nil, serviceDenied("ambiguous_netapp_group_nic_address")
				}
				matched = append(matched, key)
				found[key] = id
			}
		}
		if len(matched) == 0 && known[id] == nil {
			continue
		}
		slices.Sort(matched)
		workloads, valid := p["hostedWorkloads"].([]any)
		linked := []string{}
		verified := valid && len(workloads) > 0 && len(matched) > 0
		for _, value := range workloads {
			workload, ok := value.(string)
			workload = strings.ToLower(workload)
			if !ok || c.netappIdentity(workload, netappVolumeType) != nil || slices.Contains(linked, workload) {
				verified = false
				continue
			}
			linked = append(linked, workload)
			if members[workload] == nil {
				verified = false
			}
		}
		for _, key := range matched {
			for _, volume := range addresses[key].([]string) {
				if !slices.Contains(linked, volume) {
					verified = false
				}
			}
		}
		slices.Sort(linked)
		uid, ok := p["resourceGuid"].(string)
		if p["resourceGuid"] != nil && (!ok || uid != "" && !uuidPattern.MatchString(uid)) {
			return nil, serviceDenied("invalid_netapp_group_nic_uuid")
		}
		if uid != "" {
			canonical := strings.ToLower(uid)
			if identities[canonical] != "" {
				return nil, serviceDenied("ambiguous_netapp_group_nic_identity")
			}
			identities[canonical] = id
		}
		verified = verified && uuidPattern.MatchString(uid) && p["provisioningState"] == "Succeeded"
		complete = complete && verified
		interfaces[id] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": strings.ToLower(uid), "addresses": matched, "workloads": linked, "correlated": verified}
	}
	for key := range addresses {
		if found[key] == "" {
			complete = false
		}
	}
	return map[string]any{"volumes": volumes, "addresses": addresses, "interfaces": interfaces, "complete": complete}, nil
}
