package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func kustoRecordings(t *testing.T) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/kusto/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var source struct {
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &source) != nil || len(source.Records) != 45 || source.SHA != "653dc385bfeae90470db0f4749ca4d7c2f435b2401d300421a65a8fb4bb90ee1" || !strings.Contains(source.Source, "/0349eb646d3225db5fd677114e200efdfd11e3f8/") {
		t.Fatal("invalid native Kusto recording", err)
	}
	result := map[int]redisRecordedResponse{}
	for _, row := range source.Records {
		result[row.Index] = row
	}
	return result
}

func TestKustoRecordedNativeDeletesAndResumedReadback(t *testing.T) {
	for _, test := range []struct{ read, delete int }{{183, 285}, {152, 289}, {190, 291}, {148, 294}, {145, 296}, {179, 303}, {63, 305}, {248, 307}} {
		t.Run(strconv.Itoa(test.delete), func(t *testing.T) {
			rows := kustoRecordings(t)
			s := newDNSScenario()
			// Earlier terminal parent states are original responses, not the state
			// immediately preceding DELETE. Database GET 63 predates attachment.
			for _, i := range []int{28, 63, 248, test.read} {
				s.add(rows[i].Body, kustoVersion)
			}
			raw, deletion := rows[test.read].Body, rows[test.delete]
			id, kind, err := parseID(text(raw["id"]))
			if err != nil || !isKustoType(kind) {
				t.Fatal("invalid native Kusto resource", err)
			}
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			if kustoKind(kind) == kustoManagedEndpointType {
				// No target-storage GET was recorded. This explicitly synthetic
				// resource supplies native target/configuration/protection reads.
				targetID, err := kustoEndpointTarget(raw)
				if err != nil {
					t.Fatal(err)
				}
				mapping, _ := findType("Microsoft.Storage/storageAccounts")
				s.add(map[string]any{"id": targetID, "name": last(targetID), "type": mapping.NativeType, "location": "westus2", "properties": map[string]any{}}, mapping.Version)
				groupID := strings.Join(strings.Split(targetID, "/")[:5], "/")
				s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = append(s.lists["/subscriptions/"+testSubscription+"/resourcegroups"], map[string]any{"id": groupID, "type": groupType})
			}
			// Synthetic indexes represent reviewed prerequisite removal. They are
			// not a full reproduction of the recorded CLI creation/update timeline.
			for resourceID, body := range s.records {
				_, resourceKind, _ := parseID(resourceID)
				for _, child := range kustoOwnedKinds(resourceKind) {
					path := resourceID + "/" + strings.ToLower(last(child))
					s.lists[path], s.version[path] = []any{}, kustoVersion
				}
				if kustoKind(resourceKind) == kustoType {
					path := resourceID + "/listfollowerdatabases"
					s.lists[path], s.version[path] = []any{}, kustoVersion
					if len(array(object(body["properties"])["privateEndpointConnections"])) != 0 {
						t.Fatal("unmodeled native endpoint index")
					}
				}
			}
			headers := http.Header{}
			for key, values := range deletion.Headers {
				for _, value := range values {
					headers.Add(key, value)
				}
			}
			operation := headers.Get("Azure-AsyncOperation")
			operationURL, _ := url.Parse(operation)
			var polls []redisRecordedResponse
			for i := test.delete + 1; i <= 322; i++ {
				row := rows[i]
				if row.Method == "GET" && row.URI == operationURL.String() {
					polls = append(polls, row)
				}
			}
			if len(polls) == 0 || polls[len(polls)-1].Body["status"] != "Succeeded" {
				t.Fatal("native completion missing")
			}
			deleted, absent, pollIndex := false, false, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					expected, _ := url.Parse(deletion.URI)
					// Explicit bridge: selected resource API is 2025-02-14; the
					// original 2022-02-01 response and LRO URLs remain unchanged.
					if !strings.EqualFold(req.URL.Path, expected.Path) || req.URL.Query().Get("api-version") != kustoVersion {
						t.Fatal("wrong Kusto DELETE", req.URL)
					}
					deleted = true
					return jsonResponse(deletion.Status, deletion.Body, headers), true
				}
				if req.URL.String() == operationURL.String() {
					row := polls[pollIndex]
					if pollIndex < len(polls)-1 {
						pollIndex++
					}
					return jsonResponse(row.Status, row.Body, nil), true
				}
				if strings.EqualFold(req.URL.Path, id) && absent {
					return jsonResponse(404, nil, nil), true
				}
				return nil, false
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, raw)
			if target.Location != "westus2" {
				t.Fatal("native display/proxy location was not normalized", target.Location)
			}
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || !deleted {
				t.Fatal("native Kusto deletion", err)
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
					t.Fatal("native LRO skipped absence check", waited, err)
				}
			}
			// The recording ends at LRO success. Final absence is synthetic.
			absent = true
			if waited, err := driver.Wait(context.Background(), request, result); err != nil || !waited.Done {
				t.Fatal("Kusto final absence", waited, err)
			}
			deleted = false
			if _, err := driver.Execute(context.Background(), request); err != nil || deleted {
				t.Fatal("Kusto repeated deletion", err)
			}
			if result.ProviderOperationID != operation {
				t.Fatal("native operation URL changed")
			}
		})
	}
}

func TestKustoRecordedEmptyLocationResult(t *testing.T) {
	row := kustoRecordings(t)[301]
	if row.Status != 200 || row.Body != nil {
		t.Fatal("native empty result changed")
	}
	for _, mode := range []string{"native-empty", "null", "status-empty", "resource-empty"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			endpoint := row.URI
			if mode == "status-empty" {
				endpoint = strings.Replace(endpoint, "&operationResultResponseType=Location", "", 1)
			}
			if mode == "resource-empty" {
				endpoint = apiURL(assets[7].Identity.NativeID, kustoVersion)
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.String() != endpoint {
					return nil, false
				}
				body := ""
				if mode == "null" {
					body = "null"
				}
				return &http.Response{StatusCode: row.Status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, true
			}
			c, _ := r.resolve(context.Background(), "connection")
			result, err := c.requestAt(context.Background(), "GET", endpoint, nil, nil, func(value string) error {
				if mode == "resource-empty" {
					return c.validateURL(value)
				}
				return validateKustoOperationURL(testSubscription, "westus2", kustoVersion, value)
			})
			if mode == "native-empty" {
				if err != nil || len(result.data) != 0 {
					t.Fatal("native empty Kusto Location result rejected", err)
				}
			} else if err == nil {
				t.Fatal("empty resource or null operation accepted")
			}
		})
	}
}
