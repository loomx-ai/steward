package cleanup

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

func routerScope(identity asset.Identity) (string, error) {
	if identity.Provider != asset.ProviderGCP {
		return "", nil
	}
	// The persisted Router scope mechanism also coordinates Monitoring's
	// project writes and account-scoped Billing Budget deletion.
	if identity.NativeType == "monitoring.googleapis.com/UptimeCheckConfig" || identity.NativeType == "monitoring.googleapis.com/Dashboard" || identity.NativeType == "monitoring.googleapis.com/Group" || identity.NativeType == "monitoring.googleapis.com/AlertPolicy" || identity.NativeType == "monitoring.googleapis.com/NotificationChannel" || identity.NativeType == "billingbudgets.googleapis.com/Budget" {
		collection := "alertPolicies"
		if identity.NativeType == "monitoring.googleapis.com/UptimeCheckConfig" {
			collection = "uptimeCheckConfigs"
		}
		if identity.NativeType == "monitoring.googleapis.com/Dashboard" {
			collection = "dashboards"
		}
		if identity.NativeType == "monitoring.googleapis.com/Group" {
			collection = "groups"
		}
		host, root := "monitoring.googleapis.com", "projects"
		if identity.NativeType == "billingbudgets.googleapis.com/Budget" {
			host, root, collection = "billingbudgets.googleapis.com", "billingAccounts", "budgets"
		}
		if identity.NativeType == "monitoring.googleapis.com/NotificationChannel" {
			collection = "notificationChannels"
		}
		parts := strings.Split(strings.TrimPrefix(identity.NativeID, "//"+host+"/"), "/")
		if identity.ConnectionID == "" || (identity.Partition != "gcp" && identity.Partition != "google-cloud") || !strings.HasPrefix(identity.NativeID, "//"+host+"/") || len(parts) != 4 || parts[0] != root || parts[2] != collection {
			return "", fmt.Errorf("invalid native Monitoring mutation identity")
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, " ?#%\\\t\r\n") {
				return "", fmt.Errorf("invalid native Monitoring mutation segment")
			}
		}
		return monitoringProjectScope(string(identity.ConnectionID) + "/gcp///" + host + "/" + root + "/" + parts[1] + "/" + collection), nil
	}
	suffix := ""
	switch identity.NativeType {
	case "compute.googleapis.com/Router":
	case "compute.googleapis.com/RouterNat":
		suffix = "nats"
	case "compute.googleapis.com/RoutePolicy":
		suffix = "routePolicies"
	case "compute.googleapis.com/NamedSet":
		suffix = "namedSets"
	default:
		return "", nil
	}
	parts := strings.Split(strings.TrimPrefix(identity.NativeID, "//compute.googleapis.com/"), "/")
	size := 6
	if suffix != "" {
		size = 8
	}
	if identity.ConnectionID == "" || (identity.Partition != "gcp" && identity.Partition != "google-cloud") || !strings.HasPrefix(identity.NativeID, "//compute.googleapis.com/") || len(parts) != size || parts[0] != "projects" || parts[2] != "regions" || parts[4] != "routers" {
		return "", fmt.Errorf("invalid native Router mutation identity")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "?#%\\") {
			return "", fmt.Errorf("invalid native Router mutation segment")
		}
	}
	if suffix != "" && parts[6] != suffix {
		return "", fmt.Errorf("invalid native Router component")
	}
	return string(identity.ConnectionID) + "/gcp///compute.googleapis.com/" + strings.Join(parts[:6], "/"), nil
}

// Frozen identities are authoritative even if older tasks lack scope annotations.
func taskRouterScopes(ctx context.Context, repositories persistence.Repositories, task persistence.CleanupTaskAggregate) (map[asset.AssetID]string, error) {
	result := map[asset.AssetID]string{}
	for _, step := range task.Steps {
		var value asset.Asset
		if raw, exists := step.Evidence[plan.EvidencePlannedAsset]; exists {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(encoded, &value); err != nil {
				return nil, err
			}
			if value.ID == "" || value.ID != step.AssetID {
				return nil, fmt.Errorf("reviewed cleanup asset identity is missing or changed")
			}
		} else {
			// Preserve legacy explicit reservations when no reviewed snapshot exists.
			scope, _ := step.Evidence[routerMutationScope].(string)
			if scope == "" {
				scope, _ = step.Evidence[natMutationScope].(string)
			}
			if scope != "" {
				result[step.AssetID] = monitoringProjectScope(strings.Replace(scope, "/google-cloud/", "/gcp/", 1))
				continue
			}
			if step.Action != "delete" {
				continue
			}
			var err error
			value, err = repositories.Inventory().GetAsset(ctx, step.AssetID)
			if err != nil {
				return nil, err
			}
		}
		if step.Action != "delete" {
			continue
		}
		scope, err := routerScope(value.Identity)
		if err != nil {
			return nil, err
		}
		if scope != "" && (value.ID != step.AssetID || value.Identity.ConnectionID != task.Task.ConnectionID) {
			return nil, fmt.Errorf("reviewed Router mutation identity changed")
		}
		result[step.AssetID] = scope
	}
	return result, nil
}

func serializeRouterSteps(steps []plan.CleanupTaskStep, scopes map[asset.AssetID]string) ([]plan.CleanupTaskStep, bool, error) {
	relevant := false
	for _, step := range steps {
		relevant = relevant || step.Action == "delete" && scopes[step.AssetID] != ""
	}
	if !relevant {
		return steps, false, nil
	}
	ordered, err := plan.OrderSteps(steps)
	if err != nil {
		return nil, false, err
	}
	// Existing transitive prerequisites already serialize a pair. Do not add a
	// redundant edge that would misclassify a safe legacy continuation as unsafe.
	dependencies := map[plan.StepID][]plan.StepID{}
	for _, step := range ordered {
		dependencies[step.ID] = step.DependsOn
	}
	dependsOn := func(step, prior plan.StepID) bool {
		pending := slices.Clone(dependencies[step])
		visited := map[plan.StepID]bool{}
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if id == prior {
				return true
			}
			if !visited[id] {
				visited[id] = true
				pending = append(pending, dependencies[id]...)
			}
		}
		return false
	}
	changed := false
	previous := map[string]plan.StepID{}
	for i := range ordered {
		step := &ordered[i]
		scope := scopes[step.AssetID]
		if scope == "" || step.Action != "delete" {
			continue
		}
		if step.Evidence[routerMutationScope] != scope {
			step.Evidence = cloneRequest(step.Evidence)
			if step.Evidence == nil {
				step.Evidence = map[string]any{}
			}
			step.Evidence[routerMutationScope] = scope
			changed = true
		}
		if prior := previous[scope]; prior != "" && !dependsOn(step.ID, prior) {
			step.DependsOn = append(slices.Clone(step.DependsOn), prior)
			dependencies[step.ID] = step.DependsOn
			changed = true
		}
		previous[scope] = step.ID
	}
	return ordered, changed, nil
}

// Upgrade old plans before starting workers. A continuation with newly required
// edges cannot rewrite ordering around an already-issued, unsettled native write.
func prepareRouterConfiguration(ctx context.Context, repositories persistence.Repositories, task *persistence.CleanupTaskAggregate, attempt *execution.ExecutionAttempt) (bool, error) {
	scopes, err := taskRouterScopes(ctx, repositories, *task)
	if err != nil {
		return false, err
	}
	steps, changed, err := serializeRouterSteps(task.Steps, scopes)
	if err != nil || !changed {
		return false, err
	}
	affected := map[string]bool{}
	prior := map[plan.StepID]plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		prior[step.ID] = step
	}
	for _, step := range steps {
		for _, dependency := range step.DependsOn {
			if !slices.Contains(prior[step.ID].DependsOn, dependency) {
				affected[scopes[step.AssetID]] = true
			}
		}
	}
	if attempt != nil && len(affected) != 0 {
		if err := repositories.Executions().LockExecution(ctx, attempt.ID); err != nil {
			return false, err
		}
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
		if err != nil {
			return false, err
		}
		for _, job := range jobs {
			step := prior[plan.StepID(payloadString(job.Payload, "cleanup_task_step_id"))]
			if affected[scopes[step.AssetID]] && payloadString(job.Payload, "execution_id") == string(attempt.ID) && job.Status != execution.JobSucceeded && job.Status != execution.JobFailed && job.Status != execution.JobCanceled {
				return false, fmt.Errorf("%w: legacy Router ordering requires terminal worker jobs", persistence.ErrConflict)
			}
		}
		actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
		if err != nil {
			return false, err
		}
		for _, action := range actions {
			if !affected[scopes[action.AssetID]] || action.Status == execution.ActionSucceeded {
				continue
			}
			if action.Status == execution.ActionIntentPersisted && action.ResumeStatus == "" && action.FailedFrom == "" && action.ProviderRequestID == "" && action.ProviderOperationID == "" && len(action.ProviderResult) == 0 && len(action.PreflightEvidence) == 0 {
				continue
			}
			return false, fmt.Errorf("%w: legacy Router ordering has an unsettled native action", persistence.ErrConflict)
		}
	}
	task.Steps = steps
	return true, nil
}

// Older tasks persisted a collection suffix. Coordinate them with new project
// reservations without rewriting their frozen action or evidence.
func monitoringProjectScope(scope string) string {
	parts := strings.Split(scope, "/")
	if len(parts) == 8 && parts[1] == "gcp" && parts[2] == "" && parts[3] == "" && parts[4] == "monitoring.googleapis.com" && parts[5] == "projects" {
		switch parts[7] {
		case "alertPolicies", "notificationChannels", "groups", "dashboards", "uptimeCheckConfigs":
			return strings.Join(parts[:7], "/")
		}
	}
	return scope
}
