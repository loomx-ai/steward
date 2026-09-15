package azure

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func pipelineReferenceActivity(typ, key, reference, name string) map[string]any {
	return map[string]any{"name": "activity", "type": typ, "typeProperties": map[string]any{key: map[string]any{"type": reference, "referenceName": name}}}
}
func newSynapsePipelineFixture(t *testing.T) (*synapseDataInventoryFixture, map[string]any) {
	f := newSynapseDataInventoryFixture(t)
	raw := f.item("Pipeline_GetPipeline")
	raw["properties"] = map[string]any{"activities": []any{
		pipelineReferenceActivity("SynapseNotebook", "notebook", "NotebookReference", "item"),
		pipelineReferenceActivity("SparkJob", "sparkJob", "SparkJobDefinitionReference", "item"),
		pipelineReferenceActivity("ExecutePipeline", "pipeline", "PipelineReference", "child"),
	}, "parameters": map[string]any{"private": "data-secret-canary"}}
	data := f.data
	f.data = func(q *http.Request) *http.Response {
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{raw}}, nil)
		}
		if strings.HasPrefix(q.URL.Path, "/pipelines/") {
			if last(q.URL.Path) == "item" {
				return jsonResponse(200, raw, nil)
			}
			child := batchClone(raw)
			child["id"] = strings.TrimSuffix(text(raw["id"]), "item") + "child"
			child["name"] = "child"
			child["properties"] = map[string]any{}
			return jsonResponse(200, child, nil)
		}
		return data(q)
	}
	return f, raw
}
func TestSynapsePipelineNestedReferences(t *testing.T) {
	ref := pipelineReferenceActivity("SynapseNotebook", "notebook", "NotebookReference", "MixedCase")
	values := []any{ref}
	for _, container := range []string{"ForEach", "Until", "IfCondition", "Switch"} {
		props := map[string]any{"activities": values}
		if container == "IfCondition" {
			props = map[string]any{"ifTrueActivities": values, "ifFalseActivities": []any{}}
		}
		if container == "Switch" {
			props = map[string]any{"cases": []any{map[string]any{"activities": values}}, "defaultActivities": []any{}}
		}
		values = []any{map[string]any{"type": container, "typeProperties": props}}
	}
	refs, unresolved := synapsePipelineActivityReferences("/workspace", values)
	if unresolved || len(refs[synapseNotebookType]) != 1 || refs[synapseNotebookType][0] != "/workspace/notebooks/MixedCase" {
		t.Fatal(refs, unresolved)
	}
	for _, bad := range []any{"@pipeline().parameters.name", map[string]any{"type": "Expression", "value": "data-secret-canary"}, "../other", nil, 42, " name", "name%2fother"} {
		object(object(ref["typeProperties"])["notebook"])["referenceName"] = bad
		refs, unresolved = synapsePipelineActivityReferences("/workspace", values)
		if !unresolved || len(refs) != 0 {
			t.Fatal("dynamic/unsafe reference accepted", bad, refs)
		}
	}
	fake := pipelineReferenceActivity("SetVariable", "value", "NotebookReference", "not-a-dependency")
	refs, unresolved = synapsePipelineActivityReferences("/workspace", []any{fake})
	if unresolved || len(refs) != 0 {
		t.Fatal("user payload interpreted as native reference", refs)
	}
}
func TestSynapsePipelineInventoryAndDependencyFailures(t *testing.T) {
	for _, fault := range []string{"none", "missing", "forbidden", "dynamic", "drift"} {
		t.Run(fault, func(t *testing.T) {
			f, raw := newSynapsePipelineFixture(t)
			reads := 0
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if q.URL.Host == "first.dev.azuresynapse.net" && q.URL.Path == "/notebooks/item" {
					if fault == "missing" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), true
					}
					if fault == "forbidden" {
						return jsonResponse(403, nil, nil), true
					}
				}
				if q.URL.Path == "/pipelines/item" {
					reads++
					if fault == "drift" && reads > 1 {
						object(raw["properties"])["changed"] = true
					}
				}
				return nil, false
			}
			if fault == "dynamic" {
				object(raw["properties"])["activities"] = []any{pipelineReferenceActivity("SynapseNotebook", "notebook", "NotebookReference", "@pipeline().parameters.name")}
			}
			batch, err := f.runtime.List(t.Context(), synapseDataInventoryRequest(f.runtime, synapsePipelineType))
			if fault == "forbidden" || fault == "drift" {
				if err == nil || batch.Complete || len(batch.Items) > 0 {
					t.Fatal(batch, err)
				}
				return
			}
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			if item.Actionable == nil || *item.Actionable {
				t.Fatal("artifact deletion enabled")
			}
			if item.Normalized["_synapse_dynamic_references"] != (fault == "dynamic") {
				t.Fatal(item.Normalized)
			}
			if fault != "dynamic" && !slices.Contains(item.NetworkReferences, strings.ToLower(text(f.items[synapseNotebookType]["id"]))) {
				t.Fatal(item.NetworkReferences)
			}
			if fault == "missing" && len(stringValues(item.Normalized["_synapse_missing_references"])) != 1 {
				t.Fatal(item.Normalized)
			}
			encoded, _ := json.Marshal(batch)
			if bytes.Contains(encoded, []byte("data-secret-canary")) {
				t.Fatal("private authored data leaked")
			}
		})
	}
}
func TestSynapsePipelineRegisteredGraph(t *testing.T) {
	f, _ := newSynapsePipelineFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, synapseSource, repo, registry, []string{synapseType}, false, false)
	azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repo, registry, []string{synapseNotebookType, synapseJobDefinitionType}, false, false)
	values := azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repo, registry, []string{synapsePipelineType}, false, false)
	var pipeline asset.Asset
	for _, v := range values {
		if v.Identity.NativeType == synapsePipelineType {
			pipeline = v
		}
	}
	if pipeline.ID == "" {
		t.Fatal("pipeline not persisted")
	}
	edges, err := repo.Graph().ListRelationships(t.Context(), pipeline.ID)
	if err != nil || len(edges) != 3 {
		t.Fatal("missing workspace/notebook/job edges", edges, err)
	}
	for _, edge := range edges {
		if edge.SourceAssetID != pipeline.ID || edge.Type != graph.RelationshipUses {
			t.Fatal(edge)
		}
	}
	unresolved, err := repo.ListUnresolvedByConnection(t.Context(), "connection")
	found := false
	for _, ref := range unresolved {
		if ref.ControllerID == pipeline.ID && ref.NativeType == synapsePipelineType && strings.HasSuffix(ref.NativeID, "/child") {
			found = true
		}
	}
	if err != nil || !found {
		t.Fatal("unscanned child pipeline dependency lost", unresolved, err)
	}
}

func TestSynapsePipelineKnownAbsence(t *testing.T) {
	f, _ := newSynapsePipelineFixture(t)
	request := synapseDataInventoryRequest(f.runtime, synapsePipelineType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	item := batch.Items[0]
	request.KnownNativeIDs = []string{item.NativeID}
	request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
	status := 200
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		if q.URL.Path == "/pipelines/item" && status != 200 {
			return jsonResponse(status, map[string]any{"error": map[string]any{"code": "Unavailable"}}, nil), true
		}
		return nil, false
	}
	for _, code := range []int{200, 403, 404} {
		status = code
		batch, err = f.runtime.List(t.Context(), request)
		switch code {
		case 200:
			if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal(batch, err)
			}
		case 403:
			if err == nil || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal(batch, err)
			}
		case 404:
			if err != nil || len(batch.Items) != 0 || !slices.Equal(batch.AbsentNativeIDs, []string{item.NativeID}) {
				t.Fatal(batch, err)
			}
		}
	}
}
func TestSynapsePipelineDependencySnapshotAndCursor(t *testing.T) {
	for _, fault := range []string{"none", "dependency drift", "cursor drift"} {
		t.Run(fault, func(t *testing.T) {
			f, raw := newSynapsePipelineFixture(t)
			child := batchClone(raw)
			child["id"] = strings.TrimSuffix(text(raw["id"]), "item") + "child"
			child["name"] = "child"
			child["properties"] = map[string]any{}
			reads := 0
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if q.URL.Path == "/pipelines" {
					row := child
					body := map[string]any{}
					if q.URL.Query().Get("continuationToken") == "next" {
						row = raw
					} else {
						body["nextLink"] = "https://first.dev.azuresynapse.net/pipelines?api-version=" + synapseDataVersion + "&continuationToken=next"
					}
					body["value"] = []any{row}
					return jsonResponse(200, body, nil), true
				}
				if q.URL.Path == "/pipelines/child" {
					return jsonResponse(200, child, nil), true
				}
				if q.URL.Path == "/notebooks/item" {
					reads++
					if fault == "dependency drift" && reads > 1 {
						object(f.items[synapseNotebookType]["properties"])["changed"] = true
					}
				}
				return nil, false
			}
			request := synapseDataInventoryRequest(f.runtime, synapsePipelineType)
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if fault == "dependency drift" {
				if err == nil || len(first.Items) > 0 {
					t.Fatal(first, err)
				}
				return
			}
			if err != nil || first.NextCursor == "" || len(first.Items) != 1 {
				t.Fatal(first, err)
			}
			request.Cursor = first.NextCursor
			if fault == "cursor drift" {
				object(f.items[synapseNotebookType]["properties"])["changed"] = true
			}
			second, err := f.runtime.List(t.Context(), request)
			if fault == "cursor drift" {
				if err == nil || len(second.Items) > 0 {
					t.Fatal(second, err)
				}
				return
			}
			if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
				t.Fatal(second, err)
			}
		})
	}
}

func TestSynapsePipelinePoolAndCaseSensitiveSelectors(t *testing.T) {
	f, raw := newSynapsePipelineFixture(t)
	notebook := f.items[synapseNotebookType]
	notebook["name"] = "MixedCase"
	notebook["id"] = strings.TrimSuffix(text(notebook["id"]), "item") + "MixedCase"
	activity := pipelineReferenceActivity("SynapseNotebook", "notebook", "NotebookReference", "MixedCase")
	object(activity["typeProperties"])["sparkPool"] = map[string]any{"type": "BigDataPoolReference", "referenceName": "pool"}
	object(raw["properties"])["activities"] = []any{activity, activity}
	reads := 0
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Path == "/notebooks/MixedCase" {
			reads++
			return jsonResponse(200, notebook, nil), true
		}
		return nil, false
	}
	batch, err := f.runtime.List(t.Context(), synapseDataInventoryRequest(f.runtime, synapsePipelineType))
	if err != nil || len(batch.Items) != 1 || reads != 2 {
		t.Fatal("original selector or reference deduplication lost", batch, err, reads)
	}
	refs := batch.Items[0].NetworkReferences
	if !slices.Contains(refs, strings.ToLower(text(f.pool["id"]))) || !slices.Contains(refs, strings.ToLower(text(notebook["id"]))) {
		t.Fatal(refs)
	}
}
