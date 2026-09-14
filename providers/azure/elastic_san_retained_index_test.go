package azure

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func elasticSanRetainedSnapshotFixture(t *testing.T) (*elasticSanFixture, string, string, string) {
	t.Helper()
	f := newElasticSanFixture(t)
	group := f.ids[elasticSanGroupType] + "-retained"
	volume := group + "/volumes/child-1751081600"
	snapshot := group + "/snapshots/known"
	raw := batchClone(f.values[f.ids[elasticSanSnapshotType]])
	raw["id"], raw["name"] = snapshot, last(snapshot)
	object(raw["properties"])["creationData"] = map[string]any{"sourceId": volume}
	f.values[snapshot] = raw
	return f, group, volume, snapshot
}

func TestElasticSanRetainedSnapshotIndexFullFamilyRecovery(t *testing.T) {
	f, group, volume, snapshot := elasticSanRetainedSnapshotFixture(t)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 9 {
		t.Fatal("initial family", len(values))
	}
	// Recover known snapshots with a new runtime and database after the retained
	// parent's snapshot index disappears. Its own GET remains independently live.
	var err error
	repo, err = sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	f.runtime = fresh
	registry = providerruntime.NewRegistry()
	if err := registry.Register(fresh); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(fresh.Bundle()); err != nil {
		t.Fatal(err)
	}
	f.override = func(r *http.Request) (*http.Response, bool) {
		if strings.EqualFold(r.URL.Path, group+"/snapshots") {
			return jsonResponse(404, map[string]any{}, nil), true
		}
		return nil, false
	}
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 9 {
		t.Fatal("missing index closed known resource", len(values))
	}
	var volumeID, snapshotID asset.AssetID
	for _, value := range values {
		if value.Identity.NativeID == snapshot {
			snapshotID = value.ID
		}
		if value.Identity.NativeID == volume {
			volumeID = value.ID
			state := object(value.Normalized[elasticSanSnapshotCleanup])
			if state["protected"] != true || object(state["volume"])["snapshots_complete"] != false || object(object(state["volume"])["snapshots"])[snapshot] == nil || value.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("incomplete snapshot population authorized cleanup", value.Normalized)
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
				t.Fatal("incomplete volume resolved deletion")
			}
		}
	}
	unresolved, err := repo.ListUnresolvedByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range unresolved {
		found = found || ref.ControllerID == volumeID && ref.BlocksCleanup && ref.Evidence["reason"] == "elastic_san_volume_snapshot_membership_incomplete"
	}
	if volumeID == "" || !found {
		t.Fatal("incomplete volume constraint not persisted", unresolved)
	}
	task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: volumeID}}})
	if err != nil || len(task.Steps) != 0 {
		t.Fatal("incomplete controller released child cleanup", task.Task, err)
	}
	independent, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: snapshotID}}})
	if err != nil || independent.Task.Status != plan.StatusReady || len(independent.Steps) != 1 || independent.Steps[0].AssetID != snapshotID {
		t.Fatal("known independent snapshot cleanup lost", independent.Task, err)
	}
	missingIndex := f.override
	f.override = func(r *http.Request) (*http.Response, bool) {
		if strings.EqualFold(r.URL.Path, snapshot) {
			return jsonResponse(403, map[string]any{}, nil), true
		}
		return missingIndex(r)
	}
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, true, true)
	if len(values) != 9 {
		t.Fatal("denied own read closed family assets", len(values))
	}
	f.override = missingIndex
	// An own-resource 404, separately from the collection 404, can reconcile this
	// known snapshot. Unknown membership remains incomplete and blocks its volume.
	delete(f.values, snapshot)
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	if len(values) != 8 {
		t.Fatal("known own absence not reconciled", len(values))
	}
	for _, value := range values {
		if value.Identity.NativeID == snapshot {
			t.Fatal("absent snapshot retained")
		}
	}
	// Restored collection access removes the incompleteness proof and blocker.
	f.override = nil
	values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	for _, value := range values {
		if value.Identity.NativeID == volume {
			if !value.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("fresh complete volume stayed protected", value.Normalized)
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err != nil {
				t.Fatal("fresh volume cannot resolve", err)
			}
		}
	}
	unresolved, err = repo.ListUnresolvedByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range unresolved {
		if ref.ControllerID == volumeID {
			t.Fatal("refreshed membership kept stale blocker", ref)
		}
	}
}

func TestElasticSanRetainedSnapshotIndexReadBoundaries(t *testing.T) {
	for _, mode := range []string{"known-live", "known-absent", "unknown", "index-denied", "own-denied", "active-parent", "parent-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, group, _, snapshot := elasticSanRetainedSnapshotFixture(t)
			request := f.request(elasticSanSnapshotType)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "unknown" {
				request.KnownNativeMetadata = map[string]map[string]any{}
				for _, item := range batch.Items {
					request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
					request.KnownNativeMetadata[item.NativeID] = item.Normalized
				}
			}
			if mode == "known-absent" {
				delete(f.values, snapshot)
			}
			if mode == "active-parent" {
				f.retained[group] = false
				object(f.values[group]["properties"])["provisioningState"] = "Succeeded"
			}
			f.override = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if mode == "own-denied" && path == snapshot || mode == "parent-denied" && path == group {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if path == group+"/snapshots" {
					status := 404
					if mode == "index-denied" {
						status = 403
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				return nil, false
			}
			next, err := f.runtime.List(t.Context(), request)
			if !slices.Contains([]string{"known-live", "known-absent", "unknown"}, mode) {
				if err == nil {
					t.Fatal("unverified or denied population accepted")
				}
				return
			}
			if err != nil || !next.Complete {
				t.Fatal("known native inventory failed", err)
			}
			found := false
			for _, item := range next.Items {
				found = found || item.NativeID == snapshot
			}
			if found != (mode == "known-live") {
				t.Fatal("known own read not respected", found)
			}
			if slices.Contains(next.AbsentNativeIDs, snapshot) != (mode == "known-absent") {
				t.Fatal("unread or unknown snapshot marked absent", next.AbsentNativeIDs)
			}
		})
	}
}

func TestElasticSanRetainedSnapshotIndexAvailabilityChanges(t *testing.T) {
	for _, mode := range []string{"two-pass-empty", "cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanFixture(t)
			group := f.ids[elasticSanGroupType] + "-retained"
			request := f.request(elasticSanSnapshotType)
			if mode == "two-pass-empty" {
				delete(f.values, f.ids[elasticSanSnapshotType])
				calls := 0
				f.override = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, group+"/snapshots") {
						calls++
						if calls == 1 {
							return jsonResponse(404, map[string]any{}, nil), true
						}
					}
					return nil, false
				}
			} else {
				raw := batchClone(f.values[f.ids[elasticSanSnapshotType]])
				id := f.ids[elasticSanSnapshotType] + "-second"
				raw["id"], raw["name"] = id, last(id)
				f.values[id] = raw
				request.Limit = 1
				batch, err := f.runtime.List(t.Context(), request)
				if err != nil || batch.NextCursor == "" {
					t.Fatal("missing initial cursor", err)
				}
				request.Cursor = batch.NextCursor
				f.override = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, group+"/snapshots") {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					return nil, false
				}
			}
			if _, err := f.runtime.List(t.Context(), request); err == nil {
				t.Fatal("changed empty collection availability accepted")
			}
		})
	}
}

func TestElasticSanRetainedSnapshotIndexProof(t *testing.T) {
	for _, mode := range []string{"unsigned-marker", "unprotected-marker"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanFixture(t)
			batch, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
			if err != nil {
				t.Fatal(err)
			}
			value := elasticSanTestAsset(batch.Items[0])
			state := object(value.Normalized[elasticSanSnapshotCleanup])
			object(state["volume"])["snapshots_complete"] = false
			if mode == "unprotected-marker" {
				value.Normalized[elasticSanSnapshotCleanupProof] = f.client.elasticSanChildBinding(value, state)
			}
			s := &serviceCascades{client: f.client, connectionID: "connection"}
			result := governance.Contribution{}
			if err := s.contributeElasticSanVolumes(t.Context(), []asset.Asset{value}, &result); err == nil {
				t.Fatal("untrusted incompleteness accepted", mode)
			}
		})
	}
}
