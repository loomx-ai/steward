package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	tpuHost            = "tpu.googleapis.com"
	tpuNodeType        = tpuHost + "/Node"
	tpuQueueType       = tpuHost + "/QueuedResource"
	tpuReservationType = tpuHost + "/Reservation"
	tpuProof           = "_tpu_configuration"
	tpuBaseProof       = "_tpu_without_disks"
	tpuQueueProof      = "_tpu_queue_configuration"
	tpuDiskProofs      = "_tpu_disks"
	tpuNodeProofs      = "_tpu_nodes"
)

var tpuSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.~+%-]+$`)

func isTPU(kind string) bool { return strings.HasPrefix(kind, tpuHost+"/") }
func tpuSegment(value string) bool {
	return value != "." && value != ".." && tpuSegmentPattern.MatchString(value)
}

func tpuCanonical(value string) string {
	if !strings.HasPrefix(value, "https://"+tpuHost+"/") {
		return value
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host != tpuHost {
		return value
	}
	for _, version := range []string{"v2", "v2alpha1"} {
		if strings.HasPrefix(u.Path, "/"+version+"/projects/") {
			return "//" + tpuHost + strings.TrimPrefix(u.Path, "/"+version)
		}
	}
	return value
}

func (c *client) tpuName(kind, id string) (string, error) {
	prefix := "//" + tpuHost + "/"
	name := strings.TrimPrefix(id, prefix)
	parts := strings.Split(name, "/")
	collection := map[string]string{tpuNodeType: "nodes", tpuQueueType: "queuedResources", tpuReservationType: "reservations"}[kind]
	if !strings.HasPrefix(id, prefix) || collection == "" || len(parts) != 6 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number) || parts[2] != "locations" || parts[4] != collection || !tpuSegment(parts[5]) || !segmentPattern.MatchString(parts[3]) || regionOf(parts[3]) == parts[3] {
		return "", groupDenied("tpu_resource_identity_invalid")
	}
	return name, nil
}

func (c *client) tpuID(kind, value string) (string, error) {
	if strings.HasPrefix(value, "projects/") {
		value = "//" + tpuHost + "/" + value
	}
	id := c.canonicalName(value)
	_, err := c.tpuName(kind, id)
	return id, err
}

func tpuConfiguration(kind string, raw map[string]any, withoutDisks bool) string {
	value := cloneParameters(raw)
	for key := range value {
		if slices.Contains([]string{"name", "state", "health", "healthDescription", "symptoms", "networkEndpoints", "upcomingMaintenance", "updateTime", "project_id", "project_number"}, key) || strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") {
			delete(value, key)
		}
	}
	if kind == tpuNodeType && (withoutDisks || len(array(value["dataDisks"])) == 0) {
		delete(value, "dataDisks")
	}
	if kind == tpuReservationType {
		standard := cloneParameters(object(value["standard"]))
		delete(standard, "usage")
		value["standard"] = standard
	}
	// The request embeds a Node template, not the changing status of each VM.
	if kind == tpuQueueType {
		tpu := cloneParameters(object(value["tpu"]))
		var specs []any
		for _, raw := range array(tpu["nodeSpec"]) {
			entry := cloneParameters(object(raw))
			entry["node"] = tpuConfiguration(tpuNodeType, object(entry["node"]), false)
			specs = append(specs, entry)
		}
		tpu["nodeSpec"] = specs
		value["tpu"] = tpu
	}
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func tpuSameResource(kind string, planned, live map[string]any) error {
	if !isTPU(kind) {
		return nil
	}
	expected := text(planned[tpuProof])
	if expected == "" {
		expected = tpuConfiguration(kind, planned, false)
	}
	if expected != tpuConfiguration(kind, live, false) {
		return groupDenied("tpu_configuration_changed")
	}
	return nil
}

func (c *client) tpuIdentity(kind, id string, data map[string]any) error {
	actual, err := c.tpuID(kind, text(data["name"]))
	if err != nil || actual != id {
		return groupDenied("tpu_resource_identity_changed")
	}
	if kind != tpuReservationType {
		if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
			return groupDenied("tpu_creation_time_missing")
		}
	}
	if kind == tpuNodeType {
		uid, err := strconv.ParseInt(text(data["id"]), 10, 64)
		if err != nil || uid <= 0 {
			return groupDenied("tpu_node_id_missing")
		}
	}
	return nil
}

func (c *client) tpuRead(ctx context.Context, kind, id string) (map[string]any, error) {
	if kind == tpuReservationType {
		name, err := c.tpuName(kind, id)
		if err != nil {
			return nil, err
		}
		parent := strings.Join(strings.Split(name, "/")[:4], "/")
		records, err := c.batchList(ctx, "tpu.projects.locations.reservations.list", map[string]any{"parent": parent}, "reservations")
		if err != nil {
			return nil, err
		}
		var found map[string]any
		seen := map[string]bool{}
		for _, record := range records {
			actual, err := c.tpuID(kind, text(record["name"]))
			if err != nil || !strings.HasPrefix(actual, c.canonicalName("//"+tpuHost+"/"+parent)+"/reservations/") || seen[actual] {
				return nil, groupDenied("tpu_reservation_list_invalid")
			}
			seen[actual] = true
			if actual == id {
				found = record
			}
		}
		if found == nil {
			return nil, apiError(http.StatusNotFound, "not_found", nil, "")
		}
		return found, nil
	}
	native, _ := findType(kind)
	endpoint, err := c.resourceURL(native, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if _, present := data["error"]; present {
		return nil, groupDenied("tpu_error_response")
	}
	if err := c.tpuIdentity(kind, id, data); err != nil {
		return nil, err
	}
	return data, nil
}

type tpuRequestedNode struct {
	Parent string
	ID     string
	Count  int
}

func (c *client) tpuRequestedNodes(id string, data map[string]any) ([]tpuRequestedNode, error) {
	if _, err := c.tpuName(tpuQueueType, id); err != nil {
		return nil, err
	}
	records, err := productRecords(object(data["tpu"]), "nodeSpec")
	if err != nil || len(records) == 0 || len(records) > 256 {
		return nil, groupDenied("tpu_node_specs_missing")
	}
	var result []tpuRequestedNode
	seen := map[string]bool{}
	for _, record := range records {
		entry := record.Data
		parent := c.canonicalName("//" + tpuHost + "/" + text(entry["parent"]))
		if _, err := c.tpuName(tpuNodeType, parent+"/nodes/scope-check"); err != nil || len(object(entry["node"])) == 0 {
			return nil, groupDenied("tpu_node_spec_invalid")
		}
		if _, err := c.tpuAttachments(object(entry["node"])); err != nil {
			return nil, err
		}
		name := text(entry["nodeId"])
		if raw, present := entry["multisliceParams"]; present {
			multi, ok := raw.(map[string]any)
			count, err := strconv.Atoi(fmt.Sprint(multi["nodeCount"]))
			if !ok || err != nil || count < 2 || count > 10000 || name != "" {
				return nil, groupDenied("tpu_multislice_spec_invalid")
			}
			prefix := text(multi["nodeIdPrefix"])
			if prefix == "" {
				prefix = last(id) // The native MultisliceParams schema defines this default.
			}
			if !tpuSegment(prefix) {
				return nil, groupDenied("tpu_multislice_prefix_invalid")
			}
			for i := 0; i < count; i++ {
				result = append(result, tpuRequestedNode{Parent: parent, ID: parent + "/nodes/" + prefix + "-" + strconv.Itoa(i), Count: 1})
			}
		} else {
			if name != "" && !tpuSegment(name) {
				return nil, groupDenied("tpu_node_spec_name_invalid")
			}
			full := ""
			if name != "" {
				full = parent + "/nodes/" + name
			}
			// Single-node requests may let the service allocate a name. The
			// actual Node.queuedResource backlink identifies that node.
			result = append(result, tpuRequestedNode{Parent: parent, ID: full, Count: 1})
		}
	}
	for _, node := range result {
		if node.ID != "" && seen[node.ID] {
			return nil, groupDenied("tpu_node_spec_duplicate")
		}
		seen[node.ID] = true
	}
	return result, nil
}

func (c *client) tpuQueueRelation(queueID string, queue map[string]any, nodeID string, node map[string]any) error {
	parent, err := c.tpuID(tpuQueueType, text(node["queuedResource"]))
	if err != nil || parent != queueID {
		return groupDenied("tpu_node_queue_changed")
	}
	created, err := time.Parse(time.RFC3339Nano, text(node["createTime"]))
	requested, requestErr := time.Parse(time.RFC3339Nano, text(queue["createTime"]))
	if err != nil || requestErr != nil || created.Before(requested) {
		return groupDenied("tpu_queue_incarnation_changed")
	}
	specs, err := c.tpuRequestedNodes(queueID, queue)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		if spec.ID == nodeID || spec.ID == "" && strings.HasPrefix(nodeID, spec.Parent+"/nodes/") {
			return nil
		}
	}
	return groupDenied("tpu_node_outside_request")
}

type tpuAttachment struct {
	Kind string
	ID   string
	Mode string
}

func (c *client) tpuAttachments(data map[string]any) ([]tpuAttachment, error) {
	records, err := productRecords(data, "dataDisks")
	if err != nil || len(records) > 256 {
		return nil, groupDenied("tpu_data_disks_invalid")
	}
	var result []tpuAttachment
	seen := map[string]bool{}
	for _, record := range records {
		for field := range record.Data {
			if field != "sourceDisk" && field != "mode" {
				return nil, groupDenied("tpu_data_disk_field_unsupported")
			}
		}
		id := text(record.Data["sourceDisk"])
		if strings.HasPrefix(id, "projects/") {
			id = "//compute.googleapis.com/" + id
		}
		id = c.canonicalName(id)
		kind := "compute.googleapis.com/Disk"
		if strings.Contains(id, "/regions/") {
			kind = "compute.googleapis.com/RegionDisk"
		}
		native, _ := findType(kind)
		if _, err := c.resourceURL(native, id); err != nil || seen[id] {
			return nil, groupDenied("tpu_data_disk_identity_invalid")
		}
		mode := text(record.Data["mode"])
		if mode == "" || mode == "DISK_MODE_UNSPECIFIED" {
			mode = "READ_WRITE"
		}
		if mode != "READ_WRITE" && mode != "READ_ONLY" {
			return nil, groupDenied("tpu_data_disk_mode_invalid")
		}
		seen[id] = true
		result = append(result, tpuAttachment{kind, id, mode})
	}
	slices.SortFunc(result, func(a, b tpuAttachment) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

type tpuDiskProof struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	UID           string `json:"uid"`
	Configuration string `json:"configuration"`
}

func (c *client) tpuDisks(ctx context.Context, data map[string]any) ([]serviceChild, []tpuDiskProof, error) {
	attachments, err := c.tpuAttachments(data)
	if err != nil {
		return nil, nil, err
	}
	var children []serviceChild
	var proofs []tpuDiskProof
	for _, disk := range attachments {
		live, err := c.nativeGet(ctx, disk.Kind, disk.ID)
		if err != nil {
			return nil, nil, err
		}
		if c.canonicalName(text(live["selfLink"])) != disk.ID || text(live["id"]) == "" {
			return nil, nil, groupDenied("tpu_data_disk_identity_changed")
		}
		children = append(children, serviceChild{kind: disk.Kind, id: disk.ID, data: live, retain: true})
		proofs = append(proofs, tpuDiskProof{disk.Kind, disk.ID, text(live["id"]), batchComputeConfiguration(disk.Kind, live)})
	}
	return children, proofs, nil
}

func (c *client) tpuInventoryData(ctx context.Context, kind, id string, data map[string]any) (map[string]any, error) {
	if kind == tpuQueueType {
		_, proofs, err := c.tpuNodeSnapshot(ctx, id, data)
		if err != nil {
			return nil, err
		}
		_, again, err := c.tpuNodeSnapshot(ctx, id, data)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(proofs, again) {
			return nil, groupDenied("tpu_nodes_changed_during_read")
		}
		live, err := c.tpuRead(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		if err := tpuSameResource(kind, data, live); err != nil {
			return nil, err
		}
		data = cloneParameters(data)
		encoded, _ := json.Marshal(proofs)
		data[tpuNodeProofs] = string(encoded)
		return data, nil
	}
	if kind != tpuNodeType {
		return data, nil
	}
	data = cloneParameters(data)
	_, disks, err := c.tpuDisks(ctx, data)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(disks)
	data[tpuDiskProofs] = string(encoded)
	var parent map[string]any
	var queueID string
	if name := text(data["queuedResource"]); name != "" {
		queueID, err = c.tpuID(tpuQueueType, name)
		if err != nil {
			return nil, err
		}
		parent, err = c.tpuRead(ctx, tpuQueueType, queueID)
		if isNotFound(err) {
			data[tpuQueueProof] = "absent"
		} else if err != nil {
			return nil, err
		} else {
			if err := c.tpuQueueRelation(queueID, parent, id, data); err != nil {
				return nil, err
			}
			data[tpuQueueProof] = tpuConfiguration(tpuQueueType, parent, false)
		}
	}
	live, err := c.tpuRead(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if err := tpuSameResource(kind, data, live); err != nil {
		return nil, err
	}
	if queueID != "" {
		again, err := c.tpuRead(ctx, tpuQueueType, queueID)
		if data[tpuQueueProof] == "absent" && isNotFound(err) {
			return data, nil
		}
		if err != nil || tpuConfiguration(tpuQueueType, again, false) != text(data[tpuQueueProof]) {
			return nil, groupDenied("tpu_queue_changed_during_read")
		}
	}
	return data, nil
}

func (c *client) tpuReferences(kind, id string, data map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(target, value string) {
		if value != "" && !slices.Contains(refs[target], value) {
			refs[target] = append(refs[target], value)
		}
	}
	var nodes []map[string]any
	if kind == tpuNodeType {
		nodes = append(nodes, data)
		if name := text(data["queuedResource"]); name != "" {
			parent, err := c.tpuID(tpuQueueType, name)
			if err != nil {
				return nil, err
			}
			add(tpuQueueType, parent)
		}
	}
	if kind == tpuQueueType {
		if name := text(data["reservationName"]); name != "" {
			reservation, err := c.tpuID(tpuReservationType, name)
			if err != nil {
				return nil, err
			}
			add(tpuReservationType, reservation)
		}
		for _, raw := range array(object(data["tpu"])["nodeSpec"]) {
			node := cloneParameters(object(object(raw)["node"]))
			node["_location"] = last(text(object(raw)["parent"]))
			nodes = append(nodes, node)
		}
	}
	for _, node := range nodes {
		zone := strings.Split(strings.TrimPrefix(id, "//"+tpuHost+"/"), "/")[3]
		if location := text(node["_location"]); location != "" {
			zone = location
		}
		var networks []map[string]any
		if raw := node["networkConfig"]; raw != nil {
			network, ok := raw.(map[string]any)
			if !ok {
				return nil, groupDenied("tpu_network_invalid")
			}
			networks = append(networks, network)
		}
		records, err := productRecords(node, "networkConfigs")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			networks = append(networks, record.Data)
		}
		for _, network := range networks {
			for field, target := range map[string]string{"network": "compute.googleapis.com/Network", "subnetwork": "compute.googleapis.com/Subnetwork"} {
				value := text(network[field])
				if value == "" {
					continue
				}
				if !strings.Contains(value, "/") {
					if field == "network" {
						value = "projects/" + c.project + "/global/networks/" + value
					} else {
						value = "projects/" + c.project + "/regions/" + regionOf(zone) + "/subnetworks/" + value
					}
				}
				if strings.HasPrefix(value, "projects/") {
					value = "//compute.googleapis.com/" + value
				}
				add(target, c.canonicalName(value))
			}
		}
		for target, values := range references(c, map[string]any{"serviceAccount": text(object(node["serviceAccount"])["email"]), "bootDiskConfig": node["bootDiskConfig"]}) {
			for _, value := range values {
				add(target, value)
			}
		}
		disks, err := c.tpuAttachments(node)
		if err != nil {
			return nil, err
		}
		for _, disk := range disks {
			add(disk.Kind, disk.ID)
		}
	}
	return refs, nil
}

func safeTPUPayload(raw map[string]any) map[string]any {
	value := safePayload(raw)
	var redact func(any)
	redact = func(raw any) {
		switch data := raw.(type) {
		case map[string]any:
			for key, child := range data {
				if slices.Contains([]string{"metadata", "description", "healthDescription", "symptoms", "statusDetail"}, key) {
					data[key] = "[REDACTED]"
				} else if key == "state" {
					if state, ok := child.(map[string]any); ok {
						data[key] = map[string]any{"state": state["state"], "stateInitiator": state["stateInitiator"]}
					}
				} else {
					redact(child)
				}
			}
		case []any:
			for _, child := range data {
				redact(child)
			}
		}
	}
	redact(value)
	return value
}

func tpuReview(request contracts.ActionRequest) string {
	encoded, _ := json.Marshal(struct {
		Impacts       []contracts.ActionImpact
		Prerequisites []contracts.ActionImpact
	}{request.LifecycleImpacts, request.PrerequisiteDeletions})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
