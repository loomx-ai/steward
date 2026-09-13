package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type localNetworkCleanupFixture struct {
	*localRootFixture
	workload string
	deletes  map[string]int
}

func newLocalNetworkCleanupFixture(t *testing.T, role string) *localNetworkCleanupFixture {
	t.Helper()
	f := &localNetworkCleanupFixture{localRootFixture: newLocalRootFixture(t, azureLocalNetworkType), deletes: map[string]int{}}
	object(f.values[f.id]["properties"])["networkType"] = role
	if role == "Infrastructure" {
		f.workload = f.id + "-workload"
		raw := batchClone(f.values[f.id])
		raw["id"], raw["name"] = f.workload, last(f.workload)
		object(raw["properties"])["networkType"] = "Workload"
		f.values[f.workload] = raw
		configs := array(object(f.values[f.ids[azureLocalNICType]]["properties"])["ipConfigurations"])
		for _, config := range configs {
			object(object(object(config)["properties"])["subnet"])["id"] = f.workload
		}
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "DELETE" && (id == f.ids[azureLocalNICType] || f.workload != "" && id == f.workload) {
			version := "2024-01-01"
			if id == f.workload {
				version = "2025-06-01-preview"
			}
			if req.URL.Query().Get("api-version") != version || len(req.URL.Query()) != 1 || req.ContentLength > 0 {
				t.Fatal("invalid network prerequisite request", req.URL)
			}
			if f.values[f.ids[azureLocalVMType]] != nil {
				t.Fatal("deleted network prerequisite before VM")
			}
			if id == f.workload && f.values[f.ids[azureLocalNICType]] != nil {
				t.Fatal("deleted workload network before NIC")
			}
			f.deletes[id]++
			if f.deletes[id] != 1 {
				t.Fatal("replayed network prerequisite", id)
			}
			delete(f.values, id)
			return jsonResponse(204, nil, http.Header{"Azure-Asyncoperation": {"http://azure.async.operation/status"}, "X-Ms-Request-Id": {azureRequestID(id)}}), true
		}
		if req.Method == "DELETE" && id == f.id {
			if f.values[f.ids[azureLocalNICType]] != nil || f.workload != "" && f.values[f.workload] != nil {
				t.Fatal("deleted network before native consumers")
			}
			if role == "Infrastructure" && f.values[f.ids[azureLocalVMType]] != nil {
				t.Fatal("deleted infrastructure before VM")
			}
		}
		return previous(req)
	}
	return f
}

func (f *localNetworkCleanupFixture) clearConsumers() {
	delete(f.values, f.ids[azureLocalNICType])
	delete(f.values, f.workload)
	delete(f.values, f.ids[azureLocalVMType])
}

func TestAzureLocalNetworkCleanupOwnReadback(t *testing.T) {
	for _, role := range []string{"Workload", "Infrastructure"} {
		for _, status := range []int{202, 204, 404} {
			t.Run(role+"/"+http.StatusText(status), func(t *testing.T) {
				f := newLocalNetworkCleanupFixture(t, role)
				f.status = status
				nicBefore := batchClone(f.values[f.ids[azureLocalNICType]])
				request := f.requestAsset(t)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := driver.Preflight(t.Context(), request); err == nil {
					t.Fatal("in-use network allowed")
				}
				f.clearConsumers()
				if result, err := driver.Preflight(t.Context(), request); err != nil || !result.Allowed {
					t.Fatal(result, err)
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				request.ExecutionResult = &result
				// A terminal HTTP status/poll cannot prove absence. Restart the runtime and
				// serialize the exact receipt repeatedly without replaying the DELETE.
				f.retain = true
				for range 3 {
					data, _ := json.Marshal(result)
					if json.Unmarshal(data, &result) != nil {
						t.Fatal("restore")
					}
					request.ExecutionResult = &result
					fresh, err := NewRuntime(f.runtime.credentials)
					if err != nil {
						t.Fatal(err)
					}
					fresh.transport = f.runtime.transport
					driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						t.Fatal(err)
					}
					wait, err := driver.Wait(t.Context(), request, result)
					if err != nil {
						t.Fatal(err)
					}
					if wait.Done {
						t.Fatal("live network completed")
					}
					result.Data = wait.Data
					own, err := driver.Readback(t.Context(), request)
					if err != nil || !own.Exists {
						t.Fatal("network closed before own absence", own, err)
					}
				}
				delete(f.values, f.id)
				f.values[f.ids[azureLocalNICType]] = nicBefore
				residual, err := driver.Readback(t.Context(), request)
				if err != nil || !residual.Exists {
					t.Fatal("network own absence hid a surviving NIC", residual, err)
				}
				delete(f.values, f.ids[azureLocalNICType])
				own, err := driver.Readback(t.Context(), request)
				if err != nil || own.Exists {
					t.Fatal("network absence", own, err)
				}
				if f.rootDeletes != 1 || len(f.deletes) != 0 {
					t.Fatal("cleanup replay/cascade", f.rootDeletes, f.deletes)
				}
			})
		}
	}
}

func TestAzureLocalNetworkGraphExplicitPrerequisites(t *testing.T) {
	for _, role := range []string{"Workload", "Infrastructure"} {
		t.Run(role, func(t *testing.T) {
			f := newLocalNetworkCleanupFixture(t, role)
			// Use the registered scan/graph/solver so reverse requirements remain
			// explicit and a retained NIC cannot be hidden by VM-managed impacts.
			repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
			values := azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalNICType, azureLocalDiskType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType}, false, true)
			var target asset.Asset
			for _, value := range values {
				if value.Identity.NativeID == f.id {
					target = value
				}
			}
			relationships, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			dependencies := 0
			for _, edge := range relationships {
				if edge.SourceAssetID == target.ID && edge.Type == graph.RelationshipDependsOn {
					if edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true {
						t.Fatal("network auto-selected consumers", edge)
					}
					dependencies++
				}
			}
			want := 1
			if role == "Infrastructure" {
				want = 3
			}
			if dependencies != want {
				t.Fatal("network dependencies", dependencies, want)
			}
			values = azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repo, registry, []string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType}, false, true)
			service := cleanup.NewService(repo, registry)
			for _, mode := range []string{"network-only", "all", "retain-nic"} {
				selectors := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: target.ID}}
				if mode != "network-only" {
					for _, v := range values {
						if v.Identity.NativeType == azureLocalVMType || v.Identity.NativeType == azureLocalNICType || v.Identity.NativeID == f.workload {
							selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: v.ID})
						}
					}
				}
				request := cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"}
				if mode == "retain-nic" {
					request.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{f.ids[azureLocalNICType]}}}
				}
				task, err := service.CreateTask(t.Context(), request)
				if err != nil || (task.Task.Status == plan.StatusReady) != (mode == "all") {
					t.Fatal(mode, task.Task.Status, task.Task.Blockers, err)
				}
			}
		})
	}
}

func TestAzureLocalNetworkHistoryAndScope(t *testing.T) {
	for _, role := range []string{"Workload", "Infrastructure"} {
		for _, mode := range []string{"known-omission", "lowercase-type", "new-nic", "unknown-placement", "known-other-network", "unknown-location", "other-location", "role-change", "location-change", "history-tamper", "devices-without-index", "stale-devices"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				f := newLocalNetworkCleanupFixture(t, role)
				if mode == "devices-without-index" || mode == "stale-devices" {
					object(f.values[f.id]["properties"])["subnets"] = []any{map[string]any{"properties": map[string]any{"ipConfigurationReferences": []any{}}}}
				}
				request := f.requestAsset(t)
				nic := batchClone(f.values[f.ids[azureLocalNICType]])
				f.clearConsumers()
				allowed := false
				switch mode {
				case "lowercase-type":
					nic["type"] = strings.ToLower(azureLocalNICType)
					f.values[f.ids[azureLocalNICType]] = nic
				case "known-omission":
					f.values[f.ids[azureLocalNICType]] = nic
					f.omitted[f.ids[azureLocalNICType]] = true
				case "new-nic":
					id := f.ids[azureLocalNICType] + "-new"
					nic["id"], nic["name"] = id, last(id)
					f.values[id] = nic
				case "unknown-placement":
					delete(object(nic["properties"]), "ipConfigurations")
					f.values[f.ids[azureLocalNICType]] = nic
				case "known-other-network":
					for _, config := range array(object(nic["properties"])["ipConfigurations"]) {
						object(object(object(config)["properties"])["subnet"])["id"] = f.id + "-elsewhere"
					}
					f.values[f.ids[azureLocalNICType]] = nic
					allowed = role == "Workload"
				case "unknown-location":
					delete(nic, "extendedLocation")
					f.values[f.ids[azureLocalNICType]] = nic
				case "other-location":
					object(nic["extendedLocation"])["name"] = text(object(nic["extendedLocation"])["name"]) + "-other"
					f.values[f.ids[azureLocalNICType]] = nic
					allowed = role == "Infrastructure"
				case "role-change":
					object(f.values[f.id]["properties"])["networkType"] = "Unknown"
				case "location-change":
					object(f.values[f.id]["extendedLocation"])["name"] = text(object(f.values[f.id]["extendedLocation"])["name"]) + "-other"
				case "history-tamper":
					object(request.Asset.Normalized[azureLocalCleanup])["resources"] = []string{}
				case "devices-without-index", "stale-devices":
					object(f.values[f.id]["properties"])["subnets"] = []any{map[string]any{"properties": map[string]any{"ipConfigurationReferences": []any{map[string]any{"ID": f.ids[azureLocalNICType]}}}}}
					if mode == "devices-without-index" {
						f.values[f.ids[azureLocalNICType]] = nic
						f.omitted[f.ids[azureLocalNICType]] = true
					}
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err == nil {
					_, err = driver.Preflight(t.Context(), request)
				}
				if (err == nil) != allowed {
					t.Fatal("network boundary", allowed, err)
				}
				if f.rootDeletes != 0 {
					t.Fatal("boundary issued deletion")
				}
			})
		}
	}
}

func TestAzureLocalNetworkRecordedHistoryBoundaries(t *testing.T) {
	for _, mode := range []string{"legacy", "missing", "wrong-kind", "foreign", "duplicate", "wrong-shape", "unknown-role", "parameters", "impact", "etag", "group", "lock"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalNetworkCleanupFixture(t, "Workload")
			request := f.requestAsset(t)
			state := object(request.Asset.Normalized[azureLocalCleanup])
			switch mode {
			case "legacy":
				delete(request.Asset.Normalized, azureLocalCleanup)
				delete(request.Asset.Normalized, azureLocalCleanupProof)
				request.Asset.Normalized["_azure_local_configuration"] = strings.TrimPrefix(text(request.Asset.Normalized["_azure_local_configuration"]), azureLocalRootPrefix)
				request.Asset.Normalized["_azure_local_reference_binding"] = f.client.privateConfiguration(map[string]any{"id": f.id, "connection": "connection", "configuration": request.Asset.Normalized["_azure_local_configuration"], "references": request.Asset.Normalized["_azure_local_references"]})
				if _, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset); err == nil {
					t.Fatal("legacy network authorized cleanup")
				}
				scan := f.request(azureLocalNetworkType)
				scan.KnownNativeIDs = []string{f.id}
				scan.KnownNativeMetadata = map[string]map[string]any{f.id: request.Asset.Normalized}
				batch, err := f.runtime.List(t.Context(), scan)
				if err != nil || len(batch.Items) != 1 || len(object(batch.Items[0].Normalized[azureLocalCleanup])) != 8 {
					t.Fatal("legacy network rescan", batch, err)
				}
				return
			case "missing":
				delete(state, "resources")
			case "wrong-kind":
				state["resources"] = []string{f.dataDisk}
			case "foreign":
				state["resources"] = []string{strings.Replace(f.id, testSubscription, testTenant, 1)}
			case "duplicate":
				state["resources"] = []string{f.id, f.id}
			case "wrong-shape":
				state["resources"] = false
			case "unknown-role":
				state["network_type"] = "Unknown"
				request.Asset.Normalized["networkType"] = "Unknown"
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			case "impact":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: f.asset(t, azureLocalNICType), ControllerID: request.Asset.ID, Delete: true}}
			case "etag":
				f.values[f.id]["etag"] = "changed"
			case "group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			if slices.Contains([]string{"missing", "wrong-kind", "foreign", "duplicate", "wrong-shape", "unknown-role"}, mode) {
				request.Asset.Normalized[azureLocalCleanupProof] = f.client.azureLocalRootBinding(f.id, "connection", state)
			}
			f.clearConsumers()
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err == nil {
				_, err = driver.Execute(t.Context(), request)
			}
			if err == nil || f.rootDeletes != 0 {
				t.Fatal("network review boundary", mode, err)
			}
		})
	}
}
