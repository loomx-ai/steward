package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func streamAnalyticsRecordings(t *testing.T, file string) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/streamanalytics/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 8 {
		t.Fatal("invalid Stream Analytics recordings", err)
	}
	result, total := map[int]redisRecordedResponse{}, 0
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/0349eb646d3225db5fd677114e200efdfd11e3f8/") {
			t.Fatal("unpinned recording")
		}
		total += len(source.Records)
		if source.File == file+".yaml" {
			for _, row := range source.Records {
				result[row.Index] = row
			}
		}
	}
	if total != 72 || len(result) == 0 {
		t.Fatal("missing native Stream Analytics responses", total)
	}
	return result
}

func streamAnalyticsRecordedHTTP(row redisRecordedResponse) *http.Response {
	headers := http.Header{}
	for key, values := range row.Headers {
		for _, value := range values {
			headers.Add(key, value)
		}
	}
	var payload []byte
	if row.Body != nil {
		payload, _ = json.Marshal(row.Body)
	}
	return &http.Response{StatusCode: row.Status, Header: headers, Body: io.NopCloser(bytes.NewReader(payload))}
}

func TestStreamAnalyticsRecordedDeletesRequireResumedAbsence(t *testing.T) {
	for _, test := range []struct {
		file                      string
		parent, read, delete, end int
	}{
		{"test_job_crud", 5, 5, 6, 6},
		{"test_input_crud", 1, 11, 12, 12},
		{"test_output_crud", 1, 10, 11, 11},
		{"test_private_endpoint_crud", 62, 67, 68, 85},
		{"test_cluster_crud", 337, 337, 338, 371},
	} {
		t.Run(test.file, func(t *testing.T) {
			rows := streamAnalyticsRecordings(t, test.file)
			s := newDNSScenario()
			raw, deletion := rows[test.read].Body, rows[test.delete]
			id, kind, err := parseID(text(raw["id"]))
			if err != nil || !isStreamAnalyticsType(kind) {
				t.Fatal("invalid native resource", err)
			}
			// Earlier original parent bodies supply terminal state. Supporting indexes,
			// storage reads and final absence are synthetic, not a full CLI timeline.
			s.add(rows[test.parent].Body, streamAnalyticsVersion)
			s.add(raw, streamAnalyticsVersion)
			root := "/subscriptions/" + testSubscription
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			mapping, _ := findType(storageType)
			s.lists[root+"/providers/"+strings.ToLower(storageType)] = []any{}
			s.version[root+"/providers/"+strings.ToLower(storageType)] = mapping.Version
			if streamAnalyticsKind(kind) == streamAnalyticsEndpointType {
				ids, err := streamAnalyticsEndpointTargets(raw)
				if err != nil {
					t.Fatal(err)
				}
				for _, targetID := range ids {
					s.add(map[string]any{"id": targetID, "name": last(targetID), "type": storageType, "location": "westus", "properties": map[string]any{}}, mapping.Version)
				}
			}
			for resourceID := range s.records {
				_, typ, _ := parseID(resourceID)
				for _, child := range streamAnalyticsOwnedKinds(typ) {
					if child == streamAnalyticsTransformationType {
						continue
					}
					path := resourceID + "/" + strings.ToLower(last(child))
					s.lists[path], s.version[path] = []any{}, streamAnalyticsVersion
				}
			}
			headers := streamAnalyticsRecordedHTTP(deletion).Header
			operation := headers.Get("Location")
			var polls []redisRecordedResponse
			for i := test.delete + 1; i <= test.end; i++ {
				row := rows[i]
				expected := operation
				if len(polls) > 0 {
					expected = streamAnalyticsRecordedHTTP(polls[len(polls)-1]).Header.Get("Location")
				}
				if row.Method != "GET" || row.URI != expected {
					t.Fatal("broken native polling sequence", i)
				}
				polls = append(polls, row)
			}
			deleted, absent, pollIndex := false, false, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					expected, _ := url.Parse(deletion.URI)
					// Input recording uses 2021-10-01-preview; stable 2020-03-01 is
					// selected here. All original response fields and LRO URLs remain.
					if !strings.EqualFold(req.URL.Path, expected.Path) || req.URL.Query().Get("api-version") != streamAnalyticsVersion {
						t.Fatal("wrong native DELETE", req.URL)
					}
					deleted = true
					return streamAnalyticsRecordedHTTP(deletion), true
				}
				if operation != "" && strings.Contains(strings.ToLower(req.URL.Path), "/operationresults/") {
					if req.URL.String() != polls[pollIndex].URI {
						t.Fatal("did not resume the refreshed native Location")
					}
					row := polls[pollIndex]
					if pollIndex < len(polls)-1 {
						pollIndex++
					}
					return streamAnalyticsRecordedHTTP(row), true
				}
				if strings.EqualFold(req.URL.Path, id) && absent {
					return jsonResponse(404, nil, nil), true
				}
				if req.Method == "POST" && strings.HasSuffix(strings.ToLower(req.URL.Path), "/liststreamingjobs") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return nil, false
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, raw)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || !deleted {
				t.Fatal("native Stream Analytics delete", err)
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
				waited, err := driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done {
					t.Fatal("native completion skipped resource absence", waited, err)
				}
				if waited.Data != nil {
					result.Data = waited.Data
				}
				payload, _ := json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
			}
			absent = true
			if waited, err := driver.Wait(context.Background(), request, result); err != nil || !waited.Done {
				t.Fatal("native deletion never reached confirmed absence", waited, err)
			}
			deleted = false
			if _, err := driver.Execute(context.Background(), request); err != nil || deleted {
				t.Fatal("repeated native DELETE", err)
			}
			if result.ProviderOperationID != operation {
				t.Fatal("native signed operation URL changed")
			}
		})
	}
}

func TestStreamAnalyticsRecordedEmptyResultIsNarrow(t *testing.T) {
	row := streamAnalyticsRecordings(t, "test_job_scale")[17]
	if row.Status != 200 || row.Body != nil {
		t.Fatal("native empty result changed")
	}
	for _, mode := range []string{"native-empty", "null", "resource-empty", "wrong-subscription", "unsigned"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			endpoint := row.URI
			switch mode {
			case "resource-empty":
				endpoint = apiURL(cdnAsset(t, assets, streamAnalyticsJobType).Identity.NativeID, streamAnalyticsVersion)
			case "wrong-subscription":
				endpoint = strings.ReplaceAll(endpoint, testSubscription, "foreign")
			case "unsigned":
				u, _ := url.Parse(endpoint)
				q := u.Query()
				q.Del("s")
				u.RawQuery = q.Encode()
				endpoint = u.String()
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.String() != endpoint {
					return nil, false
				}
				res := streamAnalyticsRecordedHTTP(row)
				if mode == "null" {
					res.Body = io.NopCloser(strings.NewReader("null"))
				}
				return res, true
			}
			c, _ := r.resolve(context.Background(), "connection")
			_, err := c.requestAt(context.Background(), "GET", endpoint, nil, nil, c.validateURL)
			if (err == nil) != (mode == "native-empty") {
				t.Fatal("empty result boundary", mode, err)
			}
		})
	}
}

func TestStreamAnalyticsPrivateEndpointNativeEnvelopeDoesNotSkipReadback(t *testing.T) {
	for _, mode := range []string{"native", "pending", "accepted", "status-protocol", "missing-error", "error", "other-kind", "extra-field"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target := cdnAsset(t, assets, streamAnalyticsEndpointType)
			if mode == "other-kind" {
				target = cdnAsset(t, assets, streamAnalyticsClusterType)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			a := monitorTargetInner(driver).(*action)
			row := streamAnalyticsRecordings(t, "test_private_endpoint_crud")[85]
			u, _ := url.Parse(row.URI)
			u.Path = target.Identity.NativeID + "/OperationResults/" + last(u.Path)
			result := contracts.ActionResult{ProviderOperationID: u.String(), Data: map[string]any{"polling": "location", "stream_analytics_operation_binding": a.operationBinding(u.String())}}
			switch mode {
			case "pending":
				row.Body["status"] = "Pending"
			case "accepted":
				row.Status = 202
			case "status-protocol":
				result.Data["polling"] = "status"
			case "missing-error":
				delete(row.Body, "error")
			case "error":
				row.Body["error"] = map[string]any{"code": "Conflict"}
			case "extra-field":
				row.Body["progress"] = 1
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.String() == u.String() {
					return streamAnalyticsRecordedHTTP(row), true
				}
				return nil, false
			}
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			result = monitorTargetTestReceipt(driver, request, result)
			if waited, err := driver.Wait(context.Background(), request, result); waited.Done || mode == "error" && err == nil || mode != "error" && err != nil {
				t.Fatal("live resource accepted", waited, err)
			}
			s.gone[target.Identity.NativeID] = true
			waited, err := driver.Wait(context.Background(), request, result)
			if mode == "native" {
				if err != nil || !waited.Done {
					t.Fatal("native final envelope rejected", waited, err)
				}
			} else if waited.Done {
				t.Fatal("unproven envelope accepted", mode)
			}
		})
	}
}
