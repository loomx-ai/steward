package gcp

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"time"
)

// These Terraform resources manage complete native objects. Deliberately do not
// derive this mapping by removing a google_ prefix: IAM members/policies, bucket
// objects, router NAT, peering and attachment resources can name a containing CAI
// asset without owning that asset's lifecycle. The native TF ID must also agree
// with the CAI full resource name before a mapping can authorize destruction.
var infraTerraformKinds = map[string]string{
	"google_compute_network":                       "compute.googleapis.com/Network",
	"google_compute_subnetwork":                    "compute.googleapis.com/Subnetwork",
	"google_compute_firewall":                      "compute.googleapis.com/Firewall",
	"google_compute_disk":                          "compute.googleapis.com/Disk",
	"google_compute_region_disk":                   "compute.googleapis.com/RegionDisk",
	"google_compute_snapshot":                      "compute.googleapis.com/Snapshot",
	"google_compute_image":                         "compute.googleapis.com/Image",
	"google_compute_instance":                      instanceType,
	"google_compute_instance_template":             "compute.googleapis.com/InstanceTemplate",
	"google_compute_region_instance_template":      "compute.googleapis.com/InstanceTemplate",
	"google_compute_instance_group":                instanceGroupType,
	"google_compute_instance_group_manager":        managerType,
	"google_compute_region_instance_group_manager": managerType,
	"google_compute_autoscaler":                    autoscalerType,
	"google_compute_region_autoscaler":             autoscalerType,
	"google_compute_address":                       "compute.googleapis.com/Address",
	"google_compute_global_address":                "compute.googleapis.com/GlobalAddress",
	"google_compute_router":                        "compute.googleapis.com/Router",
	"google_compute_route":                         "compute.googleapis.com/Route",
	"google_compute_forwarding_rule":               "compute.googleapis.com/ForwardingRule",
	"google_compute_global_forwarding_rule":        "compute.googleapis.com/GlobalForwardingRule",
	"google_compute_backend_service":               "compute.googleapis.com/BackendService",
	"google_compute_region_backend_service":        "compute.googleapis.com/RegionBackendService",
	"google_compute_backend_bucket":                "compute.googleapis.com/BackendBucket",
	"google_compute_health_check":                  "compute.googleapis.com/HealthCheck",
	"google_compute_region_health_check":           "compute.googleapis.com/HealthCheck",
	"google_compute_http_health_check":             "compute.googleapis.com/HttpHealthCheck",
	"google_compute_https_health_check":            "compute.googleapis.com/HttpsHealthCheck",
	"google_compute_url_map":                       "compute.googleapis.com/UrlMap",
	"google_compute_region_url_map":                "compute.googleapis.com/UrlMap",
	"google_compute_ssl_certificate":               "compute.googleapis.com/SslCertificate",
	"google_compute_region_ssl_certificate":        "compute.googleapis.com/SslCertificate",
	"google_compute_security_policy":               "compute.googleapis.com/SecurityPolicy",
	"google_compute_region_security_policy":        "compute.googleapis.com/SecurityPolicy",
	"google_compute_network_endpoint_group":        "compute.googleapis.com/NetworkEndpointGroup",
	"google_compute_region_network_endpoint_group": "compute.googleapis.com/NetworkEndpointGroup",
	"google_compute_global_network_endpoint_group": "compute.googleapis.com/NetworkEndpointGroup",
	"google_compute_network_attachment":            "compute.googleapis.com/NetworkAttachment",
	"google_compute_resource_policy":               "compute.googleapis.com/ResourcePolicy",
	"google_compute_reservation":                   "compute.googleapis.com/Reservation",
	"google_storage_bucket":                        "storage.googleapis.com/Bucket",
	"google_pubsub_topic":                          "pubsub.googleapis.com/Topic",
	"google_pubsub_subscription":                   "pubsub.googleapis.com/Subscription",
	"google_cloud_run_v2_service":                  "run.googleapis.com/Service",
	"google_cloud_run_v2_job":                      "run.googleapis.com/Job",
	"google_container_cluster":                     clusterType,
	"google_container_node_pool":                   nodePoolType,
	"google_service_account":                       "iam.googleapis.com/ServiceAccount",
	"google_sql_database_instance":                 "sqladmin.googleapis.com/Instance",
	"google_secret_manager_secret":                 "secretmanager.googleapis.com/Secret",
	"google_artifact_registry_repository":          "artifactregistry.googleapis.com/Repository",
	"google_redis_instance":                        "redis.googleapis.com/Instance",
	"google_spanner_instance":                      "spanner.googleapis.com/Instance",
	"google_spanner_database":                      "spanner.googleapis.com/Database",
	"google_bigtable_instance":                     "bigtableadmin.googleapis.com/Instance",
	"google_bigtable_table":                        "bigtableadmin.googleapis.com/Table",
	"google_alloydb_cluster":                       "alloydb.googleapis.com/Cluster",
	"google_alloydb_instance":                      "alloydb.googleapis.com/Instance",
	"google_filestore_instance":                    "file.googleapis.com/Instance",
	"google_data_fusion_instance":                  fusionInstanceType,
	"google_dataform_repository":                   dataformRepositoryType,
	"google_tpu_v2_vm":                             tpuNodeType,
	"google_cloud_tasks_queue":                     "cloudtasks.googleapis.com/Queue",
	"google_datastream_stream":                     "datastream.googleapis.com/Stream",
	"google_datastream_connection_profile":         "datastream.googleapis.com/ConnectionProfile",
	"google_datastream_private_connection":         "datastream.googleapis.com/PrivateConnection",
	"google_certificate_manager_certificate":       "certificatemanager.googleapis.com/Certificate",
	"google_certificate_manager_certificate_map":   "certificatemanager.googleapis.com/CertificateMap",
}

// CAI search/analysis use broader types than list/export for these collections.
// Accept those documented aliases only after trying the distinct native type;
// the full CAI name and Terraform state ID must still identify the same object.
func infraCAIType(kind string) string {
	switch kind {
	case "compute.googleapis.com/RegionDisk":
		return "compute.googleapis.com/Disk"
	case "compute.googleapis.com/GlobalAddress":
		return "compute.googleapis.com/Address"
	case "compute.googleapis.com/RegionBackendService":
		return "compute.googleapis.com/BackendService"
	case "compute.googleapis.com/GlobalForwardingRule":
		return "compute.googleapis.com/ForwardingRule"
	}
	return kind
}

func (c *client) infraPhysicalMember(ctx context.Context, record map[string]any) (infraMember, bool, error) {
	if record["intent"] == "DELETE" && record["state"] == "RECONCILED" {
		return infraMember{}, true, nil // A completed historical deletion owns nothing now.
	}
	if record["state"] != "RECONCILED" || !slices.Contains([]string{"CREATE", "UPDATE", "RECREATE", "UNCHANGED"}, text(record["intent"])) {
		return infraMember{}, false, nil
	}
	info := object(record["terraformInfo"])
	kind := infraTerraformKinds[text(info["type"])]
	assets, ok := record["caiAssets"].(map[string]any)
	if kind == "" || !ok || len(assets) != 1 {
		return infraMember{}, false, nil
	}
	value, present := assets[kind]
	if !present {
		value = assets[infraCAIType(kind)]
	}
	fullName := text(object(value)["fullResourceName"])
	if !strings.HasPrefix(fullName, "//") {
		return infraMember{}, false, nil
	}
	id := c.canonicalName(fullName)
	if strings.HasPrefix(kind, "bigtableadmin.googleapis.com/") {
		id = strings.Replace(id, "//bigtable.googleapis.com/", "//bigtableadmin.googleapis.com/", 1)
	}
	native, known := findType(kind)
	if !known {
		return infraMember{}, false, nil
	}
	if _, err := c.resourceURL(native, id); err != nil {
		return infraMember{}, false, nil
	}
	tfID := text(info["id"])
	// Spanner's provider persists these short state IDs, even though its import
	// examples also accept full resource names. The CAI path still fixes the
	// project and parent, and resourceURL above enforces the connection boundary.
	parts := strings.Split(id, "/")
	if kind == "spanner.googleapis.com/Instance" && len(parts) == 7 && (tfID == c.project+"/"+parts[6] || tfID == c.number+"/"+parts[6]) {
		tfID = id
	}
	if kind == "spanner.googleapis.com/Database" && len(parts) == 9 && tfID == parts[6]+"/"+parts[8] {
		tfID = id
	}
	if strings.HasPrefix(tfID, "projects/") || kind == "storage.googleapis.com/Bucket" && !strings.Contains(tfID, "/") {
		tfID = "//" + strings.Split(kind, "/")[0] + "/" + tfID
	}
	tfID = c.canonicalName(tfID)
	if tfID != id && kind != "iam.googleapis.com/ServiceAccount" {
		return infraMember{}, false, nil
	}
	if _, err := c.resourceURL(native, tfID); err != nil {
		return infraMember{}, false, nil
	}
	member := infraMember{Kind: kind, ID: tfID}
	live, err := c.infraPhysicalRead(ctx, kind, tfID)
	if isNotFound(err) {
		if tfID != id {
			// IAM accepts both email and unique ID. Both must be absent before
			// an unmatched alias can be recorded as drift.
			_, aliasErr := c.infraPhysicalRead(ctx, kind, id)
			if !isNotFound(aliasErr) {
				if aliasErr != nil {
					return member, false, aliasErr
				}
				return member, false, groupDenied("infra_service_account_alias_changed")
			}
		}
		// Record native drift as an observed absence, not an invented live asset.
		// Reappearance must invalidate the reviewed deployment manifest.
		member.Absent = true
		return member, true, nil
	}
	if err != nil {
		return member, false, err
	}
	if kind == "iam.googleapis.com/ServiceAccount" {
		name := c.canonicalName("//iam.googleapis.com/" + text(live["name"]))
		unique := strings.TrimSuffix(name, last(name)) + text(live["uniqueId"])
		if id != name && id != unique {
			return member, false, groupDenied("infra_service_account_alias_changed")
		}
		member.ID = name
	}
	member.Proof = infraPhysicalConfiguration(live)
	member.VisibleProof = infraPhysicalVisible(kind, live)
	member.Incarnation = infraPhysicalIncarnation(kind, live)
	return member, true, nil
}

func (c *client) infraPhysicalRead(ctx context.Context, kind, id string) (map[string]any, error) {
	data, err := c.nativeGet(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if _, present := data["error"]; present {
		return nil, groupDenied("infra_physical_error_payload")
	}
	valid := false
	switch {
	case strings.HasPrefix(kind, "compute.googleapis.com/"):
		valid = c.canonicalName(text(data["selfLink"])) == id && text(data["id"]) != ""
	case kind == "storage.googleapis.com/Bucket":
		valid = text(data["name"]) == last(id) && text(data["projectNumber"]) == c.number && text(data["timeCreated"]) != ""
	case kind == "sqladmin.googleapis.com/Instance":
		valid = text(data["name"]) == last(id) && (data["project"] == c.project || data["project"] == c.number) && text(data["createTime"]) != ""
	case kind == "iam.googleapis.com/ServiceAccount":
		name := c.canonicalName("//iam.googleapis.com/" + text(data["name"]))
		unique := text(data["uniqueId"])
		valid = unique != "" && (data["projectId"] == c.project || data["projectId"] == c.number) && last(name) == text(data["email"]) && (id == name || id == strings.TrimSuffix(name, last(name))+unique)
	case kind == clusterType:
		valid = text(data["name"]) == last(id) && text(data["id"]) != "" && text(data["location"]) == strings.Split(id, "/")[6]
		if self := text(data["selfLink"]); self != "" {
			valid = valid && c.canonicalName(self) == id
		}
	case kind == nodePoolType:
		actual, err := c.nodePoolID(strings.Split(id, "/nodePools/")[0], data)
		valid = err == nil && actual == id
	default:
		name := text(data["name"])
		if strings.HasPrefix(name, "projects/") {
			name = "//" + strings.Split(kind, "/")[0] + "/" + name
		}
		valid = c.canonicalName(name) == id
	}
	if !valid {
		return nil, groupDenied("infra_physical_identity_changed")
	}
	return data, nil
}

func infraPhysicalIncarnation(kind string, data map[string]any) string {
	identity := map[string]any{}
	for _, field := range []string{"createTime", "timeCreated", "creationTimestamp"} {
		if value := text(data[field]); value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
				identity[field] = value
			}
		}
	}
	fields := []string{"uid"}
	if strings.HasPrefix(kind, "compute.googleapis.com/") || kind == clusterType || kind == tpuNodeType {
		fields = append(fields, "id")
	}
	if kind == "iam.googleapis.com/ServiceAccount" {
		fields = append(fields, "uniqueId")
	}
	for _, field := range fields {
		if value := text(data[field]); value != "" {
			identity[field] = value
		}
	}
	if len(identity) == 0 {
		return "" // This API cannot distinguish incarnations independently of config.
	}
	return infraConfiguration(identity)
}

func infraPhysicalConfiguration(data map[string]any) string {
	value := cloneParameters(data)
	for key := range value {
		if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") || slices.Contains([]string{
			"status", "state", "health", "healthDescription", "statusMessage", "statusDetail", "updateTime", "updated", "kind", "fingerprint", "labelFingerprint", "selfLinkWithId", "vpc_id", "subnet_ids", "vswitch_id", "zone_id", "project_id", "project_number", "cleanup_protected", "cleanup_protection_reason",
		}, key) {
			delete(value, key)
		}
	}
	return infraConfiguration(value)
}

func infraPhysicalVisible(kind string, data map[string]any) string {
	value := safePayload(data)
	if isFusion(kind) {
		value = safeFusionPayload(data)
	}
	if isTPU(kind) {
		value = safeTPUPayload(data)
	}
	if isDiscovery(kind) {
		value = safeDiscoveryPayload(data)
	}
	if kind == clusterType {
		value["labels"] = value["resourceLabels"]
	}
	if kind == nodePoolType {
		value["labels"] = object(value["config"])["resourceLabels"]
	}
	// Inventory adds declared query aliases after constructing the native proof.
	// Strip only aliases that still equal their source; changed aliases must not
	// hide tampering, and native fields (Path == field) remain part of the proof.
	if metadata, err := providerData(); err == nil {
		for _, compiled := range metadata.bundle.Specs {
			if compiled.ResourceKind.NativeType != kind {
				continue
			}
			for field, property := range compiled.Definition.Fields {
				if property.Path != field && reflect.DeepEqual(value[field], productValue(data, property.Path)) {
					delete(value, field)
				}
			}
			break
		}
	}
	return infraPhysicalConfiguration(value)
}
