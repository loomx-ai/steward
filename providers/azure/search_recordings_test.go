package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func searchRecordings(t *testing.T, file string) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/search/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File       string                  `json:"file"`
		Source     string                  `json:"source_uri"`
		SHA        string                  `json:"source_sha256"`
		Recordings []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 3 {
		t.Fatal("invalid Search recordings", err)
	}
	count := 0
	result := map[int]redisRecordedResponse{}
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/db34d9752ceddcde6db94eec5d681f6742d86403/") {
			t.Fatal("unpinned Search recording")
		}
		for _, row := range source.Recordings {
			count++
			if source.File == file {
				result[row.Index] = row
			}
		}
	}
	if count != 19 || len(result) == 0 {
		t.Fatal("missing native Search responses", count)
	}
	return result
}

func TestSearchRecordedDeletionAndNativeFinalAbsence(t *testing.T) {
	for _, tc := range []struct {
		file                         string
		read, parent, delete, absent int
		polls                        []int
	}{
		{"test_service_create_delete_show.yaml", 20, 20, 21, 22, nil},
		{"test_private_endpoint_connection_crud.yaml", 42, 40, 46, 47, nil},
		{"test_shared_private_link_resource_crud.yaml", 17, 3, 18, 22, []int{19, 20}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			rows := searchRecordings(t, tc.file)
			read := rows[tc.read]
			root := rows[tc.parent].Body
			id, kind, err := parseID(text(read.Body["id"]))
			if err != nil {
				t.Fatal(err)
			}
			mapping, _ := findType(kind)
			kind = mapping.NativeType
			s := newDNSScenario()
			s.add(read.Body, "2025-05-01")
			s.add(root, "2025-05-01")
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			if kind == searchType {
				for _, child := range serviceChildKinds(searchType) {
					s.lists[id+"/"+strings.ToLower(last(child))] = []any{}
				}
			}
			if kind == searchLinkType {
				// The CLI creates its data source outside this recording. This supporting
				// GET is synthetic and uses the target's pinned Storage API, not Search.
				targetID, _ := searchLinkTarget(read.Body)
				targetKind, _ := findType(storageType)
				s.add(map[string]any{"id": targetID, "name": last(targetID), "type": storageType, "location": root["location"], "properties": map[string]any{"creationTime": "2025-09-17T00:00:00Z"}}, targetKind.Version)
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, read.Body)
			target.Location = resourceRegion(root)
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			deleted, polls, absent := false, 0, false
			endpoint := operationLocation(rows[tc.delete].response().Header)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					// Native CLI root is 2025-05-01. Its older child commands record
					// 2022-09-01; replay preserves response bytes and returned poll URLs,
					// while our independently bound child request uses 2025-05-01.
					if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != "2025-05-01" || deleted {
						t.Fatal("Search native DELETE changed identity/version")
					}
					deleted = true
					return rows[tc.delete].response(), true
				}
				if endpoint != "" && req.URL.String() == endpoint {
					i := tc.polls[min(polls, len(tc.polls)-1)]
					polls++
					return rows[i].response(), true
				}
				if absent && strings.EqualFold(req.URL.Path, id) {
					return rows[tc.absent].response(), true
				}
				return nil, false
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
			result, err := driver.Execute(ctx, request)
			if err != nil || !deleted || result.ProviderOperationID != endpoint {
				t.Fatal("recorded Search DELETE failed", err)
			}
			payload, _ := json.Marshal(result)
			json.Unmarshal(payload, &result)
			payload, _ = json.Marshal(request)
			json.Unmarshal(payload, &request)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			for i := 0; i < max(1, len(tc.polls)); i++ {
				waited, err := driver.Wait(ctx, request, result)
				if err != nil || waited.Done {
					t.Fatal("Search operation completed before resource absence", err)
				}
			}
			absent = true
			waited, err := driver.Wait(ctx, request, result)
			if err != nil || !waited.Done {
				t.Fatal("native recorded Search 404 did not complete delete", err)
			}
			payload, _ = json.Marshal(logs)
			u, _ := url.Parse(endpoint)
			for _, key := range []string{"t", "c", "s", "h"} {
				if v := u.Query().Get(key); v != "" && bytes.Contains(payload, []byte(v)) {
					t.Fatal("Search signed operation leaked", key)
				}
			}
		})
	}
}
