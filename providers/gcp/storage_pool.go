package gcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const storagePoolType = "compute.googleapis.com/StoragePool"

// Aggregate scope, native zone and resource identity must agree. A pool is a
// zonal Compute resource even though inventory groups zones into scan regions.
func (c *client) storagePoolIdentity(id string, data map[string]any, scope string) error {
	parts := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] != c.project || parts[2] != "zones" || parts[4] != "storagePools" || text(data["name"]) != parts[5] || scope != "zones/"+parts[3] || c.canonicalName(text(data["selfLink"])) != id || data["kind"] != "compute#storagePool" {
		return groupDenied("storage_pool_identity_changed")
	}
	if c.canonicalName(text(data["zone"])) != "//compute.googleapis.com/projects/"+c.project+"/zones/"+parts[3] {
		return groupDenied("storage_pool_zone_changed")
	}
	return nil
}

// storagePoolObservation is a pool's member reads, kept apart from the item so
// inventory can read several pools at once.
type storagePoolObservation struct {
	members []map[string]any
	planned []poolMember
}

func (c *client) storagePoolObserve(ctx context.Context, id string, data map[string]any) (storagePoolObservation, error) {
	members, err := c.storagePoolDisks(ctx, id)
	if err != nil {
		return storagePoolObservation{}, err
	}
	kind, _ := findType(storagePoolType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return storagePoolObservation{}, err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return storagePoolObservation{}, err
	}
	zone := "zones/" + last(text(data["zone"]))
	if err := c.storagePoolIdentity(id, live, zone); err != nil {
		return storagePoolObservation{}, err
	}
	// Reusing a pool name while paginating must not attach the replacement's
	// disks to the old creation. Mutable utilization need not be identical.
	if text(data["id"]) == "" || text(data["creationTimestamp"]) == "" || text(data["id"]) != text(live["id"]) || text(data["creationTimestamp"]) != text(live["creationTimestamp"]) {
		return storagePoolObservation{}, groupDenied("storage_pool_creation_changed")
	}
	planned, err := c.storagePoolMembers(id, members)
	if err != nil {
		return storagePoolObservation{}, err
	}
	if storagePoolConfiguration(data) != storagePoolConfiguration(live) {
		return storagePoolObservation{}, groupDenied("storage_pool_configuration_changed")
	}
	return storagePoolObservation{members, planned}, nil
}

// Member summaries are observations of disks, not child delete actions. Disk
// inventory remains authoritative for the disk kind and supplies its own edges.
func (c *client) applyStoragePool(item *contracts.InventoryItem, data map[string]any, observed storagePoolObservation) {
	rows := make([]any, 0, len(observed.members))
	for _, member := range observed.members {
		rows = append(rows, safePayload(member))
		id, _ := c.storagePoolDiskID(text(member["disk"]), last(text(data["zone"])))
		// Shared pool summaries may name another project's disks. Keep those
		// observations without expanding this connection's discovery boundary.
		if strings.HasPrefix(id, "//compute.googleapis.com/projects/"+c.project+"/") {
			item.NetworkReferences = append(item.NetworkReferences, id)
		}
	}
	encoded, _ := json.Marshal(observed.planned)
	proof := storagePoolConfiguration(data)
	item.Normalized[poolConfigurationKey] = proof
	item.Normalized[poolMembersKey] = string(encoded)
	item.Normalized[poolSnapshotKey] = infraManifestHash(proof, string(encoded))
	actionable := c.storagePoolProtection(data, observed.planned) == ""
	item.Actionable = &actionable
	item.Normalized["storage_pool_disks"] = rows
	item.Raw["storagePoolDisks"] = map[string]any{"items": rows}
}

func (c *client) storagePoolDisks(ctx context.Context, id string) ([]map[string]any, error) {
	kind, _ := findType(storagePoolType)
	_, parameters, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation("compute.storagePools.listDisks")
	rows, err := c.nativeList(ctx, operation, parameters, "items")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		disk, err := c.storagePoolDiskID(text(row["disk"]), text(parameters["zone"]))
		if err != nil || text(row["name"]) != last(disk) || seen[disk] {
			return nil, groupDenied("storage_pool_disk_identity_changed")
		}
		seen[disk] = true
	}
	sort.Slice(rows, func(i, j int) bool { return text(rows[i]["disk"]) < text(rows[j]["disk"]) })
	return rows, nil
}

// listDisks can include disks in projects sharing the pool. Validate their native
// identity without resolving credentials or making requests in those projects.
func (c *client) storagePoolDiskID(value, zone string) (string, error) {
	if strings.HasPrefix(value, "projects/") {
		value = "//compute.googleapis.com/" + value
	}
	id := c.canonicalName(value)
	prefix := "//compute.googleapis.com/"
	parts := strings.Split(strings.TrimPrefix(id, prefix), "/")
	if !strings.HasPrefix(id, prefix) || len(parts) != 6 || parts[0] != "projects" || parts[2] != "zones" || parts[3] != zone || parts[4] != "disks" {
		return "", groupDenied("storage_pool_disk_identity_changed")
	}
	for _, part := range parts {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return "", groupDenied("storage_pool_disk_identity_changed")
		}
	}
	return id, nil
}
