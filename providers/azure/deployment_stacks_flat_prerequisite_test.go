package azure

import (
	"encoding/json"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackFlatPrerequisiteScope(t *testing.T) {
	for _, mode := range []string{"completed", "not_completed", "different_parent", "retained_child", "different_product", "missing_membership", "other_controller", "different_connection"} {
		t.Run(mode, func(t *testing.T) {
			parent := actionAsset(hostGroupType, "parent")
			child := actionAsset(hostType, "child")
			child.Identity.NativeID = parent.Identity.NativeID + "/hosts/child"
			controller := asset.AssetID("stack")
			switch mode {
			case "different_parent":
				child.Identity.NativeID = resourceID(hostGroupType, "other") + "/hosts/child"
			case "different_product":
				child = actionAsset(diskType, "child")
			case "other_controller":
				controller = "not-reviewed"
			case "different_connection":
				child.Identity.ConnectionID = "other-connection"
			}
			c, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: parent, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: child, ControllerID: controller, Delete: mode != "retained_child"})
			req.IdempotencyKey = "flat-projection"
			if mode == "missing_membership" {
				delete(object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"]), child.Identity.NativeID)
			}
			before, _ := json.Marshal(req)
			completed := map[asset.AssetID]bool{child.ID: mode != "not_completed"}
			out, err := c.deploymentStackProductRequest(req, parent.ID, completed)
			switch mode {
			case "completed":
				if err != nil || len(out.PrerequisiteDeletions) != 1 || out.PrerequisiteDeletions[0].Asset.ID != child.ID || out.PrerequisiteDeletions[0].ControllerID != parent.ID || len(out.LifecycleImpacts) != 0 {
					t.Fatal("flat native prerequisite missing", out, err)
				}
			case "not_completed", "different_parent", "different_product":
				if err != nil || len(out.PrerequisiteDeletions) != 0 {
					t.Fatal("unrelated or incomplete member projected", out, err)
				}
			default:
				if err == nil && len(out.PrerequisiteDeletions) != 0 {
					t.Fatal("unreviewed scope projected", out, err)
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("projection changed frozen Stack request")
			}
		})
	}
}
