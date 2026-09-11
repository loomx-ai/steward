package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func newFleetMeshFixture(t *testing.T) (*fleetFixture, string) {
	t.Helper()
	f := newFleetFixture(t)
	profile := fleetTestBody(t, fleetMeshType, "mesh1")
	id := text(profile["id"])
	object(profile["properties"])["futurePrivateSetting"] = "mesh-private-configuration"
	f.resources[id] = profile
	member := f.resources[fleetParent(id, fleetMeshType)+"/members/member1"]
	object(member["properties"])["meshProperties"] = map[string]any{"clusterMeshProfileResourceId": id, "ciliumProperties": map[string]any{"id": float64(1), "name": "cilium-private-member"}, "status": map[string]any{"state": "Connected"}}
	// A selector match is not an applied attachment.
	other := fleetTestBody(t, fleetMemberType, "member2")
	object(other["properties"])["clusterResourceId"] = resourceID(aksType, "cluster2")
	object(other["properties"])["labels"] = map[string]any{"env": "production"}
	f.resources[text(other["id"])] = other
	return f, id
}

func fleetMeshAssets(t *testing.T, f *fleetFixture) []asset.Asset {
	t.Helper()
	values := f.assets(t)
	batch, err := f.runtime.List(t.Context(), f.request(fleetMeshType))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: fleetMeshType, NativeID: item.NativeID}, Location: item.Location, Name: item.Name, Normalized: item.Normalized})
	}
	encoded, _ := json.Marshal(values)
	if err := json.Unmarshal(encoded, &values); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestFleetMeshNativeOperationAndReadVersions(t *testing.T) {
	c := directClient(nil)
	parent := strings.ToLower(resourceID(fleetType, "fleet1"))
	for _, method := range []string{"GET", "PUT", "POST", "DELETE"} {
		var body []map[string]any
		if method == "PUT" {
			body = append(body, map[string]any{"properties": map[string]any{"memberSelector": map[string]any{"byLabel": ""}}})
		}
		bound, err := c.fleetRequest(fleetMeshType, parent, "mesh1", method, body...)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(bound.URL)
		path := parent + "/clustermeshprofiles/mesh1"
		if method == "POST" {
			path += "/apply"
		}
		if !strings.EqualFold(u.Path, path) || u.Query().Get("api-version") != fleetMeshVersion || len(u.Query()) != 1 || bound.Method != method || (len(bound.Body) != 0) != (method == "PUT") {
			t.Fatal("mesh operation escaped the native contract", bound)
		}
	}
	for _, method := range []string{"PUT", "POST", "DELETE"} {
		if _, err := c.fleetRequest(fleetMeshType, parent, "", method); err == nil {
			t.Fatal("mesh collection mutation accepted", method)
		}
	}
	for _, method := range []string{"GET", "DELETE"} {
		bound, err := c.fleetRequest(fleetMemberType, parent, "member1", method)
		u, _ := url.Parse(bound.URL)
		version := fleetVersion
		if method == "GET" {
			version = fleetMeshVersion
		}
		if err != nil || u.Query().Get("api-version") != version {
			t.Fatal("member mesh observation or stable delete version changed", bound, err)
		}
	}
	for _, collection := range []string{"members", "clusterMeshProfiles"} {
		for _, suffix := range []string{"&$filter=clusterMeshProfile%20eq%20mesh1", "&$top=1", "&$select=properties", "&$skiptoken=a&$skiptoken=b"} {
			u, _ := url.Parse(apiURL(parent+"/"+collection, fleetMeshVersion) + suffix)
			if fleetListQuery(u) == nil {
				t.Fatal("filtered mesh discovery became a complete member list", u)
			}
		}
		u, _ := url.Parse(apiURL(parent+"/"+collection, fleetVersion))
		if fleetListQuery(u) == nil {
			t.Fatal("stable API hid native mesh membership", u)
		}
	}
}

func TestFleetMeshInventoryRecordsAppliedMembers(t *testing.T) {
	f, id := newFleetMeshFixture(t)
	batch, err := f.runtime.List(t.Context(), f.request(fleetMeshType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatal("registered mesh inventory failed", batch, err)
	}
	item := batch.Items[0]
	memberID := fleetParent(id, fleetMeshType) + "/members/member1"
	if item.Location != "westus" || len(object(object(item.Normalized[fleetMeshState])["members"])) != 1 || !slices.Equal(stringValues(object(item.Normalized["_fleet_references"])[fleetMemberType]), []string{memberID}) || !slices.Contains(item.NetworkReferences, memberID) {
		t.Fatal("mesh inventory confused selectors and actual members", item)
	}
	encoded, _ := json.Marshal(batch)
	for _, private := range []string{"env=production", "cilium-private-member", "mesh-private-configuration", "memberSelector", "meshProperties", "futurePrivateSetting"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private mesh configuration reached inventory", private)
		}
	}
	request := f.request(fleetMeshType)
	request.KnownNativeIDs = []string{id}
	request.KnownNativeMetadata = map[string]map[string]any{id: item.Normalized}
	f.omitted[id], f.omitted[memberID] = true, true
	clear(f.calls)
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[fleetMeshState])["members"])) != 1 || f.calls["GET "+memberID] < 2 || f.calls["GET "+id] < 2 {
		t.Fatal("known mesh or attached member escaped native recovery", batch, err, f.calls)
	}
	delete(f.resources, memberID)
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[fleetMeshState])["members"])) != 0 {
		t.Fatal("member's own absence did not refresh mesh inventory", batch, err)
	}
}

func TestFleetMeshReferenceGraphAndReverseGuard(t *testing.T) {
	f, id := newFleetMeshFixture(t)
	values := fleetMeshAssets(t, f)
	profile := fleetAssetByKind(t, values, fleetMeshType)
	member := fleetAssetByKind(t, values, fleetMemberType)
	var other asset.Asset
	for _, value := range values {
		if value.Identity.NativeID == fleetParent(id, fleetMeshType)+"/members/member2" {
			other = value
		}
	}
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	c, _ := f.runtime.resolve(t.Context(), "connection")
	contribution, err := c.contributeFleetReferences(t.Context(), profile, values)
	if err != nil || len(contribution.Bindings) != 0 {
		t.Fatal("mesh graph failed or acquired member ownership", contribution, err)
	}
	uses, required := false, false
	for _, edge := range contribution.Relationships {
		if edge.SourceAssetID == profile.ID && edge.TargetAssetID == member.ID && edge.Type == graph.RelationshipUses {
			uses = true
		}
		if edge.SourceAssetID == member.ID && edge.TargetAssetID == profile.ID && edge.Type == graph.RelationshipDependsOn {
			required = edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true && edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
		}
	}
	if !uses || !required {
		t.Fatal("mesh cleanup was not an explicit member prerequisite", contribution)
	}
	f.omitted[id], f.omitted[member.Identity.NativeID] = true, true
	incoming, err := c.monitorIncomingTargets(t.Context(), []asset.Asset{member, other}, values...)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(incoming[member.Identity.NativeID], func(source monitorIncomingSource) bool { return source.resource.id == id }) || slices.ContainsFunc(incoming[other.Identity.NativeID], func(source monitorIncomingSource) bool { return source.resource.id == id }) {
		t.Fatal("reverse guard confused proposed and applied membership", incoming)
	}
	delete(f.resources, id)
	if _, err := c.monitorIncomingTargets(t.Context(), []asset.Asset{member}, values...); err == nil || isNotFound(err) {
		t.Fatal("missing profile erased a live member's mesh attachment", err)
	}
}

func TestFleetMeshInventoryAndGraphBoundaries(t *testing.T) {
	for _, scenario := range []string{"forbidden member", "member list 404", "profile list 404", "missing profile", "unknown member", "retargeted cluster", "changed selector", "changed applied selector", "changed cilium", "invalid member profile", "invalid cilium", "ambiguous member", "tampered state", "tampered references", "late member"} {
		t.Run(scenario, func(t *testing.T) {
			f, id := newFleetMeshFixture(t)
			values := fleetMeshAssets(t, f)
			profile := fleetAssetByKind(t, values, fleetMeshType)
			memberID := fleetParent(id, fleetMeshType) + "/members/member1"
			otherID := fleetParent(id, fleetMeshType) + "/members/member2"
			props := object(f.resources[memberID]["properties"])
			attach := func() { object(f.resources[otherID]["properties"])["meshProperties"] = object(props["meshProperties"]) }
			clear(f.calls)
			switch scenario {
			case "forbidden member", "member list 404", "profile list 404":
				f.override = func(req *http.Request) (*http.Response, bool) {
					path, status := memberID, 403
					if scenario == "member list 404" {
						path, status = fleetParent(id, fleetMeshType)+"/members", 404
					}
					if scenario == "profile list 404" {
						path, status = fleetParent(id, fleetMeshType)+"/clustermeshprofiles", 404
					}
					if strings.EqualFold(req.URL.Path, path) {
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "Denied"}}, nil), true
					}
					return nil, false
				}
			case "missing profile":
				delete(f.resources, id)
			case "unknown member":
				attach()
			case "retargeted cluster":
				props["clusterResourceId"] = resourceID(aksType, "changed")
			case "changed selector":
				object(object(f.resources[id]["properties"])["memberSelector"])["byLabel"] = "env=other"
			case "changed applied selector":
				object(object(object(f.resources[id]["properties"])["status"])["lastAppliedMemberSelector"])["byLabel"] = "env=other"
			case "changed cilium":
				object(object(props["meshProperties"])["ciliumProperties"])["name"] = "changed"
			case "invalid member profile":
				object(props["meshProperties"])["clusterMeshProfileResourceId"] = strings.Replace(id, "/fleets/fleet1/", "/fleets/other/", 1)
			case "invalid cilium":
				object(object(props["meshProperties"])["ciliumProperties"])["id"] = 256
			case "ambiguous member":
				props["MeshProperties"] = props["meshProperties"]
			case "tampered state":
				profile.Normalized = maps.Clone(profile.Normalized)
				profile.Normalized[fleetMeshState] = map[string]any{"members": map[string]any{}}
			case "tampered references":
				profile.Normalized = maps.Clone(profile.Normalized)
				profile.Normalized["_fleet_references"] = map[string]any{fleetType: []string{fleetParent(id, fleetMeshType)}}
			case "late member":
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := fleetParent(id, fleetMeshType) + "/members"
					if strings.EqualFold(req.URL.Path, path) && f.calls["GET "+path] == 2 {
						attach()
					}
					return nil, false
				}
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			if scenario == "profile list 404" || scenario == "missing profile" {
				if _, err := f.runtime.List(t.Context(), f.request(fleetMeshType)); err == nil || isNotFound(err) {
					t.Fatal("failed profile discovery became absence", err)
				}
				return
			}
			result, err := c.contributeFleetReferences(t.Context(), profile, values)
			if err == nil || isNotFound(err) || len(result.Relationships)+len(result.Bindings)+len(result.Unresolved) != 0 {
				t.Fatal("changed or unreadable mesh produced a usable graph", result, err)
			}
			if strings.HasPrefix(scenario, "tampered") && len(f.calls) != 0 {
				t.Fatal("altered mesh proof reached native APIs", f.calls)
			}
		})
	}
}
