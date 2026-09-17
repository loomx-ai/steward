package azure

import (
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func recoveryHanaSnapshot(f *recoveryItemActionFixture) string {
	id := f.container + "/protecteditems/saphanadbinstance;hdb"
	database := object(f.objects[f.item]["properties"])
	database["parentName"], database["serverName"] = "hdb", "hana-host"
	raw := batchClone(f.objects[f.item])
	raw["id"], raw["name"] = id, last(id)
	p := object(raw["properties"])
	p["protectedItemType"], p["friendlyName"] = recoveryHanaInstance, "HDB"
	delete(p, "parentName")
	f.objects[id] = raw
	return id
}

func TestRecoveryHanaPrerequisiteIdentity(t *testing.T) {
	for _, mode := range []string{"related", "other-instance", "other-server", "stopped", "retained", "retained-transition", "missing-parent", "missing-server", "known-omitted", "known-absent", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryItemActionFixture(t)
			snapshot := recoveryHanaSnapshot(f)
			p := object(f.objects[snapshot]["properties"])
			known := map[string]any{}
			switch mode {
			case "other-instance":
				p["friendlyName"] = "other"
			case "other-server":
				p["serverName"] = "other"
			case "stopped":
				p["protectionState"] = "ProtectionStopped"
			case "retained":
				p["isScheduledForDeferredDelete"] = true
				p["protectionState"] = "ProtectionStopped"
			case "retained-transition":
				p["isScheduledForDeferredDelete"] = true
			case "missing-parent":
				delete(object(f.objects[f.item]["properties"]), "parentName")
			case "missing-server":
				delete(p, "serverName")
			case "known-omitted":
				f.omitted[snapshot] = true
				known[snapshot] = map[string]any{}
			case "known-absent":
				delete(f.objects, snapshot)
				known[snapshot] = map[string]any{}
			case "ambiguous":
				other := snapshot + "-other"
				raw := batchClone(f.objects[snapshot])
				raw["id"], raw["name"] = other, last(other)
				f.objects[other] = raw
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			deps, _, err := c.recoveryItemPrerequisites(t.Context(), f.item, f.objects[f.item], known)
			if mode == "missing-parent" || mode == "missing-server" || mode == "ambiguous" {
				if err == nil {
					t.Fatal("ambiguous HANA relation accepted")
				}
				return
			}
			want := 0
			if mode == "related" || mode == "known-omitted" || mode == "retained-transition" {
				want = 1
			}
			if err != nil || len(deps) != want {
				t.Fatal("native HANA dependency", len(deps), want, err)
			}
		})
	}
}

func TestRecoveryHanaGraphExplicitOrderingAndDriverGuard(t *testing.T) {
	f := newRecoveryItemActionFixture(t)
	snapshotID := recoveryHanaSnapshot(f)
	batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f.recoveryServicesFixture, recoveryServicesItem))
	if err != nil || len(batch.Items) != 2 {
		t.Fatal("native HANA inventory", err)
	}
	var values []asset.Asset
	var database, snapshot asset.Asset
	for _, item := range batch.Items {
		value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: recoveryServicesItem, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
		values = append(values, value)
		if item.NativeID == f.item {
			database = value
		}
		if item.NativeID == snapshotID {
			snapshot = value
		}
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := c.contributeRecoveryItemPrerequisites(t.Context(), "connection", database, values)
	if err != nil || len(contribution.Relationships) != 1 || len(contribution.Bindings) != 0 {
		t.Fatal("missing independent prerequisite", err)
	}
	edge := contribution.Relationships[0]
	if edge.SourceAssetID != database.ID || edge.TargetAssetID != snapshot.ID || edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
		t.Fatal("HANA source was selected implicitly")
	}
	missing, err := c.contributeRecoveryItemPrerequisites(t.Context(), "connection", database, []asset.Asset{database})
	if err != nil || len(missing.Unresolved) != 1 || !missing.Unresolved[0].BlocksCleanup {
		t.Fatal("unscanned snapshot not blocking", err)
	}
	for _, selected := range [][]asset.AssetID{{database.ID}, {snapshot.ID}, {database.ID, snapshot.ID}} {
		solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: selected, Relationships: contribution.Relationships})
		if err != nil {
			t.Fatal(err)
		}
		if len(selected) == 1 && selected[0] == database.ID {
			if len(solved.Blockers) == 0 {
				t.Fatal("database alone bypassed snapshot prerequisite")
			}
			continue
		}
		if len(solved.Blockers) != 0 || len(solved.Steps) != len(selected) || len(solved.ImpactItems) != 0 {
			t.Fatal("independent HANA selection changed")
		}
		if len(selected) == 2 {
			var first, second plan.CleanupTaskStep
			for _, step := range solved.Steps {
				if step.AssetID == snapshot.ID {
					first = step
				}
				if step.AssetID == database.ID {
					second = step
				}
			}
			if !slices.Contains(second.DependsOn, first.ID) || len(first.DependsOn) != 0 {
				t.Fatal("database scheduled before snapshot")
			}
		}
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", database)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: database, Action: "delete", IdempotencyKey: "hana-database"}
	if _, err = driver.Execute(t.Context(), request); err == nil || f.deletes != 0 {
		t.Fatal("missing selected prerequisite bypassed")
	}
	request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: snapshot, ControllerID: database.ID, Delete: true}}
	if _, err = driver.Execute(t.Context(), request); err == nil || f.deletes != 0 {
		t.Fatal("active snapshot protection bypassed")
	}
	object(f.objects[snapshotID]["properties"])["protectionState"] = "ProtectionStopped"
	if _, err = driver.Execute(t.Context(), request); err != nil || f.deletes != 1 {
		t.Fatal("stopped snapshot did not release database deletion", err)
	}
}
