package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func nestedAKSScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s, uniform := uniformScaleSetScenario()
	// Uniform instance deletion also owns a referenced disk outside the group.
	disk := uniform[3]
	oldDisk := strings.ToLower(text(disk["id"]))
	disk["id"] = strings.Replace(text(disk["id"]), "/resourceGroups/test/", "/resourceGroups/shared-compute/", 1)
	object(object(array(object(object(uniform[1]["properties"])["storageProfile"])["dataDisks"])[0])["managedDisk"])["id"] = disk["id"]
	delete(s.records, oldDisk)
	s.add(disk, "2024-03-02")
	endpoint, pe := privateEndpointScenario()
	registration, _ := privateDNSLinkScenario(false)
	// Place the registration zone outside the node group while keeping its
	// VNet in the group. Preserve the native document structure and references.
	oldZone := strings.ToLower(resourceID(privateDNSZoneType, "internal.example.com"))
	newZone := strings.Replace(oldZone, "/resourcegroups/test/", "/resourcegroups/shared-dns/", 1)
	rewrite := func(value any) []byte {
		payload, _ := json.Marshal(value)
		return []byte(strings.NewReplacer(resourceID(privateDNSZoneType, "internal.example.com"), newZone, oldZone, newZone).Replace(string(payload)))
	}
	for path, raw := range registration.records {
		var updated map[string]any
		if err := json.Unmarshal(rewrite(raw), &updated); err != nil {
			t.Fatal(err)
		}
		s.add(updated, registration.version[path])
	}
	for path, values := range registration.lists {
		var updated []any
		if err := json.Unmarshal(rewrite(values), &updated); err != nil {
			t.Fatal(err)
		}
		s.lists[strings.ReplaceAll(path, oldZone, newZone)] = updated
	}
	for path, raw := range endpoint.records {
		s.add(raw, endpoint.version[path])
	}
	for path, values := range endpoint.lists {
		s.lists[path] = values
	}
	root := "/subscriptions/" + testSubscription
	groupID := root + "/resourcegroups/test"
	clusterID := strings.Replace(resourceID(aksType, "cluster"), "/resourceGroups/test/", "/resourceGroups/management/", 1)
	cluster := map[string]any{"id": clusterID, "type": aksType, "name": "cluster", "location": "eastus", "etag": "cluster-etag", "properties": map[string]any{"nodeResourceGroup": "test", "resourceUID": "cluster-creation-id"}}
	group := map[string]any{"id": groupID, "type": groupType, "name": "test", "location": "eastus", "etag": "node-group-etag", "managedBy": clusterID}
	s.add(cluster, "2024-02-01")
	s.add(group, resourcesVersion)
	vnetID := strings.ToLower(resourceID(vnetType, "vnet1"))
	s.lists[vnetID+"/subnets"] = []any{}
	// An ARM resource-group index can omit all lifecycle properties and every
	// nested resource. Product GET/List calls must reconstruct the entire tree.
	members := []any{}
	for _, value := range []map[string]any{uniform[0], uniform[2], pe[0], pe[1], s.records[vnetID]} {
		id := text(value["id"])
		_, kind, _ := parseID(id)
		members = append(members, map[string]any{"id": id, "type": kind, "location": "eastus"})
	}
	s.lists[groupID+"/resources"] = members
	zones := []any{s.records[newZone], pe[5]}
	s.lists[strings.ToLower(root+"/providers/"+privateDNSZoneType)] = zones
	s.lists[root+"/resourcegroups"] = []any{group, map[string]any{"id": root + "/resourcegroups/shared-dns", "location": "eastus"}, map[string]any{"id": root + "/resourcegroups/management", "location": "eastus"}}
	r := s.runtime(t)
	assets := []asset.Asset{dnsAsset(t, r, cluster)}
	ids := []string{}
	for id := range s.records {
		if !strings.EqualFold(id, clusterID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		assets = append(assets, dnsAsset(t, r, s.records[id]))
	}
	return s, r, assets
}

func nestedAKSRequest(t *testing.T, r *Runtime, assets []asset.Asset) (contracts.ActionRequest, plan.Input) {
	t.Helper()
	cluster, err := r.ClusterLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := cluster.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatalf("nested AKS contribution %+v %v", contribution, err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{assets[0].ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	service, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Contribute(context.Background(), "scope", assets)
	if err != nil || len(other.Unresolved) != 0 {
		t.Fatalf("nested service contribution %+v %v", other, err)
	}
	input.LifecycleBindings = append(input.LifecycleBindings, other.Bindings...)
	input.Relationships = append(input.Relationships, other.Relationships...)
	attachments, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
	if err != nil || len(attachments.Bindings) != 0 {
		t.Fatalf("AKS attachment ownership conflicted %+v %v", attachments, err)
	}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 {
		t.Fatalf("nested AKS plan %+v %v", result, err)
	}
	return servicePlanRequest(result, assets, assets[0]), input
}

func TestAKSReviewsNestedScaleSetEndpointAndExternalDNSCascades(t *testing.T) {
	s, r, assets := nestedAKSScenario(t)
	request, input := nestedAKSRequest(t, r, assets)
	if len(request.LifecycleImpacts) != 18 {
		t.Fatalf("nested AKS impact count %d %+v", len(request.LifecycleImpacts), request.LifecycleImpacts)
	}
	preserved := []string{}
	for _, value := range assets[1:] {
		if strings.EqualFold(value.Identity.NativeType, privateDNSZoneType) || strings.HasSuffix(value.Identity.NativeID, "/a/manual") {
			preserved = append(preserved, value.Identity.NativeID)
			if slices.ContainsFunc(request.LifecycleImpacts, func(impact contracts.ActionImpact) bool { return impact.Asset.ID == value.ID }) {
				t.Fatal("AKS included shared DNS zone or manual record")
			}
		}
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || !impact.Delete {
			t.Fatalf("nested AKS ownership lost %+v", impact)
		}
		input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_resources": []string{impact.Asset.Identity.NativeID}}}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) == 0 {
			t.Fatalf("AKS retention lost for %s %+v %v", impact.Asset.Identity.NativeID, result, err)
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	op, err := driver.Execute(context.Background(), request)
	if err != nil || !slices.Equal(s.deletes, []string{request.Asset.Identity.NativeID}) {
		t.Fatalf("nested AKS native deletion %v %v", s.deletes, err)
	}
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
	if err != nil || wait.Done || wait.State != "deleting_node_resource_group" {
		t.Fatalf("AKS group readback %+v %v", wait, err)
	}
	s.gone["/subscriptions/"+testSubscription+"/resourcegroups/test"] = true
	for _, impact := range request.LifecycleImpacts {
		if strings.EqualFold(impact.Asset.Identity.NativeType, groupType) {
			continue
		}
		wait, err = driver.Wait(context.Background(), request, op)
		if err != nil || wait.Done {
			t.Fatalf("AKS finished before descendant %s %+v %v", impact.Asset.Identity.NativeID, wait, err)
		}
		if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
			t.Fatalf("AKS resumed deletion repeated %v %v", s.deletes, err)
		}
		s.gone[impact.Asset.Identity.NativeID] = true
	}
	wait, err = driver.Wait(context.Background(), request, op)
	if err != nil || !wait.Done || len(preserved) != 3 {
		t.Fatalf("AKS final descendant readback %+v %v preserved=%v", wait, err, preserved)
	}
	for _, id := range preserved {
		if s.gone[id] {
			t.Fatalf("AKS removed shared resource %s", id)
		}
	}
}

func TestAKSRejectsExternalAndNestedResourceChanges(t *testing.T) {
	for _, mode := range []string{"missing-external-record", "missing-external-link", "retain-record", "foreign-impact", "external-unknown", "frozen-link-owner", "frozen-disk-owner", "record-generation", "record-protected", "record-forbidden", "external-group-owned", "external-group-forbidden", "external-group-foreign", "external-lock", "vm-generation", "vm-protected", "node-group-generation", "node-group-owner-during-list", "native-detail-forbidden", "nested-list-forbidden", "new-extension", "new-external-record", "removed-external-link-still-live"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := nestedAKSScenario(t)
			request, _ := nestedAKSRequest(t, r, assets)
			find := func(kind string) int {
				for i, impact := range request.LifecycleImpacts {
					if strings.EqualFold(impact.Asset.Identity.NativeType, kind) {
						return i
					}
				}
				t.Fatalf("missing test kind %s", kind)
				return -1
			}
			ri, li, vi, di := find(privateDNSZoneType+"/A"), find(privateDNSLinkType), find(scaleSetVMType), find(diskType)
			recordID := request.LifecycleImpacts[ri].Asset.Identity.NativeID
			linkID := request.LifecycleImpacts[li].Asset.Identity.NativeID
			vmID := request.LifecycleImpacts[vi].Asset.Identity.NativeID
			external := "/subscriptions/" + testSubscription + "/resourcegroups/shared-dns"
			group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
			switch mode {
			case "missing-external-record":
				request.LifecycleImpacts = slices.Delete(request.LifecycleImpacts, ri, ri+1)
			case "missing-external-link":
				request.LifecycleImpacts = slices.Delete(request.LifecycleImpacts, li, li+1)
			case "retain-record":
				request.LifecycleImpacts[ri].Delete = false
			case "foreign-impact":
				request.LifecycleImpacts[ri].Asset.Identity.ConnectionID = "other"
			case "external-unknown":
				request.LifecycleImpacts[ri].Asset.Identity.NativeType = "Microsoft.Example/widgets"
				request.LifecycleImpacts[ri].Asset.Identity.NativeID = external + "/providers/Microsoft.Example/widgets/foreign"
			case "frozen-link-owner":
				request.LifecycleImpacts[li].Asset.Normalized["virtualNetwork"] = map[string]any{"id": resourceID(vnetType, "other")}
			case "frozen-disk-owner":
				// Move a disk outside the group in the frozen request without a
				// matching Uniform VM storage reference and reciprocal owner.
				request.LifecycleImpacts[di].Asset.Identity.NativeID = external + "/providers/Microsoft.Compute/disks/foreign"
			case "record-generation":
				s.records[recordID]["etag"] = "recreated"
			case "record-protected":
				object(s.records[recordID]["properties"])["metadata"] = map[string]any{"steward/protected": "true"}
			case "record-forbidden":
				s.status[recordID] = 403
			case "external-group-owned":
				s.records[external] = map[string]any{"id": external, "managedBy": resourceID(aksType, "other")}
			case "external-group-forbidden":
				s.status[external] = 403
			case "external-group-foreign":
				s.records[external] = map[string]any{"id": group}
			case "external-lock":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": external + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "ReadOnly"}}}}, nil), true
					}
					return nil, false
				}
			case "vm-generation":
				object(s.records[vmID]["properties"])["vmId"] = "recreated"
			case "vm-protected":
				object(s.records[vmID]["properties"])["protectionPolicy"] = map[string]any{"protectFromScaleSetActions": true}
			case "node-group-generation":
				s.records[group]["etag"] = "recreated"
			case "node-group-owner-during-list":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, group+"/resources") {
						s.records[group]["managedBy"] = resourceID(aksType, "other")
					}
					return nil, false
				}
			case "native-detail-forbidden":
				s.status[vmID] = 403
			case "nested-list-forbidden":
				s.status[vmID+"/networkinterfaces"] = 403
			case "new-extension":
				id := vmID + "/extensions/new"
				extension := map[string]any{"id": id, "name": "new", "properties": map[string]any{}}
				s.add(extension, "2024-07-01")
				s.lists[vmID+"/extensions"] = append(s.lists[vmID+"/extensions"], extension)
			case "new-external-record":
				zone := strings.Join(strings.Split(linkID, "/")[:9], "/")
				record := map[string]any{"id": zone + "/A/new", "name": "new", "etag": "new-etag", "properties": map[string]any{"isAutoRegistered": true, "ttl": 10, "aRecords": []any{map[string]any{"ipv4Address": "10.1.0.9"}}}}
				s.add(record, "2024-06-01")
				s.lists[zone+"/a"] = append(s.lists[zone+"/a"], record)
			case "removed-external-link-still-live":
				object(s.records[linkID]["properties"])["virtualNetwork"] = map[string]any{"id": resourceID(vnetType, "other")}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("unsafe AKS nested deletion accepted %v %v", s.deletes, err)
			}
		})
	}
}

func TestAKSExternalInventoryOmissionsAndChildReadbackFailures(t *testing.T) {
	for _, mode := range []string{"omitted", "wrong-connection", "wrong-partition", "stale-inventory", "read-forbidden", "read-partial", "read-wrong-id", "read-wrong-type"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := nestedAKSScenario(t)
			i := slices.IndexFunc(assets, func(value asset.Asset) bool {
				return strings.Contains(value.Identity.NativeID, "/resourcegroups/shared-dns/") && strings.HasSuffix(value.Identity.NativeID, "/a/endpoint")
			})
			if i < 0 {
				t.Fatal("missing external DNS test resource")
			}
			id := assets[i].Identity.NativeID
			if !strings.HasPrefix(mode, "read-") {
				switch mode {
				case "omitted":
					assets = slices.Delete(assets, i, i+1)
				case "wrong-connection":
					assets[i].Identity.ConnectionID = "other"
				case "wrong-partition":
					assets[i].Identity.Partition = "other"
				case "stale-inventory":
					assets[i].Normalized["_arm_generation"] = "previous"
				}
				contributor, err := r.ClusterLifecycle(context.Background(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				result, err := contributor.Contribute(context.Background(), "scope", assets)
				if mode == "stale-inventory" {
					if err == nil {
						t.Fatal("stale external inventory accepted")
					}
				} else if err != nil || len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != id {
					t.Fatalf("external resource omission lost %+v %v", result.Unresolved, err)
				}
				return
			}
			request, _ := nestedAKSRequest(t, r, assets)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			op, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			s.gone[id] = false
			switch mode {
			case "read-forbidden":
				s.status[id] = 403
			case "read-partial":
				s.status[id] = 206
			case "read-wrong-id":
				s.records[id]["id"] = strings.Replace(id, "/a/", "/a/other-", 1)
			case "read-wrong-type":
				s.records[id]["type"] = vmType
			}
			wait, err := driver.Wait(context.Background(), request, op)
			if err == nil || wait.Done || len(s.deletes) != 1 {
				t.Fatalf("invalid external readback accepted %+v %v", wait, err)
			}
		})
	}
}
