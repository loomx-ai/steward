package azure

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

// Preserve the published list objects. Matching GET arrays are composed from
// those same native objects; their original EventTime is retained, not used as
// evidence for which dates a live Azure query would return.
func addInsightsAnnotationExamples(t *testing.T, f *insightsInventoryFixture) []string {
	t.Helper()
	list := insightsScopedExample(t, "stable/2015-05-01/examples/AnnotationsList.json", f.parentID)
	ids := []string{}
	for _, value := range array(list["value"]) {
		raw := object(value)
		id, err := insightsLegacyURL(f.parentID, insightsAnnotationType, text(raw["Id"]))
		if err != nil {
			t.Fatal(err)
		}
		f.children[id] = raw
		ids = append(ids, id)
	}
	return ids
}

func TestApplicationInsightsAnnotationNativeInventoryAndCursor(t *testing.T) {
	for _, mode := range []string{"unchanged", "private", "membership", "window", "missing-window", "expired-window", "other-kind", "other-source", "custom-options", "known-ids"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			ids := addInsightsAnnotationExamples(t, f)
			request := productRequest(f.runtime, insightsAnnotationType)
			request.Limit = 1
			page, err := f.runtime.List(t.Context(), request)
			if err != nil || page.Complete || page.NextCursor == "" || len(page.Items) != 1 || len(f.annotationWindows) != 2 || f.annotationWindows[0] != f.annotationWindows[1] {
				t.Fatal("annotation inventory lost bounded native snapshots", page, err)
			}
			item := page.Items[0]
			if item.NativeType != insightsAnnotationType || item.Actionable == nil || !*item.Actionable || item.Name != text(f.children[item.NativeID]["AnnotationName"]) || item.Normalized["_inventory_source"] != insightsAnnotationSource || item.Normalized["_insights_annotation_window"] != f.annotationWindows[0] || text(item.Normalized[insightsChildProofKey(insightsAnnotationType)]) == "" {
				t.Fatal("annotation lost native identity, name, scope or coverage evidence", item)
			}
			encoded, _ := json.Marshal(page)
			if strings.Contains(string(encoded), "ReleaseRequestedFor") || strings.Contains(string(encoded), "BuildRepositoryName") || strings.Contains(string(encoded), "mseng.visualstudio.com") {
				t.Fatal("opaque annotation properties leaked")
			}
			request.Cursor = page.NextCursor
			switch mode {
			case "private":
				f.children[ids[0]]["Properties"] = "PRIVATE_CHANGED_ANNOTATION"
			case "membership":
				delete(f.children, ids[0])
			case "window", "missing-window", "expired-window":
				bytes, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
				var cursor insightsInventoryCursor
				if err := json.Unmarshal(bytes, &cursor); err != nil {
					t.Fatal(err)
				}
				if mode == "missing-window" {
					cursor.Window = insightsAnnotationWindow{}
				} else if mode == "expired-window" {
					cursor.Window.Start = time.Now().Add(-91 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
				} else {
					cursor.Window.Start = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
				}
				bytes, _ = json.Marshal(cursor)
				request.Cursor = base64.RawURLEncoding.EncodeToString(bytes)
			case "other-kind":
				kind := f.runtime.resourceKind(insightsAnalyticsType)
				request.ResourceKind = &kind
				request.Source = productInventorySource
			case "other-source":
				request.Source = productInventorySource
			case "custom-options":
				request.Options = map[string]any{"start": "2018-01-01"}
			case "known-ids":
				request.KnownNativeIDs = ids
			}
			page, err = f.runtime.List(t.Context(), request)
			if mode != "unchanged" {
				if err == nil || len(page.Items) != 0 || page.Complete {
					t.Fatal("changed annotation cursor/source accepted", page, err)
				}
				return
			}
			if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].NativeID == item.NativeID || len(f.annotationWindows) != 4 {
				t.Fatal("stable bounded cursor failed", page, err)
			}
			for _, window := range f.annotationWindows {
				if window != f.annotationWindows[0] {
					t.Fatal("moving clock changed query bounds between pages")
				}
			}
		})
	}
}

func TestApplicationInsightsAnnotationReadFailures(t *testing.T) {
	for _, mode := range []string{"list-403", "list-404", "list-206", "list-next", "list-duplicate", "list-private-drift", "get-403", "get-404", "get-206", "get-lro", "get-empty-array", "get-two-items", "get-object", "get-other-id", "get-private-drift", "snapshot-private-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			ids := addInsightsAnnotationExamples(t, f)
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				rows := []any{f.children[ids[0]], f.children[ids[1]]}
				if path == f.parentID+"/annotations" {
					switch mode {
					case "list-403":
						return jsonResponse(403, nil, nil), true
					case "list-404":
						return jsonResponse(404, nil, nil), true
					case "list-206":
						return jsonResponse(206, map[string]any{"value": rows}, nil), true
					case "list-next":
						return jsonResponse(200, map[string]any{"value": rows, "nextLink": req.URL.String() + "&skiptoken=one"}, nil), true
					case "list-duplicate":
						return jsonResponse(200, map[string]any{"value": append(rows, rows[0])}, nil), true
					case "list-private-drift":
						copy := maps.Clone(object(rows[0]))
						copy["Properties"] = "PRIVATE_DIFFERENT_LIST"
						return jsonResponse(200, map[string]any{"value": []any{copy, rows[1]}}, nil), true
					case "snapshot-private-drift":
						if f.componentLists > 1 {
							f.children[ids[0]]["Properties"] = "PRIVATE_CHANGED_DURING_SCAN"
						}
					}
				}
				if path != strings.TrimPrefix(ids[0], "https://management.azure.com") {
					return nil, false
				}
				raw := maps.Clone(f.children[ids[0]])
				switch mode {
				case "get-403":
					return jsonResponse(403, nil, nil), true
				case "get-404":
					return jsonResponse(404, nil, nil), true
				case "get-206":
					return jsonResponse(206, []any{raw}, nil), true
				case "get-lro":
					return jsonResponse(200, []any{raw}, http.Header{"Azure-Asyncoperation": {apiURL(f.parentID+"/operations/one", insightsLegacyVersion)}}), true
				case "get-empty-array":
					return jsonResponse(200, []any{}, nil), true
				case "get-two-items":
					return jsonResponse(200, []any{raw, raw}, nil), true
				case "get-object":
					return jsonResponse(200, raw, nil), true
				case "get-other-id":
					raw["Id"] = "another"
				case "get-private-drift":
					raw["Properties"] = "PRIVATE_CHANGED_DETAIL"
				default:
					return nil, false
				}
				return jsonResponse(200, []any{raw}, nil), true
			}
			if page, err := f.runtime.List(t.Context(), productRequest(f.runtime, insightsAnnotationType)); err == nil || page.Complete || len(page.Items) != 0 {
				t.Fatal("incomplete annotation query succeeded", page, err)
			}
		})
	}
}

func TestApplicationInsightsAnnotationHistoryProjectionAndPlanning(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	f.children = map[string]map[string]any{}
	ids := addInsightsAnnotationExamples(t, f)
	f.annotationVisible = map[string]bool{ids[0]: true, ids[1]: true}
	payload, err := os.ReadFile("fixtures/applicationinsights/stable/2015-05-01/examples/AnnotationsGet.json")
	var example map[string]any
	if err != nil || json.Unmarshal(payload, &example) != nil {
		t.Fatal("native historical GET example", err)
	}
	oldRaw := object(array(object(object(example["responses"])["200"])["body"])[0])
	oldID, err := insightsLegacyURL(f.parentID, insightsAnnotationType, text(oldRaw["Id"]))
	if err != nil {
		t.Fatal(err)
	}
	f.children[oldID] = oldRaw
	ctx, r := t.Context(), f.runtime
	parentPage, err := r.List(ctx, productRequest(r, applicationInsightsType))
	if err != nil || len(parentPage.Items) != 1 {
		t.Fatal(err)
	}
	c, err := r.resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	oldItem, err := r.insightsChildItem(ctx, c, parentPage.Items[0], serviceChild{id: oldID, kind: insightsAnnotationType, data: oldRaw})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "annotations.db"), "../../migrations")
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
	if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "east", ConnectionID: connection.ID, RegionID: "eastus", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	projection := inventory.NewService(repository)
	_, oldShards, err := projection.CreateScan(ctx, inventory.ScanRequest{ConnectionID: connection.ID, RequestedBy: "historical-observation", Shards: []inventory.ShardRequest{{Provider: asset.ProviderAzure, Source: insightsAnnotationSource, ScopeID: root.ID, ResourceKindID: oldItem.ResourceKind.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	// Seed a previously observed native object. Today's time-window API never
	// supplies this item; neither a new page nor an empty scan may close it.
	if err := projection.ProjectBatch(ctx, &oldShards[0], connection, contracts.InventoryBatch{Items: []contracts.InventoryItem{oldItem}, Complete: true}, inventory.ProjectionOptions{ObservedAt: now.Add(-180 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := projection.FinishShard(ctx, &oldShards[0], asset.ShardSucceeded, ""); err != nil {
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
	for _, empty := range []bool{false, true} {
		if empty {
			f.annotationVisible = map[string]bool{}
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "native-annotations", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"eastus"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(applicationInsightsType).ID, r.resourceKind(insightsAnnotationType).ID}})
		if err != nil || len(created.Shards) != 2 {
			t.Fatal("native scan creator lost annotation source", created, err)
		}
		for i := range created.Shards {
			shard := &created.Shards[i]
			scope, err := repository.GetScope(ctx, shard.ScopeID)
			if err != nil {
				t.Fatal(err)
			}
			kind := r.resourceKind(applicationInsightsType)
			if shard.ResourceKindID == r.resourceKind(insightsAnnotationType).ID {
				kind = r.resourceKind(insightsAnnotationType)
				if shard.Source != insightsAnnotationSource || shard.Authoritative || shard.Coverage.Authoritative {
					t.Fatal("server-created bounded scan became authoritative", shard)
				}
			} else if shard.Source != productInventorySource || !shard.Authoritative {
				t.Fatal("component inventory lost complete source", shard)
			}
			page, err := r.List(ctx, contracts.InventoryRequest{ConnectionID: connection.ID, Source: shard.Source, ResourceKind: &kind, Scope: scope})
			if err != nil || !page.Complete {
				t.Fatal("native annotation source failed", err)
			}
			if err := projection.ProjectBatch(ctx, shard, connection, page, inventory.ProjectionOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := projection.FinishShard(ctx, shard, asset.ShardSucceeded, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 4 {
		t.Fatal("bounded scan closed saved annotation history", len(values), err)
	}
	var parent, old asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == applicationInsightsType {
			parent = value
		}
		if value.Identity.NativeID == oldID {
			old = value
			if value.ClosedAt != nil || !value.LastSeenAt.Before(now.Add(-90*24*time.Hour)) {
				t.Fatal("old observation was closed or falsely refreshed", value)
			}
		}
	}
	// A real worker run includes saved IDs and refreshes a changed historical
	// annotation that the native time-window list still omits.
	previousProof := text(old.Normalized[insightsChildProofKey(insightsAnnotationType)])
	oldRaw["Properties"] = "PRIVATE_REFRESHED_HISTORY"
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "refresh-history", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"eastus"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(applicationInsightsType).ID, r.resourceKind(insightsAnnotationType).ID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range created.Shards {
		if shard.Source == insightsAnnotationSource {
			shard.Authoritative = true // Even stale persisted metadata cannot close history.
			if err := repository.PutScanShard(ctx, shard); err != nil {
				t.Fatal(err)
			}
		}
	}
	handler := inventory.NewScanHandler(repository, registry, projection)
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil {
			t.Fatal("worker did not reconcile known annotations", err)
		}
	}
	for _, shard := range created.Shards {
		stored, err := repository.GetScanShard(ctx, shard.ID)
		if err != nil || stored.Status != asset.ShardSucceeded || stored.Source == insightsAnnotationSource && (stored.Authoritative || stored.Coverage.Authoritative) {
			t.Fatal("bounded worker lost its source authority", stored, err)
		}
	}
	values, err = repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 4 {
		t.Fatal("history reconciliation lost assets", len(values), err)
	}
	for _, value := range values {
		if value.Identity.NativeType == applicationInsightsType {
			parent = value
		}
		if value.Identity.NativeID == oldID {
			old = value
			if value.Normalized["_insights_annotation_discovery"] != "known-id" || text(value.Normalized[insightsChildProofKey(insightsAnnotationType)]) == previousProof || value.LastSeenAt.Before(now) {
				t.Fatal("historical native GET did not refresh the saved configuration", value)
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := governance.NewService(repository, repository).RebuildGraph(ctx, root.ID, connection.ID, "annotation-history", r.bundle, []governance.Contributor{lifecycle, NewResourceAttachments()})
	if err != nil || len(graph.Bindings) != 3 || len(graph.Unresolved) != 0 {
		t.Fatal("saved annotations escaped native reconciliation", graph, err)
	}
	input := plan.Input{Assets: values, Relationships: graph.Relationships, LifecycleBindings: graph.Bindings, ResolvedAssetIDs: []asset.AssetID{parent.ID}}
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 4 || len(servicePlanRequest(planned, values, parent).PrerequisiteDeletions) != 3 {
		t.Fatal("component plan lost historical annotations", planned, err)
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{parent.ID: {"retain_resources": []string{string(old.ID)}}}
	if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
		t.Fatal("retention of historical annotation failed", retained, err)
	}
	input.RequestOptions, input.ResolvedAssetIDs = nil, []asset.AssetID{old.ID}
	planned, err = plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 {
		t.Fatal("historical annotation lost independent cleanup", planned, err)
	}
	request := servicePlanRequest(planned, values, old)
	driver, err := registry.ResolveAction(ctx, connection.ID, old)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || !slices.Equal(f.deletes, []string{oldID}) {
		t.Fatal("historical native DELETE failed", result, err, f.deletes)
	}
	driver, err = registry.ResolveAction(ctx, connection.ID, old)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
		t.Fatal("historical annotation native absence failed", wait, err)
	}
}

func TestApplicationInsightsAnnotationComponentDeletion(t *testing.T) {
	f := newInsightsComponentFixture(t)
	annotations := addInsightsAnnotationExamples(t, f.insightsInventoryFixture)
	request, planned, values := insightsComponentPlan(t, f, insightsAnnotationType)
	if len(planned.Steps) != 16 || len(request.PrerequisiteDeletions) != 15 || len(request.LifecycleImpacts) != 2 {
		t.Fatal("annotations lost separate native deletion steps", planned, request)
	}
	f.annotationVisible = map[string]bool{} // Query-window omission cannot bypass exact prerequisite GETs.
	// Remove every other prerequisite first, so only live annotations can
	// explain the blocked component deletion below.
	other := planned
	other.Steps = slices.DeleteFunc(slices.Clone(planned.Steps), func(step plan.CleanupTaskStep) bool {
		index := slices.IndexFunc(values, func(value asset.Asset) bool { return value.ID == step.AssetID })
		return values[index].Identity.NativeType == insightsAnnotationType
	})
	deleteInsightsPrerequisites(t, f, other, values, request.Asset.ID)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 13 {
		t.Fatal("omitted live annotation bypassed prerequisites", err)
	}
	remaining := planned
	remaining.Steps = slices.DeleteFunc(slices.Clone(planned.Steps), func(step plan.CleanupTaskStep) bool {
		index := slices.IndexFunc(values, func(value asset.Asset) bool { return value.ID == step.AssetID })
		return values[index].Identity.NativeType != insightsAnnotationType
	})
	deleteInsightsPrerequisites(t, f, remaining, values, request.Asset.ID)
	for _, id := range annotations {
		if !slices.Contains(f.deletes, id) {
			t.Fatal("missing native annotation DELETE", id)
		}
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(request)
	var restored contracts.ActionRequest
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	driver, err = f.runtime.ResolveAction(t.Context(), "connection", restored.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), restored, result); err != nil || !wait.Done {
		t.Fatal("component completion lost annotation absence", wait, err)
	}
}

func TestApplicationInsightsAnnotationKnownIdentityReconciliation(t *testing.T) {
	for _, mode := range []string{"live", "gone", "duplicate", "foreign-subscription", "foreign-host", "other-kind", "noncanonical", "parent-omitted", "parent-denied", "parent-gone", "survived-parent", "child-denied", "child-empty-array", "private-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			ids := addInsightsAnnotationExamples(t, f)
			f.annotationVisible = map[string]bool{}
			request := productRequest(f.runtime, insightsAnnotationType)
			request.KnownNativeIDs = []string{ids[0]}
			switch mode {
			case "duplicate":
				request.KnownNativeIDs = []string{ids[0], ids[0]}
			case "foreign-subscription":
				request.KnownNativeIDs[0] = strings.Replace(ids[0], testSubscription, testTenant, 1)
			case "foreign-host":
				request.KnownNativeIDs[0] = strings.Replace(ids[0], "management.azure.com", "other.example", 1)
			case "other-kind":
				request.KnownNativeIDs[0], _ = insightsLegacyURL(f.parentID, insightsFavoriteType, "one")
			case "noncanonical":
				request.KnownNativeIDs[0] = strings.Replace(ids[0], "/subscriptions/", "/Subscriptions/", 1)
			}
			reads := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				omitted := strings.HasPrefix(mode, "parent-") || mode == "survived-parent"
				if omitted && path == "/subscriptions/"+testSubscription+"/providers/microsoft.insights/components" {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				if omitted && path == f.parentID {
					switch mode {
					case "parent-denied":
						return jsonResponse(403, nil, nil), true
					case "parent-gone", "survived-parent":
						return jsonResponse(404, nil, nil), true
					}
				}
				if path != strings.TrimPrefix(ids[0], "https://management.azure.com") {
					return nil, false
				}
				reads++
				switch mode {
				case "gone", "parent-gone":
					return jsonResponse(404, nil, nil), true
				case "child-denied":
					return jsonResponse(403, nil, nil), true
				case "child-empty-array":
					return jsonResponse(200, []any{}, nil), true
				case "private-drift":
					if reads == 2 {
						f.children[ids[0]]["Properties"] = "PRIVATE_CHANGED_KNOWN_ANNOTATION"
					}
				}
				return nil, false
			}
			page, err := f.runtime.List(t.Context(), request)
			success := mode == "live" || mode == "gone" || mode == "parent-gone"
			if !success {
				if err == nil || page.Complete || len(page.Items) != 0 {
					t.Fatal("unsafe historical identity accepted", page, err)
				}
				return
			}
			want := 0
			if mode == "live" {
				want = 1
			}
			if err != nil || !page.Complete || len(page.Items) != want || reads != 2 {
				t.Fatal("known identity was not reconciled twice", page, err, reads)
			}
			if want == 1 && (page.Items[0].NativeID != ids[0] || page.Items[0].Normalized["_insights_annotation_discovery"] != "known-id") {
				t.Fatal("historical GET was mislabeled as a window query result", page.Items)
			}
		})
	}
}

func TestApplicationInsightsAnnotationLateAndSurvivingChildrenBlockComponent(t *testing.T) {
	for _, mode := range []string{"late-child", "known-read-denied", "known-read-empty", "survived-component"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			ids := addInsightsAnnotationExamples(t, f.insightsInventoryFixture)
			request, planned, values := insightsComponentPlan(t, f, insightsAnnotationType)
			raw := maps.Clone(f.children[ids[0]])
			deleteInsightsPrerequisites(t, f, planned, values, request.Asset.ID)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			var result contracts.ActionResult
			if mode == "survived-component" {
				result, err = driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				f.children[ids[0]] = raw
			} else if mode == "late-child" {
				raw["Id"] = "created-after-review"
				id, _ := insightsLegacyURL(f.parentID, insightsAnnotationType, text(raw["Id"]))
				f.children[id] = raw
			} else {
				f.response = func(req *http.Request) (*http.Response, bool) {
					if strings.ToLower(req.URL.Path) != strings.TrimPrefix(ids[0], "https://management.azure.com") {
						return nil, false
					}
					if mode == "known-read-denied" {
						return jsonResponse(403, nil, nil), true
					}
					return jsonResponse(200, []any{}, nil), true
				}
			}
			if mode == "survived-component" {
				wait, err := driver.Wait(t.Context(), request, result)
				if err == nil && wait.Done {
					t.Fatal("component absence hid surviving annotation", wait)
				}
			} else if _, err := driver.Execute(t.Context(), request); err == nil || f.parentGone {
				t.Fatal("annotation change/read failure allowed component deletion", err)
			}
		})
	}
}
