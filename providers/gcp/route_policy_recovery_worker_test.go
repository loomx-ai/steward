package gcp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRoutePolicySQLiteUnrecordedDeleteSettlement(t *testing.T) {
	for _, mode := range []string{"detach-receipt", "lost-receipts", "failed-delete"} {
		t.Run(mode, func(t *testing.T) {
			routerComponentSQLiteCleanup(t, routePolicyType, true, false, false, func(c routerCleanupCheckpoint) {
				ctx := t.Context()
				repositories := c.Repositories
				competing, err := c.Planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: c.Task.Steps[0].AssetID}}, CreatedBy: "test"})
				if err != nil {
					t.Fatal(err)
				}
				actions, err := repositories.Executions().ListActions(ctx, c.Attempt.ID)
				if err != nil || len(actions) != 1 {
					t.Fatal(actions, err)
				}
				original := actions[0]
				live, err := repositories.Inventory().GetAsset(ctx, original.AssetID)
				if err != nil {
					t.Fatal(err)
				}
				reviewed, err := plan.PlannedAsset(c.Task.Steps[0].Evidence, live)
				if err != nil {
					t.Fatal(err)
				}
				request := contracts.ActionRequest{Asset: reviewed, Action: "delete", IdempotencyKey: original.IdempotencyKey}
				result := contracts.ActionResult{ProviderOperationID: original.ProviderOperationID, ProviderRequestID: original.ProviderRequestID, Data: original.ProviderResult}
				c.Fixture.patchStatus = "DONE"
				applyRoutePolicyPatch(t, c.Fixture)
				driver, err := c.Runtime.ResolveAction(ctx, "connection", reviewed)
				if err != nil {
					t.Fatal(err)
				}
				next, err := driver.Wait(ctx, request, result)
				if err != nil || next.Data["phase"] != "route_policy_delete" || c.Fixture.deletes != 1 {
					t.Fatal(next, err)
				}
				// Simulate process loss after native POST but before the worker saves next.Data.
				old := c.Attempt
				old.Status = execution.ExecutionCanceled
				if mode == "failed-delete" {
					old.Status = execution.ExecutionFailed
				}
				if err := repositories.Executions().UpdateExecution(ctx, old); err != nil {
					t.Fatal(err)
				}
				job := c.Job
				job.Status, job.LeaseOwner, job.LeaseUntil = execution.JobCanceled, "", nil
				if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				if mode == "lost-receipts" {
					original.ProviderResult, original.ProviderOperationID, original.Status = nil, "", execution.ActionInvoking
					if err := repositories.Executions().UpdateAction(ctx, original); err != nil {
						t.Fatal(err)
					}
				}
				present, lookups := false, 0
				transport := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != "GET" || !strings.Contains(req.URL.Path, "/regions/us-central1/operations") {
						t.Fatal("recovery mutated cloud/read resource", req.Method, req.URL)
					}
					if strings.HasSuffix(req.URL.Path, "/operations") {
						lookups++
						items := []any{}
						for _, operation := range []map[string]any{c.Fixture.patchOperation, c.Fixture.operation} {
							if req.URL.Query().Get("filter") != "clientOperationId = \""+text(operation["clientOperationId"])+"\"" {
								continue
							}
							if operation["name"] == c.Fixture.operation["name"] && !present {
								continue
							}
							data := cloneParameters(operation)
							data["status"] = c.Fixture.patchStatus
							if operation["name"] == c.Fixture.operation["name"] {
								data["status"] = c.Fixture.status
							}
							items = append(items, data)
						}
						return dataformResponse(req, 200, map[string]any{"items": items}), nil
					}
					if strings.HasSuffix(req.URL.Path, "/bgp-operation") {
						data := cloneParameters(c.Fixture.patchOperation)
						data["status"] = c.Fixture.patchStatus
						return dataformResponse(req, 200, data), nil
					}
					t.Fatal("unexpected recovery request", req.URL)
					return nil, nil
				})
				create := cleanup.CreateExecutionRequest{CleanupTaskID: competing.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "settled-policy", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}
				for _, checkpoint := range []string{"missing-delete", "running-delete", "running-detach", "done"} {
					repositories, err = sqlite.Open(c.DatabasePath, "../../migrations")
					if err != nil {
						t.Fatal(err)
					}
					c.Fixture.patchStatus = "DONE"
					if checkpoint != "missing-delete" {
						present = true
					}
					if checkpoint == "running-detach" {
						c.Fixture.patchStatus = "RUNNING"
						c.Fixture.status = "DONE"
					}
					if checkpoint == "done" {
						c.Fixture.status = "DONE"
					}
					if mode == "failed-delete" && checkpoint == "done" {
						c.Fixture.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
					}
					fresh := protocolRuntime(t, transport.transport.RoundTrip)
					planner := cleanup.NewService(repositories, identityRegistry(t, fresh))
					created, err := planner.CreateExecution(ctx, create)
					if checkpoint != "done" {
						if !errors.Is(err, persistence.ErrConflict) {
							t.Fatal("uncertain phase released Router", checkpoint, created, err)
						}
						saved, err := repositories.Executions().GetAction(ctx, original.ID)
						if err != nil || saved.MutationSettlement != nil {
							t.Fatal("partial proof was persisted", saved, err)
						}
						continue
					}
					if err != nil || created.ID == "" {
						t.Fatal(created, err)
					}
				}
				repositories, err = sqlite.Open(c.DatabasePath, "../../migrations")
				if err != nil {
					t.Fatal(err)
				}
				saved, err := repositories.Executions().GetAction(ctx, original.ID)
				if err != nil || saved.MutationSettlement == nil || len(strings.Split(saved.MutationSettlement.Operation, "\n")) != 2 {
					t.Fatal("missing durable all-phase proof", saved, err)
				}
				saved.MutationSettlement = nil
				if firewallDigest(saved) != firewallDigest(original) {
					t.Fatal("settlement changed original action history")
				}
				retained, err := repositories.Inventory().GetAsset(ctx, original.AssetID)
				if err != nil || retained.DeletedAt != nil || !c.Fixture.exists || c.Fixture.deletes != 1 || c.Fixture.patches != 1 || lookups == 0 {
					t.Fatal("settlement changed deletion outcome", retained, err)
				}
				previous, err := repositories.Executions().GetExecution(ctx, old.ID)
				if err != nil || previous.Status != old.Status {
					t.Fatal(previous, err)
				}
			})
		})
	}
}

func TestRouterComponentSQLiteSinglePhaseSettlement(t *testing.T) {
	for _, kind := range []string{routePolicyType, namedSetType} {
		t.Run(kind, func(t *testing.T) {
			routerComponentSQLiteCleanup(t, kind, false, false, false, func(c routerCleanupCheckpoint) {
				ctx := t.Context()
				competing, err := c.Planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: c.Task.Steps[0].AssetID}}, CreatedBy: "test"})
				if err != nil {
					t.Fatal(err)
				}
				old := c.Attempt
				old.Status = execution.ExecutionFailed
				if err := c.Repositories.Executions().UpdateExecution(ctx, old); err != nil {
					t.Fatal(err)
				}
				job := c.Job
				job.Status, job.LeaseOwner, job.LeaseUntil = execution.JobFailed, "", nil
				if err := c.Repositories.Jobs().UpdateJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				actions, err := c.Repositories.Executions().ListActions(ctx, old.ID)
				if err != nil || len(actions) != 1 {
					t.Fatal(actions, err)
				}
				original := actions[0]
				original.ProviderOperationID, original.ProviderResult, original.Status = "", nil, execution.ActionInvoking
				if err := c.Repositories.Executions().UpdateAction(ctx, original); err != nil {
					t.Fatal(err)
				}
				lookup := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/regions/us-central1/operations") || req.URL.Query().Get("filter") != "clientOperationId = \""+c.Fixture.requestIDs[0]+"\"" {
						t.Fatal(req.Method, req.URL)
					}
					data := cloneParameters(c.Fixture.operation)
					data["status"] = c.Fixture.status
					return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
				})
				for _, state := range []string{"RUNNING", "DONE"} {
					repositories, err := sqlite.Open(c.DatabasePath, "../../migrations")
					if err != nil {
						t.Fatal(err)
					}
					c.Fixture.status = state
					planner := cleanup.NewService(repositories, identityRegistry(t, protocolRuntime(t, lookup.transport.RoundTrip)))
					created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: competing.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "settled-component", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
					if state == "RUNNING" {
						if !errors.Is(err, persistence.ErrConflict) {
							t.Fatal(created, err)
						}
						continue
					}
					if err != nil || created.ID == "" {
						t.Fatal(created, err)
					}
					saved, err := repositories.Executions().GetAction(ctx, original.ID)
					if err != nil || saved.MutationSettlement == nil {
						t.Fatal(saved, err)
					}
					saved.MutationSettlement = nil
					if firewallDigest(saved) != firewallDigest(original) || c.Fixture.deletes != 1 || !c.Fixture.exists {
						t.Fatal("recovery changed original execution")
					}
				}
			})
		})
	}
}
