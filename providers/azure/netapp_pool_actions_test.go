package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type netappPoolFixture struct {
	*netappFixture
	id                            string
	volumes                       []string
	deletes, readFault, pollFault map[string]int
	operations                    map[string]string
	done                          map[string]bool
	failAfterDelete               bool
}

func newNetappPoolFixture(t *testing.T) *netappPoolFixture {
	base := newNetappFixture(t)
	pool := strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item"
	first := pool + "/volumes/item"
	second := pool + "/volumes/second"
	f := &netappPoolFixture{netappFixture: base, id: pool, volumes: []string{first, second}, deletes: map[string]int{}, readFault: map[string]int{}, pollFault: map[string]int{}, operations: map[string]string{}, done: map[string]bool{}}
	for id, raw := range base.objects {
		if id != first && !strings.HasPrefix(id, first+"/") {
			continue
		}
		clone := batchClone(raw)
		target := strings.Replace(id, first, second, 1)
		clone["id"] = target
		names := []string{}
		parts := strings.Split(target, "/")
		for i := 8; i < len(parts); i += 2 {
			names = append(names, parts[i])
		}
		clone["name"] = strings.Join(names, "/")
		if id == first {
			object(clone["properties"])["fileSystemId"] = testApplication
			object(clone["properties"])["creationToken"] = "second"
		}
		base.objects[target] = clone
	}
	base.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != f.id && path != first && path != second || len(q.URL.Query()) != 1 || q.URL.Query().Get("api-version") != netappVersion || q.ContentLength != 0 {
				t.Fatal("unreviewed pool mutation", q.Method, q.URL)
			}
			if path == f.id {
				for _, id := range f.volumes {
					if !f.missing[id] {
						t.Fatal("pool DELETE before volume prerequisites", id)
					}
				}
			}
			f.deletes[path]++
			if f.failAfterDelete {
				f.readFault[path] = 503
			}
			op := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.operations)+1)
			f.operations[op] = path
			callback := func(role string) string {
				u, _ := url.Parse(netappTestPollURL(role))
				u.Path = strings.TrimSuffix(u.Path, "/"+last(u.Path)) + "/" + op
				return u.String()
			}
			h := http.Header{}
			h.Set("Azure-AsyncOperation", callback("status_url"))
			h.Set("Location", callback("result_url"))
			return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if f.readFault[path] != 0 {
			return jsonResponse(f.readFault[path], nil, nil), true
		}
		if strings.Contains(path, "/operationresults/") {
			target := f.operations[last(path)]
			if target == "" {
				t.Fatal("unknown pool operation", q.URL)
			}
			if f.pollFault[target] != 0 {
				return jsonResponse(f.pollFault[target], nil, nil), true
			}
			if q.URL.Query().Get("operationResultResponseType") == "Location" {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
			}
			state := "Deleting"
			if f.done[target] {
				state = "Succeeded"
			}
			body := netappTestStatus(target, state)
			body["id"], body["name"] = q.URL.Path, last(q.URL.Path)
			return jsonResponse(200, body, nil), true
		}
		return fleetGraphEmptyIndexes(t, q)
	}
	return f
}
func (f *netappPoolFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	out := []asset.Asset{}
	for _, kind := range netappResources {
		req := netappRequest(f.runtime, kind.kind)
		req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
		page, err := f.runtime.List(t.Context(), req)
		if err != nil {
			t.Fatal(kind.kind, err)
		}
		for _, item := range page.Items {
			out = append(out, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized})
		}
	}
	return out
}
func (f *netappPoolFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	values := f.assets(t)
	req := contracts.ActionRequest{Action: "delete", IdempotencyKey: "pool-delete"}
	for _, v := range values {
		if v.Identity.NativeID == f.id {
			req.Asset = v
		}
	}
	for _, v := range values {
		if v.Identity.NativeType == netappVolumeType {
			req.PrerequisiteDeletions = append(req.PrerequisiteDeletions, contracts.ActionImpact{Asset: v, ControllerID: req.Asset.ID, Delete: true})
		}
	}
	return req
}
func (f *netappPoolFixture) finishVolume(id string) {
	for child := range f.objects {
		if child == id || strings.HasPrefix(child, id+"/") {
			f.missing[child] = true
		}
	}
	object(f.objects[f.id]["properties"])["utilizedThroughputMibps"] = json.Number("0")
	f.objects[f.id]["etag"] = "changed-by-volume-removal"
}
func TestNetappPoolDurableActionAndPrerequisites(t *testing.T) {
	f := newNetappPoolFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), req); err == nil || len(f.deletes) != 0 {
		t.Fatal("live volumes did not block", err)
	}
	for _, id := range f.volumes {
		f.finishVolume(id)
	}
	f.failAfterDelete = true
	result, err := driver.Execute(t.Context(), req)
	if err != nil || f.deletes[f.id] != 1 || result.Data["binding"] == nil {
		t.Fatal("pool ack not saved", result, err)
	}
	wire, _ := json.Marshal(result)
	if json.Unmarshal(wire, &result) != nil {
		t.Fatal("receipt serialization")
	}
	req.ExecutionResult = &result
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), req); err != nil || f.deletes[f.id] != 1 {
		t.Fatal("pool DELETE replay", err)
	}
	if _, err := driver.Readback(t.Context(), req); err == nil {
		t.Fatal("own read failure hidden")
	}
	f.readFault[f.id] = 0
	f.done[f.id] = true
	for i := 0; i < 2; i++ {
		wait, err := driver.Wait(t.Context(), req, result)
		if err != nil || wait.Done {
			t.Fatal("callback replaced own absence", wait, err)
		}
		result.Data = wait.Data
	}
	if result.Data["operation_done"] != true {
		t.Fatal("terminal native checkpoint missing")
	}
	req.ExecutionResult = &result
	wait, err := driver.Wait(t.Context(), req, result)
	if err != nil || wait.Done {
		t.Fatal("live pool closed", wait, err)
	}
	f.missing[f.id] = true
	wait, err = driver.Wait(t.Context(), req, result)
	if err != nil || !wait.Done {
		t.Fatal("pool own absence", wait, err)
	}
	f.missing[redisParentID(f.id)] = true
	if _, err := driver.Readback(t.Context(), req); err == nil {
		t.Fatal("missing account treated as pool absence")
	}
}
func TestNetappPoolChangedReviewAndNewVolume(t *testing.T) {
	for _, fault := range []string{"missing prerequisite", "retained prerequisite", "foreign prerequisite", "forged volume", "new volume", "pool UUID", "writable configuration", "unknown configuration", "parent", "lock tag", "receipt", "parameter"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappPoolFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range f.volumes {
				f.finishVolume(id)
			}
			switch fault {
			case "missing prerequisite":
				req.PrerequisiteDeletions = nil
			case "retained prerequisite":
				req.PrerequisiteDeletions[0].Delete = false
			case "foreign prerequisite":
				req.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "forged volume":
				req.PrerequisiteDeletions[0].Asset.Normalized[netappVolumeProof] = "forged"
			case "new volume":
				f.missing[f.volumes[0]] = false
			case "pool UUID":
				object(f.objects[f.id]["properties"])["poolId"] = testApplication
			case "writable configuration":
				object(f.objects[f.id]["properties"])["qosType"] = "Auto"
			case "unknown configuration":
				object(f.objects[f.id]["properties"])["unknown"] = true
			case "parent":
				object(f.objects[redisParentID(f.id)]["properties"])["unknown"] = true
			case "lock tag":
				f.objects[f.id]["tags"] = map[string]any{"steward:protected": "true"}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
			case "parameter":
				req.Parameters = map[string]any{"forceDelete": true}
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || len(f.deletes) != 0 {
				t.Fatal("changed pool context deleted", fault, err)
			}
		})
	}
}
func TestNetappPoolWorkerOrdersVolumesAndKeepsBackups(t *testing.T) {
	f := newNetappPoolFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(values) != 26 {
		t.Fatal("complete native graph", len(values))
	}
	var pool asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.id {
			pool = v
		}
	}
	if pool.ID == "" {
		t.Fatal("missing pool")
	}
	planner := cleanup.NewService(repo, registry)
	selector := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: pool.ID}}
	retained, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: selector, RequestOptions: map[asset.AssetID]map[string]any{pool.ID: {"retain_all_resources": true}}})
	if err != nil || len(retained.Task.Blockers) == 0 {
		t.Fatal("retained volumes allowed pool removal", retained, err)
	}
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: selector})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 3 || len(task.ImpactItems) != 6 {
		t.Fatal("pool prerequisite plan", task, err)
	}
	foundWarning := false
	for _, warning := range task.Task.Warnings {
		if warning.Code == plan.WarningNetappPoolDelete && warning.AssetID == pool.ID {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatal("missing pool destruction warning")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "pool-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	run := func(step plan.CleanupTaskStep, pending bool) execution.ActionAttempt {
		t.Helper()
		var job execution.Job
		for _, j := range jobs {
			if text(j.Payload["cleanup_task_step_id"]) == string(step.ID) {
				job = j
			}
		}
		if job.ID == "" {
			t.Fatal("missing step job")
		}
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		registry = providerruntime.NewRegistry()
		if err = registry.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err = registry.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			t.Fatal("worker restart", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(step.ID))
		if errors.Is(err, persistence.ErrNotFound) && pending && step.AssetID == pool.ID && len(f.deletes) == 0 {
			return execution.ActionAttempt{}
		}
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	var poolStep plan.CleanupTaskStep
	volSteps := []plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		if step.AssetID == pool.ID {
			poolStep = step
		} else {
			volSteps = append(volSteps, step)
		}
	}
	// The controller job must wait even when a caller invokes it before its jobs.
	run(poolStep, true)
	if len(f.deletes) != 0 {
		t.Fatal("pool ran before dependencies")
	}
	for _, step := range append(volSteps, poolStep) {
		id := f.id
		for _, v := range values {
			if v.ID == step.AssetID {
				id = v.Identity.NativeID
			}
		}
		f.failAfterDelete = true
		first := run(step, true)
		if first.ProviderResult["binding"] == nil || f.deletes[id] != 1 {
			t.Fatal("step receipt missing", first)
		}
		again := run(step, true)
		if again.ProviderResult["binding"] != first.ProviderResult["binding"] {
			t.Fatal("saved receipt lost")
		}
		f.failAfterDelete = false
		f.readFault[id] = 0
		f.done[id] = true
		run(step, true)
		terminal := run(step, true)
		if terminal.ProviderResult["operation_done"] != true {
			t.Fatal("native checkpoint missing")
		}
		if id == f.id {
			f.missing[id] = true
		} else {
			f.finishVolume(id)
		}
		run(step, true)
		run(step, false)
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != 17 || len(f.deletes) != 3 {
		t.Fatal("wrong pool removal scope", len(active), f.deletes, err)
	}
	backups := 0
	for _, v := range active {
		if v.Identity.NativeType == netappBackupType {
			backups++
		}
		if v.Identity.NativeID == f.id || strings.HasPrefix(v.Identity.NativeID, f.id+"/") {
			t.Fatal("pool child still active", v.Identity.NativeID)
		}
	}
	if backups != 2 {
		t.Fatal("pool cleanup removed retained backups")
	}
	for _, count := range f.deletes {
		if count != 1 {
			t.Fatal("replayed native delete")
		}
	}
}

func TestNetappPoolMemberReadBoundaries(t *testing.T) {
	for _, fault := range []string{"omitted live", "omitted absent", "own forbidden", "collection unavailable", "foreign hint", "region changed", "listed missing"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappPoolFixture(t)
			req := f.request(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			known := object(object(req.Asset.Normalized[netappPoolReview])["members"])
			target := f.volumes[0]
			previous := f.override
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, f.id+"/volumes") {
					if fault == "collection unavailable" {
						return jsonResponse(503, nil, nil), true
					}
					if strings.HasPrefix(fault, "omitted") || fault == "own forbidden" {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
				}
				if strings.EqualFold(q.URL.Path, target) {
					if fault == "own forbidden" {
						return jsonResponse(403, nil, nil), true
					}
					if fault == "listed missing" {
						return jsonResponse(404, nil, nil), true
					}
				}
				return previous(q)
			}
			switch fault {
			case "omitted absent":
				f.finishVolume(target)
			case "foreign hint":
				known[strings.Replace(target, testSubscription, testApplication, 1)] = known[target]
			case "region changed":
				f.objects[target]["location"] = "westus"
			}
			review, err := c.netappPoolBoundary(t.Context(), f.id, known)
			if strings.HasPrefix(fault, "omitted") {
				if err != nil {
					t.Fatal(err)
				}
				members := object(review["members"])
				if len(members) != 2 || object(members[target])["absent"] != (fault == "omitted absent") {
					t.Fatal("known own reads lost", members)
				}
			} else if err == nil {
				t.Fatal("incomplete pool review accepted", fault)
			}
			if len(f.deletes) != 0 {
				t.Fatal("read boundary mutated resources")
			}
		})
	}
}

func TestNetappPoolStaleGraphBlocks(t *testing.T) {
	for _, fault := range []string{"missing", "unsigned", "changed", "unreviewed"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappPoolFixture(t)
			req := f.request(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			values := []asset.Asset{}
			for _, v := range req.PrerequisiteDeletions {
				values = append(values, v.Asset)
			}
			switch fault {
			case "missing":
				values = values[1:]
			case "unsigned":
				values[0].Normalized[netappVolumeProof] = "forged"
			case "changed":
				values[0].Normalized["_netapp_configuration"] = "changed"
			case "unreviewed":
				values[0].Identity.NativeID = f.id + "/volumes/new"
			}
			contribution, err := c.netappPoolContribution(req.Asset, values)
			if err != nil {
				t.Fatal(err)
			}
			if len(contribution.Unresolved) == 0 {
				t.Fatal("stale graph allowed pool removal", fault)
			}
		})
	}
}
