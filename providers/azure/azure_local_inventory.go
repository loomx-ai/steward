package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const azureLocalNetworkConfiguration = "azure-local-network:"
const azureLocalRootPrefix = "azure-local-root:"

func (r *Runtime) azureLocalSnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	kind := azureLocalKind(request.ResourceKind.NativeType)
	networkDisks := kind == azureLocalDiskType && request.NetworkTarget != nil
	independent := azureLocalIndependent(kind)
	networkVMs := map[string]bool{}
	values, machines, instances := map[string]map[string]any{}, map[string]map[string]any{}, map[string]map[string]any{}
	known, provenance := map[string]bool{}, ""
	read := func(id, typ string) (map[string]any, error) {
		var res response
		var err error
		if typ == hybridMachineType {
			res, err = c.hybridComputeRead(ctx, id, typ)
		} else {
			res, err = c.azureLocalRead(ctx, id, typ)
		}
		if res.requestID != "" {
			provenance = res.requestID
		}
		return res.data, err
	}
	for _, id := range request.KnownNativeIDs {
		canonical, err := c.azureLocalIdentity(id, kind)
		if err != nil || id != canonical || known[id] {
			return nil, nil, "", serviceDenied("invalid_azure_local_known_identity")
		}
		known[id] = true
		raw, err := read(id, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, "", err
		}
		values[id] = raw
		if prior := request.KnownNativeMetadata[id]; independent && len(prior) != 0 {
			value := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: request.ConnectionID, Partition: "azure", NativeID: id, NativeType: kind}, Location: text(object(prior[azureLocalCleanup])["location"]), Normalized: prior}
			if _, err := c.azureLocalRecordedReferences(value); err != nil {
				return nil, nil, "", err
			}
			if azureLocalRootConfiguration(text(prior["_azure_local_configuration"])) || prior[azureLocalCleanup] != nil || prior[azureLocalCleanupProof] != nil {
				if err := c.azureLocalRootRecord(value); err != nil {
					return nil, nil, "", err
				}
				for _, vm := range stringValues(object(prior[azureLocalCleanup])["vms"]) {
					networkVMs[vm] = true
				}
			}
		}

		if prior := request.KnownNativeMetadata[id]; kind == azureLocalDiskType && strings.HasPrefix(text(prior["_azure_local_configuration"]), azureLocalNetworkConfiguration) {
			vms := append([]string{}, stringValues(prior["_azure_local_network_vms"])...)
			expected := c.privateConfiguration(map[string]any{"id": id, "connection": request.ConnectionID, "configuration": prior["_azure_local_configuration"], "vms": vms})
			if prior["_azure_local_network_binding"] != expected {
				return nil, nil, "", serviceDenied("invalid_azure_local_network_membership")
			}
			for _, vm := range vms {
				networkVMs[vm] = true
			}
			networkDisks = true
		}
		if machine := azureLocalMachine(id); machine != "" {
			if machines[machine] == nil {
				machines[machine], err = read(machine, hybridMachineType)
				if err != nil {
					return nil, nil, "", err
				}
			}
			if kind != azureLocalVMType {
				parent := azureLocalParent(id, kind)
				if instances[parent] == nil {
					instances[parent], err = read(parent, azureLocalVMType)
					if err != nil {
						return nil, nil, "", err
					}
				}
			}
		}
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return nil, nil, "", serviceDenied("unrelated_azure_local_known_metadata")
		}
	}
	for _, vm := range slices.Sorted(maps.Keys(networkVMs)) {
		canonical, err := c.azureLocalIdentity(vm, azureLocalVMType)
		if err != nil || vm != canonical {
			return nil, nil, "", serviceDenied("invalid_azure_local_network_vm")
		}
		raw, err := read(vm, azureLocalVMType)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, "", err
		}
		machine := azureLocalMachine(vm)
		if machines[machine] == nil {
			machines[machine], err = read(machine, hybridMachineType)
			if err != nil {
				return nil, nil, "", err
			}
		}
		instances[vm] = raw
	}
	collect := func(typ, parent string, target map[string]map[string]any) error {
		path := c.root() + "/providers/" + typ
		if typ == azureLocalVMType {
			path = parent + "/providers/" + azureLocalVMType
		} else if parent != "" {
			path = parent + "/" + last(typ)
		}
		version := azureLocalVersion
		if typ == hybridMachineType {
			version = hybridComputeVersion
		}
		rows, err := c.unfilteredARMIndex(ctx, path, version)
		if isNotFound(err) && parent != "" {
			rows, err = nil, nil
		}
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range rows {
			raw := object(row)
			var id string
			if typ == hybridMachineType {
				id, err = c.hybridComputeIdentity(text(raw["id"]), typ)
			} else {
				id, err = c.azureLocalIdentity(text(raw["id"]), typ)
			}
			if err != nil || seen[id] || azureLocalParent(id, typ) != parent || !strings.EqualFold(text(raw["type"]), typ) {
				return serviceDenied("invalid_azure_local_index_member")
			}
			seen[id] = true
			live, err := read(id, typ)
			if err != nil {
				return err
			}
			if serviceListedIncarnation(raw, live) != nil || !nativeConfigurationContains(raw, live) {
				return serviceDenied("azure_local_index_changed")
			}
			if prior := target[id]; prior != nil && c.privateConfiguration(prior) != c.privateConfiguration(live) {
				return serviceDenied("azure_local_known_resource_changed")
			}
			target[id] = live
		}
		if parent != "" {
			// These are native singletons. Probe their fixed ID even when an
			// empty/missing index omits a resource not seen in an earlier scan.
			id := strings.ToLower(path + "/default")
			if !seen[id] {
				raw, err := read(id, typ)
				if isNotFound(err) && target[id] == nil {
					return nil
				}
				if err != nil {
					return err
				}
				if prior := target[id]; prior != nil && c.privateConfiguration(prior) != c.privateConfiguration(raw) {
					return serviceDenied("azure_local_known_resource_changed")
				}
				target[id] = raw
			}
		}
		return nil
	}
	extension := kind == azureLocalVMType || kind == azureLocalAgentType || kind == azureLocalIdentityType
	if extension || networkDisks || independent && !azureLocalImage(kind) {
		if err := collect(hybridMachineType, "", machines); err != nil {
			return nil, nil, "", err
		}
		if kind == azureLocalVMType {
			instances = values
		}
		for _, machine := range slices.Sorted(maps.Keys(machines)) {
			if err := collect(azureLocalVMType, machine, instances); err != nil {
				return nil, nil, "", err
			}
		}
		if kind != azureLocalVMType && !networkDisks && !independent {
			for _, instance := range slices.Sorted(maps.Keys(instances)) {
				if err := collect(kind, instance, values); err != nil {
					return nil, nil, "", err
				}
			}
		}
	}
	if !extension {
		if err := collect(kind, "", values); err != nil {
			return nil, nil, "", err
		}
	}
	diskVMs := map[string][]string{}
	if networkDisks {
		for _, vm := range slices.Sorted(maps.Keys(instances)) {
			refs, err := azureLocalReferences(vm, azureLocalVMType, instances[vm])
			if err != nil {
				return nil, nil, "", err
			}
			for _, disk := range refs[azureLocalDiskType] {
				diskVMs[disk] = append(diskVMs[disk], vm)
			}
		}
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(values)) {
		raw := values[id]
		location := resourceRegion(raw)
		machine := azureLocalMachine(id)
		if machine != "" {
			if machines[machine] == nil {
				return nil, nil, "", serviceDenied("azure_local_machine_missing")
			}
			location = resourceRegion(machines[machine])
			if text(raw["location"]) != "" && resourceRegion(raw) != location {
				return nil, nil, "", serviceDenied("azure_local_extension_location_changed")
			}
			if kind != azureLocalVMType {
				parent := instances[azureLocalParent(id, kind)]
				if parent == nil || text(parent["location"]) != "" && resourceRegion(parent) != location {
					return nil, nil, "", serviceDenied("azure_local_instance_location_changed")
				}
			}
		}
		refs, err := azureLocalReferences(id, kind, raw)
		if err != nil {
			return nil, nil, "", err
		}
		safe := safePayload(object(azureLocalSafeValue(raw)))
		normalized := maps.Clone(object(safe["properties"]))
		normalized["name"], normalized["tags"], normalized["state"] = safe["name"], safe["tags"], object(safe["properties"])["provisioningState"]
		normalized["subscription_id"], normalized["resource_group"], normalized["_inventory_source"] = c.subscription, strings.Split(id, "/")[4], azureLocalSource
		configuration := c.privateConfiguration(map[string]any{"resource": raw, "machine": machines[machine], "instance": instances[azureLocalParent(id, kind)]})
		if kind == azureLocalVMType {
			configuration = azureLocalVMPrefix + configuration
		}
		if independent {
			configuration = azureLocalRootPrefix + configuration
		}
		if networkDisks {
			configuration = azureLocalNetworkConfiguration + configuration
			vms := append([]string{}, diskVMs[id]...)
			normalized["_azure_local_network_vms"] = vms
			normalized["_azure_local_network_binding"] = c.privateConfiguration(map[string]any{"id": id, "connection": request.ConnectionID, "configuration": configuration, "vms": vms})
		}
		bindings[id], normalized["_azure_local_configuration"] = configuration, configuration
		recorded, network := map[string]any{}, []string{}
		for typ, ids := range refs {
			recorded[typ], normalized[referenceKey(typ)] = ids, ids
			network = append(network, ids...)
		}
		// VM attachment locates a disk within a network scan. It does not turn
		// the disk's independent reference graph into a reverse ownership edge.
		network = append(network, diskVMs[id]...)
		slices.Sort(network)
		normalized["_azure_local_references"] = recorded
		normalized["_azure_local_reference_binding"] = c.privateConfiguration(map[string]any{"id": id, "connection": request.ConnectionID, "configuration": configuration, "references": refs})
		// Independent resources retain separate cleanup lifecycles.
		actionable := false
		if kind == azureLocalAgentType || kind == azureLocalIdentityType {
			vm := instances[azureLocalParent(id, kind)]
			reason := azureLocalGuestProtection(raw, vm, machines[machine])
			state := map[string]any{"resource": c.privateConfiguration(azureLocalCleanupSnapshot(raw)), "vm": c.privateConfiguration(azureLocalCleanupSnapshot(vm)), "machine": c.privateConfiguration(hybridComputeParentStamp(machines[machine])), "etag": c.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}), "inventory": configuration, "protected": reason != ""}
			normalized[azureLocalCleanup], normalized[azureLocalCleanupProof] = state, c.azureLocalCleanupBinding(id, request.ConnectionID, location, state)
			normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
			actionable = kind == azureLocalAgentType && reason == ""
		}
		if independent {
			reason := protectionReason(resourceType{NativeType: kind}, raw)
			state := map[string]any{"resource": c.privateConfiguration(azureLocalCleanupSnapshot(raw)), "etag": c.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}), "inventory": configuration, "protected": reason != "", "location": location}
			vms := maps.Clone(networkVMs)
			for vm := range instances {
				vms[vm] = true
			}
			state["vms"] = append([]string{}, slices.Sorted(maps.Keys(vms))...)
			bindings[id], actionable = c.privateConfiguration(state), reason == ""
			normalized[azureLocalCleanup], normalized[azureLocalCleanupProof] = state, c.azureLocalRootBinding(id, request.ConnectionID, state)
			normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
		}
		if kind == azureLocalVMType {
			for _, value := range array(object(object(raw["properties"])["storageProfile"])["dataDisks"]) {
				if azureLocalOSDisk(raw) != "" && strings.EqualFold(text(object(value)["id"]), azureLocalOSDisk(raw)) {
					return nil, nil, "", serviceDenied("azure_local_os_disk_also_data_disk")
				}
			}
			hints := map[string]any{}
			if prior := request.KnownNativeMetadata[id]; strings.HasPrefix(text(prior["_azure_local_configuration"]), azureLocalVMPrefix) || prior[azureLocalCleanup] != nil || prior[azureLocalCleanupProof] != nil {
				if err := c.azureLocalVMRecorded(id, request.ConnectionID, prior); err != nil {
					return nil, nil, "", err
				}
				hints = object(object(prior[azureLocalCleanup])["members"])
			}
			children, err := c.azureLocalVMChildren(ctx, id, hints, true, location, azureLocalOSDisk(raw))
			if err != nil {
				return nil, nil, "", err
			}
			reason := azureLocalGuestProtection(raw, raw, machines[machine])
			state := map[string]any{"resource": c.privateConfiguration(azureLocalCleanupSnapshot(raw)), "machine": c.privateConfiguration(hybridComputeParentStamp(machines[machine])), "etag": c.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}), "members": c.azureLocalVMMembers(children), "os_disk": azureLocalOSDisk(raw), "vms": slices.Sorted(maps.Keys(instances)), "protected": reason != "", "inventory": configuration, "location": location}
			normalized[azureLocalCleanup], normalized[azureLocalCleanupProof] = state, c.azureLocalVMBinding(id, request.ConnectionID, state)
			normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
			bindings[id], actionable = c.privateConfiguration(state), reason == ""
		}
		tags := map[string]string{}
		for k, v := range object(safe["tags"]) {
			if str, ok := v.(string); ok {
				tags[k] = str
			}
		}
		item := contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: text(raw["name"]), State: text(normalized["state"]), Tags: tags, Location: location, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Raw: safe, Normalized: normalized, Actionable: &actionable, NativeAliases: []string{id, text(raw["id"])}, NetworkReferences: slices.Compact(network)}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	bindings["parents"] = c.privateConfiguration(map[string]any{"machines": machines, "instances": instances})
	return items, bindings, provenance, nil
}

func (r *Runtime) listAzureLocal(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != azureLocalSource || request.ResourceKind == nil || azureLocalKind(request.ResourceKind.NativeType) == "" || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeRegion || request.Scope.Kind == asset.ScopeRegion && request.Scope.NativeID == "" {
		return batch, serviceDenied("invalid_azure_local_inventory_request")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("azure_local_cursor_too_large")
		}
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_azure_local_cursor")
		}
	}
	items, before, provenance, err := r.azureLocalSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.azureLocalSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("azure_local_inventory_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("azure_local_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if batch.Complete {
		for _, id := range request.KnownNativeIDs {
			if before[id] == nil {
				batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id)
			}
		}
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
