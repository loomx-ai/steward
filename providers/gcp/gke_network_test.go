package gcp

import (
	"context"
	"encoding/json"
	"fmt"
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

type gkeNetworkFixture struct {
	*gkeFixture
	tls                        *kubernetesFixture
	cloud                      map[string]map[string]any
	workloads                  map[string]map[string]any
	servicePolls, clusterPolls int
	clusterDeleting, orphan    bool
	mutations                  []string
	operations                 map[string]string
	opPolls                    map[string]int
	workloadPolls              map[string]int
	workloadResources          map[string][]string
	gateway                    bool
	fault                      func(*http.Request) (*http.Response, bool)
}

func newGKENetworkFixture(t *testing.T) *gkeNetworkFixture {
	f := &gkeNetworkFixture{gkeFixture: newGKEFixture(t), cloud: map[string]map[string]any{}, workloads: map[string]map[string]any{}, operations: map[string]string{}, opPolls: map[string]int{}, workloadPolls: map[string]int{}, workloadResources: map[string][]string{}}
	f.tls = newKubernetesFixture(t, f.kubeHTTP)
	f.tls.client.number = "123456"
	f.tls.native = f.cloudHTTP
	live := f.gke[f.cluster.Identity.NativeID]
	live["endpoint"], live["masterAuth"] = "192.0.2.5", f.tls.live["masterAuth"]
	live["network"], f.cluster.Normalized["network"] = "cluster-network", "cluster-network"
	for _, id := range []string{"vm", "other"} {
		f.live(id)["tags"] = map[string]any{"items": []any{"gke-test-abc12345-node", "custom-shared-tag"}}
	}
	service := kubeService("web")
	object(service["metadata"])["uid"] = "123e4567-e89b-12d3-a456-426614174000"
	object(service["spec"])["ports"] = []any{map[string]any{"port": 80, "protocol": "TCP"}}
	service["status"] = map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.0.2.100"}}}}
	f.workloads["services/default/web"] = service
	legacy := "a" + strings.ReplaceAll(text(object(service["metadata"])["uid"]), "-", "")
	legacy = legacy[:32]
	f.addNetwork("legacy-hc", "HttpHealthCheck", "global/httpHealthChecks/health", map[string]any{"port": 10256})
	f.addNetwork("target-pool", "TargetPool", "regions/us-central1/targetPools/"+legacy, map[string]any{"instances": []any{f.live("vm")["selfLink"], f.live("other")["selfLink"]}, "healthChecks": []any{f.cloud[f.value("legacy-hc").Identity.NativeID]["selfLink"]}})
	f.addNetwork("frontend", "ForwardingRule", "regions/us-central1/forwardingRules/"+legacy, map[string]any{"IPAddress": "192.0.2.100", "IPProtocol": "TCP", "portRange": "80-80", "target": f.cloud[f.value("target-pool").Identity.NativeID]["selfLink"]})
	network := "https://compute.googleapis.com/compute/v1/projects/sample-project/global/networks/cluster-network"
	f.addNetwork("cluster-firewall", "Firewall", "global/firewalls/gke-test-abc12345-all", map[string]any{"network": network, "targetTags": []any{"gke-test-abc12345-node"}})
	f.addNetwork("service-firewall", "Firewall", "global/firewalls/k8s-fw-"+legacy, map[string]any{"network": network, "targetTags": []any{"gke-test-abc12345-node"}, "description": `{"kubernetes.io/service-name":"default/web"}`})
	f.addNetwork("other-firewall", "Firewall", "global/firewalls/gke-test-other123-all", map[string]any{"network": network, "targetTags": []any{"gke-test-other123-node"}})
	f.addNetwork("custom-firewall", "Firewall", "global/firewalls/custom-service-rule", map[string]any{"network": network, "targetTags": []any{"custom-shared-tag"}, "description": `{"kubernetes.io/service-name":"default/web"}`})
	f.addNetwork("static-ip", "Address", "regions/us-central1/addresses/user-reservation", map[string]any{"address": "192.0.2.100", "users": []any{f.cloud[f.value("frontend").Identity.NativeID]["selfLink"]}})
	f.addNetwork("node-route", "Route", "global/routes/cluster-node-route", map[string]any{"network": network, "description": "k8s-node-route", "nextHopInstance": f.live("vm")["selfLink"]})
	f.addNetwork("foreign-frontend", "ForwardingRule", "regions/us-central1/forwardingRules/foreign-network", map[string]any{"network": "https://compute.googleapis.com/compute/v1/projects/sample-project/global/networks/another-network", "loadBalancingScheme": "INTERNAL", "IPAddress": "192.0.2.100", "IPProtocol": "TCP", "portRange": "80-80"})
	return f
}

func (f *gkeNetworkFixture) addNetwork(id, kind, path string, data map[string]any) {
	value := diskAsset(id, "compute.googleapis.com/"+kind, path)
	value.Normalized = data
	value.Normalized["id"], value.Normalized["name"] = "uid-"+id, last(path)
	value.Normalized["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/" + path
	f.assets = append(f.assets, value)
	f.cloud[value.Identity.NativeID] = groupCopy(data)
}

func (f *gkeNetworkFixture) kubeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.fault != nil {
		if response, handled := f.fault(r); handled {
			copyHTTP(w, response)
			return
		}
	}
	if r.URL.Path == "/api/v1/namespaces/kube-system" {
		json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": "kubernetes-system-uid"}})
		return
	}
	if r.URL.Path == "/apis/gateway.networking.k8s.io" {
		if f.gateway {
			io.WriteString(w, `{"kind":"APIGroup","name":"gateway.networking.k8s.io","versions":[{"groupVersion":"gateway.networking.k8s.io/v1"}]}`)
			return
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound"}`)
		return
	}
	if r.URL.Path == "/api/v1/services" || r.URL.Path == "/apis/networking.k8s.io/v1/ingresses" || r.URL.Path == "/apis/gateway.networking.k8s.io/v1/gateways" {
		collection := serviceCollection
		if strings.HasSuffix(r.URL.Path, "ingresses") {
			collection = ingressCollection
		} else if strings.HasSuffix(r.URL.Path, "gateways") {
			collection = kubernetesCollection{"gateway.networking.k8s.io/v1", "gateways", "Gateway"}
		}
		if collection == serviceCollection {
			if service := f.workloads["services/default/web"]; service != nil && object(service["metadata"])["deletionTimestamp"] != nil {
				f.servicePolls++
				if f.servicePolls >= 2 {
					delete(f.workloads, "services/default/web")
					for _, id := range append([]string{"frontend", "target-pool", "legacy-hc", "service-firewall"}, f.workloadResources["services/default/web"]...) {
						if f.orphan && id != "frontend" {
							continue
						}
						delete(f.cloud, f.value(id).Identity.NativeID)
					}
				}
			}
		}
		items := []map[string]any{}
		for key, item := range f.workloads {
			if strings.HasPrefix(key, collection.resource+"/") {
				if collection != serviceCollection && object(item["metadata"])["deletionTimestamp"] != nil {
					f.workloadPolls[key]++
					if f.workloadPolls[key] >= 2 {
						delete(f.workloads, key)
						for _, id := range f.workloadResources[key] {
							if !f.orphan {
								delete(f.cloud, f.value(id).Identity.NativeID)
							}
						}
						continue
					}
				}
				items = append(items, item)
			}
		}
		json.NewEncoder(w).Encode(kubeList(collection, "101", "", items...))
		return
	}
	if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/apis/") {
		for key, item := range f.workloads {
			collection := ingressCollection
			if strings.HasPrefix(key, "gateways/") {
				collection = kubernetesCollection{"gateway.networking.k8s.io/v1", "gateways", "Gateway"}
			}
			path, _ := collection.path(item)
			if r.URL.Path != path {
				continue
			}
			var options map[string]any
			_ = json.NewDecoder(r.Body).Decode(&options)
			meta := object(item["metadata"])
			if object(options["preconditions"])["uid"] != meta["uid"] || object(options["preconditions"])["resourceVersion"] != meta["resourceVersion"] {
				f.t.Error("unconditional frontend deletion")
			}
			meta["deletionTimestamp"] = "2026-09-09T00:00:00Z"
			f.mutations = append(f.mutations, strings.ToLower(collection.kind))
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(item)
			return
		}
	}
	if r.Method == "DELETE" && r.URL.Path == "/api/v1/namespaces/default/services/web" {
		var options map[string]any
		if json.NewDecoder(r.Body).Decode(&options) != nil {
			f.t.Error("invalid Kubernetes deletion body")
		}
		service := f.workloads["services/default/web"]
		if service == nil {
			w.WriteHeader(404)
			io.WriteString(w, `{}`)
			return
		}
		meta := object(service["metadata"])
		if object(options["preconditions"])["uid"] != meta["uid"] || object(options["preconditions"])["resourceVersion"] != meta["resourceVersion"] {
			f.t.Error("unconditional Kubernetes deletion")
		}
		meta["deletionTimestamp"] = "2026-09-09T00:00:00Z"
		meta["finalizers"] = []any{"service.kubernetes.io/load-balancer-cleanup"}
		f.mutations = append(f.mutations, "service")
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(service)
		return
	}
	f.t.Errorf("unexpected Kubernetes API call: %s %s", r.Method, r.URL)
	w.WriteHeader(500)
	io.WriteString(w, `{}`)
}

func copyHTTP(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
	response.Body.Close()
}

func (f *gkeNetworkFixture) cloudHTTP(w http.ResponseWriter, incoming *http.Request) {
	r := incoming.Clone(incoming.Context())
	r.URL.Scheme = "https"
	r.URL.Host = incoming.Host
	if f.fault != nil {
		if response, handled := f.fault(r); handled {
			copyHTTP(w, response)
			return
		}
	}
	w.Header().Set("X-Goog-Request-Id", "native-gcp-request")
	if r.URL.Host == "container.googleapis.com" && r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/clusters/test") {
		if len(f.workloads) > 0 || f.live("vm") == nil {
			f.t.Error("cluster deleted before finalizers or after premature node deletion")
		}
		f.clusterDeleting = true
		f.mutations = append(f.mutations, "cluster")
		json.NewEncoder(w).Encode(map[string]any{"name": "delete-cluster", "status": "PENDING", "location": "us-central1-a"})
		return
	}
	if r.URL.Host == "container.googleapis.com" && strings.HasSuffix(r.URL.Path, "/operations/delete-cluster") {
		f.clusterPolls++
		status := "RUNNING"
		if f.clusterPolls >= 2 {
			status = "DONE"
			delete(f.gke, f.cluster.Identity.NativeID)
			delete(f.gke, f.pool.Identity.NativeID)
		}
		if f.clusterPolls >= 3 {
			for id := range f.resources {
				if id != f.value("data").Identity.NativeID && id != f.value("ip").Identity.NativeID {
					delete(f.resources, id)
				}
			}
			for _, id := range []string{"cluster-firewall", "node-route"} {
				if !f.orphan {
					delete(f.cloud, f.value(id).Identity.NativeID)
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "delete-cluster", "status": status, "location": "us-central1-a"})
		return
	}
	if r.URL.Host == "compute.googleapis.com" {
		if strings.Contains(r.URL.Path, "/operations/") && f.operations[last(r.URL.Path)] != "" {
			op := last(r.URL.Path)
			f.opPolls[op]++
			status := "RUNNING"
			if f.opPolls[op] >= 2 {
				status = "DONE"
			}
			if f.opPolls[op] >= 3 {
				delete(f.cloud, f.operations[op])
			}
			json.NewEncoder(w).Encode(map[string]any{"name": op, "status": status})
			return
		}
		for _, collection := range []string{"forwardingRules", "firewalls", "routes", "addresses"} {
			if last(r.URL.Path) != collection {
				continue
			}
			items := []map[string]any{}
			for id, data := range f.cloud {
				if strings.HasPrefix(id, "//compute.googleapis.com/"+strings.TrimPrefix(r.URL.Path, "/compute/v1/")+"/") {
					items = append(items, data)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		id := "//compute.googleapis.com/" + strings.TrimPrefix(r.URL.Path, "/compute/v1/")
		if data, exists := f.cloud[id]; exists {
			if r.Method == "DELETE" {
				if len(f.workloads) > 0 {
					f.t.Error("native cleanup bypassed Kubernetes finalizers")
				}
				op := "cleanup-" + text(data["id"])
				f.operations[op] = id
				f.mutations = append(f.mutations, id)
				json.NewEncoder(w).Encode(map[string]any{"name": op, "status": "PENDING"})
				return
			}
			json.NewEncoder(w).Encode(data)
			return
		}
		for _, value := range f.assets {
			if value.Identity.NativeID == id && strings.HasPrefix(text(value.Normalized["id"]), "uid-") {
				w.WriteHeader(404)
				io.WriteString(w, `{}`)
				return
			}
		}
	}
	response, err := f.gkeFixture.roundTrip(r)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(500)
		return
	}
	copyHTTP(w, response)
}

func (f *gkeNetworkFixture) driver() *action {
	driver := protocolAction(f.t, clusterType, strings.TrimPrefix(f.cluster.Identity.NativeID, "//container.googleapis.com/"), f.gkeFixture.roundTrip)
	driver.client = f.tls.client
	return driver
}

func (f *gkeNetworkFixture) enrich() gkeNetworkSnapshot {
	f.t.Helper()
	r, err := NewRuntime(credentialFunc(func(_ context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		if id != f.cluster.Identity.ConnectionID {
			f.t.Fatal("enrichment used another connection")
		}
		return f.tls.credential, nil
	}))
	if err != nil {
		f.t.Fatal(err)
	}
	r.clients[f.cluster.Identity.ConnectionID] = f.tls.client
	_, err = r.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{ConnectionID: f.cluster.Identity.ConnectionID}, []contracts.InventoryItem{{NativeType: clusterType, NativeID: f.cluster.Identity.NativeID, Normalized: f.cluster.Normalized}})
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot, err := plannedGKENetwork(f.cluster)
	if err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func (f *gkeNetworkFixture) request() (contracts.ActionRequest, plan.Result, governance.Contribution) {
	f.t.Helper()
	f.enrich()
	contribution, err := (&computeGroups{client: f.tls.client}).Contribute(context.Background(), "scope", f.assets)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := plan.Solve(plan.Input{Assets: f.assets, ResolvedAssetIDs: []asset.AssetID{f.cluster.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) > 0 {
		f.t.Fatalf("cluster plan %+v err=%v", result.Blockers, err)
	}
	request := contracts.ActionRequest{Asset: f.cluster, Action: "delete", IdempotencyKey: "cluster-cleanup"}
	for _, impact := range result.ImpactItems {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: f.value(string(impact.AssetID)), ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
	}
	return request, result, contribution
}

func TestGKEClusterPlanIncludesNativeNetworkAndPreservesReservations(t *testing.T) {
	f := newGKENetworkFixture(t)
	request, result, contribution := f.request()
	if len(result.Steps) != 1 || result.Steps[0].AssetID != f.cluster.ID || len(result.ImpactItems) != 15 || len(contribution.Unresolved) != 0 {
		t.Fatalf("incomplete cluster plan: %+v", result)
	}
	for _, impact := range result.ImpactItems {
		if impact.AssetID == "other-firewall" || impact.AssetID == "custom-firewall" || impact.AssetID == "foreign-frontend" {
			t.Fatal("name/description lookalike became owned")
		}
		if impact.AssetID == "static-ip" && impact.Expected != plan.ExpectedRetainShared {
			t.Fatal("pre-existing address was claimed for deletion")
		}
	}
	check, err := f.driver().Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("valid cluster preflight: %+v %v", check, err)
	}
	for _, retained := range []string{"frontend", "cluster-firewall", "pool"} {
		blocked, err := plan.Solve(plan.Input{Assets: f.assets, ResolvedAssetIDs: []asset.AssetID{f.cluster.ID}, LifecycleBindings: contribution.Bindings, RequestOptions: map[asset.AssetID]map[string]any{f.cluster.ID: {"retain_resources": []string{retained}}}})
		if err != nil || len(blocked.Blockers) == 0 {
			t.Fatalf("unsupported retention %s accepted: %+v %v", retained, blocked, err)
		}
	}
	encoded, _ := json.Marshal(f.cluster.Normalized[gkeNetworkKey])
	if strings.Contains(string(encoded), "PRIVATE_ANNOTATION") || strings.Contains(string(encoded), "PRIVATE_CLIENT_KEY") {
		t.Fatal("network inventory persisted secret content")
	}
}

func TestGKEClusterFinalizersNativeReadbackAndOrphanRecoverySurviveRestarts(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-controller", true: "orphan-recovery"}[orphan], func(t *testing.T) {
			f := newGKENetworkFixture(t)
			f.orphan = orphan
			request, _, _ := f.request()
			result, err := f.driver().Execute(context.Background(), request)
			if err != nil || result.Data["phase"] != "gke_network" || !slices.Equal(f.mutations, []string{"service"}) {
				t.Fatalf("initial cluster deletion=%+v writes=%v err=%v", result, f.mutations, err)
			}
			done := false
			for poll := 0; poll < 50; poll++ {
				encoded, _ := json.Marshal(result)
				var restored contracts.ActionResult
				_ = json.Unmarshal(encoded, &restored)
				wait, err := f.driver().Wait(context.Background(), request, restored)
				if err != nil {
					t.Fatalf("poll %d phase %s writes=%v: %v", poll, text(result.Data["phase"]), f.mutations, err)
				}
				if poll == 0 && (wait.Done || f.clusterDeleting) {
					t.Fatal("cluster deletion bypassed a pending Service finalizer")
				}
				if wait.Done {
					done = true
					break
				}
				if wait.Data != nil {
					result.Data = wait.Data
				}
			}
			if !done || f.clusterPolls < 3 {
				t.Fatalf("cluster did not verify complete absence: polls=%d writes=%v", f.clusterPolls, f.mutations)
			}
			for _, id := range []string{"static-ip", "custom-firewall", "other-firewall"} {
				if f.cloud[f.value(id).Identity.NativeID] == nil {
					t.Fatalf("retained resource %s lost", id)
				}
			}
			if f.resources[f.value("data").Identity.NativeID] == nil || f.resources[f.value("ip").Identity.NativeID] == nil {
				t.Fatal("retained node volume or reservation lost")
			}
			if orphan && len(f.operations) != 5 {
				t.Fatalf("orphan recovery skipped planned members: %v", f.operations)
			}
		})
	}
}

func TestGKEClusterRejectsChangedWorkloadsNetworkOwnershipAndImpacts(t *testing.T) {
	cases := map[string]func(*gkeNetworkFixture, *contracts.ActionRequest){
		"workload reincarnation": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			object(f.workloads["services/default/web"]["metadata"])["uid"] = "recreated-service"
		},
		"workload spec changed": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			object(f.workloads["services/default/web"]["spec"])["loadBalancerIP"] = "192.0.2.200"
		},
		"workload protected": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			object(f.workloads["services/default/web"]["metadata"])["labels"] = map[string]any{"steward_protected": "true"}
		},
		"new workload": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			f.workloads["services/default/new"] = kubeService("new")
		},
		"cloud reincarnation": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			f.cloud[f.value("frontend").Identity.NativeID]["id"] = "new-forwarding-rule"
		},
		"backend changed": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			f.addNetwork("new-backend", "TargetPool", "regions/us-central1/targetPools/new-backend", map[string]any{})
			f.cloud[f.value("frontend").Identity.NativeID]["target"] = f.cloud[f.value("new-backend").Identity.NativeID]["selfLink"]
		},
		"firewall foreign target": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			f.cloud[f.value("cluster-firewall").Identity.NativeID]["targetTags"] = []any{"foreign-node-tag"}
		},
		"node protected": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			f.live("vm")["labels"] = map[string]any{"steward_protected": "true"}
		},
		"missing snapshot":   func(_ *gkeNetworkFixture, r *contracts.ActionRequest) { delete(r.Asset.Normalized, gkeNetworkKey) },
		"missing impact":     func(_ *gkeNetworkFixture, r *contracts.ActionRequest) { r.LifecycleImpacts = r.LifecycleImpacts[1:] },
		"foreign controller": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) { r.LifecycleImpacts[0].ControllerID = "foreign" },
		"retain managed LB": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) {
			for i := range r.LifecycleImpacts {
				if r.LifecycleImpacts[i].Asset.ID == "frontend" {
					r.LifecycleImpacts[i].Delete = false
				}
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newGKENetworkFixture(t)
			request, _, _ := f.request()
			change(f, &request)
			check, err := f.driver().Preflight(context.Background(), request)
			if err == nil && check.Allowed {
				t.Fatalf("unsafe cluster preflight accepted: %+v", check)
			}
			if len(f.mutations) > 0 {
				t.Fatal("preflight mutated cloud resources")
			}
		})
	}
}

func TestGKEFrontendAssociationUsesAddressProtocolAndPort(t *testing.T) {
	service := gkeWorkload{Kind: "Service", data: map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": 80, "protocol": "TCP"}}}, "status": map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.0.2.100"}}}}}}
	for _, candidate := range []struct {
		ip, protocol, port string
		want               bool
	}{
		{"192.0.2.100", "TCP", "80-80", true}, {"192.0.2.100", "TCP", "443-443", false}, {"192.0.2.100", "UDP", "80-80", false}, {"192.0.2.101", "TCP", "80-80", false},
	} {
		if got := forwardingMatchesWorkload(map[string]any{"IPAddress": candidate.ip, "IPProtocol": candidate.protocol, "portRange": candidate.port}, service); got != candidate.want {
			t.Fatalf("frontend match %+v=%v", candidate, got)
		}
	}
	object(service.data["status"])["loadBalancer"] = map[string]any{"ingress": []any{map[string]any{"ip": "2600:1901:0:abcd::"}}}
	for _, candidate := range []struct {
		ip   string
		want bool
	}{{"2600:1901:0:abcd::/96", true}, {"2600:1901:0:abcd::", true}, {"2600:1901:0:ffff::/96", false}} {
		if forwardingMatchesWorkload(map[string]any{"IPAddress": candidate.ip, "IPProtocol": "TCP", "ports": []any{"80"}}, service) != candidate.want {
			t.Fatalf("IPv6 frontend %s", candidate.ip)
		}
	}
}

func TestGKECertificateOwnershipUsesVerifiedIngressFrontend(t *testing.T) {
	uid := "kubernetes-system-uid"
	lb := gkeContentHash(uid) + "-default-ingress-suffix12"
	urlMap := "k8s2-um-" + lb
	workload := gkeWorkload{Kind: "Ingress", Name: "ingress", Namespace: "default", data: map[string]any{"metadata": map[string]any{"annotations": map[string]any{"ingress.kubernetes.io/url-map": urlMap}}}}
	resources := map[string]gkeNetworkResource{"url-map": {ID: "//compute.googleapis.com/projects/sample-project/global/urlMaps/" + urlMap, Kind: "compute.googleapis.com/UrlMap", data: map[string]any{"description": `{"kubernetes.io/ingress-name":"default/ingress"}`}}}
	cert := map[string]any{"name": "k8s2-cr-" + gkeContentHash(uid) + "-" + gkeHash(lb, 16) + "-certhash"}
	if !gkeGeneratedCertificate(workload, cert, uid, resources) {
		t.Fatal("native Ingress certificate lost ownership")
	}
	for _, name := range []string{"pre-shared-certificate", "k8s2-cr-foreign-cluster-cert", "k8s2-cr-" + gkeContentHash(uid) + "-foreign-lb-cert"} {
		if gkeGeneratedCertificate(workload, map[string]any{"name": name}, uid, resources) {
			t.Fatalf("foreign certificate %s claimed", name)
		}
	}
	if gkeGeneratedCertificate(workload, cert, "new-system-uid", resources) || gkeGeneratedCertificate(workload, cert, uid, nil) {
		t.Fatal("certificate prefix alone authorized ownership")
	}
}

var _ contracts.InventoryBatchEnricher = (*Runtime)(nil)

// A full HTTP(S) frontend graph: native forwarding rules, proxies, URL map,
// backend service, health check, and a Service NEG with native cluster identity.
func (f *gkeNetworkFixture) addHTTPWorkload(gateway bool) string {
	f.t.Helper()
	uid := "kubernetes-system-uid"
	lb := gkeContentHash(uid) + "-default-edge-suffix12"
	urlMap, frontendName := "k8s2-um-"+lb, "k8s2-fr-"+lb
	certName := "k8s2-cr-" + gkeContentHash(uid) + "-" + gkeHash(lb, 16) + "-certhash"
	kind, api, resource := "Ingress", "networking.k8s.io/v1", "ingresses"
	annotations := map[string]any{"kubernetes.io/ingress.class": "gce", "ingress.kubernetes.io/url-map": urlMap, "ingress.kubernetes.io/forwarding-rule": frontendName}
	spec := map[string]any{"tls": []any{map[string]any{"secretName": "private-tls"}}}
	status := map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.0.2.110"}}}}
	if gateway {
		f.gateway = true
		kind, api, resource = "Gateway", "gateway.networking.k8s.io/v1", "gateways"
		annotations = map[string]any{}
		spec = map[string]any{"gatewayClassName": "gke-l7-global-external-managed", "listeners": []any{
			map[string]any{"name": "http", "protocol": "HTTP", "port": 80},
			map[string]any{"name": "https", "protocol": "HTTPS", "port": 443, "tls": map[string]any{"mode": "Terminate", "certificateRefs": []any{map[string]any{"name": "private-tls"}}}},
			map[string]any{"name": "shared", "protocol": "HTTPS", "port": 443, "hostname": "shared.example.com", "tls": map[string]any{"mode": "Terminate", "options": map[string]any{"networking.gke.io/pre-shared-certs": "pre-shared-certificate"}}},
		}}
		status = map[string]any{"addresses": []any{map[string]any{"type": "IPAddress", "value": "192.0.2.110"}}}
		certName = "gke-gateway-generated-secret-certificate"
	}
	key := resource + "/default/edge"
	f.workloads[key] = map[string]any{"kind": kind, "apiVersion": api, "metadata": map[string]any{"name": "edge", "namespace": "default", "uid": "edge-workload-uid", "resourceVersion": "98", "annotations": annotations}, "spec": spec, "status": status}
	f.addNetwork("neg", "NetworkEndpointGroup", "zones/us-central1-a/networkEndpointGroups/k8s-neg-web", map[string]any{"description": `{"cluster-uid":"kubernetes-system-uid","namespace":"default","service-name":"web","port":"80"}`, "network": "https://compute.googleapis.com/compute/v1/projects/sample-project/global/networks/cluster-network", "networkEndpointType": "GCE_VM_IP_PORT"})
	object(f.workloads["services/default/web"]["metadata"])["annotations"] = map[string]any{"cloud.google.com/neg": `{"ingress":true}`, "cloud.google.com/neg-status": `{"network_endpoint_groups":{"80":"k8s-neg-web"},"zones":["us-central1-a"]}`}
	f.workloadResources["services/default/web"] = []string{"neg"}
	link := func(id string) any { return f.cloud[f.value(id).Identity.NativeID]["selfLink"] }
	f.addNetwork("http-hc", "HealthCheck", "global/healthChecks/web-hc", map[string]any{"type": "HTTP"})
	f.addNetwork("backend", "BackendService", "global/backendServices/web-backend", map[string]any{"backends": []any{map[string]any{"group": link("neg")}}, "healthChecks": []any{link("http-hc")}})
	f.addNetwork("url-map", "UrlMap", "global/urlMaps/"+urlMap, map[string]any{"defaultService": link("backend"), "description": `{"kubernetes.io/ingress-name":"default/edge"}`})
	f.addNetwork("generated-cert", "SslCertificate", "global/sslCertificates/"+certName, map[string]any{"type": "SELF_MANAGED"})
	f.addNetwork("shared-cert", "SslCertificate", "global/sslCertificates/pre-shared-certificate", map[string]any{"type": "SELF_MANAGED"})
	f.addNetwork("https-proxy", "TargetHttpsProxy", "global/targetHttpsProxies/edge-https-proxy", map[string]any{"urlMap": link("url-map"), "sslCertificates": []any{link("generated-cert"), link("shared-cert")}})
	f.addNetwork("http-proxy", "TargetHttpProxy", "global/targetHttpProxies/edge-http-proxy", map[string]any{"urlMap": link("url-map")})
	f.addNetwork("http-frontend", "GlobalForwardingRule", "global/forwardingRules/"+frontendName, map[string]any{"IPAddress": "192.0.2.110", "IPProtocol": "TCP", "portRange": "80-80", "target": link("http-proxy")})
	f.addNetwork("https-frontend", "GlobalForwardingRule", "global/forwardingRules/edge-https", map[string]any{"IPAddress": "192.0.2.110", "IPProtocol": "TCP", "portRange": "443-443", "target": link("https-proxy")})
	f.workloadResources[key] = []string{"http-hc", "backend", "url-map", "generated-cert", "https-proxy", "http-proxy", "http-frontend", "https-frontend"}
	if !gateway {
		f.addNetwork("generated-address", "GlobalAddress", "global/addresses/"+frontendName, map[string]any{"address": "192.0.2.110", "users": []any{link("http-frontend"), link("https-frontend")}})
		f.workloadResources[key] = append(f.workloadResources[key], "generated-address")
	}
	return key
}

func TestGKEHTTPWorkloadPlanAndCleanup(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, orphan := range []bool{false, true} {
			t.Run(fmt.Sprintf("gateway=%v/orphan=%v", gateway, orphan), func(t *testing.T) {
				f := newGKENetworkFixture(t)
				f.orphan = orphan
				f.addHTTPWorkload(gateway)
				request, result, _ := f.request()
				for _, id := range []string{"generated-cert", "shared-cert", "neg", "backend", "http-frontend"} {
					found := false
					for _, impact := range result.ImpactItems {
						if impact.AssetID != asset.AssetID(id) {
							continue
						}
						found = true
						if (impact.Expected == plan.ExpectedDelegatedDelete) != (id != "shared-cert") {
							t.Fatalf("wrong impact %s: %+v", id, impact)
						}
					}
					if !found {
						t.Fatalf("missing %s impact", id)
					}
				}
				check, err := f.driver().Preflight(context.Background(), request)
				if err != nil || !check.Allowed {
					t.Fatalf("HTTP preflight: %+v %v", check, err)
				}
				actionResult, err := f.driver().Execute(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				done := false
				for poll := 0; poll < 120; poll++ {
					encoded, _ := json.Marshal(actionResult)
					var restored contracts.ActionResult
					_ = json.Unmarshal(encoded, &restored)
					wait, err := f.driver().Wait(context.Background(), request, restored)
					if err != nil {
						t.Fatalf("phase %v: %v", restored.Data, err)
					}
					if wait.Done {
						done = true
						break
					}
					if wait.Data != nil {
						actionResult.Data = wait.Data
					}
				}
				if !done {
					t.Fatal("HTTP controller cleanup never completed")
				}
				if f.cloud[f.value("shared-cert").Identity.NativeID] == nil {
					t.Fatal("pre-shared TLS certificate deleted")
				}
				first := "ingress"
				if gateway {
					first = "gateway"
				}
				if len(f.mutations) < 3 || f.mutations[0] != first || f.mutations[1] != "service" {
					t.Fatalf("wrong finalizer order: %v", f.mutations)
				}
				for _, id := range []string{"http-frontend", "https-frontend", "backend", "neg", "generated-cert"} {
					if f.cloud[f.value(id).Identity.NativeID] != nil {
						t.Fatalf("leftover %s", id)
					}
				}
			})
		}
	}
}

func TestGKENetworkRejectsIncompleteSnapshotsAndCleanupEscapes(t *testing.T) {
	for name, change := range map[string]func(*gkeNetworkFixture, *contracts.ActionRequest){
		"duplicate cloud resource": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) {
			s := object(r.Asset.Normalized[gkeNetworkKey])
			resources := array(s["resources"])
			s["resources"] = append(resources, resources[0])
		},
		"invalid cleanup phase": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) {
			object(array(object(r.Asset.Normalized[gkeNetworkKey])["resources"])[0])["phase"] = "anything"
		},
		"injected node": func(f *gkeNetworkFixture, r *contracts.ActionRequest) {
			s := object(r.Asset.Normalized[gkeNetworkKey])
			s["resources"] = append(array(s["resources"]), map[string]any{"kind": instanceType, "id": f.value("vm").Identity.NativeID, "uid": text(f.live("vm")["id"]), "delete": true, "phase": "workload"})
		},
		"injected firewall": func(f *gkeNetworkFixture, r *contracts.ActionRequest) {
			v := f.value("other-firewall")
			s := object(r.Asset.Normalized[gkeNetworkKey])
			s["resources"] = append(array(s["resources"]), map[string]any{"kind": v.Identity.NativeType, "id": v.Identity.NativeID, "uid": v.Normalized["id"], "delete": true, "phase": "cluster"})
			r.LifecycleImpacts = append(r.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: r.Asset.ID, Delete: true})
		},
		"invalid Kubernetes API": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) {
			object(array(object(r.Asset.Normalized[gkeNetworkKey])["workloads"])[0])["api"] = "../../../foreign"
		},
		"duplicate workload": func(_ *gkeNetworkFixture, r *contracts.ActionRequest) {
			s := object(r.Asset.Normalized[gkeNetworkKey])
			workloads := array(s["workloads"])
			s["workloads"] = append(workloads, workloads[0])
		},
		"missing retained address": func(f *gkeNetworkFixture, _ *contracts.ActionRequest) {
			delete(f.cloud, f.value("static-ip").Identity.NativeID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newGKENetworkFixture(t)
			r, _, _ := f.request()
			change(f, &r)
			check, err := f.driver().Preflight(context.Background(), r)
			if err == nil && check.Allowed {
				t.Fatal("invalid snapshot accepted")
			}
			if len(f.mutations) > 0 {
				t.Fatal("validation mutated resources")
			}
		})
	}
}

func TestGKENetworkFailuresAndDriftDuringResume(t *testing.T) {
	for _, status := range []int{403, 409, 410, 500} {
		t.Run(fmt.Sprintf("kubernetes-delete-%d", status), func(t *testing.T) {
			f := newGKENetworkFixture(t)
			request, _, _ := f.request()
			f.fault = func(r *http.Request) (*http.Response, bool) {
				if r.Method == "DELETE" && r.URL.Path == "/api/v1/namespaces/default/services/web" {
					return apiResponse(r, status, `{"kind":"Status","reason":"Denied"}`), true
				}
				return nil, false
			}
			if _, err := f.driver().Execute(context.Background(), request); err == nil {
				t.Fatal("Kubernetes delete failure ignored")
			}
			if len(f.mutations) > 0 {
				t.Fatal("cluster/cloud cleanup followed failed finalizer request")
			}
		})
	}
	for _, scenario := range []string{"new workload", "late node protection", "late cloud protection", "operation failed", "foreign operation", "forged after cluster"} {
		t.Run(scenario, func(t *testing.T) {
			f := newGKENetworkFixture(t)
			f.orphan = true
			request, _, _ := f.request()
			result, err := f.driver().Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "new workload" {
				f.workloads["services/default/new"] = kubeService("new")
			}
			if scenario == "late node protection" {
				f.live("vm")["labels"] = map[string]any{"steward_protected": "true"}
			}
			before := len(f.mutations)
			if scenario == "new workload" || scenario == "late node protection" {
				if _, err = f.driver().Wait(context.Background(), request, result); err == nil {
					t.Fatal("resume accepted drift")
				}
				if len(f.mutations) != before {
					t.Fatal("resume mutated after drift")
				}
				return
			}
			for poll := 0; poll < 10 && result.Data["phase"] != "gke_network_cleanup"; poll++ {
				wait, err := f.driver().Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				if wait.Data != nil {
					result.Data = wait.Data
				}
			}
			if result.Data["phase"] != "gke_network_cleanup" {
				t.Fatal("no recoverable cleanup state")
			}
			if scenario == "late cloud protection" {
				for id, data := range f.cloud {
					if id != text(result.Data["resource"]) && (id == f.value("legacy-hc").Identity.NativeID || id == f.value("service-firewall").Identity.NativeID) {
						data["labels"] = map[string]any{"steward_protected": "true"}
					}
				}
			} else if scenario == "operation failed" {
				f.fault = func(r *http.Request) (*http.Response, bool) {
					if strings.Contains(r.URL.Path, "/operations/") {
						return apiResponse(r, 200, `{"status":"DONE","error":{"errors":[{"code":"RESOURCE_IN_USE_BY_ANOTHER_RESOURCE"}]}}`), true
					}
					return nil, false
				}
			} else if scenario == "foreign operation" {
				result.Data["operation"] = "https://foreign.example/operations/steal-token"
			} else {
				result.Data["after_cluster"] = true
			}
			before = len(f.mutations)
			failed := false
			for poll := 0; poll < 10; poll++ {
				wait, err := f.driver().Wait(context.Background(), request, result)
				if err != nil {
					failed = true
					break
				}
				if wait.Done {
					t.Fatal("unsafe state completed")
				}
				if wait.Data != nil {
					result.Data = wait.Data
				}
			}
			if scenario != "forged after cluster" && !failed {
				t.Fatal("unsafe resume did not fail")
			}
			if len(f.mutations) != before {
				t.Fatalf("new writes after fault: %v", f.mutations[before:])
			}
		})
	}
}

func TestGKENetworkNativeAncillaryOwnership(t *testing.T) {
	f := newGKENetworkFixture(t)
	f.gke[f.cluster.Identity.NativeID]["networkConfig"] = map[string]any{"disableL4LbFirewallReconciliation": true}
	uid := "kubernetes-system-uid"
	network := "https://compute.googleapis.com/compute/v1/projects/sample-project/global/networks/cluster-network"
	for id, name := range map[string]string{"shared-hc-firewall": "k8s2-" + gkeContentHash(uid) + "-l4-shared-hc-fw", "ipv6-firewall": "gke-abc12345-ipv6-all", "v2-service-firewall": "k8s2-" + gkeContentHash(uid) + "-default-web-" + gkeContentHash(gkeContentHash(uid)+";default;web")} {
		f.addNetwork(id, "Firewall", "global/firewalls/"+name, map[string]any{"network": network, "targetTags": []any{"gke-test-abc12345-node"}, "description": `{"networking.gke.io/service-name":"default/web"}`})
	}
	_, result, _ := f.request()
	for _, id := range []string{"shared-hc-firewall", "ipv6-firewall", "v2-service-firewall"} {
		found := false
		for _, impact := range result.ImpactItems {
			if impact.AssetID == asset.AssetID(id) {
				found = impact.Expected == plan.ExpectedDelegatedDelete
			}
		}
		if !found {
			t.Fatalf("controller cleanup missing %s despite native ownership", id)
		}
	}
}

func TestGKEFirewallOwnershipWithZeroNodesUsesNativeInstanceTemplate(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			f := newGKENetworkFixture(t)
			tag := "gke-test-abc12345-node"
			if !valid {
				tag = "gke-another-cluster-abc12345-node"
			}
			f.addNetwork("template", "InstanceTemplate", "global/instanceTemplates/zero-node-pool-template", map[string]any{"properties": map[string]any{"tags": map[string]any{"items": []any{tag, "custom-shared-tag"}}}})
			manager := groupCopy(f.live("mig"))
			manager["instanceTemplate"] = f.cloud[f.value("template").Identity.NativeID]["selfLink"]
			resources := map[string]gkeNetworkResource{}
			err := f.tls.client.gkeNetworkAncillary(context.Background(), f.cluster, f.gke[f.cluster.Identity.NativeID], []gkeMember{{id: f.value("mig").Identity.NativeID, kind: managerType, data: manager}}, "kubernetes-system-uid", nil, resources)
			if !valid {
				if err == nil {
					t.Fatal("foreign template tags proved cluster ownership")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !resources[f.value("cluster-firewall").Identity.NativeID].Delete {
				t.Fatal("zero-node pool lost native firewall ownership")
			}
			if _, ok := resources[f.value("node-route").Identity.NativeID]; ok {
				t.Fatal("route inferred without a live node")
			}
		})
	}
}

func TestGKEGatewaySecretCertificateRequiresExactProxyReference(t *testing.T) {
	f := newGKENetworkFixture(t)
	key := f.addHTTPWorkload(true)
	snapshot := f.enrich()
	resources := map[string]gkeNetworkResource{}
	for _, r := range snapshot.Resources {
		r.data = f.cloud[r.ID]
		resources[r.ID] = r
	}
	w := gkeWorkload{Kind: "Gateway", data: f.workloads[key]}
	cert := resources[f.value("generated-cert").Identity.NativeID]
	if !f.tls.client.gatewaySecretCertificate(w, cert, resources) {
		t.Fatal("native Secret certificate not recognized")
	}
	delete(resources, f.value("https-proxy").Identity.NativeID)
	if f.tls.client.gatewaySecretCertificate(w, cert, resources) {
		t.Fatal("Secret existence claimed an unrelated certificate")
	}
}

func TestGKEL4FirewallNamesMatchPinnedControllerExamples(t *testing.T) {
	// Expected names from ingress-gce 93bdce86, pkg/utils/namer/l4_namer_test.go.
	for _, test := range []struct {
		namespace, name string
		expected        []string
	}{
		{"namespace", "name", []string{"k8s2-7kpbhpki-namespace-name-956p2p7x", "k8s2-7kpbhpki-namespace-name-956p2p7x-fw", "k8s2-7kpbhpki-namespace-name-956p2p7x-ipv6", "k8s2-7kpbhpki-namespace-name-956p2p7x-deny"}},
		{"012345678901234567890123456789012345678901234567890123456789abc", "012345678901234567890123456789012345678901234567890123456789pqr", []string{"k8s2-7kpbhpki-01234567890123456789-0123456789012345678-hwm400mg", "k8s2-7kpbhpki-01234567890123456789-0123456789012345678--fw-ipv6", "k8s2-7kpbhpki-012345678901234-01234567890123-hwm400mg-deny-ipv6"}},
	} {
		w := gkeWorkload{Namespace: test.namespace, Name: test.name}
		for _, name := range test.expected {
			if !gkeL4FirewallName(name, w, "kubesystem-uid1") {
				t.Fatalf("controller name %s rejected", name)
			}
		}
		for _, name := range []string{"k8s2-7kpbhpki-arbitrary-user-firewall", test.expected[0] + "-custom"} {
			if gkeL4FirewallName(name, w, "kubesystem-uid1") {
				t.Fatalf("lookalike %s authorized", name)
			}
		}
	}
}
