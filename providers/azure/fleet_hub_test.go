package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These composed ARM/AKS responses exercise the ownership joins. The native
// Fleet CLI recordings contain no hub AKS or managed-resource-group reads.
type fleetHubFixture struct {
	*fleetFixture
	fleet, hub, nodes, cluster string
	groups, resources          map[string]map[string]any
	lists                      map[string][]any
	versions                   map[string]string
	omit, gone                 map[string]bool
	override                   func(*http.Request) (*http.Response, bool)
}

func newFleetHubFixture(t *testing.T, private bool) *fleetHubFixture {
	t.Helper()
	f := newFleetFixture(t)
	root := "/subscriptions/" + testSubscription
	h := &fleetHubFixture{fleetFixture: f, fleet: strings.ToLower(resourceID(fleetType, "fleet1")), hub: root + "/resourcegroups/native-hub-owner", nodes: root + "/resourcegroups/native-node-owner", groups: map[string]map[string]any{text(f.group["id"]): f.group}, resources: map[string]map[string]any{}, lists: map[string][]any{}, versions: map[string]string{}, omit: map[string]bool{}, gone: map[string]bool{}}
	h.cluster = h.hub + "/providers/microsoft.containerservice/managedclusters/hub"
	fqdn, field := "hub-private-proof.hcp.westus.azmk8s.io", "fqdn"
	if private {
		fqdn, field = "hub-private-proof.privatelink.westus.azmk8s.io", "privateFQDN"
	}
	profile := object(object(f.resources[h.fleet]["properties"])["hubProfile"])
	profile["fqdn"] = fqdn
	object(profile["apiServerAccessProfile"])["enablePrivateCluster"] = private
	h.resources[h.cluster] = map[string]any{"id": h.cluster, "type": aksType, "name": "hub", "location": "westus", "etag": "hub-etag", "properties": map[string]any{field: fqdn, "nodeResourceGroup": last(h.nodes), "provisioningState": "Succeeded", "apiServerAccessProfile": map[string]any{"enablePrivateCluster": private}, "futurePrivateSetting": "hub-authored-secret"}}
	for id, owner := range map[string]string{h.hub: h.fleet, h.nodes: h.cluster} {
		h.groups[id] = map[string]any{"id": id, "type": groupType, "name": last(id), "location": "westus", "managedBy": owner, "properties": map[string]any{"provisioningState": "Succeeded"}}
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if h.override != nil {
			if res, handled := h.override(req); handled {
				return res, true
			}
		}
		path := strings.ToLower(req.URL.Path)
		if fleetPath(path) || strings.HasSuffix(path, "/locks") {
			return nil, false
		}
		if _, explicit := h.lists[path]; !explicit {
			if response, handled := emptyMonitorIndexResponse(t, req); handled {
				return response, true
			}
		}
		if req.Method != "GET" || len(req.URL.Query()) != 1 || req.URL.Host != "management.azure.com" {
			t.Fatal("hub inventory made an unexpected request", req.Method, req.URL)
		}
		version := resourcesVersion
		if raw := h.resources[path]; raw != nil {
			if kind, known := findType(text(raw["type"])); known {
				version = kind.Version
			}
		}
		if h.versions[path] != "" {
			version = h.versions[path]
		}
		if req.URL.Query().Get("api-version") != version {
			t.Fatal("hub inventory changed native API version", req.URL)
		}
		if h.gone[path] {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		if group := h.groups[path]; group != nil {
			return jsonResponse(200, group, nil), true
		}
		if resource := h.resources[path]; resource != nil {
			return jsonResponse(200, resource, nil), true
		}
		if values, ok := h.lists[path]; ok {
			return jsonResponse(200, map[string]any{"value": values}, nil), true
		}
		values := []any{}
		if path == root+"/resourcegroups" {
			for _, id := range slices.Sorted(maps.Keys(h.groups)) {
				if !h.omit[id] {
					values = append(values, h.groups[id])
				}
			}
		} else if strings.HasSuffix(path, "/resources") && h.groups[strings.TrimSuffix(path, "/resources")] != nil {
			group := strings.TrimSuffix(path, "/resources")
			for _, id := range slices.Sorted(maps.Keys(h.resources)) {
				if inResourceGroup(id, group) && !h.omit[id] {
					values = append(values, h.resources[id])
				}
			}
		} else {
			t.Fatal("hub inventory requested an unrelated resource", req.URL)
		}
		return jsonResponse(200, map[string]any{"value": values}, nil), true
	}
	return h
}

func TestFleetHubRegisteredInventoryAnchors(t *testing.T) {
	for _, mode := range []string{"public", "private", "hubless", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, mode == "private")
			if mode == "hubless" {
				delete(object(h.fleetFixture.resources[h.fleet]["properties"]), "hubProfile")
			}
			if mode == "hubless" || mode == "unverified" {
				delete(h.groups, h.hub)
				delete(h.groups, h.nodes)
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			batch, err := h.runtime.List(ctx, h.request(fleetType))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("native hub scan failed", batch, err)
			}
			item := batch.Items[0]
			client, _ := h.runtime.resolve(t.Context(), "connection")
			state, err := client.fleetRecordedHub(item.NativeID, item.Normalized)
			if err != nil || state["location"] != "westus" || item.Actionable == nil || *item.Actionable {
				t.Fatal("hub anchors or root cleanup boundary lost", item, err)
			}
			want := map[string]string{"public": "managed", "private": "managed", "hubless": "none", "unverified": "unverified"}[mode]
			if state["mode"] != want {
				t.Fatal("wrong hub ownership mode", state)
			}
			if want == "managed" {
				if state["group"] != h.hub || state["cluster"] != h.cluster || state["node_group"] != h.nodes || len(object(state["anchors"])) != 3 {
					t.Fatal("missing native bidirectional anchors", state)
				}
				for _, id := range []string{h.hub, h.cluster, h.nodes} {
					if h.calls["GET "+id] < 2 || text(object(object(state["anchors"])[id])["configuration"]) == "" {
						t.Fatal("anchor lacked independent GET or full configuration proof", id)
					}
				}
			}
			data, _ := json.Marshal(batch)
			if strings.Contains(string(data), "hub-authored-secret") || strings.Contains(string(data), "hub-private-proof") || strings.Contains(string(data), "hub-etag") {
				t.Fatal("private cluster or endpoint leaked into Fleet inventory")
			}
			logData, _ := json.Marshal(logs)
			if strings.Contains(string(logData), "hub-authored-secret") || strings.Contains(string(logData), "hub-private-proof") || strings.Contains(string(logData), "hub-etag") || strings.Contains(string(logData), "futurePrivateSetting") || len(logs) == 0 {
				t.Fatal("Hub observation log leaked private configuration")
			}
			var serialized contracts.InventoryBatch
			if json.Unmarshal(data, &serialized) != nil {
				t.Fatal("cannot restore Fleet hub state")
			}
			if _, err := client.fleetRecordedHub(item.NativeID, serialized.Items[0].Normalized); err != nil {
				t.Fatal("hub proof failed JSON recovery", err)
			}
			if slices.Contains(item.NetworkReferences, h.cluster) || len(item.NetworkReferences) != 1 && mode != "hubless" {
				t.Fatal("hub ownership was published as an ordinary external dependency", item.NetworkReferences)
			}
		})
	}
}

func TestFleetHubOwnershipBoundaries(t *testing.T) {
	for _, mode := range []string{"group-owner-empty", "group-owner-foreign", "group-owner-alias", "second-group", "group-forbidden", "group-missing", "group-asynchronous", "group-filtered-list", "cluster-missing", "cluster-forbidden", "cluster-asynchronous", "cluster-location", "cluster-endpoint", "cluster-endpoint-alias", "cluster-access", "cluster-access-type", "cluster-node-group-self", "cluster-node-group-root", "cluster-node-group-space", "cluster-node-group-alias", "cluster-id", "cluster-list-disagrees", "duplicate-cluster", "second-cluster", "member-outside-group", "nodes-owner-empty", "nodes-owner-foreign", "nodes-forbidden", "nodes-missing", "hub-fqdn-missing", "hub-fqdn-url", "hubless-with-group"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			cluster := h.resources[h.cluster]
			props := object(cluster["properties"])
			profile := object(object(h.fleetFixture.resources[h.fleet]["properties"])["hubProfile"])
			path, status := "", 0
			switch mode {
			case "group-owner-empty":
				delete(h.groups[h.hub], "managedBy")
			case "group-owner-foreign":
				h.groups[h.hub]["managedBy"] = resourceID(fleetType, "another")
			case "group-owner-alias":
				h.groups[h.hub]["ManagedBy"] = h.fleet
			case "second-group":
				extra := maps.Clone(h.groups[h.hub])
				extra["id"], extra["name"] = h.hub+"2", last(h.hub)+"2"
				h.groups[h.hub+"2"] = extra
			case "group-forbidden", "group-missing", "group-asynchronous":
				path, status = h.hub, map[string]int{"group-forbidden": 403, "group-missing": 404, "group-asynchronous": 202}[mode]
			case "group-filtered-list":
				path = h.hub + "/resources"
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.ToLower(req.URL.Path) == path {
						return jsonResponse(200, map[string]any{"value": []any{cluster}, "nextLink": apiURL(path, resourcesVersion) + "&$filter=name%20eq%20'hub'"}, nil), true
					}
					return nil, false
				}
			case "cluster-missing", "cluster-forbidden", "cluster-asynchronous":
				path, status = h.cluster, map[string]int{"cluster-missing": 404, "cluster-forbidden": 403, "cluster-asynchronous": 202}[mode]
			case "cluster-location":
				cluster["location"] = "eastus"
			case "cluster-endpoint":
				props["fqdn"] = "other.hcp.westus.azmk8s.io"
			case "cluster-endpoint-alias":
				props["Fqdn"] = props["fqdn"]
			case "cluster-access":
				object(props["apiServerAccessProfile"])["enablePrivateCluster"] = true
			case "cluster-access-type":
				props["apiServerAccessProfile"] = "private"
			case "cluster-node-group-self":
				props["nodeResourceGroup"] = last(h.hub)
			case "cluster-node-group-root":
				props["nodeResourceGroup"] = "test"
			case "cluster-node-group-space":
				props["nodeResourceGroup"] = " " + last(h.nodes)
			case "cluster-node-group-alias":
				props["NodeResourceGroup"] = last(h.nodes)
			case "cluster-id", "cluster-list-disagrees":
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.ToLower(req.URL.Path) == h.cluster {
						changed := maps.Clone(cluster)
						if mode == "cluster-id" {
							changed["id"] = h.cluster + "2"
						} else {
							changed["properties"] = maps.Clone(props)
							object(changed["properties"])["futurePrivateSetting"] = "different-private-config"
						}
						return jsonResponse(200, changed, nil), true
					}
					return nil, false
				}
			case "second-cluster":
				extra := maps.Clone(cluster)
				extra["id"], extra["name"] = h.cluster+"2", "hub2"
				h.resources[h.cluster+"2"] = extra
			case "duplicate-cluster", "member-outside-group":
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.ToLower(req.URL.Path) == h.hub+"/resources" {
						extra := cluster
						if mode == "member-outside-group" {
							extra = nativeResource(aksType, "member", "westus", map[string]any{})
						}
						return jsonResponse(200, map[string]any{"value": []any{cluster, extra}}, nil), true
					}
					return nil, false
				}
			case "nodes-owner-empty":
				delete(h.groups[h.nodes], "managedBy")
			case "nodes-owner-foreign":
				h.groups[h.nodes]["managedBy"] = resourceID(aksType, "member")
			case "nodes-forbidden", "nodes-missing":
				path, status = h.nodes, map[string]int{"nodes-forbidden": 403, "nodes-missing": 404}[mode]
			case "hub-fqdn-missing":
				delete(profile, "fqdn")
			case "hub-fqdn-url":
				profile["fqdn"] = "https://hub.hcp.westus.azmk8s.io"
			case "hubless-with-group":
				delete(object(h.fleetFixture.resources[h.fleet]["properties"]), "hubProfile")
			}
			if status != 0 {
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.ToLower(req.URL.Path) == path {
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "Incomplete"}}, nil), true
					}
					return nil, false
				}
			}
			batch, err := h.runtime.List(t.Context(), h.request(fleetType))
			if err == nil {
				if len(batch.Items) != 1 || object(batch.Items[0].Normalized[fleetHubState])["mode"] != "unverified" || text(object(batch.Items[0].Normalized[fleetHubState])["reason"]) == "" || *batch.Items[0].Actionable {
					t.Fatal("unproven hub ownership accepted", mode, batch)
				}
			} else if len(batch.Items)+len(batch.AbsentNativeIDs) != 0 || batch.Complete {
				t.Fatal("incomplete hub observations published partial/absent inventory", mode, batch, err)
			}
		})
	}
}

func TestFleetHubKnownAnchorsAndProofRecovery(t *testing.T) {
	for _, mode := range []string{"group-omitted", "cluster-omitted", "nodes-omitted", "all-omitted", "known-cluster-gone", "known-group-gone", "tampered-group", "tampered-binding", "changed-id", "changed-private"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			request := h.request(fleetType)
			batch, err := h.runtime.List(t.Context(), request)
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("initial hub scan failed", err)
			}
			request.KnownNativeIDs = []string{h.fleet}
			request.KnownNativeMetadata = map[string]map[string]any{h.fleet: batch.Items[0].Normalized}
			data, _ := json.Marshal(request)
			var restored contracts.InventoryRequest
			if json.Unmarshal(data, &restored) != nil {
				t.Fatal("invalid saved inventory request")
			}
			before := maps.Clone(h.calls)
			switch mode {
			case "group-omitted", "known-group-gone":
				h.omit[h.hub] = true
			case "cluster-omitted", "known-cluster-gone":
				h.omit[h.cluster] = true
			case "nodes-omitted":
				h.omit[h.nodes] = true
			case "all-omitted":
				h.omit[h.hub], h.omit[h.cluster], h.omit[h.nodes] = true, true, true
			case "tampered-group":
				object(restored.KnownNativeMetadata[h.fleet][fleetHubState])["group"] = h.nodes
			case "tampered-binding":
				restored.KnownNativeMetadata[h.fleet][fleetHubProof] = "forged"
			case "changed-id":
				other := strings.ToLower(resourceID(fleetType, "another"))
				restored.KnownNativeIDs = []string{other}
				restored.KnownNativeMetadata = map[string]map[string]any{other: restored.KnownNativeMetadata[h.fleet]}
			case "changed-private":
				object(h.resources[h.cluster]["properties"])["futurePrivateSetting"] = "new-authored-value"
			}
			if mode == "known-group-gone" {
				h.gone[h.hub] = true
			}
			if mode == "known-cluster-gone" {
				h.gone[h.cluster] = true
			}
			after, err := h.runtime.List(t.Context(), restored)
			if strings.HasPrefix(mode, "tampered") || mode == "changed-id" {
				if err == nil || !maps.Equal(before, h.calls) {
					t.Fatal("changed hub receipt reached the API", mode, err)
				}
				return
			}
			if err != nil || len(after.Items) != 1 || len(after.AbsentNativeIDs) != 0 {
				t.Fatal("known native hub reconciliation failed", mode, after, err)
			}
			state := object(after.Items[0].Normalized[fleetHubState])
			if strings.HasSuffix(mode, "gone") {
				if state["mode"] != "unverified" {
					t.Fatal("missing native anchor retained ownership", state)
				}
			} else if state["mode"] != "managed" {
				t.Fatal("omitted native anchor was not recovered", state)
			}
			if mode == "changed-private" {
				if after.Items[0].Normalized[fleetHubProof] == batch.Items[0].Normalized[fleetHubProof] {
					t.Fatal("fresh inventory did not record private Hub change")
				}
			} else if !strings.HasSuffix(mode, "gone") && after.Items[0].Normalized[fleetHubProof] != batch.Items[0].Normalized[fleetHubProof] {
				t.Fatal("list omission changed the proven hub identity")
			}
		})
	}
}

func TestFleetHubChangesBetweenInventoryObservations(t *testing.T) {
	for _, mode := range []string{"hub-private", "hub-owner", "node-owner", "hub-profile"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			passes := 0
			h.override = func(req *http.Request) (*http.Response, bool) {
				if strings.ToLower(req.URL.Path) == "/subscriptions/"+testSubscription+"/resourcegroups" {
					passes++
					if passes == 2 {
						switch mode {
						case "hub-private":
							object(h.resources[h.cluster]["properties"])["futurePrivateSetting"] = "changed"
						case "hub-owner":
							h.groups[h.hub]["managedBy"] = resourceID(fleetType, "new")
						case "node-owner":
							h.groups[h.nodes]["managedBy"] = resourceID(aksType, "new")
						case "hub-profile":
							object(object(h.fleetFixture.resources[h.fleet]["properties"])["hubProfile"])["fqdn"] = "new.hcp.westus.azmk8s.io"
						}
					}
				}
				return nil, false
			}
			batch, err := h.runtime.List(t.Context(), h.request(fleetType))
			if err == nil || batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("hub change during two-pass scan accepted", mode, batch, err)
			}
		})
	}
}

func TestFleetHubInventoryCursorBindsAnchors(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "changed"}[changed], func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			other := fleetTestBody(t, fleetType, "fleet2")
			delete(object(other["properties"]), "hubProfile")
			h.fleetFixture.resources[text(other["id"])] = other
			request := h.request(fleetType)
			request.Limit = 1
			first, err := h.runtime.List(t.Context(), request)
			if err != nil || len(first.Items) != 1 || first.Complete || first.NextCursor == "" {
				t.Fatal("Fleet paging failed", first, err)
			}
			if changed {
				object(h.resources[h.cluster]["properties"])["futurePrivateSetting"] = "changed-between-pages"
			}
			request.Cursor = first.NextCursor
			second, err := h.runtime.List(t.Context(), request)
			if changed {
				if err == nil || second.Complete || len(second.Items)+len(second.AbsentNativeIDs) != 0 {
					t.Fatal("Fleet cursor ignored a private Hub change", second, err)
				}
			} else if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID != text(other["id"]) {
				t.Fatal("stable Fleet cursor failed", second, err)
			}
		})
	}
}
