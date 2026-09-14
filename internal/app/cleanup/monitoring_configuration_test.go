package cleanup

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func monitoringPolicyAsset(id, project string) asset.Asset {
	return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: "monitoring.googleapis.com/AlertPolicy", NativeID: "//monitoring.googleapis.com/projects/" + project + "/alertPolicies/" + id}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
}
func TestMonitoringPolicyPlansSerializeProjectWrites(t *testing.T) {
	input := plan.Input{CleanupTaskID: "monitoring-plan"}
	for _, id := range []string{"a", "b", "c"} {
		p := "sample-project"
		if id == "c" {
			p = "other-project"
		}
		value := monitoringPolicyAsset(id, p)
		if id == "b" {
			value.Identity.Partition = "google-cloud"
		}
		input.Assets = append(input.Assets, value)
		input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, value.ID)
	}
	result, err := solveCleanupPlan(input)
	if err != nil || len(result.Steps) != 3 {
		t.Fatal(result, err)
	}
	byID := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range result.Steps {
		byID[step.AssetID] = step
	}
	if !slices.Contains(byID["b"].DependsOn, byID["a"].ID) || len(byID["c"].DependsOn) != 0 {
		t.Fatal(result.Steps)
	}
	for _, step := range result.Steps {
		if !strings.Contains(step.Evidence[routerMutationScope].(string), "monitoring.googleapis.com/projects/") {
			t.Fatal(step)
		}
	}
	for _, mode := range []string{"host", "collection", "extra", "query", "whitespace", "partition", "connection"} {
		value := monitoringPolicyAsset("a", "sample-project")
		switch mode {
		case "host":
			value.Identity.NativeID = strings.Replace(value.Identity.NativeID, "monitoring.googleapis.com", "evil.example", 1)
		case "collection":
			value.Identity.NativeID = strings.Replace(value.Identity.NativeID, "alertPolicies", "uptimeCheckConfigs", 1)
		case "extra":
			value.Identity.NativeID += "/extra"
		case "query":
			value.Identity.NativeID += "?a=1"
		case "whitespace":
			value.Identity.NativeID += " "
		case "partition":
			value.Identity.Partition = "other"
		case "connection":
			value.Identity.ConnectionID = ""
		}
		if _, err := routerScope(value.Identity); err == nil {
			t.Fatal("malformed mutation scope", mode)
		}
	}
}
func TestMonitoringPolicyProjectScopePersistsThroughFailures(t *testing.T) {
	ctx := t.Context()
	db := filepath.Join(t.TempDir(), "monitoring.db")
	repos, err := sqlite.Open(db, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	prior := monitoringPolicyAsset("nat-a", "sample-project")
	next := monitoringPolicyAsset("policy-b", "sample-project")
	for _, value := range []asset.Asset{prior, next} {
		if err := repos.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	old := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "old", ConnectionID: "connection", Status: plan.StatusExecuting, CreatedAt: now}, Steps: []plan.CleanupTaskStep{{ID: "old-step", CleanupTaskID: "old", AssetID: prior.ID, Action: "delete", Evidence: map[string]any{plan.EvidencePlannedAsset: prior}}}}
	current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "new", ConnectionID: "connection"}, Steps: []plan.CleanupTaskStep{{ID: "new-step", AssetID: next.ID, Action: "delete", Evidence: map[string]any{plan.EvidencePlannedAsset: next}}}}
	if err := repos.CleanupTasks().CreateTask(ctx, old.Task, old.Steps, nil); err != nil {
		t.Fatal(err)
	}
	attempt := execution.ExecutionAttempt{ID: "old-run", ConnectionID: "connection", CleanupTaskID: "old", Status: execution.ExecutionRunning, IdempotencyKey: "old-run", CreatedAt: now}
	if err := repos.Executions().CreateExecution(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	action := execution.ActionAttempt{ID: "old-action", ExecutionID: attempt.ID, CleanupTaskStepID: "old-step", AssetID: prior.ID, Action: "delete", Status: execution.ActionInvoking, IdempotencyKey: "old-action", CreatedAt: now, UpdatedAt: now}
	if err := repos.Executions().AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	for _, status := range []execution.ExecutionStatus{execution.ExecutionRunning, execution.ExecutionWaiting, execution.ExecutionPaused, execution.ExecutionFailed, execution.ExecutionCanceled} {
		attempt.Status = status
		if err := repos.Executions().UpdateExecution(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		if err := guardSharedConfiguration(ctx, repos, current, nil, ""); !errors.Is(err, persistence.ErrConflict) {
			t.Fatal("unsettled project write released", status, err)
		}
	}
	if err := guardSharedConfiguration(ctx, repos, old, nil, attempt.ID); err != nil {
		t.Fatal("original task cannot resume", err)
	}
	other := current
	other.Steps = append([]plan.CleanupTaskStep(nil), current.Steps...)
	foreign := monitoringPolicyAsset("policy-b", "other-project")
	other.Steps[0].Evidence = map[string]any{plan.EvidencePlannedAsset: foreign}
	if err := guardSharedConfiguration(ctx, repos, other, nil, ""); err != nil {
		t.Fatal("other project blocked", err)
	}
	// A provider's read-only terminal proof releases scope without rewriting the
	// failed action or inventing a successful deletion. Native proof logic is
	// exercised by the GCP driver tests.
	registry := &mutationProofRegistry{}
	if err := repos.WithTx(ctx, func(tx persistence.Repositories) error {
		return guardSharedConfiguration(ctx, tx, current, registry, "")
	}); err != nil || registry.calls != 1 {
		t.Fatal(err, registry.calls)
	}
	saved, err := repos.Executions().GetAction(ctx, action.ID)
	if err != nil || saved.MutationSettlement == nil || saved.Status != execution.ActionInvoking {
		t.Fatal(saved, err)
	}
	repos, err = sqlite.Open(db, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := guardSharedConfiguration(ctx, repos, current, nil, ""); err != nil {
		t.Fatal("persisted settlement lost on restart", err)
	}
	saved.UpdatedAt = saved.UpdatedAt.Add(time.Second)
	if err := repos.Executions().UpdateAction(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err := guardSharedConfiguration(ctx, repos, current, nil, ""); !errors.Is(err, persistence.ErrConflict) {
		t.Fatal("stale proof survived action update", err)
	}
}
