package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSynapseRecordedArtifactReads(t *testing.T) {
	payload, err := os.ReadFile("fixtures/synapse/data-plane/cli-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "62128650c1d8719fbe5461ed73dd8c3677a1ad0ab4ae89f33fdadd8636fceb9b" {
		t.Fatal("recording extraction changed", err)
	}
	var sources []struct {
		File string                  `json:"file"`
		URI  string                  `json:"source_uri"`
		SHA  string                  `json:"source_sha256"`
		Rows []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 2 {
		t.Fatal("invalid recordings")
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	count, success, absent := 0, 0, 0
	for _, source := range sources {
		expected := map[string]string{"test_notebook.yaml": "c712c41bae3470b6e5dfc5cfd9cc16b57ffab90bdb19337391ceda3fcfae35bd", "test_spark_job_definition.yaml": "033079355277b797e330400f307080b8a0664cba0f2d38dfe011038cc082650f"}[source.File]
		if source.SHA != expected || !strings.HasSuffix(source.URI, "/c683a64f397974bae397d77a204e2ae86a908fa0/src/azure-cli/azure/cli/command_modules/synapse/tests/latest/recordings/"+source.File) {
			t.Fatal("unverified recording provenance")
		}
		var workspace synapseWorkspace
		for _, row := range source.Rows {
			t.Run(fmt.Sprintf("%s/%d", source.File, row.Index), func(t *testing.T) {
				u, err := url.Parse(row.URI)
				if err != nil || row.Method != "GET" || u.Query().Get("api-version") != synapseDataVersion {
					t.Fatal("recorded request changed")
				}
				parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
				operation, key := "Notebook_GetNotebooksByWorkspace", "notebookName"
				if parts[0] == "sparkJobDefinitions" {
					operation, key = "SparkJobDefinition_GetSparkJobDefinitionsByWorkspace", "sparkJobDefinitionName"
				}
				params := map[string]any{"endpoint": u.Scheme + "://" + u.Host}
				if len(parts) == 2 {
					params[key] = parts[1]
					if key == "notebookName" {
						operation = "Notebook_GetNotebook"
					} else {
						operation = "SparkJobDefinition_GetSparkJobDefinition"
					}
				}
				op, ok := metadata.catalog.Operation(synapseDataOperationPrefix + operation)
				if !ok {
					t.Fatal("missing native operation")
				}
				bound, err := bindAzureREST(op, params)
				if err != nil || bound.URL != row.URI {
					t.Fatal("recorded stable request does not bind", bound, err)
				}
				if workspace.id == "" {
					id := text(row.Body["id"])
					if values, ok := row.Body["value"].([]any); ok && len(values) > 0 {
						id = text(object(values[0])["id"])
					}
					segments := strings.Split(id, "/")
					if len(segments) != 11 {
						t.Fatal("missing recorded workspace")
					}
					workspace = synapseWorkspace{id: strings.Join(segments[:9], "/"), endpoint: u.Scheme + "://" + u.Host}
				}
				headers := http.Header{}
				for k, values := range row.Headers {
					for _, v := range values {
						headers.Add(k, v)
					}
				}
				res := response{data: row.Body, status: row.Status, header: headers}
				next, err := synapseDataResponse(workspace, op, params, res)
				if row.Status == 200 {
					if err != nil || next != "" {
						t.Fatal("recorded native artifact rejected", err)
					}
					success++
					foreign := workspace
					foreign.id += "other"
					if _, err := synapseDataResponse(foreign, op, params, res); err == nil {
						t.Fatal("recorded artifact accepted for another workspace")
					}
				} else if row.Status == 404 {
					if err == nil {
						t.Fatal("recorded absence treated as detail")
					}
					absent++
				} else {
					t.Fatal("unexpected selected status")
				}
				count++
			})
		}
	}
	if count != 7 || success != 5 || absent != 2 {
		t.Fatal("recording coverage incomplete", count, success, absent)
	}
}
