package azure

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const containerGroupType = "Microsoft.ContainerInstance/containerGroups"

// Containers and init containers share the group's native lifecycle. Their
// runtime state is not configuration; external volumes are never owned by it.
func containerGroupConfiguration(raw map[string]any) string {
	return serviceParentConfiguration(containerGroupType, containerGroupSnapshot(raw))
}

func containerGroupSnapshot(raw map[string]any) map[string]any {
	snapshot := maps.Clone(raw)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	systemData := maps.Clone(object(raw["systemData"]))
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(systemData, field)
	}
	if raw["systemData"] != nil {
		snapshot["systemData"] = systemData
	}
	properties := maps.Clone(object(raw["properties"]))
	snapshot["properties"] = properties
	for _, field := range []string{"provisioningState", "instanceView", "isCreatedFromStandbyPool"} {
		delete(properties, field)
	}
	address := maps.Clone(object(properties["ipAddress"]))
	for _, field := range []string{"ip", "fqdn"} {
		delete(address, field)
	}
	if properties["ipAddress"] != nil {
		properties["ipAddress"] = address
	}
	for _, field := range []string{"containers", "initContainers"} {
		if properties[field] == nil {
			continue
		}
		members := []any{}
		for _, value := range array(properties[field]) {
			container := maps.Clone(object(value))
			configuration := maps.Clone(object(container["properties"]))
			delete(configuration, "instanceView")
			container["properties"] = configuration
			members = append(members, container)
		}
		properties[field] = members
	}
	return snapshot
}

// ACI can return no ETag or immutable creation ID. A connection-keyed digest
// binds commands, environment and other sensitive settings without persisting
// their plaintext or an unkeyed digest that could expose low-entropy secrets.
func (c *client) containerGroupPrivateConfiguration(raw map[string]any) string {
	return c.privateConfiguration(containerGroupSnapshot(raw))
}

func (c *client) privateConfiguration(snapshot map[string]any) string {
	payload, _ := json.Marshal(snapshot)
	hash := hmac.New(sha256.New, c.fingerprint[:])
	hash.Write(payload)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (c *client) containerGroupPrivateIncarnation(planned asset.Asset, live map[string]any) error {
	if strings.EqualFold(planned.Identity.NativeType, containerGroupType) {
		if expected := text(planned.Normalized["_container_group_private_configuration"]); expected == "" || expected != c.containerGroupPrivateConfiguration(live) {
			return serviceDenied("container_group_private_configuration_changed")
		}
	}
	return nil
}

func validateContainerGroup(raw map[string]any) error {
	properties := object(raw["properties"])
	for _, field := range []string{"containers", "initContainers"} {
		if field == "initContainers" && properties[field] == nil {
			continue
		}
		containers, ok := properties[field].([]any)
		if !ok || (field == "containers" && len(containers) == 0) {
			return serviceDenied("invalid_container_group_members")
		}
		seen := map[string]bool{}
		for _, value := range containers {
			container := object(value)
			name := strings.ToLower(text(container["name"]))
			_, configured := container["properties"].(map[string]any)
			if name == "" || strings.ContainsAny(name, "/\\?#%\x00\r\n ") || seen[name] || !configured {
				return serviceDenied("invalid_container_group_member")
			}
			seen[name] = true
		}
	}
	if properties["subnetIds"] != nil {
		values, ok := properties["subnetIds"].([]any)
		if !ok {
			return serviceDenied("invalid_container_group_subnets")
		}
		seen := map[string]bool{}
		for _, value := range values {
			id, kind, err := parseID(text(object(value)["id"]))
			if err != nil || !strings.EqualFold(kind, subnetType) || seen[id] {
				return serviceDenied("invalid_container_group_subnet")
			}
			seen[id] = true
		}
	}
	return nil
}

func containerGroupIncarnation(planned asset.Asset, live map[string]any) error {
	if !strings.EqualFold(planned.Identity.NativeType, containerGroupType) {
		return nil
	}
	if err := validateContainerGroup(live); err != nil {
		return err
	}
	if expected := text(planned.Normalized["_container_group_configuration"]); expected == "" || expected != containerGroupConfiguration(live) {
		return serviceDenied("container_group_configuration_changed")
	}
	if expected := text(planned.Normalized["_arm_generation"]); expected != "" && expected != productGeneration(live) {
		return serviceDenied("container_group_generation_changed")
	}
	return nil
}
