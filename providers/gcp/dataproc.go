package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dataprocClusterType   = "dataproc.googleapis.com/Cluster"
	dataprocJobType       = "dataproc.googleapis.com/Job"
	dataprocNodeGroupType = "dataproc.googleapis.com/NodeGroup"
	dataprocPolicyType    = "dataproc.googleapis.com/AutoscalingPolicy"
	dataprocTemplateType  = "dataproc.googleapis.com/WorkflowTemplate"
	dataprocProof         = "_dataproc_configuration"
	dataprocParentProof   = "_dataproc_parent_configuration"
)

func isDataproc(kind string) bool { return strings.HasPrefix(kind, "dataproc.googleapis.com/") }

// Exclude progress and public-key rotation, but bind configuration and native
// instance identities before commands, queries, properties and metadata are redacted.
func dataprocConfiguration(raw map[string]any) string {
	value := cloneParameters(raw)
	for key := range value {
		switch key {
		case "name", "projectId", "reference", "status", "statusHistory", "metrics", "done", "yarnApplications", "driverOutputResourceUri", "driverControlFilesUri", "updateTime", "project_id", "project_number":
			delete(value, key)
		default:
			if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") {
				delete(value, key)
			}
		}
	}
	var copyValue func(any) any
	copyValue = func(raw any) any {
		switch v := raw.(type) {
		case map[string]any:
			result := map[string]any{}
			for key, child := range v {
				if key != "publicKey" && key != "publicEciesKey" && key != "idleStartTime" {
					result[key] = copyValue(child)
				}
			}
			return result
		case []any:
			result := make([]any, len(v))
			for i, child := range v {
				result[i] = copyValue(child)
			}
			return result
		default:
			return raw
		}
	}
	encoded, _ := json.Marshal(copyValue(value))
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func dataprocSameResource(kind string, planned, live map[string]any) error {
	if !isDataproc(kind) {
		return nil
	}
	expected := text(planned[dataprocProof])
	if expected == "" {
		expected = dataprocConfiguration(planned)
	}
	if expected != dataprocConfiguration(live) {
		return groupDenied("dataproc_configuration_changed")
	}
	return nil
}

func (c *client) dataprocIdentity(kind, id string, data map[string]any) error {
	rule, found := findType(kind)
	if !found {
		return groupDenied("dataproc_kind_invalid")
	}
	if _, err := c.resourceURL(rule, id); err != nil {
		return err
	}
	switch kind {
	case dataprocClusterType:
		if text(data["clusterName"]) != last(id) || (data["projectId"] != c.project && data["projectId"] != c.number) || text(data["clusterUuid"]) == "" {
			return groupDenied("dataproc_cluster_identity_invalid")
		}
	case dataprocJobType:
		ref := object(data["reference"])
		if text(ref["jobId"]) != last(id) || (text(ref["projectId"]) != "" && ref["projectId"] != c.project && ref["projectId"] != c.number) || text(data["jobUuid"]) == "" || text(object(data["placement"])["clusterName"]) == "" || text(object(data["placement"])["clusterUuid"]) == "" {
			return groupDenied("dataproc_job_identity_invalid")
		}
	default:
		if c.canonicalName("//dataproc.googleapis.com/"+text(data["name"])) != id {
			return groupDenied("dataproc_resource_identity_invalid")
		}
	}
	return nil
}

func (c *client) verifyDataprocParent(ctx context.Context, target productTarget) error {
	if target.ParentType != dataprocClusterType {
		return nil
	}
	if target.ParentUID == "" || target.ParentConfiguration == "" {
		return groupDenied("dataproc_parent_proof_missing")
	}
	live, err := c.nativeGet(ctx, dataprocClusterType, target.ParentID)
	if err != nil {
		return err
	}
	if err := c.dataprocIdentity(dataprocClusterType, target.ParentID, live); err != nil {
		return err
	}
	if text(live["clusterUuid"]) != target.ParentUID || dataprocConfiguration(live) != target.ParentConfiguration {
		return groupDenied("dataproc_parent_changed")
	}
	return nil
}

// Node groups have native GET but no LIST/DELETE. Their identifiers are returned
// by clusters.get in auxiliaryNodeGroups, including server-generated IDs.
func (c *client) dataprocNodeGroups(parent string, data map[string]any) ([]map[string]any, error) {
	config := object(data["config"])
	if raw := config["auxiliaryNodeGroups"]; raw != nil {
		if _, ok := raw.([]any); !ok {
			return nil, groupDenied("dataproc_node_groups_invalid")
		}
	}
	result := []map[string]any{}
	seen := map[string]bool{}
	for _, raw := range array(config["auxiliaryNodeGroups"]) {
		entry := object(raw)
		group := cloneParameters(object(entry["nodeGroup"]))
		if len(group) == 0 {
			return nil, groupDenied("dataproc_node_group_missing")
		}
		name := text(group["name"])
		if name == "" {
			atom := text(entry["nodeGroupId"])
			if atom == "" || !segmentPattern.MatchString(atom) || atom == "." || atom == ".." {
				return nil, groupDenied("dataproc_node_group_id_missing")
			}
			name = strings.TrimPrefix(parent, "//dataproc.googleapis.com/") + "/nodeGroups/" + atom
			group["name"] = name
		}
		id := c.canonicalName("//dataproc.googleapis.com/" + name)
		if !strings.HasPrefix(id, parent+"/nodeGroups/") || strings.Contains(strings.TrimPrefix(id, parent+"/nodeGroups/"), "/") || seen[id] || text(entry["nodeGroupId"]) != "" && text(entry["nodeGroupId"]) != last(id) {
			return nil, groupDenied("dataproc_node_group_scope_changed")
		}
		if err := c.dataprocIdentity(dataprocNodeGroupType, id, group); err != nil {
			return nil, err
		}
		seen[id] = true
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool { return text(result[i]["name"]) < text(result[j]["name"]) })
	return result, nil
}

func dataprocRegion(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "//dataproc.googleapis.com/"), "/")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

func dataprocTerminalJob(data map[string]any) (bool, error) {
	switch text(object(data["status"])["state"]) {
	case "DONE", "ERROR", "CANCELLED":
		return true, nil
	case "PENDING", "SETUP_DONE", "RUNNING", "CANCEL_PENDING", "CANCEL_STARTED", "ATTEMPT_FAILURE":
		return false, nil
	default:
		return false, groupDenied("dataproc_job_state_unknown")
	}
}

func (c *client) dataprocReferences(kind, id string, data map[string]any) map[string][]string {
	result := map[string][]string{}
	add := func(kind, value string) {
		if value == "" {
			return
		}
		if strings.HasPrefix(kind, "compute.googleapis.com/") && (strings.HasPrefix(value, "global/") || strings.HasPrefix(value, "regions/") || strings.HasPrefix(value, "zones/")) {
			value = "projects/" + c.project + "/" + value
		}
		if strings.HasPrefix(value, "projects/") {
			value = "//" + strings.Split(kind, "/")[0] + "/" + value
		}
		value = c.canonicalName(value)
		rule, ok := findType(kind)
		if !ok {
			return
		}
		if _, err := c.resourceURL(rule, value); err != nil {
			return
		}
		for _, existing := range result[kind] {
			if existing == value {
				return
			}
		}
		result[kind] = append(result[kind], value)
	}
	region := dataprocRegion(id)
	var configs []map[string]any
	switch kind {
	case dataprocClusterType:
		configs = append(configs, object(data["config"]))
	case dataprocTemplateType:
		configs = append(configs, object(object(object(data["placement"])["managedCluster"])["config"]))
	}
	buckets := []any{}
	addBucket := func(raw any) {
		value := text(raw)
		if strings.HasPrefix(value, "gs://") {
			value = strings.Split(strings.TrimPrefix(value, "gs://"), "/")[0]
		}
		if value != "" {
			buckets = append(buckets, value)
		}
	}
	keys := []any{}
	for _, config := range configs {
		gce := object(config["gceClusterConfig"])
		for key, target := range map[string]string{"networkUri": "Network", "subnetworkUri": "Subnetwork"} {
			value := text(gce[key])
			if value != "" && !strings.Contains(value, "/") {
				scope := "global/networks/"
				if key == "subnetworkUri" {
					scope = "regions/" + region + "/subnetworks/"
				}
				value = "projects/" + c.project + "/" + scope + value
			}
			add("compute.googleapis.com/"+target, value)
		}
		for key, nativeType := range map[string]string{"nodeGroupUri": "compute.googleapis.com/NodeGroup"} {
			value := text(object(gce["nodeGroupAffinity"])[key])
			if value != "" && !strings.Contains(value, "/") {
				value = "projects/" + c.project + "/zones/" + last(text(gce["zoneUri"])) + "/nodeGroups/" + value
			}
			add(nativeType, value)
		}
		reservation := object(gce["reservationAffinity"])
		if reservation["consumeReservationType"] == "SPECIFIC_RESERVATION" && reservation["key"] == "compute.googleapis.com/reservation-name" {
			for _, raw := range array(reservation["values"]) {
				value := text(raw)
				if value != "" && !strings.Contains(value, "/") && text(gce["zoneUri"]) != "" {
					value = "projects/" + c.project + "/zones/" + last(text(gce["zoneUri"])) + "/reservations/" + value
				}
				add("compute.googleapis.com/Reservation", value)
			}
		}
		refData := map[string]any{"serviceAccount": gce["serviceAccount"]}
		for target, values := range references(c, refData) {
			for _, value := range values {
				add(target, value)
			}
		}
		for _, key := range []string{"configBucket", "tempBucket", "diagnosticBucket"} {
			addBucket(config[key])
		}
		for _, raw := range array(config["initializationActions"]) {
			addBucket(object(raw)["executableFile"])
		}
		encryption := object(config["encryptionConfig"])
		keys = append(keys, encryption["gcePdKmsKeyName"], encryption["kmsKey"])
		kerberos := object(object(config["securityConfig"])["kerberosConfig"])
		keys = append(keys, kerberos["kmsKeyUri"])
		addBucket(kerberos["rootPrincipalPasswordUri"])
		policy := text(object(config["autoscalingConfig"])["policyUri"])
		add(dataprocPolicyType, policy)
	}
	virtual := object(data["virtualClusterConfig"])
	addBucket(virtual["stagingBucket"])
	gke := object(object(virtual["kubernetesClusterConfig"])["gkeClusterConfig"])
	if gke == nil {
		gke = object(object(data["config"])["gkeClusterConfig"])
	}
	add(clusterType, text(gke["gkeClusterTarget"]))
	for _, raw := range array(gke["nodePoolTarget"]) {
		add(nodePoolType, text(object(raw)["nodePool"]))
	}
	phs := object(object(virtual["auxiliaryServicesConfig"])["sparkHistoryServerConfig"])
	add(dataprocClusterType, text(phs["dataprocCluster"]))
	// Job placement is linked by the lifecycle contributor after matching the
	// cluster UUID; a same-name historical cluster is a different controller.

	jobs := []any{data}
	if kind == dataprocTemplateType {
		jobs = array(data["jobs"])
	}
	for _, raw := range jobs {
		for _, jobKey := range []string{"hadoopJob", "sparkJob", "pysparkJob", "sparkRJob", "sparkSqlJob", "pigJob", "hiveJob", "prestoJob", "flinkJob", "trinoJob"} {
			job := object(object(raw)[jobKey])
			for _, key := range []string{"queryFileUri", "mainPythonFileUri", "mainRFileUri"} {
				addBucket(job[key])
			}
			for _, key := range []string{"fileUris", "jarFileUris", "archiveUris", "pythonFileUris"} {
				for _, value := range array(job[key]) {
					addBucket(value)
				}
			}
		}
	}
	keys = append(keys, object(data["encryptionConfig"])["kmsKey"])
	for target, values := range references(c, map[string]any{"bucketName": buckets, "kmsKeyName": keys}) {
		for _, value := range values {
			add(target, value)
		}
	}
	for target := range result {
		sort.Strings(result[target])
	}
	return result
}

func (a *action) dataprocActionIdentity(request contracts.ActionRequest) error {
	if !isDataproc(a.kind.NativeType) {
		return nil
	}
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity != a.identity || request.Asset.Identity.Provider != asset.ProviderGCP || request.Asset.Identity.NativeType != a.kind.NativeType || text(request.Asset.Normalized[dataprocProof]) == "" {
		return groupDenied("dataproc_action_identity_changed")
	}
	return a.client.dataprocIdentity(a.kind.NativeType, request.Asset.Identity.NativeID, request.Asset.Normalized)
}

func (a *action) dataprocPreflight(request contracts.ActionRequest, live map[string]any) error {
	if !isDataproc(a.kind.NativeType) {
		return nil
	}
	if err := a.dataprocActionIdentity(request); err != nil {
		return err
	}
	if err := a.client.dataprocIdentity(a.kind.NativeType, request.Asset.Identity.NativeID, live); err != nil {
		return err
	}
	if a.kind.NativeType == dataprocClusterType {
		if live["clusterUuid"] != request.Asset.Normalized["clusterUuid"] {
			return groupDenied("dataproc_cluster_recreated")
		}
		switch text(object(live["status"])["state"]) {
		case "DELETING":
			return nil
		case "CREATING", "RUNNING", "ERROR", "UPDATING", "STOPPING", "STOPPED", "STARTING", "ERROR_DUE_TO_UPDATE", "REPAIRING", "SCHEDULED":
		default:
			return groupDenied("dataproc_cluster_state_unknown")
		}
	}
	if err := dataprocSameResource(a.kind.NativeType, request.Asset.Normalized, live); err != nil {
		return err
	}
	if a.kind.NativeType == dataprocJobType {
		_, err := dataprocTerminalJob(live)
		return err
	}
	return nil
}

func redactDataprocPayload(value map[string]any) {
	if value["instanceName"] != nil && value["instanceId"] != nil {
		for _, key := range []string{"publicKey", "publicEciesKey"} {
			if value[key] != nil {
				value[key] = "[REDACTED]"
			}
		}
	}
	for _, field := range []string{"hadoopJob", "sparkJob", "pysparkJob", "sparkRJob", "sparkSqlJob", "pigJob", "hiveJob", "prestoJob", "flinkJob", "trinoJob"} {
		if _, ok := value[field].(map[string]any); ok {
			value[field] = "[REDACTED]"
		}
	}
	for _, field := range []string{"softwareConfig", "kubernetesSoftwareConfig"} {
		if config := object(value[field]); config != nil && config["properties"] != nil {
			config["properties"] = "[REDACTED]"
		}
	}
	if config := object(value["gceClusterConfig"]); config != nil && config["metadata"] != nil {
		config["metadata"] = "[REDACTED]"
	}
	if value["gceClusterConfig"] != nil || value["masterConfig"] != nil {
		for _, field := range []string{"securityConfig", "initializationActions"} {
			if value[field] != nil {
				value[field] = "[REDACTED]"
			}
		}
	}
	if value["clusterUuid"] != nil || value["jobUuid"] != nil {
		if status := object(value["status"]); status != nil {
			for _, field := range []string{"detail", "details"} {
				if status[field] != nil {
					status[field] = "[REDACTED]"
				}
			}
		}
		for _, field := range []string{"statusHistory", "yarnApplications", "description", "warnings"} {
			if value[field] != nil {
				value[field] = "[REDACTED]"
			}
		}
	}
}
