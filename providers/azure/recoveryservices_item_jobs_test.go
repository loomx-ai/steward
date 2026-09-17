package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func recoveryItemJobEvidence(t *testing.T, name string) (string, map[string]any, map[string]any) {
	t.Helper()
	wire, err := os.ReadFile("fixtures/recoveryservices/" + name + "-item-delete-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Request  struct{ Method, URI string }
		Response struct{ Body struct{ String string } }
	}
	if err = json.Unmarshal(wire, &rows); err != nil {
		t.Fatal(err)
	}
	id := ""
	var status, job map[string]any
	for _, row := range rows {
		u, err := url.Parse(row.Request.URI)
		if err != nil {
			t.Fatal(err)
		}
		if row.Request.Method == "DELETE" {
			id = strings.ToLower(u.Path)
		}
		var raw map[string]any
		if json.Unmarshal([]byte(row.Response.Body.String), &raw) != nil {
			continue
		}
		if raw["status"] == "Succeeded" {
			status = raw
		}
		if strings.Contains(strings.ToLower(u.Path), "/backupjobs/") {
			job = raw
		}
	}
	if id == "" || status == nil || job == nil {
		t.Fatal("incomplete native delete/job evidence")
	}
	return id, status, job
}

func TestRecoveryItemRecordedJobs(t *testing.T) {
	for _, name := range []string{"hana", "vm"} {
		t.Run(name, func(t *testing.T) {
			id, status, job := recoveryItemJobEvidence(t, name)
			ids, err := recoveryItemOperationJobs(status)
			if err != nil || len(ids) != 1 || ids[0] != job["name"] {
				t.Fatal("recorded job link", err)
			}
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" || q.URL.Scheme != "https" || q.URL.Host != "management.azure.com" || !strings.EqualFold(q.URL.Path, recoveryServicesVaultID(id)+"/backupJobs/"+ids[0]) || last(q.URL.Path) != ids[0] || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || len(q.URL.Query()) != 1 {
					t.Fatal("job request escaped native boundary")
				}
				return jsonResponse(200, job, nil), nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			c.subscription = strings.Split(id, "/")[2]
			done, err := c.recoveryItemJobs(t.Context(), id, ids)
			if err != nil || !done || calls != 1 {
				t.Fatal("recorded deletion job rejected", err)
			}
		})
	}
}

func TestRecoveryItemJobFailuresAndPending(t *testing.T) {
	for _, mode := range []string{"InProgress", "Cancelling", "CompletedWithWarnings", "CompletedWithWarnings-details", "Failed", "Cancelled", "unknown", "foreign-vault", "foreign-job", "wrong-operation", "error-details", "async", "not-found", "forbidden", "unavailable", "duplicate", "invalid-token"} {
		t.Run(mode, func(t *testing.T) {
			id, status, job := recoveryItemJobEvidence(t, "hana")
			ids, err := recoveryItemOperationJobs(status)
			if err != nil {
				t.Fatal(err)
			}
			p := object(job["properties"])
			code := 200
			headers := http.Header{}
			switch mode {
			case "InProgress", "Cancelling", "CompletedWithWarnings", "Failed", "Cancelled", "unknown":
				p["status"] = mode
			case "CompletedWithWarnings-details":
				p["status"] = "CompletedWithWarnings"
				p["errorDetails"] = []any{map[string]any{"code": "Warning"}}
			case "foreign-vault":
				job["id"] = strings.Replace(text(job["id"]), "/vaults/", "/vaults/other/", 1)
			case "foreign-job":
				job["name"] = "another"
			case "wrong-operation":
				p["operation"] = "Backup"
			case "error-details":
				p["errorDetails"] = []any{map[string]any{"code": "Failure"}}
			case "async":
				headers.Set("Location", "")
			case "not-found":
				code = 404
			case "forbidden":
				code = 403
			case "unavailable":
				code = 503
			case "duplicate":
				ids = append(ids, ids[0])
			case "invalid-token":
				ids[0] = "../another"
			}
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) { calls++; return jsonResponse(code, job, headers), nil })
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			c.subscription = strings.Split(id, "/")[2]
			done, err := c.recoveryItemJobs(t.Context(), id, ids)
			switch mode {
			case "InProgress", "Cancelling":
				if err != nil || done {
					t.Fatal("pending job completed", err)
				}
			case "CompletedWithWarnings", "CompletedWithWarnings-details":
				if err != nil || !done {
					t.Fatal("documented completion rejected", err)
				}
			default:
				if err == nil || done {
					t.Fatal("invalid job completed")
				}
			}
			if (mode == "duplicate" || mode == "invalid-token") && calls != 0 {
				t.Fatal("invalid job reached transport")
			}
		})
	}
}

func TestRecoveryItemMultipleOperationJobs(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "duplicate", "failed", "malformed-errors", "missing", "foreign-id", "unknown-type"} {
		t.Run(mode, func(t *testing.T) {
			p := map[string]any{"objectType": "OperationStatusJobsExtendedInfo", "jobIds": []any{"first", "second"}, "failedJobsError": map[string]any{}}
			switch mode {
			case "empty":
				p["jobIds"] = []any{}
			case "duplicate":
				p["jobIds"] = []any{"same", "same"}
			case "failed":
				p["failedJobsError"] = map[string]any{"first": "Failure"}
			case "malformed-errors":
				p["failedJobsError"] = []any{}
			case "missing":
				delete(p, "jobIds")
			case "foreign-id":
				p["jobIds"] = []any{"https://foreign.invalid/job"}
			case "unknown-type":
				p["objectType"] = "UnexpectedInfo"
			}
			ids, err := recoveryItemOperationJobs(map[string]any{"properties": p})
			if mode == "valid" {
				if err != nil || len(ids) != 2 {
					t.Fatal("multiple jobs lost", err)
				}
			} else if err == nil {
				t.Fatal("invalid job links accepted")
			}
		})
	}
}
