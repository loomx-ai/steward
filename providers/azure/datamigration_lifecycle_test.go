package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func (f *dataMigrationFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	values := []asset.Asset{}
	for _, kind := range []string{dataMigrationServiceType, dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType, dataMigrationSQLServiceType, dataMigrationMongoServiceType, dataMigrationType} {
		batch, err := f.list(t, kind)
		if err != nil {
			t.Fatal("native migration lifecycle inventory", kind, err)
		}
		for _, item := range batch.Items {
			value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Name: item.Name, Normalized: item.Normalized}
			if item.Actionable != nil && *item.Actionable {
				value.Capabilities = asset.CapabilitySet{asset.CapabilityActionable}
			}
			values = append(values, value)
		}
	}
	payload, _ := json.Marshal(values)
	if err := json.Unmarshal(payload, &values); err != nil {
		t.Fatal(err)
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if previous != nil {
			if res, ok := previous(req); ok {
				return res, true
			}
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	return values
}

func (f *dataMigrationFixture) schemaFileTask() {
	props := object(f.resources[f.ids[dataMigrationTaskType]]["properties"])
	props["taskType"] = "MigrateSchemaSqlServerSqlDb"
	props["input"] = map[string]any{"selectedDatabases": []any{map[string]any{"schemaSetting": map[string]any{"schemaOption": "UseStorageFile", "fileId": f.ids[dataMigrationFileType]}}}}
}

func TestDataMigrationNativeLifecycleAndPlans(t *testing.T) {
	f := newDataMigrationFixture(t)
	f.schemaFileTask()
	values := f.assets(t)
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil || len(contribution.Bindings) != 4 {
		t.Fatal("native migration lifecycle", err, len(contribution.Bindings))
	}
	for _, ref := range contribution.Unresolved {
		if ref.Relationship == graph.RelationshipAttachedTo || ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			t.Fatal("missing owned member or consumer", ref)
		}
	}
	for _, binding := range contribution.Bindings {
		if binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDirect || !binding.DirectCleanupAllowed || binding.Evidence["retention_supported"] != false {
			t.Fatal("classic member lost direct prerequisite", binding)
		}
		if f.kinds[string(binding.ManagedAssetID)] == dataMigrationType {
			t.Fatal("target-scoped migration became an owned service child")
		}
	}
	for _, test := range []struct {
		name     string
		selected []string
		steps    int
		blocked  bool
	}{
		{"classic-service", []string{dataMigrationServiceType}, 5, false},
		{"classic-project", []string{dataMigrationProjectType}, 3, false},
		{"task", []string{dataMigrationTaskType}, 1, false},
		{"service-task", []string{dataMigrationServiceTaskType}, 1, false},
		{"referenced-file", []string{dataMigrationFileType}, 1, true},
		{"file-and-task", []string{dataMigrationFileType, dataMigrationTaskType}, 2, false},
		{"sql-service", []string{dataMigrationSQLServiceType}, 1, true},
		{"sql-service-and-migrations", []string{dataMigrationSQLServiceType, "SqlDb", "SqlMi", "SqlVm"}, 4, false},
		{"mongo-service", []string{dataMigrationMongoServiceType}, 1, true},
		{"mongo-service-and-migrations", []string{dataMigrationMongoServiceType, "MongoRU", "MongoVCore"}, 3, false},
		{"sql-migration", []string{"SqlMi"}, 1, false},
		{"mongo-migration", []string{"MongoVCore"}, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ids := []asset.AssetID{}
			for _, key := range test.selected {
				ids = append(ids, asset.AssetID(f.ids[key]))
			}
			result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: ids, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || len(result.Steps) != test.steps || len(result.ImpactItems) != 0 || (len(result.Blockers) != 0) != test.blocked {
				t.Fatal("migration plan changed", err, len(result.Steps), len(result.ImpactItems), result.Blockers)
			}
			order := map[string]int{}
			for i, step := range result.Steps {
				id := string(step.AssetID)
				if dataMigrationKind(f.kinds[id]) == "" {
					t.Fatal("migration plan deleted independent target", id)
				}
				order[id] = i
			}
			if !test.blocked {
				if file, ok := order[f.ids[dataMigrationFileType]]; ok && file <= order[f.ids[dataMigrationTaskType]] {
					t.Fatal("schema file removed before consuming task", order)
				}
				for _, binding := range contribution.Bindings {
					parent, p := order[string(binding.ControllerAssetID)]
					child, c := order[string(binding.ManagedAssetID)]
					if p && (!c || child >= parent) {
						t.Fatal("native parent ran before child", order)
					}
				}
			}
		})
	}
	for _, mode := range []string{"retained", "protected"} {
		input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[dataMigrationServiceType])}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
		child := asset.AssetID(f.ids[dataMigrationFileType])
		if mode == "retained" {
			input.RequestOptions = map[asset.AssetID]map[string]any{asset.AssetID(f.ids[dataMigrationServiceType]): {"retain_resources": []string{string(child)}}}
		} else {
			input.Protections = []plan.ProtectionPolicy{{AssetID: child, Protected: true}}
		}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) == 0 {
			t.Fatal("classic parent bypassed retained/protected child", mode, err)
		}
	}
}

func TestDataMigrationGraphRequiresCurrentNativeContext(t *testing.T) {
	for _, mode := range []string{"proof", "connection", "partition", "location", "duplicate-native", "duplicate-asset", "private-child", "private-service", "target", "lock", "group", "node", "missing-child", "missing-migration", "omitted-child", "state-during-walk", "new-migration"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			values := f.assets(t)
			child, migration := f.ids[dataMigrationTaskType], f.ids["SqlDb"]
			switch mode {
			case "proof":
				values[0].Normalized[dataMigrationProof] = "forged"
			case "connection":
				values[0].Identity.ConnectionID = "other"
			case "partition":
				values[0].Identity.Partition = "other"
			case "location":
				values[0].Location = "eastus"
			case "duplicate-native":
				values = append(values, values[0])
			case "duplicate-asset":
				values[1].ID = values[0].ID
			case "private-child":
				f.resources[child]["futurePrivateSetting"] = "changed"
			case "private-service":
				f.resources[f.ids[dataMigrationMongoServiceType]]["futurePrivateSetting"] = "changed"
			case "target":
				target, _ := dataMigrationTarget(migration)
				object(f.resources[target]["properties"])["administratorLogin"] = "changed"
			case "lock":
				f.locks = []any{map[string]any{"id": child + "/providers/Microsoft.Authorization/locks/new", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "node":
				object(array(f.nodes[f.ids[dataMigrationSQLServiceType]]["nodes"])[0])["futurePrivateSetting"] = "changed"
			case "missing-child", "missing-migration":
				id := child
				if mode == "missing-migration" {
					id = migration
				}
				values = slices.DeleteFunc(values, func(value asset.Asset) bool { return value.Identity.NativeID == id })
			case "omitted-child":
				f.omitted[child], f.omitted[migration] = true, true
			case "state-during-walk":
				start := f.calls["GET "+child]
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.ToLower(req.URL.Path) == child && f.calls["GET "+child] > start+2 {
						object(f.resources[child]["properties"])["state"] = "Running"
					}
					return previous(req)
				}
			case "new-migration":
				id := strings.TrimSuffix(migration, "/database") + "/new"
				raw := batchClone(f.resources[migration])
				raw["id"], raw["name"] = id, "new"
				f.resources[id], f.kinds[id] = raw, dataMigrationType
			}
			lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
			contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
			if strings.HasPrefix(mode, "missing-") {
				id, relation := child, graph.RelationshipAttachedTo
				if mode == "missing-migration" {
					id, relation = migration, graph.RelationshipDependsOn
				}
				if err != nil || !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool { return ref.NativeID == id && ref.Relationship == relation }) {
					t.Fatal("missing selected resource lost unresolved prerequisite", mode, err)
				}
			} else if mode == "omitted-child" {
				if err != nil || len(contribution.Bindings) != 4 {
					t.Fatal("known own GET did not restore omitted child", err)
				}
			} else if err == nil {
				t.Fatal("changed migration context produced authoritative graph", mode)
			}
		})
	}
}
