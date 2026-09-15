package azure

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackPrerequisiteOrder(t *testing.T) {
	for _, mode := range []string{"shared", "reordered", "cycle", "self_cycle", "unverified_parent", "unverified_child", "duplicate_child", "unreferenced", "duplicate_candidate", "retained", "empty"} {
		t.Run(mode, func(t *testing.T) {
			impact := func(id asset.AssetID) contracts.ActionImpact {
				a := actionAsset(hostType, string(id))
				a.ID = id
				return contracts.ActionImpact{Asset: a, ControllerID: "original-stack-controller", Delete: true}
			}
			closure := deploymentStackServiceClosure{Parents: []asset.AssetID{"a", "b", "root"}, DirectChildren: []contracts.ActionImpact{impact("a"), impact("b"), impact("z")}, Prerequisites: map[asset.AssetID][]asset.AssetID{"root": {"a", "b"}, "a": {"z"}, "b": {"z"}}}
			switch mode {
			case "reordered":
				slices.Reverse(closure.Parents)
				slices.Reverse(closure.DirectChildren)
				slices.Reverse(closure.Prerequisites["root"])
			case "cycle":
				closure.Parents = append(closure.Parents, "z")
				closure.Prerequisites["z"] = []asset.AssetID{"a"}
			case "self_cycle":
				closure.Prerequisites["a"] = []asset.AssetID{"a"}
			case "unverified_parent":
				closure.Prerequisites["unknown"] = []asset.AssetID{"z"}
			case "unverified_child":
				closure.Prerequisites["root"] = []asset.AssetID{"unknown"}
			case "unreferenced":
				delete(closure.Prerequisites, "a")
				delete(closure.Prerequisites, "b")
			case "duplicate_child":
				closure.Prerequisites["root"] = []asset.AssetID{"a", "a"}
			case "duplicate_candidate":
				closure.DirectChildren = append(closure.DirectChildren, impact("z"))
			case "retained":
				closure.DirectChildren[0].Delete = false
			case "empty":
				closure = deploymentStackServiceClosure{}
			}
			before, _ := json.Marshal(closure)
			ordered, err := deploymentStackPrerequisiteOrder(closure)
			if mode == "shared" || mode == "reordered" {
				ids := []asset.AssetID{}
				for _, entry := range ordered {
					ids = append(ids, entry.Asset.ID)
					if entry.ControllerID != "original-stack-controller" {
						t.Fatal("order rewrote controller")
					}
				}
				if err != nil || !slices.Equal(ids, []asset.AssetID{"z", "a", "b"}) {
					t.Fatal("shared child not ordered first exactly once", ids, err)
				}
			} else if mode == "empty" {
				if err != nil || len(ordered) != 0 {
					t.Fatal(ordered, err)
				}
			} else if err == nil || ordered != nil {
				t.Fatal("invalid graph returned partial order", ordered, err)
			}
			after, _ := json.Marshal(closure)
			if string(before) != string(after) {
				t.Fatal("ordering mutated native evidence")
			}
		})
	}
}
