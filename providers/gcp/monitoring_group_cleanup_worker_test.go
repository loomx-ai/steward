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
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
)

type monitoringGroupCleanupContributors struct{ r *Runtime }

func (h monitoringGroupCleanupContributors) ResolveContributors(ctx context.Context, conn asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	native, err := h.r.MonitoringDependencies(ctx, conn.ID)
	return []governance.Contributor{NewUptimeTargets(), native}, err
}
func TestMonitoringGroupSQLiteOrderedCleanupRestart(t *testing.T) {
	ctx := t.Context()
	s := newMonitoringGroupConsumerScenario(t)
	object(object(array(s.values["alertPolicies"][0]["conditions"])[0])["conditionThreshold"])["filter"] = uptimeMetricFilter + ` AND metric.labels.check_id="public-check" AND group.id="9876"`
	s.assets[3].Normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, s.assets[3].Identity.NativeID, s.values["alertPolicies"][0])
	// Uptime consumer discovery also follows reverse metric scopes and Logging
	// routes; retain the existing protocol fixtures for those separate APIs.
	fallback, _, _, _, _ := uptimeScenario(t)
	transport := s.r.transport
	writes := []string{}
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" || req.URL.Host == loggingHost {
			return fallback.transport.RoundTrip(req)
		}
		if req.Method == "DELETE" {
			parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
			if req.URL.Host != "monitoring.googleapis.com" || len(parts) != 5 || parts[0] != "v3" || parts[1] != "projects" || parts[2] != "sample-project" {
				t.Fatal("unexpected deletion", req.URL)
			}
			collection, id := parts[3], parts[4]
			if collection != "groups" && collection != "uptimeCheckConfigs" && collection != "alertPolicies" {
				t.Fatal("deleted member/other resource", req.URL)
			}
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				if len(body) != 0 {
					t.Fatal("native DELETE body")
				}
			}
			if collection == "groups" {
				if req.URL.RawQuery != "recursive=false" {
					t.Fatal("recursive group DELETE", req.URL)
				}
				for _, row := range s.values["groups"] {
					if last(text(row["parentName"])) == id {
						return apiResponse(req, 400, `{"error":{"code":400,"message":"descendants remain"}}`), nil
					}
				}
				if id == "9876" && (len(s.values["uptimeCheckConfigs"]) != 0 || len(s.values["alertPolicies"]) != 0) {
					t.Fatal("group deleted with live consumers")
				}
			} else if req.URL.RawQuery != "" {
				t.Fatal(req.URL)
			}
			if collection == "uptimeCheckConfigs" && len(s.values["alertPolicies"]) != 0 {
				t.Fatal("Uptime deleted before its policy")
			}
			rows, found := []map[string]any{}, false
			for _, row := range s.values[collection] {
				if last(text(row["name"])) == id {
					found = true
				} else {
					rows = append(rows, row)
				}
			}
			if !found {
				return apiResponse(req, 404, `{}`), nil
			}
			s.values[collection] = rows
			writes = append(writes, collection+"/"+id)
			return apiResponse(req, 200, `{}`), nil
		}
		return transport.RoundTrip(req)
	})
	dsn := filepath.Join(t.TempDir(), "group-cleanup.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	defer func() { closeDB() }()
	now := time.Now().UTC()
	conn := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: conn.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	if err := repos.Connections().PutConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	selectors := []plan.CleanupSelector{}
	for _, value := range s.assets {
		value.ScopeID = scope.ID
		value.FirstSeenAt = now
		value.LastSeenAt = now
		if err := repos.Inventory().PutResourceKind(ctx, s.r.resourceKind(value.Identity.NativeType)); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
		selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
	}
	if err := repos.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "groups", ConnectionID: conn.ID, Status: asset.ScanSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "groups", ScanRunID: "groups", Provider: asset.ProviderGCP, ScopeID: scope.ID, ResourceKindID: s.assets[0].ResourceKindID, Source: productInventorySource, Status: asset.ShardSucceeded, Authoritative: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := governance.NewGraphHandler(repos, identityRegistry(t, s.r), monitoringGroupCleanupContributors{s.r}).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "groups"}}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repos, identityRegistry(t, s.r), cleanup.WithClock(func() time.Time { return now }))
	blocked, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: conn.ID, CreatedBy: "test", Selectors: selectors[:1]})
	if err != nil || len(blocked.Task.Blockers) == 0 {
		t.Fatal("consumers automatically selected", blocked, err)
	}
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: conn.ID, CreatedBy: "test", Selectors: selectors})
	if err != nil || len(task.Task.Blockers) != 0 || len(task.Steps) != 4 || task.Steps[3].AssetID != s.assets[0].ID {
		t.Fatal(task, err)
	}
	prerequisites, err := plan.RequiredDeletions(task.Steps[3])
	if err != nil || len(prerequisites) != 3 {
		t.Fatal(prerequisites, err)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: conn.ID, RequestedBy: "test", IdempotencyKey: "group-order", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs := []execution.Job{}
	for range task.Steps {
		job, err := repos.Jobs().ClaimNext(ctx, "group-worker", now, time.Minute, execution.JobExecute)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	done := map[execution.JobID]bool{}
	for round := 0; round < 20 && len(done) != len(jobs); round++ {
		// Close/reopen before every worker action, including after each native DELETE.
		// Provider state survives; runtime and all SQLite handles are reconstructed.
		for _, job := range jobs {
			if done[job.ID] {
				continue
			}
			closeDB()
			repos, closeDB = monitoringSQLite(t, dsn)
			fresh := protocolRuntime(t, s.r.transport.RoundTrip)
			service = cleanup.NewService(repos, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
			worker := cleanup.NewExecutionHandler(service, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
			}))
			err := worker.Handle(ctx, job)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal(round, job, err)
			}
			if err == nil {
				done[job.ID] = true
				if err := repos.Jobs().Complete(ctx, job.ID, "group-worker", execution.JobSucceeded, "", now); err != nil {
					t.Fatal(err)
				}
			}
		}
		now = now.Add(3 * time.Second)
	}
	current, err := repos.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || current.Status != execution.ExecutionSucceeded || len(done) != 4 || len(writes) != 4 || writes[3] != "groups/9876" {
		t.Fatal(current, done, writes, err)
	}
	actions, err := repos.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(actions) != 4 {
		t.Fatal(actions, err)
	}
	for _, action := range actions {
		if action.Status != execution.ActionSucceeded {
			t.Fatal(action)
		}
		payload, _ := json.Marshal(action)
		if strings.Contains(string(payload), "PRIVATE_") {
			t.Fatal("private native data in action")
		}
	}
	for _, value := range s.assets {
		saved, err := repos.Inventory().GetAsset(ctx, value.ID)
		if err != nil || saved.DeletedAt == nil {
			t.Fatal(saved, err)
		}
	}
}
