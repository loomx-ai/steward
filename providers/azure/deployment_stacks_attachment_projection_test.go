package azure

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackFlatAttachmentProjection(t *testing.T) {
	for _, mode := range []string{"delete", "detach_nic", "missing_option", "detach_disk", "detach_ip", "invalid_option", "duplicate_nic", "foreign_nic", "unrelated_nic", "ambiguous_vm"} {
		t.Run(mode, func(t *testing.T) {
			assets := attachmentAssets(t, attachmentResources())
			vm := &assets[0]
			nics := object(vm.Normalized["networkProfile"])["networkInterfaces"].([]any)
			option := object(object(nics[0])["properties"])
			switch mode {
			case "detach_nic":
				option["deleteOption"] = "Detach"
			case "missing_option":
				delete(option, "deleteOption")
			case "detach_disk":
				object(object(vm.Normalized["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
			case "detach_ip":
				ip := object(object(assets[3].Normalized["ipConfigurations"].([]any)[0])["properties"])
				object(object(ip["publicIPAddress"])["properties"])["deleteOption"] = "Detach"
			case "invalid_option":
				option["deleteOption"] = "Unknown"
			case "duplicate_nic":
				object(vm.Normalized["networkProfile"])["networkInterfaces"] = append(nics, nics[0])
			case "foreign_nic":
				object(nics[0])["id"] = "/subscriptions/other/resourceGroups/test/providers/Microsoft.Network/networkInterfaces/nic"
			case "unrelated_nic":
				object(nics[0])["id"] = resourceID(nicType, "other")
			case "ambiguous_vm":
				other := assets[0]
				other.ID = "other-vm"
				other.Identity.NativeID = resourceID(vmType, "other")
				assets = append(assets, other)
			}
			impacts := []contracts.ActionImpact{}
			for _, value := range assets {
				value.Identity.Partition = "azure"
				impacts = append(impacts, contracts.ActionImpact{Asset: value, ControllerID: "stack", Delete: true})
			}
			c, req := stackDeletePlanFixture(t, false, impacts...)
			req.IdempotencyKey = "flat-attachments"
			before, _ := json.Marshal(req)
			member, err := c.deploymentStackMemberRequest(req, "vm")
			switch mode {
			case "invalid_option", "duplicate_nic", "foreign_nic", "ambiguous_vm":
				if err == nil {
					t.Fatal("unverified attachment projection accepted", member)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				controllers := map[asset.AssetID]asset.AssetID{}
				for _, impact := range member.LifecycleImpacts {
					controllers[impact.Asset.ID] = impact.ControllerID
				}
				expected := map[asset.AssetID]asset.AssetID{"boot": "vm", "data": "vm", "nic": "vm", "ip": "nic"}
				order := []asset.AssetID{"vm"}
				switch mode {
				case "detach_nic", "missing_option", "unrelated_nic":
					delete(expected, "nic")
					delete(expected, "ip")
					order = []asset.AssetID{"nic", "vm"}
				case "detach_disk":
					delete(expected, "boot")
				case "detach_ip":
					delete(expected, "ip")
				}
				if len(controllers) != len(expected) {
					t.Fatal("wrong native attachment scope", controllers, expected)
				}
				for child, parent := range expected {
					if controllers[child] != parent {
						t.Fatal("lost typed delete option relation", child, controllers)
					}
				}
				actual, err := c.deploymentStackPreparationOrder(req)
				if err != nil || !slices.Equal(actual, order) {
					t.Fatal("wrong attachment preparation coverage", actual, order, err)
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("attachment projection rewrote frozen plan")
			}
		})
	}
}
