package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseTransportFixture struct {
	runtime         *Runtime
	workspace, pool map[string]any
	secret          string
	scopes, reads   map[string]int
	dataCalls       int
	data            func(*http.Request) *http.Response
	override        func(*http.Request) (*http.Response, bool)
}

func newSynapseTransportFixture(t *testing.T) *synapseTransportFixture {
	t.Helper()
	inv := newSynapseInventoryFixture(t)
	workspaceID := strings.ToLower(resourceID(synapseType, "first"))
	f := &synapseTransportFixture{workspace: batchClone(inv.objects[workspaceID]), pool: batchClone(inv.objects[workspaceID+"/bigdatapools/pool"]), secret: "explicit-secret", scopes: map[string]int{}, reads: map[string]int{}}
	object(f.workspace["properties"])["connectivityEndpoints"] = map[string]any{"dev": "https://first.dev.azuresynapse.net"}
	var err error
	f.runtime, err = NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) {
		c := testCredential()
		c.Values = maps.Clone(c.Values)
		c.Values["client_secret"] = f.secret
		return c, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.reads[path]++
		if req.URL.Host == "login.microsoftonline.com" {
			if err := req.ParseForm(); err != nil || req.Method != "POST" || path != "/"+testTenant+"/oauth2/v2.0/token" || req.Form.Get("client_id") != testApplication || req.Form.Get("client_secret") != f.secret {
				t.Fatal("unexpected credential request", err)
			}
			scope := req.Form.Get("scope")
			if scope != armOrigin+"/.default" && scope != synapseOAuthScope {
				t.Fatal("wrong OAuth audience", scope)
			}
			f.scopes[scope]++
			if f.override != nil {
				if res, ok := f.override(req); ok {
					return res, nil
				}
			}
			return jsonResponse(200, map[string]any{"access_token": scope, "token_type": "Bearer", "expires_in": 3600}, nil), nil
		}
		if req.URL.Host == "management.azure.com" {
			if req.Header.Get("Authorization") != "Bearer "+armOrigin+"/.default" {
				t.Fatal("Synapse token reached ARM")
			}
			if f.override != nil {
				if res, ok := f.override(req); ok {
					return res, nil
				}
			}
			if path == "/subscriptions/"+testSubscription {
				return jsonResponse(200, map[string]any{"subscriptionId": testSubscription, "tenantId": testTenant, "state": "Enabled"}, nil), nil
			}
			if req.URL.Query().Get("api-version") != synapseVersion {
				t.Fatal("changed ARM version")
			}
			if path == "/subscriptions/"+testSubscription+"/providers/microsoft.synapse/workspaces" {
				return jsonResponse(200, map[string]any{"value": []any{f.workspace}}, nil), nil
			}
			if path == strings.ToLower(text(f.workspace["id"])) {
				return jsonResponse(200, f.workspace, nil), nil
			}
			if path == strings.ToLower(text(f.pool["id"])) {
				return jsonResponse(200, f.pool, nil), nil
			}
		}
		if req.URL.Host == "first.dev.azuresynapse.net" {
			if req.Header.Get("Authorization") != "Bearer "+synapseOAuthScope || req.Method != "GET" && req.Method != "DELETE" {
				t.Fatal("wrong token or method reached Synapse")
			}
			f.dataCalls++
			if f.override != nil {
				if res, ok := f.override(req); ok {
					return res, nil
				}
			}
			if f.data != nil {
				return f.data(req), nil
			}
			return jsonResponse(200, map[string]any{"from": 0, "total": 0, "sessions": []any{}}, http.Header{"X-Ms-Request-Id": {"synapse-data-request"}}), nil
		}
		t.Fatal("request escaped authorized endpoints", req.Method, req.URL)
		return nil, nil
	})
	return f
}
func synapseReadInvocation(name string) contracts.Invocation {
	params := map[string]any{"endpoint": "https://first.dev.azuresynapse.net"}
	if strings.HasPrefix(name, "SparkBatch_") || strings.HasPrefix(name, "SparkSession_") {
		params["sparkPoolName"] = "pool"
		params["detailed"] = true
		if name == "SparkBatch_GetSparkBatchJob" {
			params["batchId"] = 0
		}
		if name == "SparkSession_GetSparkSession" {
			params["sessionId"] = 0
		}
	} else if name == "Notebook_GetNotebook" {
		params["notebookName"] = "item"
	} else if name == "SparkJobDefinition_GetSparkJobDefinition" {
		params["sparkJobDefinitionName"] = "item"
	} else if name == "Pipeline_GetPipeline" {
		params["pipelineName"] = "item"
	}
	return contracts.Invocation{ConnectionID: "connection", Operation: synapseDataOperationPrefix + name, Parameters: params, IdempotencyKey: "synapse-read"}
}
func (f *synapseTransportFixture) item(name string) map[string]any {
	if strings.HasPrefix(name, "SparkBatch_") || strings.HasPrefix(name, "SparkSession_") {
		kind := "SparkBatch"
		if strings.HasPrefix(name, "SparkSession_") {
			kind = "SparkSession"
		}
		return map[string]any{"id": 0, "state": "running", "workspaceName": "first", "sparkPoolName": "pool", "jobType": kind, "log": []any{"data-secret-canary"}, "tags": map[string]any{"innocent": "data-secret-canary"}, "livyInfo": map[string]any{"currentState": "running", "jobCreationRequest": map[string]any{"args": []any{"data-secret-canary"}}}, "futurePrivate": "data-secret-canary"}
	}
	collection := "notebooks"
	if strings.HasPrefix(name, "SparkJobDefinition_") {
		collection = "sparkjobdefinitions"
	}
	if strings.HasPrefix(name, "Pipeline_") {
		collection = "pipelines"
	}
	return map[string]any{"id": text(f.workspace["id"]) + "/" + collection + "/item", "name": "item", "type": synapseType + "/" + collection, "etag": "etag", "properties": map[string]any{"bigDataPool": map[string]any{"referenceName": "pool", "type": "BigDataPoolReference"}, "cells": []any{map[string]any{"source": []any{"data-secret-canary"}}}, "jobProperties": map[string]any{"args": []any{"data-secret-canary"}}, "futurePrivate": "data-secret-canary"}}
}
func (f *synapseTransportFixture) body(name string) map[string]any {
	item := f.item(name)
	if name == "SparkBatch_GetSparkBatchJobs" || name == "SparkSession_GetSparkSessions" {
		return map[string]any{"from": 0, "total": 1, "sessions": []any{item}}
	}
	if strings.HasSuffix(name, "ByWorkspace") {
		return map[string]any{"value": []any{item}}
	}
	return item
}

var synapseDataReads = []string{"SparkBatch_GetSparkBatchJobs", "SparkBatch_GetSparkBatchJob", "SparkSession_GetSparkSessions", "SparkSession_GetSparkSession", "Notebook_GetNotebooksByWorkspace", "Notebook_GetNotebook", "SparkJobDefinition_GetSparkJobDefinitionsByWorkspace", "SparkJobDefinition_GetSparkJobDefinition", "Pipeline_GetPipeline", "Pipeline_GetPipelinesByWorkspace"}

func TestSynapseDataReadAuthorizationAndPrivacy(t *testing.T) {
	for _, name := range synapseDataReads {
		t.Run(name, func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			body := f.body(name)
			f.data = func(req *http.Request) *http.Response {
				if req.Header.Get("X-Ms-Client-Request-Id") != azureRequestID("synapse-read") {
					t.Fatal("missing request correlation")
				}
				if strings.HasPrefix(name, "Spark") && !strings.HasPrefix(name, "SparkJobDefinition_") {
					if req.URL.Query().Has("api-version") || !strings.Contains(req.URL.Path, "/versions/2020-12-01/") {
						t.Fatal("wrong Livy version")
					}
				} else if req.URL.Query().Get("api-version") != synapseDataVersion {
					t.Fatal("wrong artifact version")
				}
				return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"synapse-data-request"}})
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
			for range 2 {
				result, err := f.runtime.Invoke(ctx, synapseReadInvocation(name))
				if err != nil || result.RequestID != "synapse-data-request" || len(result.Data) == 0 {
					t.Fatal(result, err)
				}
				b, _ := json.Marshal(result)
				if strings.Contains(string(b), "data-secret-canary") {
					t.Fatal("private code/config escaped invocation")
				}
			}
			b, _ := json.Marshal(logs)
			if len(logs) == 0 || strings.Contains(string(b), "data-secret-canary") {
				t.Fatal("private code/config escaped logs")
			}
			original, _ := json.Marshal(body)
			if !strings.Contains(string(original), "data-secret-canary") {
				t.Fatal("privacy filtering altered native state")
			}
			if f.scopes[armOrigin+"/.default"] != 1 || f.scopes[synapseOAuthScope] != 1 || f.dataCalls != 2 {
				t.Fatal("audience cache changed", f.scopes, f.dataCalls)
			}
			old := f.runtime.synapseClients["connection"]
			f.secret = "rotated-secret"
			if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err != nil {
				t.Fatal(err)
			}
			if f.runtime.synapseClients["connection"] == old || f.scopes[synapseOAuthScope] != 2 {
				t.Fatal("credential rotation retained Synapse token")
			}
		})
	}
}

func TestSynapseDataRejectsUnownedOrChangedParents(t *testing.T) {
	for name, setup := range map[string]func(*synapseTransportFixture){
		"missing index": func(f *synapseTransportFixture) {
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(strings.ToLower(q.URL.Path), "/microsoft.synapse/workspaces") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return nil, false
			}
		},
		"list forbidden": func(f *synapseTransportFixture) {
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(strings.ToLower(q.URL.Path), "/microsoft.synapse/workspaces") {
					return jsonResponse(403, nil, nil), true
				}
				return nil, false
			}
		},
		"foreign subscription": func(f *synapseTransportFixture) {
			f.workspace["id"] = strings.Replace(text(f.workspace["id"]), testSubscription, testTenant, 1)
		},
		"foreign endpoint": func(f *synapseTransportFixture) {
			object(f.workspace["properties"])["connectivityEndpoints"] = map[string]any{"dev": "https://foreign.dev.azuresynapse.net"}
		},
		"endpoint path": func(f *synapseTransportFixture) {
			object(f.workspace["properties"])["connectivityEndpoints"] = map[string]any{"dev": "https://first.dev.azuresynapse.net/"}
		},
		"missing endpoint": func(f *synapseTransportFixture) { delete(object(f.workspace["properties"]), "connectivityEndpoints") },
		"pool absent": func(f *synapseTransportFixture) {
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
					return jsonResponse(404, nil, nil), true
				}
				return nil, false
			}
		},
		"workspace absent": func(f *synapseTransportFixture) {
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, text(f.workspace["id"])) {
					return jsonResponse(404, nil, nil), true
				}
				return nil, false
			}
		},
		"pool drift": func(f *synapseTransportFixture) {
			f.data = func(*http.Request) *http.Response {
				object(f.pool["properties"])["nodeSize"] = "changed"
				return jsonResponse(200, map[string]any{"from": 0, "total": 0, "sessions": []any{}}, nil)
			}
		},
		"workspace drift": func(f *synapseTransportFixture) {
			f.data = func(*http.Request) *http.Response {
				object(f.workspace["properties"])["workspaceUID"] = "changed"
				return jsonResponse(200, map[string]any{"from": 0, "total": 0, "sessions": []any{}}, nil)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			setup(f)
			if result, err := f.runtime.Invoke(t.Context(), synapseReadInvocation("SparkBatch_GetSparkBatchJobs")); err == nil || len(result.Data) != 0 {
				t.Fatal("unowned/drifting parent accepted", result, err)
			}
			if !strings.Contains(name, "drift") && (f.dataCalls != 0 || f.scopes[synapseOAuthScope] != 0) {
				t.Fatal("unowned endpoint acquired or received a token")
			}
		})
	}
}

func TestSynapseDataRejectsIncompleteHTTPResponses(t *testing.T) {
	for _, name := range synapseDataReads {
		t.Run(name, func(t *testing.T) {
			for _, status := range []int{202, 204, 206, 304, 403, 404, 429, 500} {
				t.Run(http.StatusText(status), func(t *testing.T) {
					f := newSynapseTransportFixture(t)
					f.data = func(*http.Request) *http.Response {
						return jsonResponse(status, f.body(name), http.Header{"Retry-After": {"3"}})
					}
					if result, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil || len(result.Data) != 0 {
						t.Fatal("incomplete HTTP result accepted", status, result, err)
					}
				})
			}
			for _, header := range []string{"Location", "Azure-AsyncOperation", "Operation-Location"} {
				t.Run(header, func(t *testing.T) {
					f := newSynapseTransportFixture(t)
					f.data = func(*http.Request) *http.Response {
						headers := http.Header{}
						headers.Set(header, "https://foreign.invalid/poll")
						return jsonResponse(200, f.body(name), headers)
					}
					if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
						t.Fatal("unsolicited polling header accepted")
					}
				})
			}
		})
	}
}

func TestSynapseSparkReadIdentityAndPaging(t *testing.T) {
	for _, name := range synapseDataReads[:4] {
		t.Run(name, func(t *testing.T) {
			for fault, mutate := range map[string]func(map[string]any){
				"id absent": func(v map[string]any) { delete(v, "id") }, "id fractional": func(v map[string]any) { v["id"] = 0.5 }, "id negative": func(v map[string]any) { v["id"] = -1 },
				"workspace": func(v map[string]any) { v["workspaceName"] = "foreign" }, "pool": func(v map[string]any) { v["sparkPoolName"] = "foreign" }, "job type": func(v map[string]any) { v["jobType"] = "Other" },
				"missing state": func(v map[string]any) { delete(v, "state") }, "missing detailed identity": func(v map[string]any) { delete(v, "workspaceName") }, "malformed scheduler": func(v map[string]any) { v["schedulerInfo"] = []any{} },
			} {
				t.Run(fault, func(t *testing.T) {
					f := newSynapseTransportFixture(t)
					body := f.body(name)
					raw := body
					if rows, ok := body["sessions"].([]any); ok {
						raw = object(rows[0])
					}
					mutate(raw)
					f.data = func(*http.Request) *http.Response { return jsonResponse(200, body, nil) }
					if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
						t.Fatal("invalid Spark observation accepted")
					}
				})
			}
		})
	}
	for _, name := range []string{"SparkBatch_GetSparkBatchJobs", "SparkSession_GetSparkSessions"} {
		for fault, change := range map[string]func(map[string]any){
			"wrong offset": func(b map[string]any) { b["from"] = 1 }, "negative total": func(b map[string]any) { b["total"] = -1 }, "fractional total": func(b map[string]any) { b["total"] = 1.5 },
			"missing rows": func(b map[string]any) { delete(b, "sessions") }, "wrong rows": func(b map[string]any) { b["sessions"] = map[string]any{} }, "truncated rows": func(b map[string]any) { b["sessions"] = []any{} },
			"overreported rows": func(b map[string]any) { b["total"] = 0 }, "duplicate": func(b map[string]any) {
				b["total"] = 2
				b["sessions"] = append(b["sessions"].([]any), b["sessions"].([]any)[0])
			},
		} {
			t.Run(name+"/"+fault, func(t *testing.T) {
				f := newSynapseTransportFixture(t)
				body := f.body(name)
				change(body)
				f.data = func(*http.Request) *http.Response { return jsonResponse(200, body, nil) }
				if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
					t.Fatal("incomplete Spark page accepted")
				}
			})
		}
		t.Run(name+"/offset continuation", func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			inv := synapseReadInvocation(name)
			inv.Parameters["size"] = 1
			f.data = func(q *http.Request) *http.Response {
				body := f.body(name)
				body["total"] = 2
				if q.URL.Query().Get("from") == "1" {
					body["from"] = 1
					object(body["sessions"].([]any)[0])["id"] = 1
				}
				return jsonResponse(200, body, nil)
			}
			first, err := f.runtime.Invoke(t.Context(), inv)
			if err != nil || first.NextToken != "1" {
				t.Fatal("lost native offset continuation", first, err)
			}
			inv.Parameters["from"] = 1
			second, err := f.runtime.Invoke(t.Context(), inv)
			if err != nil || second.NextToken != "" {
				t.Fatal("last native page did not terminate", second, err)
			}
		})
	}
}

func TestSynapseArtifactResponseBoundaries(t *testing.T) {
	for _, name := range synapseDataReads[4:] {
		t.Run(name, func(t *testing.T) {
			for fault, change := range map[string]func(map[string]any){
				"foreign workspace": func(b map[string]any) {
					b["id"] = strings.Replace(text(b["id"]), "/workspaces/first/", "/workspaces/foreign/", 1)
				},
				"wrong type": func(b map[string]any) { b["type"] = "Microsoft.Other/resources" }, "empty name": func(b map[string]any) { b["name"] = "" },
				"missing properties": func(b map[string]any) { delete(b, "properties") }, "malformed properties": func(b map[string]any) { b["properties"] = []any{} }, "malformed etag": func(b map[string]any) { b["etag"] = 42 },
			} {
				t.Run(fault, func(t *testing.T) {
					f := newSynapseTransportFixture(t)
					body := f.body(name)
					raw := body
					if rows, ok := body["value"].([]any); ok {
						raw = object(rows[0])
					}
					change(raw)
					f.data = func(*http.Request) *http.Response { return jsonResponse(200, body, nil) }
					if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
						t.Fatal("invalid artifact accepted")
					}
				})
			}
			if strings.HasSuffix(name, "ByWorkspace") {
				for _, next := range []any{true, "https://foreign.dev.azuresynapse.net/notebooks?api-version=2020-12-01", "https://first.dev.azuresynapse.net/other?api-version=2020-12-01", "https://first.dev.azuresynapse.net/notebooks?api-version=2020-12-01&api-version=2020-12-01", "https://first.dev.azuresynapse.net/notebooks?api-version=preview", "https://first.dev.azuresynapse.net/notebooks?api-version=2020-12-01#fragment"} {
					f := newSynapseTransportFixture(t)
					body := f.body(name)
					body["nextLink"] = next
					f.data = func(*http.Request) *http.Response { return jsonResponse(200, body, nil) }
					if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
						t.Fatal("unsafe continuation accepted", next)
					}
				}
			}
		})
	}
}

func TestSynapseDataRedirectAndCancelledContext(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{true: "oauth", false: "data"}[oauth], func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			f.override = func(q *http.Request) (*http.Response, bool) {
				if !oauth && q.URL.Host == "first.dev.azuresynapse.net" || oauth && q.URL.Host == "login.microsoftonline.com" && q.Form.Get("scope") == synapseOAuthScope {
					return jsonResponse(302, nil, http.Header{"Location": {"https://foreign.invalid/token"}}), true
				}
				return nil, false
			}
			if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation("SparkBatch_GetSparkBatchJobs")); err == nil {
				t.Fatal("redirect accepted")
			}
		})
	}
	f := newSynapseTransportFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.runtime.Invoke(ctx, synapseReadInvocation("SparkBatch_GetSparkBatchJobs")); err == nil || f.dataCalls != 0 || len(f.scopes) != 0 {
		t.Fatal("cancelled request obtained credentials or queried data")
	}
}

func TestSynapseWorkspaceAuthorizationPagination(t *testing.T) {
	for _, fault := range []string{"cycle", "duplicate", "foreign continuation", "late forbidden", "changed detail", "valid second page"} {
		t.Run(fault, func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			collection := armOrigin + "/subscriptions/" + testSubscription + "/providers/Microsoft.Synapse/workspaces?api-version=" + synapseVersion
			f.override = func(q *http.Request) (*http.Response, bool) {
				if fault == "changed detail" && strings.EqualFold(q.URL.Path, text(f.workspace["id"])) {
					v := batchClone(f.workspace)
					object(v["properties"])["workspaceUID"] = "different"
					return jsonResponse(200, v, nil), true
				}
				if !strings.HasSuffix(strings.ToLower(q.URL.Path), "/microsoft.synapse/workspaces") {
					return nil, false
				}
				if q.URL.Query().Get("$skiptoken") == "next" {
					if fault == "late forbidden" {
						return jsonResponse(403, nil, nil), true
					}
					return jsonResponse(200, map[string]any{"value": []any{f.workspace}}, nil), true
				}
				body := map[string]any{"value": []any{f.workspace}, "nextLink": collection + "&%24skiptoken=next"}
				if fault == "cycle" {
					body["nextLink"] = collection
				}
				if fault == "foreign continuation" {
					body["nextLink"] = "https://foreign.invalid/"
				}
				if fault == "valid second page" {
					body["value"] = []any{}
				}
				if fault == "changed detail" {
					delete(body, "nextLink")
				}
				return jsonResponse(200, body, nil), true
			}
			result, err := f.runtime.Invoke(t.Context(), synapseReadInvocation("SparkBatch_GetSparkBatchJobs"))
			if fault == "valid second page" {
				if err != nil || f.dataCalls != 1 {
					t.Fatal("second-page workspace was not resolved", result, err)
				}
			} else if err == nil || f.dataCalls != 0 || f.scopes[synapseOAuthScope] != 0 {
				t.Fatal("incomplete ownership index authorized data", result, err, f.scopes)
			}
		})
	}
}

func TestSynapseArtifactContinuationAndSparkIdentityMismatch(t *testing.T) {
	for _, name := range []string{"Notebook_GetNotebooksByWorkspace", "SparkJobDefinition_GetSparkJobDefinitionsByWorkspace"} {
		f := newSynapseTransportFixture(t)
		path := "/notebooks"
		if strings.HasPrefix(name, "SparkJobDefinition_") {
			path = "/sparkJobDefinitions"
		}
		next := "https://first.dev.azuresynapse.net" + path + "?api-version=2020-12-01&continuationToken=opaque"
		f.data = func(*http.Request) *http.Response {
			body := f.body(name)
			body["nextLink"] = next
			return jsonResponse(200, body, nil)
		}
		result, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name))
		if err != nil || result.NextToken != next {
			t.Fatal("native artifact continuation lost", result, err)
		}
	}
	for _, name := range []string{"SparkBatch_GetSparkBatchJob", "SparkSession_GetSparkSession"} {
		f := newSynapseTransportFixture(t)
		f.data = func(*http.Request) *http.Response {
			body := f.body(name)
			body["id"] = 1
			return jsonResponse(200, body, nil)
		}
		if _, err := f.runtime.Invoke(t.Context(), synapseReadInvocation(name)); err == nil {
			t.Fatal("different Spark ID accepted")
		}
	}
}
