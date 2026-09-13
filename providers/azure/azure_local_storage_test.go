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
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type localStorageFixture struct {
	*localRootFixture
	consumerDeletes map[string]int
}

func newLocalStorageFixture(t *testing.T) *localStorageFixture {
	f := &localStorageFixture{localRootFixture: newLocalRootFixture(t, azureLocalStorageType), consumerDeletes: map[string]int{}}
	// These composed resources have explicit placement; unchanged Swagger disk
	// examples also exercise omitted placement in separate boundary tests below.
	for _, raw := range f.values {
		if azureLocalStorageResource(text(raw["type"])) {
			object(raw["properties"])["containerId"] = f.id
		}
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "DELETE" && id == f.id {
			for candidate, raw := range f.values {
				kind := text(raw["type"])
				if kind != azureLocalVMType && !azureLocalStorageResource(kind) {
					continue
				}
				refs, err := azureLocalReferences(candidate, kind, raw)
				if err != nil || azureLocalRootReference(refs, azureLocalStorageType, f.id) {
					t.Fatal("storage deleted with remaining/unknown workload placement", candidate, err)
				}
			}
		}
		if raw := f.values[id]; req.Method == "DELETE" && raw != nil && azureLocalStorageResource(text(raw["type"])) {
			if id == f.ids[azureLocalDiskType] {
				t.Fatal("OS disk must remain a VM-managed impact")
			}
			body, _ := io.ReadAll(req.Body)
			if req.URL.Query().Get("api-version") != azureLocalVersion || len(req.URL.Query()) != 1 || len(body) != 0 || req.Header.Get("X-Ms-Client-Request-Id") == "" {
				t.Fatal("invalid native storage consumer DELETE")
			}
			if raw["type"] == azureLocalDiskType {
				for vmID, vm := range f.values {
					if vm["type"] != azureLocalVMType {
						continue
					}
					refs, err := azureLocalReferences(vmID, azureLocalVMType, vm)
					if err != nil || slices.Contains(refs[azureLocalDiskType], id) {
						t.Fatal("data disk removed before VM", err)
					}
				}
			}
			f.consumerDeletes[id]++
			if f.consumerDeletes[id] != 1 {
				t.Fatal("consumer DELETE replayed")
			}
			delete(f.values, id)
			return &http.Response{StatusCode: 204, Header: http.Header{"Azure-Asyncoperation": {"http://azure.async.operation/status"}, "X-Ms-Request-Id": {azureRequestID(id)}}, Body: http.NoBody}, true
		}
		return previous(req)
	}
	return f
}

func (f *localStorageFixture) relocate() {
	other := strings.ToLower(resourceID(azureLocalStorageType, "another-path"))
	for _, raw := range f.values {
		if azureLocalStorageResource(text(raw["type"])) {
			object(raw["properties"])["containerId"] = other
		}
		if raw["type"] == azureLocalVMType {
			object(object(raw["properties"])["storageProfile"])["vmConfigStoragePathId"] = other
		}
	}
}

func TestAzureLocalStorageOwnReadbackRecovery(t *testing.T) {
	for _, status := range []int{202, 204, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newLocalStorageFixture(t)
			request := f.requestAsset(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), request); err == nil {
				t.Fatal("in-use storage deletion allowed")
			}
			f.relocate()
			f.status, f.retain = status, true
			result, err := driver.Execute(t.Context(), request)
			if err != nil || f.rootDeletes != 1 {
				t.Fatal("storage DELETE", err)
			}
			for range 3 {
				blob, _ := json.Marshal(result)
				_ = json.Unmarshal(blob, &result)
				fresh, _ := NewRuntime(f.runtime.credentials)
				fresh.transport = f.runtime.transport
				driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				request.ExecutionResult = &result
				if _, err = driver.Execute(t.Context(), request); err != nil || f.rootDeletes != 1 {
					t.Fatal("storage replay", err)
				}
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || wait.Done {
					t.Fatal("live path completed", wait, err)
				}
				result.Data = wait.Data
			}
			delete(f.values, f.id)
			// An old storage ID still referenced by a workload cannot establish completion.
			object(f.values[f.dataDisk]["properties"])["containerId"] = f.id
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
				t.Fatal("storage 404 hid surviving workload", wait, err)
			}
			f.relocate()
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("own absence with cleared references", wait, err)
			}
			if f.vmDeletes != 0 || f.deleted != 0 || len(f.arc.deleted) != 0 || len(f.consumerDeletes) != 0 {
				t.Fatal("path cleanup cascaded workloads")
			}
		})
	}
}

func TestAzureLocalStorageConsumerBoundaries(t *testing.T) {
	for _, kind := range []string{azureLocalVMType, azureLocalDiskType, azureLocalImageType, azureLocalMarketplaceType} {
		for _, mode := range []string{"known-omitted", "unknown-placement", "foreign-placement", "denied", "new-resource", "malformed-reference"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalStorageFixture(t)
				request := f.requestAsset(t)
				f.relocate()
				id := f.ids[kind]
				if kind == azureLocalDiskType {
					id = f.dataDisk
				}
				if mode == "new-resource" {
					if kind == azureLocalVMType {
						id = strings.Replace(id, "/local-vm/", "/new-vm/", 1)
						machine := azureLocalMachine(id)
						f.values[machine] = batchClone(f.values[f.ids[hybridMachineType]])
						f.values[machine]["id"], f.values[machine]["name"] = machine, last(machine)
					} else {
						id += "-new"
					}
					f.values[id] = batchClone(f.values[f.ids[kind]])
					f.values[id]["id"], f.values[id]["name"] = id, last(id)
				}
				props := object(f.values[id]["properties"])
				field := "containerId"
				if kind == azureLocalVMType {
					props = object(props["storageProfile"])
					field = "vmConfigStoragePathId"
				}
				props[field] = f.id
				switch mode {
				case "known-omitted":
					f.omitted[id] = true
					if kind == azureLocalVMType {
						f.omitted[azureLocalMachine(id)] = true
					}
				case "unknown-placement":
					delete(props, field)
				case "foreign-placement":
					props[field] = strings.Replace(f.id, testSubscription, testTenant, 1)
				case "malformed-reference":
					props[field] = true
				case "denied":
					previous := f.override
					f.override = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return previous(req)
					}
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err == nil {
					_, err = driver.Execute(t.Context(), request)
				}
				if (err == nil) != (mode == "foreign-placement") || f.rootDeletes != 0 && mode != "foreign-placement" {
					t.Fatal("storage consumer boundary", mode, err, f.rootDeletes)
				}
			})
		}
	}
}

func TestAzureLocalStorageReviewAndHistory(t *testing.T) {
	for _, mode := range []string{"etag", "configuration", "group", "lock", "parameters", "impact", "forged", "missing-resources", "wrong-history-type", "foreign-history", "duplicate-history", "known-omitted", "deleted-history"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalStorageFixture(t)
			value := f.requestAsset(t).Asset
			f.relocate()
			state := object(value.Normalized[azureLocalCleanup])
			switch mode {
			case "etag":
				f.values[f.id]["etag"] = "changed"
			case "configuration":
				object(f.values[f.id]["properties"])["path"] = "private-local-replaced"
			case "group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.id + "/providers/microsoft.authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forged":
				value.Normalized[azureLocalCleanupProof] = "forged"
			case "missing-resources":
				delete(state, "resources")
			case "wrong-history-type":
				state["resources"] = []string{f.ids[azureLocalNICType]}
			case "foreign-history":
				state["resources"] = []string{strings.Replace(f.dataDisk, testSubscription, testTenant, 1)}
			case "duplicate-history":
				state["resources"] = []string{f.dataDisk, f.dataDisk}
			case "known-omitted", "deleted-history":
				for _, id := range stringValues(state["resources"]) {
					f.omitted[id] = true
					if mode == "deleted-history" {
						delete(f.values, id)
					}
				}
				request := f.request(azureLocalStorageType)
				request.KnownNativeIDs = []string{f.id}
				request.KnownNativeMetadata = map[string]map[string]any{f.id: value.Normalized}
				batch, err := f.runtime.List(t.Context(), request)
				if err != nil || len(batch.Items) != 1 {
					t.Fatal("storage history scan", err)
				}
				if !slices.Equal(stringValues(state["resources"]), stringValues(object(batch.Items[0].Normalized[azureLocalCleanup])["resources"])) {
					t.Fatal("storage history lost")
				}
				value.Normalized = batch.Items[0].Normalized
			}
			if strings.Contains(mode, "history") && mode != "deleted-history" || mode == "missing-resources" {
				value.Normalized[azureLocalCleanupProof] = f.client.azureLocalRootBinding(f.id, "connection", state)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "storage-test"}
			if mode == "parameters" {
				request.Parameters = map[string]any{"force": true}
			}
			if mode == "impact" {
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: f.asset(t, azureLocalNICType), ControllerID: value.ID, Delete: true}}
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err == nil {
				_, err = driver.Execute(t.Context(), request)
			}
			allowed := mode == "known-omitted" || mode == "deleted-history"
			if (err == nil) != allowed || f.rootDeletes != 0 && !allowed {
				t.Fatal("storage review boundary", mode, err)
			}
		})
	}
}

func TestAzureLocalStorageExplicitSelection(t *testing.T) {
	for _, mode := range []string{"storage-only", "all", "retain-image", "protect-image", "missing-image", "unknown-placement", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalStorageFixture(t)
			if mode == "empty" {
				f.relocate()
			}
			if mode == "unknown-placement" {
				delete(object(f.values[f.dataDisk]["properties"]), "containerId")
			}
			root, vm := f.requestAsset(t), f.vmRequest(t)
			values := []asset.Asset{root.Asset, vm.Asset}
			for _, member := range append(vm.PrerequisiteDeletions, vm.LifecycleImpacts...) {
				values = append(values, member.Asset)
			}
			for _, kind := range []string{azureLocalDiskType, azureLocalImageType, azureLocalMarketplaceType} {
				id := f.ids[kind]
				if kind == azureLocalDiskType {
					id = f.dataDisk
				}
				v := f.asset(t, kind, id)
				v.ID = asset.AssetID(id)
				values = append(values, v)
			}
			var image asset.Asset
			for _, v := range values {
				if v.Identity.NativeType == azureLocalImageType {
					image = v
				}
			}
			if mode == "missing-image" {
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == image.ID })
			}
			result := governance.Contribution{}
			s := &serviceCascades{client: f.client, connectionID: "connection"}
			if err := s.contributeAzureLocalRoots(t.Context(), values, &result); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-image" {
				if len(result.Unresolved) == 0 {
					t.Fatal("missing image inventory ignored")
				}
				return
			}
			if err := s.contributeAzureLocalVMs(t.Context(), values, &result); err != nil {
				t.Fatal(err)
			}
			input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.Asset.ID}, Relationships: result.Relationships, LifecycleBindings: result.Bindings}
			if mode != "storage-only" && mode != "empty" {
				for _, v := range values {
					input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, v.ID)
				}
			}
			if mode == "retain-image" {
				input.RequestOptions = map[asset.AssetID]map[string]any{root.Asset.ID: {"retain_resources": []string{image.Identity.NativeID}}}
			}
			if mode == "protect-image" {
				input.Protections = []plan.ProtectionPolicy{{AssetID: image.ID, Protected: true}}
			}
			solved, err := plan.Solve(input)
			allowed := mode == "all" || mode == "unknown-placement" || mode == "empty"
			if err != nil || (len(solved.Blockers) == 0) != allowed {
				t.Fatal("storage selection", mode, err, solved.Blockers)
			}
			if allowed {
				expected := 9
				if mode == "empty" {
					expected = 1
				}
				if len(solved.Steps) != expected || solved.Steps[expected-1].AssetID != root.Asset.ID {
					t.Fatal("storage deletion ordering", len(solved.Steps), solved.Steps)
				}
			}
		})
	}
}

func TestAzureLocalStorageNativeIndexBoundaries(t *testing.T) {
	for _, mode := range []string{"denied", "missing-index", "duplicate", "foreign-member", "changed-member", "wrong-type"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalStorageFixture(t)
			request := f.requestAsset(t)
			f.relocate()
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+azureLocalDiskType) {
					if mode == "denied" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					if mode == "missing-index" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					row := batchClone(f.values[f.dataDisk])
					rows := []any{row}
					switch mode {
					case "duplicate":
						rows = append(rows, row)
					case "foreign-member":
						row["id"] = strings.Replace(f.dataDisk, testSubscription, testTenant, 1)
					case "changed-member":
						object(row["properties"])["diskSizeGB"] = 999
					case "wrong-type":
						row["type"] = azureLocalImageType
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				return previous(req)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), request); err == nil || f.rootDeletes != 0 {
				t.Fatal("invalid storage consumer index allowed deletion", mode, err)
			}
			delete(f.values, f.id)
			if _, err = driver.Readback(t.Context(), request); err == nil {
				t.Fatal("path 404 hid unreadable consumer index", mode)
			}
		})
	}
}

func TestAzureLocalStorageLegacyRescan(t *testing.T) {
	f := newLocalStorageFixture(t)
	value := f.requestAsset(t).Asset
	refs, err := f.client.azureLocalRecordedReferences(value)
	if err != nil {
		t.Fatal(err)
	}
	config := strings.TrimPrefix(text(value.Normalized["_azure_local_configuration"]), azureLocalRootPrefix)
	value.Normalized["_azure_local_configuration"] = config
	value.Normalized["_azure_local_reference_binding"] = f.client.privateConfiguration(map[string]any{"id": value.Identity.NativeID, "connection": value.Identity.ConnectionID, "configuration": config, "references": refs})
	delete(value.Normalized, azureLocalCleanup)
	delete(value.Normalized, azureLocalCleanupProof)
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
		t.Fatal("old inventory authorized storage deletion")
	}
	request := f.request(azureLocalStorageType)
	request.KnownNativeIDs = []string{f.id}
	request.KnownNativeMetadata = map[string]map[string]any{f.id: value.Normalized}
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || len(object(batch.Items[0].Normalized[azureLocalCleanup])) != 7 || !*batch.Items[0].Actionable {
		t.Fatal("old storage inventory could not upgrade", err)
	}
}
