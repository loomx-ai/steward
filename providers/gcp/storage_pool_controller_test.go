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

func TestStoragePoolManagedControllersPreserveOwnershipAndRetention(t *testing.T) {
	for _, mode := range []string{"mig", "gke-pool", "gke-cluster"} {
		t.Run(mode, func(t *testing.T) {
			var network *gkeNetworkFixture
			var f *gkeFixture
			if mode == "gke-cluster" {
				network = newGKENetworkFixture(t)
				f = network.gkeFixture
			} else {
				f = newGKEFixture(t)
			}
			var underlying roundTripFunc = f.roundTrip
			root := f.pool
			if mode == "mig" {
				f.assets = f.assets[:len(f.assets)-2]
				underlying = f.managedGroupFixture.roundTrip
				root = f.value("mig")
			}
			if mode == "gke-cluster" {
				root = f.cluster
			}
			root.Identity.ConnectionID = "connection"
			for i := range f.assets {
				f.assets[i].Identity.ConnectionID = "connection"
			}
			s := newPoolCleanupScenario()
			boot := f.value("boot")
			for _, data := range []map[string]any{boot.Normalized, f.live("boot")} {
				data["storagePool"] = s.pool["selfLink"]
				data["creationTimestamp"] = s.disk["creationTimestamp"]
				data["disk"] = data["selfLink"]
			}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "storagePools") || strings.HasSuffix(req.URL.Path, "/operations/delete-pool") {
					s.disk = f.live("boot")
					return s.transport(t)(req)
				}
				return underlying(req)
			})
			if network != nil {
				native := network.tls.native
				network.tls.native = func(w http.ResponseWriter, req *http.Request) {

					if strings.Contains(req.URL.Path, "storagePools") || strings.HasSuffix(req.URL.Path, "/operations/delete-pool") {
						s.disk = f.live("boot")
						response, err := s.transport(t)(req)
						if err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						copyHTTP(w, response)
						return
					}
					native(w, req)
				}
				network.enrich()
			}
			r := protocolRuntime(t, transport)
			if network != nil {
				r.credentials = credentialFunc(func(_ context.Context, id asset.ConnectionID) (contracts.Credential, error) {
					if id != "connection" {
						t.Fatal("wrong TLS credential connection", id)
					}
					return network.tls.credential, nil
				})
				r.clients["connection"] = network.tls.client
			}
			batch, err := r.List(t.Context(), productRequest(r, storagePoolType, "us-central1"))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			pool := asset.Asset{ID: "storage", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: root.Identity.ConnectionID, Partition: root.Identity.Partition, NativeType: storagePoolType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
			values := append(f.assets, pool)
			contributor, err := r.ComputeLifecycle(t.Context(), root.Identity.ConnectionID)
			if err != nil {
				t.Fatal(err)
			}
			c, err := contributor.Contribute(t.Context(), "scope", values)
			if err != nil || len(c.Unresolved) != 0 {
				t.Fatal(c, err)
			}
			var solved plan.Result
			for _, choice := range []string{"selected", "unselected", "retained", "protected"} {
				input := plan.Input{Assets: values, Relationships: c.Relationships, LifecycleBindings: c.Bindings, ResolvedAssetIDs: []asset.AssetID{pool.ID}}
				if choice != "unselected" {
					input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, root.ID)
				}
				if choice == "retained" {
					input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{"boot"}}}
				}
				if choice == "protected" {
					input.Protections = []plan.ProtectionPolicy{{AssetID: "boot", Protected: true}}
				}
				result, err := plan.Solve(input)
				if err != nil || (len(result.Blockers) == 0) != (choice == "selected") {
					t.Fatal(choice, result.Blockers, err)
				}
				if choice == "selected" {
					solved = result
				}
			}
			if len(solved.Steps) != 2 {
				t.Fatal("managed disk gained independent deletion", solved)
			}
			for _, step := range solved.Steps {
				if step.AssetID == pool.ID {
					required, err := plan.RequiredDeletions(step)
					if err != nil || len(required) != 1 || required[0].AssetID != "boot" || required[0].ControllerAssetID != root.ID {
						t.Fatal("nested controller prerequisite", required, err)
					}
				}
			}
			request := dataformRequest(t, solved, values, root)
			if mode == "mig" {
				f.mutation = func(req *http.Request, _ map[string]any) func() {
					if req.Method != "DELETE" || !strings.HasSuffix(req.URL.Path, "instanceGroupManagers/workers") {
						t.Fatal("unexpected controller mutation", req.Method, req.URL)
					}
					return func() {
						delete(f.resources, root.Identity.NativeID)
						for _, impact := range request.LifecycleImpacts {
							if impact.Delete {
								delete(f.resources, impact.Asset.Identity.NativeID)
							}
						}
					}
				}
			}
			driver, err := r.ResolveAction(t.Context(), root.Identity.ConnectionID, root)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			done := false
			for i := 0; i < 50; i++ {
				encoded, _ := json.Marshal(receipt)
				_ = json.Unmarshal(encoded, &receipt)
				fresh := protocolRuntime(t, transport)
				if network != nil {
					fresh.credentials = r.credentials
					fresh.clients["connection"] = network.tls.client
				}
				driver, err = fresh.ResolveAction(t.Context(), root.Identity.ConnectionID, root)
				if err != nil {
					t.Fatal(err)
				}
				wait, err := driver.Wait(t.Context(), request, receipt)
				if err != nil {
					t.Fatal(err)
				}
				if wait.Data != nil {
					receipt.Data = wait.Data
				}
				if wait.Done {
					done = true
					break
				}
			}
			if !done {
				t.Fatal("controller never confirmed members absent")
			}
			poolDriver, err := r.ResolveAction(t.Context(), pool.Identity.ConnectionID, pool)
			if err != nil {
				t.Fatal(err)
			}
			poolRequest := contracts.ActionRequest{Asset: pool, Action: "delete", IdempotencyKey: "pool-after-controller", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: boot, ControllerID: pool.ID, Delete: true}}}
			receipt, err = poolDriver.Execute(context.Background(), poolRequest)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := poolDriver.Wait(t.Context(), poolRequest, receipt)
			if err != nil || !wait.Done || len(s.writes) != 1 {
				t.Fatal(wait, err, s.writes)
			}
		})
	}
}

func TestInstanceOwnAbsenceDoesNotHideDiskOutcomes(t *testing.T) {
	for _, mode := range []string{"absent", "survivor", "denied", "retained-missing", "retained-replaced", "missing-impact"} {
		t.Run(mode, func(t *testing.T) {
			request := diskActionRequest(mode == "retained-missing" || mode == "retained-replaced")
			request.LifecycleImpacts[0].Asset.Normalized["id"] = "original"
			if mode == "missing-impact" {
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			}
			transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal("absent VM was mutated", req.Method, req.URL)
				}
				if strings.HasSuffix(req.URL.Path, "/disks/boot") {
					if mode == "denied" {
						return apiResponse(req, 403, `{"error":{"code":403}}`), nil
					}
					if mode == "survivor" {
						return apiResponse(req, 200, `{"id":"original"}`), nil
					}
					if mode == "retained-replaced" {
						return apiResponse(req, 200, `{"id":"replacement"}`), nil
					}
				}
				return apiResponse(req, 404, `{}`), nil
			})
			driver := protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", transport)
			read, err := driver.Readback(t.Context(), request)
			if mode == "absent" {
				if err != nil || read.Exists {
					t.Fatal(read, err)
				}
			} else if mode == "survivor" {
				if err != nil || !read.Exists {
					t.Fatal(read, err)
				}
			} else if err == nil {
				t.Fatal("unverified disk outcome accepted", read)
			}
			result, err := driver.Execute(t.Context(), request)
			if mode == "absent" || mode == "survivor" {
				if err != nil {
					t.Fatal(err)
				}
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || wait.Done != (mode == "absent") {
					t.Fatal(wait, err)
				}
			} else if err == nil {
				t.Fatal("bad disk outcome allowed execution")
			}
		})
	}
}
