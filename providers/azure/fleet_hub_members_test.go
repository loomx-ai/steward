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
)

func newFleetHubMembersFixture(t *testing.T) *fleetHubFixture {
	t.Helper()
	h := newFleetHubFixture(t, true)
	root := "/subscriptions/" + testSubscription
	nativeGroup := root + "/resourceGroups/test"
	external := root + "/resourcegroups/shared"
	zone := external + "/providers/Microsoft.Network/privateDnsZones/internal.example.com"
	disk := external + "/providers/Microsoft.Compute/disks/scale-data"
	replace := strings.NewReplacer(
		resourceID(privateDNSZoneType, "internal.example.com"), zone, strings.ToLower(resourceID(privateDNSZoneType, "internal.example.com")), strings.ToLower(zone),
		resourceID(diskType, "scale-data"), disk, strings.ToLower(resourceID(diskType, "scale-data")), strings.ToLower(disk),
		nativeGroup, h.nodes, strings.ToLower(nativeGroup), h.nodes,
	)
	uniform, _ := uniformScaleSetScenario()
	dns, _ := privateDNSLinkScenario(false)
	for _, source := range []*dnsScenario{uniform, dns} {
		for path, raw := range source.records {
			data, _ := json.Marshal(raw)
			var value map[string]any
			if err := json.Unmarshal([]byte(replace.Replace(string(data))), &value); err != nil {
				t.Fatal(err)
			}
			id, kind, _ := parseID(text(value["id"]))
			value["type"] = kind
			h.resources[id] = value
			h.versions[id] = source.version[path]
			// Generic ARM intentionally omits all nested resources and disks.
			h.omit[id] = !strings.EqualFold(kind, scaleSetType) && !strings.EqualFold(kind, vnetType)
		}
		for path, values := range source.lists {
			version := source.version[path]
			data, _ := json.Marshal(values)
			var updated []any
			if err := json.Unmarshal([]byte(replace.Replace(string(data))), &updated); err != nil {
				t.Fatal(err)
			}
			path = strings.ToLower(replace.Replace(path))
			h.lists[path] = updated
			if version != "" {
				h.versions[path] = version
			}
			if h.versions[path] == "" {
				h.versions[path] = "2024-06-01"
			}
		}
	}
	h.groups[external] = map[string]any{"id": external, "type": groupType, "name": last(external), "location": "westus"}
	vnet := h.nodes + "/providers/microsoft.network/virtualnetworks/vnet1"
	h.lists[vnet+"/subnets"], h.versions[vnet+"/subnets"] = []any{}, "2024-05-01"
	zone = strings.ToLower(zone)
	path := root + "/providers/microsoft.network/privatednszones"
	h.lists[path], h.versions[path] = []any{h.resources[zone]}, "2024-06-01"
	// Unknown contained resources remain visible. They have no invented API.
	id := h.hub + "/providers/contoso.example/controllers/custom"
	h.resources[id] = map[string]any{"id": id, "type": "Contoso.Example/controllers", "name": "custom", "properties": map[string]any{"authored": "unknown-private-secret"}}
	// Native Monitor discovery must recover this omitted group member.
	raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, "actions-2023-01-01/getActionGroup.json"), "actions-2023-01-01/getActionGroup.json")
	id = h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
	raw["id"], raw["name"] = id, "hub-alerts"
	h.resources[id], h.omit[id] = raw, true
	path = root + "/providers/microsoft.insights/actiongroups"
	h.lists[path], h.versions[path] = []any{raw}, "2023-01-01"
	return h
}

func TestFleetHubInventoryCapturesNativeDescendants(t *testing.T) {
	h := newFleetHubMembersFixture(t)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	batch, err := h.runtime.List(ctx, h.request(fleetType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatal("native Hub descendant inventory failed", batch, err)
	}
	item := batch.Items[0]
	c, _ := h.runtime.resolve(ctx, "connection")
	state, err := c.fleetRecordedHub(item.NativeID, item.Normalized)
	if err != nil || state["mode"] != "managed" {
		t.Fatal("Hub descendant receipt failed", state, err)
	}
	members := object(state["members"])
	// Three anchors, nine Uniform resources, VNet/link/automatic DNS record,
	// one unknown resource and an omitted native Monitor action group.
	if len(members) != 17 {
		t.Fatal("incomplete Hub descendant set", slices.Sorted(maps.Keys(members)))
	}
	for id, value := range members {
		member := object(value)
		raw := h.resources[id]
		if raw == nil {
			raw = h.groups[id]
		}
		if text(member["configuration"]) != c.privateConfiguration(insightsWorkspaceResourceSnapshot(raw)) || member["group"] != h.hub && member["group"] != h.nodes {
			t.Fatal("full native configuration or owning group lost", id, member)
		}
		if _, known := findType(text(member["kind"])); known && h.calls["GET "+id] < 2 {
			t.Fatal("known Hub member lacked independent native reads", id)
		}
	}
	for id, raw := range h.resources {
		_, kind, _ := parseID(id)
		if strings.EqualFold(kind, privateDNSZoneType) || strings.HasSuffix(id, "/a/manual") {
			if members[id] != nil {
				t.Fatal("shared DNS zone or manually authored record became Hub-owned", id)
			}
		}
		if strings.HasPrefix(text(raw["type"]), "Contoso.") && h.calls["GET "+id] != 0 {
			t.Fatal("invented API for an unknown contained resource")
		}
	}
	data, _ := json.Marshal(map[string]any{"batch": batch, "logs": logs})
	for _, private := range []string{"hub-private-proof", "hub-authored-secret", "do-not-expose", "secret-in-script", "unknown-private-secret", "emailAddress"} {
		if strings.Contains(string(data), private) {
			t.Fatal("Hub private member configuration leaked", private)
		}
	}
	request := h.request(fleetType)
	data, _ = json.Marshal(item.Normalized)
	var restored map[string]any
	if json.Unmarshal(data, &restored) != nil {
		t.Fatal("cannot restore Hub members")
	}
	request.KnownNativeIDs, request.KnownNativeMetadata = []string{item.NativeID}, map[string]map[string]any{item.NativeID: restored}
	refreshed, err := h.runtime.List(ctx, request)
	if err != nil || !refreshed.Complete || len(refreshed.Items) != 1 || refreshed.Items[0].Normalized[fleetHubProof] != item.Normalized[fleetHubProof] {
		t.Fatal("stable serialized Hub members failed recovery", refreshed, err)
	}
}

func TestFleetHubKnownMemberOwnReads(t *testing.T) {
	for _, mode := range []string{"omitted", "gone", "forbidden", "asynchronous", "changed", "tampered", "unknown-omitted", "external-omitted", "external-gone", "external-forbidden", "external-owner-changed", "registration-link-omitted", "registration-record-omitted"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubMembersFixture(t)
			root := "/subscriptions/" + testSubscription
			zone := root + "/resourcegroups/shared/providers/microsoft.network/privatednszones/internal.example.com"
			extra := zone + "/virtualnetworklinks/lookup"
			expectedCount := 17
			if slices.Contains([]string{"external-omitted", "external-gone", "external-forbidden"}, mode) {
				raw := maps.Clone(h.resources[zone+"/virtualnetworklinks/link1"])
				raw["id"], raw["name"], raw["properties"] = extra, "lookup", maps.Clone(object(raw["properties"]))
				object(raw["properties"])["registrationEnabled"] = false
				h.resources[extra], h.versions[extra] = raw, "2024-06-01"
				h.lists[zone+"/virtualnetworklinks"] = append(h.lists[zone+"/virtualnetworklinks"], raw)
				expectedCount++
			}
			id := h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
			batch, err := h.runtime.List(t.Context(), h.request(fleetType))
			if err != nil {
				t.Fatal("cannot create reviewed Hub", err)
			}
			item := batch.Items[0]
			request := h.request(fleetType)
			request.KnownNativeIDs, request.KnownNativeMetadata = []string{item.NativeID}, map[string]map[string]any{item.NativeID: item.Normalized}
			h.lists[root+"/providers/microsoft.insights/actiongroups"] = []any{}
			before := h.calls["GET "+id]
			switch mode {
			case "gone":
				h.gone[id] = true
			case "forbidden", "asynchronous":
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(map[string]int{"forbidden": 403, "asynchronous": 202}[mode], h.resources[id], nil), true
					}
					return nil, false
				}
			case "changed":
				object(h.resources[id]["properties"])["futureSetting"] = "new-private-value"
			case "tampered":
				object(object(object(item.Normalized[fleetHubState])["members"])[id])["group"] = h.nodes
			case "unknown-omitted":
				h.omit[h.hub+"/providers/contoso.example/controllers/custom"] = true
			case "external-omitted", "external-gone", "external-forbidden":
				h.lists[zone+"/virtualnetworklinks"] = h.lists[zone+"/virtualnetworklinks"][:1]
				if mode == "external-gone" {
					h.gone[extra] = true
					expectedCount--
				}
				if mode == "external-forbidden" {
					h.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, extra) {
							return jsonResponse(403, map[string]any{}, nil), true
						}
						return nil, false
					}
				}
			case "registration-link-omitted", "external-owner-changed":
				h.lists[zone+"/virtualnetworklinks"] = []any{}
				if mode == "external-owner-changed" {
					object(h.resources[zone+"/virtualnetworklinks/link1"]["properties"])["virtualNetwork"] = map[string]any{"id": resourceID(vnetType, "unrelated")}
				}
			case "registration-record-omitted":
				h.lists[zone+"/a"] = h.lists[zone+"/a"][1:]
			}
			result, err := h.runtime.List(t.Context(), request)
			if slices.Contains([]string{"forbidden", "asynchronous", "tampered", "unknown-omitted", "external-owner-changed", "external-forbidden", "registration-link-omitted", "registration-record-omitted"}, mode) {
				if err == nil || result.Complete || len(result.Items)+len(result.AbsentNativeIDs) != 0 {
					t.Fatal("invalid member recovery closed a Fleet scan", result, err)
				}
				if mode == "tampered" && h.calls["GET "+id] != before {
					t.Fatal("tampered membership used the API")
				}
				return
			}
			if err != nil || !result.Complete || len(result.Items) != 1 || h.calls["GET "+id] <= before || len(result.AbsentNativeIDs) != 0 {
				t.Fatal("native member own-read recovery failed", result, err)
			}
			members := object(object(result.Items[0].Normalized[fleetHubState])["members"])
			if mode == "gone" {
				if members[id] != nil || len(members) != 16 {
					t.Fatal("own 404 failed to reconcile an omitted member", members)
				}
			} else if len(members) != expectedCount {
				t.Fatal("omitted live descendants lost", members)
			}
			if (mode == "changed" || mode == "gone" || mode == "external-gone") == (result.Items[0].Normalized[fleetHubProof] == item.Normalized[fleetHubProof]) {
				t.Fatal("member refresh failed to bind full private configuration")
			}
		})
	}
}

func TestFleetHubMemberObservationBoundaries(t *testing.T) {
	for _, mode := range []string{"get-forbidden", "get-missing", "get-asynchronous", "get-operation-header", "get-private-disagrees", "get-id-alias", "get-id-space", "get-type", "get-type-number", "get-type-null", "list-type-number", "list-name-number", "list-owner-number", "list-forbidden", "list-missing", "list-asynchronous", "list-filtered", "list-duplicate", "list-foreign", "monitor-forbidden", "monitor-missing", "nested-aks", "nested-monitor-workspace", "nested-insights", "changed-after-read", "new-between-passes", "unknown-changed-between-passes"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			id := h.nodes + "/providers/microsoft.compute/disks/data"
			raw := map[string]any{"id": id, "type": diskType, "name": "data", "location": "westus", "properties": map[string]any{"uniqueId": "creation-id", "authored": "private-disk-setting"}}
			h.resources[id] = raw
			path := id
			status, header := 200, http.Header{}
			body := any(raw)
			switch mode {
			case "get-forbidden", "get-missing", "get-asynchronous":
				status = map[string]int{"get-forbidden": 403, "get-missing": 404, "get-asynchronous": 202}[mode]
			case "get-operation-header":
				header.Set("Azure-AsyncOperation", apiURL(id+"/operations/pending", "2024-03-02"))
			case "get-private-disagrees":
				changed := maps.Clone(raw)
				changed["properties"] = maps.Clone(object(raw["properties"]))
				object(changed["properties"])["authored"] = "different-private-setting"
				body = changed
			case "get-id-alias", "get-id-space", "get-type", "get-type-number", "get-type-null":
				changed := maps.Clone(raw)
				switch mode {
				case "get-id-alias":
					changed["Id"] = id
				case "get-id-space":
					changed["id"] = " " + id
				case "get-type":
					changed["type"] = vnetType
				case "get-type-number":
					changed["type"] = 42
				case "get-type-null":
					changed["type"] = nil
				}
				body = changed
			case "list-type-number", "list-name-number", "list-owner-number":
				field := map[string]string{"list-type-number": "type", "list-name-number": "name", "list-owner-number": "managedBy"}[mode]
				raw[field], path = 42, ""
			case "list-forbidden", "list-missing", "list-asynchronous":
				path, body = h.nodes+"/resources", map[string]any{"value": []any{raw}}
				status = map[string]int{"list-forbidden": 403, "list-missing": 404, "list-asynchronous": 202}[mode]
			case "list-filtered":
				path = h.nodes + "/resources"
				body = map[string]any{"value": []any{raw}, "nextLink": apiURL(path, resourcesVersion) + "&$filter=name%20eq%20'data'"}
			case "list-duplicate", "list-foreign":
				path = h.nodes + "/resources"
				extra := maps.Clone(raw)
				if mode == "list-foreign" {
					extra["id"] = resourceID(diskType, "foreign")
				}
				body = map[string]any{"value": []any{raw, extra}}
			case "monitor-forbidden", "monitor-missing":
				path = "/subscriptions/" + testSubscription + "/providers/microsoft.insights/actiongroups"
				status = map[string]int{"monitor-forbidden": 403, "monitor-missing": 404}[mode]
				body = map[string]any{}
			case "nested-aks", "nested-monitor-workspace", "nested-insights":
				kind := map[string]string{"nested-aks": aksType, "nested-monitor-workspace": monitorWorkspaceType, "nested-insights": applicationInsightsType}[mode]
				id := h.nodes + "/providers/" + strings.ToLower(kind) + "/unreconciled"
				h.resources[id] = map[string]any{"id": id, "type": kind, "name": "unreconciled", "properties": map[string]any{}}
				path = ""
			case "changed-after-read":
				path = ""
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) && h.calls["GET "+id] == 2 {
						object(raw["properties"])["authored"] = "changed-after-own-get"
					}
					return nil, false
				}
			case "new-between-passes", "unknown-changed-between-passes":
				path = ""
				unknown := h.nodes + "/providers/contoso.example/resources/custom"
				if mode == "unknown-changed-between-passes" {
					h.resources[unknown] = map[string]any{"id": unknown, "type": "Contoso.Example/resources", "name": "custom", "properties": map[string]any{"authored": "before"}}
				}
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, h.nodes+"/resources") && h.calls["GET "+h.nodes+"/resources"] == 2 {
						h.resources[unknown] = map[string]any{"id": unknown, "type": "Contoso.Example/resources", "name": "custom", "properties": map[string]any{"authored": "after"}}
					}
					return nil, false
				}
			}
			if path != "" {
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, path) {
						return jsonResponse(status, body, header), true
					}
					return nil, false
				}
			}
			batch, err := h.runtime.List(t.Context(), h.request(fleetType))
			if err == nil || batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("incomplete or changed Hub descendants accepted", batch, err)
			}
		})
	}
}

func TestFleetHubPagedMembersKeepExtensionsIndependent(t *testing.T) {
	h := newFleetHubFixture(t, false)
	id := h.nodes + "/providers/microsoft.compute/disks/paged"
	raw := map[string]any{"id": id, "type": diskType, "name": "paged", "properties": map[string]any{"uniqueId": "paged-creation"}}
	h.resources[id] = raw
	role := h.hub + "/providers/microsoft.authorization/roleassignments/" + rbacTestRoleName
	diagnostic := h.cluster + "/providers/microsoft.insights/diagnosticsettings/monitor"
	h.resources[role] = map[string]any{"id": role, "type": rbacAssignmentType, "name": rbacTestRoleName}
	h.resources[diagnostic] = map[string]any{"id": diagnostic, "type": diagnosticSettingsType, "name": "monitor", "properties": map[string]any{}}
	secondPages := 0
	h.override = func(req *http.Request) (*http.Response, bool) {
		if !strings.EqualFold(req.URL.Path, h.nodes+"/resources") {
			return nil, false
		}
		if req.URL.Query().Get("api-version") != resourcesVersion || req.Method != "GET" {
			t.Fatal("changed native paged group operation", req.URL)
		}
		body := map[string]any{"value": []any{}, "nextLink": apiURL(h.nodes+"/resources", resourcesVersion) + "&$skiptoken=second"}
		if req.URL.Query().Get("$skiptoken") == "second" {
			secondPages++
			body = map[string]any{"value": []any{raw}}
		}
		return jsonResponse(200, body, nil), true
	}
	batch, err := h.runtime.List(t.Context(), h.request(fleetType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || secondPages != 2 {
		t.Fatal("Hub omitted native continuation", batch, secondPages, err)
	}
	members := object(object(batch.Items[0].Normalized[fleetHubState])["members"])
	if len(members) != 4 || members[id] == nil || members[role] != nil || members[diagnostic] != nil || h.calls["GET "+role]+h.calls["GET "+diagnostic] != 0 {
		t.Fatal("group membership claimed independent extension deletion", members)
	}
}
