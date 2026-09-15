package azure

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSynapseArtifactInventoryWorkReview(t *testing.T) {
	for _, kind := range []string{synapseNotebookType, synapseJobDefinitionType} {
		t.Run(kind, func(t *testing.T) {
			f, _, _ := sparkWorkFixture(t)
			req := synapseDataInventoryRequest(f.runtime, kind)
			first, err := f.runtime.List(t.Context(), req)
			if err != nil || len(first.Items) != 1 {
				t.Fatal(first, err)
			}
			item := first.Items[0]
			review := object(item.Normalized[synapseArtifactReviewKey])
			if review["blocked"] != true || len(object(review["pools"])) != 1 || item.Actionable == nil || *item.Actionable {
				t.Fatal("active workspace jobs not reviewed", item)
			}
			wire, _ := json.Marshal(item.Normalized)
			if bytes.Contains(wire, []byte("data-secret-canary")) {
				t.Fatal("private work data escaped")
			}
			var metadata map[string]any
			if err = json.Unmarshal(wire, &metadata); err != nil {
				t.Fatal(err)
			}
			req.KnownNativeIDs = []string{item.NativeID}
			req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: metadata}
			f.hidePools = true
			f.hidden[synapseBatchType] = true
			for _, kind := range []string{synapseBatchType, synapseSessionType} {
				endSynapseSpark(f.items[kind])
			}
			second, err := f.runtime.List(t.Context(), req)
			if err != nil || len(second.Items) != 1 {
				t.Fatal("known omitted pool/job lost", second, err)
			}
			after := object(second.Items[0].Normalized[synapseArtifactReviewKey])
			pool := object(object(after["pools"])[strings.ToLower(text(f.pool["id"]))])
			if after["blocked"] != false || len(object(pool["work"])) != 2 || *second.Items[0].Actionable {
				t.Fatal("quiescence falsely enabled complete artifact cleanup", second)
			}
			f.secret = "rotated-artifact-review-credential"
			third, err := f.runtime.List(t.Context(), req)
			if err != nil || len(third.Items) != 1 || third.Items[0].Normalized[synapseArtifactProofKey] == second.Items[0].Normalized[synapseArtifactProofKey] {
				t.Fatal("review not refreshed after credential rotation", third, err)
			}
		})
	}
}
func TestSynapseArtifactReviewIncomingPipeline(t *testing.T) {
	for _, kind := range []string{synapseNotebookType, synapseJobDefinitionType} {
		for _, name := range []string{"item", "other", "@pipeline().parameters.name"} {
			t.Run(kind+name, func(t *testing.T) {
				f, _, _ := sparkWorkFixture(t)
				for _, kind := range []string{synapseBatchType, synapseSessionType} {
					endSynapseSpark(f.items[kind])
				}
				typ, key, ref := "SynapseNotebook", "notebook", "NotebookReference"
				if kind == synapseJobDefinitionType {
					typ, key, ref = "SparkJob", "sparkJob", "SparkJobDefinitionReference"
				}
				raw := f.item("Pipeline_GetPipeline")
				raw["properties"] = map[string]any{"activities": []any{pipelineReferenceActivity(typ, key, ref, name)}}
				hidden := false
				f.intercept = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Path == "/pipelines" {
						rows := []any{raw}
						if hidden {
							rows = []any{}
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					if q.URL.Path == "/pipelines/item" {
						return jsonResponse(200, raw, nil), true
					}
					return nil, false
				}
				req := synapseDataInventoryRequest(f.runtime, kind)
				first, err := f.runtime.List(t.Context(), req)
				if err != nil || len(first.Items) != 1 {
					t.Fatal(first, err)
				}
				item := first.Items[0]
				review := object(item.Normalized[synapseArtifactReviewKey])
				if review["blocked"] != (name != "other") || len(object(review["pipelines"])) != 1 {
					t.Fatal(review)
				}
				hidden = true
				req.KnownNativeIDs = []string{item.NativeID}
				req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
				second, err := f.runtime.List(t.Context(), req)
				if err != nil || len(second.Items) != 1 || len(object(object(second.Items[0].Normalized[synapseArtifactReviewKey])["pipelines"])) != 1 {
					t.Fatal("omitted pipeline not checked", second, err)
				}
			})
		}
	}
}
func TestSynapseArtifactReviewRejectsUncertainWork(t *testing.T) {
	for _, fault := range []string{"forbidden", "missing incarnation", "foreign known pool", "pool changed", "pipeline changed"} {
		t.Run(fault, func(t *testing.T) {
			f, _, _ := sparkWorkFixture(t)
			req := synapseDataInventoryRequest(f.runtime, synapseNotebookType)
			first, err := f.runtime.List(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			item := first.Items[0]
			req.KnownNativeIDs = []string{item.NativeID}
			req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: batchClone(item.Normalized)}
			switch fault {
			case "missing incarnation":
				delete(object(f.items[synapseBatchType]["schedulerInfo"]), "submittedAt")
			case "foreign known pool":
				review := object(req.KnownNativeMetadata[item.NativeID][synapseArtifactReviewKey])
				review["pools"] = map[string]any{"/subscriptions/foreign/pool": map[string]any{"name": "pool"}}
			case "forbidden":
				f.intercept = func(q *http.Request) (*http.Response, bool) {
					if strings.Contains(q.URL.Path, "/batches") {
						return jsonResponse(403, nil, nil), true
					}
					return nil, false
				}
			case "pool changed":
				calls := 0
				f.intercept = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
						calls++
						if calls > 2 {
							object(f.pool["properties"])["nodeCount"] = 99
						}
					}
					return nil, false
				}
			case "pipeline changed":
				calls := 0
				raw := f.item("Pipeline_GetPipeline")
				f.intercept = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Path == "/pipelines" {
						calls++
						rows := []any{}
						if calls > 1 {
							rows = append(rows, raw)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					return nil, false
				}
			}
			second, err := f.runtime.List(t.Context(), req)
			if err == nil || second.Complete || len(second.Items)+len(second.AbsentNativeIDs) != 0 {
				t.Fatal("uncertain cleanup context published", second, err)
			}
		})
	}
}
