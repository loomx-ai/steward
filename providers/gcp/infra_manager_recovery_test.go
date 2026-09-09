package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func infraRestart(t *testing.T, s *infraScenario, request contracts.ActionRequest, result contracts.ActionResult) (contracts.ActionDriver, contracts.ActionResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var resumed contracts.ActionResult
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatal(err)
	}
	driver, err := protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	return driver, resumed
}

func TestInfraManagerSettlingRecoveryAndAlreadyAbsent(t *testing.T) {
	for _, mode := range []string{"settle", "existing-delete", "already-absent", "delete-404-absent", "delete-404-live", "empty-deployment", "missing-physical", "historical-delete"} {
		t.Run(mode, func(t *testing.T) {
			s := newInfraScenario(t)
			if mode == "missing-physical" {
				delete(s.physical, infraTestNetwork)
			}
			if mode == "historical-delete" {
				s.resources[infraTestRevision+"/resources/network"]["intent"] = "DELETE"
			}
			name := infraTestDeployment
			if mode == "empty-deployment" {
				name = "projects/sample-project/locations/europe-west1/deployments/stack-a"
			}
			r, _, _, request := infraReviewed(t, s, name, nil)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			remove := func() {
				for id := range s.resources {
					if id == name || strings.HasPrefix(id, name+"/") {
						delete(s.resources, id)
					}
				}
				delete(s.physical, infraTestNetwork)
				delete(s.physical, infraTestBucket)
			}
			switch mode {
			case "settle":
				s.resources[name]["state"] = "UPDATING"
			case "existing-delete":
				s.resources[name]["state"] = "DELETING"
			case "already-absent":
				remove()
			case "delete-404-absent", "delete-404-live":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						if mode == "delete-404-absent" {
							remove()
						}
						return dataformResponse(req, 404, map[string]any{}), true
					}
					return nil, false
				}
			}
			result, err := driver.Execute(context.Background(), request)
			if mode == "delete-404-live" {
				if err == nil {
					t.Fatal("DELETE 404 accepted while reviewed resources still exist")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			driver, result = infraRestart(t, s, request, result)
			if mode == "settle" || mode == "existing-delete" {
				if len(s.writes) != 0 {
					t.Fatal("in-progress controller caused a competing delete")
				}
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatalf("in-progress state: %+v %v", wait, err)
				}
				if mode == "settle" {
					result.Data = wait.Data // The executor only replaces Data on Wait.
					s.resources[name]["state"] = "ACTIVE"
					wait, err = driver.Wait(context.Background(), request, result)
					if err != nil || wait.Done || len(s.writes) != 1 || wait.Data["operation"] == "" {
						t.Fatalf("settled controller did not start native delete: %+v %v", wait, err)
					}
					result.Data = wait.Data
					driver, result = infraRestart(t, s, request, result)
					if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done || len(s.writes) != 1 {
						t.Fatalf("restart lost delete operation: %+v %v", wait, err)
					}
					s.finish(text(result.Data["operation"]), false)
				} else {
					remove()
				}
			} else if result.ProviderOperationID != "" {
				s.finish(result.ProviderOperationID, false)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatalf("recovered final proof: %+v %v", wait, err)
			}
			if len(s.writes) > 1 {
				t.Fatal("recovery repeated native deletion")
			}
		})
	}
}

func TestInfraManagerTransitiveGKENodePoolCleanup(t *testing.T) {
	for _, mode := range []string{"delete", "abandon", "retained-vm-missing", "retained-vm-recreated", "retained-vm-delete-tampered"} {
		t.Run(mode, func(t *testing.T) {
			s, f := newInfraScenario(t), newGKEFixture(t)
			id := infraTestRevision + "/resources/node-pool"
			s.resources[id] = map[string]any{"name": id, "state": "RECONCILED", "intent": "CREATE", "terraformInfo": map[string]any{"address": "google_container_node_pool.workers", "type": "google_container_node_pool", "id": strings.TrimPrefix(f.pool.Identity.NativeID, "//container.googleapis.com/")}, "caiAssets": map[string]any{"container.googleapis.com/NodePool": map[string]any{"fullResourceName": f.pool.Identity.NativeID}}}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Host == "container.googleapis.com" || req.URL.Host == "compute.googleapis.com" && !strings.Contains(req.URL.Path, "/global/networks") {
					if req.Method != "GET" && !(req.Method == "POST" && (strings.HasSuffix(req.URL.Path, "/listPerInstanceConfigs") || strings.HasSuffix(req.URL.Path, "/listManagedInstances"))) {
						t.Fatalf("Infra Manager bypassed Terraform: %s %s", req.Method, req.URL)
					}
					response, err := f.roundTrip(req)
					if err != nil {
						t.Fatal(err)
					}
					return response, true
				}
				return nil, false
			}
			r := protocolRuntime(t, s.transport(t))
			values := s.inventory(t, r)
			for _, value := range f.assets {
				if value.ID != f.cluster.ID {
					value.Identity.ConnectionID = "connection"
					if live := f.resources[value.Identity.NativeID]; live != nil {
						value.Normalized = groupCopy(live)
					}
					values = append(values, value)
				}
			}
			hook, err := r.ServiceLifecycle(context.Background(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := hook.Contribute(context.Background(), "scope", values)
			if err != nil || len(contribution.Unresolved) != 0 {
				t.Fatalf("Infra Manager nested ownership: %+v %v", contribution, err)
			}
			groups, err := (&computeGroups{client: &client{project: "sample-project", number: "123456", http: &http.Client{Transport: s.transport(t)}}}).Contribute(context.Background(), "scope", values)
			if err != nil || len(groups.Unresolved) != 0 {
				t.Fatalf("GKE native ownership: %+v %v", groups, err)
			}
			root := batchAsset(values, infraTestDeployment)
			options := map[string]any{"retain_all_resources": mode != "delete"}
			result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: append(contribution.Bindings, groups.Bindings...), Relationships: append(contribution.Relationships, groups.Relationships...), RequestOptions: map[asset.AssetID]map[string]any{root.ID: options}})
			if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 {
				t.Fatalf("nested deployment plan: %+v %v", result, err)
			}
			request := dataformRequest(t, result, values, root)
			request.Parameters, request.IdempotencyKey = options, "nested-infra"
			driver, err := r.ResolveAction(context.Background(), "connection", root)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retained-vm-delete-tampered" {
				for i := range request.LifecycleImpacts {
					if request.LifecycleImpacts[i].Asset.ID == "vm" {
						request.LifecycleImpacts[i].Delete = true
					}
				}
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
					t.Fatal("ABANDON accepted descendant destruction")
				}
				return
			}
			action, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.writes) != 1 {
				t.Fatalf("native Terraform delete: %+v %v", action, err)
			}
			s.finish(action.ProviderOperationID, false)
			driver, action = infraRestart(t, s, request, action)
			switch mode {
			case "delete":
				delete(f.gke, f.pool.Identity.NativeID)
				if wait, err := driver.Wait(context.Background(), request, action); err != nil || wait.Done {
					t.Fatalf("absent node pool hid surviving VMs: %+v %v", wait, err)
				}
				for id := range f.resources {
					if id != f.value("data").Identity.NativeID && id != f.value("ip").Identity.NativeID {
						delete(f.resources, id)
					}
				}
			case "retained-vm-missing":
				delete(f.resources, f.value("vm").Identity.NativeID)
			case "retained-vm-recreated":
				f.live("vm")["id"] = "replacement-vm"
			}
			wait, err := driver.Wait(context.Background(), request, action)
			if strings.HasPrefix(mode, "retained-vm-") {
				if err == nil || wait.Done {
					t.Fatalf("missing/recreated retained descendant accepted: %+v %v", wait, err)
				}
			} else if err != nil || !wait.Done {
				t.Fatalf("completed nested lifecycle: %+v %v", wait, err)
			}
			if f.live("data") == nil || f.live("ip") == nil || f.deletes != 0 || len(f.mutations) != 0 || len(s.writes) != 1 {
				t.Fatal("Terraform delegation lost persistent resources or sent child writes")
			}
		})
	}
}
