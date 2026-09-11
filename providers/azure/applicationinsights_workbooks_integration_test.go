package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestApplicationInsightsWorkbooksNativeScanWorkerAndPlan(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		t.Run(last(kind), func(t *testing.T) {
			ctx := t.Context()
			f := newWorkbookFixture(t, kind)
			r := f.runtime
			repository, err := sqlite.Open(filepath.Join(t.TempDir(), "workbooks.db"), "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
			if err := repository.PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			if err := repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: asset.ProviderAzure, Type: asset.CredentialAzureServicePrincipal, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			root := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
			if err := repository.PutScope(ctx, root); err != nil {
				t.Fatal(err)
			}
			if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "west", ConnectionID: connection.ID, RegionID: "westus", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			registry := providerruntime.NewRegistry()
			if err := registry.Register(r); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterBundle(r.Bundle()); err != nil {
				t.Fatal(err)
			}
			creator, err := inventory.NewCreator(repository, registry)
			if err != nil {
				t.Fatal(err)
			}
			handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
			scan := func() []asset.Asset {
				t.Helper()
				created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "workbook-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"westus"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(kind).ID}})
				if err != nil || len(created.Shards) != 1 {
					t.Fatal("creator omitted dedicated workbook source", created, err)
				}
				shard := created.Shards[0]
				authoritative := kind == insightsWorkbookTemplateType
				if shard.Source != insightsInventorySource(kind) || shard.Authoritative != authoritative {
					t.Fatal("incorrect workbook source/authority", shard)
				}
				if !authoritative {
					shard.Authoritative = true // Stale persisted coverage cannot widen this source.
					if err := repository.PutScanShard(ctx, shard); err != nil {
						t.Fatal(err)
					}
				}
				for _, job := range created.Jobs {
					if err := handler.Handle(ctx, job); err != nil {
						t.Fatal("native worker failed", err)
					}
				}
				stored, err := repository.GetScanShard(ctx, shard.ID)
				if err != nil || stored.Status != asset.ShardSucceeded || stored.Authoritative != authoritative || stored.Coverage.Authoritative != authoritative {
					t.Fatal("worker lost source authority", stored, err)
				}
				values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
				if err != nil {
					t.Fatal(err)
				}
				return values
			}
			values := scan()
			if len(values) != 2 {
				t.Fatal("worker lost original workbook identities", len(values))
			}
			first := values[0]
			previous := text(first.Normalized[insightsWorkbookProof])
			changeWorkbookContent(kind, f.objects[first.Identity.NativeID])
			if kind != insightsWorkbookTemplateType {
				object(f.objects[first.Identity.NativeID]["properties"])["category"] = "custom-saved-workbook"
			}
			values = scan()
			if len(values) != 2 {
				t.Fatal("known category refresh lost workbook", len(values))
			}
			for _, value := range values {
				if value.ID == first.ID {
					first = value
				}
			}
			if first.Normalized[insightsWorkbookProof] == previous || first.LastSeenAt.Before(now) || !first.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("worker failed to refresh private content through known IDs")
			}
			lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			built, err := governance.NewService(repository, repository).RebuildGraph(ctx, root.ID, connection.ID, "workbook-native", r.bundle, []governance.Contributor{lifecycle, NewResourceAttachments()})
			if err != nil || len(built.Bindings)+len(built.Unresolved) != 0 {
				t.Fatal("independent workbooks acquired component ownership", built, err)
			}
			planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{first.ID}})
			if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != first.ID {
				t.Fatal("projected workbook cannot be deleted independently", planned, err)
			}
			request := servicePlanRequest(planned, values, first)
			request.IdempotencyKey = "persisted-workbook"
			driver, err := registry.ResolveAction(ctx, connection.ID, first)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(ctx, request)
			if err != nil || !slices.Equal(f.deletes, []string{first.Identity.NativeID}) {
				t.Fatal("projected native deletion failed", result, err)
			}
			encoded, _ := json.Marshal(request)
			if strings.Contains(string(encoded), "PRIVATE_CHANGED_WORKBOOK") {
				t.Fatal("private content leaked into persisted action")
			}
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			driver, err = registry.ResolveAction(ctx, connection.ID, request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
				t.Fatal("persisted action could not prove active absence", wait, err)
			}
			// Bounded lists still have no blanket authority. Saved workbook IDs
			// are individually re-read and can close only on their own absence.
			values = scan()
			if len(values) != 1 {
				t.Fatal("inventory did not reconcile native workbook absence", len(values))
			}
			closed, err := repository.GetAsset(ctx, first.ID)
			if err != nil || closed.ClosedAt == nil || closed.DeletedAt != nil {
				t.Fatal("inventory absence did not close the record or invented a cleanup tombstone", closed, err)
			}
		})
	}
}

func TestApplicationInsightsWorkbookSharedReferenceGraph(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType} {
		t.Run(last(kind), func(t *testing.T) {
			f := newWorkbookFixture(t, kind)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			storage := nativeResource(storageType, "workbookstore", "westus", map[string]any{})
			storageID, _, _ := parseID(text(storage["id"]))
			container := map[string]any{"id": storageID + "/blobServices/default/containers/workbooks", "name": "workbooks", "type": containerType, "location": "westus", "properties": map[string]any{}}
			identity := nativeResource("Microsoft.ManagedIdentity/userAssignedIdentities", "workbook-reader", "westus", map[string]any{"principalId": rbacTestPrincipal, "clientId": rbacTestClientID, "tenantId": testTenant})
			props := object(f.objects[id]["properties"])
			props["sourceId"], props["storageUri"] = storageID, text(container["id"])
			f.objects[id]["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{text(identity["id"]): map[string]any{}}}
			// Opaque authored queries are content, not authoritative ARM links.
			props["serializedData"] = `{"resourceId":"/subscriptions/ignored/resourceGroups/ignored/providers/Unknown/widgets/ignored"}`
			values := []asset.Asset{dnsAsset(t, f.runtime, storage), dnsAsset(t, f.runtime, container), dnsAsset(t, f.runtime, identity), dnsAsset(t, f.runtime, f.objects[id])}
			workbook := values[3]
			dependencies := slices.Clone(values[:3])
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := c.contributeWorkbookReferences(t.Context(), workbook, values)
			if err != nil || len(contribution.Relationships) != 3 || len(contribution.Unresolved)+len(contribution.Bindings) != 0 {
				t.Fatal("native shared references failed", contribution, err)
			}
			store := batchReferenceGraph{assets: values}
			built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "workbook-shared", f.runtime.bundle, nil)
			if err != nil || len(built.Unresolved)+len(built.Bindings) != 0 {
				t.Fatal("workbook reference projection failed", built, err)
			}
			for _, target := range dependencies {
				if !slices.ContainsFunc(built.Relationships, func(ref graph.Relationship) bool {
					return ref.SourceAssetID == workbook.ID && ref.TargetAssetID == target.ID
				}) {
					t.Fatal("shared reference lost from graph", target.Identity.NativeID)
				}
				planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, ResolvedAssetIDs: []asset.AssetID{target.ID, workbook.ID}})
				if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 || planned.Steps[0].AssetID != workbook.ID || !slices.Contains(planned.Steps[1].DependsOn, planned.Steps[0].ID) {
					t.Fatal("shared deletion was not ordered after selected workbook", planned, err)
				}
			}
			planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, ResolvedAssetIDs: []asset.AssetID{workbook.ID}})
			if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != workbook.ID {
				t.Fatal("workbook plan selected shared dependencies", planned, err)
			}
			request := servicePlanRequest(planned, values, workbook)
			request.IdempotencyKey = "independent-byos-workbook"
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", workbook)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || !slices.Equal(f.deletes, []string{id}) {
				t.Fatal("workbook deletion touched an external target", err, f.deletes)
			}
		})
	}
}

func TestApplicationInsightsWorkbookReferenceBoundaries(t *testing.T) {
	for _, mode := range []string{"missing", "other-connection", "other-partition", "foreign-subscription", "duplicate", "content-drift", "opaque-url"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkbookFixture(t, insightsWorkbookType)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			raw := nativeResource(storageType, "sharedsource", "westus", map[string]any{})
			target := dnsAsset(t, f.runtime, raw)
			if mode == "foreign-subscription" {
				raw["id"] = strings.Replace(text(raw["id"]), testSubscription, testTenant, 1)
				target.Identity.NativeID, _, _ = parseID(text(raw["id"]))
				target.ID = asset.AssetID(target.Identity.NativeID)
			}
			object(f.objects[id]["properties"])["sourceId"] = text(raw["id"])
			if mode == "opaque-url" {
				object(f.objects[id]["properties"])["sourceId"] = "https://untrusted.invalid/private-context?secret=PRIVATE_SOURCE"
			}
			workbook := dnsAsset(t, f.runtime, f.objects[id])
			values := []asset.Asset{target, workbook}
			switch mode {
			case "missing":
				values = values[1:]
			case "other-connection":
				values[0].Identity.ConnectionID = "another"
			case "other-partition":
				values[0].Identity.Partition = "another"
			case "duplicate":
				copy := target
				copy.ID = "duplicate-native-target"
				values = append(values, copy)
			case "content-drift":
				changeWorkbookContent(insightsWorkbookType, f.objects[id])
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.contributeWorkbookReferences(t.Context(), workbook, values)
			if mode == "duplicate" || mode == "content-drift" {
				if err == nil {
					t.Fatal("changed/ambiguous workbook reference accepted", result)
				}
				return
			}
			want := 1
			if mode == "opaque-url" {
				want = 0
			}
			if err != nil || len(result.Unresolved) != want || len(result.Relationships)+len(result.Bindings) != 0 {
				t.Fatal("reference gained foreign authority", result, err)
			}
		})
	}
}

func TestApplicationInsightsComponentManagedWorkbook(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		modes := []string{"delayed", "private-change"}
		if kind == insightsWorkbookType {
			modes = append(modes, "history-change", "history-denied", "readback-history-change")
		}
		for _, mode := range modes {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newInsightsComponentFixture(t)
				clear(f.children)
				w := newWorkbookFixture(t, kind)
				oldID := slices.Sorted(maps.Keys(w.objects))[0]
				raw := w.objects[oldID]
				id := f.managedID + "/providers/" + strings.ToLower(kind) + "/" + last(oldID)
				raw["id"], raw["name"], raw["location"] = id, last(id), "southcentralus"
				f.members[id] = raw
				revisions := w.revisions[oldID]
				for _, version := range revisions {
					version["id"], version["name"], version["location"] = id, last(id), "southcentralus"
				}
				// The original private example's source string is only metadata;
				// managed group membership comes from the native group index.
				gone, historyDenied := false, false
				f.response = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == "/subscriptions/"+testSubscription+"/resources" {
						rows := []any{}
						if !gone {
							rows = append(rows, raw)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					if path == id || strings.HasPrefix(path, id+"/revisions") || path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(kind) || kind == insightsWorkbookTemplateType && strings.HasSuffix(path, "/providers/"+strings.ToLower(kind)) {
						if req.Method != "GET" || req.URL.Query().Get("api-version") != insightsWorkbookVersion(kind) {
							t.Fatal("managed workbook received independent mutation/wrong version", req.URL)
						}
						if path == id {
							if kind == insightsWorkbookType && req.URL.Query().Get("canFetchContent") != "true" {
								t.Fatal("managed member GET omitted full content")
							}
							if gone {
								return jsonResponse(404, nil, nil), true
							}
							return jsonResponse(200, raw, nil), true
						}
						if strings.HasPrefix(path, id+"/revisions") {
							if historyDenied {
								return jsonResponse(404, nil, nil), true
							}
							if path != id+"/revisions" {
								return jsonResponse(200, revisions[last(path)], nil), true
							}
							rows := []any{}
							for _, revision := range slices.Sorted(maps.Keys(revisions)) {
								version := maps.Clone(revisions[revision])
								version["properties"] = maps.Clone(object(version["properties"]))
								object(version["properties"])["serializedData"] = nil
								rows = append(rows, version)
							}
							return jsonResponse(200, map[string]any{"value": rows}, nil), true
						}
						rows := []any{}
						if !strings.Contains(path, "/revisions") && (!strings.HasPrefix(path, f.groupID+"/") || f.groupID == f.managedID) && (kind == insightsWorkbookTemplateType || req.URL.Query().Get("category") == "workbook") {
							rows = append(rows, raw)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					return nil, false
				}
				request, planned, _ := insightsComponentPlan(t, f, kind)
				if len(planned.Steps) != 1 || len(request.LifecycleImpacts) != 3 || len(request.PrerequisiteDeletions) != 0 {
					t.Fatal("managed workbook lost controller delegation", planned, request)
				}
				encoded, _ := json.Marshal(request)
				if strings.Contains(string(encoded), "serializedData") || strings.Contains(string(encoded), "templateData") {
					t.Fatal("private managed workbook content leaked")
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "private-change" || mode == "history-change" || mode == "history-denied" {
					switch mode {
					case "private-change":
						changeWorkbookContent(kind, raw)
					case "history-denied":
						historyDenied = true
					case "history-change":
						for _, version := range revisions {
							changeWorkbookContent(kind, version)
						}
					}
					if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
						t.Fatal("changed managed workbook reached component DELETE", err)
					}
					return
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal("component deletion with native workbook failed", err)
				}
				if err := json.Unmarshal(encoded, &request); err != nil {
					t.Fatal(err)
				}
				driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("component absence hid live managed workbook", wait, err)
				}
				if mode == "readback-history-change" {
					for _, version := range revisions {
						changeWorkbookContent(kind, version)
					}
					if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
						t.Fatal("changed residual workbook history accepted", wait, err)
					}
					return
				}
				gone = true
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done || len(f.deletes) != 1 {
					t.Fatal("managed workbook absence failed after restart", wait, err)
				}
			})
		}
	}
}
