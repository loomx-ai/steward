package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRecoveryContainerCallbackBoundaries(t *testing.T) {
	item, _ := recoveryServicesTestItem()
	id := redisParentID(item)
	vault := recoveryServicesVaultID(id)
	c := &client{subscription: testSubscription}
	token := "Operation-AbC=="
	result := apiURL(id+"/operationResults/"+token, recoveryServicesBackupVersion)
	status := apiURL(vault+"/backupFabrics/azure/operationsStatus/"+token, recoveryServicesBackupVersion)
	legacy := "https://management.azure.com" + vault + "/backupOperationResults/" + token + "?fabricName=Azure"
	for _, endpoint := range []string{result, legacy, apiURL(vault+"/backupOperationResults/"+token, recoveryServicesBackupVersion), result + "&t=1&c=2&s=3&h=4", strings.Replace(result, ";", "%3B", -1)} {
		h := http.Header{}
		h.Set("Location", endpoint)
		h.Set("Azure-AsyncOperation", status)
		got, err := c.recoveryContainerPollHeaders(id, h)
		if err != nil {
			t.Fatal("native callback refused", err)
		}
		if endpoint == legacy && got != result {
			t.Fatal("legacy Location did not bind own container")
		}
		if endpoint != legacy && got != endpoint {
			t.Fatal("authenticated callback was rewritten")
		}
	}
	for _, mode := range []string{"host", "subscription", "vault", "container", "version", "filter", "partial-signature", "encoded-slash", "duplicate-header", "empty-header", "status-token", "status-scope", "legacy-fabric", "legacy-extra", "legacy-owner-route", "legacy-malformed-version", "legacy-double-query", "old-signed", "status-version", "userinfo", "fragment", "port"} {
		t.Run(mode, func(t *testing.T) {
			h := http.Header{}
			h.Set("Location", result)
			h.Set("Azure-AsyncOperation", status)
			switch mode {
			case "host":
				h.Set("Location", strings.Replace(result, "management.azure.com", "foreign.invalid", 1))
			case "subscription":
				h.Set("Location", strings.Replace(result, testSubscription, "00000000-0000-0000-0000-000000000000", 1))
			case "vault":
				h.Set("Location", strings.Replace(result, "/vaults/vault/", "/vaults/other/", 1))
			case "container":
				h.Set("Location", strings.Replace(result, "vmappcontainer;", "other;", 1))
			case "version":
				h.Set("Location", strings.Replace(result, recoveryServicesBackupVersion, "1999-01-01", 1))
			case "filter":
				h.Set("Location", result+"&$filter=anything")
			case "partial-signature":
				h.Set("Location", result+"&t=1")
			case "encoded-slash":
				h.Set("Location", strings.Replace(result, "/operationResults/", "%2foperationResults/", 1))
			case "duplicate-header":
				h.Add("Location", result)
			case "empty-header":
				h.Set("Azure-AsyncOperation", "")
			case "status-token":
				h.Set("Azure-AsyncOperation", strings.Replace(status, token, "operation-abc==", 1))
			case "status-scope":
				h.Set("Azure-AsyncOperation", strings.Replace(status, "/azure/", "/other/", 1))
			case "legacy-fabric":
				h.Set("Location", strings.Replace(legacy, "fabricName=Azure", "fabricName=Other", 1))
			case "legacy-extra":
				h.Set("Location", legacy+"&unknown=1")
			case "legacy-owner-route":
				h.Set("Location", strings.Replace(result, "api-version="+recoveryServicesBackupVersion, "fabricName=Azure", 1))
			case "legacy-malformed-version":
				h.Set("Location", legacy+"?api-version=1999-01-01")
			case "legacy-double-query":
				h.Set("Location", legacy+"?api-version=2023-04-01?other=1")
			case "old-signed":
				h.Set("Location", strings.Replace(result, recoveryServicesBackupVersion, "2023-04-01", 1)+"&t=1&c=2&s=3&h=4")
			case "status-version":
				h.Set("Azure-AsyncOperation", strings.Replace(status, recoveryServicesBackupVersion, "1999-01-01", 1))
			case "userinfo":
				h.Set("Location", strings.Replace(result, "https://", "https://user@", 1))
			case "fragment":
				h.Set("Location", result+"#fragment")
			case "port":
				h.Set("Location", strings.Replace(result, "management.azure.com", "management.azure.com:443", 1))
			}
			if _, err := c.recoveryContainerPollHeaders(id, h); err == nil {
				t.Fatal("changed callback accepted")
			}
		})
	}
}

func TestRecoveryContainerPollPersistence(t *testing.T) {
	for _, mode := range []string{"empty-200", "resource-200", "empty-204", "failed", "redirected", "pending-body", "pending-error-shape", "pending-null", "null-200", "object-200", "foreign-resource"} {
		t.Run(mode, func(t *testing.T) {
			item, _ := recoveryServicesTestItem()
			id := redisParentID(item)
			endpoint := apiURL(id+"/operationResults/AbC", recoveryServicesBackupVersion)
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" || q.URL.String() != endpoint {
					t.Fatal("poll changed native request")
				}
				status := 202
				body := ""
				headers := http.Header{}
				headers.Set("Retry-After", "60")
				if calls == 2 {
					status = 200
					switch mode {
					case "empty-204":
						status = 204
					case "resource-200", "foreign-resource":
						rid := id
						if mode == "foreign-resource" {
							rid += "-other"
						}
						wire, _ := json.Marshal(map[string]any{"id": rid, "name": last(rid), "type": recoveryServicesContainer, "properties": map[string]any{"containerType": "VMAppContainer"}})
						body = string(wire)
					case "failed":
						status = 500
						body = `{"error":{"code":"ServiceFailure"}}`
					case "redirected":
						status = 202
						headers.Set("Location", strings.Replace(endpoint, "/AbC?", "/Other?", 1))
					case "pending-body":
						status = 202
						body = `{}`
					case "pending-error-shape":
						status = 202
						body = `{"unexpected":"value"}`
					case "pending-null":
						status = 202
						body = `null`
					case "null-200":
						body = `null`
					case "object-200":
						body = `{}`
					}
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{}
			h.Set("Location", endpoint)
			saved, err := c.recoveryContainerDeleteReceipt(id, response{status: 202, header: h}, true)
			if err != nil {
				t.Fatal(err)
			}
			var delay time.Duration
			saved, delay, err = c.recoveryContainerPoll(t.Context(), id, saved)
			if err != nil || saved["operation_done"] != false || delay != time.Minute {
				t.Fatal("pending operation completed", err)
			}
			wire, _ := json.Marshal(saved)
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
			result, _, err := c.recoveryContainerPoll(t.Context(), id, resumed)
			if mode == "empty-200" || mode == "empty-204" || mode == "resource-200" {
				if err != nil || result["operation_done"] != true {
					t.Fatal("native completion not persisted", err)
				}
				if _, _, err = c.recoveryContainerPoll(t.Context(), id, result); err != nil || calls != 2 {
					t.Fatal("completed receipt repeated poll", err)
				}
			} else if mode == "pending-body" || mode == "pending-null" {
				if err != nil || result["operation_done"] != false {
					t.Fatal("empty pending response did not preserve receipt", err)
				}
			} else if err == nil {
				t.Fatal("failed/ambiguous result completed")
			}
			changed := batchClone(resumed)
			changed["operation_done"] = true
			if _, _, err = c.recoveryContainerPoll(t.Context(), id, changed); err == nil || calls != 2 {
				t.Fatal("tampered phase accepted")
			}
		})
	}
}
