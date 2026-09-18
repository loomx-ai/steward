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

func infraFixtureAction(t *testing.T, retain bool) (*infraScenario, contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	s := newInfraScenario(t)
	r, _, _, request := infraReviewed(t, s, infraTestDeployment, map[string]any{"retain_all_resources": retain})
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	return s, driver, request
}

func TestInfraManagerIncompleteInventoryPreservesAuthority(t *testing.T) {
	for _, mode := range []string{"locations-denied", "locations-partial", "root-denied", "root-error", "root-time", "root-project", "root-region", "latest-missing", "revision-time", "revision-parent", "resource-identity", "resource-tf-info", "resource-list-null", "resource-list-typed", "resource-list-partial", "duplicate", "page-cycle", "page-type", "physical-denied", "physical-identity", "physical-project", "root-recreated", "revision-changed", "late-resource", "late-root-404"} {
		t.Run(mode, func(t *testing.T) {
			s := newInfraScenario(t)
			rootReads, resourceLists := 0, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				respond := func(code int, data any) (*http.Response, bool) { return dataformResponse(req, code, data), true }
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					if mode == "locations-denied" {
						return respond(403, map[string]any{})
					}
					if mode == "locations-partial" {
						return respond(200, map[string]any{"unreachable": []any{"europe-west1"}})
					}
				}
				if req.URL.Path == "/v1/"+infraTestDeployment {
					rootReads++
					data := roundTripDataformJSON(t, s.resources[infraTestDeployment])
					switch mode {
					case "root-denied":
						return respond(403, map[string]any{})
					case "root-error":
						data["error"] = map[string]any{"code": 7}
					case "root-time":
						delete(data, "createTime")
					case "root-project":
						data["name"] = strings.Replace(infraTestDeployment, "sample-project", "other-project", 1)
					case "root-region":
						data["name"] = strings.Replace(infraTestDeployment, "us-central1", "europe-west1", 1)
					case "latest-missing":
						data["latestRevision"] = infraTestDeployment + "/revisions/missing"
					case "root-recreated":
						if rootReads > 1 {
							data["createTime"] = "2026-09-09T12:00:00Z"
						}
					case "late-root-404":
						if rootReads > 1 {
							return respond(404, map[string]any{})
						}
					default:
						return nil, false
					}
					return respond(200, data)
				}
				if req.URL.Path == "/v1/"+infraTestRevision {
					data := roundTripDataformJSON(t, s.resources[infraTestRevision])
					if mode == "revision-time" {
						delete(data, "createTime")
						return respond(200, data)
					}
					if mode == "revision-parent" {
						data["name"] = strings.Replace(infraTestRevision, "stack-a", "stack-b", 1)
						return respond(200, data)
					}
				}
				if req.URL.Path == "/v1/"+infraTestRevision+"/resources" {
					resourceLists++
					rows := []any{s.resources[infraTestRevision+"/resources/bucket"], s.resources[infraTestRevision+"/resources/network"]}
					data := map[string]any{"resources": rows}
					switch mode {
					case "resource-list-null":
						data["resources"] = nil
					case "resource-list-typed":
						data["resources"] = map[string]any{}
					case "resource-list-partial":
						data["unreachable"] = []any{"resources"}
					case "duplicate":
						data["resources"] = append(rows, rows[0])
					case "page-cycle":
						data["nextPageToken"] = "repeat"
					case "page-type":
						data["nextPageToken"] = true
					case "late-resource":
						if resourceLists > 1 {
							data["resources"] = rows[:1]
						}
					case "revision-changed":
						if resourceLists == 1 {
							s.resources[infraTestRevision]["serviceAccount"] = "projects/sample-project/serviceAccounts/changed@sample-project.iam.gserviceaccount.com"
						}
						return nil, false
					default:
						return nil, false
					}
					return respond(200, data)
				}
				if req.URL.Path == "/v1/"+infraTestRevision+"/resources/network" {
					data := roundTripDataformJSON(t, s.resources[infraTestRevision+"/resources/network"])
					if mode == "resource-identity" {
						data["name"] = infraTestRevision + "/resources/other"
						return respond(200, data)
					}
					if mode == "resource-tf-info" {
						data["terraformInfo"] = "hidden"
						return respond(200, data)
					}
				}
				if req.URL.Path == "/compute/v1/projects/sample-project/global/networks/owned-network" {
					data := roundTripDataformJSON(t, s.physical[infraTestNetwork])
					if mode == "physical-denied" {
						return respond(403, map[string]any{})
					}
					if mode == "physical-identity" {
						data["selfLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/other"
						return respond(200, data)
					}
				}
				if req.URL.Path == "/storage/v1/b/infra-owned-data" && mode == "physical-project" {
					data := roundTripDataformJSON(t, s.physical[infraTestBucket])
					data["projectNumber"] = "999999"
					return respond(200, data)
				}
				return nil, false
			}
			r := protocolRuntime(t, s.transport(t))
			batch, err := r.List(context.Background(), productRequest(r, infraDeployment, "us-central1"))
			if err == nil || batch.Complete || len(s.writes) > 0 {
				t.Fatalf("incomplete native inventory accepted: %+v %v", batch, err)
			}
		})
	}
}

func TestInfraManagerChangedPlansNeverReachDelete(t *testing.T) {
	for _, mode := range []string{"provider", "connection", "partition", "kind", "identity", "asset-id", "missing-proof", "missing-manifest", "changed-manifest", "missing-snapshot", "missing-impact", "duplicate-impact", "foreign-impact", "wrong-controller", "retained-metadata", "extra-impact", "extra-prerequisite", "root-recreated", "root-config", "root-locked", "unknown-state", "failed-revision", "physical-recreated", "physical-config", "physical-removed", "protected-root", "protected-physical", "nonempty-bucket", "membership-added", "membership-removed", "physical-permission"} {
		t.Run(mode, func(t *testing.T) {
			s, driver, request := infraFixtureAction(t, false)
			switch mode {
			case "provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "partition":
				request.Asset.Identity.Partition = "other"
			case "kind":
				request.Asset.Identity.NativeType = infraPreview
			case "identity":
				request.Asset.Identity.NativeID += "-other"
			case "asset-id":
				request.Asset.ID = ""
			case "missing-proof":
				delete(request.Asset.Normalized, infraProof)
			case "missing-manifest":
				delete(request.Asset.Normalized, infraManifestKey)
			case "changed-manifest":
				request.Asset.Normalized[infraManifestKey] = "[]"
			case "missing-snapshot":
				delete(request.Asset.Normalized, infraSnapshotKey)
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "foreign-impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "wrong-controller":
				request.LifecycleImpacts[0].ControllerID = "other"
			case "retained-metadata":
				for i := range request.LifecycleImpacts {
					if isInfra(request.LifecycleImpacts[i].Asset.Identity.NativeType) {
						request.LifecycleImpacts[i].Delete = false
						break
					}
				}
			case "extra-impact":
				extra := request.LifecycleImpacts[0]
				extra.Asset.ID = "extra"
				extra.Asset.Identity.NativeID += "-extra"
				request.LifecycleImpacts = append(request.LifecycleImpacts, extra)
			case "extra-prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{request.LifecycleImpacts[0]}
			case "root-recreated":
				s.resources[infraTestDeployment]["createTime"] = "2026-09-09T12:00:00Z"
			case "root-config":
				object(object(s.resources[infraTestDeployment]["terraformBlueprint"])["inputValues"])["extra"] = map[string]any{"inputValue": "INFRA_PRIVATE_CHANGED"}
			case "root-locked":
				s.resources[infraTestDeployment]["lockState"] = "LOCKED"
			case "unknown-state":
				s.resources[infraTestDeployment]["state"] = "UNKNOWN"
			case "failed-revision":
				s.resources[infraTestRevision]["state"] = "FAILED"
			case "physical-recreated":
				s.physical[infraTestNetwork]["id"] = "999"
			case "physical-config":
				s.physical[infraTestNetwork]["mtu"] = 1500
			case "physical-removed":
				delete(s.physical, infraTestNetwork)
			case "protected-root":
				s.resources[infraTestDeployment]["labels"] = map[string]any{"steward-protected": "true"}
			case "protected-physical":
				s.physical[infraTestNetwork]["labels"] = map[string]any{"steward-protected": "true"}
			case "membership-added":
				row := roundTripDataformJSON(t, s.resources[infraTestRevision+"/resources/network"])
				row["name"] = infraTestRevision + "/resources/extra"
				s.resources[text(row["name"])] = row
			case "membership-removed":
				delete(s.resources, infraTestRevision+"/resources/network")
			case "physical-permission":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/networks/owned-network") {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			case "nonempty-bucket":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/infra-owned-data/o") {
						return dataformResponse(req, 200, map[string]any{"items": []any{map[string]any{"name": "kept-version", "generation": "2"}}}), true
					}
					return nil, false
				}
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
				t.Fatalf("changed plan reached native DELETE: %v %v", s.writes, err)
			}
		})
	}
}

func TestInfraManagerOpaqueDeploymentRecordsRequireExplicitAbandon(t *testing.T) {
	for _, mode := range []string{"iam-member", "unknown-provider", "missing-cai", "many-cai", "wrong-tf-id", "cross-project", "unreconciled"} {
		t.Run(mode, func(t *testing.T) {
			s := newInfraScenario(t)
			row := s.resources[infraTestRevision+"/resources/network"]
			switch mode {
			case "iam-member":
				object(row["terraformInfo"])["type"] = "google_project_iam_member"
			case "unknown-provider":
				object(row["terraformInfo"])["type"] = "custom_resource"
			case "missing-cai":
				delete(row, "caiAssets")
			case "many-cai":
				object(row["caiAssets"])["compute.googleapis.com/Disk"] = map[string]any{"fullResourceName": "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/data"}
			case "wrong-tf-id":
				object(row["terraformInfo"])["id"] = "projects/sample-project/global/networks/other"
			case "cross-project":
				object(object(row["caiAssets"])["compute.googleapis.com/Network"])["fullResourceName"] = "//compute.googleapis.com/projects/other-project/global/networks/owned-network"
			case "unreconciled":
				row["state"] = "FAILED"
			}
			r, values, input, request := infraReviewed(t, s, infraTestDeployment, nil)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
				t.Fatal("opaque logical record authorized native resource destruction")
			}
			options := map[string]any{"retain_all_resources": true}
			input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: options}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) > 0 {
				t.Fatalf("explicit ABANDON plan: %+v %v", result, err)
			}
			request = dataformRequest(t, result, values, request.Asset)
			request.Parameters = options
			deleted, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.writes) != 1 || !strings.Contains(s.writes[0], "deletePolicy=ABANDON") {
				t.Fatalf("explicit ABANDON: %+v %v", deleted, err)
			}
			s.finish(deleted.ProviderOperationID, false)
			if wait, err := driver.Wait(context.Background(), request, deleted); err != nil || !wait.Done {
				t.Fatalf("ABANDON observation: %+v %v", wait, err)
			}
			if len(s.physical) != 4 {
				t.Fatal("ABANDON removed a physical object")
			}
		})
	}
}

func TestInfraManagerWaitRequiresPhysicalAndMetadataProof(t *testing.T) {
	for _, mode := range []string{"resource-remains", "operation-expired-resource-remains", "operation-expired-absent", "pending-absent", "root-recreated", "physical-recreated", "physical-deleting-config", "retained-missing", "retained-changed", "metadata-remains", "metadata-recreated", "late-root-reappears", "member-permission", "operation-permission", "empty-result-with-live-resource"} {
		t.Run(mode, func(t *testing.T) {
			retain := strings.HasPrefix(mode, "retained-")
			s, driver, request := infraFixtureAction(t, retain)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			oldRoot := roundTripDataformJSON(t, s.resources[infraTestDeployment])
			oldMetadata := roundTripDataformJSON(t, s.resources[infraTestRevision])
			oldPhysical := roundTripDataformJSON(t, s.physical[infraTestNetwork])
			s.finish(result.ProviderOperationID, strings.Contains(mode, "resource-remains") || mode == "empty-result-with-live-resource")
			op := strings.TrimPrefix(result.ProviderOperationID, "https://config.googleapis.com/v1/")
			wantErr, wantDone := false, false
			switch mode {
			case "operation-expired-resource-remains":
				delete(s.operations, op)
			case "operation-expired-absent":
				delete(s.operations, op)
				wantDone = true
			case "pending-absent":
				s.operations[op]["done"] = false
				delete(s.operations[op], "response")
			case "root-recreated":
				oldRoot["createTime"] = "2026-09-09T12:00:00Z"
				s.resources[infraTestDeployment] = oldRoot
				wantErr = true
			case "physical-recreated":
				oldPhysical["id"] = "999"
				s.physical[infraTestNetwork] = oldPhysical
				wantErr = true
			case "physical-deleting-config":
				// The deployment engine can change attachments/configuration during teardown.
				// The original immutable Compute ID must remain a pending resource.
				oldPhysical["subnetworks"] = []any{}
				s.physical[infraTestNetwork] = oldPhysical
			case "retained-missing":
				delete(s.physical, infraTestNetwork)
				wantErr = true
			case "retained-changed":
				s.physical[infraTestNetwork]["mtu"] = 1500
				wantErr = true
			case "metadata-remains":
				s.resources[infraTestRevision] = oldMetadata
			case "metadata-recreated":
				oldMetadata["createTime"] = "2026-09-09T12:00:00Z"
				s.resources[infraTestRevision] = oldMetadata
				wantErr = true
			case "late-root-reappears":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+infraTestDeployment {
						reads++
						if reads > 1 {
							return dataformResponse(req, 200, oldRoot), true
						}
					}
					return nil, false
				}
				wantErr = true
			case "member-permission":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/networks/owned-network") {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
				wantErr = true
			case "operation-permission":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.Contains(req.URL.Path, "/operations/") {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
				wantErr = true
			case "empty-result-with-live-resource":
				result = contracts.ActionResult{}
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if (err != nil) != wantErr || wait.Done != wantDone || len(s.writes) != 1 {
				t.Fatalf("wait accepted incomplete or changed deletion: %+v %v", wait, err)
			}
		})
	}
}

func TestInfraManagerRejectsChangedOperationAndRecoveryState(t *testing.T) {
	for _, mode := range []string{"phase", "configuration", "manifest", "review", "resource", "abandon", "initial-operation", "operation-host", "operation-project", "operation-region", "operation-version", "operation-query", "operation-fragment", "operation-user", "operation-escape", "name", "done", "error", "error-null", "metadata-type", "metadata-target", "metadata-verb", "metadata-api", "metadata-null", "cancelled", "cancellation-type", "metadata-failed", "metadata-wrong-family", "response-type", "response-target", "response-live", "response-pending"} {
		t.Run(mode, func(t *testing.T) {
			s, driver, request := infraFixtureAction(t, false)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			op := s.operations[strings.TrimPrefix(result.ProviderOperationID, "https://config.googleapis.com/v1/")]
			metadata := object(op["metadata"])
			switch mode {
			case "phase":
				result.Data["phase"] = "ready"
			case "configuration":
				result.Data["configuration"] = "wrong"
			case "manifest":
				result.Data["members"] = "[]"
			case "review":
				result.Data["review"] = "wrong"
			case "resource":
				result.Data["resource"] = "wrong"
			case "abandon":
				result.Data["abandon"] = true
			case "initial-operation":
				result.Data["initial_operation"] = ""
			case "operation-host":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "config.googleapis.com", "evil.invalid", 1)
			case "operation-project":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "sample-project", "other-project", 1)
			case "operation-region":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "us-central1", "europe-west1", 1)
			case "operation-version":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "/v1/", "/v2/", 1)
			case "operation-query":
				result.ProviderOperationID += "?token=private"
			case "operation-fragment":
				result.ProviderOperationID += "#private"
			case "operation-user":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "https://", "https://user@", 1)
			case "operation-escape":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "/operations/", "/operations/%2e%2e/", 1)
			case "name":
				op["name"] = infraTestParent + "/operations/another"
			case "done":
				op["done"] = "true"
			case "error":
				op["error"] = map[string]any{"code": 7, "message": "INFRA_PRIVATE_ERROR"}
			case "error-null":
				op["error"] = nil
			case "metadata-type":
				metadata["@type"] = "type.googleapis.com/google.cloud.Other"
			case "metadata-target":
				metadata["target"] = infraTestPreview
			case "metadata-verb":
				metadata["verb"] = "create"
			case "metadata-api":
				metadata["apiVersion"] = "v2"
			case "metadata-null":
				op["metadata"] = nil
			case "cancelled":
				metadata["requestedCancellation"] = true
			case "cancellation-type":
				metadata["requestedCancellation"] = "false"
			case "metadata-failed":
				metadata["deploymentMetadata"] = map[string]any{"step": "FAILED"}
			case "metadata-wrong-family":
				metadata["previewMetadata"] = map[string]any{}
			default:
				op["done"] = true
				response := map[string]any{"@type": "type.googleapis.com/google.cloud.config.v1.Deployment", "name": infraTestDeployment, "state": "DELETED"}
				op["response"] = response
				if mode == "response-type" {
					response["@type"] = "type.googleapis.com/google.protobuf.Empty"
				}
				if mode == "response-target" {
					response["name"] = infraTestPreview
				}
				if mode == "response-live" {
					response["state"] = "ACTIVE"
				}
				if mode == "response-pending" {
					op["done"] = false
				}
			}
			if strings.HasPrefix(mode, "operation-") {
				result.Data["operation"] = result.ProviderOperationID
				result.Data["initial_operation"] = result.ProviderOperationID
			}
			_, err = driver.Wait(context.Background(), request, result)
			if err == nil || len(s.writes) != 1 {
				t.Fatalf("changed recovery or native operation accepted: %v", err)
			}
			encoded, _ := json.Marshal(err)
			if strings.Contains(string(encoded), "INFRA_PRIVATE_ERROR") {
				t.Fatal("deployment failure leaked private text")
			}
		})
	}
}
