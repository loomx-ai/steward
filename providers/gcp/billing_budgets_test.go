package gcp

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const testBillingAccount = "billingAccounts/012345-678901-ABCDEF"
const testBillingBudget = testBillingAccount + "/budgets/budget-1"

func billingAccountFixture() map[string]any {
	return map[string]any{"name": testBillingAccount, "displayName": "PRIVATE_ACCOUNT", "open": false, "parent": "organizations/123456", "currencyCode": "USD"}
}

func billingBudgetFixture() map[string]any {
	return map[string]any{"name": testBillingBudget, "displayName": "PRIVATE_BUDGET", "etag": "review-1", "budgetFilter": map[string]any{"projects": []any{"projects/222222"}, "calendarPeriod": "MONTH"}, "amount": map[string]any{"specifiedAmount": map[string]any{"currencyCode": "USD", "units": "100"}}, "thresholdRules": []any{map[string]any{"thresholdPercent": 0.9}}, "notificationsRule": map[string]any{"monitoringNotificationChannels": []any{"projects/123456/notificationChannels/9876"}, "disableDefaultIamRecipients": true}}
}

// Explicit protocol fixture, never represented as an independent native backend.
func emptyBillingAccountsFixture(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	if req.Method != "GET" || req.URL.String() != "https://cloudbilling.googleapis.com/v1/billingAccounts?pageSize=100" {
		t.Fatal("unexpected billing fixture request", req.Method, req.URL)
	}
	return apiResponse(req, 200, `{}`)
}

func assertBudgetCoverage(t *testing.T, refs []graph.UnresolvedReference, id asset.AssetID) []graph.UnresolvedReference {
	t.Helper()
	remaining := []graph.UnresolvedReference{}
	count := 0
	for _, ref := range refs {
		if ref.Evidence["reason"] == "notification_channel_budget_scope_unverified" {
			count++
			if !ref.BlocksCleanup || ref.ControllerID != id || ref.NativeType != notificationChannelType || !strings.Contains(ref.NativeID, "/notificationChannels/") {
				t.Fatal("invalid budget coverage barrier", ref)
			}
		} else {
			remaining = append(remaining, ref)
		}
	}
	if count != 1 {
		t.Fatal("missing or duplicate budget coverage barrier", refs)
	}
	return remaining
}

type billingBudgetScenario struct {
	r               *Runtime
	mode            string
	account, budget map[string]any
	calls           map[string]int
}

func budgetScenario(t *testing.T, fallback roundTripFunc) *billingBudgetScenario {
	t.Helper()
	s := &billingBudgetScenario{account: billingAccountFixture(), budget: billingBudgetFixture(), calls: map[string]int{}}
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "cloudbilling.googleapis.com" && req.URL.Host != "billingbudgets.googleapis.com" {
			if fallback == nil {
				t.Fatal("unexpected non-billing request", req.URL)
			}
			return fallback(req)
		}
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		if req.Method != "GET" || len(body) != 0 {
			t.Fatal("billing discovery wrote", req.Method, req.URL)
		}
		kind := ""
		switch req.URL.Host + req.URL.Path {
		case "cloudbilling.googleapis.com/v1/billingAccounts":
			kind = "accounts"
		case "cloudbilling.googleapis.com/v1/" + testBillingAccount:
			kind = "account"
		case "billingbudgets.googleapis.com/v1/" + testBillingAccount + "/budgets":
			kind = "budgets"
		case "billingbudgets.googleapis.com/v1/" + testBillingBudget:
			kind = "budget"
		default:
			t.Fatal("unexpected native billing endpoint", req.URL)
		}
		s.calls[kind]++
		if strings.HasSuffix(kind, "s") {
			if req.URL.Query().Get("pageSize") != "100" {
				t.Fatal("missing page size")
			}
			for key := range req.URL.Query() {
				if key != "pageSize" && key != "pageToken" {
					t.Fatal("scope/filter hid consumers", key)
				}
			}
		} else if req.URL.RawQuery != "" {
			t.Fatal("unexpected GET query", req.URL)
		}
		if s.mode == kind+"-denied" {
			return apiResponse(req, 403, `{"error":{"code":403}}`), nil
		}
		if s.mode == kind+"-missing" {
			return apiResponse(req, 404, `{"error":{"code":404}}`), nil
		}
		data := map[string]any{}
		collection := ""
		switch kind {
		case "accounts":
			collection = "billingAccounts"
			data[collection] = []any{s.account}
		case "budgets":
			collection = "budgets"
			data[collection] = []any{s.budget}
		case "account":
			data = maps.Clone(s.account)
		case "budget":
			data = maps.Clone(s.budget)
		}
		if collection != "" {
			if s.mode == "empty" || (s.mode == kind+"-changed" && s.calls[kind] > 1) {
				data[collection] = []any{}
			}
			switch s.mode {
			case kind + "-null":
				data[collection] = nil
			case kind + "-element":
				data[collection] = []any{nil}
			case kind + "-duplicate":
				data[collection] = append(array(data[collection]), array(data[collection])...)
			case kind + "-token-null":
				data["nextPageToken"] = nil
			case kind + "-token-loop":
				data["nextPageToken"] = "loop"
			case "paged":
				if req.URL.Query().Get("pageToken") == "" {
					data[collection] = []any{}
					data["nextPageToken"] = "next"
				} else if req.URL.Query().Get("pageToken") != "next" {
					t.Fatal("wrong page token")
				}
			}
		}
		if s.mode == kind+"-partial" {
			data["unreachable"] = []any{"hidden"}
		}
		if s.mode == kind+"-drift" || (s.mode == kind+"-late" && s.calls[kind] > 1) {
			data["displayName"] = "changed"
		}
		if s.mode == kind+"-identity" {
			data["name"] = "foreign"
		}
		b, _ := json.Marshal(data)
		return apiResponse(req, 200, string(b)), nil
	})
	return s
}

func TestBillingBudgetSnapshot(t *testing.T) {
	modes := []string{"present", "empty", "paged"}
	for _, kind := range []string{"accounts", "account", "budgets", "budget"} {
		for _, suffix := range []string{"denied", "missing", "partial"} {
			modes = append(modes, kind+"-"+suffix)
		}
		if strings.HasSuffix(kind, "s") {
			for _, suffix := range []string{"null", "element", "duplicate", "token-null", "token-loop", "changed"} {
				modes = append(modes, kind+"-"+suffix)
			}
		} else {
			modes = append(modes, kind+"-drift", kind+"-identity")
		}
	}
	modes = append(modes, "account-late")
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			s := budgetScenario(t, nil)
			s.mode = mode
			c, err := s.r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			refs, err := c.visibleBillingBudgets(t.Context())
			good := mode == "present" || mode == "empty" || mode == "paged"
			if (err == nil) != good {
				t.Fatal(mode, refs, err)
			}
			if !good {
				if strings.HasSuffix(mode, "-missing") {
					var call *contracts.ProviderCallError
					if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation {
						t.Fatal("related 404 treated as target absence", err)
					}
				}
				if len(refs) != 0 {
					t.Fatal("partial reference set returned")
				}
				return
			}
			if mode == "empty" {
				if len(refs) != 0 || s.calls["accounts"] != 2 {
					t.Fatal(refs, s.calls)
				}
				return
			}
			channels, err := c.billingBudgetData(testBillingAccount, refs["//billingbudgets.googleapis.com/"+testBillingBudget])
			if err != nil || len(refs) != 1 || len(channels) != 1 || channels[0] != notificationChannelID {
				t.Fatal(refs)
			}
			wantLists := 2
			if mode == "paged" {
				wantLists = 4
			}
			if s.calls["accounts"] != wantLists || s.calls["budgets"] != wantLists || s.calls["account"] != 2 || s.calls["budget"] != 1 {
				t.Fatal(s.calls)
			}
		})
	}
}

func TestBillingBudgetReferenceShape(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, mode := range []string{"account-name", "account-parent", "account-open", "budget-parent", "budget-name", "budget-object", "budget-thresholds", "rule-null", "channels-null", "channel-invalid", "rule-bool"} {
		t.Run(mode, func(t *testing.T) {
			account, budget := billingAccountFixture(), billingBudgetFixture()
			switch mode {
			case "account-name":
				account["name"] = "billingAccounts/../hidden"
			case "account-parent":
				account["parent"] = "projects/123456"
			case "account-open":
				account["open"] = "false"
			case "budget-parent":
				budget["name"] = "billingAccounts/ABCDEF-678901-ABCDEF/budgets/budget-1"
			case "budget-name":
				budget["name"] = testBillingBudget + "/extra"
			case "budget-object":
				budget["budgetFilter"] = nil
			case "budget-thresholds":
				budget["thresholdRules"] = []any{nil}
			case "rule-null":
				budget["notificationsRule"] = nil
			case "channels-null":
				object(budget["notificationsRule"])["monitoringNotificationChannels"] = nil
			case "channel-invalid":
				object(budget["notificationsRule"])["monitoringNotificationChannels"] = []any{"projects/sample-project/notificationChannels/../extra"}
			case "rule-bool":
				object(budget["notificationsRule"])["disableDefaultIamRecipients"] = "false"
			}
			if strings.HasPrefix(mode, "account-") {
				if billingAccountData(account) == nil {
					t.Fatal("malformed account accepted")
				}
				return
			}
			if _, err := c.billingBudgetData(testBillingAccount, budget); err == nil {
				t.Fatal("malformed budget accepted")
			}
		})
	}
}

func TestBillingBudgetAllAccountsAndPages(t *testing.T) {
	accounts := []map[string]any{billingAccountFixture(), billingAccountFixture()}
	accounts[1]["name"], accounts[1]["open"] = "billingAccounts/ABCDEF-012345-678901", true
	objects := map[string]map[string]any{}
	budgets := map[string][]map[string]any{}
	for _, account := range accounts {
		name := text(account["name"])
		objects[name] = account
		for _, suffix := range []string{"first", "second"} {
			budget := billingBudgetFixture()
			budget["name"] = name + "/budgets/" + suffix
			objects[text(budget["name"])] = budget
			budgets[name] = append(budgets[name], budget)
		}
	}
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" {
			t.Fatal("billing mutation")
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		var data map[string]any
		page := func(collection string, rows []map[string]any) map[string]any {
			if req.URL.Query().Get("pageSize") != "100" || len(req.URL.Query()) > 2 {
				t.Fatal("filtered list", req.URL)
			}
			switch req.URL.Query().Get("pageToken") {
			case "":
				return map[string]any{collection: []any{rows[0]}, "nextPageToken": "second"}
			case "second":
				return map[string]any{collection: []any{rows[1]}}
			default:
				t.Fatal("wrong page token")
				return nil
			}
		}
		switch {
		case req.URL.Host == "cloudbilling.googleapis.com" && name == "billingAccounts":
			data = page("billingAccounts", accounts)
		case req.URL.Host == "billingbudgets.googleapis.com" && strings.HasSuffix(name, "/budgets"):
			rows := budgets[strings.TrimSuffix(name, "/budgets")]
			if len(rows) != 2 {
				t.Fatal("wrong parent", req.URL)
			}
			data = page("budgets", rows)
		default:
			data = objects[name]
			if data == nil || req.URL.RawQuery != "" {
				t.Fatal("unexpected detail read", req.URL)
			}
		}
		b, _ := json.Marshal(data)
		return apiResponse(req, 200, string(b)), nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := c.visibleBillingBudgets(t.Context())
	if err != nil || len(refs) != 4 || calls != 20 {
		t.Fatal(refs, calls, err)
	}
	for id, budget := range refs {
		_, parent, err := billingBudgetName(id)
		if err != nil {
			t.Fatal(err)
		}
		channels, err := c.billingBudgetData(parent, budget)
		if err != nil || len(channels) != 1 || channels[0] != notificationChannelID {
			t.Fatal(channels)
		}
	}
}

func TestBillingBudgetChannelGraph(t *testing.T) {
	for _, mode := range []string{"present", "empty", "unrelated", "spoofed-type", "pubsub", "denied"} {
		t.Run(mode, func(t *testing.T) {
			delivery := "email"
			if mode == "pubsub" {
				delivery = "pubsub"
			}
			channel, _, _, _ := channelDependencyFixture(t, delivery)
			s := budgetScenario(t, channel.r.transport.RoundTrip)
			if mode == "empty" {
				s.mode = "empty"
			}
			if mode == "denied" {
				s.mode = "budget-denied"
			}
			if mode == "spoofed-type" {
				channel.request.Asset.Normalized["type"] = "pubsub"
			}
			if mode == "unrelated" {
				object(s.budget["notificationsRule"])["monitoringNotificationChannels"] = []any{"projects/foreign-project/notificationChannels/9876"}
			}
			h, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.Contribute(t.Context(), "global", []asset.Asset{channel.request.Asset, channel.policy})
			if mode == "denied" {
				if err == nil {
					t.Fatal("billing failure ignored")
				}
				return
			}
			if err != nil || len(result.Relationships) != 1 {
				t.Fatal(result, err)
			}
			if mode == "pubsub" {
				if len(s.calls) != 0 || len(result.Unresolved) != 0 {
					t.Fatal("non-email requires Billing permissions", s.calls, result)
				}
				return
			}
			refs := assertBudgetCoverage(t, result.Unresolved, channel.request.Asset.ID)
			want := 1
			if mode == "empty" || mode == "unrelated" {
				want = 0
			}
			if len(refs) != want {
				t.Fatal(refs)
			}
			if want == 1 && (!refs[0].BlocksCleanup || refs[0].NativeType != billingBudgetType || refs[0].NativeID != "//billingbudgets.googleapis.com/"+testBillingBudget || refs[0].Evidence["reason"] != "notification_channel_referenced_by_budget") {
				t.Fatal(refs)
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "PRIVATE_") || strings.Contains(string(b), "222222") {
				t.Fatal("private budget configuration persisted")
			}
		})
	}
}

func TestBillingBudgetFixturesMatchNativeSchemas(t *testing.T) {
	for _, test := range []struct {
		file, revision, hash, schema string
		value                        map[string]any
	}{
		{"cloudbilling", "20260904", "14862c5e884d2f899280acdba70c187055a0d30052c10091b0428a2e92e65b25", "BillingAccount", billingAccountFixture()},
		{"billingbudgets", "20260906", "43f6da9d356ad5d16fdcc1b1f10382f2c0b0a5e056a24719fcf25e1748ea03ce", "GoogleCloudBillingBudgetsV1Budget", billingBudgetFixture()},
	} {
		compiler := infraFixtureSchemas(t, "fixtures/billing-budget/"+test.file+"-schemas.json", test.revision, test.hash)
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + test.schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(test.value); err != nil {
			t.Fatal(err)
		}
	}
}
