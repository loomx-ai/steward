package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"

	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const poolTestID = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/storagePools/pool"
const poolDiskID = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/disk"

type poolCleanupScenario struct {
	pool, disk map[string]any
	writes     []string
	hook       func(*http.Request) (*http.Response, bool)
}

func newPoolCleanupScenario() *poolCleanupScenario {
	pool := storagePoolFixture("us-central1-a", "pool")
	disk := storagePoolDiskFixture("us-central1-a", "disk")
	disk["selfLink"], disk["kind"], disk["id"], disk["storagePool"] = disk["disk"], "compute#disk", "101", pool["selfLink"]
	return &poolCleanupScenario{pool: pool, disk: disk}
}
func (s *poolCleanupScenario) transport(t *testing.T) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if s.hook != nil {
			if response, ok := s.hook(req); ok {
				return response, nil
			}
		}
		var data map[string]any
		path := req.URL.Path
		switch {
		case strings.HasSuffix(path, "/aggregated/storagePools"):
			data = map[string]any{}
			if s.pool != nil {
				data["items"] = map[string]any{"zones/us-central1-a": map[string]any{"storagePools": []any{s.pool}}}
			}
		case strings.HasSuffix(path, "/aggregated/disks"):
			data = map[string]any{}
			if s.disk != nil {
				data["items"] = map[string]any{"zones/us-central1-a": map[string]any{"disks": []any{s.disk}}}
			}
		case strings.HasSuffix(path, "/storagePools/pool/listDisks"):
			if s.pool == nil {
				return apiResponse(req, 404, `{"error":{"code":404}}`), nil
			}
			data = map[string]any{"kind": "compute#storagePoolListDisks"}
			if s.disk != nil {
				data["items"] = []any{s.disk}
			}
		case strings.Contains(path, "/operations/"):
			data = map[string]any{"kind": "compute#operation", "name": last(path), "status": "DONE"}
		case strings.HasSuffix(path, "/storagePools/pool"), strings.HasSuffix(path, "/disks/disk"):
			pool := strings.Contains(path, "/storagePools/")
			data = s.disk
			if pool {
				data = s.pool
			}
			if data == nil {
				return apiResponse(req, 404, `{"error":{"code":404}}`), nil
			}
			if req.Method == "DELETE" {
				if pool && s.disk != nil {
					t.Fatal("pool deleted before disk")
				}
				if req.URL.Query().Get("requestId") == "" {
					t.Fatal("missing native request UUID")
				}
				s.writes = append(s.writes, path)
				target, uid := data["selfLink"], data["id"]
				if pool {
					s.pool = nil
				} else {
					s.disk = nil
				}
				data = map[string]any{"kind": "compute#operation", "name": "delete-" + last(path), "status": "RUNNING", "operationType": "delete", "targetLink": target, "targetId": uid}
			}
		default:
			t.Fatal("unexpected pool cleanup request", req.Method, req.URL)
		}
		raw, _ := json.Marshal(data)
		return apiResponse(req, 200, string(raw)), nil
	}
}
func poolCleanupReviewed(t *testing.T, s *poolCleanupScenario) (*Runtime, []asset.Asset) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	values := []asset.Asset{}
	for _, kind := range []string{storagePoolType, "compute.googleapis.com/Disk"} {
		batch, err := r.List(t.Context(), productRequest(r, kind, "us-central1"))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			value := asset.Asset{ID: asset.AssetID(last(item.NativeID)), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Name: item.Name, Location: item.Location}
			if item.Actionable != nil && *item.Actionable {
				value.Capabilities = asset.CapabilitySet{asset.CapabilityActionable}
			}
			values = append(values, value)
		}
	}
	return r, values
}
func poolRequest(values []asset.Asset) contracts.ActionRequest {
	return contracts.ActionRequest{Asset: values[0], Action: "delete", IdempotencyKey: "pool-delete"}
}

func TestStoragePoolOrderedCleanupAndRestart(t *testing.T) {
	s := newPoolCleanupScenario()
	r, values := poolCleanupReviewed(t, s)
	contributor, err := r.ComputeLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c, err := contributor.Contribute(t.Context(), "scope", values)
	if err != nil || len(c.Relationships) != 1 || len(c.Bindings) != 0 || len(c.Unresolved) != 0 {
		t.Fatal(c, err)
	}
	blocked, err := plan.Solve(plan.Input{Assets: values, Relationships: c.Relationships, ResolvedAssetIDs: []asset.AssetID{"pool"}})
	if err != nil || len(blocked.Blockers) == 0 {
		t.Fatal("unselected disks silently deleted", blocked, err)
	}
	result, err := plan.Solve(plan.Input{Assets: values, Relationships: c.Relationships, ResolvedAssetIDs: []asset.AssetID{"pool", "disk"}})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 {
		t.Fatal("pool plan", result, err)
	}
	var diskStep, poolStep plan.CleanupTaskStep
	for _, step := range result.Steps {
		if step.AssetID == "disk" {
			diskStep = step
		}
		if step.AssetID == "pool" {
			poolStep = step
		}
	}
	if !slices.Contains(poolStep.DependsOn, diskStep.ID) {
		t.Fatal("missing disk-before-pool order", result)
	}
	prerequisites, err := plan.RequiredDeletions(poolStep)
	if err != nil || len(prerequisites) != 1 || prerequisites[0].AssetID != diskStep.AssetID || prerequisites[0].StepID != diskStep.ID {
		t.Fatal("missing reviewed disk prerequisite", prerequisites, err)
	}
	request := poolRequest(values)
	driver, err := r.ResolveAction(t.Context(), "connection", values[0])
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(t.Context(), request)
	if err != nil || check.Allowed {
		t.Fatal("nonempty pool allowed", check, err)
	}
	disk, err := r.ResolveAction(t.Context(), "connection", values[1])
	if err != nil {
		t.Fatal(err)
	}
	diskRequest := contracts.ActionRequest{Asset: values[1], Action: "delete", IdempotencyKey: "disk-delete"}
	receipt, err := disk.Execute(t.Context(), diskRequest)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := disk.Wait(t.Context(), diskRequest, receipt)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: values[1], ControllerID: values[0].ID, Delete: true}}
	receipt, err = driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(receipt)
	var restored contracts.ActionResult
	_ = json.Unmarshal(encoded, &restored)
	fresh := protocolRuntime(t, s.transport(t))
	driver, err = fresh.ResolveAction(t.Context(), "connection", values[0])
	if err != nil {
		t.Fatal(err)
	}
	wait, err = driver.Wait(t.Context(), request, restored)
	if err != nil || !wait.Done {
		t.Fatal("pool restart", wait, err)
	}
	request.ExecutionResult = &restored
	read, err := driver.Readback(t.Context(), request)
	if err != nil || read.Exists {
		t.Fatal(read, err)
	}
	if len(s.writes) != 2 || !strings.Contains(s.writes[0], "/disks/") || !strings.Contains(s.writes[1], "/storagePools/") {
		t.Fatal(s.writes)
	}
}

func TestStoragePoolCleanupFailsClosed(t *testing.T) {
	for _, mode := range []string{"changed-capacity", "changed-sharing", "replacement", "not-ready", "protected", "new-member", "omitted-live-member", "member-denied", "parent-absent-disk-live", "foreign-parameter", "wrong-connection", "forged-proof", "forged-member", "retained-impact", "extra-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s := newPoolCleanupScenario()
			r, values := poolCleanupReviewed(t, s)
			request := poolRequest(values)
			driver, _ := r.ResolveAction(t.Context(), "connection", values[0])
			savedDisk := s.disk
			s.disk = nil
			switch mode {
			case "changed-capacity":
				s.pool["poolProvisionedCapacityGb"] = "40960"
			case "changed-sharing":
				s.pool["shareSettings"] = map[string]any{"projectMap": map[string]any{"other": map[string]any{"projectId": "other"}}}
			case "replacement":
				s.pool["id"] = "new"
			case "not-ready":
				s.pool["state"] = "CREATING"
			case "protected":
				s.pool["labels"] = map[string]any{"do-not-delete": "true"}
			case "new-member":
				s.disk = savedDisk
			case "omitted-live-member":
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/disks/disk") {
						data, _ := json.Marshal(savedDisk)
						return apiResponse(req, 200, string(data)), true
					}
					return nil, false
				}
			case "member-denied":
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/disks/disk") {
						return apiResponse(req, 403, `{"error":{"code":403}}`), true
					}
					return nil, false
				}
			case "parent-absent-disk-live":
				s.pool = nil
				s.disk = savedDisk
			case "foreign-parameter":
				request.Parameters = map[string]any{"force": true}
			case "wrong-connection":
				request.Asset.Identity.ConnectionID = "other"
			case "forged-proof":
				request.Asset.Normalized[poolSnapshotKey] = "bad"
			case "forged-member":
				request.Asset.Normalized[poolMembersKey] = "[]"
			case "retained-impact":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: values[1], ControllerID: values[0].ID}}
			case "extra-prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: values[0], ControllerID: values[0].ID, Delete: true}}
			}
			_, err := driver.Execute(t.Context(), request)
			if err == nil || len(s.writes) != 0 {
				t.Fatal("unsafe pool deletion", mode, err, s.writes)
			}
		})
	}
}

func TestStoragePoolCleanupGraphRequiresRefresh(t *testing.T) {
	for _, mode := range []string{"missing-disk", "changed-disk", "changed-pool", "changed-members", "missing-parent"} {
		t.Run(mode, func(t *testing.T) {
			s := newPoolCleanupScenario()
			r, values := poolCleanupReviewed(t, s)
			switch mode {
			case "missing-disk":
				values = values[:1]
			case "changed-disk":
				values[1].Normalized["creationTimestamp"] = "new"
			case "changed-pool":
				s.pool["description"] = "new"
			case "changed-members":
				s.disk = nil
			case "missing-parent":
				s.pool = nil
			}
			contributor, _ := r.ComputeLifecycle(context.Background(), "connection")
			c, err := contributor.Contribute(t.Context(), "scope", values)
			if err != nil || len(c.Unresolved) == 0 || !c.Unresolved[0].BlocksCleanup || len(c.Bindings) != 0 {
				t.Fatal(c, err)
			}
		})
	}
}

func TestStoragePoolNativeWaitAndReadbackBoundaries(t *testing.T) {
	for _, mode := range []string{"surviving-disk", "parent-still-live", "parent-replaced", "running", "expired-operation", "wrong-target", "wrong-operation", "wrong-kind", "corrupt-receipt", "foreign-operation", "alias-link", "operation-error", "malformed-error"} {
		t.Run(mode, func(t *testing.T) {
			s := newPoolCleanupScenario()
			r, values := poolCleanupReviewed(t, s)
			request := poolRequest(values)
			oldDisk, oldPool := s.disk, s.pool
			s.disk = nil
			driver, _ := r.ResolveAction(t.Context(), "connection", values[0])
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "surviving-disk":
				s.disk = oldDisk
			case "parent-still-live":
				s.pool = oldPool
				s.pool["state"] = "DELETING"
			case "parent-replaced":
				s.pool = oldPool
				s.pool["id"] = "999"
			case "corrupt-receipt":
				receipt.Data["storage_pool_receipt"] = "wrong"
			case "foreign-operation":
				receipt.ProviderOperationID = strings.Replace(receipt.ProviderOperationID, "sample-project", "other-project", 1)
			default:
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if !strings.Contains(req.URL.Path, "/operations/") {
						return nil, false
					}
					if mode == "expired-operation" {
						return apiResponse(req, 404, `{"error":{"code":404}}`), true
					}
					data := map[string]any{"kind": "compute#operation", "name": "delete-pool", "status": "DONE"}
					switch mode {
					case "running":
						data["status"] = "RUNNING"
					case "wrong-target":
						data["targetId"] = "another"
					case "wrong-operation":
						data["name"] = "other"
					case "wrong-kind":
						data["kind"] = "different"
					case "alias-link":
						data["selfLink"] = strings.Replace(receipt.ProviderOperationID, "https://compute.googleapis.com/", "https://www.googleapis.com/", 1)
					case "malformed-error":
						data["error"] = map[string]any{}
					case "operation-error":
						data["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED", "message": "PRIVATE_FAILURE"}}}
					}
					raw, _ := json.Marshal(data)
					return apiResponse(req, 200, string(raw)), true
				}
			}
			wait, err := driver.Wait(t.Context(), request, receipt)
			succeeds := mode == "expired-operation" || mode == "alias-link"
			waiting := mode == "surviving-disk" || mode == "parent-still-live" || mode == "running"
			if succeeds && (err != nil || !wait.Done) || waiting && (err != nil || wait.Done) || !succeeds && !waiting && err == nil {
				t.Fatal(mode, wait, err)
			}
			if mode == "corrupt-receipt" {
				request.ExecutionResult = &receipt
				if _, err := driver.Readback(t.Context(), request); err == nil {
					t.Fatal("corrupt receipt readback succeeded")
				}
			}
		})
	}
}

func TestStoragePoolUnsupportedCleanupAndNativeRejection(t *testing.T) {
	for _, mode := range []string{"exapool", "unknown-type", "foreign-member", "future-reservation-rejection"} {
		t.Run(mode, func(t *testing.T) {
			s := newPoolCleanupScenario()
			attempted := false
			switch mode {
			case "exapool":
				s.pool["exapoolProvisionedCapacityGb"] = map[string]any{"readOptimized": "1024"}
			case "unknown-type":
				s.pool["storagePoolType"] = "future-pool"
			case "foreign-member":
				s.disk["disk"] = strings.Replace(text(s.disk["disk"]), "sample-project", "consumer-project", 1)
			}
			r := protocolRuntime(t, s.transport(t))
			batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
			if err != nil {
				t.Fatal(err)
			}
			item := batch.Items[0]
			root := asset.Asset{ID: "pool", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: storagePoolType, NativeID: poolTestID}, Normalized: item.Normalized}
			request := contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: "protected-pool"}
			driver, _ := r.ResolveAction(t.Context(), "connection", root)
			if mode == "future-reservation-rejection" {
				s.disk = nil
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						attempted = true
						if req.URL.Query().Get("force") != "" {
							t.Fatal("forced deletion")
						}
						return apiResponse(req, 400, `{"error":{"code":400,"errors":[{"reason":"STORAGE_POOL_DELETE_HAS_ACTIVE_FR"}]}}`), true
					}
					return nil, false
				}
			} else if item.Actionable == nil || *item.Actionable {
				t.Fatal("protected pool actionable", mode)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(s.writes) != 0 {
				t.Fatal("native protection bypassed", mode, err)
			}
			if mode == "future-reservation-rejection" && !attempted {
				t.Fatal("native future reservation rejection was not exercised")
			}
		})
	}
}

func TestStoragePoolSupportedTypeFormsDeleteEmptyPools(t *testing.T) {
	for _, value := range []string{"hyperdisk-balanced", "hyperdisk-throughput", "projects/sample-project/zones/us-central1-a/storagePoolTypes/hyperdisk-balanced"} {
		t.Run(value, func(t *testing.T) {
			s := newPoolCleanupScenario()
			s.disk = nil
			s.pool["storagePoolType"] = value
			r, assets := poolCleanupReviewed(t, s)
			if len(assets) != 1 || !assets[0].Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("supported empty pool not actionable", assets)
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", assets[0])
			request := poolRequest(assets)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil || len(s.writes) != 1 {
				t.Fatal(receipt, err, s.writes)
			}
			wait, err := driver.Wait(t.Context(), request, receipt)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
		})
	}
}
