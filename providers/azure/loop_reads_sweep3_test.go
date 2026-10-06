package azure

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func sweep3Denied(code string) *http.Response {
	return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil)
}

// sweep3FailInOrder makes ids[3] and ids[9] fail and requires the earlier one.
func sweep3FailInOrder(t *testing.T, s *dnsScenario, ids []string, call func() error) {
	t.Helper()
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return sweep3Denied("FirstDenied"), true
		case ids[9]:
			return sweep3Denied("LaterDenied"), true
		}
		return nil, false
	}
	defer func() { s.handle = nil }()
	for range 5 {
		if err := call(); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func sweep3ReadOnce(t *testing.T, calls map[string]int, peak *peakTracker, ids []string) {
	t.Helper()
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("not read exactly once", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("reads were not concurrent within the bound", peak.peak)
	}
}

func sweep3Client(t *testing.T, s *dnsScenario) *client {
	t.Helper()
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Retained instances, vault children and policy consumers read their members
// concurrently and fail in id order.
func TestSweep3DataProtectionMemberReads(t *testing.T) {
	s := newDNSScenario()
	vault := strings.ToLower(resourceID(dataProtectionVault, "vault"))
	policy := vault + "/backuppolicies/policy"
	s.add(map[string]any{"id": policy, "name": "policy", "type": dataProtectionPolicy, "properties": map[string]any{"objectType": "BackupPolicy"}}, dataProtectionVersion)
	s.lists[vault+"/backuppolicies"] = []any{s.records[policy]}
	s.lists[vault+"/backupinstances"] = []any{}
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/deletedbackupinstances/inst%02d", vault, i)
		props := map[string]any{"objectType": "BackupInstance", "policyInfo": map[string]any{"policyId": policy}, "dataSourceInfo": map[string]any{"resourceID": strings.ToLower(resourceID(diskType, "source")), "resourceName": "source"}}
		s.add(map[string]any{"id": id, "name": last(id), "type": dataProtectionDeletedInstance, "properties": props}, dataProtectionVersion)
		s.lists[vault+"/deletedbackupinstances"] = append(s.lists[vault+"/deletedbackupinstances"], s.records[id])
		ids = append(ids, id)
	}
	c := sweep3Client(t, s)
	ctx := context.Background()
	cases := map[string]struct {
		want int
		call func() (map[string]any, error)
	}{
		"retained": {12, func() (map[string]any, error) {
			return c.protectionRetainedInstances(ctx, vault+"/backupinstances/instance", nil)
		}},
		"children":  {13, func() (map[string]any, error) { return c.dataProtectionVaultChildren(ctx, vault, nil) }},
		"consumers": {12, func() (map[string]any, error) { return c.protectionPolicyConsumers(ctx, policy, nil) }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/deletedbackupinstances/inst") }, 5*time.Millisecond)
			got, err := tc.call()
			if err != nil || len(got) != tc.want {
				t.Fatal("members changed", len(got), err)
			}
			sweep3ReadOnce(t, calls, peak, ids)
			sweep3FailInOrder(t, s, ids, func() error { _, err := tc.call(); return err })
		})
	}
}

// Classic DMS: a service's projects are read concurrently, the projects'
// indexes are listed concurrently, and an invalid row is not read past.
func TestSweep3DataMigrationClassicForest(t *testing.T) {
	s := newDNSScenario()
	service := strings.ToLower(resourceID(dataMigrationServiceType, "svc"))
	s.add(map[string]any{"id": service, "name": "svc", "type": dataMigrationServiceType, "location": "eastus", "properties": map[string]any{}}, dataMigrationVersion)
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.datamigration/services"] = []any{s.records[service]}
	s.lists[service+"/servicetasks"] = []any{}
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/projects/p%02d", service, i)
		s.add(map[string]any{"id": id, "name": last(id), "type": dataMigrationProjectType, "location": "eastus", "properties": map[string]any{}}, dataMigrationVersion)
		s.lists[service+"/projects"] = append(s.lists[service+"/projects"], s.records[id])
		s.lists[id+"/tasks"], s.lists[id+"/files"] = []any{}, []any{}
		ids = append(ids, id)
	}
	c := sweep3Client(t, s)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/projects/p") }, 5*time.Millisecond)
	forest, err := c.dataMigrationClassicForest(t.Context(), nil)
	if err != nil || len(forest.members) != 13 {
		t.Fatal("forest changed", len(forest.members), err)
	}
	sweep3ReadOnce(t, calls, peak, ids)
	for _, id := range ids {
		if calls["GET "+id+"/tasks"] != 1 || calls["GET "+id+"/files"] != 1 {
			t.Fatal("project index not listed exactly once", id)
		}
	}
	sweep3FailInOrder(t, s, ids, func() error { _, err := c.dataMigrationClassicForest(t.Context(), nil); return err })
	// A failing index of an earlier project wins over a later project's.
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[2] + "/files":
			return sweep3Denied("FirstDenied"), true
		case ids[8] + "/tasks":
			return sweep3Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := c.dataMigrationClassicForest(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first index failure in order lost", err)
		}
	}
	s.handle = nil
	listed := s.lists[service+"/projects"]
	listed[5] = listed[4]
	clear(calls)
	if _, err := c.dataMigrationClassicForest(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "invalid_datamigration_index_identity") {
		t.Fatal("duplicate project accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

// Every resource group is read and indexed concurrently; results and the
// first failure follow group order.
func TestSweep3DeploymentStackSnapshotScopes(t *testing.T) {
	s := newDNSScenario()
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.resources/deploymentstacks"] = []any{}
	var groups []string
	for i := range 12 {
		group := fmt.Sprintf("/subscriptions/%s/resourcegroups/g%02d", testSubscription, i)
		s.records[group] = map[string]any{"id": group, "name": last(group), "type": groupType, "location": "eastus"}
		path := group + "/providers/microsoft.resources/deploymentstacks"
		stack := map[string]any{"id": path + "/stack", "name": "stack", "type": deploymentStackType, "properties": map[string]any{"provisioningState": "succeeded"}}
		s.records[path+"/stack"] = stack
		s.lists[path] = []any{stack}
		groups = append(groups, group)
	}
	r := s.runtime(t)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.InventoryRequest{ConnectionID: "connection"}
	calls, peak := countCalls(s, func(path string) bool {
		return strings.HasPrefix(path, "/subscriptions/"+testSubscription+"/resourcegroups/g")
	}, 5*time.Millisecond)
	items, absent, err := r.deploymentStackSnapshot(t.Context(), c, req)
	if err != nil || len(items) != 12 || len(absent) != 0 {
		t.Fatal("stacks changed", len(items), absent, err)
	}
	for i, item := range items {
		if item.NativeID != groups[i]+"/providers/microsoft.resources/deploymentstacks/stack" || item.Location != "eastus" {
			t.Fatal("stack out of order or lost its group location", i, item.NativeID, item.Location)
		}
	}
	sweep3ReadOnce(t, calls, peak, groups)
	sweep3FailInOrder(t, s, groups, func() error { _, _, err := r.deploymentStackSnapshot(t.Context(), c, req); return err })
}

// A scope's stacks are read concurrently and fail in id order.
func TestSweep3DeploymentStackInventoryReads(t *testing.T) {
	s := newDNSScenario()
	path := "/subscriptions/" + testSubscription + "/providers/microsoft.resources/deploymentstacks"
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/s%02d", path, i)
		s.records[id] = map[string]any{"id": id, "name": last(id), "type": deploymentStackType, "properties": map[string]any{}}
		s.lists[path] = append(s.lists[path], map[string]any{"id": id, "type": deploymentStackType})
		ids = append(ids, id)
	}
	c := sweep3Client(t, s)
	calls, peak := countCalls(s, func(p string) bool { return strings.HasPrefix(p, path+"/s") }, 5*time.Millisecond)
	rows, absent, err := c.deploymentStackInventory(t.Context(), "/subscriptions/"+testSubscription, nil)
	if err != nil || len(rows) != 12 || len(absent) != 0 {
		t.Fatal("stacks changed", len(rows), absent, err)
	}
	sweep3ReadOnce(t, calls, peak, ids)
	sweep3FailInOrder(t, s, ids, func() error {
		_, _, err := c.deploymentStackInventory(t.Context(), "/subscriptions/"+testSubscription, nil)
		return err
	})
}

// Deny assignments are read concurrently, in id order.
func TestSweep3DenyAssignmentIndexReads(t *testing.T) {
	s := newDNSScenario()
	scope := "/subscriptions/" + testSubscription + "/resourcegroups/group"
	list := scope + "/providers/microsoft.authorization/denyassignments"
	var ids []string
	for i := range 12 {
		body := denyTestBody(scope, fmt.Sprintf("d%02d", i))
		id := strings.ToLower(text(body["id"]))
		s.records[id] = body
		s.lists[list] = append(s.lists[list], body)
		ids = append(ids, id)
	}
	c := sweep3Client(t, s)
	calls, peak := countCalls(s, func(p string) bool { return strings.HasPrefix(p, list+"/") }, 5*time.Millisecond)
	rows, absent, err := c.denyAssignmentIndex(t.Context(), scope, nil)
	if err != nil || len(rows) != 12 || len(absent) != 0 {
		t.Fatal("deny assignments changed", len(rows), absent, err)
	}
	sweep3ReadOnce(t, calls, peak, ids)
	sweep3FailInOrder(t, s, ids, func() error { _, _, err := c.denyAssignmentIndex(t.Context(), scope, nil); return err })
}
