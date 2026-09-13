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
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDataMigrationIncomingTargetsRequireReviewedSources(t *testing.T) {
	f := newDataMigrationFixture(t)
	values := f.assets(t)
	for _, scenario := range []string{"SqlDb", "SqlMi", "SqlVm", "MongoRU", "MongoVCore"} {
		t.Run(scenario, func(t *testing.T) {
			id, _ := dataMigrationTarget(f.ids[scenario])
			_, kind, _ := parseID(id)
			if mapping, ok := findType(kind); ok {
				kind = mapping.NativeType
			}
			target := asset.Asset{ID: "target", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
			if kind == "Microsoft.DocumentDB/databaseAccounts" {
				target = dnsAsset(t, f.runtime, f.resources[id])
			}
			incoming, err := f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
			if err != nil || len(incoming[id]) != 1 || incoming[id][0].resource.id != f.ids[scenario] {
				t.Fatal("native migration target lost its independent incoming source", err, len(incoming[id]))
			}
			unindexed, err := f.client.contributeIncomingSources([]asset.Asset{target}, []asset.Asset{target}, incoming)
			if err != nil || len(unindexed.Unresolved) != 1 || unindexed.Unresolved[0].Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || unindexed.Unresolved[0].Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
				t.Fatal("unindexed migration did not block target cleanup", err, unindexed)
			}
			assets := append(slices.Clone(values), target)
			contribution, err := f.client.contributeIncomingSources([]asset.Asset{target}, assets, incoming)
			if err != nil || len(contribution.Relationships) != 1 || len(contribution.Bindings) != 0 {
				t.Fatal("migration acquired target ownership or lost prerequisite", err, contribution)
			}
			input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, Relationships: contribution.Relationships}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) == 0 {
				t.Fatal("target implicitly selected a migration", err)
			}
			input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, asset.AssetID(f.ids[scenario]))
			result, err = plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || result.Steps[0].AssetID != asset.AssetID(f.ids[scenario]) {
				t.Fatal("jointly selected migration did not precede target", err, result.Blockers)
			}
		})
	}
}

func TestDataMigrationIncomingSQLTargetDriverRecovery(t *testing.T) {
	f := newDataMigrationFixture(t)
	values := f.assets(t)
	migrationID := f.ids["SqlDb"]
	targetID, _ := dataMigrationTarget(migrationID)
	target := dnsAsset(t, f.runtime, f.resources[targetID])
	previous := f.override
	deletes := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "GET" && (path == targetID+"/databases" || path == targetID+"/elasticpools") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		if req.Method == "DELETE" && path == targetID {
			deletes++
			delete(f.resources, targetID)
			return jsonResponse(200, nil, nil), true
		}
		return previous(req)
	}
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	assets := append(slices.Clone(values), target)
	contribution, err := lifecycle.Contribute(t.Context(), "scope", assets)
	if err != nil {
		t.Fatal("registered lifecycle did not retain incoming migrations", err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
	if result, err := plan.Solve(input); err != nil || len(result.Blockers) == 0 {
		t.Fatal("full lifecycle lost the target-only cleanup blocker", result, err)
	}
	input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, asset.AssetID(migrationID))
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 || planned.Steps[0].AssetID != asset.AssetID(migrationID) {
		t.Fatal("full lifecycle lost migration-before-target ordering", planned, err)
	}
	plannedRequest := servicePlanRequest(planned, assets, target)
	if len(plannedRequest.PrerequisiteDeletions) != 1 || plannedRequest.PrerequisiteDeletions[0].Asset.Identity.NativeID != migrationID {
		t.Fatal("plan did not freeze native migration prerequisite", plannedRequest)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "native-dms-target"}
	if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || deletes != 0 {
		t.Fatal("unselected migration did not protect registered SQL target", err)
	}
	request.PrerequisiteDeletions = plannedRequest.PrerequisiteDeletions
	if _, err := driver.Execute(t.Context(), request); err == nil || deletes != 0 {
		t.Fatal("target bypassed a live reviewed migration")
	}
	raw := f.resources[migrationID]
	delete(f.resources, migrationID)
	payload, _ := json.Marshal(request)
	if json.Unmarshal(payload, &request) != nil {
		t.Fatal("target request restore failed")
	}
	driver, _ = f.runtime.ResolveAction(t.Context(), "connection", target)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || deletes != 1 {
		t.Fatal("target did not execute after recorded migration absence", err, deletes)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("target did not verify absence", wait, err)
	}
	// A late source cannot hide behind target 404 or the original receipt.
	for _, id := range []string{migrationID, migrationID + "-new"} {
		late := batchClone(raw)
		late["id"], late["name"] = id, last(id)
		f.resources[id], f.kinds[id] = late, dataMigrationType
		if wait, err := driver.Wait(t.Context(), request, result); err == nil || isNotFound(err) || wait.Done || deletes != 1 {
			t.Fatal("late migration was ignored after target deletion", wait, err)
		}
		delete(f.resources, id)
	}
}

func TestDataMigrationIncomingDatabaseAndConnectionBoundaries(t *testing.T) {
	f := newDataMigrationFixture(t)
	values := f.assets(t)
	for _, scenario := range []string{"SqlDb", "SqlMi"} {
		t.Run(scenario, func(t *testing.T) {
			id := f.ids[scenario]
			parent, _ := dataMigrationTarget(id)
			_, kind, _ := parseID(parent)
			target := asset.Asset{ID: "database", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind + "/databases", NativeID: parent + "/databases/" + last(id)}}
			incoming, err := f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
			if err != nil || len(incoming[target.Identity.NativeID]) != 1 || incoming[target.Identity.NativeID][0].resource.id != id {
				t.Fatal("native targetDbName did not protect its database", err)
			}
			for _, boundary := range []string{"connection", "partition"} {
				foreign := target
				if boundary == "connection" {
					foreign.Identity.ConnectionID = "other-connection"
				} else {
					foreign.Identity.Partition = "azure-public"
				}
				contribution, err := f.client.contributeIncomingSources([]asset.Asset{foreign}, values, incoming)
				if err != nil || len(contribution.Unresolved) != 1 || len(contribution.Relationships) != 0 {
					t.Fatal("foreign target borrowed migration source identity", boundary, contribution, err)
				}
			}
			target.Identity.NativeID += "-unrelated"
			incoming, err = f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
			if err != nil || len(incoming[target.Identity.NativeID]) != 0 {
				t.Fatal("migration incorrectly protected a different database", err)
			}
		})
	}
}

func TestDataMigrationIncomingKnownAndChangedSources(t *testing.T) {
	for _, mode := range []string{"source-omitted", "service-omitted", "source-gone", "parent-gone", "source-forbidden", "changed-source", "forged-proof", "changed-reference", "during-walk"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			values := f.assets(t)
			id := f.ids["SqlDb"]
			targetID, _ := dataMigrationTarget(id)
			target := dnsAsset(t, f.runtime, f.resources[targetID])
			switch mode {
			case "source-omitted":
				f.omitted[id] = true
			case "service-omitted":
				f.omitted[f.ids[dataMigrationSQLServiceType]] = true
			case "source-gone":
				delete(f.resources, id)
			case "parent-gone":
				delete(f.resources, f.ids[dataMigrationSQLServiceType])
			case "changed-source":
				f.resources[id]["futurePrivateSetting"] = "changed"
			case "forged-proof", "changed-reference":
				for i := range values {
					if values[i].Identity.NativeID == id {
						if mode == "forged-proof" {
							values[i].Normalized[dataMigrationProof] = "forged"
						} else {
							values[i].Normalized["_datamigration_references"] = map[string]any{}
						}
					}
				}
			case "source-forbidden", "during-walk":
				previous, before := f.override, f.calls["GET "+id]
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
						if mode == "source-forbidden" {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						if f.calls["GET "+id] > before+2 {
							f.resources[id]["futurePrivateSetting"] = "changed"
						}
					}
					return previous(req)
				}
			}
			incoming, err := f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
			if mode == "changed-source" && err == nil {
				_, err = f.client.contributeIncomingSources([]asset.Asset{target}, append(values, target), incoming)
			}
			switch mode {
			case "source-omitted", "service-omitted":
				if err != nil || len(incoming[targetID]) != 1 || incoming[targetID][0].resource.id != id {
					t.Fatal("native known source omission authorized target cleanup", err)
				}
			case "source-gone":
				if err != nil || len(incoming[targetID]) != 0 {
					t.Fatal("own source absence did not unblock independent target", err)
				}
			default:
				if err == nil || isNotFound(err) {
					t.Fatal("changed or unreadable migration dependency was accepted", mode, err)
				}
			}
		})
	}
}

func TestDataMigrationClassicReferencesProtectExternalAncestors(t *testing.T) {
	f := newDataMigrationFixture(t)
	id := f.ids[dataMigrationTaskType]
	server, _ := dataMigrationTarget(f.ids["SqlDb"])
	serverType := "Microsoft.Sql/servers"
	props := object(f.resources[id]["properties"])
	props["input"] = map[string]any{"sourceConnectionInfo": map[string]any{"type": "SqlConnectionInfo", "resourceId": server + "/databases/source"}, "script": resourceID(serverType, "unrelated")}
	values := f.assets(t)
	target := asset.Asset{ID: "server", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: serverType, NativeID: server}}
	incoming, err := f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
	if err != nil || len(incoming[server]) != 2 {
		t.Fatal("classic database reference lost its SQL server ancestor", err, len(incoming[server]))
	}
	target.Identity.NativeID = strings.ToLower(resourceID(serverType, "unrelated"))
	incoming, err = f.client.monitorIncomingTargets(t.Context(), []asset.Asset{target}, values...)
	if err != nil || len(incoming[target.Identity.NativeID]) != 0 {
		t.Fatal("opaque script introduced a migration dependency", err)
	}
}
