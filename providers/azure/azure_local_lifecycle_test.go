package azure

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestAzureLocalVMPlanOrderingRetentionAndProtection(t *testing.T) {
	for _, mode := range []string{"ordinary", "retain-guest", "retain-metadata", "retain-os-disk", "retain-arc", "retain-all", "protected-guest", "protected-metadata", "protected-os-disk", "protected-arc", "guest-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.vmRequest(t)
			values := []asset.Asset{request.Asset}
			for _, impact := range append(request.LifecycleImpacts, request.PrerequisiteDeletions...) {
				values = append(values, impact.Asset)
			}
			contribution := governance.Contribution{}
			cascades := &serviceCascades{client: f.client, connectionID: "connection"}
			if err := cascades.contributeAzureLocalVMs(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 3 || len(contribution.Relationships) != 3 || len(contribution.Unresolved) != 0 {
				t.Fatal("VM lifecycle", contribution, err)
			}
			for _, binding := range contribution.Bindings {
				if binding.ControllerAssetID != request.Asset.ID || binding.Ownership != graph.OwnershipExclusive || binding.Authority != graph.AuthorityAuthoritative {
					t.Fatal("incorrect VM ownership", binding)
				}
			}
			input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{request.Asset.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			target := request.PrerequisiteDeletions[0].Asset
			if mode == "retain-metadata" || mode == "protected-metadata" {
				target = request.LifecycleImpacts[0].Asset
			}
			if mode == "retain-os-disk" || mode == "protected-os-disk" {
				target = request.LifecycleImpacts[1].Asset
			}
			if mode == "retain-arc" || mode == "protected-arc" {
				target = request.PrerequisiteDeletions[1].Asset
			}
			switch mode {
			case "retain-guest", "retain-metadata", "retain-os-disk", "retain-arc":
				input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_resources": []string{target.Identity.NativeID}}}
			case "retain-all":
				input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_all_resources": true}}
			case "protected-guest", "protected-metadata", "protected-os-disk", "protected-arc":
				input.Protections = []plan.ProtectionPolicy{{AssetID: target.ID, Protected: true}}
			case "guest-only":
				input.ResolvedAssetIDs = []asset.AssetID{target.ID}
			}
			payload, _ := json.Marshal(input)
			_ = json.Unmarshal(payload, &input)
			solved, err := plan.Solve(input)
			allowed := mode == "ordinary" || mode == "guest-only"
			if err != nil || (len(solved.Blockers) == 0) != allowed {
				t.Fatal("VM plan bypassed review", mode, err, solved.Blockers)
			}
			if mode == "guest-only" && (len(solved.Steps) != 1 || solved.Steps[0].AssetID != target.ID || len(solved.ImpactItems) != 0) {
				t.Fatal("guest selection removed its VM", solved)
			}
			if mode == "ordinary" {
				if len(solved.Steps) != 5 || len(solved.ImpactItems) != 2 {
					t.Fatal("VM plan omitted resources", solved)
				}
				vm := solved.Steps[len(solved.Steps)-1]
				prerequisites, err := plan.RequiredDeletions(vm)
				if err != nil || vm.AssetID != request.Asset.ID || len(prerequisites) != 3 {
					t.Fatal("Arc prerequisite snapshots missing", prerequisites, err)
				}
				for _, step := range solved.Steps[:len(solved.Steps)-1] {
					if !slices.Contains(vm.DependsOn, step.ID) {
						t.Fatal("VM may precede child cleanup", vm, step)
					}
				}
			}
		})
	}
}

func TestAzureLocalVMGraphRequiresKnownChildren(t *testing.T) {
	f := newLocalVMFixture(t)
	request := f.vmRequest(t)
	values := []asset.Asset{request.Asset}
	cascades := &serviceCascades{client: f.client, connectionID: "connection"}
	contribution := governance.Contribution{}
	if err := cascades.contributeAzureLocalVMs(t.Context(), values, &contribution); err != nil || len(contribution.Unresolved) != 6 || len(contribution.Bindings) != 0 {
		t.Fatal("missing child inventory was ignored", contribution, err)
	}
	for _, reference := range contribution.Unresolved {
		if reference.ControllerID != request.Asset.ID || reference.NativeID == "" || reference.Evidence["retention_supported"] != false {
			t.Fatal("unresolved child lost controller evidence", reference)
		}
	}
	for _, impact := range append(request.LifecycleImpacts, request.PrerequisiteDeletions...) {
		values = append(values, impact.Asset)
		delete(f.values, impact.Asset.Identity.NativeID)
	}
	delete(f.values, f.ids[azureLocalVMType])
	delete(f.values, f.ids[hybridMachineType])
	contribution = governance.Contribution{}
	if err := cascades.contributeAzureLocalVMs(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 3 || len(contribution.Relationships) != 3 {
		t.Fatal("own-absent resources lost frozen cleanup verification", contribution, err)
	}
}
