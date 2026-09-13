package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func elasticSanGroupItem(t *testing.T, f *elasticSanFixture, batch contracts.InventoryBatch) contracts.InventoryItem {
	t.Helper()
	for _, item := range batch.Items {
		if item.NativeID == f.ids[elasticSanGroupType] {
			return item
		}
	}
	t.Fatal("missing active group")
	return contracts.InventoryItem{}
}

func TestElasticSanGroupNativeMembershipAndPersistence(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanGroupType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanGroupItem(t, f, batch)
	state, err := f.client.elasticSanGroupRecorded(elasticSanTestAsset(item))
	if err != nil || state["complete"] != true || len(object(state["members"])) != 3 || len(object(state["connections"])) != 1 {
		t.Fatal("incomplete native group context", state, err)
	}
	for field, expected := range map[string]int{"active_volume_count": 1, "retained_volume_count": 1, "snapshot_count": 1, "connection_count": 1, "unverified_connection_count": 0} {
		if item.Normalized[field] != expected {
			t.Fatal("incorrect count", field, item.Normalized[field])
		}
	}
	for _, other := range batch.Items {
		if other.Normalized["retained"] == true && (other.Normalized["members_verified"] != false || other.Normalized["active_volume_count"] != nil) {
			t.Fatal("retained-group history claimed empty membership")
		}
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "private-elastic-") {
		t.Fatal("private child configuration leaked")
	}
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanGroupType}, false, true)
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
		if _, err := c.elasticSanGroupRecorded(stored); err != nil {
			t.Fatal("group context did not survive SQLite/runtime restart", err)
		}
	}
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(item)); err != nil {
		t.Fatal("verified active group did not resolve parent action", err)
	}
}

func TestElasticSanGroupKnownMembersSurviveIndexOmission(t *testing.T) {
	f := newElasticSanFixture(t)
	request := f.request(elasticSanGroupType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanGroupItem(t, f, batch)
	request.KnownNativeIDs = []string{item.NativeID}
	request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
	for _, kind := range []string{elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		f.omitted[f.ids[kind]] = true
	}
	next, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	current := elasticSanGroupItem(t, f, next)
	for _, field := range []string{"active_volume_count", "snapshot_count", "connection_count"} {
		if current.Normalized[field] != 1 {
			t.Fatal("omitted known member disappeared", field)
		}
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	next, err = f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if elasticSanGroupItem(t, f, next).Normalized["snapshot_count"] != 0 {
		t.Fatal("own snapshot absence not reflected")
	}
}

func TestElasticSanGroupContextAndCursorBoundaries(t *testing.T) {
	for _, mode := range []string{"member-proof", "connection-proof", "policy-proof", "foreign-member", "member-change", "new-connection", "denied-member"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanFixture(t)
			request := f.request(elasticSanGroupType)
			request.Limit = 1
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || batch.NextCursor == "" {
				t.Fatal("initial cursor", err)
			}
			item := batch.Items[0]
			request.Cursor = batch.NextCursor
			if strings.HasSuffix(mode, "proof") || mode == "foreign-member" {
				request.Cursor = ""
				meta := maps.Clone(item.Normalized)
				state := maps.Clone(object(meta[elasticSanGroupContext]))
				switch mode {
				case "member-proof":
					state["members"] = map[string]any{}
				case "connection-proof":
					state["connections"] = map[string]any{}
				case "policy-proof":
					state["policy"] = map[string]any{"policyState": "Disabled"}
				case "foreign-member":
					state["members"] = map[string]any{resourceID(vmType, "unreviewed"): map[string]any{"kind": vmType}}
				}
				meta[elasticSanGroupContext] = state
				request.KnownNativeIDs = []string{item.NativeID}
				request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: meta}
				calls := 0
				f.override = func(r *http.Request) (*http.Response, bool) { calls++; return nil, false }
				if _, err := f.runtime.List(t.Context(), request); err == nil || calls != 0 {
					t.Fatal("forged context reached native calls", calls, err)
				}
				return
			}
			switch mode {
			case "member-change":
				object(f.values[f.ids[elasticSanVolumeType]]["properties"])["sizeGiB"] = 16
			case "new-connection":
				raw := batchClone(f.values[f.ids[elasticSanEndpointType]])
				id := f.ids[elasticSanEndpointType] + "-other"
				raw["id"], raw["name"] = id, last(id)
				f.values[id] = raw
			case "denied-member":
				f.override = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, f.ids[elasticSanVolumeType]) {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					return nil, false
				}
			}
			if _, err := f.runtime.List(t.Context(), request); err == nil {
				t.Fatal("changed/incomplete cascade continued old cursor")
			}
		})
	}
}

func TestElasticSanGroupUnknownPrivateMappingStaysExplicit(t *testing.T) {
	f := newElasticSanFixture(t)
	object(f.values[f.ids[elasticSanEndpointType]]["properties"])["groupIds"] = []any{"opaque-group"}
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanGroupType))
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanGroupItem(t, f, batch)
	if item.Normalized["connection_count"] != 1 || item.Normalized["unverified_connection_count"] != 1 || object(object(item.Normalized[elasticSanGroupContext])["connections"])[f.ids[elasticSanEndpointType]] == nil {
		t.Fatal("unresolved private link treated as unrelated")
	}
}

func TestElasticSanRetainedGroupHistoryAndUnavailableSnapshotIndex(t *testing.T) {
	f := newElasticSanFixture(t)
	req := f.request(elasticSanGroupType)
	batch, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanGroupItem(t, f, batch)
	req.KnownNativeIDs = []string{item.NativeID}
	req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
	f.retained[item.NativeID] = true
	object(f.values[item.NativeID]["properties"])["provisioningState"] = "Deleted"
	next, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	current := elasticSanGroupItem(t, f, next)
	state, err := f.client.elasticSanGroupRecorded(elasticSanTestAsset(current))
	if err != nil || state["complete"] != false || len(object(state["members"])) != 3 || current.Normalized["snapshot_count"] != nil {
		t.Fatal("retained historical membership misrepresented", err)
	}
	for _, status := range []int{404, 403} {
		g := newElasticSanFixture(t)
		retainedGroup := g.ids[elasticSanGroupType] + "-retained"
		g.override = func(r *http.Request) (*http.Response, bool) {
			if strings.EqualFold(r.URL.Path, retainedGroup+"/snapshots") {
				return jsonResponse(status, map[string]any{}, nil), true
			}
			return nil, false
		}
		batch, err := g.runtime.List(t.Context(), g.request(elasticSanVolumeType))
		if status == 403 {
			if err == nil {
				t.Fatal("permission failure converted to empty snapshots")
			}
			continue
		}
		if err != nil || !batch.Complete {
			t.Fatal("retained volumes lost with unavailable snapshot index", err)
		}
		found := false
		for _, volume := range batch.Items {
			if strings.HasPrefix(volume.NativeID, retainedGroup+"/") {
				found = true
				if volume.Actionable == nil || *volume.Actionable || volume.Normalized["cleanup_protection_reason"] != "elastic_san_retained_group_snapshot_index_unavailable" {
					t.Fatal("unavailable snapshots authorized volume purge")
				}
				if _, err := g.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(volume)); err == nil {
					t.Fatal("unverified retained volume got driver")
				}
			}
		}
		if !found {
			t.Fatal("retained member disappeared")
		}
	}
}

func TestElasticSanOriginalGroupRetentionPreservesARMIdentity(t *testing.T) {
	values := map[int]map[string]any{}
	for _, index := range []int{23, 28} {
		name := "cli-soft-delete-23.json"
		if index == 28 {
			name = "cli-soft-delete-28.json"
		}
		raw, err := os.ReadFile("fixtures/elastic-san/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if json.Unmarshal(raw, &response) != nil {
			t.Fatal("original recording")
		}
		for _, value := range array(response["value"]) {
			group := object(value)
			if strings.HasSuffix(text(group["id"]), "volume-group000004") {
				values[index] = group
			}
		}
	}
	if text(values[23]["id"]) == "" || values[23]["id"] != values[28]["id"] || object(values[28]["properties"])["provisioningState"] != "Deleted" {
		t.Fatal("native group retention identity changed")
	}
	c := &client{}
	if c.privateConfiguration(hybridComputeChildSnapshot(values[23])) != c.privateConfiguration(hybridComputeChildSnapshot(values[28])) {
		t.Fatal("native retained group changed its creation/configuration identity")
	}
}

func TestElasticSanGroupCreatingVolumeIdentityRemainsVisible(t *testing.T) {
	f := newElasticSanFixture(t)
	props := object(f.values[f.ids[elasticSanVolumeType]]["properties"])
	delete(props, "volumeId")
	props["provisioningState"] = "Creating"
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanGroupType))
	if err != nil {
		t.Fatal("creating member hid group", err)
	}
	item := elasticSanGroupItem(t, f, batch)
	state, err := f.client.elasticSanGroupRecorded(elasticSanTestAsset(item))
	if err != nil || state["complete"] != false || len(object(state["members"])) != 3 || item.Normalized["active_volume_count"] != nil {
		t.Fatal("incomplete member incarnation claimed verified context", err)
	}
}
