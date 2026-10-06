package azure

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func sweep5Denied(code string) *http.Response {
	return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil)
}

// sweep5FailTwo fails an earlier and a later path; the earlier must win.
func sweep5FailTwo(s *dnsScenario, first, later string) {
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case first:
			return sweep5Denied("FirstDenied"), true
		case later:
			return sweep5Denied("LaterDenied"), true
		}
		return nil, false
	}
}

func sweep5Client(t *testing.T, s *dnsScenario) *client {
	t.Helper()
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sweep5CheckPeak(t *testing.T, peak *peakTracker) {
	t.Helper()
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("reads were not concurrent within the bound", peak.peak)
	}
}

func sweep5Hybrid(kind, id string) map[string]any {
	return map[string]any{"id": id, "type": kind, "name": last(id), "location": "eastus", "properties": map[string]any{}}
}

func TestSweep5HybridMachineChildrenConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	machine := strings.ToLower(resourceID(hybridMachineType, "m"))
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/extensions/e%02d", machine, i)
		raw := sweep5Hybrid(hybridExtensionType, id)
		s.add(raw, hybridComputeVersion)
		s.lists[machine+"/extensions"] = append(s.lists[machine+"/extensions"], raw)
		ids = append(ids, id)
	}
	s.lists[machine+"/runcommands"], s.lists[machine+"/licenseprofiles"] = []any{}, []any{}
	c := sweep5Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/extensions/") }, 5*time.Millisecond)
	children, err := c.hybridComputeMachineChildren(t.Context(), machine, map[string]any{}, true)
	if err != nil || len(children) != len(ids) {
		t.Fatal("children changed", len(children), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 || children[id] == nil {
			t.Fatal("child not read exactly once", id, calls["GET "+id])
		}
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, ids[3], ids[9])
	for range 5 {
		if _, err := c.hybridComputeMachineChildren(t.Context(), machine, map[string]any{}, true); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep5HybridLicenseAssignmentsConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	license := strings.ToLower(resourceID(hybridLicenseType, "license"))
	machines := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + hybridMachineType)
	var ids, profiles []string
	for i := range 12 {
		machine := strings.ToLower(resourceID(hybridMachineType, fmt.Sprintf("m%02d", i)))
		raw := sweep5Hybrid(hybridMachineType, machine)
		s.add(raw, hybridComputeVersion)
		s.lists[machines] = append(s.lists[machines], raw)
		profile := sweep5Hybrid(hybridProfileType, machine+"/licenseprofiles/default")
		if i%2 == 0 {
			profile["properties"] = map[string]any{"esuProfile": map[string]any{"assignedLicense": license}}
		}
		s.add(profile, hybridComputeVersion)
		s.lists[machine+"/licenseprofiles"] = []any{profile}
		ids, profiles = append(ids, machine), append(profiles, machine+"/licenseprofiles/default")
	}
	c := sweep5Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { _, ok := s.records[path]; return ok }, 5*time.Millisecond)
	result, err := c.hybridComputeLicenseAssignments(t.Context(), license, map[string]any{}, true)
	if err != nil || len(result) != 6 {
		t.Fatal("assignments changed", len(result), err)
	}
	for i := range ids {
		if calls["GET "+ids[i]] != 1 || calls["GET "+ids[i]+"/licenseprofiles"] != 1 || calls["GET "+profiles[i]] != 1 {
			t.Fatal("machine or profile not read exactly once", ids[i], calls["GET "+ids[i]], calls["GET "+profiles[i]])
		}
	}
	sweep5CheckPeak(t, peak)
	for _, failing := range [][]string{ids, profiles} {
		sweep5FailTwo(s, failing[2], failing[9])
		for range 5 {
			if _, err := c.hybridComputeLicenseAssignments(t.Context(), license, map[string]any{}, true); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
				t.Fatal("first failure in order lost", err)
			}
		}
	}
	// A malformed machine row stops the walk; rows at or after it are not read.
	s.handle = nil
	object(s.lists[machines][4])["id"] = "not-an-id"
	clear(calls)
	if _, err := c.hybridComputeLicenseAssignments(t.Context(), license, map[string]any{}, true); err == nil || !strings.Contains(err.Error(), "invalid_hybrid_compute_license_machine_index") {
		t.Fatal("invalid machine row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 4]; calls["GET "+id] != want {
			t.Fatal("machine reads around the invalid row", id, calls["GET "+id])
		}
	}
}

func TestSweep5ManagementGroupsConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	collection := strings.ToLower(managementGroupCollection)
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/g%02d", managementGroupCollection, i)
		raw := map[string]any{"id": id, "type": managementGroupType, "name": last(id), "properties": map[string]any{"tenantId": testTenant, "displayName": last(id)}}
		s.add(raw, "")
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, strings.ToLower(id))
	}
	r := s.runtime(t)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	kind := r.resourceKind(managementGroupType)
	known := strings.ToLower(managementGroupCollection + "/gone")
	s.gone[known] = true
	request := contracts.InventoryRequest{ConnectionID: "connection", Source: managementGroupSource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}, KnownNativeIDs: []string{known}}
	calls, peak := countCalls(s, func(path string) bool { return strings.HasPrefix(path, collection+"/") }, 5*time.Millisecond)
	batch, err := r.listManagementGroups(t.Context(), c, request)
	if err != nil || len(batch.Items) != len(ids) || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != known {
		t.Fatal("management groups changed", len(batch.Items), batch.AbsentNativeIDs, err)
	}
	for i, id := range ids {
		if calls["GET "+id] != 1 || batch.Items[i].NativeID != id {
			t.Fatal("group not read once in order", id, calls["GET "+id])
		}
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, ids[3], ids[9])
	for range 5 {
		if _, err := r.listManagementGroups(t.Context(), c, request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep5MonitorRuleIndexConcurrentInOrder(t *testing.T) {
	f := newMonitorInventoryFixture(t, monitorActionGroupType)
	var template map[string]any
	for _, raw := range f.objects {
		template = raw
	}
	s := newDNSScenario()
	collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(monitorActionGroupType)
	var ids []string
	for i := range 12 {
		raw := batchClone(template)
		id := strings.TrimSuffix(text(template["id"]), last(text(template["id"]))) + fmt.Sprintf("group%02d", i)
		raw["id"], raw["name"] = id, last(id)
		s.add(raw, f.version)
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, strings.ToLower(id))
	}
	c := sweep5Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { _, ok := s.records[path]; return ok }, 5*time.Millisecond)
	values, _, err := c.monitorRuleIndex(t.Context(), monitorActionGroupType)
	if err != nil || len(values) != len(ids) {
		t.Fatal("rule index changed", len(values), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 || values[id] == nil {
			t.Fatal("rule not read exactly once", id, calls["GET "+id])
		}
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, ids[3], ids[9])
	for range 5 {
		if _, _, err := c.monitorRuleIndex(t.Context(), monitorActionGroupType); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
	// A later duplicate row is still rejected, and rows from it on are not read.
	s.handle = nil
	s.lists[collection] = append(s.lists[collection][:6], append([]any{s.lists[collection][1]}, s.lists[collection][6:]...)...)
	clear(calls)
	if _, _, err := c.monitorRuleIndex(t.Context(), monitorActionGroupType); err == nil || !strings.Contains(err.Error(), "invalid_monitor_rule_list_identity") {
		t.Fatal("duplicate rule row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 6]; calls["GET "+id] != want {
			t.Fatal("rule reads around the duplicate row", id, calls["GET "+id])
		}
	}
}

func TestSweep5NetappPoolMembersConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	pool := strings.ToLower(resourceID(netappAccountType, "acct") + "/capacityPools/pool")
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/volumes/v%02d", pool, i)
		raw := map[string]any{"id": id, "type": netappVolumeType, "name": last(id), "location": "eastus", "properties": map[string]any{"fileSystemId": fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)}}
		s.add(raw, netappVersion)
		s.lists[pool+"/volumes"] = append(s.lists[pool+"/volumes"], raw)
		ids = append(ids, id)
	}
	gone := pool + "/volumes/gone"
	s.gone[gone] = true
	known := map[string]any{gone: map[string]any{"configuration": "c", "uid": "99999999-0000-4000-8000-000000000000", "absent": false}}
	c := sweep5Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/volumes/") }, 5*time.Millisecond)
	members, err := c.netappPoolMembers(t.Context(), pool, "eastus", known)
	if err != nil || len(members) != len(ids)+1 || object(members[gone])["absent"] != true {
		t.Fatal("pool members changed", len(members), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 || object(members[id])["absent"] != false {
			t.Fatal("volume not read exactly once", id, calls["GET "+id])
		}
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, ids[3], ids[9])
	for range 5 {
		if _, err := c.netappPoolMembers(t.Context(), pool, "eastus", known); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep5KeyVaultVaultsConcurrentInOrder(t *testing.T) {
	template := batchClone(keyVaultExample(t, "GetCertificate-example"))
	object(template["attributes"])["recoveryLevel"] = "Recoverable+Purgeable"
	var mu sync.Mutex
	calls, peak, failing := map[string]int{}, &peakTracker{}, map[string]string{}
	vaultID := func(i int) string { return strings.ToLower(resourceID(keyVaultType, fmt.Sprintf("vault%02d", i))) }
	r := concurrentProtocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		path := strings.ToLower(q.URL.Path)
		mu.Lock()
		calls[q.URL.Host+path]++
		code := failing[path]
		mu.Unlock()
		if code != "" {
			return sweep5Denied(code), nil
		}
		host := q.URL.Hostname()
		switch {
		case path == "/subscriptions/"+testSubscription+"/providers/microsoft.keyvault/vaults":
			values := []any{}
			for i := range 12 {
				values = append(values, map[string]any{"id": vaultID(i), "type": keyVaultType, "name": last(vaultID(i)), "location": "eastus"})
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		case strings.HasPrefix(path, "/subscriptions/"):
			peak.hold(5 * time.Millisecond)
			return jsonResponse(200, map[string]any{"id": path, "type": keyVaultType, "name": last(path), "location": "eastus", "properties": map[string]any{"vaultUri": "https://" + last(path) + ".vault.azure.net/"}}, nil), nil
		case strings.HasSuffix(host, ".vault.azure.net") && path == "/certificates":
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": "https://" + host + "/certificates/cert", "attributes": template["attributes"], "x5t": template["x5t"]}}}, nil), nil
		case strings.HasSuffix(host, ".vault.azure.net") && path == "/certificates/cert/":
			body := batchClone(template)
			body["id"] = "https://" + host + "/certificates/cert/f60f2a4f8ae442cfb41ca2090bd4b769"
			return jsonResponse(200, body, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	request := keyVaultRequest(&keyVaultFixture{runtime: r})
	batch, err := r.List(t.Context(), request)
	if err != nil || len(batch.Items) != 12 {
		t.Fatal("certificates changed", len(batch.Items), err)
	}
	for i := range 12 {
		host := last(vaultID(i)) + ".vault.azure.net"
		if !strings.HasPrefix(batch.Items[i].NativeID, vaultID(i)+"/") || calls["management.azure.com"+vaultID(i)] != 1 || calls[host+"/certificates"] != 1 || calls[host+"/certificates/cert/"] != 1 {
			t.Fatal("vault not read once in order", i, batch.Items[i].NativeID, calls["management.azure.com"+vaultID(i)])
		}
	}
	sweep5CheckPeak(t, peak)
	mu.Lock()
	failing[vaultID(3)], failing[vaultID(9)] = "FirstDenied", "LaterDenied"
	mu.Unlock()
	for range 5 {
		if _, err := r.List(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

// The subscription walk enumerates each parent's children concurrently and
// keeps the serial item order: each parent, then its children.
func TestSweep5InventoryChildrenConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/providers/microsoft.authorization/locks"] = []any{}
	var vnets []string
	for i := range 12 {
		vnet := nativeResource(vnetType, fmt.Sprintf("vnet%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded"})
		id := strings.ToLower(text(vnet["id"]))
		s.add(vnet, "")
		s.lists[root+"/resources"] = append(s.lists[root+"/resources"], vnet)
		s.lists[id+"/subnets"] = []any{map[string]any{"id": id + "/subnets/a", "name": "a", "type": subnetType, "properties": map[string]any{"addressPrefix": "10.0.0.0/24"}}}
		vnets = append(vnets, id)
	}
	r := s.runtime(t)
	calls, peak := countCalls(s, func(path string) bool { return strings.HasSuffix(path, "/subnets") }, 5*time.Millisecond)
	request := contracts.InventoryRequest{ConnectionID: "connection", Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
	batch, err := r.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, item := range batch.Items {
		if item.NativeType == vnetType || item.NativeType == subnetType {
			order = append(order, item.NativeID)
		}
	}
	if len(order) != 2*len(vnets) {
		t.Fatal("inventory changed", order)
	}
	for i, id := range vnets {
		if order[2*i] != id || order[2*i+1] != id+"/subnets/a" || calls["GET "+id+"/subnets"] != 1 || calls["GET "+id] != 1 {
			t.Fatal("parent or child not read once in order", id, order[2*i], calls["GET "+id+"/subnets"])
		}
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, vnets[3]+"/subnets", vnets[9]+"/subnets")
	for range 5 {
		if _, err := r.List(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
	// A failed detail read stops the walk: no later parent's children are read.
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) == vnets[4] {
			return sweep5Denied("DetailDenied"), true
		}
		return nil, false
	}
	clear(calls)
	if _, err := r.List(t.Context(), request); err == nil || !strings.Contains(err.Error(), "DetailDenied") {
		t.Fatal("detail failure lost", err)
	}
	for i, id := range vnets {
		if want := map[bool]int{true: 1, false: 0}[i < 4]; calls["GET "+id+"/subnets"] != want {
			t.Fatal("children read around the failed detail", id, calls["GET "+id+"/subnets"])
		}
	}
}

func TestSweep5IncomingMigrationsConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	collection := "/subscriptions/" + testSubscription + "/providers/microsoft.servicebus/namespaces"
	var ids []string
	for i := range 12 {
		raw := nativeResource(serviceBusNamespaceType, fmt.Sprintf("ns%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded"})
		id := strings.ToLower(text(raw["id"]))
		s.add(raw, "")
		s.lists[collection] = append(s.lists[collection], raw)
		s.lists[id+"/migrationconfigurations"] = []any{}
		ids = append(ids, id)
	}
	c := sweep5Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { _, ok := s.records[path]; return ok }, 5*time.Millisecond)
	if incoming, err := c.incomingMigrations(t.Context()); err != nil || len(incoming) != 0 {
		t.Fatal("incoming migrations changed", incoming, err)
	}
	for _, id := range ids {
		// Its own GET, then the child walk's parent generation recheck.
		if calls["GET "+id] != 2 || calls["GET "+id+"/migrationconfigurations"] != 1 {
			t.Fatal("namespace not read once", id, calls["GET "+id], calls["GET "+id+"/migrationconfigurations"])
		}
	}
	if calls["GET "+collection] != 2 {
		t.Fatal("namespace set not fenced by two lists", calls["GET "+collection])
	}
	sweep5CheckPeak(t, peak)
	sweep5FailTwo(s, ids[3], ids[9])
	for range 5 {
		if _, err := c.incomingMigrations(t.Context()); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}
