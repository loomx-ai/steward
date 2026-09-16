package azure

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackFlatProductProjection(t *testing.T) {
	site, slot, function := actionAsset(appSiteType, "site"), actionAsset(appSlotType, "slot"), actionAsset(appSlotFunctionType, "function")
	slot.Identity.NativeID = site.Identity.NativeID + "/slots/slot"
	function.Identity.NativeID = slot.Identity.NativeID + "/functions/function"
	c, req := stackDeletePlanFixture(t, false,
		contracts.ActionImpact{Asset: function, ControllerID: "stack", Delete: true},
		contracts.ActionImpact{Asset: site, ControllerID: "stack", Delete: true},
		contracts.ActionImpact{Asset: slot, ControllerID: "stack", Delete: true})
	req.IdempotencyKey = "flat-product-projection"
	before, _ := json.Marshal(req)
	for _, parent := range []asset.Asset{site, slot} {
		member, err := c.deploymentStackMemberRequest(req, parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		controllers := map[asset.AssetID]asset.AssetID{}
		for _, impact := range member.LifecycleImpacts {
			controllers[impact.Asset.ID] = impact.ControllerID
		}
		expected := 1
		if parent.ID == site.ID {
			expected = 2
			if controllers[slot.ID] != site.ID {
				t.Fatal("flat slot lost native site controller", member)
			}
		}
		if len(controllers) != expected || controllers[function.ID] != slot.ID {
			t.Fatal("multi-level projection incomplete", member)
		}
		originalPayload, err := deploymentStackRequestPayload(member)
		if err != nil {
			t.Fatal(err)
		}
		reordered := req
		reordered.LifecycleImpacts = slices.Clone(req.LifecycleImpacts)
		slices.Reverse(reordered.LifecycleImpacts)
		again, err := c.deploymentStackMemberRequest(reordered, parent.ID)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := deploymentStackRequestPayload(again)
		if err != nil || payload != originalPayload {
			t.Fatal("projection depends on input order", again, err)
		}
		// Mutating the derived controller cannot rewrite a signed Stack impact.
		member.LifecycleImpacts[0].ControllerID = "changed"
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("projection rewrote original membership")
	}
}

func TestDeploymentStackFlatProductProjectionBoundaries(t *testing.T) {
	for _, mode := range []string{"unrelated", "wrong_kind", "missing_membership", "prerequisite", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			parent, child := actionAsset(sqlServerType, "parent"), actionAsset(sqlDatabaseType, "child")
			child.Identity.NativeID = parent.Identity.NativeID + "/databases/child"
			switch mode {
			case "unrelated":
				child.Identity.NativeID = resourceID(sqlServerType, "other") + "/databases/child"
			case "wrong_kind":
				child = actionAsset(diskType, "child")
			case "prerequisite":
				parent = actionAsset(hostGroupType, "parent")
				child = actionAsset(hostType, "child")
				child.Identity.NativeID = parent.Identity.NativeID + "/hosts/child"
			case "ambiguous":
				parent = actionAsset(privateDNSZoneType, "zone")
				child = actionAsset(privateDNSZoneType+"/A", "record")
				child.Identity.NativeID = parent.Identity.NativeID + "/a/record"
				child.Normalized = map[string]any{"isAutoRegistered": true}
			}
			impacts := []contracts.ActionImpact{{Asset: parent, ControllerID: "stack", Delete: true}, {Asset: child, ControllerID: "stack", Delete: true}}
			if mode == "ambiguous" {
				link := actionAsset(privateDNSLinkType, "link")
				link.Identity.NativeID = parent.Identity.NativeID + "/virtualnetworklinks/link"
				link.Normalized = map[string]any{"registrationEnabled": true}
				impacts = append(impacts, contracts.ActionImpact{Asset: link, ControllerID: "stack", Delete: true})
			}
			c, req := stackDeletePlanFixture(t, false, impacts...)
			req.IdempotencyKey = "flat-product-boundary"
			if mode == "missing_membership" {
				delete(object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"]), child.Identity.NativeID)
			}
			before, _ := json.Marshal(req)
			member, err := c.deploymentStackMemberRequest(req, parent.ID)
			if mode == "ambiguous" || mode == "missing_membership" {
				if err == nil {
					t.Fatal("unverified projection accepted", member)
				}
				if mode == "ambiguous" && !strings.Contains(err.Error(), "ambiguous_deployment_stack_product_controller") {
					t.Fatal("wrong ambiguity rejection", err)
				}
			} else if err != nil || len(member.LifecycleImpacts) != 0 {
				t.Fatal("unrelated or independent resource acquired cascade controller", member, err)
			}
			if mode == "prerequisite" {
				product, err := c.deploymentStackProductRequest(req, parent.ID, map[asset.AssetID]bool{child.ID: true})
				if err != nil || len(product.LifecycleImpacts) != 0 || len(product.PrerequisiteDeletions) != 1 || product.PrerequisiteDeletions[0].ControllerID != parent.ID {
					t.Fatal("independent prerequisite lost", product, err)
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("boundary check changed signed plan")
			}
		})
	}
}
