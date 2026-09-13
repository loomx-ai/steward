package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *localVMFixture) registrationRequest(t *testing.T) contracts.ActionRequest {
	t.Helper()
	request := contracts.ActionRequest{Asset: f.asset(t, hybridMachineType), Action: "delete", IdempotencyKey: "local-registration"}
	for _, kind := range []string{azureLocalVMType, hybridExtensionType, hybridCommandType, hybridProfileType} {
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: f.asset(t, kind), ControllerID: request.Asset.ID, Delete: true})
	}
	return request
}

func (f *localVMFixture) removeLocalVM() {
	f.removePrerequisites()
	for _, kind := range []string{azureLocalVMType, azureLocalIdentityType, azureLocalDiskType} {
		delete(f.values, f.ids[kind])
	}
}

func TestAzureLocalRegistrationOwnAbsenceAndResume(t *testing.T) {
	f := newLocalVMFixture(t)
	request := f.registrationRequest(t)
	residuals := map[string]map[string]any{}
	for _, kind := range []string{azureLocalVMType, azureLocalIdentityType, azureLocalDiskType} {
		residuals[f.ids[kind]] = batchClone(f.values[f.ids[kind]])
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	f.removePrerequisites()
	for _, kind := range []string{azureLocalVMType, azureLocalIdentityType, azureLocalDiskType} {
		if _, err := driver.Execute(t.Context(), request); err == nil || len(f.arc.deleted) != 0 {
			t.Fatal("surviving Local dependency allowed registration DELETE", kind, err)
		}
		delete(f.values, f.ids[kind])
	}
	f.arc.hold = true
	result, err := driver.Execute(t.Context(), request)
	if err != nil || f.arc.deleted[request.Asset.Identity.NativeID] != 1 {
		t.Fatal("registration DELETE", err)
	}
	payload, _ := json.Marshal(result)
	_ = json.Unmarshal(payload, &result)
	fresh, _ := NewRuntime(f.runtime.credentials)
	fresh.transport = f.runtime.transport
	driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		wait, err := driver.Wait(t.Context(), request, result)
		if err != nil || wait.Done {
			t.Fatal("poll replaced registration readback", wait, err)
		}
		result.Data = wait.Data
	}
	delete(f.values, request.Asset.Identity.NativeID)
	request.ExecutionResult = &result
	for id, raw := range residuals {
		f.values[id] = raw
		if read, err := driver.Readback(t.Context(), request); err != nil || !read.Exists {
			t.Fatal("parent absence hid Local resource", id, read, err)
		}
		if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
			t.Fatal("residual completed registration", id, wait, err)
		}
		delete(f.values, id)
	}

	f.arc.hold = false
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done || f.arc.deleted[request.Asset.Identity.NativeID] != 1 || f.values[f.dataDisk] == nil {
		t.Fatal("registration recovery", wait, err)
	}
}

func TestAzureLocalRegistrationDriftAndDeniedReads(t *testing.T) {
	for _, mode := range []string{"vm", "os", "identity", "new-guest", "new-vm", "registration", "protected", "forged", "foreign-vm", "retained-vm", "duplicate-vm", "missing-context", "denied-vm", "denied-os", "denied-identity", "denied-machine"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.registrationRequest(t)
			switch mode {
			case "vm":
				object(f.values[f.ids[azureLocalVMType]]["properties"])["hardwareProfile"] = map[string]any{"processors": 32}
			case "os":
				f.values[f.ids[azureLocalDiskType]]["tags"] = map[string]any{"changed": "true"}
			case "identity":
				object(f.values[f.ids[azureLocalIdentityType]]["properties"])["vmId"] = "changed"
			case "new-guest":
				f.values[f.ids[azureLocalAgentType]]["etag"] = "changed"
				object(f.values[f.ids[azureLocalAgentType]]["properties"])["newConfiguration"] = true
			case "new-vm":
				object(f.values[f.ids[azureLocalVMType]]["properties"])["guestAgentInstallStatus"] = map[string]any{"vmUuid": "changed"}
			case "registration":
				object(f.values[f.ids[hybridMachineType]]["properties"])["vmId"] = "22222222-2222-3333-4444-555555555555"
			case "protected":
				f.values[f.ids[hybridMachineType]]["tags"] = map[string]any{"steward:protected": "true"}
			case "forged":
				object(object(request.Asset.Normalized[hybridComputeCleanup])["local_vm"])["os_disk"] = ""
			case "foreign-vm":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "retained-vm":
				request.PrerequisiteDeletions[0].Delete = false
			case "duplicate-vm":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "missing-context":
				delete(object(request.Asset.Normalized[hybridComputeCleanup]), "local_vm")
			default:
				denied := map[string]string{"denied-vm": azureLocalVMType, "denied-os": azureLocalDiskType, "denied-identity": azureLocalIdentityType, "denied-machine": hybridMachineType}[mode]
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.ToLower(req.URL.Path) == f.ids[denied] {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return previous(req)
				}
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err == nil {
				if _, readErr := driver.Readback(t.Context(), request); readErr == nil {
					t.Fatal("drift/invalid request was accepted by readback", mode)
				}
				_, err = driver.Execute(t.Context(), request)
			}
			if err == nil || len(f.arc.deleted) != 0 {
				t.Fatal("invalid registration cleanup accepted", err)
			}
		})
	}
}

func TestAzureLocalRegistrationHistory(t *testing.T) {
	for _, mode := range []string{"same", "no-history", "new-registration", "forged", "surviving-os", "surviving-identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			prior := f.asset(t, hybridMachineType)
			disk, identity := f.values[f.ids[azureLocalDiskType]], f.values[f.ids[azureLocalIdentityType]]
			f.removeLocalVM()
			request := f.request(hybridMachineType)
			request.Source = hybridComputeSource
			if mode != "no-history" {
				request.KnownNativeIDs = []string{prior.Identity.NativeID}
				request.KnownNativeMetadata = map[string]map[string]any{prior.Identity.NativeID: prior.Normalized}
			}
			if mode == "new-registration" {
				object(f.values[f.ids[hybridMachineType]]["properties"])["vmId"] = "22222222-2222-3333-4444-555555555555"
			}
			if mode == "forged" {
				object(prior.Normalized[hybridComputeCleanup])["local_vm"] = map[string]any{}
			}
			if mode == "surviving-os" {
				f.values[f.ids[azureLocalDiskType]] = disk
			}
			if mode == "surviving-identity" {
				f.values[f.ids[azureLocalIdentityType]] = identity
			}
			batch, err := f.runtime.List(t.Context(), request)
			if mode == "forged" {
				if err == nil {
					t.Fatal("forged history accepted")
				}
				return
			}
			allowed := mode != "no-history" && mode != "new-registration"
			if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable != allowed {
				t.Fatal("history eligibility", err, mode)
			}
			if !allowed {
				return
			}
			value := prior
			value.Normalized = batch.Items[0].Normalized
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			check, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if mode == "same" {
				if err != nil || !check.Allowed {
					t.Fatal("orphaned registration could not finish", check, err)
				}
				action := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "later-scan"}
				result, err := driver.Execute(t.Context(), action)
				if err != nil {
					t.Fatal("later-scan registration deletion", err)
				}
				done := false
				for range 4 {
					wait, err := driver.Wait(t.Context(), action, result)
					if err != nil {
						t.Fatal(err)
					}
					result.Data = wait.Data
					if wait.Done {
						done = true
						break
					}
				}
				if !done || f.arc.deleted[value.Identity.NativeID] != 1 {
					t.Fatal("later scan did not finish registration")
				}

			} else if err == nil {
				t.Fatal("surviving delegated resource ignored")
			}
		})
	}
}

func TestAzureLocalRegistrationPlan(t *testing.T) {
	for _, mode := range []string{"all", "retain-vm", "protect-vm", "retain-disk", "vm-only", "missing-vm"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request, vm := f.registrationRequest(t), f.vmRequest(t)
			values := []asset.Asset{request.Asset, vm.Asset}
			for _, member := range append(vm.PrerequisiteDeletions, vm.LifecycleImpacts...) {
				values = append(values, member.Asset)
			}
			if mode == "missing-vm" {
				values = append(values[:1], values[2:]...)
			}
			cascades := &serviceCascades{client: f.client, connectionID: "connection"}
			contribution := governance.Contribution{}
			if err := cascades.contributeAzureLocalVMs(t.Context(), values, &contribution); err != nil {
				t.Fatal(err)
			}
			if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-vm" {
				if len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != vm.Asset.Identity.NativeID {
					t.Fatal("missing VM ignored", contribution)
				}
				return
			}
			input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{request.Asset.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
			if mode == "retain-vm" {
				input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_resources": []string{vm.Asset.Identity.NativeID}}}
			}
			if mode == "retain-disk" {
				input.RequestOptions = map[asset.AssetID]map[string]any{vm.Asset.ID: {"retain_resources": []string{vm.LifecycleImpacts[1].Asset.Identity.NativeID}}}
			}
			if mode == "protect-vm" {
				input.Protections = []plan.ProtectionPolicy{{AssetID: vm.Asset.ID, Protected: true}}
			}
			if mode == "vm-only" {
				input.ResolvedAssetIDs = []asset.AssetID{vm.Asset.ID}
			}
			solved, err := plan.Solve(input)
			allowed := mode == "all" || mode == "vm-only"
			if err != nil || (len(solved.Blockers) == 0) != allowed {
				t.Fatal("registration review", err, solved.Blockers)
			}
			if allowed {
				count, last := 6, request.Asset.ID
				if mode == "vm-only" {
					count, last = 5, vm.Asset.ID
				}
				if len(solved.Steps) != count || len(solved.ImpactItems) != 2 || solved.Steps[count-1].AssetID != last {
					t.Fatal("registration ordering", solved)
				}
			}
		})
	}
}

func TestAzureLocalRegistrationProtectionAfterVMRemoval(t *testing.T) {
	for _, mode := range []string{"lock", "group", "denied-group", "denied-locks", "missing-parent", "deleting-parent"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.registrationRequest(t)
			f.removeLocalVM()
			machine := f.ids[hybridMachineType]
			switch mode {
			case "lock":
				f.locks = []any{map[string]any{"id": machine + "/providers/microsoft.authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "missing-parent":
				delete(f.values, machine)
			case "deleting-parent":
				object(f.values[machine]["properties"])["provisioningState"] = "Deleting"
			default:
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if (mode == "denied-group" && path == text(f.group["id"])) || (mode == "denied-locks" && strings.HasSuffix(path, "/microsoft.authorization/locks")) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return previous(req)
				}
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(t.Context(), request)
			wait := mode == "missing-parent" || mode == "deleting-parent"
			if (err == nil) != wait || len(f.arc.deleted) != 0 {
				t.Fatal("registration boundary", mode, err)
			}
		})
	}
}

func TestAzureLocalRegistrationSynchronousOwnReadback(t *testing.T) {
	for _, status := range []int{204, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.registrationRequest(t)
			f.removeLocalVM()
			original := f.override
			deletes := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.ToLower(req.URL.Path) == request.Asset.Identity.NativeID {
					deletes++
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: http.NoBody}, true
				}
				return original(req)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || deletes != 1 {
				t.Fatal("synchronous registration DELETE", err, deletes)
			}
			request.ExecutionResult = &result
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
				t.Fatal("response substituted for own absence", wait, err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || deletes != 1 {
				t.Fatal("synchronous DELETE replayed", err, deletes)
			}
			delete(f.values, request.Asset.Identity.NativeID)
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("synchronous registration did not complete", wait, err)
			}
		})
	}
}

func TestAzureLocalRegistrationChangingObservations(t *testing.T) {
	for _, mode := range []string{"inventory", "graph"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			var parent, vm asset.Asset
			if mode == "graph" {
				parent, vm = f.asset(t, hybridMachineType), f.asset(t, azureLocalVMType)
			}
			previous, reads := f.override, 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.ToLower(req.URL.Path) == f.ids[azureLocalVMType] {
					reads++
					res := jsonResponse(200, f.values[f.ids[azureLocalVMType]], nil)
					if reads == 1 {
						object(f.values[f.ids[azureLocalVMType]]["properties"])["hardwareProfile"] = map[string]any{"processors": 128}
					}
					return res, true
				}
				return previous(req)
			}
			var err error
			if mode == "inventory" {
				request := f.request(hybridMachineType)
				request.Source = hybridComputeSource
				_, err = f.runtime.List(t.Context(), request)
			} else {
				cascades := &serviceCascades{client: f.client, connectionID: "connection"}
				err = cascades.contributeAzureLocalRegistration(t.Context(), parent, []asset.Asset{parent, vm}, &governance.Contribution{})
			}
			if err == nil || reads < 2 {
				t.Fatal("changing Local observations accepted", mode, reads, err)
			}
		})
	}
}

func TestAzureLocalRegistrationMalformedContext(t *testing.T) {
	for _, mode := range []string{"extra", "foreign-disk", "foreign-vm", "foreign-child", "missing-members", "missing-resource", "nil-context"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			value := f.asset(t, hybridMachineType)
			state := object(value.Normalized[hybridComputeCleanup])
			local := object(state["local_vm"])
			switch mode {
			case "extra":
				local["future"] = "unreviewed"
			case "foreign-disk":
				local["os_disk"] = strings.ReplaceAll(text(local["os_disk"]), testSubscription, "22222222-2222-4333-8444-555555555555")
			case "foreign-vm":
				local["id"] = strings.ReplaceAll(text(local["id"]), "local-vm", "other-vm")
			case "foreign-child":
				object(local["members"])[f.dataDisk] = map[string]any{"kind": azureLocalDiskType, "configuration": "unreviewed"}
			case "missing-members":
				local["members"] = nil
			case "missing-resource":
				delete(local, "resource")
			case "nil-context":
				state["local_vm"] = nil
			}
			// Even a correctly sealed but malformed stored record must not authorize a
			// different native resource; this also exercises persisted schema validation.
			value.Normalized[hybridComputeCleanupProof] = f.client.hybridComputeMachineBinding(value.Identity.NativeID, value.Identity.ConnectionID, state)
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
				t.Fatal("malformed context accepted", mode)
			}
		})
	}
}
