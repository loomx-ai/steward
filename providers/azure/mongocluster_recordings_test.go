package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func mongoClusterRecordings(t *testing.T, file string) []redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/mongocluster/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 10 {
		t.Fatal("invalid DocumentDB recordings", err)
	}
	count := 0
	var result []redisRecordedResponse
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/0349eb646d3225db5fd677114e200efdfd11e3f8/") {
			t.Fatal("unpinned DocumentDB recording")
		}
		count += len(source.Records)
		if source.File == file {
			result = source.Records
		}
	}
	if count != 483 || len(result) == 0 {
		t.Fatal("missing native DocumentDB responses", count)
	}
	return result
}

func TestMongoClusterRecordedNativeDeletesAndResumedReadback(t *testing.T) {
	for _, test := range []struct {
		file         string
		read, delete int
	}{
		{"test_documentdb_mongocluster_cmk.yaml", 31, 32},
		{"test_documentdb_mongocluster_crud.yaml", 36, 44},
		{"test_documentdb_mongocluster_firewall.yaml", 29, 31},
		{"test_documentdb_mongocluster_firewall.yaml", 20, 35},
		{"test_documentdb_mongocluster_identity.yaml", 35, 36},
		{"test_documentdb_mongocluster_properties.yaml", 57, 61},
		{"test_documentdb_mongocluster_replica.yaml", 49, 51},
		{"test_documentdb_mongocluster_replica.yaml", 21, 58},
		{"test_documentdb_mongocluster_replica_promote.yaml", 21, 71},
		{"test_documentdb_mongocluster_replica_promote.yaml", 70, 78},
		{"test_documentdb_mongocluster_restore.yaml", 77, 78},
		{"test_documentdb_mongocluster_restore.yaml", 55, 84},
		{"test_documentdb_mongocluster_user.yaml", 27, 29},
		{"test_documentdb_mongocluster_user.yaml", 21, 34},
	} {
		t.Run(test.file+"/"+strconv.Itoa(test.delete), func(t *testing.T) {
			rows := mongoClusterRecordings(t, test.file)
			byIndex := map[int]redisRecordedResponse{}
			s := newDNSScenario()
			for _, row := range rows {
				byIndex[row.Index] = row
				if row.Index > test.delete || row.Method != "GET" || row.Status != 200 {
					continue
				}
				if id := text(row.Body["id"]); id != "" {
					if _, kind, err := parseID(id); err == nil && isMongoClusterType(kind) {
						s.add(row.Body, mongoClusterVersion)
					}
				}
			}
			raw := byIndex[test.read].Body
			id, kind, err := parseID(text(raw["id"]))
			if err != nil || !isMongoClusterType(kind) {
				t.Fatal("recording lacks resource", err)
			}
			s.add(raw, mongoClusterVersion)
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			// The recorded CLI sequence is not an inventory/cleanup scan. Supporting
			// collections below are synthetic empty indexes after prerequisite removal.
			// The selected resource GET, DELETE and every LRO response stay unchanged.
			for resourceID, body := range s.records {
				_, resourceKind, _ := parseID(resourceID)
				if !strings.EqualFold(resourceKind, mongoClusterType) {
					continue
				}
				for _, child := range append(mongoClusterOwnedKinds(), "replicas") {
					path := resourceID + "/" + strings.ToLower(last(child))
					s.lists[path], s.version[path] = []any{}, mongoClusterVersion
				}
				if len(array(object(body["properties"])["privateEndpointConnections"])) != 0 {
					t.Fatal("native root has unmodeled private endpoints")
				}
			}
			deletion := byIndex[test.delete]
			headers := http.Header{}
			for key, values := range deletion.Headers {
				for _, value := range values {
					headers.Add(key, value)
				}
			}
			operation := headers.Get("Azure-AsyncOperation")
			var polls []redisRecordedResponse
			for _, row := range rows {
				if row.Index > test.delete && row.Method == "GET" && row.URI == operation {
					polls = append(polls, row)
				}
			}
			if len(polls) == 0 || text(polls[len(polls)-1].Body["status"]) != "Succeeded" {
				t.Fatal("missing native operation completion")
			}
			deleted, absent, pollIndex := false, false, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					expected, _ := url.Parse(deletion.URI)
					if !strings.EqualFold(req.URL.Path, expected.Path) || req.URL.RawQuery != expected.RawQuery {
						t.Fatal("wrong native DocumentDB DELETE", req.URL.Path)
					}
					deleted = true
					return jsonResponse(deletion.Status, deletion.Body, headers), true
				}
				if req.URL.String() == operation {
					row := polls[pollIndex]
					if pollIndex < len(polls)-1 {
						pollIndex++
					}
					return jsonResponse(row.Status, row.Body, nil), true
				}
				if strings.EqualFold(req.URL.Path, id) && absent {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				return nil, false
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, raw)
			if !strings.EqualFold(kind, mongoClusterType) {
				target.Location = text(s.records[redisParentID(id)]["location"])
			}
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			result, err := driver.Execute(ctx, request)
			if err != nil || !deleted {
				t.Fatal("native DocumentDB deletion", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			for range len(polls) + 1 {
				waited, err := driver.Wait(ctx, request, result)
				if err != nil || waited.Done {
					t.Fatal("LRO success replaced native absence check", waited, err)
				}
			}
			// The recordings stop at LRO completion. This final 404 is an explicit
			// synthetic readback and idempotent-resume test, not a native-recording claim.
			absent = true
			waited, err := driver.Wait(ctx, request, result)
			if err != nil || !waited.Done {
				t.Fatal("DocumentDB final absence", waited, err)
			}
			deleted = false
			if _, err := driver.Execute(ctx, request); err != nil || deleted {
				t.Fatal("repeated native deletion after absence", err)
			}
			if result.ProviderOperationID != operation {
				t.Fatal("native operation URI changed")
			}
			payload, _ = json.Marshal(logs)
			u, _ := url.Parse(operation)
			for _, key := range []string{"t", "c", "s", "h"} {
				if value := u.Query().Get(key); value != "" && bytes.Contains(payload, []byte(value)) {
					t.Fatal("signed polling material reached logs", key)
				}
			}
		})
	}
}
