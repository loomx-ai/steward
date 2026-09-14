package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Account access is denied by default; every project endpoint remains explicit.
func projectBudgetScenario(t *testing.T) *billingBudgetScenario {
	t.Helper()
	s := &billingBudgetScenario{account: map[string]any{"name": "projects/sample-project/billingInfo", "projectId": "sample-project", "billingAccountName": testBillingAccount, "billingEnabled": false}, budget: billingBudgetFixture(), calls: map[string]int{}}
	object(s.budget["budgetFilter"])["projects"] = []any{"projects/123456"}
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		if req.Method != "GET" || len(body) != 0 {
			t.Fatal("project inventory wrote", req.Method, req.URL)
		}
		kind := ""
		switch req.URL.Host + req.URL.Path {
		case "cloudbilling.googleapis.com/v1/projects/sample-project/billingInfo":
			kind = "project"
		case "cloudbilling.googleapis.com/v1/billingAccounts":
			kind = "accounts"
		case "billingbudgets.googleapis.com/v1/" + testBillingAccount + "/budgets":
			kind = "budgets"
		case "billingbudgets.googleapis.com/v1/" + testBillingBudget:
			kind = "budget"
		default:
			t.Fatal("unexpected project-only billing request", req.URL)
		}
		s.calls[kind]++
		if kind == "accounts" {
			if req.URL.RawQuery != "pageSize=100" {
				t.Fatal(req.URL)
			}
			if s.mode == "empty-accounts" {
				return apiResponse(req, 200, `{}`), nil
			}
			return apiResponse(req, 403, `{"error":{"code":403}}`), nil
		}
		if kind == "budgets" {
			if req.URL.Query().Get("scope") != "projects/sample-project" || req.URL.Query().Get("pageSize") != "100" {
				t.Fatal("missing project list scope", req.URL)
			}
			for k := range req.URL.Query() {
				if k != "scope" && k != "pageSize" && k != "pageToken" {
					t.Fatal(req.URL)
				}
			}
		} else if req.URL.RawQuery != "" {
			t.Fatal(req.URL)
		}
		if s.mode == kind+"-denied" || kind == "project" && (s.mode == "account-denied" || s.mode == "known-account-denied") || kind == "budget" && s.mode == "known-denied" || kind == "project" && s.mode == "late-project-denied" && s.calls[kind] >= 4 {
			return apiResponse(req, 403, `{"error":{"code":403}}`), nil
		}
		if s.mode == kind+"-missing" || kind == "project" && (s.mode == "account-missing" || s.mode == "known-account-missing") || kind == "budget" && s.mode == "known-gone" {
			return apiResponse(req, 404, `{}`), nil
		}
		data := map[string]any{}
		switch kind {
		case "project":
			for k, v := range s.account {
				data[k] = v
			}
			if s.mode == "project-drift" && s.calls[kind] > 1 {
				data["billingEnabled"] = true
			}
		case "budget":
			for k, v := range s.budget {
				data[k] = v
			}
			if s.mode == "budget-drift" && s.calls[kind] > 1 {
				data["etag"] = "changed"
			}
		case "budgets":
			data["budgets"] = []any{s.budget}
			if strings.HasPrefix(s.mode, "known-") || s.mode == "empty" {
				data["budgets"] = []any{}
			}
			if s.mode == "paged" && req.URL.Query().Get("pageToken") == "" {
				data["budgets"] = []any{}
				data["nextPageToken"] = "next"
			}
			if s.mode == "loop" {
				data["nextPageToken"] = "loop"
			}
			if s.mode == "duplicate" {
				data["budgets"] = []any{s.budget, s.budget}
			}
			if s.mode == "list-drift" && s.calls[kind] > 1 {
				data["budgets"] = []any{}
			}
			if s.mode == "partial" {
				data["unreachable"] = []any{"unknown"}
			}
		}
		b, _ := json.Marshal(data)
		return apiResponse(req, 200, string(b)), nil
	})
	return s
}

func TestBillingBudgetProjectDiscoveryAndSavedIdentity(t *testing.T) {
	for _, mode := range []string{"normal", "empty-accounts", "empty", "paged", "project-denied", "project-missing", "budgets-denied", "budget-missing", "project-drift", "budget-drift", "late-project-denied", "loop", "duplicate", "list-drift", "partial", "foreign-project", "foreign-account", "multiple-projects", "missing-projects", "malformed-projects", "known-live", "known-gone", "known-denied", "known-account-denied", "known-account-missing", "known-relinked", "known-wrong-project"} {
		t.Run(mode, func(t *testing.T) {
			s := projectBudgetScenario(t)
			request := billingInventoryRequest(s.r)
			if strings.HasPrefix(mode, "known-") {
				batch, err := s.r.List(t.Context(), request)
				if err != nil || len(batch.Items) != 1 {
					t.Fatal(batch, err)
				}
				request.KnownNativeIDs = []string{testBillingBudgetID}
				request.KnownNativeMetadata = map[string]map[string]any{testBillingBudgetID: batch.Items[0].Normalized}
			}
			s.mode = mode
			s.calls = map[string]int{}
			switch mode {
			case "foreign-project":
				s.account["projectId"] = "other-project"
			case "foreign-account":
				s.account["billingAccountName"] = "billingAccounts/bad"
			case "multiple-projects":
				object(s.budget["budgetFilter"])["projects"] = []any{"projects/123456", "projects/222222"}
			case "missing-projects":
				delete(s.budget, "budgetFilter")
			case "malformed-projects":
				object(s.budget["budgetFilter"])["projects"] = nil
			case "known-relinked":
				s.account["billingAccountName"] = ""
				s.account["billingEnabled"] = false
			case "known-wrong-project":
				request.KnownNativeMetadata[testBillingBudgetID][billingBudgetProject] = "projects/foreign-project"
			}
			batch, err := s.r.List(t.Context(), request)
			good := mode == "normal" || mode == "empty-accounts" || mode == "empty" || mode == "paged" || mode == "known-live" || mode == "known-gone"
			if (err == nil) != good {
				t.Fatal(batch, err)
			}
			if !good {
				if batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("failed scope emitted inventory", batch)
				}
				return
			}
			if !batch.Complete {
				t.Fatal(batch)
			}
			if mode == "known-gone" {
				if len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != testBillingBudgetID || len(batch.Items) != 0 {
					t.Fatal(batch)
				}
				return
			}
			if mode == "empty" {
				if len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal(batch)
				}
				return
			}
			if len(batch.Items) != 1 {
				t.Fatal(batch)
			}
			item := batch.Items[0]
			if item.Normalized[billingBudgetProjectReview] != firewallDigest(s.account) || item.Normalized[billingBudgetProject] != "projects/sample-project" || item.Normalized[billingBudgetAccountReview] != nil || item.Normalized[billingBudgetReview] != firewallDigest(s.budget) {
				t.Fatal(item)
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "PRIVATE_FILTER") || strings.Contains(string(encoded), "PRIVATE_DELIVERY") {
				t.Fatal("private data escaped")
			}
		})
	}
}

func TestBillingBudgetProjectDeleteReviewAndRestart(t *testing.T) {
	for _, mode := range []string{"normal", "account-denied", "account-missing", "relinked", "project-drift", "foreign-project", "missing-proof", "mixed-proof", "broader-filter", "gone-account-late"} {
		t.Run(mode, func(t *testing.T) {
			s, r, request, state, deletes := budgetDeleteScenario(t, true)
			switch mode {
			case "account-denied", "account-missing":
				s.mode = mode
			case "relinked":
				s.account["billingAccountName"] = "billingAccounts/ABCDEF-ABCDEF-ABCDEF"
			case "project-drift":
				s.account["billingEnabled"] = true
			case "foreign-project":
				request.Asset.Normalized[billingBudgetProject] = "projects/other-project"
			case "missing-proof":
				delete(request.Asset.Normalized, billingBudgetProjectReview)
			case "mixed-proof":
				request.Asset.Normalized[billingBudgetAccountReview] = firewallDigest(s.account)
			case "broader-filter":
				delete(s.budget, "budgetFilter")
				request.Asset.Normalized[billingBudgetReview] = firewallDigest(s.budget)
			case "gone-account-late":
				*state = mode
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), *request)
			if mode != "normal" {
				if err == nil || *deletes != 0 {
					t.Fatal("changed project review wrote", result, err, *deletes)
				}
				return
			}
			if err != nil || *deletes != 1 {
				t.Fatal(result, err, *deletes)
			}
			b, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if err := json.Unmarshal(b, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), *request, restored)
			if err != nil || wait.Done {
				t.Fatal(wait, err)
			}
			*state = "gone"
			wait, err = driver.Wait(t.Context(), *request, restored)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			proof, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, restored)
			if err != nil || !proof.Settled {
				t.Fatal(proof, err)
			}
			proof, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, contracts.ActionResult{})
			if err == nil && proof.Settled {
				t.Fatal("lost response settled")
			}
			request.Asset.Normalized[billingBudgetProjectReview] = strings.Repeat("a", 64)
			if _, err := driver.Wait(t.Context(), *request, restored); err == nil {
				t.Fatal("changed receipt accepted")
			}
		})
	}
}

func TestBillingBudgetProjectAndAccountSnapshotMerge(t *testing.T) {
	for _, mode := range []string{"matching", "account-denied", "account-drift", "account-page-denied"} {
		t.Run(mode, func(t *testing.T) {
			project := projectBudgetScenario(t)
			account := budgetScenario(t, nil)
			account.budget = project.budget
			if mode == "account-denied" {
				account.mode = "account-denied"
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/billingInfo") || req.URL.Query().Get("scope") != "" {
					return project.r.transport.RoundTrip(req)
				}
				if mode == "account-page-denied" && req.URL.Host == "cloudbilling.googleapis.com" && req.URL.Path == "/v1/billingAccounts" {
					if req.URL.Query().Get("pageToken") == "" {
						return apiResponse(req, 200, `{"nextPageToken":"next"}`), nil
					}
					return apiResponse(req, 403, `{"error":{"code":403}}`), nil
				}
				if mode == "account-drift" && req.URL.Path == "/v1/billingAccounts" {
					account.budget["etag"] = "changed-between-scopes"
				}
				return account.r.transport.RoundTrip(req)
			})
			batch, err := r.List(t.Context(), billingInventoryRequest(r))
			if mode != "matching" {
				if err == nil || batch.Complete || len(batch.Items) != 0 {
					t.Fatal("failed visible account fell back", batch, err)
				}
				return
			}
			if err != nil || len(batch.Items) != 1 || batch.Items[0].Normalized[billingBudgetAccountReview] == nil || batch.Items[0].Normalized[billingBudgetProjectReview] != nil {
				t.Fatal(batch, err)
			}
		})
	}
}
