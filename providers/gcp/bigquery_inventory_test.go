package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// List summaries deliberately omit GET-only properties. Parent and table pages
// have repeated short names, so detail must be bound to the full native identity.
func TestBigQueryInventoryReadsNativeDetailsAcrossPages(t *testing.T) {
	const prefix = "/bigquery/v2/projects/sample-project/datasets"
	reads := map[string]int{}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "bigquery.googleapis.com" || !strings.HasPrefix(req.URL.Path, prefix) {
			t.Fatalf("unexpected native request %s", req.URL)
		}
		respond := func(data any) (*http.Response, error) { return dataformResponse(req, 200, data), nil }
		parts := strings.Split(strings.TrimPrefix(req.URL.Path, prefix), "/")
		if req.URL.Path == prefix {
			if req.URL.Query().Get("all") != "true" {
				t.Fatal("hidden datasets omitted")
			}
			dataset, token := "warehouse", "second"
			if req.URL.Query().Get("pageToken") == "second" {
				dataset, token = "archive", ""
			}
			return respond(map[string]any{"datasets": []any{map[string]any{"datasetReference": map[string]any{"projectId": "sample-project", "datasetId": dataset}}}, "nextPageToken": token})
		}
		dataset := parts[1]
		if dataset != "warehouse" && dataset != "archive" {
			t.Fatalf("foreign dataset detail %s", req.URL)
		}
		if len(parts) == 2 {
			reads[req.URL.Path]++
			return respond(map[string]any{"datasetReference": map[string]any{"projectId": "123456", "datasetId": dataset}, "location": "US", "defaultTableExpirationMs": "86400000", "defaultEncryptionConfiguration": map[string]any{"kmsKeyName": "projects/sample-project/locations/us/keyRings/analytics/cryptoKeys/data"}})
		}
		if len(parts) == 3 && parts[2] == "tables" {
			table, token := "events", "next-table"
			if req.URL.Query().Get("pageToken") == "next-table" {
				table, token = "history", ""
			}
			return respond(map[string]any{"tables": []any{map[string]any{"tableReference": map[string]any{"projectId": "sample-project", "datasetId": dataset, "tableId": table}}}, "nextPageToken": token})
		}
		if len(parts) == 4 && parts[2] == "tables" && (parts[3] == "events" || parts[3] == "history") {
			reads[req.URL.Path]++
			return respond(map[string]any{"tableReference": map[string]any{"projectId": "123456", "datasetId": dataset, "tableId": parts[3]}, "location": "US", "numRows": "9007199254740993", "numBytes": "18014398509481986", "type": "MATERIALIZED_VIEW", "materializedView": map[string]any{"query": "SELECT 'PRIVATE_VIEW_SQL' AS marker", "enableRefresh": true}, "encryptionConfiguration": map[string]any{"kmsKeyName": "projects/sample-project/locations/us/keyRings/analytics/cryptoKeys/data"}})
		}
		t.Fatalf("unexpected collection %s", req.URL)
		return nil, nil
	})
	for _, shortKind := range []string{"Dataset", "Table"} {
		kind := "bigquery.googleapis.com/" + shortKind
		request := productRequest(r, kind, "global")
		items := []contracts.InventoryItem{}
		for page := 0; ; page++ {
			if page == 10 {
				t.Fatal("native pagination did not complete")
			}
			batch, err := r.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			items = append(items, batch.Items...)
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
		want := 2
		if shortKind == "Table" {
			want = 4
		}
		if len(items) != want {
			t.Fatal("missing native parent/table pages", kind, len(items))
		}
		seen := map[string]bool{}
		for _, item := range items {
			if seen[item.NativeID] {
				t.Fatal("same-named resources collided", item.NativeID)
			}
			seen[item.NativeID] = true
			value := asset.Asset{Identity: asset.Identity{NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized}
			query := `properties.name = "` + last(item.NativeID) + `"`
			if shortKind == "Dataset" {
				query += ` AND properties.defaultTableExpirationMs = "86400000"`
				if object(item.Normalized["defaultEncryptionConfiguration"])["kmsKeyName"] == nil {
					t.Fatal("dataset detail omitted", item)
				}
			} else {
				query += ` AND properties.numRows = "9007199254740993" AND properties.numBytes = "18014398509481986"`
				if object(item.Normalized["encryptionConfiguration"])["kmsKeyName"] == nil {
					t.Fatal("table detail omitted", item)
				}
			}
			assertGCPPropertyQuery(t, r, []asset.Asset{value}, kind, item.NativeID, query)
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "PRIVATE_VIEW_SQL") {
				t.Fatal("BigQuery detail leaked private view SQL")
			}
			path := "/bigquery/v2/" + strings.TrimPrefix(item.NativeID, "//bigquery.googleapis.com/")
			if reads[path] == 0 || (shortKind == "Table" && reads[path] != 1) {
				t.Fatal("missing or duplicated detail read", path, reads[path])
			}
			if object(object(item.Raw["resource"])["data"])["name"] != nil || item.Normalized["projectId"] != "sample-project" {
				t.Fatal("native identity/observation changed", item)
			}
		}
	}
}

func TestBigQueryDetailFailuresCannotCompleteInventory(t *testing.T) {
	for _, kind := range []string{"Dataset", "Table"} {
		for _, failure := range []string{"forbidden", "not found", "foreign project", "changed dataset", "changed table", "missing reference", "empty object"} {
			if kind == "Dataset" && failure == "changed table" {
				continue
			}
			t.Run(kind+"/"+failure, func(t *testing.T) {
				detailRead := false
				r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					const prefix = "/bigquery/v2/projects/sample-project/datasets"
					if req.Method != "GET" || req.URL.Host != "bigquery.googleapis.com" {
						t.Fatalf("unexpected native request %s", req.URL)
					}
					dataset := map[string]any{"datasetReference": map[string]any{"projectId": "sample-project", "datasetId": "warehouse"}}
					table := map[string]any{"tableReference": map[string]any{"projectId": "sample-project", "datasetId": "warehouse", "tableId": "events"}}
					if req.URL.Path == prefix {
						return dataformResponse(req, 200, map[string]any{"datasets": []any{dataset}}), nil
					}
					if kind == "Table" && req.URL.Path == prefix+"/warehouse" {
						return dataformResponse(req, 200, dataset), nil
					}
					if kind == "Table" && req.URL.Path == prefix+"/warehouse/tables" {
						return dataformResponse(req, 200, map[string]any{"tables": []any{table}}), nil
					}
					want := prefix + "/warehouse"
					data, reference := dataset, object(dataset["datasetReference"])
					if kind == "Table" {
						want += "/tables/events"
						data, reference = table, object(table["tableReference"])
					}
					if req.URL.Path != want {
						t.Fatalf("unreviewed detail read %s", req.URL)
					}
					detailRead = true
					switch failure {
					case "forbidden":
						return dataformResponse(req, 403, map[string]any{}), nil
					case "not found":
						return dataformResponse(req, 404, map[string]any{}), nil
					case "foreign project":
						reference["projectId"] = "foreign-project"
					case "changed dataset":
						reference["datasetId"] = "foreign-dataset"
					case "changed table":
						reference["tableId"] = "other-table"
					case "missing reference":
						delete(reference, strings.ToLower(kind[:1])+kind[1:]+"Id")
						data["name"] = "synthetic-fallback-must-not-pass"
					case "empty object":
						data = map[string]any{}
					}
					return dataformResponse(req, 200, data), nil
				})
				batch, err := r.List(t.Context(), productRequest(r, "bigquery.googleapis.com/"+kind, "global"))
				if !detailRead || err == nil || batch.Complete || len(batch.Items) != 0 {
					t.Fatal("failed/mismatched detail became authoritative inventory", detailRead, batch, err)
				}
			})
		}
	}
}

func TestBigQueryNativeViewRedactionPreservesPublicFields(t *testing.T) {
	for _, field := range []string{"view", "materializedView"} {
		data := map[string]any{"tableReference": map[string]any{"projectId": "sample-project", "datasetId": "warehouse", "tableId": "events"}, field: map[string]any{"query": "SELECT 'PRIVATE_SQL'", "enableRefresh": true}, "labels": map[string]any{"query": "analytics"}, "numRows": "9007199254740993"}
		cleaned := safePayload(data)
		encoded, _ := json.Marshal(cleaned)
		if strings.Contains(string(encoded), "PRIVATE_SQL") || object(cleaned[field])["query"] != "[REDACTED]" || cleaned["numRows"] != "9007199254740993" || object(cleaned["labels"])["query"] != "analytics" || object(data[field])["query"] != "SELECT 'PRIVATE_SQL'" {
			t.Fatal("view SQL escaped or native/public data changed", cleaned)
		}
	}
}
