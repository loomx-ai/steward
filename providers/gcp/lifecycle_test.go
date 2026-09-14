package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func diskAsset(id, kind, path string) asset.Asset {
	return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "gcp-connection", Partition: "google-cloud", NativeType: kind, NativeID: "//compute.googleapis.com/projects/sample-project/" + path, ScopeKey: "us-central1"}, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: map[string]any{"project_id": "sample-project", "project_number": "123456"}}
}

func diskVM() asset.Asset {
	vm := diskAsset("vm", instanceType, "zones/us-central1-a/instances/web")
	vm.Normalized["disks"] = []any{
		map[string]any{"source": "https://www.googleapis.com/compute/v1/projects/123456/zones/us-central1-a/disks/boot", "deviceName": "boot", "boot": true, "autoDelete": true},
		map[string]any{"source": "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/disks/data", "deviceName": "data", "boot": false, "autoDelete": true},
		map[string]any{"source": "https://www.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/disks/keep", "deviceName": "keep", "autoDelete": false},
		map[string]any{"type": "SCRATCH", "deviceName": "local", "autoDelete": true},
	}
	return vm
}

func diskAssets() []asset.Asset {
	return []asset.Asset{diskVM(), diskAsset("boot", "compute.googleapis.com/Disk", "zones/us-central1-a/disks/boot"), diskAsset("data", "compute.googleapis.com/RegionDisk", "regions/us-central1/disks/data"), diskAsset("keep", "compute.googleapis.com/Disk", "zones/us-central1-a/disks/keep")}
}

func TestInstanceDisksPlansNativeCascadeRetentionAndOrdering(t *testing.T) {
	for _, retained := range []string{"", "boot", "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/boot"} {
		t.Run(retained, func(t *testing.T) {
			assets := diskAssets()
			assets[2].Identity.ScopeKey = "regional-disk-scope"
			contribution, err := NewInstanceDisks().Contribute(context.Background(), "scope", assets)
			if err != nil || len(contribution.Bindings) != 2 || len(contribution.Relationships) != 3 || len(contribution.Unresolved) != 0 {
				t.Fatalf("contribution=%+v err=%v", contribution, err)
			}
			for _, binding := range contribution.Bindings {
				if binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
					t.Fatalf("non-authoritative cascade: %+v", binding)
				}
			}
			input := plan.Input{CleanupTaskID: "cleanup", Assets: assets, ResolvedAssetIDs: []asset.AssetID{"vm", "boot", "data", "keep"}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
			if retained != "" {
				input.RequestOptions = map[asset.AssetID]map[string]any{"vm": {"retain_resources": []string{retained}}}
			}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 2 {
				t.Fatalf("plan=%+v err=%v", result, err)
			}
			var vmStep plan.StepID
			for _, step := range result.Steps {
				if step.AssetID == "vm" {
					vmStep = step.ID
				}
			}
			for _, step := range result.Steps {
				if step.AssetID == "keep" && (len(step.DependsOn) != 1 || step.DependsOn[0] != vmStep || step.Action != "delete") {
					t.Fatalf("retained disk must detach before direct cleanup: %+v", step)
				}
			}
			for _, impact := range result.ImpactItems {
				expected := plan.ExpectedDelegatedDelete
				if impact.AssetID == "boot" && retained != "" {
					expected = plan.ExpectedRetainExplicit
				}
				if impact.Expected != expected {
					t.Fatalf("wrong impact: %+v", impact)
				}
			}
		})
	}
}

func TestInstanceDiskDiscoveryRejectsForeignAmbiguousOrMalformedReferences(t *testing.T) {
	for _, mode := range []string{"foreign-connection", "foreign-project", "missing", "duplicate", "bad-auto-delete", "bad-shape"} {
		t.Run(mode, func(t *testing.T) {
			assets := diskAssets()
			wantError := false
			switch mode {
			case "foreign-connection":
				assets[1].Identity.ConnectionID = "another"
			case "foreign-project":
				object(array(assets[0].Normalized["disks"])[0])["source"] = "https://www.googleapis.com/compute/v1/projects/another-project/zones/us-central1-a/disks/boot"
				wantError = true
			case "missing":
				assets = append(assets[:1], assets[2:]...)
			case "duplicate":
				assets = append(assets, assets[1])
				wantError = true
			case "bad-auto-delete":
				object(array(assets[0].Normalized["disks"])[0])["autoDelete"] = "true"
				wantError = true
			case "bad-shape":
				assets[0].Normalized["disks"] = map[string]any{}
				wantError = true
			}
			result, err := NewInstanceDisks().Contribute(context.Background(), "scope", assets)
			if (err != nil) != wantError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !wantError && (len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != diskAssets()[1].Identity.NativeID) {
				t.Fatalf("missing reference lost: %+v", result)
			}
		})
	}
}

func diskActionRequest(retain bool) contracts.ActionRequest {
	assets := diskAssets()
	return contracts.ActionRequest{Asset: assets[0], Action: "delete", IdempotencyKey: "cleanup-disk-job", LifecycleImpacts: []contracts.ActionImpact{
		{Asset: assets[1], ControllerID: assets[0].ID, Delete: !retain}, {Asset: assets[2], ControllerID: assets[0].ID, Delete: true},
	}}
}

func TestInstanceDeletionRetainsDiskAndResumesPreparationAfterRestart(t *testing.T) {
	request := diskActionRequest(true)
	live := diskVM().Normalized
	live["deletionProtection"] = true
	mutations, polls := []string{}, map[string]int{}
	deleted := false
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" || r.Method == "DELETE" {
			op := last(r.URL.Path)
			mutations = append(mutations, op)
			suffix := ""
			switch op {
			case "setDiskAutoDelete":
				if r.URL.Query().Get("deviceName") != "boot" || r.URL.Query().Get("autoDelete") != "false" {
					t.Fatalf("wrong retention request: %s", r.URL)
				}
				suffix = ":retain:boot"
			case "setDeletionProtection":
				if object(array(live["disks"])[0])["autoDelete"] != false || r.URL.Query().Get("deletionProtection") != "false" {
					t.Fatal("protection changed before retaining disk")
				}
				suffix = ":deletion-protection"
			case "web":
				if live["deletionProtection"] != false || object(array(live["disks"])[0])["autoDelete"] != false {
					t.Fatal("instance deleted before preparation")
				}
				deleted = true
			default:
				t.Fatalf("unexpected mutation %s", r.URL)
			}
			if r.URL.Query().Get("requestId") != googleRequestID(request.IdempotencyKey+suffix) {
				t.Fatal("operation idempotency keys not isolated")
			}
			return apiResponse(r, 200, `{"name":"`+op+`"}`), nil
		}
		if strings.Contains(r.URL.Path, "/operations/") {
			op := last(r.URL.Path)
			polls[op]++
			if polls[op] == 1 {
				return apiResponse(r, 200, `{"status":"RUNNING"}`), nil
			}
			if op == "setDiskAutoDelete" {
				object(array(live["disks"])[0])["autoDelete"] = false
			}
			if op == "setDeletionProtection" {
				live["deletionProtection"] = false
			}
			return apiResponse(r, 200, `{"status":"DONE"}`), nil
		}
		if deleted {
			if strings.HasSuffix(r.URL.Path, "/disks/boot") {
				return apiResponse(r, 200, `{}`), nil // Explicitly retained disk survives.
			}
			return apiResponse(r, 404, `{}`), nil
		}
		body, _ := json.Marshal(live)
		return apiResponse(r, 200, string(body)), nil
	})
	newDriver := func() *action {
		return protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", transport)
	}
	result, err := newDriver().Execute(context.Background(), request)
	if err != nil || text(result.Data["phase"]) != "prepare_instance" {
		t.Fatalf("execute=%+v %v", result, err)
	}
	// Mimic persistence: ProviderOperationID remains the first operation while
	// WaitResult.Data is saved across independently reconstructed drivers.
	done := false
	for i := 0; i < 12; i++ {
		payload, _ := json.Marshal(result)
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		wait, err := newDriver().Wait(context.Background(), request, result)
		if err != nil {
			t.Fatal(err)
		}
		if wait.Data != nil {
			result.Data = wait.Data
		}
		if wait.Done {
			done = true
			break
		}
	}
	if !done || fmt.Sprint(mutations) != "[setDiskAutoDelete setDeletionProtection web]" || polls["web"] != 2 {
		t.Fatalf("done=%v mutations=%v polls=%v", done, mutations, polls)
	}
}

func TestInstanceDeleteBlocksUnreviewedDisksAndChangedAttachments(t *testing.T) {
	for _, mode := range []string{"missing-impact", "new-disk", "changed-device", "disabled-autodelete", "enabled-autodelete", "cross-connection", "missing-snapshot"} {
		t.Run(mode, func(t *testing.T) {
			request, live := diskActionRequest(false), diskVM().Normalized
			switch mode {
			case "missing-impact":
				request.LifecycleImpacts = nil
			case "new-disk":
				live["disks"] = append(array(live["disks"]), map[string]any{"source": "https://www.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/disks/new", "deviceName": "new", "autoDelete": true})
			case "changed-device":
				object(array(live["disks"])[0])["deviceName"] = "replacement"
			case "disabled-autodelete":
				object(array(live["disks"])[0])["autoDelete"] = false
			case "enabled-autodelete":
				object(array(live["disks"])[2])["autoDelete"] = true
			case "cross-connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "another"
			case "missing-snapshot":
				request.Asset.Normalized = nil
			}
			a := protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("mutated unreviewed attachment")
				}
				body, _ := json.Marshal(live)
				return apiResponse(r, 200, string(body)), nil
			})
			_, err := a.Execute(context.Background(), request)
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) {
				t.Fatalf("expected protected result, got %v", err)
			}
		})
	}
}

func TestInstancePreparationFailureNeverDeletesVM(t *testing.T) {
	for _, fail := range []string{"permission", "operation", "drift"} {
		t.Run(fail, func(t *testing.T) {
			request, live := diskActionRequest(true), diskVM().Normalized
			a := protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", func(r *http.Request) (*http.Response, error) {
				if r.Method == "DELETE" {
					t.Fatal("deleted VM after preparation failure")
				}
				if r.Method == "POST" {
					if fail == "permission" {
						return apiResponse(r, 403, `{"error":{"status":"PERMISSION_DENIED"}}`), nil
					}
					return apiResponse(r, 200, `{"name":"prepare"}`), nil
				}
				if strings.Contains(r.URL.Path, "/operations/") {
					if fail == "operation" {
						return apiResponse(r, 200, `{"status":"DONE","error":{"errors":[{"code":"FAILED"}]}}`), nil
					}
					object(array(live["disks"])[1])["deviceName"] = "changed"
					return apiResponse(r, 200, `{"status":"DONE"}`), nil
				}
				body, _ := json.Marshal(live)
				return apiResponse(r, 200, string(body)), nil
			})
			result, err := a.Execute(context.Background(), request)
			if err == nil {
				_, err = a.Wait(context.Background(), request, result)
			}
			if err == nil {
				t.Fatal("preparation failure ignored")
			}
		})
	}
}

func TestInstanceWaitsForRetentionReadbackWithoutRepeatingMutation(t *testing.T) {
	request, live := diskActionRequest(true), diskVM().Normalized
	updates, deletes := 0, 0
	a := protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			updates++
			return apiResponse(r, 200, `{"name":"retain"}`), nil
		}
		if r.Method == "DELETE" {
			deletes++
			return apiResponse(r, 200, `{"name":"delete"}`), nil
		}
		if strings.Contains(r.URL.Path, "/operations/") {
			return apiResponse(r, 200, `{"status":"DONE"}`), nil
		}
		body, _ := json.Marshal(live)
		return apiResponse(r, 200, string(body)), nil
	})
	result, err := a.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := a.Wait(context.Background(), request, result)
	if err != nil || wait.Done || updates != 1 || deletes != 0 {
		t.Fatalf("deletion advanced without retention readback: wait=%+v updates=%d deletes=%d err=%v", wait, updates, deletes, err)
	}
	object(array(live["disks"])[0])["autoDelete"] = false
	wait, err = a.Wait(context.Background(), request, result)
	if err != nil || wait.Done || text(wait.Data["phase"]) != "delete" || updates != 1 || deletes != 1 {
		t.Fatalf("did not advance after readback: %+v updates=%d deletes=%d err=%v", wait, updates, deletes, err)
	}
}
