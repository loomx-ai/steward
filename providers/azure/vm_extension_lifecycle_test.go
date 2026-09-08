package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func vmExtensionScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset, map[string]any) {
	t.Helper()
	s, r, assets := flexibleScaleSetScenario(t)
	vm := assets[2]
	extension := map[string]any{"id": vm.Identity.NativeID + "/extensions/health", "type": vmExtensionType, "name": "health", "etag": "extension-etag", "properties": map[string]any{"publisher": "Microsoft.ManagedServices", "type": "ApplicationHealthLinux", "typeHandlerVersion": "1.0", "settings": map[string]any{"sensitive": "do-not-export"}}}
	s.add(extension, "2024-07-01")
	s.lists[vm.Identity.NativeID+"/extensions"] = []any{extension}
	assets = append(assets, dnsAsset(t, r, extension))
	return s, r, assets, extension
}

func vmExtensionRequest(t *testing.T, r *Runtime, assets []asset.Asset) (contracts.ActionRequest, plan.Input) {
	t.Helper()
	// The retention request starts on a Flexible scale set, and must survive
	// the independently executed member VM's mixed attachment/extension tree.
	_, input := dnsRequest(t, r, assets, assets[0])
	attachments, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	input.LifecycleBindings = append(input.LifecycleBindings, attachments.Bindings...)
	input.Relationships = append(input.Relationships, attachments.Relationships...)
	input.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{assets[3].Identity.NativeID}}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 {
		t.Fatalf("VM extension mixed plan %+v %v", result, err)
	}
	return servicePlanRequest(result, assets, assets[2]), input
}

func TestVMExtensionNativeInventoryAndIndependentDeletion(t *testing.T) {
	s, r, assets, extension := vmExtensionScenario(t)
	list := productRequest(r, vmExtensionType)
	items := []contracts.InventoryItem{}
	for {
		batch, err := r.List(context.Background(), list)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			break
		}
		list.Cursor = batch.NextCursor
	}
	if len(items) != 1 || items[0].NativeID != text(extension["id"]) || items[0].Location != "eastus" {
		t.Fatalf("VM extension inventory %+v", items)
	}
	payload, _ := json.Marshal(items)
	if strings.Contains(string(payload), "do-not-export") {
		t.Fatal("VM extension public settings leaked into inventory")
	}
	_, input := vmExtensionRequest(t, r, assets)
	selected := assets[len(assets)-1]
	input.ResolvedAssetIDs = []asset.AssetID{selected.ID}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != selected.ID {
		t.Fatalf("independent extension plan %+v %v", result, err)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", selected)
	if err != nil {
		t.Fatal(err)
	}
	request := servicePlanRequest(result, assets, selected)
	op, err := driver.Execute(context.Background(), request)
	if err != nil || !slices.Equal(s.deletes, []string{selected.Identity.NativeID}) {
		t.Fatalf("independent extension DELETE %v %v", s.deletes, err)
	}
	wait, err := driver.Wait(context.Background(), request, op)
	if err != nil || !wait.Done || s.gone[assets[2].Identity.NativeID] {
		t.Fatalf("independent extension readback %+v %v", wait, err)
	}
}

func TestVMExtensionCascadePreservesDiskRetentionAcrossVMETagChange(t *testing.T) {
	s, r, assets, extension := vmExtensionScenario(t)
	request, input := vmExtensionRequest(t, r, assets)
	vm, disk := assets[2], assets[3]
	if len(request.LifecycleImpacts) != 2 || !slices.ContainsFunc(request.LifecycleImpacts, func(impact contracts.ActionImpact) bool { return impact.Asset.ID == disk.ID && !impact.Delete }) {
		t.Fatalf("VM extension lost mixed retention %+v", request)
	}
	input.RequestOptions[assets[0].ID]["retain_resources"] = []string{text(extension["id"])}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("extension incorrectly independently retainable %+v %v", result, err)
	}
	previous, patches := s.handle, 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "PATCH" {
			if !strings.EqualFold(req.URL.Path, vm.Identity.NativeID) || req.Header.Get("If-Match") != "member-a-etag" {
				t.Fatalf("wrong conditional VM retention %s %v", req.URL, req.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			os := object(object(object(body["properties"])["storageProfile"])["osDisk"])
			if os["deleteOption"] != "Detach" || !strings.EqualFold(text(object(os["managedDisk"])["id"]), disk.Identity.NativeID) {
				t.Fatalf("incorrect retention PATCH %+v", body)
			}
			raw := s.records[vm.Identity.NativeID]
			object(object(object(raw["properties"])["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
			raw["etag"] = "retention-update-etag"
			patches++
			return jsonResponse(200, raw, nil), true
		}
		return previous(req)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", vm)
	if err != nil {
		t.Fatal(err)
	}
	op, err := driver.Execute(context.Background(), request)
	if err != nil || patches != 1 || len(s.deletes) != 0 {
		t.Fatalf("VM retention preparation %+v %v", op, err)
	}
	for phase := 0; phase < 2; phase++ {
		payload, _ := json.Marshal(request)
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Fatal(err)
		}
		payload, _ = json.Marshal(op)
		if err := json.Unmarshal(payload, &op); err != nil {
			t.Fatal(err)
		}
		driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := driver.Wait(context.Background(), request, op)
		if err != nil || wait.Done {
			t.Fatalf("VM extension not verified after retention %+v %v", wait, err)
		}
		if wait.Data != nil {
			op.Data = wait.Data
		}
	}
	if !slices.Equal(s.deletes, []string{vm.Identity.NativeID}) || patches != 1 {
		t.Fatalf("incorrect VM retention/deletion order %v %d", s.deletes, patches)
	}
	s.gone[text(extension["id"])] = true
	wait, err := driver.Wait(context.Background(), request, op)
	if err != nil || !wait.Done || s.gone[disk.Identity.NativeID] {
		t.Fatalf("VM extension/disk completion %+v %v", wait, err)
	}
}

func TestVMExtensionCascadeRejectsMissingChangedOrForeignImpacts(t *testing.T) {
	for _, mode := range []string{"missing-extension", "retained-extension", "foreign-extension-controller", "foreign-extension-connection", "retained-disk-controller", "missing-retained-disk", "extension-etag", "extension-protected", "extension-forbidden", "collection-missing", "collection-partial", "collection-duplicate", "new-extension", "vm-recreated", "vm-etag", "new-extension-during-preparation"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, extension := vmExtensionScenario(t)
			request, _ := vmExtensionRequest(t, r, assets)
			vm := assets[2]
			ei := slices.IndexFunc(request.LifecycleImpacts, func(impact contracts.ActionImpact) bool { return impact.Asset.Identity.NativeType == vmExtensionType })
			di := 1 - ei
			add := func() {
				child := map[string]any{"id": vm.Identity.NativeID + "/extensions/new", "etag": "new-etag", "name": "new", "properties": map[string]any{}}
				s.add(child, "2024-07-01")
				s.lists[vm.Identity.NativeID+"/extensions"] = append(s.lists[vm.Identity.NativeID+"/extensions"], child)
			}
			switch mode {
			case "missing-extension":
				request.LifecycleImpacts = slices.Delete(request.LifecycleImpacts, ei, ei+1)
			case "retained-extension":
				request.LifecycleImpacts[ei].Delete = false
			case "foreign-extension-controller":
				request.LifecycleImpacts[ei].ControllerID = assets[3].ID
			case "foreign-extension-connection":
				request.LifecycleImpacts[ei].Asset.Identity.ConnectionID = "other"
			case "retained-disk-controller":
				request.LifecycleImpacts[di].ControllerID = request.LifecycleImpacts[ei].Asset.ID
			case "missing-retained-disk":
				request.LifecycleImpacts = slices.Delete(request.LifecycleImpacts, di, di+1)
			case "extension-etag":
				extension["etag"] = "replaced"
			case "extension-protected":
				extension["tags"] = map[string]any{"steward:protected": "yes"}
			case "extension-forbidden":
				s.status[text(extension["id"])] = 403
			case "collection-missing":
				s.status[vm.Identity.NativeID+"/extensions"] = 404
			case "collection-partial":
				s.status[vm.Identity.NativeID+"/extensions"] = 206
			case "collection-duplicate":
				s.lists[vm.Identity.NativeID+"/extensions"] = []any{extension, extension}
			case "new-extension":
				add()
			case "vm-recreated":
				object(s.records[vm.Identity.NativeID]["properties"])["vmId"] = "recreated"
			case "vm-etag":
				s.records[vm.Identity.NativeID]["etag"] = "unreviewed-update"
			case "new-extension-during-preparation":
				previous, reads := s.handle, 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, vm.Identity.NativeID) {
						reads++
						if reads == 3 {
							add()
						}
					}
					return previous(req)
				}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("unreviewed VM extension deletion accepted %v %v", s.deletes, err)
			}
		})
	}
}

func TestAKSIncludesStandardVMExtensionsAndWaitsForTheirAbsence(t *testing.T) {
	s := newAKSScenario()
	extension := map[string]any{"id": text(s.vm["id"]) + "/extensions/agent", "name": "agent", "etag": "agent-etag", "properties": map[string]any{"typeHandlerVersion": "1.0"}}
	s.extensions = []any{extension}
	r := s.runtime(t)
	assets := append(s.assets(t), dnsAsset(t, r, extension))
	// The existing AKS fixture uses this explicit public-cloud partition.
	assets[len(assets)-1].Identity.Partition = assets[0].Identity.Partition
	request, _ := aksRequest(t, r, assets)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	op, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.clusterGone, s.groupGone, s.childrenGone = true, true, true
	wait, err := driver.Wait(context.Background(), request, op)
	if err != nil || wait.Done || wait.State != "deleting_aks_resources" {
		t.Fatalf("AKS omitted ordinary VM extension %+v %v", wait, err)
	}
	s.extensions = nil
	wait, err = driver.Wait(context.Background(), request, op)
	if err != nil || !wait.Done || s.deletes != 1 {
		t.Fatalf("AKS VM extension readback %+v %v", wait, err)
	}
}
