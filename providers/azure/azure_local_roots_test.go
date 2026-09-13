package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type localRootFixture struct {
	*localVMFixture
	id, kind                       string
	rootDeletes, rootPolls, status int
	retain                         bool
}

func newLocalRootFixture(t *testing.T, kind string) *localRootFixture {
	t.Helper()
	f := &localRootFixture{localVMFixture: newLocalVMFixture(t), kind: kind, status: 202}
	f.id = f.ids[kind]
	if kind == azureLocalDiskType {
		f.id = f.dataDisk
	}
	previous := f.override
	endpoint := strings.ReplaceAll(localCleanupPollURL("Azure-AsyncOperation"), "11111111-2222-3333-4444-555555555555", azureRequestID(f.id))
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && req.URL.String() == endpoint {
			f.rootPolls++
			if !f.retain {
				delete(f.values, f.id)
			}
			return jsonResponse(200, map[string]any{"status": "Succeeded", "resourceId": f.id}, nil), true
		}
		if req.Method == "DELETE" && strings.ToLower(req.URL.Path) == f.id {
			body, _ := io.ReadAll(req.Body)
			if req.URL.Query().Get("api-version") != azureLocalVersion || len(req.URL.Query()) != 1 || len(body) != 0 || req.Header.Get("If-Match") != "" || req.Header.Get("X-Ms-Client-Request-Id") == "" {
				t.Fatal("invalid native root DELETE")
			}
			for id, raw := range f.values {
				if raw["type"] != azureLocalVMType {
					continue
				}
				refs, err := azureLocalReferences(id, azureLocalVMType, raw)
				if err != nil || !azureLocalImage(kind) && slices.Contains(refs[kind], f.id) {
					t.Fatal("deleted attached resource", kind, err)
				}
			}
			f.rootDeletes++
			if f.rootDeletes != 1 {
				t.Fatal("root DELETE replayed")
			}
			if f.status != 202 {
				return &http.Response{StatusCode: f.status, Header: http.Header{}, Body: http.NoBody}, true
			}
			object(f.values[f.id]["properties"])["provisioningState"] = "Deleting"
			f.values[f.id]["etag"] = "root-delete"
			return &http.Response{StatusCode: 202, Header: http.Header{"Azure-Asyncoperation": {endpoint}}, Body: http.NoBody}, true
		}
		return previous(req)
	}
	return f
}

func (f *localRootFixture) requestAsset(t *testing.T) contracts.ActionRequest {
	value := f.asset(t, f.kind, f.id)
	value.ID = asset.AssetID("root-" + last(f.kind))
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "local-root"}
}
func (f *localRootFixture) detach() {
	vm := object(f.values[f.ids[azureLocalVMType]]["properties"])
	if f.kind == azureLocalDiskType {
		object(vm["storageProfile"])["dataDisks"] = []any{}
	} else {
		object(vm["networkProfile"])["networkInterfaces"] = []any{}
	}
}

func TestAzureLocalRootsOwnReadbackRecovery(t *testing.T) {
	for _, kind := range []string{azureLocalDiskType, azureLocalNICType} {
		for _, status := range []int{202, 204, 404} {
			t.Run(kind+http.StatusText(status), func(t *testing.T) {
				f := newLocalRootFixture(t, kind)
				request := f.requestAsset(t)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := driver.Execute(t.Context(), request); err == nil {
					t.Fatal("attached resource deletion allowed")
				}
				f.detach()
				f.status, f.retain = status, true
				result, err := driver.Execute(t.Context(), request)
				if err != nil || f.rootDeletes != 1 {
					t.Fatal("native root DELETE", err)
				}
				for range 3 {
					payload, _ := json.Marshal(result)
					_ = json.Unmarshal(payload, &result)
					fresh, _ := NewRuntime(f.runtime.credentials)
					fresh.transport = f.runtime.transport
					driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						t.Fatal(err)
					}
					request.ExecutionResult = &result
					if _, err := driver.Execute(t.Context(), request); err != nil || f.rootDeletes != 1 {
						t.Fatal("restart replayed root DELETE", err)
					}
					wait, err := driver.Wait(t.Context(), request, result)
					if err != nil || wait.Done {
						t.Fatal("response completed a live root", wait, err)
					}
					result.Data = wait.Data
				}
				delete(f.values, f.id)
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal("own absence did not complete root", wait, err)
				}
				if f.values[f.ids[azureLocalVMType]] == nil || f.vmDeletes != 0 || f.deleted != 0 || len(f.arc.deleted) != 0 {
					t.Fatal("root cleanup removed other resources")
				}
			})
		}
	}
}

func TestAzureLocalRootsReviewAndConsumerBoundaries(t *testing.T) {
	for _, kind := range []string{azureLocalDiskType, azureLocalNICType} {
		for _, mode := range []string{"configuration", "etag", "tag", "group", "lock", "denied-root", "denied-vm", "denied-index", "omitted-parent", "new-reference", "forged", "parameters", "impact", "foreign-prerequisite", "retained-prerequisite", "duplicate-prerequisite", "malformed-vms"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalRootFixture(t, kind)
				request := f.requestAsset(t)
				vm := f.asset(t, azureLocalVMType)
				f.detach()
				switch mode {
				case "configuration":
					object(f.values[f.id]["properties"])["futurePrivateConfiguration"] = "changed"
				case "etag":
					f.values[f.id]["etag"] = "replaced"
				case "tag":
					f.values[f.id]["tags"] = map[string]any{"steward:protected": "true"}
				case "group":
					f.group["tags"] = map[string]any{"steward:protected": "true"}
				case "lock":
					f.locks = []any{map[string]any{"id": f.id + "/providers/microsoft.authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "omitted-parent", "new-reference":
					if kind == azureLocalDiskType {
						object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"])["dataDisks"] = []any{map[string]any{"id": f.id}}
					} else {
						object(object(f.values[f.ids[azureLocalVMType]]["properties"])["networkProfile"])["networkInterfaces"] = []any{map[string]any{"id": f.id}}
					}
					if mode == "omitted-parent" {
						f.omitted[f.ids[hybridMachineType]] = true
					}
				case "forged":
					object(request.Asset.Normalized[azureLocalCleanup])["vms"] = []string{}
				case "parameters":
					request.Parameters = map[string]any{"force": true}
				case "impact":
					request.LifecycleImpacts = []contracts.ActionImpact{{Asset: vm, ControllerID: request.Asset.ID, Delete: true}}
				case "foreign-prerequisite", "retained-prerequisite", "duplicate-prerequisite":
					member := contracts.ActionImpact{Asset: vm, ControllerID: request.Asset.ID, Delete: true}
					if mode == "foreign-prerequisite" {
						member.Asset.Identity.ConnectionID = "other"
					}
					if mode == "retained-prerequisite" {
						member.Delete = false
					}
					request.PrerequisiteDeletions = []contracts.ActionImpact{member}
					if mode == "duplicate-prerequisite" {
						request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, member)
					}
				case "malformed-vms":
					state := object(request.Asset.Normalized[azureLocalCleanup])
					state["vms"] = "not-a-list"
					request.Asset.Normalized[azureLocalCleanupProof] = f.client.azureLocalRootBinding(f.id, "connection", state)
				default:
					denied := f.id
					if mode == "denied-vm" {
						denied = f.ids[azureLocalVMType]
					}
					if mode == "denied-index" {
						denied = "/subscriptions/" + testSubscription + "/providers/microsoft.hybridcompute/machines"
					}
					previous := f.override
					f.override = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.ToLower(req.URL.Path) == denied {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return previous(req)
					}
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err == nil {
					_, err = driver.Execute(t.Context(), request)
				}
				if err == nil || f.rootDeletes != 0 {
					t.Fatal("root boundary bypassed", mode, err)
				}
			})
		}
	}
}

func TestAzureLocalRootsPlanExplicitConsumers(t *testing.T) {
	for _, kind := range []string{azureLocalDiskType, azureLocalNICType} {
		for _, mode := range []string{"root-only", "both", "retain-vm", "protect-vm", "missing-vm", "detached"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalRootFixture(t, kind)
				if mode == "detached" {
					f.detach()
				}
				root, vm := f.requestAsset(t), f.vmRequest(t)
				values := []asset.Asset{root.Asset, vm.Asset}
				for _, member := range append(vm.PrerequisiteDeletions, vm.LifecycleImpacts...) {
					values = append(values, member.Asset)
				}
				if mode == "missing-vm" {
					values = append(values[:1], values[2:]...)
				}
				cascades := &serviceCascades{client: f.client, connectionID: "connection"}
				contribution := governance.Contribution{}
				if err := cascades.contributeAzureLocalRoots(t.Context(), values, &contribution); err != nil {
					t.Fatal(err)
				}
				if mode == "missing-vm" {
					if len(contribution.Unresolved) == 0 {
						t.Fatal("missing consumer inventory ignored")
					}
					return
				}
				if err := cascades.contributeAzureLocalVMs(t.Context(), values, &contribution); err != nil {
					t.Fatal(err)
				}
				input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.Asset.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
				if mode == "both" || mode == "protect-vm" {
					input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, vm.Asset.ID)
				}
				if mode == "protect-vm" {
					input.Protections = []plan.ProtectionPolicy{{AssetID: vm.Asset.ID, Protected: true}}
				}
				if mode == "retain-vm" {
					input.RequestOptions = map[asset.AssetID]map[string]any{root.Asset.ID: {"retain_resources": []string{vm.Asset.Identity.NativeID}}}
				}
				solved, err := plan.Solve(input)
				allowed := mode == "both" || mode == "detached"
				if err != nil || (len(solved.Blockers) == 0) != allowed {
					t.Fatal("consumer selection", mode, err, solved.Blockers)
				}
				if mode == "both" {
					if len(solved.Steps) != 6 || len(solved.ImpactItems) != 2 || solved.Steps[5].AssetID != root.Asset.ID {
						t.Fatal("root ordering", solved)
					}
					required, err := plan.RequiredDeletions(solved.Steps[5])
					if err != nil || len(required) != 1 {
						t.Fatal("reviewed consumer missing", required, err)
					}
				}
				if mode == "detached" && (len(solved.Steps) != 1 || len(solved.ImpactItems) != 0) {
					t.Fatal("detached root cascaded", solved)
				}
				for _, binding := range contribution.Bindings {
					if binding.ManagedAssetID == root.Asset.ID && binding.CleanupPolicy == graph.CleanupDelegate {
						t.Fatal("data disk/NIC delegated to VM")
					}
				}
			})
		}
	}
}

func TestAzureLocalRootsInventoryHistory(t *testing.T) {
	for _, kind := range []string{azureLocalDiskType, azureLocalNICType} {
		for _, mode := range []string{"omitted", "deleted-vm", "removed-context", "removed-proof", "removed-both", "removed-prefix"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalRootFixture(t, kind)
				saved := f.requestAsset(t).Asset
				f.omitted[f.ids[hybridMachineType]] = true
				request := f.request(kind)
				request.KnownNativeIDs = []string{f.id}
				request.KnownNativeMetadata = map[string]map[string]any{f.id: saved.Normalized}
				if mode == "deleted-vm" {
					delete(f.values, f.ids[azureLocalVMType])
					delete(f.values, f.ids[hybridMachineType])
				}
				if mode == "removed-context" || mode == "removed-both" || mode == "removed-prefix" {
					delete(saved.Normalized, azureLocalCleanup)
				}
				if mode == "removed-proof" || mode == "removed-both" || mode == "removed-prefix" {
					delete(saved.Normalized, azureLocalCleanupProof)
				}
				if mode == "removed-prefix" {
					saved.Normalized["_azure_local_configuration"] = strings.TrimPrefix(text(saved.Normalized["_azure_local_configuration"]), azureLocalRootPrefix)
				}
				batch, err := f.runtime.List(t.Context(), request)
				allowed := mode == "omitted" || mode == "deleted-vm"
				if !allowed {
					if err == nil {
						t.Fatal("missing root history accepted", mode)
					}
					return
				}
				if err != nil {
					t.Fatal("known consumer scan", err)
				}
				var found bool
				for _, item := range batch.Items {
					if item.NativeID != f.id {
						continue
					}
					found = true
					if !slices.Contains(stringValues(object(item.Normalized[azureLocalCleanup])["vms"]), f.ids[azureLocalVMType]) {
						t.Fatal("known VM identity lost after index omission/404")
					}
					saved.Normalized = item.Normalized
					driver, err := f.runtime.ResolveAction(t.Context(), "connection", saved)
					if err != nil {
						t.Fatal(err)
					}
					check, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: saved, Action: "delete"})
					if mode == "omitted" {
						if err == nil {
							t.Fatal("omitted VM reference allowed cleanup")
						}
					} else if err != nil || !check.Allowed {
						t.Fatal("removed VM still blocked resource", check, err)
					}
				}
				if !found {
					t.Fatal("known root disappeared")
				}
			})
		}
	}
}

func TestAzureLocalRootsLegacyRescanAndVMImpact(t *testing.T) {
	for _, kind := range []string{azureLocalDiskType, azureLocalNICType, azureLocalImageType, azureLocalMarketplaceType} {
		t.Run(kind, func(t *testing.T) {
			f := newLocalRootFixture(t, kind)
			vm := f.vmRequest(t)
			value := f.requestAsset(t).Asset
			if kind == azureLocalDiskType {
				value = vm.LifecycleImpacts[1].Asset
			}
			refs, err := f.client.azureLocalRecordedReferences(value)
			if err != nil {
				t.Fatal(err)
			}
			configuration := strings.TrimPrefix(text(value.Normalized["_azure_local_configuration"]), azureLocalRootPrefix)
			value.Normalized["_azure_local_configuration"] = configuration
			value.Normalized["_azure_local_reference_binding"] = f.client.privateConfiguration(map[string]any{"id": value.Identity.NativeID, "connection": value.Identity.ConnectionID, "configuration": configuration, "references": refs})
			if kind == azureLocalDiskType {
				state := object(value.Normalized[azureLocalCleanup])
				delete(state, "vms")
				state["inventory"] = configuration
				value.Normalized[azureLocalCleanupProof] = f.client.azureLocalRootBinding(value.Identity.NativeID, value.Identity.ConnectionID, state)
				if err := f.client.azureLocalRootRecord(value); err != nil {
					t.Fatal("legacy VM disk impact rejected", err)
				}
				vm.LifecycleImpacts[1].Asset = value
				driver := f.driver(t, vm)
				if read, err := driver.Readback(t.Context(), vm); err != nil || !read.Exists {
					t.Fatal("pending VM impact lost compatibility", read, err)
				}
			} else {
				delete(value.Normalized, azureLocalCleanup)
				delete(value.Normalized, azureLocalCleanupProof)
				delete(value.Normalized, "cleanup_protected")
				delete(value.Normalized, "cleanup_protection_reason")
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
				t.Fatal("legacy record authorized independent cleanup")
			}
			request := f.request(kind)
			request.KnownNativeIDs = []string{value.Identity.NativeID}
			request.KnownNativeMetadata = map[string]map[string]any{value.Identity.NativeID: value.Normalized}
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal("legacy record could not rescan", err)
			}
			upgraded := false
			for _, item := range batch.Items {
				if item.NativeID == value.Identity.NativeID {
					upgraded = len(object(item.Normalized[azureLocalCleanup])) == 6 && *item.Actionable
				}
			}
			if !upgraded {
				t.Fatal("legacy root did not acquire verified consumer context")
			}
		})
	}
}
