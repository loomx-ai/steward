package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type dataMigrationRecording struct {
	Index int `json:"index"`
	dataFactoryRecording
}

func dataMigrationRecordings(t *testing.T) []dataMigrationRecording {
	t.Helper()
	payload, err := os.ReadFile("fixtures/datamigration/cli-recordings.json")
	var sources []struct {
		URI     string                   `json:"source_uri"`
		SHA     string                   `json:"source_sha256"`
		Records []dataMigrationRecording `json:"records"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "510514a27d102fbeadf201a3ac10137c7f6a5b38e891b38a1d0e187fd0a582a5" {
		t.Fatal("native Data Migration recordings changed", err)
	}
	rows := []dataMigrationRecording{}
	for _, source := range sources {
		for _, row := range source.Records {
			row.SourceURI, row.SourceSHA = source.URI, source.SHA
			rows = append(rows, row)
		}
	}
	if len(sources) != 4 || len(rows) != 107 {
		t.Fatal("native recording coverage changed")
	}
	return rows
}

func TestDataMigrationOfficialCLIRecordingProvenance(t *testing.T) {
	methods, seen := map[string]int{}, map[string]bool{}
	sources := map[string]string{
		"test_datamigration_Scenario.yaml": "52afcca05398c948fe6ce59e37ae2387b3bc4b5736886998f224ca12ee09f7a1",
		"test_project_commands.yaml":       "04bc89f55029610584c0dd2980edbf14a5c98924c67c2562001b6b8aa2df6b9e",
		"test_service_commands.yaml":       "38dddb3d706d5b090126300aa97ed3b59fcbd7f95d9d1c1c2a8f740908ce4c4c",
		"test_task_commands.yaml":          "632eb25e8716c3196559ae1cbd5981795a33ddcd21dd0d1f99396c6dfeeb4428",
	}
	for _, row := range dataMigrationRecordings(t) {
		key := fmt.Sprintf("%s:%d", row.SourceURI, row.Index)
		if seen[key] || sources[last(row.SourceURI)] != row.SourceSHA || !(strings.HasPrefix(row.SourceURI, "https://raw.githubusercontent.com/Azure/azure-cli-extensions/d2f60986756c939c3d6d7f85e89798cca158d935/src/datamigration/") || strings.HasPrefix(row.SourceURI, "https://raw.githubusercontent.com/Azure/azure-cli/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/dms/")) {
			t.Fatal("native source identity changed", key)
		}
		seen[key] = true
		u, err := url.Parse(row.URL)
		if err != nil || u.Host != "management.azure.com" || !slices.Contains([]string{dataMigrationVersion, "2021-06-30"}, u.Query().Get("api-version")) || !strings.Contains(strings.ToLower(u.Path), "/providers/microsoft.datamigration/") || row.Body != "" && !json.Valid([]byte(row.Body)) {
			t.Fatal("native response scope changed", row.Index)
		}
		if row.Method == "POST" && !strings.HasSuffix(strings.ToLower(u.Path), "/cancel") && !strings.HasSuffix(strings.ToLower(u.Path), "/listmonitoringdata") {
			t.Fatal("unselected native mutation retained")
		}
		for name := range row.Headers {
			if !slices.Contains([]string{"content-type", "azure-asyncoperation", "location"}, strings.ToLower(name)) {
				t.Fatal("unselected response header retained")
			}
		}
		for _, endpoint := range []string{row.URL, row.header().Get("Azure-AsyncOperation"), row.header().Get("Location")} {
			u, _ := url.Parse(endpoint)
			for _, field := range []string{"t", "c", "s", "h"} {
				if value := u.Query().Get(field); value != "" && value != "replay-"+field {
					t.Fatal("native signed credential retained")
				}
			}
		}
		methods[row.Method]++
	}
	if methods["GET"] != 93 || methods["POST"] != 7 || methods["DELETE"] != 7 {
		t.Fatal("native method coverage changed", methods)
	}
}

func TestDataMigrationRecordedAsyncOperations(t *testing.T) {
	rows := dataMigrationRecordings(t)
	accepted, responses := 0, 0
	for _, initial := range rows {
		if initial.Method == "GET" || strings.HasSuffix(strings.ToLower(initial.URL), "/listmonitoringdata?api-version="+dataMigrationVersion) {
			continue
		}
		t.Run(fmt.Sprintf("%s/%d", last(initial.SourceURI), initial.Index), func(t *testing.T) {
			u, _ := url.Parse(initial.URL)
			phase := "delete"
			if initial.Method == "POST" {
				phase = "cancel"
			}
			id := strings.TrimSuffix(strings.ToLower(u.Path), "/cancel")
			_, typ, _ := parseID(id)
			kind := dataMigrationKind(typ)
			endpoint := initial.header().Get("Azure-AsyncOperation")
			if endpoint == "" {
				endpoint = initial.header().Get("Location")
			}
			pending := []dataMigrationRecording{}
			for _, row := range rows {
				if row.SourceURI == initial.SourceURI && row.Method == "GET" && row.URL == endpoint {
					pending = append(pending, row)
				}
			}
			next := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				if next >= len(pending) || req.Method != "GET" || req.URL.String() != pending[next].URL {
					t.Fatal("native poll order changed", next, req.URL.Path)
				}
				row := pending[next]
				next++
				return row.response(), nil
			})
			c.subscription = strings.Split(id, "/")[2]
			body := map[string]any{}
			if initial.Body != "" && json.Unmarshal([]byte(initial.Body), &body) != nil {
				t.Fatal("invalid native mutation body")
			}
			operation, err := c.dataMigrationReceipt(id, kind, phase, response{status: initial.Status, data: body, header: initial.header()})
			if err != nil {
				t.Fatal("native mutation receipt rejected", err)
			}
			accepted++
			if len(pending) == 0 {
				if len(operation) != 0 {
					t.Fatal("accepted native receipt lost recorded polls")
				}
				return
			}
			for i := range pending {
				poll, err := c.dataMigrationPoll(t.Context(), id, kind, phase, operation)
				if err != nil || poll.Done != (i == len(pending)-1) {
					t.Fatal("native operation completion changed", i, poll, err)
				}
				operation = poll.Data
				responses++
			}
			if next != len(pending) {
				t.Fatal("native poll skipped")
			}
		})
	}
	if accepted != 13 || responses != 36 {
		t.Fatal("native asynchronous coverage changed", accepted, responses)
	}
}

func TestDataMigrationOperationBoundaries(t *testing.T) {
	for _, mode := range []string{"native-success", "pending", "foreign-host", "foreign-subscription", "wrong-provider", "wrong-phase", "wrong-kind", "wrong-guid", "wrong-region", "wrong-version", "extra-query", "duplicate-query", "encoded-path", "fragment", "userinfo", "relative", "header-disagree", "header-region", "duplicate-header", "empty-header", "unsupported-header", "missing-header", "status-no-state", "status-empty", "status-null", "status-wrong-name", "status-wrong-id", "status-wrong-resource", "status-error", "status-failed", "status-canceled", "status-unknown", "status-type", "status-204", "status-body-202", "status-forbidden", "status-expired", "status-redirect", "status-rotation", "saved-extra", "saved-phase", "saved-type", "retry-after"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.ToLower(resourceID("Microsoft.Sql/servers", "server")) + "/providers/microsoft.datamigration/databasemigrations/database"
			statusURL := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.DataMigration/locations/westus2/operationTypes/cancelsqldbmigration/operationResults/858ba109-5ab7-4fa1-8aea-bea487cacdcd", dataMigrationVersion)
			header := http.Header{}
			header.Set("Azure-AsyncOperation", statusURL)
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				body := map[string]any{"name": "858ba109-5ab7-4fa1-8aea-bea487cacdcd", "status": "Succeeded"}
				status, h := 200, http.Header{}
				switch mode {
				case "pending":
					body["status"] = "InProgress"
				case "status-no-state":
					delete(body, "status")
				case "status-empty":
					res := jsonResponse(200, nil, nil)
					res.Body = http.NoBody
					return res, nil
				case "status-null":
					return jsonResponse(200, nil, nil), nil
				case "status-wrong-name":
					body["name"] = "other"
				case "status-wrong-id":
					body["id"] = id
				case "status-wrong-resource":
					body["resourceId"] = id + "wrong"
				case "status-error":
					body["error"] = map[string]any{"code": "Failed"}
				case "status-failed":
					body["status"] = "Failed"
				case "status-canceled":
					body["status"] = "Canceled"
				case "status-unknown":
					body["status"] = "Complete"
				case "status-type":
					body["status"] = []any{"Succeeded"}
				case "status-204":
					status, body = 204, nil
				case "status-body-202":
					status = 202
				case "status-forbidden":
					status, body = 403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}
				case "status-expired":
					status, body = 404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}
				case "status-redirect":
					status, body = 302, nil
					h.Set("Location", "https://example.invalid/operation")
				case "status-rotation":
					h.Set("Azure-AsyncOperation", strings.Replace(statusURL, "858ba109", "958ba109", 1))
				case "retry-after":
					h.Set("Retry-After", "31")
					body["status"] = "InProgress"
				}
				return jsonResponse(status, body, h), nil
			})
			kind, phase := dataMigrationType, "cancel"
			switch mode {
			case "foreign-host":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "management.azure.com", "example.invalid", 1))
			case "foreign-subscription":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, testSubscription, "11111111-1111-1111-1111-111111111111", 1))
			case "wrong-provider":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "Microsoft.DataMigration", "Microsoft.Sql", 1))
			case "wrong-phase":
				phase = "delete"
			case "wrong-kind":
				id = strings.Replace(id, "microsoft.sql/servers", "microsoft.sql/managedinstances", 1)
			case "wrong-guid":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "858ba109-5ab7-4fa1-8aea-bea487cacdcd", "other", 1))
			case "wrong-region":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "westus2", "bad_region", 1))
			case "wrong-version":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, dataMigrationVersion, "2021-06-30", 1))
			case "extra-query":
				header.Set("Azure-AsyncOperation", statusURL+"&operation=other")
			case "duplicate-query":
				header.Set("Azure-AsyncOperation", statusURL+"&api-version="+dataMigrationVersion)
			case "encoded-path":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "operationResults", "%6fperationResults", 1))
			case "fragment":
				header.Set("Azure-AsyncOperation", statusURL+"#result")
			case "userinfo":
				header.Set("Azure-AsyncOperation", strings.Replace(statusURL, "https://", "https://user@", 1))
			case "relative":
				header.Set("Azure-AsyncOperation", strings.TrimPrefix(statusURL, "https://management.azure.com"))
			case "header-disagree":
				header.Set("Location", strings.Replace(statusURL, "858ba109", "958ba109", 1))
			case "header-region":
				header.Set("Location", strings.Replace(statusURL, "westus2", "eastus", 1))
			case "duplicate-header":
				header.Add("Azure-AsyncOperation", statusURL)
			case "empty-header":
				header.Set("Location", "")
			case "unsupported-header":
				header.Set("Operation-Location", statusURL)
			case "missing-header":
				header.Del("Azure-AsyncOperation")
			}
			operation, err := c.dataMigrationReceipt(id, kind, phase, response{status: 202, header: header})
			if err == nil {
				switch mode {
				case "saved-extra":
					operation["extra"] = true
				case "saved-phase":
					operation["status_done"] = true
				case "saved-type":
					operation["status_url"] = 42
				}
				poll, pollErr := c.dataMigrationPoll(t.Context(), id, kind, phase, operation)
				err = pollErr
				if mode == "native-success" || mode == "pending" || mode == "retry-after" {
					if err != nil || poll.Done != (mode == "native-success") || calls != 1 || mode == "retry-after" && poll.RetryAfter != 31*time.Second {
						t.Fatal("valid native poll changed", poll, err, calls)
					}
					return
				}
			}
			if err == nil {
				t.Fatal("unverified operation accepted", mode)
			}
			if !strings.HasPrefix(mode, "status-") && calls != 0 {
				t.Fatal("invalid operation reached transport", mode, calls)
			}
		})
	}
}

func TestDataMigrationLocationOnlyAndSignedClassicOperations(t *testing.T) {
	for _, mode := range []string{"mongo-pending", "mongo-empty-200", "mongo-empty-204", "mongo-state", "mongo-null", "mongo-error", "mongo-404", "classic-signed", "classic-old-signed", "classic-old-unsigned", "classic-incomplete-signature", "classic-empty-signature", "classic-invalid-signature", "classic-signed-extra", "location-status-path"} {
		t.Run(mode, func(t *testing.T) {
			id, kind := strings.ToLower(resourceID(dataMigrationMongoServiceType, "service")), dataMigrationMongoServiceType
			endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.DataMigration/locations/westus2/migrationServiceOperationResults/858ba109-5ab7-4fa1-8aea-bea487cacdcd", dataMigrationVersion)
			if strings.HasPrefix(mode, "classic-") {
				id, kind = strings.ToLower(resourceID(dataMigrationServiceType, "service")), dataMigrationServiceType
				endpoint = strings.Replace(endpoint, "migrationServiceOperationResults", "operationResults", 1)
				if strings.HasPrefix(mode, "classic-old-") {
					endpoint = strings.Replace(endpoint, dataMigrationVersion, "2021-06-30", 1)
				}
				if mode != "classic-old-unsigned" {
					endpoint += "&t=replay-t&c=replay-c&s=replay-s&h=replay-h"
				}
				if mode == "classic-incomplete-signature" {
					endpoint = strings.TrimSuffix(endpoint, "&h=replay-h")
				}
				if mode == "classic-empty-signature" {
					endpoint = strings.TrimSuffix(endpoint, "replay-h")
				}
				if mode == "classic-invalid-signature" {
					endpoint += "%20"
				}
				if mode == "classic-signed-extra" {
					endpoint += "&other=true"
				}
			}
			if mode == "location-status-path" {
				endpoint = strings.Replace(endpoint, "migrationServiceOperationResults", "operationStatuses", 1)
			}
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != endpoint {
					t.Fatal("signed native URL rewritten")
				}
				status, body := 200, map[string]any(nil)
				switch mode {
				case "mongo-pending":
					status = 202
				case "mongo-empty-204":
					status = 204
				case "mongo-state":
					body = map[string]any{"status": "Succeeded"}
				case "mongo-null":
					return jsonResponse(200, nil, nil), nil
				case "mongo-error":
					body = map[string]any{"error": map[string]any{"code": "Failed"}}
				case "mongo-404":
					status, body = 404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}
				}
				res := jsonResponse(status, body, nil)
				if body == nil {
					res.Body = http.NoBody
				}
				return res, nil
			})
			header := http.Header{}
			header.Set("Location", endpoint)
			operation, err := c.dataMigrationReceipt(id, kind, "delete", response{status: 202, header: header})
			valid := slices.Contains([]string{"mongo-pending", "mongo-empty-200", "mongo-empty-204", "mongo-state", "classic-signed", "classic-old-signed"}, mode)
			if err == nil {
				poll, pollErr := c.dataMigrationPoll(t.Context(), id, kind, "delete", operation)
				err = pollErr
				if valid && (err != nil || poll.Done != (mode != "mongo-pending") || calls != 1) {
					t.Fatal("native Location result changed", poll, err)
				}
			}
			if valid != (err == nil) {
				t.Fatal("native Location boundary changed", mode, err)
			}
		})
	}
}
