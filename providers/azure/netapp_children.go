package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const netappSubvolumeType = netappVolumeType + "/subvolumes"
const netappQuotaType = netappVolumeType + "/volumeQuotaRules"

func netappIndependentChild(kind string) bool {
	return kind == netappSubvolumeType || kind == netappQuotaType
}
func netappDirectLeaf(kind string) bool {
	return netappRecoveryKind(kind) || netappIndependentChild(kind)
}

// These APIs expose no immutable child UUID. Bind the native path or quota
// target plus systemData.createdAt when supplied, without inventing an identity
// field. Parent volume/pool UUIDs are checked independently. An identical child
// recreated without creation metadata is not distinguishable by this API.
func (c *client) netappLeafIdentity(kind string, raw map[string]any) (string, any) {
	p := object(raw["properties"])
	if netappIndependentChild(kind) {
		created := object(raw["systemData"])["createdAt"]
		return c.privateConfiguration(map[string]any{"id": strings.ToLower(text(raw["id"])), "path": p["path"], "parentPath": p["parentPath"], "quotaType": p["quotaType"], "quotaTarget": p["quotaTarget"], "created": created}), created
	}
	key := "created"
	if kind == netappBackupType {
		key = "creationDate"
	}
	return netappRecoveryIncarnation(kind, raw), p[key]
}

func netappIndependentChildReady(kind string, raw map[string]any) bool {
	p := object(raw["properties"])
	state := p["provisioningState"]
	if state != "Succeeded" && state != "Failed" && !(kind == netappSubvolumeType && state == nil) {
		return false
	}
	if raw["systemData"] != nil && object(raw["systemData"]) == nil {
		return false
	}
	if created := object(raw["systemData"])["createdAt"]; created != nil {
		if _, valid := netappRecoveryTime(created); !valid {
			return false
		}
	}
	if kind == netappSubvolumeType {
		path, ok := p["path"].(string)
		if !ok || path == "" {
			return false
		}
		if parent := p["parentPath"]; parent != nil {
			if _, ok := parent.(string); !ok {
				return false
			}
		}
		return true
	}
	target, ok := p["quotaTarget"].(string)
	if !ok {
		return false
	}
	switch p["quotaType"] {
	case "DefaultUserQuota", "DefaultGroupQuota":
		return target == ""
	case "IndividualUserQuota", "IndividualGroupQuota":
		return target != ""
	}
	return false
}

// Recovery points and volume-local leaves share the same durable native DELETE
// protocol. Keeping its existing proof namespace preserves in-flight receipts.
func (c *client) netappLeafDirectAllowed(value asset.Asset) bool {
	return netappDirectLeaf(value.Identity.NativeType) && value.Normalized["cleanup_protected"] != true && value.Normalized["cleanup_controller_only"] != true
}
