package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func artifactOperationFixture(t *testing.T) (*synapseTransportFixture, *synapseDataClient, synapseWorkspace, synapseDataDefinition, string) {
	t.Helper()
	f := newSynapseTransportFixture(t)
	c, err := f.runtime.synapseClient(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.arm.synapseWorkspaceRead(t.Context(), text(f.workspace["id"]))
	if err != nil {
		t.Fatal(err)
	}
	return f, c, w, synapseDataKind(synapseNotebookType), w.id + "/notebooks/item"
}

func artifactDeleteResponse(w synapseWorkspace, id string) response {
	h := http.Header{}
	h.Set("Location", w.endpoint+"/notebookOperationResults/operation?api-version="+synapseDataVersion)
	return response{status: 202, header: h, data: map[string]any{"id": id, "type": "Notebook", "name": "item", "state": "Deleting", "recordId": 0, "operationId": "operation"}}
}

func TestSynapseArtifactNativeRecordedDeletion(t *testing.T) {
	payload, err := os.ReadFile("fixtures/synapse/data-plane/cli-artifact-deletion-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "d821c44e9a211ab3e76f388df0f30a9e68768f73e0964b946db772ec63eaefcd" {
		t.Fatal("recording changed", err)
	}
	var sources []struct {
		File string `json:"file"`
		SHA  string `json:"source_sha256"`
		URI  string `json:"source_uri"`
		Rows []struct {
			Index             int `json:"interaction_index"`
			Method, URI, Body string
			Status            int
			Headers           map[string][]string
		} `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 2 {
		t.Fatal("invalid recordings")
	}
	for _, source := range sources {
		t.Run(source.File, func(t *testing.T) {
			expected := map[string]string{"test_notebook.yaml": "c712c41bae3470b6e5dfc5cfd9cc16b57ffab90bdb19337391ceda3fcfae35bd", "test_spark_job_definition.yaml": "033079355277b797e330400f307080b8a0664cba0f2d38dfe011038cc082650f"}[source.File]
			if source.SHA != expected || !strings.Contains(source.URI, "/c683a64f397974bae397d77a204e2ae86a908fa0/") || len(source.Rows) != 4 {
				t.Fatal("invalid source evidence")
			}
			var native map[string]any
			if json.Unmarshal([]byte(source.Rows[0].Body), &native) != nil {
				t.Fatal("invalid delete body")
			}
			id := strings.ToLower(text(native["id"]))
			workspaceID := strings.Join(strings.Split(id, "/")[:9], "/")
			u, _ := url.Parse(source.Rows[0].URI)
			w := synapseWorkspace{id: workspaceID, endpoint: u.Scheme + "://" + u.Host}
			c := &synapseDataClient{arm: &client{subscription: strings.Split(id, "/")[2]}}
			d := synapseDataKind(synapseNotebookType)
			if source.File == "test_spark_job_definition.yaml" {
				d = synapseDataKind(synapseJobDefinitionType)
			}
			res := response{status: source.Rows[0].Status, data: native, header: http.Header{}}
			for k, v := range source.Rows[0].Headers {
				for _, s := range v {
					res.header.Add(k, s)
				}
			}
			receipt, err := c.artifactDeleteReceipt(w, d, id, res)
			if err != nil || receipt["complete"] == true {
				t.Fatal(receipt, err)
			}
			for i := 1; i < 3; i++ {
				row := source.Rows[i]
				body := map[string]any{}
				if row.Body != "" && json.Unmarshal([]byte(row.Body), &body) != nil {
					t.Fatal("invalid poll body")
				}
				_, operation, err := synapseArtifactPollURL(w, row.URI)
				if err != nil {
					t.Fatal(err)
				}
				done, err := synapseArtifactPollResponse(operation, response{status: row.Status, data: body})
				if err != nil || done != (i == 2) {
					t.Fatal(done, err)
				}
			}
			if source.Rows[0].Method != "DELETE" || source.Rows[3].Method != "GET" || source.Rows[3].Status != 404 || source.Rows[3].URI != source.Rows[0].URI {
				t.Fatal("missing independent own readback")
			}
		})
	}
}

func TestSynapseArtifactPollingRestoresReceipt(t *testing.T) {
	f, c, w, d, id := artifactOperationFixture(t)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	receipt, err := c.artifactDeleteReceipt(w, d, id, artifactDeleteResponse(w, id))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.data = func(q *http.Request) *http.Response {
		calls++
		if q.URL.Path != "/notebookOperationResults/operation" || q.Method != "GET" {
			t.Fatal(q.Method, q.URL)
		}
		if calls == 1 {
			h := http.Header{}
			h.Set("Location", q.URL.String())
			h.Set("Retry-After", "3")
			return jsonResponse(202, map[string]any{"status": "InProgress", "futurePrivate": "artifact-private-canary"}, h)
		}
		res := jsonResponse(201, nil, nil)
		res.Body = io.NopCloser(strings.NewReader(""))
		return res
	}
	for i := 0; i < 2; i++ {
		wire, _ := json.Marshal(receipt)
		if json.Unmarshal(wire, &receipt) != nil {
			t.Fatal("receipt JSON")
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		current, err := fresh.synapseClient(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		out, err := current.pollArtifact(ctx, w, d, id, receipt)
		if err != nil || out.Done != (i == 1) {
			t.Fatal(out, err)
		}
		if i == 0 && out.RetryAfter.Seconds() != 3 {
			t.Fatal("retry lost")
		}
		receipt = out.Data
	}
	out, err := c.pollArtifact(t.Context(), w, d, id, receipt)
	if err != nil || !out.Done || calls != 2 {
		t.Fatal("completed receipt repolled", out, err, calls)
	}
	receipt["result_url"] = w.endpoint + "/notebookOperationResults/other?api-version=" + synapseDataVersion
	if _, err := c.pollArtifact(t.Context(), w, d, id, receipt); err == nil || calls != 2 {
		t.Fatal("tampered receipt used", err)
	}
	wire, _ := json.Marshal([]any{receipt, logs})
	if len(logs) == 0 || strings.Contains(string(wire), "artifact-private-canary") {
		t.Fatal("private operation data escaped")
	}
}

func TestSynapseArtifactPollingFaults(t *testing.T) {
	for _, fault := range []string{"forbidden", "missing callback", "different location", "failed", "cancelled", "unknown", "null", "array", "resource body", "error envelope"} {
		t.Run(fault, func(t *testing.T) {
			f, c, w, d, id := artifactOperationFixture(t)
			receipt, err := c.artifactDeleteReceipt(w, d, id, artifactDeleteResponse(w, id))
			if err != nil {
				t.Fatal(err)
			}
			f.data = func(q *http.Request) *http.Response {
				switch fault {
				case "forbidden":
					return jsonResponse(403, nil, nil)
				case "missing callback":
					return jsonResponse(404, nil, nil)
				case "different location":
					h := http.Header{}
					h.Set("Location", w.endpoint+"/notebookOperationResults/other?api-version="+synapseDataVersion)
					return jsonResponse(202, map[string]any{"status": "InProgress"}, h)
				case "failed":
					return jsonResponse(200, map[string]any{"status": "Failed"}, nil)
				case "cancelled":
					return jsonResponse(200, map[string]any{"status": "Canceled"}, nil)
				case "unknown":
					return jsonResponse(200, map[string]any{"status": "Future"}, nil)
				case "null":
					return jsonResponse(201, nil, nil)
				case "array":
					res := jsonResponse(201, nil, nil)
					res.Body = io.NopCloser(strings.NewReader("[]"))
					return res
				case "resource body":
					return jsonResponse(201, f.item("Notebook_GetNotebook"), nil)
				default:
					return jsonResponse(200, map[string]any{"status": "Succeeded", "code": "Failed"}, nil)
				}
			}
			if out, err := c.pollArtifact(t.Context(), w, d, id, receipt); err == nil || isNotFound(err) || out.Done {
				t.Fatal("invalid callback became completion/absence", out, err)
			}
		})
	}
}

func TestSynapseArtifactStatusThenResult(t *testing.T) {
	f, c, w, d, id := artifactOperationFixture(t)
	res := artifactDeleteResponse(w, id)
	res.header.Set("Azure-AsyncOperation", w.endpoint+"/operationStatuses/operation?api-version="+synapseDataVersion)
	receipt, err := c.artifactDeleteReceipt(w, d, id, res)
	if err != nil {
		t.Fatal(err)
	}
	stage := 0
	f.data = func(q *http.Request) *http.Response {
		expected := "/operationStatuses/operation"
		if stage >= 2 {
			expected = "/notebookOperationResults/operation"
		}
		if q.URL.Path != expected {
			t.Fatal("poll phase changed", stage, q.URL)
		}
		if stage < 2 {
			status := "InProgress"
			if stage == 1 {
				status = "Succeeded"
			}
			return jsonResponse(200, map[string]any{"status": status}, nil)
		}
		status := 202
		if stage == 3 {
			status = 200
		}
		res := jsonResponse(status, nil, nil)
		res.Body = io.NopCloser(strings.NewReader(""))
		return res
	}
	for ; stage < 4; stage++ {
		wire, _ := json.Marshal(receipt)
		if json.Unmarshal(wire, &receipt) != nil {
			t.Fatal("receipt restoration")
		}
		out, err := c.pollArtifact(t.Context(), w, d, id, receipt)
		if err != nil || out.Done != (stage == 3) {
			t.Fatal(stage, out, err)
		}
		if stage == 1 && out.Data["status_done"] != true {
			t.Fatal("status completion not checkpointed")
		}
		receipt = out.Data
	}
	c.arm.fingerprint = sha256.Sum256([]byte("changed credential"))
	if _, err := c.pollArtifact(t.Context(), w, d, id, receipt); err == nil {
		t.Fatal("receipt crossed credential incarnation")
	}
}

func TestSynapseArtifactReceiptFaults(t *testing.T) {
	for _, fault := range []string{"foreign host", "foreign collection", "different operation", "wrong id", "wrong type", "wrong state", "missing location", "duplicate location", "partial", "unexpected body", "error"} {
		t.Run(fault, func(t *testing.T) {
			_, c, w, d, id := artifactOperationFixture(t)
			res := artifactDeleteResponse(w, id)
			switch fault {
			case "foreign host":
				res.header.Set("Location", "https://other.dev.azuresynapse.net/notebookOperationResults/operation?api-version="+synapseDataVersion)
			case "foreign collection":
				res.header.Set("Location", w.endpoint+"/operationResults/operation?api-version="+synapseDataVersion)
			case "different operation":
				res.header.Set("Azure-AsyncOperation", w.endpoint+"/operationStatuses/other?api-version="+synapseDataVersion)
			case "wrong id":
				res.data["id"] = w.id + "/notebooks/other"
			case "wrong type":
				res.data["type"] = "Pipeline"
			case "wrong state":
				res.data["state"] = "Creating"
			case "missing location":
				res.header.Del("Location")
			case "duplicate location":
				res.header.Add("Location", res.header.Get("Location"))
			case "partial":
				res.status = 206
			case "unexpected body":
				res.status = 204
			case "error":
				res.data["code"] = "Failed"
			}
			if _, err := c.artifactDeleteReceipt(w, d, id, res); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}

func TestSynapseArtifactOperationNativeWire(t *testing.T) {
	for _, op := range []string{"NotebookOperationResult_Get", "OperationResult_Get", "OperationStatus_Get"} {
		for _, status := range []int{200, 201, 202, 204, 403, 404, 206} {
			t.Run(fmt.Sprintf("%s/%d", op, status), func(t *testing.T) {
				f := newSynapseTransportFixture(t)
				f.data = func(q *http.Request) *http.Response {
					if q.Header.Get("x-ms-client-request-id") != azureRequestID("artifact-read") {
						t.Fatal("missing request correlation")
					}
					res := jsonResponse(status, nil, nil)
					res.Body = io.NopCloser(strings.NewReader(""))
					return res
				}
				inv := contracts.Invocation{ConnectionID: "connection", Operation: synapseDataOperationPrefix + op, IdempotencyKey: "artifact-read", Parameters: map[string]any{"endpoint": "https://first.dev.azuresynapse.net", "operationId": "operation"}}
				out, err := f.runtime.Invoke(t.Context(), inv)
				if status == 403 || status == 404 || status == 206 {
					if err == nil || isNotFound(err) {
						t.Fatal("operation error became absence", out, err)
					}
					return
				}
				if err != nil || out.Data["operation_done"] != (status != 202) || out.Data["exists"] != nil {
					t.Fatal(out, err)
				}
				if f.scopes[synapseOAuthScope] != 1 || f.scopes[armOrigin+"/.default"] != 1 {
					t.Fatal("OAuth audience changed", f.scopes)
				}
			})
		}
	}
}
