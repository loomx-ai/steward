package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func uniformScaleSetScenario() (*dnsScenario, []map[string]any) {
	s := newDNSScenario()
	scaleID := resourceID(scaleSetType, "scale")
	vmID := scaleID + "/virtualMachines/0"
	nicID := vmID + "/networkInterfaces/nic"
	ipID := nicID + "/ipConfigurations/ip"
	publicID := ipID + "/publicIPAddresses/public"
	osID, dataID := resourceID(diskType, "scale-os"), resourceID(diskType, "scale-data")
	scale := map[string]any{"id": scaleID, "type": scaleSetType, "name": "scale", "location": "eastus", "etag": "scale-etag", "properties": map[string]any{"orchestrationMode": "Uniform", "uniqueId": "scale-creation-id"}}
	vm := map[string]any{"id": vmID, "type": vmType, "name": "scale_0", "instanceId": "0", "location": "eastus", "etag": "vm-etag", "properties": map[string]any{"vmId": "vm-creation-id", "storageProfile": map[string]any{"osDisk": map[string]any{"createOption": "FromImage", "managedDisk": map[string]any{"id": osID}}, "dataDisks": []any{map[string]any{"lun": 0, "createOption": "Empty", "managedDisk": map[string]any{"id": dataID}}}}, "networkProfile": map[string]any{"networkInterfaces": []any{map[string]any{"id": nicID}}}}}
	diskOS := map[string]any{"id": osID, "type": diskType, "name": "scale-os", "location": "eastus", "managedBy": vmID, "properties": map[string]any{"uniqueId": "os-creation-id"}}
	diskData := map[string]any{"id": dataID, "type": diskType, "name": "scale-data", "location": "eastus", "managedBy": vmID, "properties": map[string]any{"uniqueId": "data-creation-id"}}
	public := map[string]any{"id": publicID, "name": "public", "properties": map[string]any{"ipAddress": "203.0.113.2", "ipConfiguration": map[string]any{"id": ipID}}}
	ip := map[string]any{"id": ipID, "name": "ip", "properties": map[string]any{"privateIPAddress": "10.0.0.4", "publicIPAddress": map[string]any{"id": publicID}}}
	nic := map[string]any{"id": nicID, "name": "nic", "properties": map[string]any{"virtualMachine": map[string]any{"id": vmID}, "ipConfigurations": []any{ip}}}
	extension := map[string]any{"id": scaleID + "/extensions/health", "name": "health", "properties": map[string]any{"publisher": "Microsoft.ManagedServices", "type": "ApplicationHealthLinux", "typeHandlerVersion": "1.0", "settings": map[string]any{"secret-in-script": "do-not-expose"}}}
	vmExtension := map[string]any{"id": vmID + "/extensions/health", "type": "Microsoft.Compute/virtualMachines/extensions", "name": "health", "properties": map[string]any{"publisher": "Microsoft.ManagedServices", "type": "ApplicationHealthLinux", "typeHandlerVersion": "1.0"}}
	raw := []map[string]any{scale, vm, diskOS, diskData, nic, ip, public, extension, vmExtension}
	for _, value := range raw {
		_, kind, _ := parseID(text(value["id"]))
		version := "2024-07-01"
		if strings.EqualFold(kind, diskType) {
			version = "2024-03-02"
		}
		if strings.Contains(kind, "/networkinterfaces") {
			version = "2018-10-01"
		}
		s.add(value, version)
	}
	for _, collection := range []struct {
		path, version string
		values        []any
	}{
		{scaleID + "/virtualMachines", "2024-07-01", []any{vm}}, {scaleID + "/extensions", "2024-07-01", []any{extension}},
		{vmID + "/networkInterfaces", "2018-10-01", []any{nic}}, {vmID + "/extensions", "2024-07-01", []any{vmExtension}},
		{nicID + "/ipConfigurations", "2018-10-01", []any{ip}}, {ipID + "/publicIPAddresses", "2018-10-01", []any{public}},
	} {
		s.lists[strings.ToLower(collection.path)] = collection.values
		s.version[strings.ToLower(collection.path)] = collection.version
	}
	return s, raw
}

func TestUniformScaleSetReviewedNestedDeletionAndIndependentVM(t *testing.T) {
	for _, selected := range []int{0, 1, 8} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			s, raw := uniformScaleSetScenario()
			r := s.runtime(t)
			assets := []asset.Asset{}
			for _, value := range raw {
				assets = append(assets, dnsAsset(t, r, value))
			}
			request, input := dnsRequest(t, r, assets, assets[selected])
			expected := 8
			if selected == 1 {
				expected = 6
			}
			if selected == 8 {
				expected = 0
			}
			if len(request.LifecycleImpacts) != expected {
				t.Fatalf("lost Uniform descendants: %+v", request.LifecycleImpacts)
			}
			for _, target := range assets[2:7] {
				if selected == 8 {
					break
				}
				input.RequestOptions = map[asset.AssetID]map[string]any{assets[selected].ID: {"retain_resources": []string{target.Identity.NativeID}}}
				result, err := plan.Solve(input)
				if err != nil || len(result.Blockers) == 0 {
					t.Fatalf("Uniform retention ignored for %s %+v %v", target.ID, result, err)
				}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[selected])
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != strings.ToLower(text(raw[selected]["id"])) {
				t.Fatalf("Uniform deletion %v %v", s.deletes, err)
			}
			serialized, _ := json.Marshal(request)
			if err := json.Unmarshal(serialized, &request); err != nil {
				t.Fatal(err)
			}
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if expected > 0 {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatalf("Uniform descendants omitted: %+v %v", wait, err)
				}
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[strings.ToLower(impact.Asset.Identity.NativeID)] = true
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatalf("Uniform readback: %+v %v", wait, err)
			}
			if selected != 0 && s.gone[strings.ToLower(text(raw[0]["id"]))] {
				t.Fatal("independent child removed scale set")
			}
		})
	}
}

func TestScaleSetNativeProductRoutesAndControllerOnlyNetwork(t *testing.T) {
	for _, index := range []int{1, 4, 5, 6, 7, 8} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			s, raw := uniformScaleSetScenario()
			root := "/subscriptions/" + testSubscription
			s.lists[strings.ToLower(root+"/providers/"+scaleSetType)] = []any{raw[0]}
			s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test", "location": "eastus"}}
			r := s.runtime(t)
			_, nativeType, _ := parseID(text(raw[index]["id"]))
			batch, err := r.List(context.Background(), productRequest(r, nativeType))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "eastus" || batch.Items[0].NativeID != strings.ToLower(text(raw[index]["id"])) {
				t.Fatalf("native Uniform inventory %+v %v", batch, err)
			}
			item := batch.Items[0]
			value := asset.Asset{ID: "selected", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
			if index >= 4 && index <= 6 {
				if item.Normalized["cleanup_controller_only"] != true {
					t.Fatal("managed network lost controller policy")
				}
				if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
					t.Fatal("invented native network DELETE")
				}
			}
			if index == 1 && item.Normalized["instanceId"] != "0" {
				t.Fatal("native instance ID lost")
			}
			if index == 7 {
				encoded, _ := json.Marshal(item)
				if strings.Contains(string(encoded), "do-not-expose") {
					t.Fatal("extension settings leaked")
				}
			}
		})
	}
}

func TestOfficialResponseAliasesRemainBoundToNativeIDs(t *testing.T) {
	// These unchanged upstream examples are independent of the wire fixtures above.
	var sources []struct{ File, SourceURI, SourceSHA256 string }
	data, err := os.ReadFile("fixtures/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest {
		sources = append(sources, struct{ File, SourceURI, SourceSHA256 string }{entry["file"], entry["source_uri"], entry["source_sha256"]})
	}
	for _, tc := range []struct{ file, kind string }{{"VirtualMachineScaleSetVM_Get_WithUserData.json", scaleSetVMType}, {"VmssNetworkInterfaceGet.json", scaleSetNICType}, {"VmssNetworkInterfaceIpConfigGet.json", scaleSetIPConfigType}, {"VmssPublicIpGet.json", scaleSetPublicIPType}, {"VpnSiteLinkConnectionGet.json", vpnLinkConnectionType}, {"NatRuleGet.json", vpnNATRuleType}, {"ExpressRouteConnectionGet.json", expressConnectionType}} {
		t.Run(tc.file, func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, source := range sources {
				if source.File == tc.file {
					found = source.SourceSHA256 == fmt.Sprintf("%x", sha256.Sum256(payload)) && strings.HasPrefix(source.SourceURI, "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/")
				}
			}
			if !found {
				t.Fatal("official fixture provenance changed")
			}
			payload = []byte(strings.NewReplacer("{subscription-id}", testSubscription, "subid", testSubscription, "{vmss-name}", "scale").Replace(string(payload)))
			var example map[string]any
			if err = json.Unmarshal(payload, &example); err != nil {
				t.Fatal(err)
			}
			raw := object(object(object(example["responses"])["200"])["body"])
			id := responseID(tc.kind, text(raw["id"]))
			if !validResourceResponse(response{status: 200, data: raw}, id, tc.kind) {
				t.Fatalf("rejected documented native type: %s", raw["type"])
			}
			c := &client{subscription: testSubscription}
			kind, _ := findType(tc.kind)
			if _, err := c.resourceURL(kind, id); err != nil {
				t.Fatal(err)
			}
			foreign := strings.Replace(id, "/providers/", "/unrelated/", 1)
			if _, err := c.resourceURL(kind, foreign); err == nil {
				t.Fatal("type alias accepted unrelated path")
			}
			raw["type"] = privateEndpointType
			if validResourceResponse(response{status: 200, data: raw}, id, tc.kind) {
				t.Fatal("unlisted response type alias accepted")
			}
		})
	}
}

func flexibleScaleSetScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	scaleID := resourceID(scaleSetType, "flexible")
	scale := map[string]any{"id": scaleID, "type": scaleSetType, "name": "flexible", "location": "eastus", "etag": "scale-etag", "properties": map[string]any{"orchestrationMode": "Flexible", "uniqueId": "scale-uid"}}
	extension := map[string]any{"id": scaleID + "/extensions/health", "name": "health", "properties": map[string]any{"typeHandlerVersion": "1.0"}}
	raw := []map[string]any{scale, extension}
	members := []any{}
	for _, name := range []string{"member-a", "member-b", "unrelated"} {
		vmID, diskID := resourceID(vmType, name), resourceID(diskType, name+"-os")
		vm := map[string]any{"id": vmID, "type": vmType, "name": name, "location": "eastus", "etag": name + "-etag", "properties": map[string]any{"vmId": name + "-uid", "storageProfile": map[string]any{"osDisk": map[string]any{"name": name + "-os", "deleteOption": "Delete", "managedDisk": map[string]any{"id": diskID}}}, "networkProfile": map[string]any{"networkInterfaces": []any{}}}}
		if name != "unrelated" {
			object(vm["properties"])["virtualMachineScaleSet"] = map[string]any{"id": scaleID}
		}
		disk := map[string]any{"id": diskID, "type": diskType, "name": name + "-os", "location": "eastus", "properties": map[string]any{"uniqueId": name + "-disk-uid"}}
		raw = append(raw, vm, disk)
		members = append(members, vm)
		s.lists[strings.ToLower(vmID+"/extensions")] = []any{}
		s.version[strings.ToLower(vmID+"/extensions")] = "2024-07-01"
	}
	for _, value := range raw {
		version := "2024-07-01"
		if value["type"] == diskType {
			version = "2024-03-02"
		}
		s.add(value, version)
	}
	root := "/subscriptions/" + testSubscription
	s.lists[strings.ToLower(scaleID+"/extensions")] = []any{extension}
	s.version[strings.ToLower(scaleID+"/extensions")] = "2024-07-01"
	s.lists[strings.ToLower(root+"/providers/"+scaleSetType)] = []any{scale}
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test", "location": "eastus"}}
	vmList := strings.ToLower(root + "/providers/" + vmType)
	s.version[vmList] = "2024-07-01"
	s.lists[vmList] = members
	// Force two native ListAll pages, including a VM outside the scale set.
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "GET" || !strings.EqualFold(req.URL.Path, vmList) {
			return nil, false
		}
		values := []any{}
		page := s.lists[vmList][:1]
		if req.URL.Query().Get("$skiptoken") == "second" {
			page = s.lists[vmList][1:]
		}
		for _, value := range page {
			if !s.gone[strings.ToLower(text(object(value)["id"]))] {
				values = append(values, value)
			}
		}
		response := map[string]any{"value": values}
		if req.URL.Query().Get("$skiptoken") == "" {
			response["nextLink"] = apiURL(vmList, "2024-07-01") + "&$skiptoken=second"
		}
		return jsonResponse(200, response, nil), true
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, value := range raw {
		assets = append(assets, dnsAsset(t, r, value))
	}
	return s, r, assets
}

func TestFlexibleScaleSetPriorVMDeletionAndRetainedDisk(t *testing.T) {
	s, r, assets := flexibleScaleSetScenario(t)
	root, extension, memberA, diskA, memberB := assets[0], assets[1], assets[2], assets[3], assets[4]
	_, input := dnsRequest(t, r, assets, root)
	attachments, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
	if err != nil || len(attachments.Unresolved) != 0 {
		t.Fatalf("attachments %+v %v", attachments, err)
	}
	input.LifecycleBindings = append(input.LifecycleBindings, attachments.Bindings...)
	input.Relationships = append(input.Relationships, attachments.Relationships...)
	input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{diskA.Identity.NativeID}}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 {
		t.Fatalf("Flexible plan %+v %v", result, err)
	}
	steps := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range result.Steps {
		if step.Action == "delete" {
			steps[step.AssetID] = step
		}
	}
	if len(steps) != 3 || !slices.Contains(steps[root.ID].DependsOn, steps[memberA.ID].ID) || !slices.Contains(steps[root.ID].DependsOn, steps[memberB.ID].ID) {
		t.Fatalf("Flexible VM deletion order missing: %+v", result.Steps)
	}
	rootRequest := servicePlanRequest(result, assets, root)
	if len(rootRequest.LifecycleImpacts) != 1 || rootRequest.LifecycleImpacts[0].Asset.ID != extension.ID {
		t.Fatalf("Flexible direct members incorrectly delegated %+v", rootRequest.LifecycleImpacts)
	}
	rootDriver, err := r.ResolveAction(context.Background(), "connection", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootDriver.Execute(context.Background(), rootRequest); err == nil || len(s.deletes) != 0 {
		t.Fatal("scale set deleted before its Flexible VMs")
	}
	batch, err := r.List(context.Background(), productRequest(r, scaleSetVMType))
	if err != nil || !batch.Complete || len(batch.Items) != 0 {
		t.Fatalf("Flexible inventory used Uniform API: %+v %v", batch, err)
	}
	patched := 0
	originalHandler := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "PATCH" {
			if !strings.EqualFold(req.URL.Path, memberA.Identity.NativeID) || req.Header.Get("If-Match") != "member-a-etag" || len(s.deletes) != 0 {
				t.Fatalf("wrong retention PATCH %s %v", req.URL, req.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			os := object(object(object(body["properties"])["storageProfile"])["osDisk"])
			if os["deleteOption"] != "Detach" || !strings.EqualFold(text(object(os["managedDisk"])["id"]), diskA.Identity.NativeID) {
				t.Fatalf("lost retained disk %+v", body)
			}
			object(object(object(s.records[memberA.Identity.NativeID]["properties"])["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
			patched++
			return jsonResponse(200, s.records[memberA.Identity.NativeID], nil), true
		}
		return originalHandler(req)
	}
	for _, member := range []asset.Asset{memberA, memberB} {
		request := servicePlanRequest(result, assets, member)
		if len(request.LifecycleImpacts) != 1 || request.LifecycleImpacts[0].Delete != (member.ID == memberB.ID) {
			t.Fatalf("retention lost across direct VM action %+v", request)
		}
		driver, err := r.ResolveAction(context.Background(), "connection", member)
		if err != nil {
			t.Fatal(err)
		}
		op, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		done := false
		for attempts := 0; attempts < 5; attempts++ {
			// Resume each asynchronous phase with only serialized request/result.
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
			if err != nil {
				t.Fatal(err)
			}
			if wait.Data != nil {
				op.Data = wait.Data
			}
			if wait.Done {
				done = true
				break
			}
		}
		if !done {
			t.Fatal("Flexible VM never completed")
		}
	}
	op, err := rootDriver.Execute(context.Background(), rootRequest)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := rootDriver.Wait(context.Background(), rootRequest, op)
	if err != nil || wait.Done {
		t.Fatalf("root extension absence not verified %+v %v", wait, err)
	}
	s.gone[extension.Identity.NativeID] = true
	wait, err = rootDriver.Wait(context.Background(), rootRequest, op)
	if err != nil || !wait.Done || patched != 1 || !slices.Equal(s.deletes, []string{memberA.Identity.NativeID, memberB.Identity.NativeID, root.Identity.NativeID}) || s.gone[diskA.Identity.NativeID] || s.gone[assets[6].Identity.NativeID] {
		t.Fatalf("Flexible deletion/retention %+v mutations=%v patches=%d err=%v", wait, s.deletes, patched, err)
	}
}

func TestUniformScaleSetRejectsChangedOrUnreviewedDescendants(t *testing.T) {
	for _, mode := range []string{"root-mode", "root-uid", "vm-uid", "vm-etag", "vm-protected", "disk-uid", "disk-owner", "disk-other-owner", "disk-owners-malformed", "disk-shared-unknown", "disk-lock", "disk-protected", "disk-group-owned", "disk-foreign-id", "disk-detach", "disk-policy-unknown", "disk-vhd", "disk-missing", "disk-duplicate-lun", "disk-missing-os", "nic-owner", "nic-missing", "nic-duplicate", "nic-foreign", "list-duplicate", "list-forbidden", "list-partial", "list-missing", "public-ip-replaced", "missing-impact", "retained-impact", "foreign-impact", "unknown-mode"} {
		t.Run(mode, func(t *testing.T) {
			s, raw := uniformScaleSetScenario()
			r := s.runtime(t)
			assets := []asset.Asset{}
			for _, value := range raw {
				assets = append(assets, dnsAsset(t, r, value))
			}
			request, _ := dnsRequest(t, r, assets, assets[0])
			vm := object(raw[1]["properties"])
			storage := object(vm["storageProfile"])
			network := object(vm["networkProfile"])
			switch mode {
			case "root-mode":
				object(raw[0]["properties"])["orchestrationMode"] = "Flexible"
			case "root-uid":
				object(raw[0]["properties"])["uniqueId"] = "recreated"
			case "vm-uid":
				vm["vmId"] = "recreated"
			case "vm-etag":
				raw[1]["etag"] = "changed"
			case "vm-protected":
				vm["protectionPolicy"] = map[string]any{"protectFromScaleSetActions": true}
			case "disk-uid":
				object(raw[2]["properties"])["uniqueId"] = "recreated"
			case "disk-owner":
				raw[2]["managedBy"] = resourceID(vmType, "other")
			case "disk-other-owner":
				raw[2]["managedByExtended"] = []any{text(raw[1]["id"]), resourceID(vmType, "other")}
			case "disk-owners-malformed":
				raw[2]["managedByExtended"] = "unknown"
			case "disk-shared-unknown":
				object(raw[2]["properties"])["maxShares"] = 2
			case "disk-lock":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": text(raw[2]["id"]) + "/providers/Microsoft.Authorization/locks/lock", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return nil, false
				}
			case "disk-protected":
				raw[2]["tags"] = map[string]any{"steward/protected": "true"}
			case "disk-group-owned":
				group := strings.Join(strings.Split(assets[2].Identity.NativeID, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": resourceID(aksType, "other")}
			case "disk-foreign-id":
				raw[2]["id"] = resourceID(diskType, "other")
			case "disk-detach":
				object(storage["osDisk"])["deleteOption"] = "Detach"
			case "disk-policy-unknown":
				object(storage["osDisk"])["deleteOption"] = "unknown"
			case "disk-vhd":
				object(storage["osDisk"])["vhd"] = map[string]any{"uri": "https://storage.blob.core.windows.net/vhds/os.vhd"}
			case "disk-missing":
				s.gone[assets[2].Identity.NativeID] = true
			case "disk-duplicate-lun":
				storage["dataDisks"] = append(array(storage["dataDisks"]), array(storage["dataDisks"])[0])
			case "disk-missing-os":
				delete(storage, "osDisk")
			case "nic-owner":
				object(raw[4]["properties"])["virtualMachine"] = map[string]any{"id": resourceID(vmType, "other")}
			case "nic-missing":
				s.lists[assets[1].Identity.NativeID+"/networkinterfaces"] = []any{}
			case "nic-duplicate":
				network["networkInterfaces"] = append(array(network["networkInterfaces"]), array(network["networkInterfaces"])[0])
			case "nic-foreign":
				network["networkInterfaces"] = []any{map[string]any{"id": resourceID(nicType, "other")}}
			case "list-duplicate":
				s.lists[assets[0].Identity.NativeID+"/virtualmachines"] = []any{raw[1], raw[1]}
			case "list-forbidden":
				s.status[assets[4].Identity.NativeID+"/ipconfigurations"] = 403
			case "list-partial":
				s.status[assets[4].Identity.NativeID+"/ipconfigurations"] = 206
			case "list-missing":
				s.status[assets[4].Identity.NativeID+"/ipconfigurations"] = 404
			case "public-ip-replaced":
				raw[6]["etag"] = "created-after-review"
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "retained-impact":
				request.LifecycleImpacts[0].Delete = false
			case "foreign-impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "unknown-mode":
				object(raw[0]["properties"])["orchestrationMode"] = "unknown"
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("unsafe Uniform deletion allowed: %v %v", s.deletes, err)
			}
		})
	}
}

func TestFlexibleScaleSetStandaloneVMRequiresStableOwner(t *testing.T) {
	for _, mode := range []string{"allowed", "parent-uniform", "parent-unknown", "parent-forbidden", "parent-foreign", "member-removed", "member-moved", "member-protected", "member-moved-during-preparation"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := flexibleScaleSetScenario(t)
			member := assets[2]
			_, input := dnsRequest(t, r, assets, assets[0])
			attachments, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
			if err != nil {
				t.Fatal(err)
			}
			input.LifecycleBindings = append(input.LifecycleBindings, attachments.Bindings...)
			input.Relationships = append(input.Relationships, attachments.Relationships...)
			input.ResolvedAssetIDs = []asset.AssetID{member.ID}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 {
				t.Fatalf("standalone Flexible VM plan %+v %v", result, err)
			}
			request := servicePlanRequest(result, assets, member)
			root, vm := s.records[assets[0].Identity.NativeID], s.records[member.Identity.NativeID]
			switch mode {
			case "parent-uniform":
				object(root["properties"])["orchestrationMode"] = "Uniform"
			case "parent-unknown":
				object(root["properties"])["orchestrationMode"] = "unknown"
			case "parent-forbidden":
				s.status[assets[0].Identity.NativeID] = 403
			case "parent-foreign":
				root["id"] = resourceID(scaleSetType, "other")
			case "member-removed":
				delete(object(vm["properties"]), "virtualMachineScaleSet")
			case "member-moved":
				object(vm["properties"])["virtualMachineScaleSet"] = map[string]any{"id": resourceID(scaleSetType, "other")}
			case "member-protected":
				object(vm["properties"])["protectionPolicy"] = map[string]any{"protectFromScaleIn": true}
			case "member-moved-during-preparation":
				reads, original := 0, s.handle
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, member.Identity.NativeID) {
						reads++
						if reads == 3 {
							delete(object(vm["properties"]), "virtualMachineScaleSet")
						}
					}
					return original(req)
				}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", member)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if mode == "allowed" {
				if err != nil || !slices.Equal(s.deletes, []string{member.Identity.NativeID}) {
					t.Fatalf("standalone Flexible VM deletion failed %v %v", s.deletes, err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatalf("changed Flexible ownership accepted %v %v", s.deletes, err)
			}
		})
	}
}
