package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type localVMFixture struct {
	*localCleanupFixture
	arc                        *hybridCleanupFixture
	vmDeletes                  int
	vmPolls                    int
	holdVM, holdMeta, holdDisk bool
	dataDisk                   string
}

func newLocalVMFixture(t *testing.T) *localVMFixture {
	t.Helper()
	f := &localVMFixture{localCleanupFixture: newLocalCleanupFixture(t), arc: newHybridCleanupFixture(t)}
	f.dataDisk = f.ids[azureLocalDiskType] + "-data"
	dataDisk := batchClone(f.values[f.ids[azureLocalDiskType]])
	dataDisk["id"], dataDisk["name"] = f.dataDisk, last(f.dataDisk)
	f.values[f.dataDisk] = dataDisk
	object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"])["dataDisks"] = []any{map[string]any{"id": f.dataDisk}}
	machine := f.ids[hybridMachineType]
	for _, raw := range f.arc.values {
		kind := text(raw["type"])
		if !hybridComputeChild(kind) && kind != hybridLicenseType {
			continue
		}
		body, _ := json.Marshal(raw)
		body = []byte(strings.ReplaceAll(string(body), strings.ToLower(resourceID(hybridMachineType, "machine")), machine))
		var child map[string]any
		_ = json.Unmarshal(body, &child)
		id := text(child["id"])
		f.values[id], f.ids[kind] = child, id
	}
	f.arc.values = f.values
	f.collections["/subscriptions/"+testSubscription+"/providers/microsoft.hybridcompute/licenses"] = hybridLicenseType
	original := f.override
	endpoint := strings.ReplaceAll(localCleanupPollURL("Azure-AsyncOperation"), "11111111-2222-3333-4444-555555555555", azureRequestID(f.ids[azureLocalVMType]))
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.URL.String() == endpoint {
			if req.Method != "GET" {
				t.Fatal("wrong VM poll method")
			}
			f.vmPolls++
			if !f.holdVM {
				delete(f.values, f.ids[azureLocalVMType])
			}
			if !f.holdDisk {
				delete(f.values, f.ids[azureLocalDiskType])
			}
			if !f.holdMeta {
				delete(f.values, f.ids[azureLocalIdentityType])
			}
			return jsonResponse(200, map[string]any{"status": "Succeeded", "resourceId": f.ids[azureLocalVMType]}, nil), true
		}
		if path == f.ids[azureLocalVMType] && req.Method == "DELETE" {
			body, _ := io.ReadAll(req.Body)
			if req.URL.Query().Get("api-version") != azureLocalVersion || len(req.URL.Query()) != 1 || len(body) != 0 || req.Header.Get("If-Match") != "" || req.Header.Get("X-Ms-Client-Request-Id") == "" {
				t.Fatal("wrong VM native DELETE", req.URL)
			}
			for _, raw := range f.values {
				if raw["type"] == azureLocalAgentType || hybridComputeChild(text(raw["type"])) {
					t.Fatal("VM deletion preceded child absence", raw["id"])
				}
			}
			f.vmDeletes++
			if f.vmDeletes != 1 {
				t.Fatal("VM DELETE replayed")
			}
			object(f.values[path]["properties"])["provisioningState"] = "Deleting"
			object(f.values[path]["properties"])["status"] = map[string]any{"powerState": "Stopped"}
			f.values[path]["etag"] = "after-vm-delete"
			return &http.Response{StatusCode: 202, Header: http.Header{"Azure-Asyncoperation": {endpoint}, "X-Ms-Request-Id": {"vm-delete"}}, Body: http.NoBody}, true
		}
		if path == f.ids[hybridMachineType] && req.Method == "DELETE" {
			for _, kind := range []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalDiskType} {
				if f.values[f.ids[kind]] != nil {
					t.Fatal("Arc deletion preceded native Local absence", kind)
				}
			}
		}
		if armPathProvider(path) == "microsoft.hybridcompute" {
			return f.arc.override(req)
		}
		return original(req)
	}
	return f
}

func (f *localVMFixture) asset(t *testing.T, kind string, selected ...string) asset.Asset {
	t.Helper()
	request := f.request(kind)
	if hybridComputeKind(kind) != "" {
		request.Source = hybridComputeSource
	}
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal("VM family inventory", kind, err)
	}
	var item contracts.InventoryItem
	id := f.ids[kind]
	if len(selected) == 1 {
		id = selected[0]
	}
	for _, candidate := range batch.Items {
		if candidate.NativeID == id {
			item = candidate
		}
	}
	if item.NativeID == "" {
		t.Fatal("VM family resource missing", kind)
	}
	return asset.Asset{ID: asset.AssetID("local-" + last(kind)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Name: item.Name, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}
}

func (f *localVMFixture) vmRequest(t *testing.T) contracts.ActionRequest {
	t.Helper()
	request := contracts.ActionRequest{Asset: f.asset(t, azureLocalVMType), Action: "delete", IdempotencyKey: "local-vm-delete"}
	for _, kind := range []string{azureLocalIdentityType, azureLocalDiskType, azureLocalAgentType, hybridExtensionType, hybridCommandType, hybridProfileType} {
		impact := contracts.ActionImpact{Asset: f.asset(t, kind), ControllerID: request.Asset.ID, Delete: true}
		if kind == azureLocalIdentityType || kind == azureLocalDiskType {
			request.LifecycleImpacts = append(request.LifecycleImpacts, impact)
		} else {
			request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, impact)
		}
	}
	return request
}

func (f *localVMFixture) removePrerequisites() {
	for _, kind := range []string{azureLocalAgentType, hybridExtensionType, hybridCommandType, hybridProfileType} {
		delete(f.values, f.ids[kind])
	}
}

func TestAzureLocalVMDeletionOwnMetadataAndRecovery(t *testing.T) {
	f := newLocalVMFixture(t)
	request := f.vmRequest(t)
	driver := f.driver(t, request)
	if _, err := driver.Execute(t.Context(), request); err == nil || f.vmDeletes != 0 {
		t.Fatal("live prerequisite allowed VM deletion", err)
	}
	f.removePrerequisites()
	object(f.values[f.ids[azureLocalVMType]]["properties"])["instanceView"] = map[string]any{"vmAgent": map[string]any{"statuses": []any{map[string]any{"code": "ProvisioningState/failed"}}}}
	object(f.values[f.ids[azureLocalVMType]]["properties"])["guestAgentInstallStatus"] = map[string]any{"status": "Failed", "lastStatusChange": "2026-09-14T00:00:00Z"}
	f.holdVM, f.holdMeta, f.holdDisk = true, true, true
	result, err := driver.Execute(t.Context(), request)
	if err != nil || f.vmDeletes != 1 || result.ProviderRequestID != "vm-delete" {
		t.Fatal("VM DELETE", err, result)
	}
	wait, err := driver.Wait(t.Context(), request, result)
	if err != nil || wait.Done {
		t.Fatal("successful poll erased live VM", wait, err)
	}
	result.Data = wait.Data
	payload, _ := json.Marshal(result)
	var restored contracts.ActionResult
	_ = json.Unmarshal(payload, &restored)
	request.ExecutionResult = &restored
	driver = f.driver(t, request)
	delete(f.values, f.ids[azureLocalVMType])
	if _, err := driver.Execute(t.Context(), request); err != nil || f.vmDeletes != 1 {
		t.Fatal("restart replayed DELETE", err)
	}
	wait, err = driver.Wait(t.Context(), request, restored)
	if err != nil || wait.Done || f.vmPolls != 1 {
		t.Fatal("VM absence erased live metadata or replayed poll", wait, err)
	}
	delete(f.values, f.ids[azureLocalIdentityType])
	wait, err = driver.Wait(t.Context(), request, restored)
	if err != nil || wait.Done {
		t.Fatal("VM and metadata absence erased live OS disk", wait, err)
	}
	delete(f.values, f.ids[azureLocalDiskType])
	wait, err = driver.Wait(t.Context(), request, restored)
	if err != nil || !wait.Done || f.vmPolls != 1 {
		t.Fatal("metadata own absence", wait, err)
	}
	if f.values[f.dataDisk] == nil {
		t.Fatal("VM cleanup removed its data disk")
	}
	for _, kind := range []string{hybridMachineType, hybridLicenseType, azureLocalNICType, azureLocalImageType, azureLocalStorageType} {
		if f.values[f.ids[kind]] == nil {
			t.Fatal("VM cleanup removed independent resource", kind)
		}
	}
}

func TestAzureLocalVMDriftProtectionAndReview(t *testing.T) {
	for _, change := range []string{"vm-config", "vm-smbios", "os-disk-config", "os-disk-etag", "os-disk-tag", "os-disk-lock", "vm-etag", "vm-tag", "vm-owner", "machine-id", "machine-kind", "machine-location", "metadata", "metadata-tag", "metadata-lock", "group-tag", "group-owner", "parent-lock", "new-arc-child", "missing-impact", "retain-impact", "duplicate-impact", "wrong-controller", "foreign-connection", "forged-members", "parameters", "delegate-guest", "delete-identity"} {
		t.Run(change, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.vmRequest(t)
			driver := f.driver(t, request)
			extension := batchClone(f.values[f.ids[hybridExtensionType]])
			f.removePrerequisites()
			vm, machine, metadata := f.values[f.ids[azureLocalVMType]], f.values[f.ids[hybridMachineType]], f.values[f.ids[azureLocalIdentityType]]
			switch change {
			case "vm-config":
				object(vm["properties"])["futurePrivateConfiguration"] = "changed"
			case "vm-smbios":
				object(vm["properties"])["guestAgentInstallStatus"] = map[string]any{"vmUuid": testTenant}
			case "os-disk-config":
				object(f.values[f.ids[azureLocalDiskType]]["properties"])["diskSizeGB"] = 999
			case "os-disk-etag":
				f.values[f.ids[azureLocalDiskType]]["etag"] = "replaced-disk"
			case "os-disk-tag":
				f.values[f.ids[azureLocalDiskType]]["tags"] = map[string]any{"steward:protected": "true"}
			case "vm-etag":
				request.PrerequisiteDeletions = nil
				vm["etag"] = "new-generation"
			case "vm-tag":
				vm["tags"] = map[string]any{"steward:protected": "true"}
			case "vm-owner":
				vm["managedBy"] = f.ids[azureLocalStorageType]
			case "machine-id":
				object(machine["properties"])["vmId"] = testSubscription
			case "machine-kind":
				machine["kind"] = "VMware"
			case "machine-location":
				machine["location"] = "westus"
			case "metadata":
				object(metadata["properties"])["publicKey"] = "changed-private-key"
			case "metadata-tag":
				metadata["tags"] = map[string]any{"steward:protected": "true"}
			case "metadata-lock", "parent-lock", "os-disk-lock":
				id := f.ids[azureLocalIdentityType]
				if change == "parent-lock" {
					id = f.ids[hybridMachineType]
				}
				if change == "os-disk-lock" {
					id = f.ids[azureLocalDiskType]
				}
				f.locks = []any{map[string]any{"id": id + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "group-owner":
				f.group["managedBy"] = f.ids[azureLocalStorageType]
			case "new-arc-child":
				id := f.ids[hybridExtensionType] + "-new"
				extension["id"], extension["name"] = id, last(id)
				f.values[id] = extension
			case "missing-impact":
				request.LifecycleImpacts = nil
			case "retain-impact":
				request.LifecycleImpacts[0].Delete = false
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "foreign-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "forged-members":
				request.Asset.Normalized = batchClone(request.Asset.Normalized)
				object(object(request.Asset.Normalized[azureLocalCleanup])["members"])[f.ids[azureLocalNICType]] = map[string]any{"kind": azureLocalNICType, "configuration": "forged"}
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			case "delegate-guest":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.PrerequisiteDeletions[0])
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
			case "delete-identity":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.LifecycleImpacts[0])
				request.LifecycleImpacts = nil
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || f.vmDeletes != 0 {
				t.Fatal("unreviewed VM mutation", change, err)
			}
		})
	}
}

func TestAzureLocalVMMissingParentAndDeniedReads(t *testing.T) {
	for _, state := range []string{"machine-missing", "machine-deleting", "vm-missing", "vm-deleting"} {
		t.Run(state, func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.vmRequest(t)
			f.removePrerequisites()
			driver := f.driver(t, request)
			kind := azureLocalVMType
			if strings.HasPrefix(state, "machine-") {
				kind = hybridMachineType
			}
			if strings.HasSuffix(state, "-missing") {
				delete(f.values, f.ids[kind])
			} else {
				object(f.values[f.ids[kind]]["properties"])["provisioningState"] = "Deleting"
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.vmDeletes != 0 {
				t.Fatal("missing/deleting resource caused mutation", err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || wait.Done {
				t.Fatal("parent/VM absence erased surviving metadata", wait, err)
			}
		})
	}
	for _, kind := range []string{azureLocalVMType, azureLocalIdentityType, azureLocalDiskType, hybridMachineType, hybridExtensionType} {
		t.Run("denied-"+last(kind), func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.vmRequest(t)
			f.removePrerequisites()
			driver := f.driver(t, request)
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, f.ids[kind]) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				return original(req)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || f.vmDeletes != 0 {
				t.Fatal("denied dependency authorized VM deletion")
			}
		})
	}
}

func TestAzureLocalVMInventoryRecoversChildrenAndRejectsDrift(t *testing.T) {
	f := newLocalVMFixture(t)
	request := f.request(azureLocalVMType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	prior := batch.Items[0].Normalized
	request.KnownNativeIDs = []string{f.ids[azureLocalVMType]}
	request.KnownNativeMetadata = map[string]map[string]any{f.ids[azureLocalVMType]: prior}
	for _, kind := range []string{azureLocalAgentType, azureLocalIdentityType, hybridExtensionType, hybridCommandType, hybridProfileType} {
		f.omitted[f.ids[kind]] = true
	}
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[azureLocalCleanup])["members"])) != 6 {
		t.Fatal("omitted children were lost", batch, err)
	}
	original := f.override
	reads := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, f.ids[azureLocalIdentityType]) {
			reads++
			if reads == 2 {
				object(f.values[f.ids[azureLocalIdentityType]]["properties"])["publicKey"] = "changed-private-key"
			}
		}
		return original(req)
	}
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("metadata drift published an inventory snapshot")
	}
	f.override = original
	request.KnownNativeMetadata[f.ids[azureLocalVMType]] = batchClone(prior)
	object(request.KnownNativeMetadata[f.ids[azureLocalVMType]][azureLocalCleanup])["members"] = map[string]any{}
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("forged known membership accepted")
	}
}

func TestAzureLocalOSDiskOtherConsumersBlockCleanup(t *testing.T) {
	for _, mode := range []string{"new-parent", "known-omitted-parent", "denied-index", "os-disk-as-data"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			machine := f.ids[hybridMachineType] + "-other"
			vm := machine + "/providers/microsoft.azurestackhci/virtualmachineinstances/default"
			other := batchClone(f.values[f.ids[azureLocalVMType]])
			other["id"] = vm
			object(other["properties"])["storageProfile"] = map[string]any{"osDisk": map[string]any{"id": f.dataDisk}}
			parent := batchClone(f.values[f.ids[hybridMachineType]])
			parent["id"], parent["name"] = machine, last(machine)
			if mode == "known-omitted-parent" {
				f.values[machine], f.values[vm] = parent, other
			}
			// Scope-specific indexes must not return the other VM's singleton.
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == machine+"/extensions" || path == machine+"/runcommands" || path == machine+"/licenseprofiles" || path == vm+"/guestagents" || path == vm+"/hybrididentitymetadata" {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				if path == f.ids[hybridMachineType]+"/providers/microsoft.azurestackhci/virtualmachineinstances" || path == machine+"/providers/microsoft.azurestackhci/virtualmachineinstances" {
					rows := []any{}
					if raw := f.values[path+"/default"]; raw != nil {
						rows = append(rows, raw)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				return original(req)
			}
			request := f.vmRequest(t)
			f.removePrerequisites()
			driver := f.driver(t, request)
			if mode == "denied-index" {
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.hybridcompute/machines") {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return previous(req)
				}
			} else if mode == "os-disk-as-data" {
				object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"])["dataDisks"] = []any{map[string]any{"id": f.ids[azureLocalDiskType]}}
				if _, err := f.runtime.List(t.Context(), f.request(azureLocalVMType)); err == nil {
					t.Fatal("OS disk simultaneously classified as retained data")
				}
			} else {
				object(object(other["properties"])["storageProfile"])["dataDisks"] = []any{map[string]any{"id": f.ids[azureLocalDiskType]}}
				f.values[machine], f.values[vm] = parent, other
				if mode == "known-omitted-parent" {
					f.omitted[machine] = true
				}
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || f.vmDeletes != 0 {
				t.Fatal("unverified OS-disk exclusivity allowed deletion", mode, err)
			}
		})
	}
}

func TestAzureLocalOSDiskResourceGroupProtection(t *testing.T) {
	for _, mode := range []string{"allowed", "protected", "denied"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalVMFixture(t)
			old := f.ids[azureLocalDiskType]
			id := strings.Replace(old, "/resourcegroups/test/", "/resourcegroups/disks/", 1)
			if id == old {
				t.Fatal("fixture group was not replaced")
			}
			disk := f.values[old]
			delete(f.values, old)
			disk["id"] = id
			f.values[id], f.ids[azureLocalDiskType] = disk, id
			object(object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"])["osDisk"])["id"] = id
			groupID := strings.Join(strings.Split(id, "/")[:5], "/")
			group := map[string]any{"id": groupID, "name": "disks", "type": groupType, "location": "eastus", "properties": map[string]any{}}
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, groupID) {
					if mode == "denied" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					if mode == "protected" {
						group["tags"] = map[string]any{"steward:protected": "true"}
					}
					return jsonResponse(200, group, nil), true
				}
				return original(req)
			}
			request := f.vmRequest(t)
			f.removePrerequisites()
			_, err := f.driver(t, request).Execute(t.Context(), request)
			if mode == "allowed" && (err != nil || f.vmDeletes != 1) || mode != "allowed" && (err == nil || f.vmDeletes != 0) {
				t.Fatal("OS-disk group boundary", mode, err, f.vmDeletes)
			}
		})
	}
}

func TestAzureLocalVMSynchronousAndDeleteNotFoundNeedOwnAbsence(t *testing.T) {
	for _, status := range []int{204, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newLocalVMFixture(t)
			request := f.vmRequest(t)
			f.removePrerequisites()
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				res, ok := original(req)
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, f.ids[azureLocalVMType]) {
					if status == 404 {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
				}
				return res, ok
			}
			driver := f.driver(t, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.vmDeletes != 1 {
				t.Fatal("native synchronous/404 receipt", err)
			}
			request.ExecutionResult = &result
			driver = f.driver(t, request)
			if _, err := driver.Execute(t.Context(), request); err != nil || f.vmDeletes != 1 {
				t.Fatal("receipt caused mutation replay", err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || wait.Done || f.vmPolls != 0 {
				t.Fatal("DELETE response replaced own absence", wait, err)
			}
			for _, kind := range []string{azureLocalVMType, azureLocalIdentityType, azureLocalDiskType} {
				delete(f.values, f.ids[kind])
			}
			wait, err = driver.Wait(t.Context(), request, result)
			if err != nil || !wait.Done || f.vmDeletes != 1 || f.vmPolls != 0 {
				t.Fatal("synchronous own readback", wait, err)
			}
		})
	}
}

func TestAzureLocalVMWithoutRegisteredOSDisk(t *testing.T) {
	f := newLocalVMFixture(t)
	delete(object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"]), "osDisk")
	request := f.vmRequest(t)
	request.LifecycleImpacts = request.LifecycleImpacts[:1]
	f.removePrerequisites()
	state := object(request.Asset.Normalized[azureLocalCleanup])
	if state["os_disk"] != "" || object(state["members"])[f.ids[azureLocalDiskType]] != nil {
		t.Fatal("unreferenced disk was added to the VM cascade")
	}
	if check, err := f.driver(t, request).Preflight(t.Context(), request); err != nil || !check.Allowed || check.Absent {
		t.Fatal("VM without a separately registered OS disk", check, err)
	}
}

func TestAzureLocalGuestLegacyVMSnapshot(t *testing.T) {
	f := newLocalCleanupFixture(t)
	vm := f.values[f.ids[azureLocalVMType]]
	object(vm["properties"])["instanceView"] = map[string]any{"vmAgent": map[string]any{"statuses": []any{}}}
	request := f.requestAsset(t)
	legacy := hybridComputeChildSnapshot(vm)
	delete(object(legacy["properties"]), "status")
	state := object(request.Asset.Normalized[azureLocalCleanup])
	state["vm"] = f.client.privateConfiguration(legacy)
	request.Asset.Normalized[azureLocalCleanupProof] = f.client.azureLocalCleanupBinding(request.Asset.Identity.NativeID, request.Asset.Identity.ConnectionID, request.Asset.Location, state)
	driver := f.driver(t, request)
	if check, err := driver.Preflight(t.Context(), request); err != nil || !check.Allowed {
		t.Fatal("unchanged pre-VM-support guest snapshot rejected", check, err)
	}
	object(vm["properties"])["hardwareProfile"] = map[string]any{"memoryMB": 999}
	if _, err := driver.Preflight(t.Context(), request); err == nil {
		t.Fatal("legacy snapshot accepted authored VM drift")
	}
}
