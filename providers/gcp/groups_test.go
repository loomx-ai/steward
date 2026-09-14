package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Native Compute protocol fixture. It deliberately separates operation DONE
// from effective membership/configuration, as required by the Compute API.
type managedGroupFixture struct {
	t         *testing.T
	assets    []asset.Asset
	resources map[string]map[string]any
	members   []map[string]any
	configs   []map[string]any
	mutations []string
	polls     map[string]int
	apply     map[string]func()
	mutation  func(*http.Request, map[string]any) func()
	response  func(*http.Request) (*http.Response, bool)
	groupPath string
}

func groupCopy(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var copied map[string]any
	_ = json.Unmarshal(encoded, &copied)
	return copied
}

func newManagedGroupFixture(t *testing.T, regional bool) *managedGroupFixture {
	t.Helper()
	location := "zones/us-central1-a/"
	if regional {
		location = "regions/us-central1/"
	}
	f := &managedGroupFixture{t: t, resources: map[string]map[string]any{}, polls: map[string]int{}, apply: map[string]func(){}, groupPath: location + "instanceGroupManagers/workers"}
	add := func(id, kind, path, uid string) asset.Asset {
		value := diskAsset(id, kind, path)
		value.Normalized["id"], value.Normalized["name"] = uid, last(path)
		value.Normalized["selfLink"] = "https://compute.googleapis.com/compute/v1/" + strings.TrimPrefix(value.Identity.NativeID, "//compute.googleapis.com/")
		return value
	}
	manager := add("mig", managerType, f.groupPath, "9001")
	ig := add("ig", instanceGroupType, location+"instanceGroups/workers", "9002")
	if regional {
		ig.Capabilities = nil // The native regional InstanceGroup has no DELETE.
	}
	vm := add("vm", instanceType, "zones/us-central1-a/instances/web", "1001")
	other := add("other", instanceType, "zones/us-central1-a/instances/keep-vm", "1002")
	if regional {
		other = add("other", instanceType, "zones/us-central1-b/instances/keep-vm", "1002")
	}
	boot := add("boot", "compute.googleapis.com/Disk", "zones/us-central1-a/disks/boot", "2001")
	data := add("data", "compute.googleapis.com/RegionDisk", "regions/us-central1/disks/data", "2002")
	ip := add("ip", "compute.googleapis.com/Address", "regions/us-central1/addresses/public", "3001")
	otherDisk := add("other-disk", "compute.googleapis.com/Disk", strings.Replace(strings.TrimPrefix(other.Identity.NativeID, "//compute.googleapis.com/projects/sample-project/"), "instances/keep-vm", "disks/other", 1), "2003")
	manager.Normalized["instanceGroup"] = ig.Normalized["selfLink"]
	manager.Normalized["targetSize"] = 2
	manager.Normalized["status"] = map[string]any{"isStable": true}
	vm.Normalized["deletionProtection"] = true
	vm.Normalized["disks"] = []any{
		map[string]any{"source": boot.Normalized["selfLink"], "deviceName": "boot", "autoDelete": true},
		map[string]any{"source": data.Normalized["selfLink"], "deviceName": "data", "autoDelete": false},
	}
	other.Normalized["disks"] = []any{map[string]any{"source": otherDisk.Normalized["selfLink"], "deviceName": "other", "autoDelete": true}}
	ip.Normalized["address"] = "34.20.10.5"
	vm.Normalized["networkInterfaces"] = []any{map[string]any{"name": "nic0", "accessConfigs": []any{map[string]any{"natIP": "34.20.10.5"}}}}
	for _, value := range []asset.Asset{vm, other} {
		value.Normalized["metadata"] = map[string]any{"items": []any{map[string]any{"key": "created-by", "value": manager.Normalized["selfLink"]}}}
	}
	f.members = []map[string]any{
		{"instance": vm.Normalized["selfLink"], "id": "1001", "currentAction": "NONE", "preservedStateFromPolicy": map[string]any{
			"disks":       map[string]any{"data": map[string]any{"source": data.Normalized["selfLink"], "autoDelete": "ON_PERMANENT_INSTANCE_DELETION"}},
			"externalIPs": map[string]any{"nic0": map[string]any{"ipAddress": map[string]any{"address": ip.Normalized["selfLink"]}, "autoDelete": "ON_PERMANENT_INSTANCE_DELETION"}},
		}},
		{"instance": other.Normalized["selfLink"], "id": "1002", "currentAction": "NONE"},
	}
	f.configs = []map[string]any{{"name": "web", "status": "EFFECTIVE", "fingerprint": "ZmluZ2VycHJpbnQ=", "preservedState": map[string]any{"metadata": map[string]any{"custom": "private-metadata-value"}}}}
	f.assets = []asset.Asset{manager, ig, vm, other, boot, data, ip, otherDisk}
	for _, value := range f.assets {
		f.resources[value.Identity.NativeID] = groupCopy(value.Normalized)
	}
	return f
}

func (f *managedGroupFixture) value(id string) asset.Asset {
	for _, value := range f.assets {
		if string(value.ID) == id {
			return value
		}
	}
	f.t.Fatalf("fixture has no asset %s", id)
	return asset.Asset{}
}

func (f *managedGroupFixture) live(id string) map[string]any {
	return f.resources[f.value(id).Identity.NativeID]
}

func (f *managedGroupFixture) roundTrip(r *http.Request) (*http.Response, error) {
	if f.response != nil {
		if response, handled := f.response(r); handled {
			return response, nil
		}
	}
	reply := func(value any) (*http.Response, error) {
		encoded, _ := json.Marshal(value)
		return apiResponse(r, 200, string(encoded)), nil
	}
	if r.URL.Host == "container.googleapis.com" && strings.HasSuffix(r.URL.Path, "/locations/-/clusters") {
		if r.Method != "GET" || len(r.URL.Query()) != 0 {
			f.t.Fatalf("invalid native GKE ownership lookup: %s %s", r.Method, r.URL)
		}
		return reply(map[string]any{"clusters": []any{}})
	}
	method := last(r.URL.Path)
	if method == "listManagedInstances" || method == "listPerInstanceConfigs" {
		if r.Method != "POST" || r.URL.Query().Get("maxResults") != "500" {
			f.t.Fatalf("wrong native list request: %s %s", r.Method, r.URL)
		}
		if method == "listPerInstanceConfigs" {
			return reply(map[string]any{"items": f.configs})
		}
		// Force native pagination in every discovery and preflight.
		if len(f.members) > 1 && r.URL.Query().Get("pageToken") == "" {
			return reply(map[string]any{"managedInstances": f.members[:1], "nextPageToken": "second"})
		}
		members := f.members
		if r.URL.Query().Get("pageToken") != "" {
			members = members[1:]
		}
		return reply(map[string]any{"managedInstances": members})
	}
	if strings.Contains(r.URL.Path, "/operations/") {
		if r.Method != "GET" {
			f.t.Fatal("operation polling mutated state")
		}
		f.polls[method]++
		if f.polls[method] == 1 {
			return reply(map[string]any{"status": "RUNNING"})
		}
		// DONE is visible one poll before the prepared state takes effect.
		if f.polls[method] == 3 && f.apply[method] != nil {
			f.apply[method]()
		}
		return reply(map[string]any{"status": "DONE"})
	}
	if r.Method == "POST" || r.Method == "DELETE" {
		body := map[string]any{}
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				f.t.Fatal(err)
			}
		}
		if f.mutation == nil {
			f.t.Fatalf("unexpected mutation %s %s", r.Method, r.URL)
		}
		f.mutations = append(f.mutations, method)
		op := fmt.Sprintf("operation-%d", len(f.mutations))
		f.apply[op] = f.mutation(r, body)
		return reply(map[string]any{"name": op, "status": "PENDING"})
	}
	if strings.HasSuffix(r.URL.Path, "/aggregated/instanceGroupManagers") {
		return reply(map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"instanceGroupManagers": []any{f.live("mig")}}}})
	}
	id := "//compute.googleapis.com/" + strings.TrimPrefix(r.URL.Path, "/compute/v1/")
	if live := f.resources[id]; live != nil {
		return reply(live)
	}
	return apiResponse(r, 404, `{}`), nil
}

func (f *managedGroupFixture) driver() *action {
	return protocolAction(f.t, managerType, "projects/sample-project/"+f.groupPath, f.roundTrip)
}

func (f *managedGroupFixture) plan(retain ...string) (plan.Result, governance.Contribution) {
	f.t.Helper()
	contribution, err := (&computeGroups{client: f.driver().client}).Contribute(context.Background(), "scope", f.assets)
	if err != nil {
		f.t.Fatal(err)
	}
	ids := []asset.AssetID{}
	for _, value := range f.assets {
		ids = append(ids, value.ID)
	}
	result, err := plan.Solve(plan.Input{CleanupTaskID: "cleanup", Assets: f.assets, ResolvedAssetIDs: ids, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings, RequestOptions: map[asset.AssetID]map[string]any{"mig": {"retain_resources": retain}}})
	if err != nil {
		f.t.Fatal(err)
	}
	return result, contribution
}

func (f *managedGroupFixture) request(retain ...string) contracts.ActionRequest {
	result, _ := f.plan(retain...)
	if len(result.Blockers) != 0 {
		f.t.Fatalf("blocked fixture plan: %+v", result.Blockers)
	}
	request := contracts.ActionRequest{Asset: f.value("mig"), Action: "delete", IdempotencyKey: "cleanup-group"}
	for _, impact := range result.ImpactItems {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: f.value(string(impact.AssetID)), ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
	}
	return request
}

func TestManagedGroupDiscoveryAndPlanNativePolicies(t *testing.T) {
	for _, regional := range []bool{false, true} {
		t.Run(fmt.Sprint(regional), func(t *testing.T) {
			f := newManagedGroupFixture(t, regional)
			result, contribution := f.plan("other", f.value("ip").Identity.NativeID)
			if len(result.Blockers) != 0 || len(contribution.Bindings) != 7 || len(contribution.Unresolved) != 0 || len(result.ImpactItems) != 7 {
				t.Fatalf("result=%+v contribution=%+v", result, contribution)
			}
			for _, impact := range result.ImpactItems {
				expected := plan.ExpectedDelegatedDelete
				if impact.AssetID == "other" || impact.AssetID == "other-disk" || impact.AssetID == "ip" {
					expected = plan.ExpectedRetainExplicit
				}
				if impact.Expected != expected {
					t.Fatalf("wrong native policy: %+v", impact)
				}
				if impact.AssetID == "ig" && impact.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
					t.Fatal("complementary group absence is not verified")
				}
			}
			blocked, _ := f.plan("ig")
			if len(blocked.Blockers) == 0 {
				t.Fatal("native managed InstanceGroup cannot be retained after MIG deletion")
			}
		})
	}
}

func TestManagedGroupRetentionWaitsForEffectiveStateAndResumes(t *testing.T) {
	for _, mode := range []struct{ regional, manual bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := newManagedGroupFixture(t, mode.regional)
			request := f.request("other", "boot", "data", "ip")
			keys := map[string]bool{}
			f.mutation = func(r *http.Request, body map[string]any) func() {
				method := last(r.URL.Path)
				key := r.URL.Query().Get("requestId")
				if method == "applyUpdatesToInstances" {
					// This native method does not accept a requestId parameter.
					if key != "" {
						t.Fatal("invented native idempotency parameter")
					}
				} else if key == "" || keys[key] {
					t.Fatalf("missing or reused mutation identity: %s", r.URL)
				}
				keys[key] = true
				switch method {
				case "abandonInstances":
					if fmt.Sprint(body["instances"]) != "["+text(f.value("other").Normalized["selfLink"])+"]" {
						t.Fatalf("wrong abandoned VM: %+v", body)
					}
					return func() { f.members = f.members[:1]; f.live("mig")["targetSize"] = 1 }
				case "patchPerInstanceConfigs":
					if len(f.members) != 1 {
						t.Fatal("continued before retained VM left the MIG")
					}
					configs := array(body["perInstanceConfigs"])
					if len(configs) != 1 {
						t.Fatalf("wrong patch payload: %+v", body)
					}
					config := object(configs[0])
					state := object(config["preservedState"])
					if config["name"] != "web" || config["fingerprint"] != f.configs[0]["fingerprint"] || object(state["metadata"])["custom"] != "private-metadata-value" || config["status"] != nil {
						t.Fatalf("patch lost native state or concurrency check: %+v", config)
					}
					return func() {
						config["status"], config["fingerprint"] = "EFFECTIVE", "bmV3ZmluZ2VycHJpbnQ="
						f.configs[0] = config
						if mode.manual {
							config["status"] = "UNAPPLIED"
						} else {
							f.members[0]["preservedStateFromConfig"] = state
						}
					}
				case "applyUpdatesToInstances":
					if !mode.manual || body["minimalAction"] != "REFRESH" || body["mostDisruptiveAllowedAction"] != "REFRESH" || fmt.Sprint(body["instances"]) != "["+text(f.value("vm").Normalized["selfLink"])+"]" {
						t.Fatalf("unsafe configuration application: %+v", body)
					}
					if f.configs[0]["status"] != "UNAPPLIED" {
						t.Fatal("configuration applied without native UNAPPLIED readback")
					}
					return func() {
						f.configs[0]["status"] = "EFFECTIVE"
						f.members[0]["preservedStateFromConfig"] = f.configs[0]["preservedState"]
					}
				case "setDiskAutoDelete":
					if !strings.Contains(r.URL.Path, "/zones/us-central1-a/instances/web/") || r.URL.Query().Get("deviceName") != "boot" || r.URL.Query().Get("autoDelete") != "false" {
						t.Fatalf("wrong VM retention request: %s", r.URL)
					}
					return func() { object(array(f.live("vm")["disks"])[0])["autoDelete"] = false }
				case "setDeletionProtection":
					if r.URL.Query().Get("deletionProtection") != "false" {
						t.Fatal("did not disable protection")
					}
					return func() { f.live("vm")["deletionProtection"] = false }
				case "workers":
					state := object(f.members[0]["preservedStateFromConfig"])
					if f.live("vm")["deletionProtection"] != false || object(array(f.live("vm")["disks"])[0])["autoDelete"] != false || object(object(state["disks"])["data"])["autoDelete"] != "NEVER" || object(object(state["externalIPs"])["nic0"])["autoDelete"] != "NEVER" {
						t.Fatal("MIG deleted before all retained resources were safe")
					}
					delete(f.resources, f.value("mig").Identity.NativeID)
					return func() {
						delete(f.resources, f.value("ig").Identity.NativeID)
						delete(f.resources, f.value("vm").Identity.NativeID)
					}
				default:
					t.Fatalf("unexpected mutation %s", r.URL)
				}
				return nil
			}
			result, err := f.driver().Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			done := false
			for attempt := 0; attempt < 40; attempt++ {
				encoded, _ := json.Marshal(result)
				if err := json.Unmarshal(encoded, &result); err != nil {
					t.Fatal(err)
				}
				wait, err := f.driver().Wait(context.Background(), request, result)
				if err != nil {
					t.Fatalf("attempt %d: %+v %v", attempt, result, err)
				}
				if wait.Data != nil {
					result.Data = wait.Data
				}
				if wait.Done {
					done = true
					break
				}
			}
			expected := "[abandonInstances patchPerInstanceConfigs patchPerInstanceConfigs setDiskAutoDelete setDeletionProtection workers]"
			if mode.manual {
				expected = "[abandonInstances patchPerInstanceConfigs applyUpdatesToInstances patchPerInstanceConfigs applyUpdatesToInstances setDiskAutoDelete setDeletionProtection workers]"
			}
			if !done || fmt.Sprint(f.mutations) != expected {
				t.Fatalf("done=%v mutations=%v polls=%v", done, f.mutations, f.polls)
			}
			for _, id := range []string{"other", "other-disk", "boot", "data", "ip"} {
				if f.live(id) == nil {
					t.Fatalf("retained %s was deleted", id)
				}
			}
		})
	}
}

func TestManagedGroupPreflightRejectsUnreviewedChanges(t *testing.T) {
	for _, mode := range []string{"manager-recreated", "vm-recreated", "disk-recreated", "new-member", "missing-member", "new-disk", "changed-policy", "new-protected-label", "wrong-ip", "cross-project-member", "cross-region-member", "unstable", "config-unapplied", "duplicate-impact", "foreign-impact", "missing-impact", "read-permission"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedGroupFixture(t, true)
			request := f.request()
			switch mode {
			case "manager-recreated":
				f.live("mig")["id"] = "new"
			case "vm-recreated":
				f.live("vm")["id"], f.members[0]["id"] = "new", "new"
			case "disk-recreated":
				f.live("data")["id"] = "new"
			case "new-member":
				f.members = append(f.members, groupCopy(f.members[0]))
			case "missing-member":
				f.members = f.members[:1]
			case "new-disk":
				f.live("vm")["disks"] = append(array(f.live("vm")["disks"]), map[string]any{"source": "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/disks/new", "deviceName": "new", "autoDelete": true})
			case "changed-policy":
				object(object(object(f.members[0]["preservedStateFromPolicy"])["disks"])["data"])["autoDelete"] = "NEVER"
			case "new-protected-label":
				f.live("data")["labels"] = map[string]any{"steward-protected": "true"}
			case "wrong-ip":
				f.live("ip")["address"] = "1.2.3.4"
			case "cross-project-member":
				f.members[0]["instance"] = strings.Replace(text(f.members[0]["instance"]), "sample-project", "foreign", 1)
			case "cross-region-member":
				f.members[0]["instance"] = strings.Replace(text(f.members[0]["instance"]), "us-central1-a", "europe-west1-b", 1)
			case "unstable":
				object(f.live("mig")["status"])["isStable"] = false
			case "config-unapplied":
				f.configs[0]["status"] = "APPLYING"
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "foreign-impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "another"
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "read-permission":
				f.response = func(r *http.Request) (*http.Response, bool) {
					return apiResponse(r, 403, `{"error":{"status":"PERMISSION_DENIED"}}`), true
				}
			}
			_, err := f.driver().Execute(context.Background(), request)
			if err == nil || len(f.mutations) != 0 {
				t.Fatalf("unsafe group deletion: err=%v mutations=%v", err, f.mutations)
			}
		})
	}
}

func TestManagedGroupFailedOperationAndMissingRootDoNotCloseLiveChildren(t *testing.T) {
	f := newManagedGroupFixture(t, true)
	request := f.request("other")
	f.mutation = func(r *http.Request, body map[string]any) func() { return func() {} }
	result, err := f.driver().Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	f.response = func(r *http.Request) (*http.Response, bool) {
		if strings.Contains(r.URL.Path, "/operations/") {
			return apiResponse(r, 200, `{"status":"DONE","error":{"errors":[{"code":"FAILED"}]}}`), true
		}
		return nil, false
	}
	if _, err := f.driver().Wait(context.Background(), request, result); err == nil {
		t.Fatal("failed preparation accepted")
	}
	if len(f.mutations) != 1 {
		t.Fatal("continued after failed preparation")
	}
	f.response = nil
	delete(f.resources, f.value("mig").Identity.NativeID)
	check, err := f.driver().Preflight(context.Background(), request)
	if err != nil || !check.Allowed || check.Absent {
		t.Fatalf("premature completion %+v %v", check, err)
	}
	result, err = f.driver().Execute(context.Background(), request)
	if err != nil || len(f.mutations) != 1 {
		t.Fatalf("repeated deletion %v", err)
	}
	wait, err := f.driver().Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("live complementary group disappeared %+v %v", wait, err)
	}
	delete(f.resources, f.value("ig").Identity.NativeID)
	wait, err = f.driver().Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatal("group absence hid surviving managed resources", wait, err)
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.Delete {
			delete(f.resources, impact.Asset.Identity.NativeID)
		}
	}
	wait, err = f.driver().Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("final absence not accepted %+v %v", wait, err)
	}
}

func TestManagedGroupDiscoveryMissingMembersAndDefaultStatefulRetention(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	object(object(object(f.members[0]["preservedStateFromPolicy"])["disks"])["data"])["autoDelete"] = "NEVER"
	result, _ := f.plan()
	for _, impact := range result.ImpactItems {
		if impact.AssetID == "data" && impact.Expected != plan.ExpectedProviderDefaultRetain {
			t.Fatalf("stateful default lost: %+v", impact)
		}
	}
	f.assets = f.assets[:len(f.assets)-1]
	contribution, err := (&computeGroups{client: f.driver().client}).Contribute(context.Background(), "scope", f.assets)
	if err != nil || len(contribution.Unresolved) != 1 {
		t.Fatalf("missing owned resource hidden: %+v %v", contribution, err)
	}
}

func TestManagedVMDirectDeleteRequiresLeavingItsGroup(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	vm := f.value("other")
	request := contracts.ActionRequest{Asset: vm, Action: "delete", LifecycleImpacts: []contracts.ActionImpact{{Asset: f.value("other-disk"), ControllerID: vm.ID, Delete: true}}}
	driver, _ := f.driver().computeAction(vm.Identity.NativeID, instanceType)
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || check.Allowed || check.Reason != "instance_managed_by_group" {
		t.Fatalf("managed VM direct deletion allowed: %+v %v", check, err)
	}
	f.members = f.members[:1]
	check, err = driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("stale created-by blocked abandoned VM: %+v %v", check, err)
	}
}

func TestManagedGroupDependencyNotFoundIsNotControllerAbsence(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	request := f.request()
	f.response = func(r *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(r.URL.Path, "/listManagedInstances") {
			return apiResponse(r, 404, `{}`), true
		}
		return nil, false
	}
	_, err := f.driver().Execute(context.Background(), request)
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || isNotFound(err) {
		t.Fatalf("dependency absence closes MIG: %v", err)
	}
}

func TestManagedGroupAutoscalerIsAnExplicitPrerequisite(t *testing.T) {
	f := newManagedGroupFixture(t, true)
	// Compute does not support autoscaling a stateful MIG.
	delete(f.members[0], "preservedStateFromPolicy")
	f.configs = nil
	auto := diskAsset("autoscaler", autoscalerType, "regions/us-central1/autoscalers/workers")
	auto.Normalized["id"] = "4001"
	auto.Normalized["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/autoscalers/workers"
	auto.Normalized["target"] = f.value("mig").Normalized["selfLink"]
	f.assets = append(f.assets, auto)
	f.resources[auto.Identity.NativeID] = groupCopy(auto.Normalized)
	object(f.live("mig")["status"])["autoscaler"] = auto.Normalized["selfLink"]
	result, _ := f.plan()
	if len(result.Blockers) != 0 {
		t.Fatalf("blocked autoscaler plan: %+v", result.Blockers)
	}
	var prerequisite plan.StepID
	for _, step := range result.Steps {
		if step.AssetID == auto.ID {
			prerequisite = step.ID
		}
	}
	if prerequisite == "" {
		t.Fatal("autoscaler has no explicit deletion step")
	}
	for _, step := range result.Steps {
		if step.AssetID == "mig" {
			found := false
			for _, id := range step.DependsOn {
				found = found || id == prerequisite
			}
			if !found {
				t.Fatalf("MIG does not wait for autoscaler: %+v", step)
			}
		}
	}
	request := f.request()
	check, err := f.driver().Preflight(context.Background(), request)
	if err != nil || check.Allowed || check.Reason != "managed_group_autoscaler_requires_cleanup" {
		t.Fatalf("live autoscaler ignored: %+v %v", check, err)
	}
	delete(f.resources, auto.Identity.NativeID)
	check, err = f.driver().Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("deleted autoscaler still blocked MIG: %+v %v", check, err)
	}
}

func TestManagedGroupsRetainSharedReadOnlyDisks(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	boot := object(array(f.live("vm")["disks"])[0])
	boot["autoDelete"], boot["mode"] = false, "READ_ONLY"
	f.live("other")["disks"] = []any{groupCopy(boot)}
	result, contribution := f.plan()
	if len(result.Blockers) != 0 {
		t.Fatalf("shared disk blocked plan: %+v", result.Blockers)
	}
	count := 0
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == "boot" {
			count++
			if binding.Ownership != graph.OwnershipShared || binding.CleanupPolicy != graph.CleanupRetain {
				t.Fatalf("shared disk treated as exclusive: %+v", binding)
			}
		}
	}
	if count != 2 {
		t.Fatalf("shared attachments missing: %+v", contribution.Bindings)
	}
	for _, impact := range result.ImpactItems {
		if impact.AssetID == "boot" && impact.Expected != plan.ExpectedRetainShared {
			t.Fatalf("shared disk scheduled for deletion: %+v", impact)
		}
	}
	check, err := f.driver().Preflight(context.Background(), f.request())
	if err != nil || !check.Allowed {
		t.Fatalf("shared retained disk failed preflight: %+v %v", check, err)
	}
}

func TestInstanceGroupDirectDeletionRespectsItsNativeController(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	ig := f.value("ig")
	driver, err := f.driver().computeAction(ig.Identity.NativeID, instanceGroupType)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: ig, Action: "delete"}
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || check.Allowed || check.Reason != "instance_group_managed_by_controller" {
		t.Fatalf("managed InstanceGroup allowed: %+v %v", check, err)
	}
	f.response = func(r *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(r.URL.Path, "/aggregated/instanceGroupManagers") {
			return apiResponse(r, 200, `{}`), true
		}
		return nil, false
	}
	check, err = driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("unmanaged InstanceGroup blocked: %+v %v", check, err)
	}
	regional := newManagedGroupFixture(t, true)
	if _, err := regional.driver().computeAction(regional.value("ig").Identity.NativeID, instanceGroupType); err == nil {
		t.Fatal("invented regional InstanceGroup DELETE method")
	}
}

func TestGroupReferencesPreserveNetworkAndBackendDependencies(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	refs := references(c, map[string]any{"instanceGroup": "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/instanceGroups/workers", "instanceTemplate": "https://compute.googleapis.com/compute/v1/projects/sample-project/global/instanceTemplates/workers", "backends": []any{map[string]any{"group": "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/instanceGroups/backend"}}})
	if len(refs[instanceGroupType]) != 2 || len(refs["compute.googleapis.com/InstanceTemplate"]) != 1 {
		t.Fatalf("network/backend dependency lost: %+v", refs)
	}
}

func TestManagedGroupPendingConfigurationCannotBeReplacedBeforeApply(t *testing.T) {
	f := newManagedGroupFixture(t, false)
	request := f.request("data")
	var submitted map[string]any
	f.mutation = func(r *http.Request, body map[string]any) func() {
		if last(r.URL.Path) != "patchPerInstanceConfigs" {
			t.Fatalf("unexpected mutation %s", r.URL)
		}
		submitted = object(array(body["perInstanceConfigs"])[0])
		return func() {}
	}
	result, err := f.driver().Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	config := groupCopy(submitted)
	config["status"], config["fingerprint"] = "UNAPPLIED", "bmV3"
	object(object(config["preservedState"])["metadata"])["custom"] = "changed-by-another-writer"
	f.configs[0] = config
	f.response = func(r *http.Request) (*http.Response, bool) {
		if strings.Contains(r.URL.Path, "/operations/") {
			return apiResponse(r, 200, `{"status":"DONE"}`), true
		}
		return nil, false
	}
	_, err = f.driver().Wait(context.Background(), request, result)
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Code != "pending_instance_configuration_changed" || len(f.mutations) != 1 {
		t.Fatalf("unreviewed configuration applied: err=%v mutations=%v", err, f.mutations)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-metadata-value") {
		t.Fatal("secret persisted in execution state")
	}
}

func TestManagedGroupListRejectsPaginationCyclesAndPartialResults(t *testing.T) {
	for _, mode := range []string{"cycle", "partial", "bad-shape", "wrong-target-count", "pending-template"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedGroupFixture(t, false)
			if mode == "wrong-target-count" {
				f.live("mig")["targetSize"] = 3
			}
			if mode == "pending-template" {
				object(f.live("mig")["status"])["versionTarget"] = map[string]any{"isReached": false}
			}
			f.response = func(r *http.Request) (*http.Response, bool) {
				if !strings.HasSuffix(r.URL.Path, "/listManagedInstances") {
					return nil, false
				}
				switch mode {
				case "cycle":
					return apiResponse(r, 200, `{"nextPageToken":"repeat"}`), true
				case "partial":
					return apiResponse(r, 200, `{"warning":{"code":"UNREACHABLE","message":"incomplete"}}`), true
				case "bad-shape":
					return apiResponse(r, 200, `{"managedInstances":{}}`), true
				}
				return nil, false
			}
			_, err := (&computeGroups{client: f.driver().client}).Contribute(context.Background(), "scope", f.assets)
			if err == nil {
				t.Fatal("incomplete native group inventory accepted")
			}
		})
	}
}
