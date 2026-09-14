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
)

func TestRouterSQLiteTerminalRecoveryPreservesCascadeReview(t *testing.T) {
	for _, mode := range []string{"receipt", "lost-receipt", "prior-receipt", "changed-child"} {
		t.Run(mode, func(t *testing.T) {
			routerCascadeSQLiteCleanup(t, func(c routerCascadeCheckpoint) {
				ctx := t.Context()
				repos := c.Repositories
				actions, err := repos.Executions().ListActions(ctx, c.Attempt.ID)
				if err != nil {
					t.Fatal(err)
				}
				var original execution.ActionAttempt
				for _, a := range actions {
					if a.ProviderResult["phase"] == "router_delete" {
						original = a
					}
				}
				if original.ID == "" {
					t.Fatal("missing Router receipt")
				}
				task, err := repos.CleanupTasks().GetTask(ctx, c.Task.Task.ID)
				if err != nil {
					t.Fatal(err)
				}
				planner := cleanup.NewService(repos, identityRegistry(t, c.Runtime))
				competing, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: original.AssetID}}, CreatedBy: "test"})
				if err != nil {
					t.Fatal(err)
				}
				old := c.Attempt
				old.Status = execution.ExecutionCanceled
				if err := repos.Executions().UpdateExecution(ctx, old); err != nil {
					t.Fatal(err)
				}
				job := c.Job
				job.Status, job.LeaseOwner, job.LeaseUntil = execution.JobCanceled, "", nil
				if err := repos.Jobs().UpdateJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				op := c.Fixture.operations["router-delete"]
				switch mode {
				case "lost-receipt":
					original.ProviderOperationID, original.ProviderResult, original.Status = "", nil, execution.ActionInvoking
				case "prior-receipt":
					// Import an operation-only historical receipt; native UUID must also be old.
					original.ProviderResult = map[string]any{"operation": original.ProviderOperationID}
					op["clientOperationId"] = googleRequestID(original.IdempotencyKey)
				case "changed-child":
					child, err := repos.Inventory().GetAsset(ctx, task.ImpactItems[0].AssetID)
					if err != nil {
						t.Fatal(err)
					}
					child, err = plan.PlannedAsset(task.ImpactItems[0].Evidence, child)
					if err != nil {
						t.Fatal(err)
					}
					child.Normalized["review-changed"] = true
					task.ImpactItems[0].Evidence[plan.EvidencePlannedAsset] = child
					if err := repos.CleanupTasks().ReplaceTask(ctx, task.Task, task.Steps, task.ImpactItems); err != nil {
						t.Fatal(err)
					}
				}
				if err := repos.Executions().UpdateAction(ctx, original); err != nil {
					t.Fatal(err)
				}
				reads := 0
				transport := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					if req.Method != "GET" || !strings.Contains(req.URL.Path, "/regions/us-central1/operations") {
						t.Fatal("recovery mutated/read resource", req.Method, req.URL)
					}
					reads++
					if strings.HasSuffix(req.URL.Path, "/operations") {
						if req.URL.Query().Get("filter") != "clientOperationId = \""+text(op["clientOperationId"])+"\"" {
							t.Fatal("recovery lost full child-review UUID", req.URL)
						}
						return dataformResponse(req, 200, map[string]any{"items": []any{op}}), nil
					}
					return dataformResponse(req, 200, op), nil
				})
				for _, status := range []string{"RUNNING", "DONE"} {
					repos, err = sqlite.Open(c.DatabasePath, "../../migrations")
					if err != nil {
						t.Fatal(err)
					}
					op["status"] = status
					fresh := protocolRuntime(t, transport.transport.RoundTrip)
					planner = cleanup.NewService(repos, identityRegistry(t, fresh))
					created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: competing.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "router-recovered", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
					if status == "RUNNING" || mode == "changed-child" {
						if !errors.Is(err, persistence.ErrConflict) {
							t.Fatal("uncertain/review-changed Router released", created, err)
						}
						continue
					}
					if err != nil || created.ID == "" {
						t.Fatal(created, err)
					}
				}
				saved, err := repos.Executions().GetAction(ctx, original.ID)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "changed-child" {
					if saved.MutationSettlement != nil || reads != 0 {
						t.Fatal("changed child review accepted")
					}
					return
				}
				if saved.MutationSettlement == nil || saved.MutationSettlement.Operation == "" {
					t.Fatal("missing proof", saved)
				}
				saved.MutationSettlement = nil
				if firewallDigest(saved) != firewallDigest(original) {
					t.Fatal("recovery rewrote action history")
				}
				root, err := repos.Inventory().GetAsset(ctx, original.AssetID)
				if err != nil || root.DeletedAt != nil {
					t.Fatal("settlement marked Router deleted", err)
				}
				stored, err := repos.CleanupTasks().GetTask(ctx, c.Task.Task.ID)
				if err != nil || stored.ImpactItems[0].Result == plan.ImpactDeletedByController {
					t.Fatal("settlement marked cascade complete", err)
				}
				nat, err := repos.Inventory().GetAsset(ctx, stored.ImpactItems[0].AssetID)
				if err != nil || nat.DeletedAt != nil || c.Fixture.routerDeletes != 1 {
					t.Fatal("NAT tombstone/repeated delete", err)
				}
				previous, err := repos.Executions().GetExecution(ctx, old.ID)
				if err != nil || previous.Status != old.Status {
					t.Fatal(previous, err)
				}
			})
		})
	}
}
