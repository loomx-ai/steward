package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestDeploymentGroupIncompleteInventoryFails(t *testing.T) {
	for _, mode := range []string{"units-null", "units-object", "unit-id", "duplicate-unit", "duplicate-deployment", "forward-dependency", "null-dependency", "foreign-project", "escaped-deployment", "revision-identity", "snapshot-identity", "snapshot-incarnation", "revision-time", "revision-before-group", "ambiguous-success", "unknown-outcome", "missing-success", "aliases-null", "revision-list-null", "revision-list-duplicate", "revision-list-unreachable", "revision-list-token", "revision-denied", "revision-missing", "physical-denied", "metadata-changed-during-read", "new-revision-during-read"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			root, revision := s.resources[groupTestName], s.resources[groupTestName+"/revisions/g-2"]
			units := root["deploymentUnits"].([]any)
			snapshot := object(revision["snapshot"])
			switch mode {
			case "units-null":
				root["deploymentUnits"] = nil
			case "units-object":
				root["deploymentUnits"] = map[string]any{}
			case "unit-id":
				object(units[0])["id"] = ".."
			case "duplicate-unit":
				object(units[1])["id"] = "current"
			case "duplicate-deployment":
				object(units[1])["deployment"] = infraTestDeployment
			case "forward-dependency":
				object(units[0])["dependencies"] = []any{"placeholder"}
			case "null-dependency":
				object(units[0])["dependencies"] = nil
			case "foreign-project":
				object(units[0])["deployment"] = strings.Replace(infraTestDeployment, "sample-project", "foreign-project", 1)
			case "escaped-deployment":
				object(units[0])["deployment"] = infraTestDeployment + "%2fother"
			case "revision-identity":
				revision["name"] = strings.Replace(text(revision["name"]), "application", "other", 1)
			case "snapshot-identity":
				snapshot["name"] = groupTestName + "-other"
			case "snapshot-incarnation":
				snapshot["createTime"] = "2026-01-01T00:00:01Z"
			case "revision-time":
				revision["createTime"] = "bad-time"
			case "revision-before-group":
				revision["createTime"] = "2025-01-01T00:00:00Z"
			case "ambiguous-success":
				revision["createTime"] = s.resources[groupTestName+"/revisions/g-1"]["createTime"]
			case "unknown-outcome":
				snapshot["provisioningState"] = "PROVISIONING"
			case "missing-success":
				delete(s.resources, groupTestName+"/revisions/g-1")
				delete(s.resources, groupTestName+"/revisions/g-2")
			case "aliases-null":
				revision["alternativeIds"] = nil
			}
			revisionReads := 0
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/"+groupTestName+"/revisions" {
					data := map[string]any{}
					switch mode {
					case "revision-list-null":
						data["deploymentGroupRevisions"] = nil
					case "revision-list-duplicate":
						data["deploymentGroupRevisions"] = []any{revision, revision}
					case "revision-list-unreachable":
						data["unreachable"] = []any{"us-central1"}
					case "revision-list-token":
						data["nextPageToken"] = 12
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				if req.URL.Path == "/v1/"+groupTestName+"/revisions/g-2" {
					revisionReads++
					if mode == "revision-denied" || mode == "revision-missing" {
						status := 403
						if mode == "revision-missing" {
							status = 404
						}
						return dataformResponse(req, status, map[string]any{}), true
					}
					if mode == "metadata-changed-during-read" && revisionReads == 1 {
						changed := cloneParameters(revision)
						changed["createTime"] = "2026-02-01T00:00:01Z"
						return dataformResponse(req, 200, changed), true
					}
					if mode == "new-revision-during-read" && revisionReads == 1 {
						row := cloneParameters(revision)
						row["name"], row["createTime"] = groupTestName+"/revisions/new", "2026-02-02T00:00:00Z"
						s.resources[text(row["name"])] = row
					}
				}
				if mode == "physical-denied" && req.URL.Host == "storage.googleapis.com" {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				return nil, false
			}
			r := protocolRuntime(t, s.transport(t))
			batch, err := r.List(context.Background(), productRequest(r, infraGroup, "us-central1"))
			if err == nil || batch.Complete || len(s.writes) != 0 {
				t.Fatalf("incomplete group inventory accepted: %+v %v writes=%v", batch, err, s.writes)
			}
		})
	}
}

func TestDeploymentGroupChangedPlansCannotMutate(t *testing.T) {
	for _, mode := range []string{"group-replaced", "label-changed", "dag-changed", "historical-revision-changed", "deployment-replaced", "physical-changed", "deployment-lock", "protected-label", "unknown-state", "unknown-provisioning", "malformed-provisioning", "root-id", "wrong-connection", "missing-impact", "duplicate-impact", "extra-impact", "retained-metadata", "partial-deployment-retention", "partial-physical-retention", "structure-proof", "snapshot-proof", "force-option", "policy-option", "retention-null", "retention-string", "retention-target"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			r, _, _, request := deploymentGroupReviewed(t, s, nil)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			root := s.resources[groupTestName]
			switch mode {
			case "group-replaced":
				root["createTime"] = "2026-01-01T00:00:01Z"
			case "label-changed":
				root["labels"] = map[string]any{"changed": "true"}
			case "dag-changed":
				object(root["deploymentUnits"].([]any)[1])["dependencies"] = []any{}
			case "historical-revision-changed":
				object(s.resources[groupTestName+"/revisions/g-2"]["snapshot"])["labels"] = map[string]any{"changed": "true"}
			case "deployment-replaced":
				s.resources[infraTestDeployment]["createTime"] = "2026-01-01T00:00:01Z"
			case "physical-changed":
				s.physical[infraTestNetwork]["id"] = "changed-incarnation"
			case "deployment-lock":
				s.resources[infraTestDeployment]["lockState"] = "LOCKED"
			case "protected-label":
				root["labels"] = map[string]any{"steward-protected": "true"}
			case "unknown-state":
				root["state"] = "UNRECOGNIZED"
			case "unknown-provisioning":
				root["provisioningState"] = "UNRECOGNIZED"
			case "malformed-provisioning":
				root["provisioningState"] = false
			case "root-id":
				request.Asset.Identity.NativeID += "-other"
			case "wrong-connection":
				request.Asset.Identity.ConnectionID = "other"
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "extra-impact":
				impact := request.LifecycleImpacts[0]
				impact.Asset.ID, impact.Asset.Identity.NativeID = "extra", groupTestHistorical
				request.LifecycleImpacts = append(request.LifecycleImpacts, impact)
			case "retained-metadata", "partial-deployment-retention", "partial-physical-retention":
				for i := range request.LifecycleImpacts {
					impact := &request.LifecycleImpacts[i]
					if mode == "retained-metadata" && impact.Asset.Identity.NativeType == infraGroupRevision || mode == "partial-deployment-retention" && impact.Asset.Identity.NativeType == infraDeployment || mode == "partial-physical-retention" && impact.Asset.Identity.NativeType == "storage.googleapis.com/Bucket" {
						impact.Delete = false
						break
					}
				}
			case "structure-proof":
				request.Asset.Normalized[infraGroupStructure] = "changed"
			case "snapshot-proof":
				request.Asset.Normalized[infraSnapshotKey] = "changed"
			case "force-option":
				request.Parameters = map[string]any{"force": true}
			case "policy-option":
				request.Parameters = map[string]any{"deploymentReferencePolicy": "IGNORE_DEPLOYMENT_REFERENCES"}
			case "retention-null":
				request.Parameters = map[string]any{"retain_resources": nil}
			case "retention-string":
				request.Parameters = map[string]any{"retain_all_resources": "true"}
			case "retention-target":
				request.Parameters = map[string]any{"retain_resources": []any{"unreviewed"}}
			}
			if result, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
				t.Fatalf("changed group review reached mutation: %+v %v writes=%v", result, err, s.writes)
			}
		})
	}
}

func TestDeploymentGroupOperationAndPhaseValidation(t *testing.T) {
	for _, mode := range []string{"foreign-operation", "changed-target", "wrong-region", "wrong-verb", "wrong-version", "wrong-metadata-type", "cancelled", "malformed-cancellation", "failed-operation", "malformed-done", "foreign-response", "wrong-response-type", "wrong-response-state", "wrong-provisioning-state", "recreated-response", "changed-response-units", "wrong-progress-unit", "wrong-progress-deployment", "swapped-progress-deployment", "failed-progress", "recreate-intent", "failed-step", "provision-step", "null-progress", "duplicate-progress", "changed-phase", "changed-review", "changed-initial-operation", "foreign-current-operation", "unexpected-phase-field"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			r, _, _, request := deploymentGroupReviewed(t, s, nil)
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			op := s.operations[strings.TrimPrefix(result.ProviderOperationID, "https://config.googleapis.com/v1/")]
			metadata := object(op["metadata"])
			phase := object(metadata["provisionDeploymentGroupMetadata"])
			rows := phase["deploymentUnitProgresses"].([]any)
			progress := object(rows[0])
			if strings.Contains(mode, "response") || mode == "wrong-provisioning-state" {
				s.finishDeprovision(false)
			}
			switch mode {
			case "foreign-operation":
				op["name"] = strings.Replace(text(op["name"]), "sample-project", "foreign-project", 1)
			case "changed-target":
				metadata["target"] = groupTestName + "-other"
			case "wrong-region":
				op["name"] = strings.Replace(text(op["name"]), "us-central1", "europe-west1", 1)
			case "wrong-verb":
				metadata["verb"] = "delete"
			case "wrong-version":
				metadata["apiVersion"] = "v2"
			case "wrong-metadata-type":
				metadata["@type"] = "type.googleapis.com/google.protobuf.Empty"
			case "cancelled":
				metadata["requestedCancellation"] = true
			case "malformed-cancellation":
				metadata["requestedCancellation"] = "false"
			case "failed-operation":
				op["error"] = map[string]any{"code": 13, "message": "GROUP_PRIVATE_FAILURE"}
			case "malformed-done":
				op["done"] = "true"
			case "foreign-response":
				object(op["response"])["name"] = groupTestName + "-other"
			case "wrong-response-type":
				object(op["response"])["@type"] = "type.googleapis.com/google.protobuf.Empty"
			case "wrong-response-state":
				object(op["response"])["state"] = "DELETED"
			case "wrong-provisioning-state":
				object(op["response"])["provisioningState"] = "PROVISIONED"
			case "recreated-response":
				object(op["response"])["createTime"] = "2026-01-01T00:00:01Z"
			case "changed-response-units":
				object(op["response"])["deploymentUnits"] = []any{map[string]any{"id": "wrong"}}
			case "wrong-progress-unit":
				progress["unitId"] = "unreviewed"
			case "wrong-progress-deployment":
				progress["deployment"] = groupTestHistorical
			case "swapped-progress-deployment":
				progress["deployment"] = groupTestRetired
			case "failed-progress":
				progress["state"] = "FAILED"
			case "recreate-intent":
				progress["intent"] = "RECREATE_DEPLOYMENT"
			case "failed-step":
				phase["step"] = "FAILED"
			case "provision-step":
				phase["step"] = "PROVISIONING_DEPLOYMENT_UNITS"
			case "null-progress":
				phase["deploymentUnitProgresses"] = nil
			case "duplicate-progress":
				phase["deploymentUnitProgresses"] = append(rows, rows[0])
			case "changed-phase":
				result.Data["phase"] = "group_ready_delete"
			case "changed-review":
				result.Data["review"] = "changed"
			case "changed-initial-operation":
				result.Data["initial_operation"] = "changed"
			case "foreign-current-operation":
				result.Data["operation"] = "https://foreign.example/v1/operations/123"
			case "unexpected-phase-field":
				result.Data["skip_verification"] = true
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err == nil || wait.Done || len(s.writes) != 1 {
				t.Fatalf("changed group LRO/phase accepted: %+v %v writes=%v", wait, err, s.writes)
			}
			encoded, _ := json.Marshal(err)
			if strings.Contains(string(encoded), "GROUP_PRIVATE_FAILURE") {
				t.Fatal("provider failure details escaped")
			}
		})
	}
}

func TestDeploymentGroupRetentionMustRemainConsistent(t *testing.T) {
	for _, mode := range []string{"retained-deployment-missing", "retained-deployment-replaced", "retained-physical-missing", "retained-physical-replaced", "retained-metadata-missing", "abandoned-physical-missing", "abandoned-physical-replaced"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			options := map[string]any{"retain_resources": []string{"//config.googleapis.com/" + infraTestDeployment, "//config.googleapis.com/" + groupTestRetired}}
			if strings.HasPrefix(mode, "abandoned") {
				options = map[string]any{"retain_all_resources": true}
			}
			r, _, _, request := deploymentGroupReviewed(t, s, options)
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "abandoned") {
				s.finishDeprovision(false)
			}
			switch mode {
			case "retained-deployment-missing":
				delete(s.resources, infraTestDeployment)
			case "retained-deployment-replaced":
				s.resources[infraTestDeployment]["createTime"] = "2026-01-01T00:00:01Z"
			case "retained-physical-missing", "abandoned-physical-missing":
				delete(s.physical, infraTestNetwork)
			case "retained-physical-replaced", "abandoned-physical-replaced":
				s.physical[infraTestNetwork]["id"] = "new"
			case "retained-metadata-missing":
				delete(s.resources, infraTestRevision)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err == nil || wait.Done || len(s.writes) != 1 {
				t.Fatalf("retained group effect changed: %+v %v", wait, err)
			}
		})
	}
}

func TestDeploymentGroupActionConnectionCannotChange(t *testing.T) {
	s := newDeploymentGroupScenario(t)
	r, _, _, request := deploymentGroupReviewed(t, s, nil)
	if _, err := r.ResolveAction(context.Background(), asset.ConnectionID("different"), request.Asset); err == nil {
		t.Fatal("foreign connection resolved group action")
	}
	if len(s.writes) != 0 {
		t.Fatal("connection validation mutated a resource")
	}
}
