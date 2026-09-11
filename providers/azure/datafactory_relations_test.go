package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func dataFactorySharingFixture(t *testing.T, f *dataFactoryFixture, authorization string) (string, string) {
	t.Helper()
	owner := f.ids[dataFactoryIRType]
	root := strings.TrimSuffix(f.ids[dataFactoryType], "/factory") + "/consumer"
	runtime := root + "/integrationruntimes/shared-runtime"
	for _, definition := range []struct{ id, kind string }{{root, dataFactoryType}, {runtime, dataFactoryIRType}} {
		raw := batchClone(f.resources[f.ids[definition.kind]])
		raw["id"], raw["name"], raw["type"] = definition.id, last(definition.id), definition.kind
		if definition.kind == dataFactoryIRType {
			linked := map[string]any{"authorizationType": authorization}
			if authorization == "RBAC" {
				linked["resourceId"] = owner
			} else {
				linked["key"] = map[string]any{"type": "SecureString", "value": "private-shared-runtime-key"}
			}
			object(raw["properties"])["typeProperties"] = map[string]any{"linkedInfo": linked}
		}
		f.resources[definition.id], f.kinds[definition.id] = raw, definition.kind
	}
	status := batchClone(f.statuses[owner])
	status["name"] = last(runtime)
	object(status["properties"])["dataFactoryName"] = last(root)
	object(object(status["properties"])["typeProperties"])["nodes"] = []any{}
	f.statuses[runtime] = status
	object(object(f.statuses[owner]["properties"])["typeProperties"])["links"] = []any{map[string]any{"name": last(runtime), "dataFactoryName": last(root), "subscriptionId": testSubscription, "dataFactoryLocation": "East US", "createTime": "2018-06-14T09:17:45.1839685Z"}}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method != "GET" || !strings.HasPrefix(path, root+"/") {
			return nil, false
		}
		for _, kind := range dataFactoryChildKinds(dataFactoryType) {
			if path != root+"/"+strings.ToLower(last(kind)) {
				continue
			}
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.resources)) {
				if f.kinds[id] == kind && dataFactoryParent(id, kind) == root && !f.omitted[id] {
					rows = append(rows, f.resources[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		return nil, false
	}
	return root, runtime
}

func TestDataFactorySharingResolvesNativeConsumersAndMissingIndexRows(t *testing.T) {
	for _, mode := range []string{"RBAC", "Key", "omitted-consumer", "omitted-link", "stale-link", "foreign-subscription", "unknown-factory"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			authorization := "RBAC"
			if mode == "Key" {
				authorization = mode
			}
			_, consumer := dataFactorySharingFixture(t, f, authorization)
			owner := f.ids[dataFactoryIRType]
			typ := object(object(f.statuses[owner]["properties"])["typeProperties"])
			link := object(array(typ["links"])[0])
			switch mode {
			case "omitted-consumer":
				f.omitted[consumer] = true
			case "omitted-link":
				typ["links"] = []any{}
			case "stale-link":
				delete(f.resources, consumer)
			case "foreign-subscription":
				link["subscriptionId"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
			case "unknown-factory":
				link["dataFactoryName"] = "unseen-factory"
			}
			batch, err := f.runtime.List(t.Context(), f.request(dataFactoryIRType))
			if err != nil {
				t.Fatal(err)
			}
			items := map[string]map[string]any{}
			for _, item := range batch.Items {
				items[item.NativeID] = item.Normalized
			}
			normalized := items[owner]
			incoming := object(object(normalized["_datafactory_incoming"])[owner])
			links := object(object(normalized["_datafactory_links"])[owner])
			if mode != "stale-link" && incoming[consumer] == nil {
				t.Fatal("native consumer dependency missing", mode, incoming)
			}
			if mode == "stale-link" {
				if incoming[consumer] != nil || f.calls["GET "+consumer] < 2 || len(links) != 1 {
					t.Fatal("stale link confused with live consumer", incoming, links)
				}
				for _, entry := range links {
					if object(entry)["source"] != consumer || object(entry)["absent"] != true {
						t.Fatal("stale link was not independently checked", entry)
					}
				}
			} else if mode == "foreign-subscription" || mode == "unknown-factory" {
				if normalized["cleanup_protection_reason"] != "azure_datafactory_shared_runtime_unresolved" {
					t.Fatal("unresolved shared consumers did not protect host", normalized["cleanup_protection_reason"])
				}
				for _, entry := range links {
					if object(entry)["unresolved"] != true || object(entry)["source"] != nil {
						t.Fatal("unobserved consumer received an invented ARM identity", entry)
					}
				}
			} else {
				if normalized["cleanup_protected"] != false || object(incoming[consumer])["configuration"] != items[consumer][dataFactoryConfiguration] {
					t.Fatal("shared consumer configuration not sealed", incoming)
				}
				if mode == "omitted-consumer" && f.calls["GET "+consumer] < 2 {
					t.Fatal("native shared link did not recover omitted consumer")
				}
			}
			payload, _ := json.Marshal(batch)
			if strings.Contains(string(payload), "private-shared-runtime-key") {
				t.Fatal("shared authorization key escaped private inventory")
			}
		})
	}
}

func TestDataFactorySharingRejectsAmbiguousNativeOwnership(t *testing.T) {
	for _, mode := range []string{"wrong-owner", "missing-owner", "managed-consumer", "unlinked-consumer", "consumer-forbidden", "duplicate-link", "self-link", "padded-name", "wrong-subscription", "links-object", "ambiguous-factory", "drift-between-scans"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			root, consumer := dataFactorySharingFixture(t, f, "RBAC")
			owner := f.ids[dataFactoryIRType]
			typ := object(object(f.statuses[owner]["properties"])["typeProperties"])
			link := object(array(typ["links"])[0])
			linked := object(object(object(f.resources[consumer]["properties"])["typeProperties"])["linkedInfo"])
			original := f.override
			switch mode {
			case "wrong-owner":
				linked["resourceId"] = strings.TrimSuffix(owner, "/"+last(owner)) + "/other-owner"
			case "missing-owner":
				delete(linked, "resourceId")
			case "managed-consumer":
				object(f.resources[consumer]["properties"])["type"] = "Managed"
				object(f.statuses[consumer]["properties"])["type"] = "Managed"
			case "unlinked-consumer":
				delete(object(object(f.resources[consumer]["properties"])["typeProperties"]), "linkedInfo")
			case "consumer-forbidden":
				f.omitted[consumer] = true
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, consumer) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return original(req)
				}
			case "duplicate-link":
				typ["links"] = []any{link, batchClone(link)}
			case "self-link":
				link["dataFactoryName"], link["name"] = last(f.ids[dataFactoryType]), last(owner)
			case "padded-name":
				link["name"] = " shared-runtime "
			case "wrong-subscription":
				link["subscriptionId"] = "not-a-subscription"
			case "links-object":
				typ["links"] = map[string]any{}
			case "ambiguous-factory":
				other := strings.Replace(root, "/resourcegroups/test/", "/resourcegroups/other/", 1)
				raw := batchClone(f.resources[root])
				raw["id"] = other
				f.resources[other], f.kinds[other] = raw, dataFactoryType
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasPrefix(strings.ToLower(req.URL.Path), other+"/") {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
					return original(req)
				}
			case "drift-between-scans":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "POST" && strings.EqualFold(req.URL.Path, owner+"/getStatus") && f.calls["POST "+owner+"/getstatus"] > 1 {
						link["createTime"] = "2026-09-01T00:00:00Z"
					}
					return original(req)
				}
			}
			if _, err := f.runtime.List(t.Context(), f.request(dataFactoryIRType)); err == nil {
				t.Fatal("ambiguous shared runtime accepted", mode)
			}
		})
	}
}

func TestDataFactoryReferenceInventoryIncludesReverseDependencies(t *testing.T) {
	f := newDataFactoryFixture(t)
	dataset, pipeline := f.ids[dataFactoryDatasetType], f.ids[dataFactoryPipelineType]
	object(f.resources[pipeline]["properties"])["activities"] = []any{map[string]any{"name": "copy", "type": "Copy", "inputs": []any{map[string]any{"type": "DatasetReference", "referenceName": last(dataset)}}, "typeProperties": map[string]any{"source": map[string]any{"type": "BlobSource"}, "sink": map[string]any{"type": "BlobSink"}}}}
	batch, err := f.runtime.List(t.Context(), f.request(dataFactoryDatasetType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(err)
	}
	incoming := object(object(batch.Items[0].Normalized["_datafactory_incoming"])[dataset])
	entry := object(incoming[pipeline])
	if entry["kind"] != dataFactoryPipelineType || text(entry["configuration"]) == "" {
		t.Fatal("typed native consumer not in sealed reverse dependency index", incoming)
	}
	object(object(f.resources[pipeline]["properties"])["parameters"])["private"] = map[string]any{"type": "String", "defaultValue": "new-secret-value"}
	after, err := f.runtime.List(t.Context(), f.request(dataFactoryDatasetType))
	if err != nil || after.Items[0].Normalized[dataFactoryProof] == batch.Items[0].Normalized[dataFactoryProof] {
		t.Fatal("consumer drift did not change target review binding", err)
	}
}

func TestDataFactoryETagProvenanceIsValidatedAndPrivate(t *testing.T) {
	for _, mode := range []string{"header", "matching-body", "changed-header", "duplicate-header", "body-conflict", "body-number", "empty-body", "wildcard", "comma", "padded"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			id := f.ids[dataFactoryPipelineType]
			raw := f.resources[id]
			delete(raw, "etag")
			delete(raw, "eTag")
			header := http.Header{"Etag": []string{"\"native-header-stamp\""}}
			switch mode {
			case "matching-body":
				raw["etag"] = header.Get("ETag")
			case "duplicate-header":
				header["Etag"] = []string{header.Get("ETag"), header.Get("ETag")}
			case "body-conflict":
				raw["eTag"] = "\"other-stamp\""
			case "body-number":
				raw["eTag"] = 123
			case "empty-body":
				raw["eTag"] = ""
			case "wildcard":
				header.Set("ETag", "*")
			case "comma":
				header.Set("ETag", "one,two")
			case "padded":
				header.Set("ETag", " value ")
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, id) {
					if mode == "changed-header" && f.calls["GET "+id] > 1 {
						header.Set("ETag", "\"changed-stamp\"")
					}
					return jsonResponse(200, raw, header), true
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), f.request(dataFactoryPipelineType))
			ok := mode == "header" || mode == "matching-body"
			if (err == nil) != ok {
				t.Fatal("ETag provenance validation differs", mode, err)
			}
			if ok && batch.Items[0].Normalized["arm_etag"] != "\"native-header-stamp\"" {
				t.Fatal("native header-only ETag lost")
			}
		})
	}
}

func TestDataFactoryKnownConsumersSurviveIncompleteIndexes(t *testing.T) {
	for _, mode := range []string{"pipeline-omitted", "pipeline-gone", "pipeline-forbidden", "key-link-omitted", "key-consumer-omitted", "key-factory-omitted", "key-consumer-gone"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			target, source, kind := f.ids[dataFactoryDatasetType], f.ids[dataFactoryPipelineType], dataFactoryDatasetType
			root := ""
			if strings.HasPrefix(mode, "key-") {
				root, source = dataFactorySharingFixture(t, f, "Key")
				target, kind = f.ids[dataFactoryIRType], dataFactoryIRType
			}
			request := f.request(kind)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				if item.NativeID == target {
					request.KnownNativeIDs, request.KnownNativeMetadata = []string{target}, map[string]map[string]any{target: item.Normalized}
				}
			}
			if object(object(request.KnownNativeMetadata[target]["_datafactory_incoming"])[target])[source] == nil {
				t.Fatal("test did not capture its native consumer")
			}
			f.omitted[source] = true
			if strings.HasPrefix(mode, "key-") {
				object(object(f.statuses[target]["properties"])["typeProperties"])["links"] = []any{}
			}
			if mode == "key-factory-omitted" {
				f.omitted[root] = true
			}
			gone := strings.HasSuffix(mode, "-gone")
			if gone {
				delete(f.resources, source)
			}
			if mode == "pipeline-forbidden" {
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, source) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return nil, false
				}
			}
			for range 2 {
				batch, err := f.runtime.List(t.Context(), request)
				if mode == "pipeline-forbidden" {
					if err == nil {
						t.Fatal("unreadable consumer vanished from dependency index")
					}
					return
				}
				if err != nil {
					t.Fatal("known dependency could not be reconciled", err)
				}
				for _, item := range batch.Items {
					if item.NativeID != target {
						continue
					}
					entry := object(object(object(item.Normalized["_datafactory_incoming"])[target])[source])
					if (entry == nil) != gone {
						t.Fatal("list omission substituted for consumer absence", mode, entry)
					}
					request.KnownNativeMetadata[target] = item.Normalized
				}
			}
		})
	}
}

func TestDataFactoryLinkedNodeViewsDoNotOwnPhysicalRegistrations(t *testing.T) {
	f := newDataFactoryFixture(t)
	_, consumer := dataFactorySharingFixture(t, f, "Key")
	object(object(f.statuses[consumer]["properties"])["typeProperties"])["nodes"] = []any{f.resources[f.ids[dataFactoryNodeType]]}
	batch, err := f.runtime.List(t.Context(), f.request(dataFactoryNodeType))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != f.ids[dataFactoryNodeType] {
		t.Fatal("linked status view invented a second registration owner", err, batch.Items)
	}
	if f.calls["GET "+consumer+"/nodes/node_1"] != 0 {
		t.Fatal("linked runtime status authorized a physical node request")
	}
	node := batch.Items[0]
	if object(object(node.Normalized["_datafactory_incoming"])[node.NativeID])[consumer] == nil {
		t.Fatal("physical node lost its runtime's shared consumer")
	}
	if monitorARMTarget(asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: node.NativeID, NativeType: node.NativeType}}) || node.Normalized[rbacIdentityMetadata] != nil {
		t.Fatal("physical node registration acquired a fictitious ARM managed identity")
	}
}
