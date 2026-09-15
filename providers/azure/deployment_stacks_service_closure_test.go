package azure

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackServiceClosure(t *testing.T) {
	for _, mode := range []string{"implicit", "explicit", "direct", "unreviewed", "protected", "child_changed", "list_forbidden", "list_omits_implicit", "stack_changed", "retained_child", "retained_parent"} {
		t.Run(mode, func(t *testing.T) {
			parentKind, childKind, collection := vmType, vmExtensionType, "extensions"
			if mode == "direct" {
				parentKind, childKind, collection = hostGroupType, hostType, "hosts"
			}
			parent := actionAsset(parentKind, "parent")
			parent.Identity.Partition = "azure"
			child := parent
			child.ID, child.Identity.NativeType, child.Identity.NativeID = "child", childKind, parent.Identity.NativeID+"/"+collection+"/child"
			child.Normalized = map[string]any{"_arm_generation": "reviewed"}
			controller := parent.ID
			if mode == "explicit" || mode == "direct" {
				controller = "stack"
			}
			choices := []contracts.ActionImpact{{Asset: parent, ControllerID: "stack", Delete: true}}
			if mode != "unreviewed" {
				choices = append(choices, contracts.ActionImpact{Asset: child, ControllerID: controller, Delete: mode != "retained_child"})
			}
			if mode == "retained_parent" {
				for i := range choices {
					choices[i].Delete = false
				}
			}
			c, req := stackDeletePlanFixture(t, false, choices...)
			rows := []any{}
			for _, impact := range choices {
				if impact.ControllerID == "stack" {
					rows = append(rows, map[string]any{"id": impact.Asset.Identity.NativeID, "status": "managed", "denyStatus": "denyDelete"})
				}
			}
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": rows}}
			review, err := c.deploymentStackMemberReview(root)
			if err != nil {
				t.Fatal(err)
			}
			req.Asset.Normalized[deploymentStackReviewKey] = review
			req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)

			parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": parentKind, "location": "eastus", "properties": map[string]any{}}
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": childKind, "location": "eastus", "etag": "reviewed", "properties": map[string]any{}}
			child.Normalized["_arm_generation"] = productGeneration(childRaw)
			if mode == "protected" {
				childRaw["tags"] = map[string]any{"steward/protected": "true"}
			}
			rootReads, lists, childReads := 0, 0, 0
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("closure check mutated a resource")
				}
				path := strings.ToLower(r.URL.Path)
				switch path {
				case req.Asset.Identity.NativeID:
					rootReads++
					if mode == "stack_changed" && rootReads > 2 {
						object(root["systemData"])["createdAt"] = "2026-09-15T01:00:00Z"
					}
					return jsonResponse(200, root, nil), nil
				case parent.Identity.NativeID:
					return jsonResponse(200, parentRaw, nil), nil
				case child.Identity.NativeID:
					childReads++
					if mode == "child_changed" && childReads > 1 {
						childRaw["etag"] = "changed"
					}
					return jsonResponse(200, childRaw, nil), nil
				case parent.Identity.NativeID + "/" + collection:
					lists++
					if mode == "list_omits_implicit" {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
					}
					if mode == "list_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					return jsonResponse(200, map[string]any{"value": []any{childRaw}}, nil), nil
				default:
					t.Fatalf("unexpected closure endpoint %s", r.URL.Path)
					return nil, nil
				}
			})
			out, err := c.deploymentStackObserveServiceClosure(t.Context(), req)
			success := mode == "implicit" || mode == "explicit" || mode == "direct" || mode == "retained_parent"
			if !success {

				expected := map[string]string{"unreviewed": "deployment_stack_service_child_not_reviewed", "protected": "azure_protected_tag", "child_changed": "Azure resource generation changed", "stack_changed": "deployment_stack_live_incarnation_changed", "retained_child": "deployment_stack_retained_descendant_would_be_deleted", "list_omits_implicit": "deployment_stack_service_child_membership_changed"}[mode]
				if expected != "" && (err == nil || !strings.Contains(err.Error(), expected)) {
					t.Fatal("wrong rejection", mode, err)
				}
				if err == nil || len(out.Parents)+len(out.DirectChildren) != 0 {
					t.Fatal("invalid closure accepted", out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retained_parent" {
				if len(out.Parents)+len(out.DirectChildren)+lists != 0 {
					t.Fatal("retained parent acquired deletion effects", out)
				}
				return
			}
			if !slices.Equal(out.Parents, []asset.AssetID{parent.ID}) || rootReads != 4 || lists != 1 || childReads < 3 {
				t.Fatal(out, rootReads, lists, childReads)
			}
			if mode == "direct" {
				if !slices.Equal(out.Prerequisites[parent.ID], []asset.AssetID{child.ID}) {
					t.Fatal("native prerequisite parent edge missing", out)
				}
				if len(out.DirectChildren) != 1 || out.DirectChildren[0].Asset.ID != child.ID {
					t.Fatal("native Stack membership hid independent lifecycle", out)
				}
			} else if len(out.DirectChildren) != 0 {
				t.Fatal(out)
			}
		})
	}
}
