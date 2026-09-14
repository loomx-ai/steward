package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func budgetDeleteScenario(t *testing.T) (*billingBudgetScenario, *Runtime, *contracts.ActionRequest, *string, *int) {
	t.Helper()
	s := budgetScenario(t, nil)
	mode, reads, deletes := "", 0, 0
	accountReads := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && req.URL.Host == "cloudbilling.googleapis.com" && req.URL.Path == "/v1/"+testBillingAccount {
			accountReads++
			if mode == "gone-account-late" && accountReads > 1 {
				return apiResponse(req, 403, `{}`), nil
			}
		}
		if req.URL.Host == "billingbudgets.googleapis.com" && req.URL.Path == "/v1/"+testBillingBudget {
			if req.Method == "GET" {
				reads++
				if mode == "gone" || mode == "gone-account-late" {
					return apiResponse(req, 404, `{}`), nil
				}
				if mode == "get-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "race" && reads > 1 {
					s.budget["etag"] = "late-change"
				}
			}
			if req.Method == "DELETE" {
				deletes++
				body := []byte{}
				if req.Body != nil {
					body, _ = io.ReadAll(req.Body)
				}
				if req.URL.RawQuery != "" || len(body) != 0 {
					t.Fatal("unexpected native delete parameters")
				}
				switch mode {
				case "delete-denied":
					return apiResponse(req, 403, `{}`), nil
				case "delete-404-gone":
					mode = "gone"
					return apiResponse(req, 404, `{}`), nil
				case "delete-missing":
					return apiResponse(req, 404, `{}`), nil
				case "delete-error":
					return apiResponse(req, 500, `{}`), nil
				case "delete-operation":
					return apiResponse(req, 200, `{"name":"operations/invalid"}`), nil
				case "lost-response":
					return nil, errors.New("lost delete response")
				}
				return apiResponse(req, 200, `{}`), nil
			}
		}
		return s.r.transport.RoundTrip(req)
	})
	batch, err := r.List(t.Context(), billingInventoryRequest(r))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	request := &contracts.ActionRequest{Asset: asset.Asset{ID: "budget", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: billingBudgetType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}, Action: "delete", IdempotencyKey: "budget-review"}
	reads, accountReads = 0, 0
	return s, r, request, &mode, &deletes
}

func TestBillingBudgetDeleteReviewRestartAndSettlement(t *testing.T) {
	for _, mode := range []string{"normal", "gone", "gone-account-late", "get-denied", "account-denied", "account-missing", "account-drift", "configuration", "private-configuration", "unknown-configuration", "missing-review", "missing-account-review", "account-selector", "identity", "parameters", "prerequisites", "impacts", "empty-key", "race", "delete-denied", "delete-missing", "delete-error", "delete-operation", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			s, r, request, state, deletes := budgetDeleteScenario(t)
			*state = mode
			switch mode {
			case "account-denied":
				s.mode = "account-denied"
			case "account-missing":
				s.mode = "account-missing"
			case "account-drift":
				s.account["parent"] = "organizations/999999"
			case "configuration":
				s.budget["amount"] = map[string]any{"lastPeriodAmount": map[string]any{}}
			case "private-configuration":
				object(s.budget["budgetFilter"])["projects"] = []any{"projects/999999"}
			case "unknown-configuration":
				s.budget["futureField"] = "new"
			case "missing-review":
				delete(request.Asset.Normalized, billingBudgetReview)
			case "missing-account-review":
				delete(request.Asset.Normalized, billingBudgetAccountReview)
			case "account-selector":
				request.Asset.Normalized["billing_account"] = "billingAccounts/ABCDEF-012345-678901"
			case "parameters":
				request.Parameters = map[string]any{"etag": "unreviewed"}
			case "prerequisites":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Delete: true}}
			case "impacts":
				request.LifecycleImpacts = []contracts.ActionImpact{{Delete: true}}
			case "empty-key":
				request.IdempotencyKey = ""
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "identity" {
				request.Asset.Identity.Partition = "foreign"
			}
			result, err := driver.Execute(t.Context(), *request)
			if mode != "normal" && mode != "gone" {
				if err == nil {
					t.Fatal("invalid deletion accepted", result)
				}
				want := 0
				if strings.HasPrefix(mode, "delete-") || mode == "lost-response" {
					want = 1
				}
				if *deletes != want {
					t.Fatal("unexpected mutation count", *deletes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "gone" {
				if *deletes != 0 {
					t.Fatal("deleted absent budget")
				}
				return
			}
			if result.ProviderRequestID != "request-123" || result.ProviderOperationID != "" || *deletes != 1 {
				t.Fatal(result, *deletes)
			}
			encoded, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), *request, restored)
			if err != nil || wait.Done {
				t.Fatal("live budget treated as deleted", wait, err)
			}
			*state = "gone"
			wait, err = driver.Wait(t.Context(), *request, restored)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, restored)
			if err != nil || !settled.Settled || settled.Operation == "" {
				t.Fatal(settled, err)
			}
			settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, contracts.ActionResult{})
			if err != nil || settled.Settled {
				t.Fatal("lost response released scope", settled, err)
			}
			for _, field := range []string{"review", "phase", "operation", "key", "account"} {
				var changed contracts.ActionResult
				_ = json.Unmarshal(encoded, &changed)
				copyRequest := *request
				copyRequest.Asset.Normalized = cloneParameters(request.Asset.Normalized)
				switch field {
				case "review", "phase":
					changed.Data[field] = "changed"
				case "operation":
					changed.ProviderOperationID = "operations/foreign"
				case "key":
					copyRequest.IdempotencyKey = "other"
				case "account":
					copyRequest.Asset.Normalized[billingBudgetAccountReview] = strings.Repeat("a", 64)
				}
				if _, err := driver.Wait(t.Context(), copyRequest, changed); err == nil {
					t.Fatal("changed receipt accepted", field)
				}
			}
		})
	}
}

func TestBillingBudgetDeleteCannotBypassReviewedConnection(t *testing.T) {
	_, r, request, _, deletes := budgetDeleteScenario(t)
	if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: billingBudgetDelete, Parameters: map[string]any{"name": testBillingBudget}}); err == nil || *deletes != 0 {
		t.Fatal("raw Invoke bypassed review", err)
	}
	if _, err := r.ResolveAction(t.Context(), "foreign", request.Asset); err == nil {
		t.Fatal("foreign credential accepted")
	}
}

func TestBillingBudgetSQLiteCleanupRestart(t *testing.T) {
	ctx := t.Context()
	_, r, request, mode, deletes := budgetDeleteScenario(t)
	dsn := filepath.Join(t.TempDir(), "budget-cleanup.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	defer func() { closeDB() }()
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := r.resourceKind(billingBudgetType)
	value := request.Asset
	value.ResourceKindID, value.ScopeID, value.FirstSeenAt, value.LastSeenAt = kind.ID, scope.ID, now, now
	for _, err := range []error{repos.Connections().PutConnection(ctx, connection), repos.Inventory().PutScope(ctx, scope), repos.Inventory().PutResourceKind(ctx, kind), repos.Inventory().PutAsset(ctx, value)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	planner := cleanup.NewService(repos, identityRegistry(t, r), cleanup.WithClock(func() time.Time { return now }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: value.ID}}})
	if err != nil || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal(task, err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: connection.ID, RequestedBy: "test", IdempotencyKey: "budget-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := repos.Jobs().ClaimNext(ctx, "budget-worker", now, time.Minute, execution.JobExecute)
	if err != nil {
		t.Fatal(err)
	}
	for round, want := range []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded} {
		closeDB()
		repos, closeDB = monitoringSQLite(t, dsn)
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		planner = cleanup.NewService(repos, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		h := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		if round >= 2 {
			*mode = "gone"
		}
		err := h.Handle(ctx, job)
		var retry *cleanup.RetryError
		if want == execution.ActionSucceeded && err != nil || want != execution.ActionSucceeded && !errors.As(err, &retry) {
			t.Fatal(round, err)
		}
		actions, err := repos.Executions().ListActions(ctx, attempt.ID)
		if err != nil || len(actions) != 1 || actions[0].Status != want || actions[0].ProviderResult["phase"] != "billing_budget_delete" || *deletes != 1 {
			t.Fatal(round, actions, err, *deletes)
		}
		now = now.Add(3 * time.Second)
	}
	finished, err := repos.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal(finished, err)
	}
	deleted, err := repos.Inventory().GetAsset(ctx, value.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatal("budget tombstone missing", deleted, err)
	}
}

func TestBillingBudgetCancellationPreventsWrite(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		_, base, request, _, deletes := budgetDeleteScenario(t)
		ctx, cancel := context.WithCancel(t.Context())
		reads := 0
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			result, err := base.transport.RoundTrip(req)
			if req.Method == "GET" && req.URL.Host == "billingbudgets.googleapis.com" && req.URL.Path == "/v1/"+testBillingBudget {
				reads++
				if duringRead && reads == 2 {
					cancel()
				}
			}
			return result, err
		})
		driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if !duringRead {
			cancel()
		}
		_, err = driver.Execute(ctx, *request)
		cancel()
		if !errors.Is(err, context.Canceled) || *deletes != 0 {
			t.Fatal("cancellation allowed budget mutation", err, *deletes)
		}
	}
}

func TestBillingBudgetDelete404NeedsOwnAbsentRead(t *testing.T) {
	_, r, request, mode, deletes := budgetDeleteScenario(t)
	*mode = "delete-404-gone"
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), *request)
	if err != nil || *deletes != 1 || result.Data["phase"] != "billing_budget_delete" {
		t.Fatal(result, err, *deletes)
	}
	wait, err := driver.Wait(t.Context(), *request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
}
