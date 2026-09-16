package azure

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func groupProductFixture(t *testing.T) (*client, contracts.ActionRequest) {
	t.Helper()
	assets := attachmentAssets(t, attachmentResources())
	req := groupOperationRequest()
	req.Asset.ID = "group"
	req.Asset.Identity = assets[0].Identity
	req.Asset.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
	req.Asset.Identity.NativeType = groupType
	req.Asset.Identity.Partition = "azure"
	for _, v := range assets {
		v.Identity.Partition = "azure"
		parent := asset.AssetID("vm")
		if v.ID == "vm" {
			parent = "group"
		}
		if v.ID == "ip" {
			parent = "nic"
		}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: parent, Delete: true})
	}
	return directClient(nil), req
}
func TestResourceGroupProductRequests(t *testing.T) {
	c, req := groupProductFixture(t)
	req.LifecycleImpacts[0].Asset.Normalized["large_native_value"] = json.Number("9007199254740993")
	before, _ := json.Marshal(req)
	requests, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 || len(requests["vm"].LifecycleImpacts) != 4 || len(requests["nic"].LifecycleImpacts) != 1 || len(requests["boot"].LifecycleImpacts) != 0 {
		t.Fatal("lost native controller subtree", requests)
	}
	vm := requests["vm"]
	if vm.Asset.Normalized["large_native_value"] != json.Number("9007199254740993") {
		t.Fatal("native integer lost precision")
	}
	if vm.IdempotencyKey != req.IdempotencyKey+":member:vm" || vm.ExecutionResult != nil || len(vm.Parameters) != 0 {
		t.Fatal("wrong delegated context", vm)
	}
	got := []asset.AssetID{}
	for _, v := range vm.LifecycleImpacts {
		got = append(got, v.Asset.ID)
	}
	if !reflect.DeepEqual(got, []asset.AssetID{"boot", "data", "ip", "nic"}) {
		t.Fatal("unstable product impact order", got)
	}
	object(vm.LifecycleImpacts[3].Asset.Normalized["dnsSettings"])["internalDnsNameLabel"] = "mutated"
	if object(requests["nic"].Asset.Normalized["dnsSettings"])["internalDnsNameLabel"] == "mutated" {
		t.Fatal("product requests alias each other")
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("projection mutated reviewed request")
	}
}
func TestResourceGroupExternalAttachments(t *testing.T) {
	for _, mode := range []string{"delete", "retain", "detach_delete", "unrelated", "foreign", "root_controller", "retained_inside"} {
		t.Run(mode, func(t *testing.T) {
			c, req := groupProductFixture(t)
			boot := &req.LifecycleImpacts[1]
			if mode != "retained_inside" {
				boot.Asset.Identity.NativeID = strings.Replace(boot.Asset.Identity.NativeID, "/resourcegroups/test/", "/resourcegroups/external/", 1)
			}
			disk := object(object(req.LifecycleImpacts[0].Asset.Normalized["storageProfile"])["osDisk"])
			object(disk["managedDisk"])["id"] = boot.Asset.Identity.NativeID
			switch mode {
			case "retain", "retained_inside":
				boot.Delete = false
			case "detach_delete":
				disk["deleteOption"] = "Detach"
			case "unrelated":
				object(disk["managedDisk"])["id"] = resourceID(diskType, "another")
			case "foreign":
				boot.Asset.Identity.NativeID = strings.Replace(boot.Asset.Identity.NativeID, testSubscription, testTenant, 1)
			case "root_controller":
				boot.ControllerID = req.Asset.ID
			}
			out, err := c.resourceGroupProductRequests(req)
			if mode == "delete" || mode == "retain" {
				if err != nil {
					t.Fatal(err)
				}
				_, present := out["boot"]
				if present != (mode == "delete") {
					t.Fatal("retained member became independent delete")
				}
				found := false
				for _, p := range out["vm"].LifecycleImpacts {
					if p.Asset.ID == "boot" {
						found = true
						if p.Delete != (mode == "delete") {
							t.Fatal("lost retention")
						}
					}
				}
				if !found {
					t.Fatal("lost attachment")
				}
			} else if err == nil || out != nil {
				t.Fatal("unverified external or retained scope accepted", out, err)
			}
		})
	}
}
func TestResourceGroupInvalidProductTree(t *testing.T) {
	for _, mode := range []string{"cycle", "missing_parent", "duplicate_asset", "duplicate_native", "foreign_connection", "foreign_partition", "wrong_kind", "root_alias", "retained_parent", "bad_option", "missing_job", "missing_root", "group_child", "retained_root_child"} {
		t.Run(mode, func(t *testing.T) {
			c, req := groupProductFixture(t)
			switch mode {
			case "cycle":
				req.LifecycleImpacts[0].ControllerID = "nic"
			case "missing_parent":
				req.LifecycleImpacts[1].ControllerID = "missing"
			case "duplicate_asset":
				req.LifecycleImpacts = append(req.LifecycleImpacts, req.LifecycleImpacts[0])
			case "duplicate_native":
				req.LifecycleImpacts[1].Asset.Identity.NativeID = strings.ToUpper(req.LifecycleImpacts[2].Asset.Identity.NativeID)
			case "foreign_connection":
				req.LifecycleImpacts[1].Asset.Identity.ConnectionID = "other"
			case "foreign_partition":
				req.LifecycleImpacts[1].Asset.Identity.Partition = "other"
			case "wrong_kind":
				req.LifecycleImpacts[1].Asset.Identity.NativeType = nicType
			case "root_alias":
				req.LifecycleImpacts[1].Asset.ID = "group"
			case "retained_parent":
				req.LifecycleImpacts[0].Delete = false
			case "bad_option":
				object(object(req.LifecycleImpacts[0].Asset.Normalized["storageProfile"])["osDisk"])["deleteOption"] = "Unknown"
			case "missing_job":
				req.IdempotencyKey = ""
			case "missing_root":
				req.Asset.ID = ""
			case "group_child":
				req.LifecycleImpacts[1].Asset.Identity.NativeID = req.Asset.Identity.NativeID + "-other"
				req.LifecycleImpacts[1].Asset.Identity.NativeType = groupType
			case "retained_root_child":
				req.LifecycleImpacts[1].Delete = false
				req.LifecycleImpacts[1].ControllerID = "group"
			}
			if out, err := c.resourceGroupProductRequests(req); err == nil || out != nil {
				t.Fatal("invalid controller graph accepted", out, err)
			}
		})
	}
}
func TestResourceGroupProductPrerequisites(t *testing.T) {
	for _, mode := range []string{"member", "group", "external_group", "missing_parent", "duplicate", "retain", "impact_overlap"} {
		t.Run(mode, func(t *testing.T) {
			c, req := groupProductFixture(t)
			p := req.LifecycleImpacts[1]
			p.Asset.ID = "prerequisite"
			p.Asset.Identity.NativeID = resourceID(diskType, "prerequisite")
			p.ControllerID = "vm"
			switch mode {
			case "group":
				p.ControllerID = "group"
			case "external_group":
				p.ControllerID = "group"
				p.Asset.Identity.NativeID = strings.Replace(strings.ToLower(p.Asset.Identity.NativeID), "/resourcegroups/test/", "/resourcegroups/external/", 1)
			case "missing_parent":
				p.ControllerID = "missing"
			case "retain":
				p.Delete = false
			case "impact_overlap":
				p = req.LifecycleImpacts[1]
			}
			req.PrerequisiteDeletions = []contracts.ActionImpact{p}
			if mode == "duplicate" {
				req.PrerequisiteDeletions = append(req.PrerequisiteDeletions, p)
			}
			out, err := c.resourceGroupProductRequests(req)
			if mode == "member" || mode == "group" {
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if mode == "member" {
					want = 1
				}
				if len(out["vm"].PrerequisiteDeletions) != want || len(out["nic"].PrerequisiteDeletions) != 0 {
					t.Fatal("prerequisite delegated to wrong controller")
				}
			} else if err == nil || out != nil {
				t.Fatal("invalid prerequisite accepted", out, err)
			}
		})
	}
}

func TestResourceGroupNativeServiceProjection(t *testing.T) {
	c, req := groupProductFixture(t)
	parent := req.LifecycleImpacts[0].Asset
	parent.ID = "sql"
	parent.Identity.NativeType = "Microsoft.Sql/servers"
	parent.Identity.NativeID = resourceID(parent.Identity.NativeType, "sql")
	parent.Normalized = map[string]any{}
	child := parent
	child.ID = "database"
	child.Identity.NativeType = "Microsoft.Sql/servers/databases"
	child.Identity.NativeID = parent.Identity.NativeID + "/databases/master"
	req.LifecycleImpacts = []contracts.ActionImpact{{Asset: child, ControllerID: parent.ID, Delete: true}, {Asset: parent, ControllerID: req.Asset.ID, Delete: true}}
	out, err := c.resourceGroupProductRequests(req)
	if err != nil || len(out[parent.ID].LifecycleImpacts) != 1 {
		t.Fatal(out, err)
	}
	req.LifecycleImpacts[0].Asset.Identity.NativeID = resourceID("Microsoft.Sql/servers", "unrelated") + "/databases/master"
	if out, err := c.resourceGroupProductRequests(req); err == nil || out != nil {
		t.Fatal("unrelated same-type service child accepted")
	}
}
