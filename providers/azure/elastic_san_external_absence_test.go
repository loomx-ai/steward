package azure

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func elasticSanMissingParentIndexes(f *elasticSanFixture) func(*http.Request) (*http.Response, bool) {
	return func(r *http.Request) (*http.Response, bool) {
		path := strings.ToLower(r.URL.Path)
		for _, kind := range []string{elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
			parent := elasticSanParent(f.ids[kind], kind)
			if path == parent+"/"+strings.ToLower(last(kind)) && f.values[parent] == nil {
				return jsonResponse(404, map[string]any{}, nil), true
			}
		}
		return nil, false
	}
}

func TestElasticSanExternalAbsenceFullFamily(t *testing.T) {
	for _, survivor := range []string{"", elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		t.Run(last(survivor), func(t *testing.T) {
			f := elasticSanRootFixture(t)
			kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
			repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
			values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
			if len(values) != 5 {
				t.Fatal("initial family", len(values))
			}
			// Fresh persistence/runtime must recover all known own reads after external
			// deletion, even when every collection beneath a missing parent returns404.
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
			for id := range f.values {
				if id != f.ids[survivor] {
					delete(f.values, id)
				}
			}
			f.hook = elasticSanMissingParentIndexes(f.elasticSanFixture)
			values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
			expected := 0
			if survivor != "" {
				expected = 1
			}
			if len(values) != expected {
				t.Fatal("parent absence erased surviving child", survivor, len(values))
			}
			if expected == 0 {
				return
			}
			value := values[0]
			if value.Identity.NativeID != f.ids[survivor] || value.Location != "eastus" {
				t.Fatal("survivor identity lost", value.Identity)
			}
			if survivor == elasticSanGroupType || survivor == elasticSanVolumeType {
				task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: value.ID}}})
				blocked := false
				for _, b := range task.Task.Blockers {
					blocked = blocked || b.Code == plan.BlockUnresolvedCleanup && b.ControllerID == value.ID
				}
				if err != nil || task.Task.Status == plan.StatusReady || !blocked {
					t.Fatal("orphan controller authorized children", task.Task.Blockers, err)
				}
			}
			delete(f.values, f.ids[survivor])
			values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
			if len(values) != 0 {
				t.Fatal("final own absence not reconciled", len(values))
			}
		})
	}
}

func TestElasticSanExternalAbsenceChildOnlyGraph(t *testing.T) {
	for _, mode := range []string{"deleted", "root-config"} {
		t.Run(mode, func(t *testing.T) {
			f := elasticSanRootFixture(t)
			kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
			repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
			initial := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
			rootID := initial[0].ID
			for _, value := range initial {
				if value.Identity.NativeType == elasticSanType {
					rootID = value.ID
				}
			}
			if mode == "deleted" {
				for id := range f.values {
					if id != f.ids[elasticSanSnapshotType] {
						delete(f.values, id)
					}
				}
				f.hook = elasticSanMissingParentIndexes(f.elasticSanFixture)
			} else {
				object(f.values[f.ids[elasticSanType]]["properties"])["baseSizeTiB"] = 99
			}
			values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanSnapshotType}, false, true)
			if len(values) != 5 {
				t.Fatal("child scan widened closure to unscanned parents", len(values))
			}
			task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: rootID}}})
			blocked := false
			for _, b := range task.Task.Blockers {
				blocked = blocked || b.Code == plan.BlockUnresolvedCleanup && b.ControllerID == rootID
			}
			if err != nil || task.Task.Status == plan.StatusReady || !blocked {
				t.Fatal("stale root released child cleanup", task.Task.Blockers, err)
			}
			values = azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
			if mode == "deleted" && (len(values) != 1 || values[0].Identity.NativeType != elasticSanSnapshotType) {
				t.Fatal("full refresh lost survivor", len(values))
			}
			if mode == "root-config" {
				unresolved, err := repo.ListUnresolvedByConnection(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				for _, ref := range unresolved {
					if ref.ControllerID == rootID {
						t.Fatal("fresh root kept stale blocker", ref)
					}
				}
			}
		})
	}
}

func TestElasticSanExternalAbsenceReadBoundaries(t *testing.T) {
	for _, mode := range []string{"retained-index-denied", "own-denied", "live-parent-index404", "availability-change"} {
		t.Run(mode, func(t *testing.T) {
			f := elasticSanRootFixture(t)
			request := f.request(elasticSanVolumeType)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			request.KnownNativeMetadata = map[string]map[string]any{}
			for _, item := range batch.Items {
				request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
				request.KnownNativeMetadata[item.NativeID] = item.Normalized
			}
			if mode != "live-parent-index404" {
				for id := range f.values {
					delete(f.values, id)
				}
			}
			fallback := elasticSanMissingParentIndexes(f.elasticSanFixture)
			collection := f.ids[elasticSanGroupType] + "/volumes"
			calls := 0
			f.hook = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if mode == "own-denied" && path == f.ids[elasticSanVolumeType] {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if path == collection {
					if mode == "retained-index-denied" && r.Header.Get("x-ms-access-soft-deleted-resources") == "true" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					if mode == "live-parent-index404" {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					if mode == "availability-change" {
						calls++
						if calls > 2 {
							return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
						}
					}
				}
				return fallback(r)
			}
			next, err := f.runtime.List(t.Context(), request)
			if err == nil || len(next.AbsentNativeIDs)+len(next.Items) != 0 {
				t.Fatal("uncertain native absence published", mode, next, err)
			}
			if mode == "availability-change" && calls != 4 {
				t.Fatal("both populations/two passes not read", calls)
			}
		})
	}
}

func TestElasticSanExternalAbsenceRetainedAuthority(t *testing.T) {
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType} {
		t.Run(last(kind), func(t *testing.T) {
			f := newElasticSanFixture(t)
			request := f.request(kind)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			request.KnownNativeMetadata = map[string]map[string]any{}
			for _, item := range batch.Items {
				request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
				request.KnownNativeMetadata[item.NativeID] = item.Normalized
			}
			// Retained resources are deliberately addressable only through the retained
			// index in this fixture. Their own404 is not new disappearance evidence.
			for id := range f.values {
				delete(f.values, id)
			}
			f.override = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if strings.HasPrefix(path, f.ids[elasticSanType]+"/") && slices.Contains([]string{"volumegroups", "volumes", "snapshots", "privateendpointconnections"}, last(path)) {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				return nil, false
			}
			next, err := f.runtime.List(t.Context(), request)
			if err == nil || len(next.AbsentNativeIDs)+len(next.Items) != 0 {
				t.Fatal("retained index loss became permanent absence", kind, next, err)
			}
		})
	}
}

func TestElasticSanExternalAbsenceRetainedOwnRead(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanVolumeType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.KnownNativeMetadata = map[string]map[string]any{}
	surviving := map[string]map[string]any{}
	for _, item := range batch.Items {
		request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
		request.KnownNativeMetadata[item.NativeID] = item.Normalized
		if item.Normalized["retained"] == true {
			surviving[item.NativeID] = f.values[item.NativeID]
		}
	}
	for id := range f.values {
		delete(f.values, id)
	}
	f.override = func(r *http.Request) (*http.Response, bool) {
		path := strings.ToLower(r.URL.Path)
		if raw := surviving[path]; raw != nil {
			return jsonResponse(200, raw, nil), true
		}
		if strings.HasPrefix(path, f.ids[elasticSanType]+"/") && slices.Contains([]string{"volumegroups", "volumes", "snapshots"}, last(path)) {
			return jsonResponse(404, map[string]any{}, nil), true
		}
		return nil, false
	}
	next, err := f.runtime.List(t.Context(), request)
	if err != nil || len(next.Items) != 2 || len(next.AbsentNativeIDs) != 1 || next.AbsentNativeIDs[0] != f.ids[elasticSanVolumeType] {
		t.Fatal("live retained own evidence lost", next, err)
	}
	for _, item := range next.Items {
		if item.Normalized["retained"] != true || surviving[item.NativeID] == nil {
			t.Fatal("retained identity changed", item.NativeID)
		}
	}
}
