package azure

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

type dataFactoryFixture struct {
	runtime             *Runtime
	client              *client
	ids, kinds          map[string]string
	resources, statuses map[string]map[string]any
	omitted             map[string]bool
	calls               map[string]int
	group               map[string]any
	locks               []any
	override            func(*http.Request) (*http.Response, bool)
}

func newDataFactoryFixture(t *testing.T) *dataFactoryFixture {
	t.Helper()
	f := &dataFactoryFixture{ids: map[string]string{}, kinds: map[string]string{}, resources: map[string]map[string]any{}, statuses: map[string]map[string]any{}, omitted: map[string]bool{}, calls: map[string]int{}}
	groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	f.group = map[string]any{"id": groupID, "type": groupType, "name": "test", "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.locks = []any{}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	// Join unrelated upstream examples explicitly. Their original files remain
	// unchanged: align factory scope and own resource identity, fix the two
	// documented example identity defects, and use the node own-GET incarnation.
	for _, mapping := range metadata.kinds {
		kind := mapping.NativeType
		if dataFactoryKind(kind) == "" {
			continue
		}
		op, _ := metadata.catalog.Operation(mapping.ReadOperations[0])
		params := object(dataFactoryExample(t, op.Name)["parameters"])
		delete(params, "ifNoneMatch")
		params["subscriptionId"], params["resourceGroupName"], params["factoryName"] = testSubscription, "test", "factory"
		request, err := catalog.BindREST(op, params)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(request.URL)
		id := strings.ToLower(u.Path)
		raw := dataFactoryBody(t, op.Name)
		if kind != dataFactoryNodeType {
			raw["id"], raw["name"], raw["type"] = id, last(id), kind
		}
		raw["futurePrivateSetting"] = "private-native-datafactory-config"
		if kind == dataFactoryPECType {
			object(object(raw["properties"])["privateEndpoint"])["id"] = strings.ToLower(resourceID("Microsoft.Network/privateEndpoints", "connection"))
		}
		f.ids[kind], f.kinds[id], f.resources[id] = id, kind, raw
	}
	runtimeID := f.ids[dataFactoryIRType]
	status := dataFactoryBody(t, "IntegrationRuntimes_GetStatus")
	status["name"] = last(runtimeID)
	props := object(status["properties"])
	props["dataFactoryName"] = "factory"
	object(props["typeProperties"])["nodes"] = []any{f.resources[f.ids[dataFactoryNodeType]]}
	object(props["typeProperties"])["links"] = []any{}
	f.statuses[runtimeID] = status
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		key := req.Method + " " + path
		f.calls[key]++
		if f.override != nil {
			if result, ok := f.override(req); ok {
				return result, nil
			}
		}
		if req.Method == "GET" {
			switch path {
			case groupID, "/subscriptions/" + testSubscription + "/resourcegroups":
				if req.URL.Query().Get("api-version") != resourcesVersion {
					t.Fatal("wrong native group version")
				}
				if path == groupID {
					return jsonResponse(200, f.group, nil), nil
				}
				return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), nil
			case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
				if req.URL.Query().Get("api-version") != locksVersion {
					t.Fatal("wrong native locks version")
				}
				return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
			}
		}
		if req.URL.Host != "management.azure.com" || req.URL.Query().Get("api-version") != dataFactoryVersion || len(req.URL.Query()) != 1 {
			t.Fatal("unexpected Data Factory scope or version", req.Method, req.URL)
		}
		if req.Method == "POST" && strings.HasSuffix(path, "/getstatus") {
			id := strings.TrimSuffix(path, "/getstatus")
			if f.resources[id] == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
			}
			return jsonResponse(200, f.statuses[id], nil), nil
		}
		if req.Method == "POST" && strings.HasSuffix(path, "/geteventsubscriptionstatus") {
			id := strings.TrimSuffix(path, "/geteventsubscriptionstatus")
			return jsonResponse(200, map[string]any{"triggerName": last(id), "status": "Disabled"}, nil), nil
		}
		if req.Method == "POST" && (strings.HasSuffix(path, "/querypipelineruns") || strings.HasSuffix(path, "/querydataflowdebugsessions")) {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if req.Method != "GET" {
			t.Fatal("unexpected Data Factory mutation", req.Method, req.URL)
		}
		if strings.HasSuffix(path, "/status") && strings.Contains(path, "/adfcdcs/") {
			return jsonResponse(200, "Stopped", nil), nil
		}
		if raw := f.resources[path]; raw != nil {
			if f.kinds[path] == dataFactoryNodeType && last(req.URL.Path) != raw["nodeName"] {
				t.Fatal("native node spelling changed", last(req.URL.Path), raw["nodeName"])
			}
			return jsonResponse(200, raw, nil), nil
		}
		rootCollection := "/subscriptions/" + testSubscription + "/providers/microsoft.datafactory/factories"
		for _, mapping := range metadata.kinds {
			kind := mapping.NativeType
			if dataFactoryKind(kind) == "" || kind == dataFactoryNodeType {
				continue
			}
			collection := rootCollection
			if kind != dataFactoryType {
				collection = dataFactoryParent(f.ids[kind], kind) + "/" + strings.ToLower(last(kind))
			}
			if path != collection {
				continue
			}
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.resources)) {
				if f.kinds[id] == kind && !f.omitted[id] && (kind == dataFactoryType || dataFactoryParent(id, kind) == strings.TrimSuffix(path, "/"+strings.ToLower(last(kind)))) {
					rows = append(rows, f.resources[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDataFactoryNativeForest(t *testing.T) {
	f := newDataFactoryFixture(t)
	trees, missing, err := f.client.dataFactoryForest(t.Context(), nil)
	if err != nil || len(missing) != 0 || len(trees) != 1 || len(trees[f.ids[dataFactoryType]].members) != 14 {
		t.Fatal("native Data Factory forest incomplete", len(trees), missing, err)
	}
	tree := trees[f.ids[dataFactoryType]]
	for kind, id := range f.ids {
		member := tree.members[id]
		if member.kind != kind || member.root != f.ids[dataFactoryType] || member.parent != dataFactoryParent(id, kind) || f.calls["GET "+id] < 1 {
			t.Fatal("unverified native tree member", kind)
		}
		if kind == dataFactoryNodeType && member.nodeName != "Node_1" {
			t.Fatal("native node name disappeared")
		}
		if kind == dataFactoryIRType && f.calls["POST "+id+"/getstatus"] != 1 {
			t.Fatal("node collection did not use native GetStatus")
		}
		if _, err := dataFactoryMemberSnapshot(member); err != nil {
			t.Fatal(err)
		}
	}
	if tree.members[f.ids[dataFactoryCDCType]].state != "Stopped" || tree.members[f.ids[dataFactoryTriggerType]].state != "Stopped" {
		t.Fatal("native state was not observed")
	}
}

func TestDataFactoryForestRejectsUnstableOrUnboundResources(t *testing.T) {
	for _, mode := range []string{"root-drift", "child-drift", "foreign-root", "duplicate-root", "wrong-child-name", "collapsed-connection-id", "forbidden-child", "missing-value", "filtered-next-link", "foreign-next-link", "version-next-link", "duplicate-node", "missing-node-name", "node-incarnation", "wrong-runtime-status", "wrong-factory-status", "non-array-nodes", "non-array-links"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			root := f.ids[dataFactoryType]
			dataset := f.ids[dataFactoryDatasetType]
			collection := dataFactoryParent(dataset, dataFactoryDatasetType) + "/datasets"
			ir := f.ids[dataFactoryIRType]
			switch mode {
			case "wrong-child-name":
				f.resources[dataset]["name"] = "unrelated"
			case "collapsed-connection-id":
				f.resources[f.ids[dataFactoryPECType]]["id"] = root
			case "duplicate-node":
				object(object(f.statuses[ir]["properties"])["typeProperties"])["nodes"] = []any{f.resources[f.ids[dataFactoryNodeType]], f.resources[f.ids[dataFactoryNodeType]]}
			case "missing-node-name":
				delete(f.resources[f.ids[dataFactoryNodeType]], "nodeName")
			case "node-incarnation":
				changed := batchClone(f.resources[f.ids[dataFactoryNodeType]])
				changed["registerTime"] = "2026-01-01T00:00:00Z"
				object(object(f.statuses[ir]["properties"])["typeProperties"])["nodes"] = []any{changed}
			case "wrong-runtime-status":
				f.statuses[ir]["name"] = "other"
			case "wrong-factory-status":
				object(f.statuses[ir]["properties"])["dataFactoryName"] = "other"
			case "non-array-nodes":
				object(object(f.statuses[ir]["properties"])["typeProperties"])["nodes"] = map[string]any{}
			case "non-array-links":
				object(object(f.statuses[ir]["properties"])["typeProperties"])["links"] = map[string]any{}
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				switch mode {
				case "root-drift", "child-drift":
					id := root
					if mode == "child-drift" {
						id = dataset
					}
					if path == id && (mode == "child-drift" || f.calls["GET "+id] > 1) {
						changed := batchClone(f.resources[id])
						changed["futurePrivateSetting"] = "changed"
						return jsonResponse(200, changed, nil), true
					}
				case "foreign-root", "duplicate-root":
					if strings.HasSuffix(path, "/providers/microsoft.datafactory/factories") {
						changed := batchClone(f.resources[root])
						rows := []any{changed}
						if mode == "foreign-root" {
							changed["id"] = strings.Replace(root, testSubscription, testTenant, 1)
						} else {
							rows = append(rows, changed)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
				case "forbidden-child":
					if path == collection {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
				case "missing-value":
					if path == collection {
						return jsonResponse(200, map[string]any{}, nil), true
					}
				case "filtered-next-link", "foreign-next-link", "version-next-link":
					if path == collection {
						next := apiURL(collection, dataFactoryVersion) + "&$filter=name%20eq%20'x'"
						if mode == "foreign-next-link" {
							next = apiURL(strings.Replace(collection, "/factory/", "/another/", 1), dataFactoryVersion)
						}
						if mode == "version-next-link" {
							next = apiURL(collection, "2020-01-01")
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
					}
				}
				return nil, false
			}
			trees, _, err := f.client.dataFactoryForest(t.Context(), nil)
			if mode == "non-array-links" && err == nil {
				_, err = dataFactoryMemberSnapshot(trees[root].members[ir])
			}
			if err == nil {
				t.Fatal("unbound or unstable native observation accepted")
			}
		})
	}
}

func TestDataFactoryKnownOmissionsNeedOwnAbsence(t *testing.T) {
	for _, mode := range []string{"omitted-child", "omitted-root", "gone-child", "gone-root", "surviving-child", "forbidden-known", "omitted-node"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			root, id, kind := f.ids[dataFactoryType], f.ids[dataFactoryDatasetType], dataFactoryDatasetType
			if mode == "omitted-node" {
				id, kind = f.ids[dataFactoryNodeType], dataFactoryNodeType
			}
			hints := map[string]dataFactoryMember{root: {id: root, kind: dataFactoryType, root: root}, id: {id: id, kind: kind, root: root, parent: dataFactoryParent(id, kind)}}
			if kind == dataFactoryNodeType {
				node := hints[id]
				node.nodeName = "Node_1"
				hints[id] = node
				ir := f.ids[dataFactoryIRType]
				object(object(f.statuses[ir]["properties"])["typeProperties"])["nodes"] = []any{}
			} else {
				f.omitted[id] = true
			}
			switch mode {
			case "omitted-root":
				f.omitted[root] = true
			case "gone-child":
				delete(f.resources, id)
			case "gone-root":
				delete(f.resources, id)
				delete(f.resources, root)
			case "surviving-child":
				delete(f.resources, root)
			case "forbidden-known":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return nil, false
				}
			}
			trees, missing, err := f.client.dataFactoryForest(t.Context(), hints)
			if mode == "surviving-child" || mode == "forbidden-known" {
				if err == nil {
					t.Fatal("known resource lost without own absence")
				}
				return
			}
			if err != nil || f.calls["GET "+id] < 1 {
				t.Fatal("known identity was not independently verified", err)
			}
			gone := mode == "gone-child" || mode == "gone-root"
			if missing[id] != gone || !gone && trees[root].members[id].id != id {
				t.Fatal("known omission was mistaken for absence", missing)
			}
		})
	}
}
