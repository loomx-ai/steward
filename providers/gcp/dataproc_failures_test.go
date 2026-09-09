package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDataprocInventoryRejectsIncompleteAndRecreatedNativeResources(t *testing.T) {
	for _, mode := range []string{"list-403", "list-206", "list-error", "unreachable", "bad-list", "token-type", "token-loop", "duplicate", "project", "uuid", "node-foreign", "node-missing-id", "node-duplicate", "node-detail-404", "node-detail-403", "node-detail-206", "node-config-changed", "parent-config-changed", "parent-uuid-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataprocScenario(t)
			reads := 0
			if mode == "node-missing-id" {
				entry := object(array(object(s.resources[dpRoot]["config"])["auxiliaryNodeGroups"])[0])
				delete(entry, "nodeGroupId")
				delete(object(entry["nodeGroup"]), "name")
			}
			if mode == "node-duplicate" {
				config := object(s.resources[dpRoot]["config"])
				config["auxiliaryNodeGroups"] = append(array(config["auxiliaryNodeGroups"]), array(config["auxiliaryNodeGroups"])[0])
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				if name == dpRoot {
					reads++
					data := roundTripDataformJSON(t, s.resources[name])
					switch mode {
					case "uuid":
						delete(data, "clusterUuid")
					case "parent-config-changed":
						if reads > 1 {
							object(object(data["config"])["softwareConfig"])["properties"] = map[string]any{"hidden": "changed"}
						}
					case "parent-uuid-changed":
						if reads > 1 {
							data["clusterUuid"] = "new-uuid"
						}
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				if name == dpNode {
					data := roundTripDataformJSON(t, s.resources[dpNode])
					code := 200
					switch mode {
					case "node-foreign":
						data["name"] = dpOther + "/nodeGroups/aux-real-17"
					case "node-detail-404":
						code = 404
					case "node-detail-403":
						code = 403
					case "node-detail-206":
						code = 206
					case "node-config-changed":
						object(data["nodeGroupConfig"])["numInstances"] = 8
					default:
						return nil, false
					}
					return dataformResponse(req, code, data), true
				}
				if name != "projects/sample-project/regions/us-central1/clusters" {
					return nil, false
				}
				data := map[string]any{}
				code := 200
				switch mode {
				case "list-403":
					code = 403
				case "list-206":
					code = 206
				case "list-error":
					data["error"] = map[string]any{"code": 403, "message": "DATAPROC_PRIVATE_ERROR"}
				case "unreachable":
					data["unreachable"] = []any{"us-central1"}
				case "bad-list":
					data["clusters"] = "bad"
				case "token-type":
					data["nextPageToken"] = 8
				case "token-loop":
					data["nextPageToken"] = "same"
				case "duplicate":
					data["clusters"] = []any{s.resources[dpRoot], s.resources[dpRoot]}
				case "project":
					copy := roundTripDataformJSON(t, s.resources[dpRoot])
					copy["projectId"] = "foreign"
					data["clusters"] = []any{copy}
				default:
					return nil, false
				}
				return dataformResponse(req, code, data), true
			}
			r := protocolRuntime(t, s.transport(t))
			req := productRequest(r, dataprocNodeGroupType, "us-central1")
			failed := false
			for i := 0; i < 6; i++ {
				page, err := r.List(context.Background(), req)
				if err != nil {
					failed = true
					break
				}
				if page.Complete {
					break
				}
				req.Cursor = page.NextCursor
			}
			if !failed || len(s.mutations) != 0 {
				t.Fatalf("invalid inventory was accepted (%v)", s.mutations)
			}
		})
	}
}

func TestDataprocPreflightRejectsChangedEffectsAndForgedPlans(t *testing.T) {
	for _, mode := range []string{"root-uuid", "root-proof", "root-secret", "root-state", "root-protected", "job-uuid", "job-secret", "job-state", "node-config", "vm-id", "vm-metadata", "vm-zone", "vm-network", "vm-account", "vm-protected", "boot-retain", "external-delete", "disk-recreated", "disk-shared", "mig-template", "mig-unstable", "mig-policy", "template-hints", "template-403", "missing-vm", "missing-disk", "missing-job", "retain-node", "retain-vm", "retain-template", "delete-history", "foreign-controller", "foreign-connection", "foreign-partition", "foreign-kind", "duplicate-impact", "compute-403", "compute-206", "compute-error", "compute-partial", "job-list-loop", "job-detail-404", "new-job", "new-vm"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataprocScenario(t)
			r, _, _, request := dataprocReviewed(t, s)
			index := func(name string) int {
				for i, v := range request.LifecycleImpacts {
					if strings.HasSuffix(v.Asset.Identity.NativeID, "/"+name) {
						return i
					}
				}
				t.Fatal("missing impact", name)
				return 0
			}
			boot := "projects/sample-project/zones/us-central1-a/disks/analytics-m-boot"
			switch mode {
			case "root-uuid":
				s.resources[dpRoot]["clusterUuid"] = "new-uuid"
			case "root-proof":
				delete(request.Asset.Normalized, dataprocProof)
			case "root-secret":
				object(object(s.resources[dpRoot]["config"])["softwareConfig"])["properties"] = map[string]any{"secret": "DATAPROC_PRIVATE_CHANGED"}
			case "root-state":
				object(s.resources[dpRoot]["status"])["state"] = "UNKNOWN_FUTURE"
			case "root-protected":
				s.resources[dpRoot]["labels"] = map[string]any{"protected": "true"}
			case "job-uuid":
				s.resources[dpJob]["jobUuid"] = "recreated"
			case "job-secret":
				object(s.resources[dpJob]["sparkJob"])["args"] = []any{"changed"}
			case "job-state":
				object(s.resources[dpJob]["status"])["state"] = "CANCEL_DONE"
			case "node-config":
				object(s.resources[dpNode]["nodeGroupConfig"])["numInstances"] = 2
			case "vm-id":
				s.resources[dpVM]["id"] = "999"
			case "vm-metadata":
				object(array(object(s.resources[dpVM]["metadata"])["items"])[0])["value"] = "foreign-cluster"
			case "vm-zone":
				s.resources[dpVM]["selfLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/zones/europe-west1-b/instances/analytics-m"
			case "vm-network":
				object(array(s.resources[dpVM]["networkInterfaces"])[0])["network"] = "projects/sample-project/global/networks/foreign"
			case "vm-account":
				object(array(s.resources[dpVM]["serviceAccounts"])[0])["email"] = "other@sample-project.iam.gserviceaccount.com"
			case "vm-protected":
				s.resources[dpVM]["deletionProtection"] = true
			case "boot-retain":
				object(array(s.resources[dpVM]["disks"])[0])["autoDelete"] = false
			case "external-delete":
				object(array(s.resources[dpVM]["disks"])[1])["autoDelete"] = true
			case "disk-recreated":
				s.resources[boot]["id"] = "999"
			case "disk-shared":
				s.resources[boot]["users"] = []any{s.resources[dpVM]["selfLink"], s.resources[dpWorker]["selfLink"]}
			case "mig-template":
				s.resources[dpMIG]["instanceTemplate"] = "projects/sample-project/global/instanceTemplates/foreign"
			case "mig-unstable":
				object(s.resources[dpMIG]["status"])["isStable"] = false
			case "mig-policy":
				s.resources[dpMIG]["statefulPolicy"] = map[string]any{"preservedState": map[string]any{"disks": map[string]any{"boot": map[string]any{"autoDelete": "NEVER"}}}}
			case "template-hints":
				object(array(object(object(s.resources[dpTemplate]["properties"])["metadata"])["items"])[0])["value"] = "foreign"
			case "missing-vm", "missing-disk", "missing-job":
				name := map[string]string{"missing-vm": "analytics-m", "missing-disk": "analytics-m-boot", "missing-job": "spark-1"}[mode]
				i := index(name)
				request.LifecycleImpacts = append(request.LifecycleImpacts[:i], request.LifecycleImpacts[i+1:]...)
			case "retain-node", "retain-vm", "retain-template":
				name := map[string]string{"retain-node": "aux-real-17", "retain-vm": "analytics-m", "retain-template": "analytics-template"}[mode]
				request.LifecycleImpacts[index(name)].Delete = false
			case "delete-history":
				request.LifecycleImpacts[index("spark-1")].Delete = true
			case "foreign-controller":
				request.LifecycleImpacts[0].ControllerID = "foreign"
			case "foreign-connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign-partition":
				request.LifecycleImpacts[0].Asset.Identity.Partition = "foreign"
			case "foreign-kind":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = "compute.googleapis.com/Network"
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "new-job":
				copy := roundTripDataformJSON(t, s.resources[dpJob])
				object(copy["reference"])["jobId"] = "spark-new"
				copy["jobUuid"] = "new-job-uuid"
				s.resources[strings.Replace(dpJob, "spark-1", "spark-new", 1)] = copy
			case "new-vm":
				copy := roundTripDataformJSON(t, s.resources[dpWorker])
				copy["id"] = "777"
				copy["name"] = "orphan"
				id := strings.Replace(dpWorker, "analytics-w", "orphan", 1)
				copy["selfLink"] = "https://www.googleapis.com/compute/v1/" + id
				copy["disks"] = []any{}
				s.resources[id] = copy
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				name := strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/compute/v1/"), "/v1/")
				if mode == "template-403" && name == dpTemplate {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				if mode == "job-detail-404" && name == dpJob {
					return dataformResponse(req, 404, map[string]any{}), true
				}
				if mode == "job-list-loop" && strings.HasSuffix(name, "/jobs") {
					return dataformResponse(req, 200, map[string]any{"nextPageToken": "same"}), true
				}
				if !strings.HasPrefix(name, "projects/sample-project/aggregated/") {
					return nil, false
				}
				switch mode {
				case "compute-403":
					return dataformResponse(req, 403, map[string]any{}), true
				case "compute-206":
					return dataformResponse(req, 206, map[string]any{}), true
				case "compute-error":
					return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 403, "message": "DATAPROC_PRIVATE_ERROR"}}), true
				case "compute-partial":
					return dataformResponse(req, 200, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"warning": map[string]any{"code": "UNREACHABLE"}}}}), true
				}
				return nil, false
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err == nil {
				_, err = driver.Execute(context.Background(), request)
			}
			if err == nil || len(s.mutations) > 0 {
				t.Fatalf("invalid cleanup accepted: %v %v", err, s.mutations)
			}
			if strings.Contains(err.Error(), "DATAPROC_PRIVATE_") {
				t.Fatal("native error escaped")
			}
		})
	}
}

func TestDataprocWaitValidatesOperationAndRetainedHistory(t *testing.T) {
	for _, mode := range []string{"wrong-operation", "wrong-region", "wrong-host", "metadata-uuid", "metadata-name", "metadata-type", "done-type", "failed", "forged-phase", "forged-uuid", "root-recreated", "member-recreated", "history-missing", "history-recreated", "new-history", "429", "404-pending"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataprocScenario(t)
			r, _, _, request := dataprocReviewed(t, s)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			phase, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			s.operation["done"] = true
			switch mode {
			case "wrong-operation":
				s.operation["name"] = strings.Replace(dpOperation, "delete-analytics", "unrelated", 1)
			case "wrong-region":
				s.operation["name"] = strings.Replace(dpOperation, "us-central1", "europe-west1", 1)
			case "wrong-host":
				phase.ProviderOperationID = strings.Replace(phase.ProviderOperationID, "dataproc.googleapis.com", "attacker.example", 1)
				phase.Data["operation"] = phase.ProviderOperationID
			case "metadata-uuid":
				object(s.operation["metadata"])["clusterUuid"] = "foreign"
			case "metadata-name":
				object(s.operation["metadata"])["clusterName"] = "foreign"
			case "metadata-type":
				s.operation["metadata"] = "bad"
			case "done-type":
				s.operation["done"] = "true"
			case "failed":
				s.operation["error"] = map[string]any{"code": 9, "message": "DATAPROC_PRIVATE_ERROR"}
			case "forged-phase":
				phase.Data["phase"] = "other"
			case "forged-uuid":
				phase.Data["uuid"] = "foreign"
			case "root-recreated":
				s.resources[dpRoot]["clusterUuid"] = "recreated"
			case "member-recreated":
				s.resources[dpVM]["id"] = "999"
			case "history-missing":
				delete(s.resources, dpJob)
			case "history-recreated":
				s.resources[dpJob]["jobUuid"] = "recreated"
			case "new-history":
				copy := roundTripDataformJSON(t, s.resources[dpJob])
				object(copy["reference"])["jobId"] = "new"
				s.resources[strings.Replace(dpJob, "spark-1", "new", 1)] = copy
			case "429":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/operations/delete-analytics") {
						response := dataformResponse(req, 429, map[string]any{})
						response.Header.Set("Retry-After", "7")
						return response, true
					}
					return nil, false
				}
			case "404-pending":
				s.operation = nil
			}
			wait, err := driver.Wait(context.Background(), request, phase)
			if mode == "404-pending" {
				if err != nil || wait.Done {
					t.Fatalf("expired operation skipped resource verification %+v %v", wait, err)
				}
				return
			}
			if err == nil || wait.Done || len(s.mutations) != 1 {
				t.Fatalf("invalid operation accepted %+v %v %v", wait, err, s.mutations)
			}
			if mode == "429" {
				var provider *contracts.ProviderCallError
				if !errors.As(err, &provider) || provider.Provider.Category != execution.ErrorThrottled || provider.RetryAfter != 7*time.Second || provider.Provider.RequestID == "" {
					t.Fatalf("lost throttle metadata %+v", err)
				}
			}
		})
	}
}

func TestDataprocStandaloneComputeRequiresOriginalClusterAbsence(t *testing.T) {
	for _, name := range []string{dpVM, dpMIG, dpTemplate} {
		t.Run(last(name), func(t *testing.T) {
			s := newDataprocScenario(t)
			r, assets, _, _ := dataprocReviewed(t, s)
			value := batchAsset(assets, name)
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			check, err := driver.Preflight(context.Background(), request)
			if err == nil && check.Allowed {
				t.Fatal("standalone deletion bypassed Dataproc cluster")
			}
			c, err := r.resolve(context.Background(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			delete(s.resources, dpRoot)
			owned, err := c.dataprocComputeHasCluster(context.Background(), value.Identity.NativeType, s.resources[name])
			if err != nil || owned {
				t.Fatalf("orphan remains bound to nonexistent cluster %v %v", owned, err)
			}
			s.resources[dpRoot] = map[string]any{"clusterName": "analytics", "projectId": "sample-project", "clusterUuid": "new-uuid"}
			owned, err = c.dataprocComputeHasCluster(context.Background(), value.Identity.NativeType, s.resources[name])
			if err != nil || owned {
				t.Fatalf("orphan bound to new cluster %v %v", owned, err)
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(req.URL.Path, "/clusters/analytics") {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				return nil, false
			}
			if _, err = c.dataprocComputeHasCluster(context.Background(), value.Identity.NativeType, s.resources[name]); err == nil {
				t.Fatal("permissions treated as original cluster absence")
			}
		})
	}
}

func TestDataprocNativePolicyAndVersionedTemplateDeletion(t *testing.T) {
	for _, kind := range []string{dataprocPolicyType, dataprocTemplateType} {
		t.Run(kind, func(t *testing.T) {
			s := newDataprocScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r)
			var value asset.Asset
			for _, item := range assets {
				if item.Identity.NativeType == kind {
					value = item
					break
				}
			}
			if value.ID == "" {
				t.Fatal("native resource missing")
			}
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			phase, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(context.Background(), request, phase)
			if err != nil || !wait.Done {
				t.Fatalf("native delete wait %+v %v", wait, err)
			}
			read, err := driver.Readback(context.Background(), request)
			if err != nil || read.Exists || len(s.mutations) != 1 {
				t.Fatalf("native deletion %+v %v %v", read, err, s.mutations)
			}
			if kind == dataprocTemplateType {
				raw, _ := json.Marshal(value)
				if strings.Contains(string(raw), "DATAPROC_PRIVATE_") {
					t.Fatal("workflow commands leaked")
				}
			}
		})
	}
}

func TestDataprocMissingResourceStillChecksActionIdentity(t *testing.T) {
	for _, mode := range []string{"native-id", "connection", "partition", "provider", "kind", "proof"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataprocScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r)
			value := batchAsset(assets, dpJob)
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			delete(s.resources, dpJob)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			switch mode {
			case "native-id":
				request.Asset.Identity.NativeID = strings.Replace(request.Asset.Identity.NativeID, "spark-1", "other", 1)
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "partition":
				request.Asset.Identity.Partition = "other"
			case "provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "kind":
				request.Asset.Identity.NativeType = dataprocPolicyType
			case "proof":
				delete(request.Asset.Normalized, dataprocProof)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("404 bypassed action binding")
			}
			if _, err := driver.Wait(context.Background(), request, contracts.ActionResult{}); err == nil {
				t.Fatal("empty wait bypassed action binding")
			}
			if _, err := driver.Readback(context.Background(), request); err == nil {
				t.Fatal("readback bypassed action binding")
			}
			if len(s.mutations) != 0 {
				t.Fatal("invalid action wrote to cloud")
			}
		})
	}
}
