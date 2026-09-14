package gcp

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestCloudNatSQLiteTerminalOperationReleasesScopeWithoutRepeatingDelete(t *testing.T) {
	for _, mode := range []string{"canceled", "failed", "native-failure", "lost-receipt"} {
		t.Run(mode, func(t *testing.T) {
			routerComponentSQLiteCleanup(t, cloudNatType, false, false, false, func(c routerCleanupCheckpoint) {
				ctx := t.Context()
				repositories := c.Repositories
				f := c.Fixture
				old := c.Attempt
				old.Status = execution.ExecutionCanceled
				if mode == "failed" {
					old.Status = execution.ExecutionFailed
				}
				if err := repositories.Executions().UpdateExecution(ctx, old); err != nil {
					t.Fatal(err)
				}
				job := c.Job
				job.Status = execution.JobCanceled
				job.LeaseOwner = ""
				job.LeaseUntil = nil
				if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
					t.Fatal(err)
				}
				actions, err := repositories.Executions().ListActions(ctx, old.ID)
				if err != nil || len(actions) != 1 {
					t.Fatal(actions, err)
				}
				action := actions[0]
				if mode == "lost-receipt" {
					action.ProviderOperationID = ""
					action.ProviderResult = nil
					action.Status = execution.ActionInvoking
					if err := repositories.Executions().UpdateAction(ctx, action); err != nil {
						t.Fatal(err)
					}
					original := c.Runtime.transport
					c.Runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
						if strings.HasSuffix(req.URL.Path, "/operations") {
							if req.Method != "GET" || req.URL.Query().Get("filter") != "clientOperationId = \""+f.requestIDs[0]+"\"" {
								t.Fatal(req.Method, req.URL)
							}
							data := cloneParameters(f.operation)
							data["status"] = f.status
							return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
						}
						return original.RoundTrip(req)
					})
					c.Planner = cleanup.NewService(repositories, identityRegistry(t, c.Runtime))
				}
				request := cleanup.CreateExecutionRequest{CleanupTaskID: c.Competing.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "recovered-nat", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}
				if _, err := c.Planner.CreateExecution(ctx, request); !errors.Is(err, persistence.ErrConflict) {
					t.Fatal("pending native update released scope", err)
				}
				f.status = "DONE"
				if mode == "native-failure" {
					f.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
				}
				created, err := c.Planner.CreateExecution(ctx, request)
				if err != nil || created.ID == "" {
					t.Fatal("terminal operation did not release scope", created, err)
				}
				saved, err := repositories.Executions().GetAction(ctx, action.ID)
				if err != nil || saved.Status != action.Status || saved.MutationSettlement == nil || saved.MutationSettlement.Operation == "" || saved.MutationSettlement.Digest == "" {
					t.Fatal("missing persisted settlement proof", saved, err)
				}
				if saved.UpdatedAt != action.UpdatedAt || saved.ProviderOperationID != action.ProviderOperationID {
					t.Fatal("recovery rewrote original action", saved, action)
				}
				retained, err := repositories.Inventory().GetAsset(ctx, action.AssetID)
				if err != nil || retained.DeletedAt != nil || !f.exists || f.deletes != 1 {
					t.Fatal("settlement was mistaken for deletion", retained, err, f.deletes)
				}
				previous, err := repositories.Executions().GetExecution(ctx, old.ID)
				if err != nil || previous.Status != old.Status {
					t.Fatal("terminal execution was resumed", previous, err)
				}
			})
		})
	}
}
