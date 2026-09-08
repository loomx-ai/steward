package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const gkeNetworkKey = "_gke_network"
const negType = "compute.googleapis.com/NetworkEndpointGroup"

type gkeWorkload struct {
	API       string `json:"api"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Hash      string `json:"hash"`
	data      map[string]any
}

func (w gkeWorkload) collection() kubernetesCollection {
	return kubernetesCollection{w.API, w.Resource, w.Kind}
}
func (w gkeWorkload) key() string { return w.Resource + "/" + w.Namespace + "/" + w.Name }

type gkeNetworkResource struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	UID    string `json:"uid"`
	Delete bool   `json:"delete"`
	Phase  string `json:"phase"`
	data   map[string]any
}

type gkeNetworkSnapshot struct {
	ClusterUID string               `json:"cluster_uid"`
	SystemUID  string               `json:"system_uid"`
	Workloads  []gkeWorkload        `json:"workloads"`
	Resources  []gkeNetworkResource `json:"resources"`
}

func (s gkeNetworkSnapshot) payload() (map[string]any, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(encoded, &result)
	return result, err
}

func plannedGKENetwork(value asset.Asset) (gkeNetworkSnapshot, error) {
	var result gkeNetworkSnapshot
	encoded, err := json.Marshal(value.Normalized[gkeNetworkKey])
	if err != nil || json.Unmarshal(encoded, &result) != nil || result.ClusterUID == "" || result.ClusterUID != text(value.Normalized["id"]) || result.SystemUID == "" || result.Workloads == nil || result.Resources == nil {
		return result, groupDenied("gke_network_inventory_required")
	}
	seen := map[string]bool{}
	for _, resource := range result.Resources {
		if resource.ID == "" || resource.UID == "" || seen[resource.ID] || !gkeNetworkKind(resource.Kind) || (resource.Phase != "workload" && resource.Phase != "cluster") {
			return result, groupDenied("gke_network_plan_invalid")
		}
		seen[resource.ID] = true
	}
	seen = map[string]bool{}
	for _, workload := range result.Workloads {
		collection := workload.collection()
		valid := collection == serviceCollection || collection == ingressCollection || (collection.resource == "gateways" && collection.kind == "Gateway" && (collection.api == "gateway.networking.k8s.io/v1" || collection.api == "gateway.networking.k8s.io/v1beta1"))
		if !valid || !kubernetesName(workload.Namespace, 63) || strings.Contains(workload.Namespace, ".") || !kubernetesName(workload.Name, 253) || workload.UID == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(workload.Hash) || seen[workload.key()] {
			return result, groupDenied("gke_workload_plan_invalid")
		}
		seen[workload.key()] = true
	}
	return result, nil
}

func gkeNetworkKind(kind string) bool {
	switch kind {
	case "compute.googleapis.com/SslPolicy", "compute.googleapis.com/SecurityPolicy", "compute.googleapis.com/BackendBucket":
		return true
	case "compute.googleapis.com/ForwardingRule", "compute.googleapis.com/GlobalForwardingRule", "compute.googleapis.com/TargetHttpProxy", "compute.googleapis.com/TargetHttpsProxy", "compute.googleapis.com/TargetTcpProxy", "compute.googleapis.com/TargetSslProxy", "compute.googleapis.com/UrlMap", "compute.googleapis.com/BackendService", "compute.googleapis.com/RegionBackendService", "compute.googleapis.com/HealthCheck", "compute.googleapis.com/HttpHealthCheck", "compute.googleapis.com/HttpsHealthCheck", "compute.googleapis.com/TargetPool", negType, instanceGroupType, "compute.googleapis.com/SslCertificate", "compute.googleapis.com/Address", "compute.googleapis.com/GlobalAddress", "compute.googleapis.com/Firewall", "compute.googleapis.com/Route":
		return true
	}
	return false
}

func workloadHash(data map[string]any) string {
	meta := object(data["metadata"])
	annotations := map[string]any{}
	for key, value := range object(meta["annotations"]) {
		// Status annotations change during normal controller reconciliation.
		if strings.HasPrefix(key, "ingress.kubernetes.io/") || key == "cloud.google.com/neg-status" || key == "networking.gke.io/backend-service" || key == "networking.gke.io/target-pool" {
			continue
		}
		annotations[key] = value
	}
	encoded, _ := json.Marshal(map[string]any{"spec": data["spec"], "labels": meta["labels"], "annotations": annotations})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (k *kubernetesClient) networkWorkloads(ctx context.Context) (string, []gkeWorkload, error) {
	namespace, err := k.request(ctx, "GET", "/api/v1/namespaces/kube-system", nil, nil)
	if err != nil {
		return "", nil, err
	}
	meta := object(namespace.Data["metadata"])
	uid := text(meta["uid"])
	if namespace.Data["kind"] != "Namespace" || meta["name"] != "kube-system" || uid == "" {
		return "", nil, fmt.Errorf("Kubernetes system namespace identity is incomplete")
	}
	collections := []kubernetesCollection{serviceCollection, ingressCollection}
	if gateway, exists, err := k.gatewayCollection(ctx); err != nil {
		return "", nil, err
	} else if exists {
		collections = append(collections, gateway)
	}
	result := []gkeWorkload{}
	for _, collection := range collections {
		items, err := k.list(ctx, collection)
		if err != nil {
			return "", nil, err
		}
		for _, item := range items {
			meta := object(item["metadata"])
			annotations := object(meta["annotations"])
			if collection.kind == "Service" && object(item["spec"])["type"] != "LoadBalancer" && annotations["cloud.google.com/neg"] == nil && annotations["cloud.google.com/neg-status"] == nil {
				continue
			}
			if collection.kind == "Ingress" {
				class := text(annotations["kubernetes.io/ingress.class"])
				if class != "" && class != "gce" && class != "gce-internal" {
					continue
				}
				if class == "" && text(object(item["spec"])["ingressClassName"]) != "" {
					continue
				}
			}
			if collection.kind == "Gateway" && !strings.HasPrefix(text(object(item["spec"])["gatewayClassName"]), "gke-l7-") {
				continue
			}
			result = append(result, gkeWorkload{API: collection.api, Resource: collection.resource, Kind: collection.kind, Namespace: text(meta["namespace"]), Name: text(meta["name"]), UID: text(meta["uid"]), Hash: workloadHash(item), data: item})
		}
	}
	// Frontends disappear before Services/NEGs, while workload controllers and
	// their nodes remain alive. Delete all objects at one priority before waiting.
	priority := map[string]int{"Gateway": 0, "Ingress": 1, "Service": 2}
	sort.Slice(result, func(i, j int) bool {
		if priority[result[i].Kind] != priority[result[j].Kind] {
			return priority[result[i].Kind] < priority[result[j].Kind]
		}
		return result[i].key() < result[j].key()
	})
	return uid, result, nil
}

func (c *client) computeList(ctx context.Context, operationID, path string, parameters map[string]any) ([]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok {
		return nil, fmt.Errorf("GCP native list operation is unavailable")
	}
	parameters = cloneParameters(parameters)
	parameters["project"] = c.project
	return c.nativeList(ctx, operation, parameters, path)
}

func workloadIPs(w gkeWorkload) []string {
	var result []string
	status := object(w.data["status"])
	if w.Kind == "Gateway" {
		for _, raw := range array(status["addresses"]) {
			address := object(raw)
			if address["type"] == "IPAddress" {
				result = append(result, text(address["value"]))
			}
		}
	} else {
		for _, raw := range array(object(status["loadBalancer"])["ingress"]) {
			if ip := text(object(raw)["ip"]); ip != "" {
				result = append(result, ip)
			}
		}
	}
	return result
}

func forwardingMatchesWorkload(rule map[string]any, workload gkeWorkload) bool {
	matchedIP := false
	for _, value := range workloadIPs(workload) {
		ip, err := netip.ParseAddr(value)
		if err != nil {
			continue
		}
		literal := text(rule["IPAddress"])
		if prefix, err := netip.ParsePrefix(literal); err == nil {
			matchedIP = matchedIP || prefix.Contains(ip)
		} else if other, err := netip.ParseAddr(literal); err == nil {
			matchedIP = matchedIP || other == ip
		}
	}
	if !matchedIP {
		return false
	}
	ports := map[string]string{}
	spec := object(workload.data["spec"])
	switch workload.Kind {
	case "Service":
		for _, raw := range array(spec["ports"]) {
			port := object(raw)
			protocol := text(port["protocol"])
			if protocol == "" {
				protocol = "TCP"
			}
			ports[fmt.Sprint(port["port"])] = protocol
		}
	case "Ingress":
		ports["80"], ports["443"] = "TCP", "TCP"
	case "Gateway":
		for _, raw := range array(spec["listeners"]) {
			listener := object(raw)
			protocol := text(listener["protocol"])
			if protocol == "HTTP" || protocol == "HTTPS" || protocol == "TLS" {
				protocol = "TCP"
			}
			ports[fmt.Sprint(listener["port"])] = protocol
		}
	}
	for port, protocol := range ports {
		if rule["IPProtocol"] != protocol && rule["IPProtocol"] != "L3_DEFAULT" {
			continue
		}
		if rule["allPorts"] == true {
			return true
		}
		for _, raw := range array(rule["ports"]) {
			if text(raw) == port {
				return true
			}
		}
		rangeParts := strings.Split(text(rule["portRange"]), "-")
		start, err := strconv.Atoi(rangeParts[0])
		if err != nil {
			continue
		}
		end := start
		if len(rangeParts) == 2 {
			end, _ = strconv.Atoi(rangeParts[1])
		}
		number, err := strconv.Atoi(port)
		if err == nil && number >= start && number <= end {
			return true
		}
	}
	return false
}

func description(data map[string]any) map[string]any {
	var result map[string]any
	_ = json.Unmarshal([]byte(text(data["description"])), &result)
	return result
}

// Frontend identity is the live Kubernetes published address AND listener,
// joined to native Compute links. A resource-name prefix alone is never a root.
func (c *client) gkeNetwork(ctx context.Context, root asset.Asset, live map[string]any, nodes []gkeMember) (gkeNetworkSnapshot, error) {
	result := gkeNetworkSnapshot{ClusterUID: text(live["id"]), Resources: []gkeNetworkResource{}}
	networks := references(c, live)["compute.googleapis.com/Network"]
	if len(networks) != 1 {
		return result, groupDenied("gke_network_ownership_unverified")
	}
	k, err := c.kubernetes(ctx, root.Identity.NativeID, result.ClusterUID)
	if err != nil {
		return result, err
	}
	defer k.http.CloseIdleConnections()
	result.SystemUID, result.Workloads, err = k.networkWorkloads(ctx)
	if err != nil {
		return result, err
	}
	region := regionOf(last(strings.Split(root.Identity.NativeID, "/clusters/")[0]))
	params := map[string]any{"region": region}
	regional, err := c.computeList(ctx, "compute.forwardingRules.list", "items", params)
	if err != nil {
		return result, err
	}
	global, err := c.computeList(ctx, "compute.globalForwardingRules.list", "items", nil)
	if err != nil {
		return result, err
	}
	knownNodes := map[string]bool{}
	for _, node := range nodes {
		knownNodes[node.id] = true
	}
	resources := map[string]gkeNetworkResource{}
	var walk func(string, string) error
	walk = func(kind, id string) error {
		switch kind {
		case "compute.googleapis.com/Network", "compute.googleapis.com/Subnetwork", "compute.googleapis.com/Instance", "compute.googleapis.com/Disk", "compute.googleapis.com/RegionDisk":
			return nil
		}
		if knownNodes[id] {
			return nil
		}
		if !gkeNetworkKind(kind) {
			return groupDenied("gke_network_resource_kind_unresolved")
		}
		if _, seen := resources[id]; seen {
			return nil
		}
		data, err := c.nativeGet(ctx, kind, id)
		if err != nil {
			return err
		}
		if text(data["id"]) == "" {
			return fmt.Errorf("GKE network resource has no immutable identity")
		}
		resource := gkeNetworkResource{ID: id, Kind: kind, UID: text(data["id"]), Delete: true, Phase: "workload", data: data}
		switch kind {
		case negType:
			if description(data)["cluster-uid"] != result.SystemUID || c.canonicalName(text(data["network"])) != networks[0] {
				return groupDenied("gke_network_endpoint_group_ownership_unverified")
			}
		case instanceGroupType:
			// GKE's NodePort ingress groups can outlive the workload frontends;
			// their membership is verified separately before controller deletion.
			members, err := c.unmanagedGroupMembers(ctx, id)
			if err != nil {
				return err
			}
			if len(members) == 0 {
				return groupDenied("gke_instance_group_ownership_unverified")
			}
			for _, member := range members {
				if !knownNodes[member] {
					return groupDenied("gke_instance_group_ownership_unverified")
				}
			}
			resource.Phase = "cluster"
		case "compute.googleapis.com/SslCertificate", "compute.googleapis.com/SslPolicy", "compute.googleapis.com/SecurityPolicy", "compute.googleapis.com/BackendBucket":
			// Classify certificates after the complete frontend chain is available.
			resource.Delete = false
		}
		resources[id] = resource
		if kind == "compute.googleapis.com/BackendBucket" {
			return nil // Its user-owned storage bucket is not a GKE deletion impact.
		}
		for target, ids := range references(c, data) {
			for _, child := range ids {
				if err := walk(target, child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, workload := range result.Workloads {
		if text(object(workload.data["metadata"])["deletionTimestamp"]) != "" {
			continue
		}
		matched := false
		for _, rule := range append(slices.Clone(regional), global...) {
			network := c.canonicalName(text(rule["network"]))
			if network != "" && network != networks[0] {
				continue
			}
			if strings.HasPrefix(text(rule["loadBalancingScheme"]), "INTERNAL") && network == "" {
				subnetID, err := c.computeID(text(rule["subnetwork"]), "compute.googleapis.com/Subnetwork")
				if err != nil {
					return result, groupDenied("gke_frontend_network_unresolved")
				}
				subnet, err := c.nativeGet(ctx, "compute.googleapis.com/Subnetwork", subnetID)
				if err != nil {
					return result, err
				}
				if c.canonicalName(text(subnet["network"])) != networks[0] {
					continue
				}
			}
			if !forwardingMatchesWorkload(rule, workload) {
				continue
			}
			kind := "compute.googleapis.com/ForwardingRule"
			if strings.Contains(text(rule["selfLink"]), "/global/") {
				kind = "compute.googleapis.com/GlobalForwardingRule"
			}
			id, err := c.computeID(text(rule["selfLink"]), kind)
			if err != nil {
				return result, err
			}
			if err := walk(kind, id); err != nil {
				return result, err
			}
			matched = true
		}
		if len(workloadIPs(workload)) > 0 && !matched {
			return result, groupDenied("gke_load_balancer_frontend_unresolved")
		}
		if workload.Kind == "Service" {
			var status struct {
				Groups map[string]string `json:"network_endpoint_groups"`
				Zones  []string          `json:"zones"`
			}
			annotation := text(object(object(workload.data["metadata"])["annotations"])["cloud.google.com/neg-status"])
			if annotation != "" {
				if json.Unmarshal([]byte(annotation), &status) != nil || len(status.Groups) == 0 || len(status.Zones) == 0 {
					return result, fmt.Errorf("invalid GKE NEG status")
				}
				for _, zone := range status.Zones {
					for _, name := range status.Groups {
						id := "//compute.googleapis.com/projects/" + c.project + "/zones/" + zone + "/networkEndpointGroups/" + name
						if err := walk(negType, id); err != nil {
							return result, err
						}
					}
				}
			}
		}
	}
	for id, resource := range resources {
		if resource.Kind != "compute.googleapis.com/SslCertificate" {
			continue
		}
		for _, workload := range result.Workloads {
			if gkeGeneratedCertificate(workload, resource.data, result.SystemUID, resources) || c.gatewaySecretCertificate(workload, resource, resources) {
				resource.Delete = true
			}
		}
		resources[id] = resource
	}
	if err := c.gkeNetworkAncillary(ctx, root, live, nodes, result.SystemUID, result.Workloads, resources); err != nil {
		return result, err
	}
	for _, resource := range resources {
		result.Resources = append(result.Resources, resource)
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	return result, nil
}

func (c *client) unmanagedGroupMembers(ctx context.Context, id string) ([]string, error) {
	kind, _ := findType(instanceGroupType)
	read, params, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	op := strings.TrimSuffix(read.ID, "get") + "listInstances"
	metadata, _ := providerData()
	operation, ok := metadata.catalog.Operation(op)
	if !ok {
		return nil, fmt.Errorf("instance group membership API is unavailable")
	}
	items, err := c.nativeList(ctx, operation, params, "items")
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, item := range items {
		member, err := c.computeID(text(item["instance"]), instanceType)
		if err != nil {
			return nil, err
		}
		result = append(result, member)
	}
	return result, nil
}

// Ancillary resources require the live network and node tags from verified GKE
// members. Similar names in another network or targeting foreign nodes do not
// become deletion impacts. Address reservations are retained unless native
// ingress ownership proves they were dynamically allocated by that controller.
func (c *client) gkeNetworkAncillary(ctx context.Context, root asset.Asset, live map[string]any, nodes []gkeMember, systemUID string, workloads []gkeWorkload, resources map[string]gkeNetworkResource) error {
	networks := references(c, live)["compute.googleapis.com/Network"]
	if len(networks) != 1 {
		return groupDenied("gke_network_ownership_unverified")
	}
	network := networks[0]
	tags, nativeNodes := map[string]bool{}, map[string]bool{}
	nodeTagPattern := regexp.MustCompile(`^gke-` + regexp.QuoteMeta(text(live["name"])) + `-[a-z0-9]{8}-node$`)
	addTags := func(data map[string]any) {
		for _, raw := range array(object(data["tags"])["items"]) {
			if tag := text(raw); nodeTagPattern.MatchString(tag) {
				tags[tag] = true
			}
		}
	}
	templates := map[string]bool{}
	for _, node := range nodes {
		if node.kind == instanceType {
			nativeNodes[node.id] = true
			addTags(node.data)
		}
		if node.kind == managerType {
			for _, version := range append([]any{node.data}, array(node.data["versions"])...) {
				link := text(object(version)["instanceTemplate"])
				if link == "" || templates[link] {
					continue
				}
				templates[link] = true
				id, err := c.computeID(link, "compute.googleapis.com/InstanceTemplate")
				if err != nil {
					return err
				}
				data, err := c.nativeGet(ctx, "compute.googleapis.com/InstanceTemplate", id)
				if err != nil {
					return err
				}
				addTags(object(data["properties"]))
			}
		}
	}
	add := func(kind string, data map[string]any, deletes bool, phase string) error {
		id, err := c.computeID(text(data["selfLink"]), kind)
		if err != nil {
			return err
		}
		if text(data["id"]) == "" {
			return fmt.Errorf("GKE ancillary resource has no immutable identity")
		}
		resources[id] = gkeNetworkResource{ID: id, Kind: kind, UID: text(data["id"]), Delete: deletes, Phase: phase, data: data}
		return nil
	}
	clusterFirewall := regexp.MustCompile(`^gke-` + regexp.QuoteMeta(text(live["name"])) + `-([a-z0-9]{8})-(master|vms|all|inkubelet|exkubelet)$`)
	firewalls, err := c.computeList(ctx, "compute.firewalls.list", "items", nil)
	if err != nil {
		return err
	}
	for _, firewall := range firewalls {
		if c.canonicalName(text(firewall["network"])) != network {
			continue
		}
		targets := array(firewall["targetTags"])
		allTargets := len(targets) > 0
		for _, target := range targets {
			allTargets = allTargets && tags[text(target)]
		}
		name := text(firewall["name"])
		match := clusterFirewall.FindStringSubmatch(name)
		if len(match) > 0 {
			expected := "gke-" + text(live["name"]) + "-" + match[1] + "-node"
			if !tags[expected] {
				if len(tags) == 0 {
					return groupDenied("gke_firewall_ownership_unresolved")
				}
				continue
			}
			if !allTargets {
				return groupDenied("gke_firewall_targets_changed")
			}
			if err := add("compute.googleapis.com/Firewall", firewall, true, "cluster"); err != nil {
				return err
			}
			continue
		}
		if !allTargets {
			continue
		}
		owned := false
		desc := description(firewall)
		for _, workload := range workloads {
			if workload.Kind != "Service" {
				continue
			}
			service := workload.Namespace + "/" + workload.Name
			legacy := "a" + strings.ReplaceAll(workload.UID, "-", "")
			if len(legacy) > 32 {
				legacy = legacy[:32]
			}
			generated := name == "k8s-fw-"+legacy || gkeL4FirewallName(name, workload, systemUID)
			if generated && (desc["kubernetes.io/service-name"] == service || desc["networking.gke.io/service-name"] == service) {
				owned = true
			}
		}
		phase := "workload"
		// The controller's cluster-shared health-check rule contains a hash of
		// the live kube-system UID; its node targets must independently agree.
		if name == "k8s2-"+gkeContentHash(systemUID)+"-l4-shared-hc-fw" || name == "k8s2-"+gkeContentHash(systemUID)+"-l4-shared-hc-fw-ipv6" || strings.HasPrefix(name, "k8s-fw-l7--") {
			owned = true
			phase = "cluster"
		}
		for tag := range tags {
			if match := regexp.MustCompile(`^gke-` + regexp.QuoteMeta(text(live["name"])) + `-([a-z0-9]{8})-node$`).FindStringSubmatch(tag); len(match) > 0 && name == "gke-"+match[1]+"-ipv6-all" {
				owned = true
				phase = "cluster"
			}
		}
		if owned {
			// Disabling L4 firewall reconciliation stops creation and updates.
			// The native controller still deletes its named rules on Service
			// deletion (ingress-gce deleteIPv4ResourcesOnDelete).
			if err := add("compute.googleapis.com/Firewall", firewall, true, phase); err != nil {
				return err
			}
		}
	}
	routes, err := c.computeList(ctx, "compute.routes.list", "items", nil)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route["description"] != "k8s-node-route" || c.canonicalName(text(route["network"])) != network || !nativeNodes[c.canonicalName(text(route["nextHopInstance"]))] {
			continue
		}
		if err := add("compute.googleapis.com/Route", route, true, "cluster"); err != nil {
			return err
		}
	}
	region := regionOf(last(strings.Split(root.Identity.NativeID, "/clusters/")[0]))
	for _, scope := range []string{"global", "regional"} {
		op, kind, params := "compute.globalAddresses.list", "compute.googleapis.com/GlobalAddress", map[string]any{}
		if scope == "regional" {
			op, kind = "compute.addresses.list", "compute.googleapis.com/Address"
			params["region"] = region
		}
		addresses, err := c.computeList(ctx, op, "items", params)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			used, foreign := false, false
			for _, raw := range array(address["users"]) {
				if _, owned := resources[c.canonicalName(text(raw))]; owned {
					used = true
				} else {
					foreign = true
				}
			}
			if !used {
				continue
			}
			deletes := false
			for _, workload := range workloads {
				annotations := object(object(workload.data["metadata"])["annotations"])
				// ingress-gce allocates and deletes its own global reservation with
				// exactly the HTTP forwarding rule's name, even for HTTPS traffic.
				if workload.Kind == "Ingress" && kind == "compute.googleapis.com/GlobalAddress" && text(annotations["ingress.kubernetes.io/forwarding-rule"]) != "" && address["name"] == annotations["ingress.kubernetes.io/forwarding-rule"] {
					deletes = true
				}
			}
			if err := add(kind, address, deletes && !foreign, "workload"); err != nil {
				return err
			}
		}
	}
	return nil
}

func gkeContentHash(value string) string {
	return gkeHash(value, 8)
}

// Match ingress-gce's L4Namer, including its proportional namespace/name
// truncation. A cluster hash prefix does not authorize arbitrary firewall names.
func gkeL4FirewallName(candidate string, workload gkeWorkload, systemUID string) bool {
	uid := gkeContentHash(systemUID)
	hash := gkeContentHash(strings.Join([]string{uid, workload.Namespace, workload.Name}, ";"))
	base := func(maximum int) string {
		namespace, name := workload.Namespace, workload.Name
		if total := len(namespace) + len(name); total > maximum {
			excess := total - maximum
			a, b := len(namespace)-len(namespace)*excess/total-1, len(name)-len(name)*excess/total-1
			for remaining := maximum - a - b; remaining > 0; remaining-- {
				if remaining == 1 {
					a++
				} else {
					b++
				}
			}
			namespace, name = namespace[:a], name[:b]
		}
		return strings.Join([]string{"k8s2", uid, namespace, name, hash}, "-")
	}
	name := base(39)
	for _, suffix := range []string{"", "-fw", "-ipv6", "-fw-ipv6"} {
		if candidate == name[:min(len(name), 63-len(suffix))]+suffix {
			return true
		}
	}
	for _, suffix := range []string{"-deny", "-deny-ipv6"} {
		if candidate == base(39-len(suffix))+suffix {
			return true
		}
	}
	return false
}

// A Gateway's Secret references cause its HTTPS proxy to use generated Compute
// certificates. Pre-shared certificates are user resources. Join the live
// frontend and proxy to this exact certificate; never read TLS Secret contents.
func (c *client) gatewaySecretCertificate(workload gkeWorkload, certificate gkeNetworkResource, resources map[string]gkeNetworkResource) bool {
	if workload.Kind != "Gateway" || certificate.data["type"] != "SELF_MANAGED" {
		return false
	}
	hasSecret := false
	for _, raw := range array(object(workload.data["spec"])["listeners"]) {
		tls := object(object(raw)["tls"])
		for _, name := range strings.Split(text(object(tls["options"])["networking.gke.io/pre-shared-certs"]), ",") {
			if last(strings.TrimSpace(name)) == last(certificate.ID) {
				return false
			}
		}
		for _, raw := range array(tls["certificateRefs"]) {
			ref := object(raw)
			if (text(ref["kind"]) == "" || ref["kind"] == "Secret") && text(ref["group"]) == "" && text(ref["name"]) != "" {
				hasSecret = true
			}
		}
	}
	if !hasSecret {
		return false
	}
	for _, resource := range resources {
		if (resource.Kind != "compute.googleapis.com/ForwardingRule" && resource.Kind != "compute.googleapis.com/GlobalForwardingRule") || !forwardingMatchesWorkload(resource.data, workload) {
			continue
		}
		proxy := resources[c.canonicalName(text(resource.data["target"]))]
		if proxy.Kind != "compute.googleapis.com/TargetHttpsProxy" {
			continue
		}
		for _, raw := range array(proxy.data["sslCertificates"]) {
			if c.canonicalName(text(raw)) == certificate.ID {
				return true
			}
		}
	}
	return false
}

func gkeHash(value string, size int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	hash := sha256.Sum256([]byte(value))
	result := make([]byte, size)
	for i := range result {
		result[i] = alphabet[int(hash[i])%len(alphabet)]
	}
	return string(result)
}

func gkeGeneratedCertificate(workload gkeWorkload, certificate map[string]any, systemUID string, resources map[string]gkeNetworkResource) bool {
	annotations := object(object(workload.data["metadata"])["annotations"])
	name := text(certificate["name"])
	if workload.Kind == "Ingress" {
		urlMap := text(annotations["ingress.kubernetes.io/url-map"])
		verified := false
		for _, resource := range resources {
			if resource.Kind == "compute.googleapis.com/UrlMap" && last(resource.ID) == urlMap && description(resource.data)["kubernetes.io/ingress-name"] == workload.Namespace+"/"+workload.Name {
				verified = true
			}
		}
		if !verified {
			return false
		}
		if strings.HasPrefix(urlMap, "k8s2-um-"+gkeContentHash(systemUID)+"-") {
			return strings.HasPrefix(name, "k8s2-cr-"+gkeContentHash(systemUID)+"-"+gkeHash(strings.TrimPrefix(urlMap, "k8s2-um-"), 16)+"-")
		}
		if strings.HasPrefix(urlMap, "k8s-um-") {
			lb := strings.TrimPrefix(urlMap, "k8s-um-")
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(lb)))
			parts := strings.Split(lb, "--")
			if len(parts) == 2 && strings.HasPrefix(name, "k8s-ssl-"+hash[:16]+"-") && strings.HasSuffix(name, "--"+parts[1]) {
				return true
			}
			for _, prefix := range []string{"k8s-ssl-" + lb, "k8s-ssl-1-" + lb} {
				if len(prefix) > 63 {
					prefix = prefix[:63]
				}
				if strings.HasPrefix(name, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func (r *Runtime) EnrichInventoryBatch(ctx context.Context, request contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
	var c *client
	for i, item := range items {
		if item.NativeType != clusterType {
			continue
		}
		var err error
		if c == nil {
			c, err = r.resolve(ctx, request.ConnectionID)
			if err != nil {
				return nil, err
			}
		}
		live, err := c.nativeGet(ctx, clusterType, item.NativeID)
		if err != nil {
			return nil, err
		}
		if text(live["id"]) == "" || text(live["id"]) != text(item.Normalized["id"]) {
			return nil, groupDenied("gke_cluster_identity_changed")
		}
		root := asset.Asset{Identity: asset.Identity{NativeType: clusterType, NativeID: item.NativeID}}
		nodes, err := c.gkeMembers(ctx, root, live)
		if err != nil {
			return nil, err
		}
		snapshot, err := c.gkeNetwork(ctx, root, live, nodes)
		if err != nil {
			return nil, err
		}
		items[i].Normalized[gkeNetworkKey], err = snapshot.payload()
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}
