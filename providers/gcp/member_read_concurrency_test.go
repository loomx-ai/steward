package gcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	gkeTestCluster = "projects/sample-project/locations/us-central1-a/clusters/test"
	computeTestURL = "https://compute.googleapis.com/compute/v1/projects/sample-project/"
)

func jsonResponse(req *http.Request, data any) (*http.Response, error) {
	body, _ := json.Marshal(data)
	return apiResponse(req, 200, string(body)), nil
}

func gkeTestRoot(resources int) (asset.Asset, map[string]any) {
	network := gkeNetworkSnapshot{ClusterUID: "cluster-uid", SystemUID: "system-uid", Workloads: []gkeWorkload{}, Resources: []gkeNetworkResource{}}
	for i := range resources {
		network.Resources = append(network.Resources, gkeNetworkResource{ID: fmt.Sprintf("//compute.googleapis.com/projects/sample-project/global/firewalls/fw-%02d", i), Kind: "compute.googleapis.com/Firewall", UID: fmt.Sprintf("uid-%02d", i), Delete: true, Phase: "workload"})
	}
	root := asset.Asset{ID: "cluster", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: clusterType, NativeID: "//container.googleapis.com/" + gkeTestCluster}, Normalized: map[string]any{"id": "cluster-uid", gkeNetworkKey: network}}
	return root, map[string]any{"id": "cluster-uid", "name": "test", "status": "RUNNING"}
}

func gkeTestRequest(root asset.Asset, resources int) contracts.ActionRequest {
	request := contracts.ActionRequest{Asset: root, Action: "delete"}
	network, _ := plannedGKENetwork(root)
	for _, resource := range network.Resources[:resources] {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: root.ID, Delete: true, Asset: asset.Asset{ID: asset.AssetID(last(resource.ID)), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: resource.Kind, NativeID: resource.ID}, Normalized: map[string]any{"id": resource.UID}}})
	}
	return request
}

func firewallGet(r *http.Request) bool {
	return r.Method == "GET" && strings.Contains(r.URL.Path, "/firewalls/fw-")
}

// firstChangedSecondFails answers fw-00 with another identity only after fw-01
// failed; a serial walk reports the changed identity.
func firstChangedSecondFails(next roundTripFunc) roundTripFunc {
	failed := make(chan struct{})
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/fw-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/fw-00"):
			after(failed)
			return apiResponse(req, 200, `{"id":"other"}`), nil
		}
		return next(req)
	}
}

func gkeClusterOrNotFound(live map[string]any) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "container.googleapis.com" {
			return jsonResponse(req, live)
		}
		return notFound(req)
	}
}

func TestGKEPlannedNetworkReadsConcurrentlyInOrder(t *testing.T) {
	root, live := gkeTestRoot(12)
	t.Run("cleanup", func(t *testing.T) {
		probe := newReadProbe(groupReadConcurrency, firewallGet)
		probe.start()
		a := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(notFound))
		if _, pending, err := a.cleanupGKENetwork(t.Context(), gkeTestRequest(root, 12), true); err != nil || pending {
			t.Fatal(pending, err)
		}
		probe.check(t, 12, groupReadConcurrency)
		a = protocolAction(t, clusterType, gkeTestCluster, firstChangedSecondFails(notFound))
		if _, _, err := a.cleanupGKENetwork(t.Context(), gkeTestRequest(root, 12), true); deniedCode(err) != "gke_network_resource_identity_changed" {
			t.Fatal(err)
		}
		// Before the cluster is gone only workload-phase resources are read.
		reads := 0
		var mu sync.Mutex
		a = protocolAction(t, clusterType, gkeTestCluster, func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			reads++
			mu.Unlock()
			return notFound(req)
		})
		cluster := root
		network, _ := plannedGKENetwork(root)
		for i := range network.Resources {
			if i%2 == 0 {
				network.Resources[i].Phase = "cluster"
			}
		}
		cluster.Normalized = map[string]any{"id": "cluster-uid", gkeNetworkKey: network}
		if _, _, err := a.cleanupGKENetwork(t.Context(), gkeTestRequest(cluster, 12), false); err != nil || reads != 6 {
			t.Fatal(err, reads)
		}
	})
	t.Run("preflight", func(t *testing.T) {
		probe := newReadProbe(groupReadConcurrency, firewallGet)
		probe.start()
		a := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(notFound))
		if reason, err := a.plannedGKE(t.Context(), gkeTestRequest(root, 12), live); err != nil || reason != "" {
			t.Fatal(reason, err)
		}
		probe.check(t, 12, groupReadConcurrency)
		a = protocolAction(t, clusterType, gkeTestCluster, firstChangedSecondFails(notFound))
		if reason, err := a.plannedGKE(t.Context(), gkeTestRequest(root, 12), live); err != nil || reason != "gke_network_resource_identity_changed" {
			t.Fatal(reason, err)
		}
	})
	t.Run("contribution", func(t *testing.T) {
		probe := newReadProbe(groupReadConcurrency, firewallGet)
		probe.start()
		a := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(func(req *http.Request) (*http.Response, error) {
			if firewallGet(req) {
				return apiResponse(req, 200, fmt.Sprintf(`{"id":"uid-%s"}`, strings.TrimPrefix(last(req.URL.Path), "fw-"))), nil
			}
			return gkeClusterOrNotFound(live)(req)
		}))
		groups := &computeGroups{client: a.client}
		if _, _, err := groups.contributeGKE(t.Context(), []asset.Asset{root}); err != nil {
			t.Fatal(err)
		}
		probe.check(t, 12, groupReadConcurrency)
		a = protocolAction(t, clusterType, gkeTestCluster, firstChangedSecondFails(gkeClusterOrNotFound(live)))
		if _, _, err := (&computeGroups{client: a.client}).contributeGKE(t.Context(), []asset.Asset{root}); deniedCode(err) != "gke_network_resource_identity_changed" {
			t.Fatal(err)
		}
	})
}

// gkePools answers a cluster of node pools p-NN, each with one empty managed
// group m-NN and its instance group g-NN.
func gkePools(next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if response, err := next(req); response != nil || err != nil {
			return response, err
		}
		path, name := req.URL.Path, last(req.URL.Path)
		suffix := name[strings.LastIndex(name, "-")+1:]
		switch {
		case strings.Contains(path, "/nodePools/"):
			return jsonResponse(req, map[string]any{"name": name, "status": "RUNNING", "instanceGroupUrls": []any{computeTestURL + "zones/us-central1-a/instanceGroupManagers/m-" + suffix}})
		case strings.HasSuffix(path, "/listPerInstanceConfigs"), strings.HasSuffix(path, "/listManagedInstances"):
			return apiResponse(req, 200, `{}`), nil
		case strings.Contains(path, "/instanceGroupManagers/"):
			return jsonResponse(req, map[string]any{"id": "9" + suffix, "selfLink": computeTestURL + "zones/us-central1-a/instanceGroupManagers/" + name, "status": map[string]any{"isStable": true}, "instanceGroup": computeTestURL + "zones/us-central1-a/instanceGroups/g-" + suffix, "targetSize": 0})
		case strings.Contains(path, "/instanceGroups/"):
			return jsonResponse(req, map[string]any{"id": "8" + suffix})
		}
		return notFound(req)
	}
}

func gkePoolCluster(count int) (asset.Asset, map[string]any) {
	root, live := gkeTestRoot(0)
	var pools []any
	for i := range count {
		pools = append(pools, map[string]any{"name": fmt.Sprintf("p-%02d", i), "status": "RUNNING"})
	}
	live["nodePools"] = pools
	return root, live
}

func passThrough(*http.Request) (*http.Response, error) { return nil, nil }

func TestGKEMembersReadPoolsAndGroupsConcurrentlyInOrder(t *testing.T) {
	root, live := gkePoolCluster(12)
	for name, match := range map[string]func(*http.Request) bool{
		"pools": func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/nodePools/") },
		"groups": func(r *http.Request) bool {
			return r.Method == "GET" && strings.Contains(r.URL.Path, "/instanceGroupManagers/m-") && !strings.Contains(r.URL.Path, "/list")
		},
	} {
		t.Run(name, func(t *testing.T) {
			probe := newReadProbe(groupReadConcurrency, match)
			probe.start()
			a := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(gkePools(passThrough)))
			members, err := a.client.gkeMembers(t.Context(), root, live)
			if err != nil || len(members) != 36 {
				t.Fatal(len(members), err)
			}
			for i := range 12 {
				if want := fmt.Sprintf("p-%02d", i); last(members[3*i].id) != want || members[3*i+1].kind != managerType || members[3*i+2].kind != instanceGroupType {
					t.Fatalf("member %d out of order: %+v", i, members[3*i])
				}
			}
			probe.check(t, 12, groupReadConcurrency)
		})
	}
	t.Run("order", func(t *testing.T) {
		// m-00 is busy but answers last; m-01 fails first.
		failed := make(chan struct{})
		a := protocolAction(t, clusterType, gkeTestCluster, gkePools(func(req *http.Request) (*http.Response, error) {
			switch {
			case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/instanceGroupManagers/m-01"):
				defer close(failed)
				return apiResponse(req, 400, `{"error":{"code":400}}`), nil
			case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/instanceGroupManagers/m-00"):
				after(failed)
				return jsonResponse(req, map[string]any{"status": map[string]any{"isStable": false}})
			}
			return nil, nil
		}))
		if _, err := a.client.gkeMembers(t.Context(), root, live); deniedCode(err) != "managed_group_not_stable" {
			t.Fatal(err)
		}
	})
}

// tpuTestDisks returns a node with twelve data disks d-NN and a transport
// answering each disk with its own identity.
func tpuTestDisks() (map[string]any, roundTripFunc) {
	var disks []any
	for i := range 12 {
		disks = append(disks, map[string]any{"sourceDisk": fmt.Sprintf("projects/sample-project/zones/us-central2-b/disks/d-%02d", i)})
	}
	return map[string]any{"dataDisks": disks}, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(req, map[string]any{"id": "uid-" + last(req.URL.Path), "selfLink": computeTestURL + "zones/us-central2-b/disks/" + last(req.URL.Path)})
	}
}

func diskGet(r *http.Request) bool {
	return r.Method == "GET" && strings.Contains(r.URL.Path, "/disks/d-")
}

// firstDiskChangedSecondFails answers d-00 with another identity only after
// d-01 failed.
func firstDiskChangedSecondFails(next roundTripFunc) roundTripFunc {
	failed := make(chan struct{})
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/d-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/d-00"):
			after(failed)
			return apiResponse(req, 200, `{"id":"other"}`), nil
		}
		return next(req)
	}
}

func TestTPUDataDisksReadConcurrentlyInOrder(t *testing.T) {
	node, transport := tpuTestDisks()
	probe := newReadProbe(groupReadConcurrency, diskGet)
	probe.start()
	c := protocolAction(t, tpuNodeType, tpuTestNode, probe.wrap(transport)).client
	children, proofs, err := c.tpuDisks(t.Context(), node)
	if err != nil || len(children) != 12 || len(proofs) != 12 || last(children[0].id) != "d-00" || last(children[11].id) != "d-11" {
		t.Fatal(children, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	c = protocolAction(t, tpuNodeType, tpuTestNode, firstDiskChangedSecondFails(transport)).client
	if _, _, err := c.tpuDisks(t.Context(), node); deniedCode(err) != "tpu_data_disk_identity_changed" {
		t.Fatal(err)
	}
	// Retained-disk verification reads the same disks the same way.
	encoded, _ := json.Marshal(proofs)
	node[tpuDiskProofs] = string(encoded)
	probe = newReadProbe(groupReadConcurrency, diskGet)
	probe.start()
	c = protocolAction(t, tpuNodeType, tpuTestNode, probe.wrap(transport)).client
	if err := c.tpuVerifyDisks(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	c = protocolAction(t, tpuNodeType, tpuTestNode, firstDiskChangedSecondFails(transport)).client
	if err := c.tpuVerifyDisks(t.Context(), node); deniedCode(err) != "tpu_retained_disk_changed" {
		t.Fatal(err)
	}
}

// serviceNamespace lists twelve services s-NN under the namespace (none when
// empty) and answers their endpoint lists empty.
func serviceNamespace(empty bool, next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if response, err := next(req); response != nil || err != nil {
			return response, err
		}
		path := strings.TrimPrefix(req.URL.Path, "/v1/")
		switch {
		case strings.HasSuffix(path, "/namespaces/apps/services"):
			var services []any
			for i := 0; i < 12 && !empty; i++ {
				services = append(services, map[string]any{"name": fmt.Sprintf("%s/services/s-%02d", path[:strings.LastIndex(path, "/services")], i)})
			}
			return jsonResponse(req, map[string]any{"services": services})
		case strings.HasSuffix(path, "/endpoints"):
			return apiResponse(req, 200, `{}`), nil
		case strings.Contains(path, "/services/s-") && !empty:
			return jsonResponse(req, map[string]any{"name": path})
		}
		return notFound(req)
	}
}

func TestServicePreflightReadsRemovedAndNestedChildrenConcurrentlyInOrder(t *testing.T) {
	serviceGet := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/services/s-") && !strings.HasSuffix(r.URL.Path, "/endpoints")
	}
	t.Run("removed", func(t *testing.T) {
		probe := newReadProbe(groupReadConcurrency, serviceGet)
		probe.start()
		a, request, _, _ := serviceManyChildren(t, probe.wrap(serviceNamespace(true, passThrough)))
		if err := a.serviceCascadePreflight(t.Context(), request, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		probe.check(t, 12, groupReadConcurrency)
		// s-00 survives but answers last; s-01 fails first.
		failed := make(chan struct{})
		a, request, _, _ = serviceManyChildren(t, serviceNamespace(true, func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, "/s-01"):
				defer close(failed)
				return apiResponse(req, 400, `{"error":{"code":400}}`), nil
			case strings.HasSuffix(req.URL.Path, "/s-00"):
				after(failed)
				return apiResponse(req, 200, `{}`), nil
			}
			return nil, nil
		}))
		if err := a.serviceCascadePreflight(t.Context(), request, map[string]any{}); deniedCode(err) != "service_child_membership_changed" {
			t.Fatal(err)
		}
	})
	t.Run("nested", func(t *testing.T) {
		probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/endpoints") })
		probe.start()
		a, request, _, _ := serviceManyChildren(t, probe.wrap(serviceNamespace(false, passThrough)))
		if err := a.serviceCascadePreflight(t.Context(), request, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		probe.check(t, 12, groupReadConcurrency)
		// s-00's endpoints answer last with a direct (undeletable) child; s-01's
		// endpoint list fails first. The serial recursion reports s-00.
		failed := make(chan struct{})
		a, request, _, _ = serviceManyChildren(t, serviceNamespace(false, func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, "/s-01/endpoints"):
				defer close(failed)
				return apiResponse(req, 400, `{"error":{"code":400}}`), nil
			case strings.HasSuffix(req.URL.Path, "/s-00/endpoints"):
				after(failed)
				return jsonResponse(req, map[string]any{"endpoints": []any{map[string]any{"name": strings.TrimPrefix(req.URL.Path, "/v1/") + "/e-0"}}})
			}
			return nil, nil
		}))
		if err := a.serviceCascadePreflight(t.Context(), request, map[string]any{}); deniedCode(err) != "service_child_missing_from_plan" {
			t.Fatal(err)
		}
	})
}

func TestDataprocMemberDisksComeFromOneFilteredListPerScope(t *testing.T) {
	s := newDataprocScenario(t)
	_, _, _, request := dataprocReviewed(t, s)
	var mu sync.Mutex
	var gets, lists []string
	s.handle = func(req *http.Request) (*http.Response, bool) {
		path := strings.TrimPrefix(req.URL.Path, "/compute/v1/")
		if req.URL.Host != "compute.googleapis.com" || !strings.Contains(path, "/disks") {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasSuffix(path, "/disks") {
			gets = append(gets, last(path))
			return nil, false
		}
		lists = append(lists, path)
		filter := req.URL.Query().Get("filter")
		var items []any
		for id, data := range s.resources {
			if strings.HasPrefix(id, path+"/") && strings.Contains(filter, last(id)) {
				items = append(items, data)
			}
		}
		body, _ := json.Marshal(map[string]any{"items": items})
		return apiResponse(req, 200, string(body)), true
	}
	r := protocolRuntime(t, s.transport(t))
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if check, err := driver.Preflight(t.Context(), request); err != nil || !check.Allowed {
		t.Fatal(check, err)
	}
	// VM disks come from filtered lists, one per zone or region and kind for
	// each group (master: zonal boot and regional input; managed workers;
	// orphan VMs), and no attached disk is read with its own GET.
	for _, name := range gets {
		if name == "analytics-m-boot" || name == "analytics-w-boot" || name == "input-data" {
			t.Fatalf("disk %s read with its own GET; lists %v", name, lists)
		}
	}
	slices.Sort(lists)
	want := []string{"projects/sample-project/aggregated/disks", "projects/sample-project/aggregated/disks", "projects/sample-project/regions/us-central1/disks", "projects/sample-project/zones/us-central1-a/disks", "projects/sample-project/zones/us-central1-a/disks", "projects/sample-project/zones/us-central1-a/disks"}
	if !slices.Equal(lists, want) {
		t.Fatalf("lists %v", lists)
	}
}

// batchManyExternalDisks adds VMs r-NN to the reviewed job, each with its own
// retained disk ext-NN and the job's shared input disk.
func batchManyExternalDisks(s *batchScenario) {
	template := s.resources["projects/sample-project/zones/europe-west1-b/instances/worker-q91"]
	input := s.resources[batchInput]
	for i := range 12 {
		name := fmt.Sprintf("projects/sample-project/zones/europe-west1-b/instances/worker-r%02d", i)
		disk := fmt.Sprintf("projects/sample-project/zones/europe-west1-b/disks/ext-%02d", i)
		vm := groupCopy(template)
		vm["name"], vm["id"], vm["selfLink"] = last(name), fmt.Sprintf("50%02d", i), "https://www.googleapis.com/compute/v1/"+name
		vm["disks"] = []any{
			map[string]any{"source": "https://www.googleapis.com/compute/v1/" + batchInput, "deviceName": "input", "autoDelete": false, "mode": "READ_ONLY"},
			map[string]any{"source": "https://www.googleapis.com/compute/v1/" + disk, "deviceName": "ext", "autoDelete": false},
		}
		s.resources[name] = vm
		s.resources[disk] = map[string]any{"name": last(disk), "id": fmt.Sprintf("60%02d", i), "selfLink": "https://www.googleapis.com/compute/v1/" + disk, "users": []any{vm["selfLink"]}}
		input["users"] = append(array(input["users"]), vm["selfLink"])
	}
}

func TestBatchExternalDisksReadOnceConcurrentlyInOrder(t *testing.T) {
	parent := asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: batchJobType, NativeID: "//batch.googleapis.com/" + batchRoot}
	s := newBatchScenario(t)
	batchManyExternalDisks(s)
	var mu sync.Mutex
	inputReads := 0
	probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/disks/ext-") })
	probe.start()
	c := protocolAction(t, batchJobType, batchRoot, probe.wrap(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/"+batchInput) {
			mu.Lock()
			inputReads++
			mu.Unlock()
		}
		return s.transport(t)(req)
	})).client
	children, err := c.batchChildren(t.Context(), parent, s.resources[batchRoot])
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, child := range children {
		if child.retain {
			retained++
		}
	}
	// The shared input disk is read once for all thirteen VMs.
	if retained != 13 || inputReads != 1 {
		t.Fatal(retained, inputReads)
	}
	probe.check(t, 12, groupReadConcurrency)
	// ext-00 is detached but answers last; ext-01 fails first.
	failed := make(chan struct{})
	c = protocolAction(t, batchJobType, batchRoot, func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/ext-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/ext-00"):
			after(failed)
			return jsonResponse(req, map[string]any{"id": "6000", "selfLink": "https://www.googleapis.com/compute/v1/projects/sample-project/zones/europe-west1-b/disks/ext-00", "users": []any{}})
		}
		return s.transport(t)(req)
	}).client
	if _, err := c.batchChildren(t.Context(), parent, s.resources[batchRoot]); deniedCode(err) != "batch_disk_attachment_unverified" {
		t.Fatal(err)
	}
}

func TestReadAheadReadsEachResourceOnceWithBoundedConcurrency(t *testing.T) {
	probe := newReadProbe(groupReadConcurrency, firewallGet)
	probe.start()
	c := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(req, map[string]any{"id": last(req.URL.Path)})
	})).client
	ahead := newReadAhead(t.Context(), c)
	id := func(i int) string {
		return fmt.Sprintf("//compute.googleapis.com/projects/sample-project/global/firewalls/fw-%02d", i)
	}
	for i := range 12 {
		ahead.start("compute.googleapis.com/Firewall", id(i))
		ahead.start("compute.googleapis.com/Firewall", id(i))
	}
	for i := 11; i >= 0; i-- {
		if data, err := ahead.get("compute.googleapis.com/Firewall", id(i)); err != nil || data["id"] != last(id(i)) {
			t.Fatal(data, err)
		}
	}
	ahead.close()
	probe.check(t, 12, groupReadConcurrency)
}

func TestGKENetworkTemplatesReadConcurrentlyInOrder(t *testing.T) {
	var nodes []gkeMember
	for i := range 12 {
		nodes = append(nodes, gkeMember{kind: managerType, id: fmt.Sprintf("m-%02d", i), data: map[string]any{"instanceTemplate": computeTestURL + fmt.Sprintf("global/instanceTemplates/t-%02d", i)}})
	}
	root, _ := gkeTestRoot(0)
	live := map[string]any{"name": "test", "network": "cluster-network"}
	templates := func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if response, err := next(req); response != nil || err != nil {
				return response, err
			}
			if strings.Contains(req.URL.Path, "/instanceTemplates/") {
				return jsonResponse(req, map[string]any{"properties": map[string]any{"tags": map[string]any{"items": []any{"gke-test-abc12345-node"}}}})
			}
			return apiResponse(req, 200, `{"items":[]}`), nil
		}
	}
	probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/instanceTemplates/") })
	probe.start()
	c := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(templates(passThrough))).client
	if err := c.gkeNetworkAncillary(t.Context(), t.Context(), root, live, nodes, "system-uid", nil, map[string]gkeNetworkResource{}); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// t-00 is missing but answers last; t-01 fails first.
	failed := make(chan struct{})
	c = protocolAction(t, clusterType, gkeTestCluster, templates(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/t-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/t-00"):
			after(failed)
			return notFound(req)
		}
		return nil, nil
	})).client
	if err := c.gkeNetworkAncillary(t.Context(), t.Context(), root, live, nodes, "system-uid", nil, map[string]gkeNetworkResource{}); !isNotFound(err) {
		t.Fatal(err)
	}
}

// statefulGroup answers a managed group m of twelve VMs v-NN, each keeping a
// reserved external address a-NN.
func statefulGroup(next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		if response, err := next(req); response != nil || err != nil {
			return response, err
		}
		path := req.URL.Path
		switch {
		case strings.HasSuffix(path, "/listPerInstanceConfigs"):
			return apiResponse(req, 200, `{}`), nil
		case strings.HasSuffix(path, "/listManagedInstances"):
			var members []any
			for i := range 12 {
				members = append(members, map[string]any{"instance": computeTestURL + fmt.Sprintf("zones/us-central1-a/instances/v-%02d", i), "id": fmt.Sprintf("70%02d", i), "currentAction": "NONE", "preservedStateFromPolicy": map[string]any{"externalIPs": map[string]any{"nic0": map[string]any{"autoDelete": "NEVER", "ipAddress": map[string]any{"address": computeTestURL + fmt.Sprintf("regions/us-central1/addresses/a-%02d", i)}}}}})
			}
			return jsonResponse(req, map[string]any{"managedInstances": members})
		case strings.HasSuffix(path, "/instances"):
			var vms []any
			for i := range 12 {
				vms = append(vms, map[string]any{"id": fmt.Sprintf("70%02d", i), "selfLink": computeTestURL + fmt.Sprintf("zones/us-central1-a/instances/v-%02d", i), "networkInterfaces": []any{map[string]any{"name": "nic0", "accessConfigs": []any{map[string]any{"natIP": fmt.Sprintf("192.0.2.%d", i)}}}}})
			}
			return jsonResponse(req, map[string]any{"items": vms})
		case strings.Contains(path, "/addresses/a-"):
			index, _ := strconv.Atoi(strings.TrimPrefix(last(path), "a-"))
			return apiResponse(req, 200, fmt.Sprintf(`{"address":"192.0.2.%d"}`, index)), nil
		}
		return notFound(req)
	}
}

func TestManagedGroupStatefulAddressesReadConcurrentlyInOrder(t *testing.T) {
	const group = "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instanceGroupManagers/m"
	data := map[string]any{"status": map[string]any{"isStable": true}, "instanceGroup": computeTestURL + "zones/us-central1-a/instanceGroups/m", "targetSize": 12}
	addressGet := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/addresses/a-") }
	probe := newReadProbe(groupReadConcurrency, addressGet)
	probe.start()
	c := protocolAction(t, managerType, strings.TrimPrefix(group, "//compute.googleapis.com/"), probe.wrap(statefulGroup(passThrough))).client
	loaded, err := c.loadManagedGroup(t.Context(), group, data)
	if err != nil || len(loaded.nodes) != 12 || len(loaded.nodes[0].resources) != 1 || last(loaded.nodes[0].resources[0].id) != "a-00" {
		t.Fatal(loaded, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// a-00 no longer matches its VM but answers last; a-01 fails first.
	failed := make(chan struct{})
	c = protocolAction(t, managerType, strings.TrimPrefix(group, "//compute.googleapis.com/"), statefulGroup(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/a-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/a-00"):
			after(failed)
			return apiResponse(req, 200, `{"address":"198.51.100.1"}`), nil
		}
		return nil, nil
	})).client
	if _, err := c.loadManagedGroup(t.Context(), group, data); err == nil || !strings.Contains(err.Error(), "does not match its live VM interface") {
		t.Fatal(err)
	}
}

func TestInfraGKEWorkloadNetworkAbsenceReadsConcurrentlyInOrder(t *testing.T) {
	root, _ := gkeTestRoot(12)
	network, _ := plannedGKENetwork(root)
	probe := newReadProbe(groupReadConcurrency, firewallGet)
	probe.start()
	a := protocolAction(t, clusterType, gkeTestCluster, probe.wrap(notFound))
	if err := a.gkeWorkloadNetworkAbsent(t.Context(), network.Resources); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// fw-00 survives but answers last; fw-01 fails first.
	a = protocolAction(t, clusterType, gkeTestCluster, firstChangedSecondFails(notFound))
	if err := a.gkeWorkloadNetworkAbsent(t.Context(), network.Resources); deniedCode(err) != "infra_gke_finalizers_required" {
		t.Fatal(err)
	}
}

// manyBudgets answers one billing account with twelve budgets b-NN.
func manyBudgets(next roundTripFunc) roundTripFunc {
	budget := func(name string) map[string]any {
		data := billingBudgetFixture()
		data["name"] = testBillingAccount + "/budgets/" + name
		return data
	}
	return func(req *http.Request) (*http.Response, error) {
		if response, err := next(req); response != nil || err != nil {
			return response, err
		}
		switch path := strings.TrimPrefix(req.URL.Path, "/v1/"); {
		case path == "projects/sample-project/billingInfo":
			return apiResponse(req, 200, `{"name":"projects/sample-project/billingInfo","projectId":"sample-project","billingEnabled":false}`), nil
		case path == "billingAccounts":
			return jsonResponse(req, map[string]any{"billingAccounts": []any{billingAccountFixture()}})
		case path == testBillingAccount:
			return jsonResponse(req, billingAccountFixture())
		case path == testBillingAccount+"/budgets":
			var budgets []any
			for i := range 12 {
				budgets = append(budgets, budget(fmt.Sprintf("b-%02d", i)))
			}
			return jsonResponse(req, map[string]any{"budgets": budgets})
		case strings.HasPrefix(path, testBillingAccount+"/budgets/"):
			return jsonResponse(req, budget(last(path)))
		}
		return notFound(req)
	}
}

func TestBillingBudgetReadsConcurrentlyInOrder(t *testing.T) {
	budgetGet := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/budgets/b-") }
	probe := newReadProbe(groupReadConcurrency, budgetGet)
	r := protocolRuntime(t, probe.wrap(manyBudgets(passThrough)))
	probe.start()
	batch, err := r.List(t.Context(), billingInventoryRequest(r))
	if err != nil || len(batch.Items) != 12 || last(batch.Items[0].NativeID) != "b-00" || last(batch.Items[11].NativeID) != "b-11" {
		t.Fatal(batch, err)
	}
	// Each budget is read by the account walk and again by its fenced read.
	probe.check(t, 24, groupReadConcurrency)
	// The nth read of b-00 has drifted but answers last; b-01's fails first.
	for round := 1; round <= 2; round++ {
		t.Run(fmt.Sprint("read_", round), func(t *testing.T) {
			var mu sync.Mutex
			reads := map[string]int{}
			failed := make(chan struct{})
			r := protocolRuntime(t, manyBudgets(func(req *http.Request) (*http.Response, error) {
				if !budgetGet(req) {
					return nil, nil
				}
				mu.Lock()
				reads[last(req.URL.Path)]++
				count := reads[last(req.URL.Path)]
				mu.Unlock()
				switch {
				case count != round:
				case strings.HasSuffix(req.URL.Path, "/b-01"):
					defer close(failed)
					return apiResponse(req, 400, `{"error":{"code":400}}`), nil
				case strings.HasSuffix(req.URL.Path, "/b-00"):
					after(failed)
					data := billingBudgetFixture()
					data["name"], data["displayName"] = testBillingAccount+"/budgets/b-00", "changed"
					return jsonResponse(req, data)
				}
				return nil, nil
			}))
			if _, err := r.List(t.Context(), billingInventoryRequest(r)); deniedCode(err) != "billing_budget_changed" {
				t.Fatal(err)
			}
		})
	}
}

func TestTPUQueueNodesReadConcurrently(t *testing.T) {
	s := newTPUScenario(t)
	probe := newReadProbe(2, func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/nodes/training-")
	})
	probe.start()
	c := protocolAction(t, tpuQueueType, tpuTestQueue, probe.wrap(s.transport(t))).client
	nodes, err := c.tpuQueueNodes(t.Context(), "//"+tpuHost+"/"+tpuTestQueue, s.resources[tpuTestQueue])
	if err != nil || len(nodes) != 2 {
		t.Fatal(nodes, err)
	}
	probe.check(t, 2, groupReadConcurrency)
}
