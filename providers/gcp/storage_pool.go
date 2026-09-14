package gcp

import "strings"

const storagePoolType = "compute.googleapis.com/StoragePool"

// Aggregate scope, native zone and resource identity must agree. A pool is a
// zonal Compute resource even though inventory groups zones into scan regions.
func (c *client) storagePoolIdentity(id string, data map[string]any, scope string) error {
	parts := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] != c.project || parts[2] != "zones" || parts[4] != "storagePools" || text(data["name"]) != parts[5] || scope != "zones/"+parts[3] || data["kind"] != "compute#storagePool" {
		return groupDenied("storage_pool_identity_changed")
	}
	if c.canonicalName(text(data["zone"])) != "//compute.googleapis.com/projects/"+c.project+"/zones/"+parts[3] {
		return groupDenied("storage_pool_zone_changed")
	}
	return nil
}
