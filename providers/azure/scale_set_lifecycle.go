package azure

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const scaleSetType = "Microsoft.Compute/virtualMachineScaleSets"
const scaleSetVMType = scaleSetType + "/virtualMachines"
const scaleSetExtensionType = scaleSetType + "/extensions"
const scaleSetVMExtensionType = scaleSetVMType + "/extensions"
const scaleSetNICType = scaleSetVMType + "/networkInterfaces"
const scaleSetIPConfigType = scaleSetNICType + "/ipConfigurations"
const scaleSetPublicIPType = scaleSetIPConfigType + "/publicIPAddresses"

func scaleSetMode(properties map[string]any) (string, error) {
	switch strings.ToLower(text(properties["orchestrationMode"])) {
	case "", "uniform":
		return "Uniform", nil // Pre-Flexible resources omit the mode.
	case "flexible":
		return "Flexible", nil
	default:
		return "", fmt.Errorf("unrecognized Azure scale set orchestration mode")
	}
}

func (c *client) scaleSetChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	mode, err := scaleSetMode(object(raw["properties"]))
	if err != nil {
		return nil, err
	}
	if mode == "Uniform" {
		return c.nativeServiceChildren(ctx, parent, raw, []string{scaleSetVMType, scaleSetExtensionType})
	}
	children, err := c.nativeServiceChildren(ctx, parent, raw, []string{scaleSetExtensionType})
	if err != nil {
		return nil, err
	}
	members, err := c.flexibleScaleSetVMs(ctx, parent.NativeID)
	if err != nil {
		return nil, err
	}
	return append(children, members...), nil
}

// Flexible members use the standard VM APIs. Native scale-set deletion requires
// their prior deletion/removal, so these are independently executed children.
// https://learn.microsoft.com/troubleshoot/azure/virtual-machine-scale-sets/delete/vmss-operation-not-allowed
func (c *client) flexibleScaleSetVMs(ctx context.Context, parentID string) ([]serviceChild, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	definition, ok := runtime.productDefinition(vmType)
	if !ok || definition.Discovery.List == nil {
		return nil, fmt.Errorf("standard VM discovery is unavailable")
	}
	bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
	if err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(bound.URL)
	values, err := c.listAllURL(ctx, bound.URL, endpoint.Path)
	if err != nil {
		return nil, err
	}
	kind, _ := findType(vmType)
	seen := map[string]bool{}
	children := []serviceChild{}
	for _, value := range values {
		listed := object(value)
		id, parsed, err := parseID(text(listed["id"]))
		if err != nil || !strings.EqualFold(parsed, vmType) || !validResponseType(vmType, text(listed["type"])) || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate Flexible scale set VM identity")
		}
		seen[id] = true
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, id, vmType) {
			return nil, fmt.Errorf("Flexible scale set VM detail identity mismatch")
		}
		if err := serviceListedIncarnation(listed, live.data); err != nil {
			return nil, err
		}
		if strings.EqualFold(text(object(object(live.data["properties"])["virtualMachineScaleSet"])["id"]), parentID) {
			children = append(children, serviceChild{id: id, kind: vmType, data: live.data, direct: true})
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
	return children, nil
}

func uniformVMDisks(properties map[string]any) ([]string, error) {
	storage := object(properties["storageProfile"])
	os := object(storage["osDisk"])
	if os == nil {
		return nil, fmt.Errorf("Uniform scale set VM omitted its OS disk description")
	}
	disks := []map[string]any{}
	if !strings.EqualFold(text(object(os["diffDiskSettings"])["option"]), "Local") {
		disks = append(disks, os)
	}
	if storage["dataDisks"] != nil {
		values, ok := storage["dataDisks"].([]any)
		if !ok {
			return nil, fmt.Errorf("invalid Uniform scale set data disk collection")
		}
		luns := map[string]bool{}
		for _, value := range values {
			disk := object(value)
			lun := fmt.Sprint(disk["lun"])
			if disk == nil || disk["lun"] == nil || luns[lun] {
				return nil, fmt.Errorf("invalid or duplicate Uniform data disk LUN")
			}
			luns[lun] = true
			disks = append(disks, disk)
		}
	}
	ids := []string{}
	for _, disk := range disks {
		// Uniform scale-set disks share the VM's native lifetime. The scale-set
		// profile exposes Detach only for Flexible orchestration. Do not silently
		// reinterpret an unexpected policy, or delete unmodeled VHD blobs.
		if option := text(disk["deleteOption"]); option != "" && !strings.EqualFold(option, "Delete") {
			return nil, fmt.Errorf("Uniform scale set disk requires independent detachment before deletion")
		}
		if text(object(disk["vhd"])["uri"]) != "" {
			return nil, fmt.Errorf("Uniform scale set unmanaged VHD deletion is not modeled")
		}
		id, kind, err := parseID(text(object(disk["managedDisk"])["id"]))
		if err != nil || !strings.EqualFold(kind, diskType) || slices.Contains(ids, id) {
			return nil, fmt.Errorf("invalid or duplicate Uniform scale set disk identity")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (c *client) uniformVMChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	ids, err := uniformVMDisks(object(raw["properties"]))
	if err != nil {
		return nil, err
	}
	children, err := c.nativeServiceChildren(ctx, parent, raw, []string{scaleSetVMExtensionType, scaleSetNICType})
	if err != nil {
		return nil, err
	}
	nics := map[string]bool{}
	network := object(object(raw["properties"])["networkProfile"])
	if network["networkInterfaces"] != nil {
		values, ok := network["networkInterfaces"].([]any)
		if !ok {
			return nil, fmt.Errorf("invalid Uniform VM network interface collection")
		}
		for _, value := range values {
			id, kind, err := parseID(text(object(value)["id"]))
			if err != nil || !strings.EqualFold(kind, scaleSetNICType) || !strings.HasPrefix(id, strings.ToLower(parent.NativeID)+"/") || nics[id] {
				return nil, fmt.Errorf("invalid or duplicate Uniform VM NIC reference")
			}
			nics[id] = true
		}
	}
	for _, child := range children {
		if child.kind == scaleSetNICType {
			if !nics[child.id] || !strings.EqualFold(text(object(object(child.data["properties"])["virtualMachine"])["id"]), parent.NativeID) {
				return nil, fmt.Errorf("Uniform scale set NIC ownership mismatch")
			}
			delete(nics, child.id)
		}
	}
	if len(nics) != 0 {
		return nil, fmt.Errorf("Uniform scale set NIC membership changed")
	}
	kind, _ := findType(diskType)
	for _, id := range ids {
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(current, id, diskType) || !strings.EqualFold(text(current.data["managedBy"]), parent.NativeID) {
			return nil, fmt.Errorf("Uniform scale set disk ownership mismatch")
		}
		owners, valid := current.data["managedByExtended"].([]any)
		shares, _ := strconv.Atoi(fmt.Sprint(object(current.data["properties"])["maxShares"]))
		if !valid && (current.data["managedByExtended"] != nil || shares > 1) {
			return nil, fmt.Errorf("Uniform scale set disk ownership is incomplete")
		}
		for _, owner := range owners {
			if !strings.EqualFold(text(owner), parent.NativeID) {
				return nil, fmt.Errorf("Uniform scale set disk has another owner")
			}
		}
		children = append(children, serviceChild{id: id, kind: diskType, data: current.data})
	}
	return children, nil
}

func uniformVMDiskRelation(parent, child asset.Asset) bool {
	ids, err := uniformVMDisks(parent.Normalized)
	return err == nil && slices.Contains(ids, strings.ToLower(child.Identity.NativeID)) && strings.EqualFold(text(child.Normalized["managedBy"]), parent.Identity.NativeID)
}

func (a *action) validateScaleSetVMOwner(ctx context.Context, request contracts.ActionRequest, raw map[string]any) error {
	parentID := ""
	expectedMode := ""
	switch a.kind.NativeType {
	case vmType:
		parentID = text(object(object(raw["properties"])["virtualMachineScaleSet"])["id"])
		planned := text(object(request.Asset.Normalized["virtualMachineScaleSet"])["id"])
		if !strings.EqualFold(parentID, planned) {
			return serviceDenied("scale_set_vm_membership_changed")
		}
		if parentID == "" {
			return nil
		}
		expectedMode = "Flexible"
	case scaleSetVMType:
		parentID = strings.Join(strings.Split(a.id, "/")[:9], "/")
		expectedMode = "Uniform"
	default:
		return nil
	}
	kind, _ := findType(scaleSetType)
	endpoint, err := a.client.resourceURL(kind, parentID)
	if err != nil {
		return err
	}
	current, err := a.client.request(ctx, "GET", endpoint)
	if err != nil {
		return err
	}
	if !validResourceResponse(current, parentID, scaleSetType) {
		return fmt.Errorf("scale set VM parent identity mismatch")
	}
	mode, err := scaleSetMode(object(current.data["properties"]))
	if err != nil {
		return err
	}
	if mode != expectedMode {
		return serviceDenied("scale_set_vm_orchestration_changed")
	}
	return nil
}
