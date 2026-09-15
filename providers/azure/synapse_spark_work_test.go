package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func sparkWorkFixture(t *testing.T) (*synapseDataInventoryFixture, *synapseDataClient, synapseDataTarget) {
	t.Helper()
	f := newSynapseDataInventoryFixture(t)
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		f.items[kind]["schedulerInfo"] = map[string]any{"submittedAt": "2025-02-24T09:47:41Z", "currentState": "Scheduled"}
		f.items[kind]["pluginInfo"] = map[string]any{"currentState": "Monitoring"}
		f.items[kind]["result"] = "Uncertain"
	}
	data := f.data
	f.data = func(q *http.Request) *http.Response {
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil)
		}
		return data(q)
	}
	c, err := f.runtime.synapseClient(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.arm.synapseWorkspaceRead(t.Context(), text(f.workspace["id"]))
	if err != nil {
		t.Fatal(err)
	}
	return f, c, synapseDataTarget{workspace: w, pool: batchClone(f.pool)}
}

func TestSynapseSparkWorkManifest(t *testing.T) {
	f, c, target := sparkWorkFixture(t)
	before, err := c.synapseSparkWork(t.Context(), target, nil)
	if err != nil || len(before.manifest) != 4 {
		t.Fatal(before, err)
	}
	encoded, _ := json.Marshal(before.manifest)
	if strings.Contains(string(encoded), "data-secret-canary") || strings.Contains(string(encoded), "jobCreationRequest") {
		t.Fatal("private work escaped")
	}
	var restored map[string]any
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		f.hidden[kind] = true
		endSynapseSpark(f.items[kind])
	}
	after, err := c.synapseSparkWork(t.Context(), target, restored)
	if err != nil || c.arm.privateConfiguration(before.manifest) != c.arm.privateConfiguration(after.manifest) {
		t.Fatal("omitted histories or observational states changed the review", after, err)
	}
	for _, raw := range after.raw {
		if _, ok := raw["schedulerInfo"]; ok && !synapseSparkQuiesced(raw) {
			t.Fatal("fresh completion observation lost")
		}
	}
	delete(f.items, synapseBatchType)
	after, err = c.synapseSparkWork(t.Context(), target, restored)
	if err != nil || len(after.manifest) != 3 {
		t.Fatal("own GET absence not reconciled", after, err)
	}
}

func TestSynapsePoolReferenceSelectors(t *testing.T) {
	for _, tc := range []struct {
		name            string
		value           any
		ref, unresolved bool
	}{
		{"matching", map[string]any{"bigDataPool": map[string]any{"type": "BigDataPoolReference", "referenceName": "POOL"}}, true, false},
		{"other", map[string]any{"targetBigDataPool": map[string]any{"type": "BigDataPoolReference", "referenceName": "other"}}, false, false},
		{"nested", map[string]any{"activities": []any{map[string]any{"typeProperties": map[string]any{"sparkPool": map[string]any{"type": "BigDataPoolReference", "referenceName": "pool"}}}}}, true, false},
		{"expression", map[string]any{"sparkPool": map[string]any{"type": "BigDataPoolReference", "referenceName": map[string]any{"type": "Expression", "value": "@pipeline().parameters.pool"}}}, false, true},
		{"string expression", map[string]any{"sparkPool": map[string]any{"type": "BigDataPoolReference", "referenceName": "@{pipeline().parameters.pool}"}}, false, true},
		{"unknown type", map[string]any{"sparkPool": map[string]any{"type": "FutureReference", "referenceName": "other"}}, false, true},
		{"null", map[string]any{"bigDataPool": nil}, false, true},
		{"none", map[string]any{"activities": []any{}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, u := synapsePoolReferences(tc.value, "pool")
			if r != tc.ref || u != tc.unresolved {
				t.Fatal(r, u)
			}
		})
	}
}

func TestSynapseSparkWorkRejectsUnverifiedChanges(t *testing.T) {
	for _, fault := range []string{"missing incarnation", "detail drift", "index drift", "parent drift", "omitted forbidden", "foreign endpoint", "foreign kind", "selector drift", "foreign omitted identity"} {
		t.Run(fault, func(t *testing.T) {
			f, c, target := sparkWorkFixture(t)
			before, err := c.synapseSparkWork(t.Context(), target, nil)
			if err != nil {
				t.Fatal(err)
			}
			known := batchClone(before.manifest)
			batchID := target.workspace.endpoint + "/livyApi/versions/" + synapseDataVersion + "/sparkPools/pool/batches/0"
			switch fault {
			case "missing incarnation":
				delete(f.items[synapseBatchType], "schedulerInfo")
			case "parent drift":
				object(f.pool["properties"])["nodeCount"] = 99
			case "foreign endpoint":
				object(object(known[batchID])["parameters"])["endpoint"] = "https://other.dev.azuresynapse.net"
			case "foreign kind":
				object(known[batchID])["kind"] = "future"
			case "selector drift":
				object(object(known[batchID])["parameters"])["batchId"] = 12
			case "foreign omitted identity":
				known[batchID+"9"] = known[batchID]
				delete(known, batchID)
				delete(f.items, synapseBatchType)
			default:
				data, reads := f.data, 0
				f.data = func(q *http.Request) *http.Response {
					if strings.HasSuffix(q.URL.Path, "/batches/0") {
						reads++
						if fault == "omitted forbidden" {
							return jsonResponse(403, nil, nil)
						}
						if fault == "detail drift" && reads == 2 {
							object(f.items[synapseBatchType]["schedulerInfo"])["submittedAt"] = "2026-02-24T09:47:41Z"
						}
					}
					if fault == "index drift" && strings.HasSuffix(q.URL.Path, "/batches") && reads != 0 {
						return jsonResponse(200, map[string]any{"from": 0, "total": 0, "sessions": []any{}}, nil)
					}
					return data(q)
				}
				if fault == "omitted forbidden" {
					f.hidden[synapseBatchType] = true
				}
			}
			if _, err := c.synapseSparkWork(t.Context(), target, known); err == nil || isNotFound(err) {
				t.Fatal("unverified work accepted", err)
			}
		})
	}
}

func TestSynapseSparkWorkPipelineDependency(t *testing.T) {
	f, c, target := sparkWorkFixture(t)
	pipeline := map[string]any{"id": target.workspace.id + "/pipelines/pipeline", "name": "pipeline", "type": synapsePipelineDefinition.kind, "properties": map[string]any{"activities": []any{map[string]any{"name": "run", "type": "SynapseNotebook", "typeProperties": map[string]any{"sparkPool": map[string]any{"type": "BigDataPoolReference", "referenceName": "pool"}}}}}}
	data := f.data
	f.data = func(q *http.Request) *http.Response {
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{pipeline}}, nil)
		}
		if q.URL.Path == "/pipelines/pipeline" {
			return jsonResponse(200, pipeline, nil)
		}
		return data(q)
	}
	work, err := c.synapseSparkWork(t.Context(), target, nil)
	if err != nil || object(work.manifest[text(pipeline["id"])])["references"] != true {
		t.Fatal(work, err)
	}
}
