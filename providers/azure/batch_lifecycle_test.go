package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchPlan(t *testing.T, r *Runtime, assets []asset.Asset, target asset.Asset) plan.Result {
	t.Helper()
	contributor, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(t.Context(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatal("Batch contribution", err, contribution.Unresolved)
	}
	result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func batchCascadeGone(s *batchScenario) {
	for id := range s.records {
		for removed, gone := range s.gone {
			if gone && strings.HasPrefix(id, removed+"/") {
				s.gone[id] = true
			}
		}
		if strings.HasPrefix(id, "/pools/") {
			parts := strings.Split(id, "/")
			if len(parts) > 2 && s.arm.gone[s.account+"/pools/"+parts[2]] {
				s.gone[id] = true
			}
		}
	}
	for id, raw := range s.arm.records {
		if s.arm.gone[s.account] && strings.HasPrefix(id, s.account+"/") {
			s.arm.gone[id] = true
		}
		if raw["type"] == batchApplicationType {
			version := text(object(raw["properties"])["defaultVersion"])
			if version != "" && s.arm.gone[id+"/versions/"+strings.ToLower(version)] {
				delete(object(raw["properties"]), "defaultVersion")
			}
		}
	}
}

func TestBatchReviewedAccountCleanup(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	account := cdnAsset(t, assets, batchAccountType)
	solved := batchPlan(t, r, assets, account)
	if len(solved.Blockers) != 0 || len(solved.Steps) != 7 || len(solved.ImpactItems) != 3 {
		t.Fatal("Batch account did not separate direct cleanup and native cascades", solved.Blockers, len(solved.Steps), len(solved.ImpactItems))
	}
	for _, step := range solved.Steps {
		value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
		request := servicePlanRequest(solved, assets, value)
		driver, err := r.ResolveAction(t.Context(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("Batch native delete", value.Identity.NativeType, err)
		}
		if value.Identity.NativeType == batchJobType || value.Identity.NativeType == batchPoolType || value.Identity.NativeType == batchAccountType {
			waiting, err := driver.Wait(t.Context(), request, result)
			if err != nil || waiting.Done {
				t.Fatal("Batch parent absence concealed a remaining child", value.Identity.NativeType, waiting, err)
			}
		}
		batchCascadeGone(s)
		payload, _ := json.Marshal(request)
		json.Unmarshal(payload, &request)
		payload, _ = json.Marshal(result)
		json.Unmarshal(payload, &result)
		driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		waiting, err := driver.Wait(t.Context(), request, result)
		if err != nil || !waiting.Done {
			t.Fatal("resumed Batch absence check", value.Identity.NativeType, waiting, err)
		}
	}
	if len(s.arm.deletes) != 5 || len(s.writes) != 2 {
		t.Fatal("wrong native Batch delete count", s.arm.deletes, s.writes)
	}
}

func TestBatchSharedResourcesRequireExplicitSelection(t *testing.T) {
	_, r, assets := newBatchScenario(t)
	for _, kind := range []string{batchPoolType, batchApplicationType, batchPackageType} {
		target := cdnAsset(t, assets, kind)
		result := batchPlan(t, r, assets, target)
		if len(result.Blockers) == 0 {
			t.Fatal("Batch shared resource silently removed its consumers", kind)
		}
		for _, step := range result.Steps {
			value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
			if value.Identity.NativeType == batchJobType || value.Identity.NativeType == batchScheduleType {
				t.Fatal("Batch shared resource automatically selected a retained consumer")
			}
		}
	}
}

func TestBatchJobAndTaskDeletionAreConditional(t *testing.T) {
	for _, kind := range []string{batchJobType, batchTaskType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			target := cdnAsset(t, assets, kind)
			result := batchPlan(t, r, assets, target)
			if len(result.Blockers) != 0 {
				t.Fatal(result.Blockers)
			}
			request := servicePlanRequest(result, assets, target)
			seen := false
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && req.URL.Scheme+"://"+req.URL.Host+req.URL.Path == target.Identity.NativeID {
					seen = true
					if req.Header.Get("If-Match") != text(s.records[req.URL.Path]["eTag"]) || req.Header.Get("Client-Request-Id") == "" || req.URL.Query().Has("force") {
						t.Fatal("Batch delete lost its native condition or forced cleanup")
					}
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", target)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil || !seen {
				t.Fatal("Batch conditional delete", err)
			}
			batchCascadeGone(s)
			if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
				t.Fatal("Batch conditional delete readback", wait, err)
			}
			before := len(s.writes)
			if _, err := driver.Execute(t.Context(), request); err != nil || len(s.writes) != before {
				t.Fatal("Batch data deletion was not idempotent", err)
			}
		})
	}
}

func TestBatchDeletionRejectsUndeclaredSuccessShapes(t *testing.T) {
	for _, test := range []struct {
		kind   string
		status int
		body   map[string]any
	}{
		{batchTaskType, 202, nil}, {batchTaskType, 204, nil},
		{batchJobType, 200, nil}, {batchNodeType, 204, nil},
		{batchPECType, 200, nil},
		{batchTaskType, 200, map[string]any{"status": "Succeeded"}},
	} {
		t.Run(test.kind+"/"+http.StatusText(test.status), func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			target := cdnAsset(t, assets, test.kind)
			solved := batchPlan(t, r, assets, target)
			writes := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" || req.Method == "POST" {
					writes++
					return jsonResponse(test.status, test.body, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", target)
			if _, err := driver.Execute(t.Context(), servicePlanRequest(solved, assets, target)); err == nil || writes != 1 {
				t.Fatal("unexpected native success shape was accepted", err, writes)
			}
		})
	}
}

func TestBatchNativeScheduleIndexAndAutoPoolOwnership(t *testing.T) {
	for _, keepAlive := range []bool{false, true} {
		t.Run(map[bool]string{false: "expires", true: "retained"}[keepAlive], func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			job := s.records["/jobs/jobid"]
			job["poolInfo"] = map[string]any{"autoPoolSpecification": map[string]any{"poolLifetimeOption": "job", "keepAlive": keepAlive}}
			s.lists["/jobschedules/schedule/jobs"] = []any{job}
			c, _ := r.resolve(t.Context(), "connection")
			account, _ := c.batchAccount(t.Context(), s.account)
			topology, err := c.batchTopology(t.Context(), account)
			if err != nil {
				t.Fatal(err)
			}
			member := topology.members[s.origin+"/jobs/jobid"]
			if member.parent != s.origin+"/jobschedules/schedule" || member.direct {
				t.Fatal("job name overrode its native schedule index")
			}
			pool := topology.members[s.account+"/pools/poolid"]
			if keepAlive && (pool.parent != s.account || !pool.direct) || !keepAlive && (pool.parent != member.id || pool.direct) {
				t.Fatal("auto pool retention changed its actual owner", pool.parent, pool.direct)
			}
			// Existing plan metadata must still reject the changed pool policy.
			if err := batchIncarnation(c, cdnAsset(t, assets, batchJobType), job); err == nil {
				t.Fatal("auto pool policy drift was ignored")
			}
		})
	}
}

func TestBatchLifecycleRejectsIncompleteOwnership(t *testing.T) {
	for _, mode := range []string{"missing_task", "foreign_schedule_job", "private_task_change", "protected_child", "unknown_child_state", "allocation_mode_changed", "multi_instance_settings_changed"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			job := cdnAsset(t, assets, batchJobType)
			solved := batchPlan(t, r, assets, job)
			request := servicePlanRequest(solved, assets, job)
			switch mode {
			case "missing_task":
				request.LifecycleImpacts = nil
			case "foreign_schedule_job":
				foreign := batchClone(s.records["/jobs/jobid"])
				foreign["url"] = "https://foreign.japaneast.batch.azure.com/jobs/jobid"
				s.lists["/jobschedules/schedule/jobs"] = []any{foreign}
			case "private_task_change":
				s.records["/jobs/jobid/tasks/taskid"]["commandLine"] = "changed"
			case "protected_child":
				s.records["/jobs/jobid/tasks/taskid"]["metadata"] = []any{map[string]any{"name": "steward/protected", "value": "true"}}
			case "unknown_child_state":
				s.records["/jobs/jobid/tasks/taskid"]["state"] = "futureState"
			case "allocation_mode_changed":
				job = cdnAsset(t, assets, batchNodeType)
				request = contracts.ActionRequest{Asset: job, Action: "delete"}
				object(s.arm.records[s.account]["properties"])["poolAllocationMode"] = "UserSubscription"
			case "multi_instance_settings_changed":
				s.records["/jobs/jobid/tasks/taskid"]["multiInstanceSettings"] = map[string]any{"numberOfInstances": 3}
			}
			driver, err := r.ResolveAction(t.Context(), "connection", job)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(s.writes)+len(s.arm.deletes) != 0 {
				t.Fatal("Batch lifecycle boundary allowed mutation", mode, err)
			}
		})
	}
}
