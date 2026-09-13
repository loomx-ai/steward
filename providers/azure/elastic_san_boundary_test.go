package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func elasticSanRootItem(t *testing.T, f *elasticSanFixture, batch contracts.InventoryBatch) contracts.InventoryItem {
	t.Helper()
	for _, item := range batch.Items {
		if item.NativeID == f.ids[elasticSanType] {
			return item
		}
	}
	t.Fatal("missing SAN")
	return contracts.InventoryItem{}
}

func TestElasticSanBoundaryNativePopulationsAndRestart(t *testing.T) {
	f := newElasticSanFixture(t)
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanType))
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanRootItem(t, f, batch)
	state, err := f.client.elasticSanBoundaryRecorded(elasticSanTestAsset(item))
	if err != nil || state["complete"] != true || len(object(state["members"])) != 7 {
		t.Fatal("incomplete SAN population", state, err)
	}
	counts := map[string]int{}
	retained := 0
	for _, entry := range object(state["members"]) {
		member := object(entry)
		counts[text(member["kind"])]++
		if member["retained"] == true {
			retained++
		}
	}
	if counts[elasticSanGroupType] != 2 || counts[elasticSanVolumeType] != 3 || counts[elasticSanSnapshotType] != 1 || counts[elasticSanEndpointType] != 1 || retained != 3 {
		t.Fatal("lost native population", counts, retained)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "private-elastic-") {
		t.Fatal("private child configuration leaked")
	}
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(item)); err == nil {
		t.Fatal("inventory boundary alone authorized unimplemented SAN deletion")
	}
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanType}, false, true)
	repo, err = sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	c, err := fresh.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		stored, err := repo.GetAsset(t.Context(), value.ID)
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := c.elasticSanBoundaryRecorded(stored)
		if err != nil || c.privateConfiguration(persisted) != f.client.privateConfiguration(state) {
			t.Fatal("SAN boundary changed across restart", err)
		}
	}
}

func TestElasticSanBoundaryRecoversOmittedGroupsAndDescendants(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanRootItem(t, f, batch)
	request.KnownNativeIDs = []string{item.NativeID}
	request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
	for id := range f.values {
		if id != item.NativeID {
			f.omitted[id] = true
		}
	}
	// This scenario has addressable retained own reads. The default fixture
	// deliberately uses retained-index authority and returns 404 for those GETs.
	f.override = func(r *http.Request) (*http.Response, bool) {
		id := strings.ToLower(r.URL.Path)
		if raw := f.values[id]; raw != nil && f.retained[id] {
			return jsonResponse(200, raw, nil), true
		}
		return nil, false
	}
	next, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	current := elasticSanRootItem(t, f, next)
	if f.client.privateConfiguration(object(current.Normalized[elasticSanBoundary])) != f.client.privateConfiguration(object(item.Normalized[elasticSanBoundary])) {
		t.Fatal("native list omission lost signed child identities")
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	next, err = f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	members := object(object(elasticSanRootItem(t, f, next).Normalized[elasticSanBoundary])["members"])
	if len(members) != 6 || members[f.ids[elasticSanSnapshotType]] != nil {
		t.Fatal("own snapshot absence not reflected")
	}
	// Legacy inventory has no SAN boundary. It must be re-enriched from native APIs.
	f.omitted = map[string]bool{}
	legacy := maps.Clone(item.Normalized)
	delete(legacy, elasticSanBoundary)
	delete(legacy, elasticSanBoundaryProof)
	request.KnownNativeMetadata[item.NativeID] = legacy
	if batch, err := f.runtime.List(t.Context(), request); err != nil || object(elasticSanRootItem(t, f, batch).Normalized[elasticSanBoundary]) == nil {
		t.Fatal("legacy SAN inventory could not refresh", err)
	}
}

func TestElasticSanBoundaryUnavailableRetainedSnapshotIndex(t *testing.T) {
	for _, mode := range []string{"retained404", "active404", "retained403", "retained-volume404", "snapshot-own403"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanFixture(t)
			group := f.ids[elasticSanGroupType] + "-retained"
			// A known child remains individually addressable despite its missing index.
			raw := batchClone(f.values[f.ids[elasticSanSnapshotType]])
			id := group + "/snapshots/known"
			raw["id"], raw["name"] = id, last(id)
			object(raw["properties"])["creationData"] = map[string]any{"sourceId": group + "/volumes/child-1751081600"}
			f.values[id] = raw
			request := f.request(elasticSanType)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			item := elasticSanRootItem(t, f, batch)
			request.KnownNativeIDs = []string{item.NativeID}
			request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
			f.override = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if mode == "active404" && path == f.ids[elasticSanGroupType]+"/snapshots" {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				if mode == "retained-volume404" && path == group+"/volumes" {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				if mode == "snapshot-own403" && path == id {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if path == group+"/snapshots" {
					status := 404
					if mode == "retained403" {
						status = 403
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				return nil, false
			}
			next, err := f.runtime.List(t.Context(), request)
			if mode != "retained404" {
				if err == nil {
					t.Fatal("incomplete/denied boundary accepted")
				}
				return
			}
			if err != nil || !next.Complete {
				t.Fatal("retained parent prevented SAN inventory", err)
			}
			state := object(elasticSanRootItem(t, f, next).Normalized[elasticSanBoundary])
			if state["complete"] != false || object(state["unavailable_snapshot_groups"])[group] != "snapshot_index_not_found" || object(state["members"])[id] == nil {
				t.Fatal("missing index claimed empty or hid known snapshot", state)
			}
			if _, err := f.client.elasticSanBoundaryRecorded(elasticSanTestAsset(elasticSanRootItem(t, f, next))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestElasticSanBoundaryProofAndCursorIsolation(t *testing.T) {
	for _, mode := range []string{"proof", "foreign-member", "connection", "child-change", "child-denied", "new-member", "availability-change"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanFixture(t)
			root := f.ids[elasticSanType]
			other := batchClone(f.values[root])
			other["id"], other["name"] = root+"-other", last(root)+"-other"
			f.values[root+"-other"] = other
			request := f.request(elasticSanType)
			request.Limit = 1
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || batch.NextCursor == "" {
				t.Fatal("initial cursor", err)
			}
			item := elasticSanRootItem(t, f, batch)
			if mode == "proof" || mode == "foreign-member" || mode == "connection" {
				meta := maps.Clone(item.Normalized)
				state := batchClone(object(meta[elasticSanBoundary]))
				if mode == "proof" {
					state["members"] = map[string]any{}
				}
				if mode == "foreign-member" {
					object(state["members"])[root+"-other/volumegroups/group"] = map[string]any{"kind": elasticSanGroupType, "retained": false, "configuration": "foreign"}
				}
				meta[elasticSanBoundary] = state
				if mode == "foreign-member" {
					value := elasticSanTestAsset(item)
					value.Normalized = meta
					meta[elasticSanBoundaryProof] = f.client.elasticSanBoundaryBinding(value, state)
				}
				request.KnownNativeIDs = []string{root}
				request.KnownNativeMetadata = map[string]map[string]any{root: meta}
				if mode == "connection" {
					value := elasticSanTestAsset(item)
					value.Identity.ConnectionID = "other"
					if _, err := f.client.elasticSanBoundaryRecorded(value); err == nil {
						t.Fatal("cross-connection boundary accepted")
					}
					return
				}
				calls := 0
				f.override = func(*http.Request) (*http.Response, bool) { calls++; return nil, false }
				if _, err := f.runtime.List(t.Context(), request); err == nil || calls != 0 {
					t.Fatal("changed proof reached native API", calls, err)
				}
				return
			}
			request.Cursor = batch.NextCursor
			switch mode {
			case "child-change":
				object(f.values[f.ids[elasticSanVolumeType]]["properties"])["sizeGiB"] = 16
			case "new-member":
				raw := batchClone(f.values[f.ids[elasticSanEndpointType]])
				id := f.ids[elasticSanEndpointType] + "-new"
				raw["id"], raw["name"] = id, last(id)
				f.values[id] = raw
			case "child-denied":
				f.override = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, f.ids[elasticSanVolumeType]) {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "availability-change":
				f.override = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, f.ids[elasticSanGroupType]+"-retained/snapshots") {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					return nil, false
				}
			}
			if _, err := f.runtime.List(t.Context(), request); err == nil {
				t.Fatal("changed boundary continued reviewed cursor")
			}
		})
	}
}

func TestElasticSanBoundaryRetainedTransitionUsesSignedHistory(t *testing.T) {
	for _, status := range []string{"Deleting", "Restoring"} {
		t.Run(status, func(t *testing.T) {
			f := newElasticSanFixture(t)
			request := f.request(elasticSanType)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			item := elasticSanRootItem(t, f, batch)
			request.KnownNativeIDs = []string{item.NativeID}
			request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
			retained := f.ids[elasticSanVolumeType] + "-1751081600"
			f.omitted[retained] = true
			object(f.values[retained]["properties"])["provisioningState"] = status
			f.override = func(r *http.Request) (*http.Response, bool) {
				if strings.EqualFold(r.URL.Path, retained) {
					return jsonResponse(200, f.values[retained], nil), true
				}
				return nil, false
			}
			batch, err = f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			entry := object(object(object(elasticSanRootItem(t, f, batch).Normalized[elasticSanBoundary])["members"])[retained])
			if entry["retained"] != true {
				t.Fatal("transition reclassified retained native identity", entry)
			}
		})
	}
}

func TestElasticSanBoundarySeparatesSANs(t *testing.T) {
	f := newElasticSanFixture(t)
	root := f.ids[elasticSanType]
	otherID := root + "-other"
	other := batchClone(f.values[root])
	other["id"], other["name"] = otherID, last(otherID)
	f.values[otherID] = other
	groupID := otherID + "/volumegroups/group"
	group := batchClone(f.values[f.ids[elasticSanGroupType]])
	group["id"], group["name"] = groupID, last(groupID)
	f.values[groupID] = group
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanType))
	if err != nil || len(batch.Items) != 2 {
		t.Fatal("SAN population", err)
	}
	for _, item := range batch.Items {
		state, err := f.client.elasticSanBoundaryRecorded(elasticSanTestAsset(item))
		if err != nil {
			t.Fatal(err)
		}
		members := object(state["members"])
		if item.NativeID == root && (len(members) != 7 || members[groupID] != nil) {
			t.Fatal("foreign SAN group entered boundary")
		}
		if item.NativeID == otherID && (len(members) != 1 || members[groupID] == nil) {
			t.Fatal("SAN boundary inherited unrelated descendants")
		}
	}
}
