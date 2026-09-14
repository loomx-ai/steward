package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseCancelFixture struct {
	*synapseTransportFixture
	raw, group    map[string]any
	deletes, gets int
	after         func()
	receipt       func() *http.Response
	readStatus    int
}

func newSynapseCancelFixture(t *testing.T, kind string) (*synapseCancelFixture, contracts.Invocation) {
	f := &synapseCancelFixture{synapseTransportFixture: newSynapseTransportFixture(t)}
	d := synapseDataKind(kind)
	f.raw = f.item(d.read)
	f.raw["schedulerInfo"] = map[string]any{"submittedAt": "2025-02-24T09:47:41.6861206+00:00", "currentState": "Scheduled"}
	f.raw["pluginInfo"] = map[string]any{"currentState": "Monitoring"}
	f.raw["result"] = "Uncertain"
	groupID := strings.Join(strings.Split(text(f.workspace["id"]), "/")[:5], "/")
	f.group = map[string]any{"id": groupID, "type": groupType, "name": last(groupID), "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host == "management.azure.com" {
			if strings.EqualFold(q.URL.Path, groupID) {
				return jsonResponse(200, f.group, nil), true
			}
			if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.authorization/locks") {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		return nil, false
	}
	f.data = func(q *http.Request) *http.Response {
		expected := "/livyApi/versions/" + synapseDataVersion + "/sparkPools/pool/" + d.collection + "/0"
		if q.URL.Path != expected {
			t.Fatal("unexpected cancellation target", q.URL)
		}
		if q.Method == "DELETE" {
			f.deletes++
			if q.URL.RawQuery != "" || q.ContentLength > 0 || q.Header.Get("x-ms-client-request-id") != azureRequestID("synapse-cancel") {
				t.Fatal("invalid native cancel wire", q.URL, q.Header)
			}
			if f.after != nil {
				f.after()
			}
			if f.receipt != nil {
				return f.receipt()
			}
			return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"cancel-request"}})
		}
		f.gets++
		if q.URL.Query().Get("detailed") != "true" {
			t.Fatal("cancel readback was not detailed")
		}
		if f.readStatus != 0 {
			return jsonResponse(f.readStatus, nil, nil)
		}
		return jsonResponse(200, f.raw, http.Header{"X-Ms-Request-Id": {"read-request"}})
	}
	op := "SparkBatch_CancelSparkBatchJob"
	if kind == synapseSessionType {
		op = "SparkSession_CancelSparkSession"
	}
	inv := contracts.Invocation{ConnectionID: "connection", Operation: synapseDataOperationPrefix + op, Parameters: map[string]any{"endpoint": "https://first.dev.azuresynapse.net", "sparkPoolName": "pool", d.parameter: 0}, IdempotencyKey: "synapse-cancel"}
	return f, inv
}
func endSynapseSpark(raw map[string]any) {
	raw["result"] = "Cancelled"
	raw["state"] = "not_started"
	object(raw["schedulerInfo"])["currentState"] = "Ended"
	object(raw["pluginInfo"])["currentState"] = "Ended"
}
func TestSynapseCancellationReadback(t *testing.T) {
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		for _, state := range []string{"pending", "quiesced", "already quiesced", "already absent", "absent after", "delete 404 live", "delete 404 absent", "no cancel request ID"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				f, inv := newSynapseCancelFixture(t, kind)
				switch state {
				case "quiesced":
					f.after = func() { endSynapseSpark(f.raw) }
				case "already quiesced":
					endSynapseSpark(f.raw)
				case "already absent":
					f.readStatus = 404
				case "absent after", "delete 404 absent":
					f.after = func() { f.readStatus = 404 }
				}
				if state == "no cancel request ID" {
					f.receipt = func() *http.Response { return jsonResponse(200, nil, nil) }
				}
				if strings.HasPrefix(state, "delete 404") {
					f.receipt = func() *http.Response { return jsonResponse(404, nil, nil) }
				}
				var logs []execution.JobLogEntry
				ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
				result, err := f.runtime.Invoke(ctx, inv)
				if err != nil {
					t.Fatal(err)
				}
				absent := state == "already absent" || state == "absent after" || state == "delete 404 absent"
				quiesced := state == "quiesced" || state == "already quiesced"
				accepted := !strings.HasPrefix(state, "already") && !strings.HasPrefix(state, "delete 404")
				if result.Data["exists"] == absent || result.Data["quiesced"] != quiesced || result.Data["accepted"] != accepted || result.OperationID != "" {
					t.Fatal(result)
				}
				expectedDeletes := 1
				if strings.HasPrefix(state, "already") {
					expectedDeletes = 0
				}
				if f.deletes != expectedDeletes {
					t.Fatal("wrong mutation count", f.deletes)
				}
				payload, _ := json.Marshal([]any{result, logs})
				if strings.Contains(string(payload), "data-secret-canary") {
					t.Fatal("private data escaped")
				}
				expectedRequestID := "cancel-request"
				if state == "no cancel request ID" {
					expectedRequestID = "read-request"
				}
				if accepted && result.RequestID != expectedRequestID {
					t.Fatal("cancel request ID lost", result)
				}
				if quiesced {
					again, err := f.runtime.Invoke(ctx, inv)
					if err != nil || again.Data["accepted"] != false || f.deletes != expectedDeletes {
						t.Fatal("terminal retry mutated", again, err)
					}
				}
			})
		}
	}
}
func TestSynapseCancellationRejectsUncertainReadback(t *testing.T) {
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		for _, fault := range []string{"202", "204", "body", "poll", "post 403", "post 500", "incarnation", "configuration", "workspace", "pool", "redirect"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f, inv := newSynapseCancelFixture(t, kind)
				switch fault {
				case "202":
					f.receipt = func() *http.Response { return jsonResponse(202, nil, nil) }
				case "204":
					f.receipt = func() *http.Response { return jsonResponse(204, nil, nil) }
				case "body":
					f.receipt = func() *http.Response {
						return jsonResponse(200, map[string]any{"error": map[string]any{"message": "data-secret-canary"}}, nil)
					}
				case "poll":
					f.receipt = func() *http.Response {
						h := http.Header{}
						h.Set("Azure-AsyncOperation", "https://foreign.invalid/op")
						return jsonResponse(200, nil, h)
					}
				case "post 403":
					f.after = func() { f.readStatus = 403 }
				case "post 500":
					f.after = func() { f.readStatus = 500 }
				case "incarnation":
					f.after = func() { object(f.raw["schedulerInfo"])["submittedAt"] = "2026-02-24T09:47:41Z" }
				case "configuration":
					f.after = func() { object(object(f.raw["livyInfo"])["jobCreationRequest"])["args"] = []any{"changed"} }
				case "workspace":
					f.after = func() { object(f.workspace["properties"])["workspaceUID"] = "replacement" }
				case "pool":
					f.after = func() { object(f.pool["properties"])["nodeCount"] = 99 }
				case "redirect":
					f.receipt = func() *http.Response {
						return jsonResponse(307, nil, http.Header{"Location": {"https://foreign.invalid/cancel"}})
					}
				}
				result, err := f.runtime.Invoke(t.Context(), inv)
				if err == nil || len(result.Data) != 0 || f.deletes != 1 {
					t.Fatal("uncertain completion", result, err, f.deletes)
				}
			})
		}
	}
}
func TestSynapseCancellationPreflight(t *testing.T) {
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		for _, fault := range []string{"job protected", "pool protected", "workspace protected", "group protected", "group managed", "missing incarnation", "bad incarnation", "own 403", "parent 404", "lock", "version", "detailed", "foreign endpoint", "cancelled context"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f, inv := newSynapseCancelFixture(t, kind)
				ctx := t.Context()
				switch fault {
				case "job protected":
					f.raw["tags"] = map[string]any{"steward:protected": "true"}
				case "pool protected":
					f.pool["tags"] = map[string]any{"steward:protected": "true"}
				case "workspace protected":
					f.workspace["tags"] = map[string]any{"steward:protected": "true"}
				case "group protected":
					f.group["tags"] = map[string]any{"steward:protected": "true"}
				case "group managed":
					f.group["managedBy"] = text(f.workspace["id"])
				case "missing incarnation":
					delete(object(f.raw["schedulerInfo"]), "submittedAt")
				case "bad incarnation":
					object(f.raw["schedulerInfo"])["submittedAt"] = "invalid"
				case "own 403":
					f.readStatus = 403
				case "parent 404":
					old := f.override
					f.override = func(q *http.Request) (*http.Response, bool) {
						if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
							return jsonResponse(404, nil, nil), true
						}
						return old(q)
					}
				case "lock":
					old := f.override
					f.override = func(q *http.Request) (*http.Response, bool) {
						if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.authorization/locks") {
							return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": text(f.pool["id"]) + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
						}
						return old(q)
					}
				case "version":
					inv.Parameters["livyApiVersion"] = "2019-11-01-preview"
				case "detailed":
					inv.Parameters["detailed"] = true
				case "foreign endpoint":
					inv.Parameters["endpoint"] = "https://elsewhere.dev.azuresynapse.net"
				case "cancelled context":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				if result, err := f.runtime.Invoke(ctx, inv); err == nil || len(result.Data) != 0 || f.deletes != 0 {
					t.Fatal("preflight allowed mutation", result, err, f.deletes)
				}
			})
		}
	}
}
func TestSynapseSparkQuiescence(t *testing.T) {
	for _, result := range []string{"", "Uncertain", "Cancelled", "Succeeded", "Failed", "cancelled", "Future"} {
		for _, scheduler := range []string{"", "Scheduled", "Ended", "Future"} {
			for _, plugin := range []string{"", "Cleanup", "Ended", "Future"} {
				raw := map[string]any{"state": "killed", "result": result, "schedulerInfo": map[string]any{"currentState": scheduler}, "pluginInfo": map[string]any{"currentState": plugin}}
				expected := (result == "Cancelled" || result == "Succeeded" || result == "Failed") && scheduler == "Ended" && plugin == "Ended"
				if synapseSparkQuiesced(raw) != expected {
					t.Fatal(raw)
				}
				raw["state"] = "future"
				if synapseSparkQuiesced(raw) {
					t.Fatal("unknown state became cleanup evidence")
				}
			}
		}
	}
}

func TestSynapseCancellationRechecksBeforeMutation(t *testing.T) {
	for _, kind := range []string{synapseBatchType, synapseSessionType} {
		for _, fault := range []string{"configuration", "incarnation", "workspace", "pool", "read forbidden", "read absent", "becomes terminal"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f, inv := newSynapseCancelFixture(t, kind)
				original := f.data
				f.data = func(q *http.Request) *http.Response {
					if q.Method == "GET" && f.gets == 1 {
						switch fault {
						case "configuration":
							f.raw["name"] = "changed"
						case "incarnation":
							object(f.raw["schedulerInfo"])["submittedAt"] = "2026-01-01T00:00:00Z"
						case "read forbidden":
							f.readStatus = 403
						case "read absent":
							f.readStatus = 404
						case "becomes terminal":
							endSynapseSpark(f.raw)
						}
					}
					res := original(q)
					if q.Method == "GET" && f.gets == 1 {
						if fault == "workspace" {
							object(f.workspace["properties"])["workspaceUID"] = "replacement"
						}
						if fault == "pool" {
							object(f.pool["properties"])["nodeCount"] = 77
						}
					}
					return res
				}
				result, err := f.runtime.Invoke(t.Context(), inv)
				if f.deletes != 0 {
					t.Fatal("drift allowed mutation")
				}
				if fault == "read absent" || fault == "becomes terminal" {
					if err != nil || result.Data["accepted"] != false {
						t.Fatal(result, err)
					}
				} else if err == nil {
					t.Fatal("drift accepted", result)
				}
			})
		}
	}
}
