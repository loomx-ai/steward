package azure

import (
	"context"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// netappBackupIdle reads the native volume status, not an ARM resource or an
// asynchronous operation receipt. Only an explicit Idle state permits the next
// backup-policy mutation. Transferring is retryable; unavailable or ambiguous
// evidence must never authorize unassignment.
func (c *client) netappBackupIdle(ctx context.Context, id string) (bool, error) {
	if err := c.netappIdentity(id, netappVolumeType); err != nil {
		return false, err
	}
	res, err := c.request(ctx, "GET", apiURL(id+"/latestBackupStatus/current", netappVersion))
	if err != nil {
		return false, contracts.DependencyReadError(err)
	}
	if res.status != 200 || operationLocation(res.header) != "" || res.data["error"] != nil {
		return false, serviceDenied("invalid_netapp_backup_status")
	}
	switch res.data["relationshipStatus"] {
	case "Idle":
		return true, nil
	case "Transferring":
		return false, nil
	default:
		return false, serviceDenied("netapp_backup_status_unresolved")
	}
}
