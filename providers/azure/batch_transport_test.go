package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestBatchOAuthAudienceAndUnownedEndpointBoundary(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "unowned"}[owned], func(t *testing.T) {
			r, err := NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil }))
			if err != nil {
				t.Fatal(err)
			}
			account := batchExampleBody(t, "BatchAccountGet")
			accountID := "/subscriptions/" + testSubscription + "/resourcegroups/batch-rg/providers/microsoft.batch/batchaccounts/sampleacct"
			account["id"] = accountID
			origin := "https://sampleacct.japaneast.batch.azure.com"
			scopes := map[string]int{}
			dataCalls := 0
			r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "login.microsoftonline.com" {
					if err := req.ParseForm(); err != nil || req.Method != "POST" || req.URL.Path != "/"+testTenant+"/oauth2/v2.0/token" || req.Form.Get("client_id") != testApplication || req.Form.Get("client_secret") != "explicit-secret" || req.Form.Get("grant_type") != "client_credentials" {
						t.Fatal("Batch used an unexpected credential flow")
					}
					scope := req.Form.Get("scope")
					if scope != armOrigin+"/.default" && scope != "https://batch.core.windows.net//.default" {
						t.Fatal("unexpected OAuth scope", scope)
					}
					scopes[scope]++
					return jsonResponse(200, map[string]any{"access_token": scope, "token_type": "Bearer", "expires_in": 3600}, nil), nil
				}
				if req.URL.Host == "management.azure.com" {
					if req.Header.Get("Authorization") != "Bearer "+armOrigin+"/.default" {
						t.Fatal("Batch audience token reached ARM")
					}
					if req.URL.Path == "/subscriptions/"+testSubscription {
						return jsonResponse(200, map[string]any{"subscriptionId": testSubscription, "tenantId": testTenant, "state": "Enabled"}, nil), nil
					}
					if strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/Microsoft.Batch/batchAccounts") {
						return jsonResponse(200, map[string]any{"value": []any{account}}, nil), nil
					}
					if strings.EqualFold(req.URL.Path, accountID) {
						return jsonResponse(200, account, nil), nil
					}
				}
				if req.URL.Scheme+"://"+req.URL.Host == origin && req.URL.Path == "/jobs" {
					dataCalls++
					if req.Header.Get("Authorization") != "Bearer https://batch.core.windows.net//.default" || req.Header.Get("Ocp-Date") == "" {
						t.Fatal("ARM token reached Batch or native date was missing")
					}
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatal("unexpected Batch endpoint", req.URL.Host, req.URL.Path)
				return nil, nil
			})
			endpoint := origin
			if !owned {
				endpoint = "https://foreign.japaneast.batch.azure.com"
			}
			_, err = r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: batchDataPrefix + "Jobs_ListJobs", Parameters: map[string]any{"endpoint": endpoint}})
			if owned {
				if err != nil || dataCalls != 1 || scopes[armOrigin+"/.default"] != 1 || scopes["https://batch.core.windows.net//.default"] != 1 {
					t.Fatal("Batch OAuth flow", scopes, dataCalls, err)
				}
			} else if err == nil || dataCalls != 0 || scopes["https://batch.core.windows.net//.default"] != 0 {
				t.Fatal("unowned endpoint obtained or used Batch OAuth", scopes, dataCalls, err)
			}
		})
	}
}
