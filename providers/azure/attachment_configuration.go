package azure

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Project configuration after only the reviewed Delete -> Detach changes.
// Exclude transport/provisioning metadata, not unrelated writable settings.
func attachmentPreparedConfiguration(kind string, live map[string]any, retained []attachedResource) (map[string]any, error) {
	wire, err := json.Marshal(live)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, value := range retained {
		keep[value.id] = true
	}
	props := object(raw["properties"])
	switch kind {
	case nicType:
		for _, value := range array(props["ipConfigurations"]) {
			ip := object(object(object(value)["properties"])["publicIPAddress"])
			if keep[strings.ToLower(text(ip["id"]))] {
				object(ip["properties"])["deleteOption"] = "Detach"
			}
		}
		writable, err := nicWritableResource(raw)
		if err != nil {
			return nil, err
		}
		return map[string]any{"writable": writable, "vm": object(props["virtualMachine"])["id"], "private_endpoint": object(props["privateEndpoint"])["id"], "hosted_workloads": props["hostedWorkloads"], "resource_guid": props["resourceGuid"]}, nil
	case vmType:
		storage := object(props["storageProfile"])
		os := object(storage["osDisk"])
		if keep[strings.ToLower(text(object(os["managedDisk"])["id"]))] {
			os["deleteOption"] = "Detach"
		}
		for _, value := range array(storage["dataDisks"]) {
			disk := object(value)
			delete(disk, "diskIOPSReadWrite")
			delete(disk, "diskMBpsReadWrite")
			if keep[strings.ToLower(text(object(disk["managedDisk"])["id"]))] {
				disk["deleteOption"] = "Detach"
			}
		}
		for _, value := range array(object(props["networkProfile"])["networkInterfaces"]) {
			nic := object(value)
			if keep[strings.ToLower(text(nic["id"]))] {
				object(nic["properties"])["deleteOption"] = "Detach"
			}
		}
		delete(raw, "etag")
		for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
			delete(object(raw["systemData"]), key)
		}
		delete(props, "provisioningState")
		delete(props, "instanceView")
		return raw, nil
	default:
		return nil, fmt.Errorf("invalid Azure attachment configuration kind")
	}
}
