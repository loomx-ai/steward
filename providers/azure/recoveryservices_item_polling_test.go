package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestRecoveryItemRecordedStatusAndJobRecovery(t *testing.T) {
	wire, err := os.ReadFile("fixtures/recoveryservices/hana-item-delete-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Request  struct{ Method, URI string }
		Response struct {
			Body    struct{ String string }
			Headers map[string][]string
			Status  struct{ Code int }
		}
	}
	if err = json.Unmarshal(wire, &rows); err != nil || len(rows) != 9 {
		t.Fatal("native recording", err)
	}
	owner, _ := url.Parse(rows[0].Request.URI)
	id := strings.ToLower(owner.Path)
	subscription := strings.Split(id, "/")[2]
	calls := 0
	transport := func(q *http.Request) (*http.Response, error) {
		calls++
		if calls >= len(rows) {
			t.Fatal("poll exceeded recording")
		}
		row := rows[calls]
		recorded, _ := url.Parse(row.Request.URI)
		if q.Method != row.Request.Method || !strings.EqualFold(q.URL.Path, recorded.Path) || last(q.URL.Path) != last(recorded.Path) || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || len(q.URL.Query()) != 1 {
			t.Fatal("native request scope changed")
		}
		h := http.Header{}
		for k, values := range row.Response.Headers {
			for _, v := range values {
				h.Add(k, v)
			}
		}
		return &http.Response{StatusCode: row.Response.Status.Code, Header: h, Body: io.NopCloser(strings.NewReader(row.Response.Body.String))}, nil
	}
	runtime := protocolRuntime(t, transport)
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = subscription
	headers := http.Header{}
	for k, values := range rows[0].Response.Headers {
		for _, v := range values {
			headers.Add(k, v)
		}
	}
	receipt, err := c.recoveryItemDeleteReceipt(id, response{status: rows[0].Response.Status.Code, header: headers}, rows[0].Response.Body.String == "")
	if err != nil {
		t.Fatal("native item DELETE receipt", err)
	}
	for calls < len(rows)-1 {
		receipt, _, err = c.recoveryItemPoll(t.Context(), id, receipt)
		if err != nil {
			t.Fatal("native status/job poll", calls, err)
		}
		if receipt["operation_done"] != (calls == len(rows)-1) {
			t.Fatal("status success bypassed native job", calls)
		}
		// Every poll crosses a serialization and runtime boundary, including the
		// Succeeded status that creates the job and the subsequent job read.
		wire, err = json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(wire, &receipt); err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = runtime.transport
		c, err = fresh.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		c.subscription = subscription
	}
	if _, _, err = c.recoveryItemPoll(t.Context(), id, receipt); err != nil || calls != 8 {
		t.Fatal("completed job repeated requests", err)
	}
}

func TestRecoveryItemSignedCallbacksAndReceipt(t *testing.T) {
	id, _ := recoveryServicesTestItem()
	token := "Opaque-Case=="
	endpoint := apiURL(recoveryServicesVaultID(id)+"/backupOperations/"+token, recoveryServicesBackupVersion) + "&t=1&c=2&s=3&h=4"
	result := strings.Replace(endpoint, "/backupOperations/", "/backupOperationResults/", 1)
	calls := 0
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		calls++
		if q.URL.String() != endpoint {
			t.Fatal("signed callback rewritten")
		}
		return jsonResponse(200, map[string]any{"id": token, "name": token, "status": "Succeeded"}, nil), nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Azure-AsyncOperation", endpoint)
	h.Set("Location", result)
	receipt, err := c.recoveryItemDeleteReceipt(id, response{status: 202, header: h}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"phase", "url", "jobs", "owner", "unknown-field"} {
		changed := batchClone(receipt)
		owner := id
		switch mode {
		case "phase":
			changed["operation_done"] = true
		case "url":
			changed["status_url"] = strings.Replace(endpoint, "h=4", "h=other", 1)
		case "jobs":
			changed["jobs"] = []any{"foreign-job"}
		case "owner":
			owner += "-other"
		case "unknown-field":
			changed["force"] = true
		}
		if _, _, err = c.recoveryItemPoll(t.Context(), owner, changed); err == nil || calls != 0 {
			t.Fatal("tampered receipt reached transport", mode)
		}
	}
	next, _, err := c.recoveryItemPoll(t.Context(), id, receipt)
	if err != nil || next["operation_done"] != true || calls != 1 {
		t.Fatal("signed native status not completed", err)
	}
	for _, mode := range []string{"foreign-vault", "other-token", "partial-signature", "status-container-route", "missing", "duplicate", "old-signed"} {
		headers := h.Clone()
		switch mode {
		case "foreign-vault":
			headers.Set("Location", strings.Replace(result, "/vaults/vault/", "/vaults/other/", 1))
		case "other-token":
			headers.Set("Location", strings.Replace(result, token, "other", 1))
		case "partial-signature":
			headers.Set("Location", strings.TrimSuffix(result, "&h=4"))
		case "status-container-route":
			headers.Set("Azure-AsyncOperation", apiURL(redisParentID(id)+"/operationsStatus/"+token, recoveryServicesBackupVersion))
		case "missing":
			headers = http.Header{}
		case "duplicate":
			headers.Add("Location", result)
		case "old-signed":
			headers.Set("Location", strings.Replace(result, recoveryServicesBackupVersion, "2025-02-01", 1))
		}
		if _, err = c.recoveryItemDeleteReceipt(id, response{status: 202, header: headers}, true); err == nil {
			t.Fatal("invalid capability accepted", mode)
		}
	}
}

func TestRecoveryItemResultOnlyPolling(t *testing.T) {
	for _, mode := range []string{"empty-200", "empty-204", "item-200", "pending-object", "null-200", "foreign-item", "error", "redirected"} {
		t.Run(mode, func(t *testing.T) {
			id, raw := recoveryServicesTestItem()
			endpoint := apiURL(id+"/operationResults/Result-AbC", recoveryServicesBackupVersion)
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.URL.String() != endpoint {
					t.Fatal("result scope changed")
				}
				code := 200
				body := ""
				h := http.Header{"Retry-After": []string{"60"}}
				switch mode {
				case "empty-204":
					code = 204
				case "item-200", "foreign-item":
					if mode == "foreign-item" {
						raw["id"] = id + "-other"
					}
					wire, _ := json.Marshal(raw)
					body = string(wire)
				case "pending-object":
					code = 202
					body = "{}"
				case "null-200":
					body = "null"
				case "error":
					code = 500
					body = `{"error":{"code":"InternalError"}}`
				case "redirected":
					code = 202
					h.Set("Location", strings.Replace(endpoint, "Result-AbC", "other", 1))
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{}
			h.Set("Location", endpoint)
			receipt, err := c.recoveryItemDeleteReceipt(id, response{status: 202, header: h}, true)
			if err != nil {
				t.Fatal(err)
			}
			next, delay, err := c.recoveryItemPoll(t.Context(), id, receipt)
			switch mode {
			case "empty-200", "empty-204", "item-200":
				if err != nil || next["operation_done"] != true {
					t.Fatal("native result not completed", err)
				}
				if _, _, err = c.recoveryItemPoll(t.Context(), id, next); err != nil || calls != 1 {
					t.Fatal("completed result repeated request", err)
				}
			case "pending-object":
				if err != nil || next["operation_done"] != false || delay.Seconds() != 60 {
					t.Fatal("pending result lost", err)
				}
			default:
				if err == nil {
					t.Fatal("ambiguous result accepted")
				}
			}
		})
	}
}

func TestRecoveryItemJobsResumeAfterTransientFailure(t *testing.T) {
	id, status, job := recoveryItemJobEvidence(t, "hana")
	token := text(status["name"])
	endpoint := apiURL(recoveryServicesVaultID(id)+"/backupOperations/"+token, recoveryServicesBackupVersion)
	statusCalls, jobCalls := 0, 0
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if q.URL.String() == endpoint {
			statusCalls++
			return jsonResponse(200, status, nil), nil
		}
		jobCalls++
		if jobCalls == 1 {
			return jsonResponse(503, map[string]any{"error": map[string]any{"code": "ServiceUnavailable"}}, nil), nil
		}
		return jsonResponse(200, job, nil), nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = strings.Split(id, "/")[2]
	h := http.Header{}
	h.Set("Azure-AsyncOperation", endpoint)
	receipt, err := c.recoveryItemDeleteReceipt(id, response{status: 202, header: h}, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err = c.recoveryItemPoll(t.Context(), id, receipt)
	if err != nil || receipt["status_done"] != true || receipt["operation_done"] != false {
		t.Fatal("job IDs were not checkpointed", err)
	}
	if _, _, err = c.recoveryItemPoll(t.Context(), id, receipt); err == nil {
		t.Fatal("failed read became completion")
	}
	wire, _ := json.Marshal(receipt)
	var resumed map[string]any
	if err = json.Unmarshal(wire, &resumed); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewRuntime(runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = runtime.transport
	c, err = fresh.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = strings.Split(id, "/")[2]
	result, _, err := c.recoveryItemPoll(t.Context(), id, resumed)
	if err != nil || result["operation_done"] != true || statusCalls != 1 || jobCalls != 2 {
		t.Fatal("job recovery lost persisted phase", err)
	}
}
