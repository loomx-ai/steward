package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type redisRecordedResponse struct {
	grafanaRecordedResponse
	URI    string `json:"uri"`
	Method string `json:"method"`
	Index  int    `json:"interaction_index"`
}

func redisRecordings(t *testing.T, file string) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/redis/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File       string                  `json:"file"`
		Source     string                  `json:"source_uri"`
		SHA        string                  `json:"source_sha256"`
		Recordings []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 8 {
		t.Fatal("invalid native Redis recordings", err)
	}
	count := 0
	for _, source := range sources {
		count += len(source.Recordings)
		if len(source.SHA) != 64 || (!strings.Contains(source.Source, "/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/") && !strings.Contains(source.Source, "/5813689875f128709e8db10903e893138a064236/")) {
			t.Fatal("unpinned Redis recording")
		}
	}
	if count != 61 {
		t.Fatal("missing Redis native recordings", count)
	}
	for _, source := range sources {
		if source.File == file {
			rows := map[int]redisRecordedResponse{}
			for _, row := range source.Recordings {
				rows[row.Index] = row
			}
			return rows
		}
	}
	t.Fatal("missing Redis source", file)
	return nil
}
func TestRedisRecordedDeletesSignedPollingAndRestart(t *testing.T) {
	for _, tc := range []struct {
		file                                 string
		read, parent, database, peer, delete int
		polls                                []int
	}{
		{"test_redis_cache_authentication.yaml", 50, 26, 0, 0, 52, []int{53, 57}},
		{"test_redis_cache_authentication.yaml", 36, 26, 0, 0, 59, []int{60, 62}},
		{"test_redis_cache_firewall.yaml", 27, 23, 0, 0, 28, nil},
		{"test_redis_cache_firewall.yaml", 23, 0, 0, 0, 29, []int{30, 38}},
		{"test_redis_cache_patch_schedule.yaml", 25, 22, 0, 0, 26, nil},
		{"test_redis_cache_server_link.yaml", 58, 22, 0, 43, 61, []int{62, 65}},
		{"test_redisenterprise_scenario1.yaml", 19, 0, 0, 0, 47, []int{48, 62}},
		{"test_redisenterprise_scenario2.yaml", 43, 15, 0, 0, 47, []int{48, 50}},
		{"test_redisenterprise_scenario4.yaml", 48, 17, 41, 0, 50, []int{51, 52}},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.file, tc.delete), func(t *testing.T) {
			rows := redisRecordings(t, tc.file)
			read, deleteRecord := rows[tc.read], rows[tc.delete]
			id, kind, err := parseID(text(read.Body["id"]))
			if err != nil {
				t.Fatal(err)
			}
			kind = redisKind(kind)
			rootID := redisRootID(id)
			root := read.Body
			if tc.parent != 0 {
				root = rows[tc.parent].Body
			}
			s := newDNSScenario()
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			setup := []map[string]any{read.Body}
			if tc.parent != 0 {
				setup = append(setup, root)
			}
			if tc.database != 0 {
				setup = append(setup, rows[tc.database].Body)
			}
			if tc.peer != 0 {
				setup = append(setup, rows[tc.peer].Body)
			}
			readURL, _ := url.Parse(read.URI)
			version := readURL.Query().Get("api-version")
			for _, raw := range setup {
				resourceID, nativeType, err := parseID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				nativeType = redisKind(nativeType)
				s.add(raw, version)
				if nativeType == redisType || nativeType == redisEnterpriseType {
					list := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(nativeType)
					s.lists[list] = append(s.lists[list], raw)
					s.version[list] = version
				}
				if nativeType == redisType || nativeType == redisEnterpriseType || nativeType == redisDatabaseType {
					for _, child := range serviceChildKinds(nativeType) {
						path := resourceID + "/" + strings.ToLower(last(child))
						s.lists[path] = []any{}
						s.version[path] = version
					}
				}
			}
			if kind == redisLinkType {
				// Root GETs precede link creation in this recording. Stitch their later
				// link index from the recorded List; the peer has no recorded reverse view.
				object(root["properties"])["linkedServers"] = []any{map[string]any{"id": id}}
				s.lists[rootID+"/linkedservers"] = []any{read.Body}
			}
			if kind == redisPolicyType {
				s.lists[rootID+"/accesspolicyassignments"] = []any{}
			}
			r := s.runtime(t)
			enriched := map[string]any{}
			for k, v := range read.Body {
				enriched[k] = v
			}
			enriched["location"] = root["location"]
			target := dnsAsset(t, r, enriched)
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			writes, polls := 0, 0
			endpoint := operationLocation(deleteRecord.response().Header)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != version {
						t.Fatal("recorded Redis DELETE changed its bound resource")
					}
					writes++
					return deleteRecord.response(), true
				}
				if endpoint != "" && req.URL.String() == endpoint {
					if req.Method != "GET" || len(tc.polls) == 0 {
						t.Fatal("Redis polling became a mutation")
					}
					index := tc.polls[min(polls, len(tc.polls)-1)]
					polls++
					return rows[index].response(), true
				}
				return nil, false
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			result, err := driver.Execute(ctx, request)
			if err != nil || writes != 1 || result.ProviderOperationID != endpoint {
				t.Fatal("recorded Redis DELETE failed", err, writes)
			}
			payload, _ := json.Marshal(result)
			json.Unmarshal(payload, &result)
			payload, _ = json.Marshal(request)
			json.Unmarshal(payload, &request)
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", request.Asset)
			for i := 0; i < max(len(tc.polls), 1); i++ {
				wait, err := driver.Wait(ctx, request, result)
				if err != nil || wait.Done {
					t.Fatal("recorded Redis operation bypassed live resource readback", err)
				}
			}
			s.gone[id] = true
			if kind == redisLinkType {
				object(root["properties"])["linkedServers"] = []any{}
			}
			wait, err := driver.Wait(ctx, request, result)
			if err != nil || !wait.Done {
				t.Fatal("recorded Redis terminal operation plus synthetic 404 failed", err, wait)
			}
			payload, _ = json.Marshal(logs)
			if endpoint != "" {
				u, _ := url.Parse(endpoint)
				for _, key := range []string{"t", "c", "s", "h"} {
					if value := u.Query().Get(key); value != "" && bytes.Contains(payload, []byte(value)) {
						t.Fatal("Redis poll signing material leaked to logs", key)
					}
				}
			}
		})
	}
}
