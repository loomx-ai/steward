package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const batchRecordingHash = "e96fff896dabc1bb5cc05e86bf56c258293808d88fcc46af727cd210c9fc6759"

func batchRecordings(t *testing.T, file string) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/batch/cli-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	// Hash the reproducible extraction before adapting the subscription in copies.
	if fmt.Sprintf("%x", sha256.Sum256(payload)) != batchRecordingHash {
		t.Fatal("native Batch recording extraction changed")
	}
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 8 {
		t.Fatal("invalid Batch recordings")
	}
	total, result := 0, map[int]redisRecordedResponse{}
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/") {
			t.Fatal("unpinned Batch recording")
		}
		total += len(source.Records)
		if source.File == file+".yaml" {
			for _, row := range source.Records {
				result[row.Index] = row
			}
		}
	}
	if total != 75 || len(result) == 0 {
		t.Fatal("missing native Batch recordings", total)
	}
	return result
}

func TestBatchRecordedApplicationCleanupUsesNativePackageAndDefault(t *testing.T) {
	rows := batchRecordings(t, "test_batch_application_cmd")
	// The account and application recordings share their native account ID.
	// Supporting empty service indexes and post-delete GET 404s are composed.
	account := batchRecordings(t, "test_batch_general_arm_cmd")[8].Body
	id := strings.ToLower(text(account["id"]))
	endpoint, err := batchAccountEndpoint(account)
	if err != nil {
		t.Fatal(err)
	}
	s := newDNSScenario()
	s.add(account, batchVersion)
	application, pkg := rows[29].Body, rows[25].Body
	appID, packageID := strings.ToLower(text(application["id"])), strings.ToLower(text(pkg["id"]))
	if batchAccountID(appID) != id || redisParentID(packageID) != appID {
		t.Fatal("recorded application does not belong to its account")
	}
	s.add(application, batchVersion)
	s.add(pkg, batchVersion)
	group := strings.Join(strings.Split(id, "/")[:5], "/")
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	for _, kind := range []string{batchPoolType, batchApplicationType, batchPECType, batchPerimeterType} {
		s.lists[id+"/"+strings.ToLower(last(kind))] = []any{}
		s.version[id+"/"+strings.ToLower(last(kind))] = batchVersion
	}
	s.lists[id+"/applications"] = []any{application}
	s.lists[appID+"/versions"], s.version[appID+"/versions"] = []any{pkg}, batchVersion
	deletions := []int{}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Scheme+"://"+req.URL.Host == endpoint {
			if req.Method != "GET" || !slices.Contains([]string{"/jobs", "/jobschedules", "/pools"}, req.URL.Path) {
				t.Fatal("unexpected supporting application data request")
			}
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		if req.Method == "DELETE" {
			index := 31
			if len(deletions) > 0 {
				index = 33
			}
			u, _ := url.Parse(batchRecordedVersion(t, rows[index].URI))
			if !strings.EqualFold(req.URL.Path, u.Path) || req.URL.Query().Get("api-version") != batchVersion {
				t.Fatal("recorded package/application deletion order changed")
			}
			deletions = append(deletions, index)
			return batchRecordedHTTP(t, rows[index]), true
		}
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, id+"/applications") && s.gone[appID] {
			return batchRecordedHTTP(t, rows[35]), true
		}
		return nil, false
	}
	r := s.runtime(t)
	assets := []asset.Asset{dnsAsset(t, r, account), dnsAsset(t, r, application), dnsAsset(t, r, pkg)}
	solved := batchPlan(t, r, assets, assets[1])
	if len(solved.Blockers) != 0 || len(solved.Steps) != 2 || solved.Steps[0].AssetID != assets[2].ID {
		t.Fatal("native default package was not a prior deletion", solved.Blockers)
	}
	for _, target := range []asset.Asset{assets[2], assets[1]} {
		request := servicePlanRequest(solved, assets, target)
		driver, _ := r.ResolveAction(t.Context(), "connection", target)
		receipt, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("recorded application deletion", target.Identity.NativeType, err)
		}
		payload, _ := json.Marshal(receipt)
		json.Unmarshal(payload, &receipt)
		payload, _ = json.Marshal(request)
		json.Unmarshal(payload, &request)
		driver, _ = r.ResolveAction(t.Context(), "connection", request.Asset)
		if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || wait.Done {
			t.Fatal("native empty 200 skipped independent absence", wait, err)
		}
		s.gone[target.Identity.NativeID] = true
		if target.Identity.NativeType == batchPackageType {
			// Documented default-version clearing is composed; CLI does not
			// read this intermediate application state after package deletion.
			delete(object(application["properties"]), "defaultVersion")
		}
		if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
			t.Fatal("recorded application absence", wait, err)
		}
	}
	if !slices.Equal(deletions, []int{31, 33}) {
		t.Fatal("native package/application mutation count changed", deletions)
	}
}

func TestBatchRecordedPrivateEndpointDiscovery(t *testing.T) {
	rows := batchRecordings(t, "test_batch_privateendpoint_cmd")
	s := newDNSScenario()
	account, connection := rows[3].Body, rows[25].Body
	id := strings.ToLower(text(account["id"]))
	s.add(account, batchVersion)
	s.add(connection, batchVersion)
	root := "/subscriptions/" + testSubscription
	group := strings.Join(strings.Split(id, "/")[:5], "/")
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	s.lists[root+"/providers/microsoft.batch/batchaccounts"] = []any{account}
	s.version[root+"/providers/microsoft.batch/batchaccounts"] = batchVersion
	reads := map[int]int{}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		for _, index := range []int{24, 25} {
			u, _ := url.Parse(batchRecordedVersion(t, rows[index].URI))
			if strings.EqualFold(req.URL.Path, u.Path) {
				if req.Method != "GET" || req.URL.Query().Get("api-version") != batchVersion {
					t.Fatal("recorded private endpoint request changed")
				}
				reads[index]++
				return batchRecordedHTTP(t, rows[index]), true
			}
		}
		return nil, false
	}
	r := s.runtime(t)
	page, err := r.List(t.Context(), productRequest(r, batchPECType))
	if err != nil || !page.Complete || len(page.Items) != 1 || reads[24] == 0 || reads[25] == 0 {
		t.Fatal("recorded private endpoint inventory", err, reads)
	}
	item := page.Items[0]
	privateEndpointID := strings.ToLower(text(object(object(connection["properties"])["privateEndpoint"])["id"]))
	if item.NativeID != strings.ToLower(text(connection["id"])) || !slices.Contains(item.NetworkReferences, privateEndpointID) || item.Normalized["_batch_account"] != id {
		t.Fatal("recorded private endpoint identity or network reference changed")
	}
}

// The CLI recordings use ARM 2024-02-01 / data 2024-02-01.19.0. Only the API
// version in copied request and polling URLs is bridged to the pinned 2025 API.
// This verifies its wire shape, not live validity of an older signed URL.
func batchRecordedVersion(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if query.Get("api-version") != "2024-02-01" && query.Get("api-version") != "2024-02-01.19.0" {
		t.Fatal("unexpected recorded Batch version")
	}
	query.Set("api-version", batchVersion)
	u.RawQuery = query.Encode()
	return u.String()
}

func batchRecordedHTTP(t *testing.T, row redisRecordedResponse) *http.Response {
	t.Helper()
	result := streamAnalyticsRecordedHTTP(row)
	for _, name := range []string{"Location", "Azure-AsyncOperation"} {
		if endpoint := result.Header.Get(name); endpoint != "" {
			result.Header.Set(name, batchRecordedVersion(t, endpoint))
		}
	}
	return result
}

func TestBatchRecordedAccountDeletionResumesRotatingLocation(t *testing.T) {
	for _, test := range []struct {
		file           string
		read, deletion int
	}{{"test_batch_general_arm_cmd", 8, 13}, {"test_batch_byos_account_cmd", 11, 14}} {
		t.Run(test.file, func(t *testing.T) {
			rows := batchRecordings(t, test.file)
			s := newDNSScenario()
			raw := rows[test.read].Body
			id := strings.ToLower(text(raw["id"]))
			endpoint, err := batchAccountEndpoint(raw)
			if err != nil {
				t.Fatal(err)
			}
			s.add(raw, batchVersion)
			root := "/subscriptions/" + testSubscription
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			for _, kind := range []string{batchPoolType, batchApplicationType, batchPECType, batchPerimeterType} {
				s.lists[id+"/"+strings.ToLower(last(kind))] = []any{}
				s.version[id+"/"+strings.ToLower(last(kind))] = batchVersion
			}
			polls, deleted := 0, false
			expected := batchRecordedHTTP(t, rows[test.deletion]).Header.Get("Location")
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Scheme+"://"+req.URL.Host == endpoint {
					// Supporting empty indexes are composed, not recorded account
					// cleanup steps. No real cloud request is sent.
					if req.Method != "GET" || (req.URL.Path != "/pools" && req.URL.Path != "/jobs" && req.URL.Path != "/jobschedules") {
						t.Fatal("unexpected supporting Batch request")
					}
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				if req.Method == "DELETE" {
					if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != batchVersion {
						t.Fatal("wrong recorded account deletion")
					}
					deleted = true
					return batchRecordedHTTP(t, rows[test.deletion]), true
				}
				if strings.Contains(strings.ToLower(req.URL.Path), "/accountoperationresults/") {
					if req.URL.String() != expected {
						t.Fatal("did not resume the native successor Location")
					}
					index := test.deletion + min(polls+1, 2)
					response := batchRecordedHTTP(t, rows[index])
					if next := response.Header.Get("Location"); next != "" {
						expected = next
					}
					polls++
					return response, true
				}
				return nil, false
			}
			r := s.runtime(t)
			value := dnsAsset(t, r, raw)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			driver, _ := r.ResolveAction(t.Context(), "connection", value)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil || !deleted {
				t.Fatal("recorded Batch account delete", err)
			}
			for i := 0; i < 3; i++ {
				payload, _ := json.Marshal(receipt)
				json.Unmarshal(payload, &receipt)
				driver, _ = r.ResolveAction(t.Context(), "connection", value)
				if i == 2 {
					s.gone[id] = true // Fresh native absence is composed separately.
				}
				wait, err := driver.Wait(t.Context(), request, receipt)
				if err != nil || wait.Done != (i == 2) {
					t.Fatal("recorded account polling skipped native absence", i, wait.Done, err)
				}
				receipt.Data = wait.Data
			}
		})
	}
}

func TestBatchRecordedDataReadsAndMutationsUseAccountAuthority(t *testing.T) {
	for _, test := range []struct {
		file                    string
		account, read, mutation int
		operation               string
	}{
		{"test_batch_jobs_and_tasks", 4, 16, 25, "Tasks_DeleteTask"},
		{"test_batch_jobs_and_tasks", 4, 20, 21, "Tasks_TerminateTask"},
		{"test_batch_pools_and_nodes", 4, 48, 51, "Pools_RemoveNodes"},
	} {
		t.Run(test.operation, func(t *testing.T) {
			rows := batchRecordings(t, test.file)
			s := newDNSScenario()
			raw := rows[test.account].Body
			id := strings.ToLower(text(raw["id"]))
			s.add(raw, batchVersion)
			s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.batch/batchaccounts"] = []any{raw}
			mutation, read := rows[test.mutation], rows[test.read]
			expected, _ := url.Parse(batchRecordedVersion(t, mutation.URI))
			calls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Host == "management.azure.com" {
					return nil, false
				}
				if req.Method == "GET" {
					if !strings.EqualFold(req.URL.Scheme+"://"+req.URL.Host+req.URL.Path, text(read.Body["url"])) {
						t.Fatal("wrong native Batch data read")
					}
					return batchRecordedHTTP(t, read), true
				}
				calls++
				if req.Method != mutation.Method || !strings.EqualFold(req.URL.Path, expected.Path) || req.URL.Query().Get("api-version") != batchVersion || req.Header.Get("If-Match") == "" {
					t.Fatal("wrong native Batch data mutation")
				}
				if test.operation == "Pools_RemoveNodes" {
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					if len(array(body["nodeList"])) != 1 || array(body["nodeList"])[0] != read.Body["id"] || body["nodeDeallocationOption"] != "requeue" {
						t.Fatal("recorded node removal widened membership")
					}
				}
				return batchRecordedHTTP(t, mutation), true
			}
			r := s.runtime(t)
			c, _ := r.resolve(t.Context(), "connection")
			account, err := c.batchAccount(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			nativeID, kind, _, params, err := batchDataIdentity(text(read.Body["url"]))
			if err != nil {
				t.Fatal(err)
			}
			current, err := c.batchRead(t.Context(), account, nativeID, kind)
			if err != nil || current.requestID == "" {
				t.Fatal("recorded Batch read", err)
			}
			params["If-Match"] = text(read.Body["eTag"])
			if test.operation == "Pools_RemoveNodes" {
				delete(params, "nodeId")
				params["removeOptions"] = map[string]any{"nodeList": []string{text(read.Body["id"])}, "nodeDeallocationOption": "requeue"}
				params["If-Match"] = text(rows[46].Body["eTag"])
			}
			metadata, _ := providerData()
			op, _ := metadata.catalog.Operation(batchDataPrefix + test.operation)
			if _, err := c.invokeBatch(t.Context(), op, contracts.Invocation{Parameters: params, IdempotencyKey: "recorded-mutation"}); err != nil || calls != 1 {
				t.Fatal("recorded Batch mutation", calls, err)
			}
		})
	}
}

func TestBatchRecordedAutoPoolUsesActualJobSpecification(t *testing.T) {
	rows := batchRecordings(t, "test_batch_job_list_cmd")
	raw := rows[4].Body
	id := strings.ToLower(text(raw["id"]))
	endpoint, err := batchAccountEndpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	account := batchAccountContext{id: id, endpoint: endpoint, location: resourceRegion(raw), raw: raw}
	topology := batchTopology{account: account, members: map[string]batchMember{}}
	scheduleID := endpoint + "/jobschedules/xplatjobschedulejobtests"
	topology.members[scheduleID] = batchMember{id: scheduleID, kind: batchScheduleType}
	jobs := array(rows[7].Body["value"])
	if len(jobs) != 1 {
		t.Fatal("recorded schedule membership changed")
	}
	job := object(jobs[0])
	jobID := strings.ToLower(text(job["url"]))
	poolID := id + "/pools/" + strings.ToLower(text(object(job["executionInfo"])["poolId"]))
	topology.members[jobID] = batchMember{id: jobID, kind: batchJobType, parent: scheduleID, raw: job}
	topology.members[poolID] = batchMember{id: poolID, kind: batchPoolType, parent: id, direct: true}
	if err := topology.autoPools(); err != nil || topology.members[poolID].parent != scheduleID || topology.members[poolID].direct {
		t.Fatal("recorded generated job did not prove its auto pool lifetime", err)
	}
}
