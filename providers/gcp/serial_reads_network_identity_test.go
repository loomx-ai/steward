package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const nidRouter = "projects/sample-project/regions/us-central1/routers/r1"

// nidLateFirst answers the request keyed first only after the one keyed second
// failed fast with a 400; a serial walk reports first's outcome.
func nidLateFirst(key func(*http.Request) string, first, second string, answer, next roundTripFunc) roundTripFunc {
	failed := make(chan struct{})
	var once sync.Once
	return func(req *http.Request) (*http.Response, error) {
		switch key(req) {
		case second:
			defer once.Do(func() { close(failed) })
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case first:
			after(failed)
			return answer(req)
		}
		return next(req)
	}
}

func nidReply(value any) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		encoded, _ := json.Marshal(value)
		return apiResponse(req, 200, string(encoded)), nil
	}
}

func nidLast(req *http.Request) string   { return last(req.URL.Path) }
func nidPolicy(req *http.Request) string { return req.URL.Query().Get("policy") }
func nidRoutePolicyGet(req *http.Request) bool {
	return strings.HasSuffix(req.URL.Path, "/getRoutePolicy")
}

func nidRoutePolicy(name string) roundTripFunc {
	return nidReply(map[string]any{"resource": routePolicyFixture(name)})
}

// nidRouterAction acts on a component of nidRouter.
func nidRouterAction(t *testing.T, nativeType, suffix string, transport roundTripFunc) *action {
	a := protocolAction(t, routerType, nidRouter, transport)
	a.kind, _ = findType(nativeType)
	a.identity = asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: nativeType, NativeID: "//compute.googleapis.com/" + nidRouter + suffix}
	return a
}

func TestRouterPrerequisiteReadsConcurrentlyInOrder(t *testing.T) {
	var request contracts.ActionRequest
	for i := range groupReadConcurrency + 4 {
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Delete: true, Asset: asset.Asset{Identity: asset.Identity{NativeType: routePolicyType, NativeID: fmt.Sprintf("//compute.googleapis.com/%s/routePolicies/p-%02d", nidRouter, i)}}})
	}
	probe := newReadProbe(groupReadConcurrency, nidRoutePolicyGet)
	probe.start()
	a := nidRouterAction(t, routerType, "", probe.wrap(notFound))
	if err := a.routerPrerequisitesAbsent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	probe.check(t, len(request.PrerequisiteDeletions), groupReadConcurrency)
	a = nidRouterAction(t, routerType, "", nidLateFirst(nidPolicy, "p-00", "p-01", nidRoutePolicy("p-00"), notFound))
	if err := a.routerPrerequisitesAbsent(t.Context(), request); deniedCode(err) != "router_prerequisite_still_exists" {
		t.Fatal(err)
	}
}

func TestRoutePolicyRemovedSiblingReadsConcurrentlyInOrder(t *testing.T) {
	names := []any{"self"}
	for i := range groupReadConcurrency + 4 {
		names = append(names, fmt.Sprintf("p-%02d", i))
	}
	request := contracts.ActionRequest{Asset: asset.Asset{Normalized: map[string]any{routePolicyPeers: []any{map[string]any{"name": "peer", "importPolicies": names}}}}}
	parent := map[string]any{"name": "r1", "id": "4242", "selfLink": "https://www.googleapis.com/compute/v1/" + nidRouter, "bgpPeers": []any{map[string]any{"name": "peer", "importPolicies": []any{}}}}
	router := func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/routers/r1") {
				return nidReply(parent)(req)
			}
			return next(req)
		}
	}
	probe := newReadProbe(groupReadConcurrency, nidRoutePolicyGet)
	probe.start()
	a := nidRouterAction(t, routePolicyType, "/routePolicies/self", router(probe.wrap(notFound)))
	request.Asset.Normalized[a.routerComponentIncarnationKey()] = "4242"
	if _, _, err := a.routePolicyBGPMerge(t.Context(), request, parent); err != nil {
		t.Fatal(err)
	}
	probe.check(t, len(names)-1, groupReadConcurrency)
	a = nidRouterAction(t, routePolicyType, "/routePolicies/self", router(nidLateFirst(nidPolicy, "p-00", "p-01", nidRoutePolicy("p-00"), notFound)))
	if _, _, err := a.routePolicyBGPMerge(t.Context(), request, parent); deniedCode(err) != "route_policy_removed_sibling_still_exists" {
		t.Fatal(err)
	}
}

func TestNamedSetPolicyReadsConcurrentlyInOrder(t *testing.T) {
	var listed []any
	for i := range groupReadConcurrency + 4 {
		listed = append(listed, map[string]any{"name": fmt.Sprintf("p-%02d", i)})
	}
	list := func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
				return nidReply(map[string]any{"result": listed})(req)
			}
			return next(req)
		}
	}
	unrelated := func(req *http.Request) (*http.Response, error) { return nidRoutePolicy(nidPolicy(req))(req) }
	action := func(transport roundTripFunc) *action {
		a := nidRouterAction(t, namedSetType, "/namedSets/set-a", transport)
		a.deleteParameters = map[string]any{"project": "sample-project", "region": "us-central1", "router": "r1", "namedSet": "set-a"}
		return a
	}
	probe := newReadProbe(groupReadConcurrency, nidRoutePolicyGet)
	probe.start()
	if err := action(list(probe.wrap(unrelated))).namedSetUnreferenced(t.Context()); err != nil {
		t.Fatal(err)
	}
	probe.check(t, len(listed), groupReadConcurrency)
	referencing := routePolicyFixture("p-00")
	object(array(referencing["terms"])[0])["match"] = map[string]any{"expression": "destination.inAnyRange(prefixSets('set-a'))"}
	a := action(list(nidLateFirst(nidPolicy, "p-00", "p-01", nidReply(map[string]any{"resource": referencing}), unrelated)))
	if err := a.namedSetUnreferenced(t.Context()); deniedCode(err) != "named_set_referenced_by_policy" {
		t.Fatal(err)
	}
}

func TestMonitoringPrerequisiteReadsConcurrentlyInOrder(t *testing.T) {
	var request contracts.ActionRequest
	for i := range groupReadConcurrency + 4 {
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Delete: true, Asset: asset.Asset{Identity: asset.Identity{NativeType: alertPolicyType, NativeID: fmt.Sprintf("//monitoring.googleapis.com/projects/sample-project/alertPolicies/100%02d", i)}}})
	}
	const check = "projects/sample-project/uptimeCheckConfigs/check"
	probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/alertPolicies/100") })
	probe.start()
	a := protocolAction(t, uptimeType, check, probe.wrap(notFound))
	// Later consumer lists are not modeled; only the prerequisite reads count.
	if err := a.monitoringIncoming(t.Context(), request); deniedCode(err) == "monitoring_prerequisite_still_exists" {
		t.Fatal(err)
	}
	probe.check(t, len(request.PrerequisiteDeletions), groupReadConcurrency)
	fixture, _ := json.Marshal(alertPolicyFixture())
	survivor := func(req *http.Request) (*http.Response, error) {
		return apiResponse(req, 200, strings.ReplaceAll(string(fixture), alertPolicyName, "projects/sample-project/alertPolicies/10000")), nil
	}
	a = protocolAction(t, uptimeType, check, nidLateFirst(nidLast, "10000", "10001", survivor, notFound))
	if err := a.monitoringIncoming(t.Context(), request); deniedCode(err) != "monitoring_prerequisite_still_exists" {
		t.Fatal(err)
	}
}

func TestOSLoginKnownKeyReadsConcurrentlyInOrder(t *testing.T) {
	profile := func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == osLoginUserPath+"/loginProfile" {
				return nidReply(map[string]any{"name": "steward@sample-project.iam.gserviceaccount.com", "sshPublicKeys": map[string]any{}})(req)
			}
			return next(req)
		}
	}
	isKey := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/sshPublicKeys/fp-") }
	list := func(transport roundTripFunc) (contracts.InventoryBatch, error) {
		r := protocolRuntime(t, profile(transport))
		request := osLoginRequest(r)
		for i := range groupReadConcurrency + 4 {
			request.KnownNativeIDs = append(request.KnownNativeIDs, fmt.Sprintf("%susers/steward@sample-project.iam.gserviceaccount.com/sshPublicKeys/fp-%02d", osLoginPrefix, i))
		}
		return r.List(t.Context(), request)
	}
	probe := newReadProbe(groupReadConcurrency, isKey)
	probe.start()
	batch, err := list(probe.wrap(notFound))
	if err != nil || len(batch.AbsentNativeIDs) != groupReadConcurrency+4 || !strings.HasSuffix(batch.AbsentNativeIDs[0], "/fp-00") {
		t.Fatal(batch, err)
	}
	probe.check(t, groupReadConcurrency+4, groupReadConcurrency)
	// fp-00 answers an invalid key last; a serial walk reports it first.
	if _, err := list(nidLateFirst(nidLast, "fp-00", "fp-01", nidReply(map[string]any{}), notFound)); deniedCode(err) != "oslogin_key_response_invalid" {
		t.Fatal(err)
	}
}

func TestLoggingSinkReadsConcurrentlyInOrder(t *testing.T) {
	const parent = "projects/sample-project"
	sink := func(name string) map[string]any {
		return loggingSinkFixture(parent, name, "logging.googleapis.com/projects/foreign-project")
	}
	var sinks []any
	for i := range groupReadConcurrency + 4 {
		sinks = append(sinks, sink(fmt.Sprintf("s-%02d", i)))
	}
	isSink := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/sinks/s-") }
	live := func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/"+parent+"/sinks") {
			return nidReply(map[string]any{"sinks": sinks})(req)
		}
		if isSink(req) {
			return nidReply(sink(nidLast(req)))(req)
		}
		return notFound(req)
	}
	routing := func(transport roundTripFunc) (loggingRouting, error) {
		c, err := protocolRuntime(t, transport).resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		return c.loggingRouting(t.Context())
	}
	probe := newReadProbe(groupReadConcurrency, isSink)
	probe.start()
	result, err := routing(probe.wrap(live))
	if err != nil || len(result.Sinks) != len(sinks) || len(result.Projects["foreign-project"]) != len(sinks) || result.Projects["foreign-project"][0]["name"] != "s-00" {
		t.Fatal(result, err)
	}
	probe.check(t, len(sinks), groupReadConcurrency)
	changed := sink("s-00")
	changed["filter"] = "severity>=ERROR"
	if _, err := routing(nidLateFirst(nidLast, "s-00", "s-01", nidReply(changed), live)); deniedCode(err) != "logging_sink_changed" {
		t.Fatal(err)
	}
}

// nidGroupManyDisks attaches extra auto-delete disks to the group's deleted VM;
// each is re-read once before its controller can delete it.
func nidGroupManyDisks(t *testing.T, count int) *managedGroupFixture {
	f := newManagedGroupFixture(t, false)
	vm := f.value("vm")
	for i := range count {
		path := fmt.Sprintf("zones/us-central1-a/disks/extra-%02d", i)
		value := diskAsset(fmt.Sprintf("extra-%02d", i), "compute.googleapis.com/Disk", path)
		value.Normalized["id"], value.Normalized["name"] = fmt.Sprintf("50%02d", i), last(path)
		value.Normalized["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/" + path
		f.assets = append(f.assets, value)
		f.resources[value.Identity.NativeID] = groupCopy(value.Normalized)
		disk := map[string]any{"source": value.Normalized["selfLink"], "deviceName": last(path), "autoDelete": true}
		vm.Normalized["disks"] = append(array(vm.Normalized["disks"]), disk)
		f.live("vm")["disks"] = append(array(f.live("vm")["disks"]), groupCopy(disk))
	}
	return f
}

func TestManagedGroupResourceChecksReadConcurrentlyInOrder(t *testing.T) {
	isExtra := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/disks/extra-") }
	f := nidGroupManyDisks(t, groupReadConcurrency+4)
	request := f.request()
	probe := newReadProbe(groupReadConcurrency, isExtra)
	a := protocolAction(t, managerType, "projects/sample-project/"+f.groupPath, probe.wrap(f.roundTrip))
	probe.start()
	if _, reason, err := a.plannedGroup(t.Context(), request, f.live("mig")); reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	probe.check(t, groupReadConcurrency+4, groupReadConcurrency)
	// extra-00 was recreated but answers last; extra-01 fails first. A serial
	// walk reports the recreated disk, so ordered evaluation must too.
	recreated := groupCopy(f.resources["//compute.googleapis.com/projects/sample-project/zones/us-central1-a/disks/extra-00"])
	recreated["id"] = "new"
	a = protocolAction(t, managerType, "projects/sample-project/"+f.groupPath, nidLateFirst(nidLast, "extra-00", "extra-01", nidReply(recreated), f.roundTrip))
	if _, _, err := a.plannedGroup(t.Context(), request, f.live("mig")); deniedCode(err) != "managed_resource_identity_changed" {
		t.Fatal(err)
	}
}

func nidFirewallRuntime(t *testing.T, root string, transport roundTripFunc) *Runtime {
	r := protocolRuntime(t, transport)
	credentials := r.credentials
	r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		value, err := credentials.Resolve(ctx, id)
		value.Values["firewall_policy_parent"] = root
		return value, err
	})
	return r
}

func nidFirewallClient(t *testing.T, root string, transport roundTripFunc) *client {
	c, err := nidFirewallRuntime(t, root, transport).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func nidFolder(name, parent string) map[string]any {
	row := map[string]any{"name": name, "state": "ACTIVE", "createTime": "2026-01-01T00:00:00Z"}
	if parent != "" {
		row["parent"] = parent
	}
	return row
}

func TestFirewallFolderAndPolicyListReadsConcurrentlyInOrder(t *testing.T) {
	const org = "organizations/123"
	var folders []any
	for i := range groupReadConcurrency + 4 {
		folders = append(folders, nidFolder(fmt.Sprintf("folders/500%02d", i), org))
	}
	tree := func(req *http.Request) (*http.Response, error) {
		name := strings.TrimPrefix(req.URL.Path, "/v3/")
		switch {
		case name == org:
			return nidReply(nidFolder(org, ""))(req)
		case name == "folders" && req.URL.Query().Get("parent") == org:
			return nidReply(map[string]any{"folders": folders})(req)
		case name == "folders":
			return nidReply(map[string]any{})(req)
		case strings.HasPrefix(name, "folders/"):
			return nidReply(nidFolder(name, org))(req)
		case strings.HasSuffix(req.URL.Path, "/locations/global/firewallPolicies"):
			return nidReply(map[string]any{})(req)
		}
		return notFound(req)
	}
	isFolder := func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/v3/folders/500") }
	probe := newReadProbe(groupReadConcurrency, isFolder)
	probe.start()
	containers, err := nidFirewallClient(t, org, probe.wrap(tree)).firewallContainers(t.Context())
	if err != nil || len(containers) != len(folders)+1 {
		t.Fatal(containers, err)
	}
	probe.check(t, len(folders), groupReadConcurrency)
	moved := nidFolder("folders/50000", org)
	moved["createTime"] = "2026-02-01T00:00:00Z"
	c := nidFirewallClient(t, org, nidLateFirst(nidLast, "50000", "50001", nidReply(moved), tree))
	if _, err := c.firewallContainers(t.Context()); deniedCode(err) != "firewall_folder_changed" {
		t.Fatal(err)
	}

	isList := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/locations/global/firewallPolicies") }
	probe = newReadProbe(groupReadConcurrency, isList)
	probe.start()
	if rows, err := nidFirewallClient(t, org, probe.wrap(tree)).firewallPolicies(t.Context(), firewallPolicyType, containers); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	probe.check(t, len(containers), groupReadConcurrency)
	// The first folder lists a foreign policy but answers last; the second fails first.
	parentID := func(r *http.Request) string { return r.URL.Query().Get("parentId") }
	foreign := nidReply(map[string]any{"items": []any{map[string]any{"name": "1", "parent": "folders/999"}}})
	c = nidFirewallClient(t, org, nidLateFirst(parentID, "folders/50000", "folders/50001", foreign, tree))
	if _, err := c.firewallPolicies(t.Context(), firewallPolicyType, containers); deniedCode(err) != "firewall_list_parent_changed" {
		t.Fatal(err)
	}
}

// nidFirewallManyAssociations attaches the global network policy to count
// networks instead of two.
func nidFirewallManyAssociations(count int) *firewallScenario {
	s := newFirewallScenario()
	policy := s.policies[firewallTestGlobal]
	rows := []any{}
	for i := range count {
		name := fmt.Sprintf("net-%02d", i)
		path := "projects/sample-project/global/networks/" + name
		s.networks[path] = map[string]any{"id": fmt.Sprint(2101 + i), "name": name, "selfLink": "https://www.googleapis.com/compute/v1/" + path, "creationTimestamp": "2026-01-01T00:00:00Z"}
		rows = append(rows, map[string]any{"name": fmt.Sprintf("assoc-%02d", i), "attachmentTarget": s.networks[path]["selfLink"], "firewallPolicyId": policy["id"], "shortName": last(firewallTestGlobal)})
	}
	policy["associations"] = rows
	return s
}

func isGetAssociation(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/getAssociation") }
func associationName(r *http.Request) string {
	if !isGetAssociation(r) {
		return ""
	}
	return r.URL.Query().Get("name")
}

func TestFirewallSnapshotReadsAssociationsConcurrentlyInOrder(t *testing.T) {
	const count = groupReadConcurrency + 4
	s := nidFirewallManyAssociations(count)
	snapshot := func(transport roundTripFunc) (map[string]map[string]any, error) {
		c := nidFirewallClient(t, s.root, transport)
		id := "//compute.googleapis.com/" + firewallTestGlobal
		policy, err := c.firewallReadPolicy(t.Context(), networkFirewallPolicyType, id)
		if err != nil {
			t.Fatal(err)
		}
		return c.firewallSnapshot(t.Context(), networkFirewallPolicyType, id, policy)
	}
	probe := newReadProbe(groupReadConcurrency, isGetAssociation)
	probe.start()
	if rows, err := snapshot(probe.wrap(s.transport(t))); err != nil || len(rows) != count {
		t.Fatal(rows, err)
	}
	probe.check(t, count, groupReadConcurrency)
	moved := groupCopy(object(array(s.policies[firewallTestGlobal]["associations"])[0]))
	moved["attachmentTarget"] = "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/net-01"
	if _, err := snapshot(nidLateFirst(associationName, "assoc-00", "assoc-01", nidReply(moved), s.transport(t))); deniedCode(err) != "firewall_association_list_changed" {
		t.Fatal(err)
	}
}

func TestFirewallPolicyReadbackReadsAssociationsConcurrentlyInOrder(t *testing.T) {
	const count = groupReadConcurrency + 4
	s := nidFirewallManyAssociations(count)
	r := nidFirewallRuntime(t, s.root, s.transport(t))
	values := s.inventory(t, r)
	contributor, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	value := batchAsset(values, firewallTestGlobal)
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{value.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings})
	if err != nil || len(result.Blockers) != 0 {
		t.Fatal(result, err)
	}
	request := dataformRequest(t, result, values, value)
	if len(request.PrerequisiteDeletions) != count {
		t.Fatal(request.PrerequisiteDeletions)
	}
	slices.SortFunc(request.PrerequisiteDeletions, func(a, b contracts.ActionImpact) int {
		return strings.Compare(a.Asset.Identity.NativeID, b.Asset.Identity.NativeID)
	})
	// Every association is gone; the policy itself remains.
	s.policies[firewallTestGlobal]["associations"] = []any{}
	readback := func(transport roundTripFunc) (contracts.ReadbackResult, error) {
		driver, err := nidFirewallRuntime(t, s.root, transport).ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		return driver.(*action).firewallReadback(t.Context(), request)
	}
	probe := newReadProbe(groupReadConcurrency, isGetAssociation)
	probe.start()
	if read, err := readback(probe.wrap(s.transport(t))); err != nil || !read.Exists {
		t.Fatal(read, err)
	}
	// Two fenced passes each read every association once.
	probe.check(t, 2*count, groupReadConcurrency)
	stale := nidReply(map[string]any{"name": "assoc-00"})
	if _, err := readback(nidLateFirst(associationName, "assoc-00", "assoc-01", stale, s.transport(t))); deniedCode(err) != "firewall_association_visibility_changed" {
		t.Fatal(err)
	}
}

func TestFirewallInventoryReadsPoliciesConcurrentlyInOrder(t *testing.T) {
	const count = groupReadConcurrency + 4
	s := newFirewallScenario()
	base := s.policies[firewallTestHierarchy]
	for i := range count {
		id := fmt.Sprintf("30%02d", i)
		path := "locations/global/firewallPolicies/" + id
		row := groupCopy(base)
		row["id"], row["name"], row["selfLink"], row["shortName"], row["associations"] = id, id, "https://www.googleapis.com/compute/v1/"+path, "policy-"+id, []any{}
		s.policies[path] = row
	}
	isPolicy := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/locations/global/firewallPolicies/30")
	}
	list := func(transport roundTripFunc) (contracts.InventoryBatch, error) {
		r := nidFirewallRuntime(t, s.root, transport)
		request := productRequest(r, firewallPolicyType, "project")
		request.Source = firewallInventorySource
		return r.List(t.Context(), request)
	}
	probe := newReadProbe(groupReadConcurrency, isPolicy)
	probe.start()
	if batch, err := list(probe.wrap(s.transport(t))); err != nil || len(batch.Items) != count+1 {
		t.Fatal(batch, err)
	}
	// Each policy is read, then re-read after its (empty) association snapshot.
	probe.check(t, 2*count, groupReadConcurrency)
	changed := groupCopy(s.policies["locations/global/firewallPolicies/3000"])
	changed["description"] = "changed after listing"
	if _, err := list(nidLateFirst(nidLast, "3000", "3001", nidReply(changed), s.transport(t))); deniedCode(err) != "firewall_policy_list_detail_changed" {
		t.Fatal(err)
	}
}
