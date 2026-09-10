package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDiagnosticNativeReferencesRequireIndependentCleanup(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(map[bool]string{false: "resource", true: "subscription"}[subscription], func(t *testing.T) {
			f := newDiagnosticFixture(t)
			id := slices.Sorted(maps.Keys(f.settings))[1]
			if subscription {
				id = "/subscriptions/" + testSubscription + "/providers/microsoft.insights/diagnosticsettings/subscription-setting"
			}
			raw := f.settings[id]
			clear(f.settings)
			f.settings[id] = raw
			props := object(raw["properties"])
			base := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/"
			props["storageAccountId"] = base + "Microsoft.Storage/storageAccounts/archive"
			props["workspaceId"] = base + "Microsoft.OperationalInsights/workspaces/logs"
			props["eventHubAuthorizationRuleId"] = base + "Microsoft.EventHub/namespaces/events/authorizationRules/diagnostic"
			props["eventHubName"] = "platform"
			props["marketplacePartnerId"] = base + "Microsoft.Datadog/monitors/partner"
			props["serviceBusRuleId"] = base + "Microsoft.ServiceBus/namespaces/legacy/authorizationRules/diagnostic"
			source := f.asset(t, id)
			refs, err := diagnosticReferences(id, raw)
			if err != nil {
				t.Fatal(err)
			}
			assets := []asset.Asset{source}
			for kind, ids := range refs {
				for _, id := range ids {
					assets = append(assets, asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, Location: "westus", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
				}
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			contribution, err := c.contributeDiagnosticReferences(t.Context(), source, assets)
			if err != nil || len(contribution.Bindings)+len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 2*(len(assets)-1) {
				t.Fatal("native diagnostic references lost an ancestor/destination or acquired ownership", contribution, err)
			}
			wire, _ := json.Marshal(contribution.Relationships)
			var relationships []graph.Relationship
			if json.Unmarshal(wire, &relationships) != nil {
				t.Fatal("diagnostic graph could not survive persistence")
			}
			for _, relationship := range relationships {
				if relationship.Type == graph.RelationshipDependsOn && (relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || relationship.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] != nil) {
					t.Fatal("diagnostic prerequisite acquired automatic selection or cascade authority", relationship)
				}
			}
			for _, target := range assets[1:] {
				alone, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{target.ID}})
				if err != nil || len(alone.Blockers) == 0 {
					t.Fatal("retained diagnostic setting did not protect its target/ancestor", target.Identity, alone, err)
				}
				selected, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{source.ID, target.ID}})
				if err != nil || len(selected.Blockers) != 0 || len(selected.Steps) != 2 || selected.Steps[0].AssetID != source.ID || !slices.Contains(selected.Steps[1].DependsOn, selected.Steps[0].ID) {
					t.Fatal("reviewed diagnostic setting was not ordered before its target/ancestor", target.Identity, selected, err)
				}
			}
			alone, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{source.ID}})
			if err != nil || len(alone.Blockers)+len(alone.ImpactItems) != 0 || len(alone.Steps) != 1 || alone.Steps[0].AssetID != source.ID {
				t.Fatal("diagnostic deletion selected a source or shared destination", alone, err)
			}
		})
	}
}

// Compose the original diagnostic properties with one local storage target.
// The broad ARM index contains the log source; it deliberately omits the shared
// destination so incoming discovery cannot depend on that destination's index.
func diagnosticStorageTarget(t *testing.T) (*diagnosticFixture, asset.Asset, asset.Asset, *bool, *[]string) {
	t.Helper()
	f := newDiagnosticFixture(t)
	id := slices.Sorted(maps.Keys(f.settings))[1]
	raw := f.settings[id]
	clear(f.settings)
	f.settings[id] = raw
	storage := nativeResource(storageType, "diagnosticarchive", "westus", map[string]any{"provisioningState": "Succeeded"})
	storage["kind"] = "StorageV2"
	targetID := strings.ToLower(text(storage["id"]))
	object(raw["properties"])["storageAccountId"] = targetID
	gone, deletes := false, []string{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		for _, collection := range []string{"blobservices/default/containers", "fileservices/default/shares", "queueservices/default/queues", "tableservices/default/tables"} {
			if strings.EqualFold(req.URL.Path, targetID+"/"+collection) {
				includeDeleted := strings.HasSuffix(collection, "/containers") || strings.HasSuffix(collection, "/shares")
				if req.Method != "GET" || req.URL.Query().Get("api-version") != "2023-05-01" || includeDeleted && req.URL.Query().Get("$include") != "deleted" {
					t.Fatal("shared storage lost native emptiness validation", req.Method, req.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		if strings.EqualFold(req.URL.Path, targetID) {
			if gone {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
			}
			if req.Method == "DELETE" {
				deletes = append(deletes, targetID)
				gone = true
				return jsonResponse(204, nil, nil), true
			}
			if req.Method != "GET" {
				t.Fatal("unexpected shared diagnostic destination mutation", req.Method)
			}
			return jsonResponse(200, storage, nil), true
		}
		return nil, false
	}
	return f, f.asset(t, id), dnsAsset(t, f.runtime, storage), &gone, &deletes
}

func TestDiagnosticUnindexedAndLateSettingsProtectNativeTargets(t *testing.T) {
	for _, mode := range []string{"unindexed", "target-absent", "late-after-delete", "collection-403", "collection-404", "get-404", "changed-between-passes"} {
		t.Run(mode, func(t *testing.T) {
			f, setting, target, gone, deleted := diagnosticStorageTarget(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			raw := f.settings[setting.Identity.NativeID]
			if mode == "late-after-delete" {
				clear(f.settings)
				result, err := driver.Execute(t.Context(), request)
				if err != nil || len(*deleted) != 1 {
					t.Fatal("empty diagnostic scope did not permit native target deletion", err)
				}
				f.settings[setting.Identity.NativeID] = raw
				wire, _ := json.Marshal(request)
				if json.Unmarshal(wire, &request) != nil {
					t.Fatal("invalid recovered target request")
				}
				driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err == nil || isNotFound(err) || wait.Done {
					t.Fatal("late diagnostic reference vanished with target absence", wait, err)
				}
				return
			}
			if mode == "target-absent" {
				*gone = true
			}
			base := f.override
			collection := strings.TrimSuffix(setting.Identity.NativeID, "/"+last(setting.Identity.NativeID))
			reads := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == collection {
					reads++
					if mode == "collection-403" || mode == "collection-404" {
						status := 403
						if mode == "collection-404" {
							status = 404
						}
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					if mode == "changed-between-passes" && reads == 1 {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
				}
				if path == setting.Identity.NativeID && mode == "get-404" {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				return base(req)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || len(*deleted) != 0 {
				t.Fatal("unindexed/uncertain diagnostic setting did not protect target", mode, err, *deleted)
			}
			if mode == "unindexed" {
				c, _ := f.runtime.resolve(t.Context(), "connection")
				contribution, err := c.contributeMonitorIncoming(t.Context(), []asset.Asset{target}, []asset.Asset{target})
				if err != nil || len(contribution.Unresolved) != 1 || len(contribution.Relationships)+len(contribution.Bindings) != 0 {
					t.Fatal("unindexed diagnostic setting lost native graph blocker", contribution, err)
				}
				ref := contribution.Unresolved[0]
				if ref.NativeType != diagnosticSettingsType || ref.NativeID != setting.Identity.NativeID || ref.ControllerID != target.ID || ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || ref.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
					t.Fatal("unindexed diagnostic source acquired ownership or lost ordering", ref)
				}
			}
		})
	}
}

func TestDiagnosticReviewedPrerequisiteSurvivesTargetRecovery(t *testing.T) {
	f, setting, target, _, deleted := diagnosticStorageTarget(t)
	values := []asset.Asset{setting, target}
	lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
	store := batchReferenceGraph{assets: values}
	built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "diagnostic-shared", f.runtime.bundle, []governance.Contributor{lifecycle})
	if err != nil || len(built.Relationships) != 2 || len(built.Bindings) != 0 {
		t.Fatal("diagnostic target graph lost independent prerequisite", built, err)
	}
	planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{setting.ID, target.ID}})
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 || planned.Steps[0].AssetID != setting.ID {
		t.Fatal("diagnostic target deletion was not ordered", planned, err)
	}
	request := servicePlanRequest(planned, values, target)
	if len(request.PrerequisiteDeletions) != 1 {
		t.Fatal("diagnostic target lost frozen prerequisite", request)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(*deleted) != 0 {
		t.Fatal("target executed while diagnostic prerequisite remained", err)
	}
	settingDriver := f.action(t, setting)
	settingRequest := servicePlanRequest(planned, values, setting)
	result, err := settingDriver.Execute(t.Context(), settingRequest)
	if err != nil || len(f.deleted) != 1 || len(*deleted) != 0 {
		t.Fatal("diagnostic prerequisite deletion touched its shared target", err)
	}
	if wait, err := settingDriver.Wait(t.Context(), settingRequest, result); err != nil || !wait.Done {
		t.Fatal("diagnostic prerequisite did not confirm native absence", wait, err)
	}
	result, err = driver.Execute(t.Context(), request)
	if err != nil || len(*deleted) != 1 {
		t.Fatal("reviewed native diagnostic absence did not permit target cleanup", result, err)
	}
	wire, _ := json.Marshal(request)
	if json.Unmarshal(wire, &request) != nil {
		t.Fatal("target prerequisite did not survive JSON persistence")
	}
	driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("recovered target could not validate frozen diagnostic prerequisite", wait, err)
	}
	request.PrerequisiteDeletions[0].Asset.Normalized["_diagnostic_references"] = map[string]any{}
	if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
		t.Fatal("changed prerequisite proof was accepted after both resources disappeared", wait, err)
	}
}

func TestDiagnosticManagedGroupSettingIsIndependentPrerequisite(t *testing.T) {
	f := newDiagnosticFixture(t)
	s := newAKSScenario()
	group := strings.ToLower(text(s.group["id"]))
	source := f.sources[slices.Sorted(maps.Keys(f.sources))[0]]
	sourceID := group + "/providers/microsoft.keyvault/vaults/diagnostic-source"
	source["id"] = sourceID
	clear(f.sources)
	f.sources[sourceID] = source
	clear(f.groups)
	f.groups[group] = s.group
	raw := f.settings[slices.Sorted(maps.Keys(f.settings))[1]]
	id := sourceID + "/providers/microsoft.insights/diagnosticsettings/audit"
	raw["id"], raw["name"] = id, "audit"
	clear(f.settings)
	f.settings[id] = raw
	// Even a generic managed-group index that includes the extension resource
	// must not turn its diagnostic setting into a controller-owned impact.
	s.members = []any{source, raw}
	r := s.runtime(t)
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/resources" || path == sourceID || strings.Contains(path, "/providers/microsoft.insights/diagnosticsettings") {
			return f.runtime.transport.RoundTrip(req)
		}
		return base.RoundTrip(req)
	})
	values := s.assets(t)[:2]
	setting, vault := f.asset(t, id), dnsAsset(t, r, source)
	setting.Identity.Partition, vault.Identity.Partition = values[0].Identity.Partition, values[0].Identity.Partition
	values = append(values, vault, setting)
	controller := values[0]
	service, _ := r.ServiceLifecycle(t.Context(), "connection")
	cluster, _ := r.ClusterLifecycle(t.Context(), "connection")
	store := batchReferenceGraph{assets: values}
	built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "diagnostic-managed", r.bundle, []governance.Contributor{service, cluster})
	if err != nil {
		t.Fatal("diagnostic managed-group native graph failed", err)
	}
	for _, binding := range built.Bindings {
		if binding.ManagedAssetID == setting.ID {
			t.Fatal("diagnostic setting acquired controller ownership", binding)
		}
	}
	alone, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{controller.ID}})
	if err != nil || len(alone.Blockers) == 0 {
		t.Fatal("managed group bypassed retained diagnostic setting", alone, err)
	}
	planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{controller.ID, setting.ID}})
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 || len(planned.ImpactItems) != 2 || planned.Steps[0].AssetID != setting.ID {
		t.Fatal("diagnostic setting was not independently ordered before managed group cleanup", planned, err)
	}
	request := servicePlanRequest(planned, values, controller)
	request.IdempotencyKey = "delete-aks"
	if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != setting.ID {
		t.Fatal("managed controller lost its reviewed diagnostic prerequisite", request)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", controller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || s.deletes != 0 {
		t.Fatal("controller deleted while its diagnostic setting remained", err)
	}
	settingDriver := f.action(t, setting)
	if _, err := settingDriver.Execute(t.Context(), servicePlanRequest(planned, values, setting)); err != nil || len(f.deleted) != 1 || s.deletes != 0 {
		t.Fatal("independent diagnostic deletion touched its managed source", err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || s.deletes != 1 {
		t.Fatal("diagnostic prerequisite did not permit reviewed controller cleanup", result, err)
	}
	wire, _ := json.Marshal(request)
	if json.Unmarshal(wire, &request) != nil {
		t.Fatal("controller request did not survive persistence")
	}
	driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
		t.Fatal("native controller operation skipped its pending phase", wait, err)
	}
	s.clusterGone, s.groupGone, s.childrenGone = true, true, true
	delete(f.sources, sourceID)
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("native controller failed after diagnostic and source absence", wait, err)
	}
}

func TestDiagnosticPrerequisiteProofCannotBeForgedAfterNativeAbsence(t *testing.T) {
	for _, mode := range []string{"configuration", "context", "references", "foreign-connection", "foreign-partition", "wrong-controller", "duplicate", "owned-impact"} {
		t.Run(mode, func(t *testing.T) {
			f, setting, target, _, deleted := diagnosticStorageTarget(t)
			delete(f.settings, setting.Identity.NativeID)
			prerequisite := contracts.ActionImpact{Asset: setting, ControllerID: target.ID, Delete: true}
			request := contracts.ActionRequest{Asset: target, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{prerequisite}}
			switch mode {
			case "configuration":
				setting.Normalized[diagnosticConfigurationProof] = "changed"
			case "context":
				setting.Normalized[diagnosticContextProof] = "changed"
			case "references":
				setting.Normalized["_diagnostic_references"] = map[string]any{}
			case "foreign-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "another-connection"
			case "foreign-partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "another-partition"
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = setting.ID
			case "duplicate":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, prerequisite)
			case "owned-impact":
				request.PrerequisiteDeletions = nil
				request.LifecycleImpacts = []contracts.ActionImpact{prerequisite}
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(*deleted)+len(f.deleted) != 0 {
				t.Fatal("native setting absence authorized an unbound prerequisite", mode, err)
			}
		})
	}
}
