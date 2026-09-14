package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func uptimeTargetString(value any) string { s, _ := value.(string); return s }

func uptimeSegment(value string) bool {
	return tpuSegment(value) && value != "-" && !strings.ContainsAny(value, "%+~")
}

// References come only from native target fields. User labels, HTTP headers and
// response matchers may contain arbitrary strings that are not dependencies.
func (c *client) uptimeReferences(id string, data map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(kind string, parts ...string) error {
		for _, part := range parts {
			if !uptimeSegment(part) {
				return groupDenied("uptime_target_identity_invalid")
			}
		}
		host, _, _ := strings.Cut(kind, "/")
		refs[kind] = append(refs[kind], c.canonicalName("//"+host+"/"+strings.Join(parts, "/")))
		return nil
	}
	if synthetic := object(data["syntheticMonitor"]); synthetic != nil {
		raw := object(synthetic["cloudFunctionV2"])["name"]
		name, ok := raw.(string)
		parts := strings.Split(name, "/")
		if !ok || len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "functions" {
			return nil, groupDenied("uptime_target_identity_invalid")
		}
		if err := add("cloudfunctions.googleapis.com/CloudFunction", parts...); err != nil {
			return nil, err
		}
	}
	if group := object(data["resourceGroup"]); group != nil {
		parts := strings.Split(strings.TrimPrefix(id, "//monitoring.googleapis.com/"), "/")
		if len(parts) != 4 || parts[0] != "projects" || parts[2] != "uptimeCheckConfigs" {
			return nil, groupDenied("uptime_target_identity_invalid")
		}
		if err := add("monitoring.googleapis.com/Group", "projects", parts[1], "groups", uptimeTargetString(group["groupId"])); err != nil {
			return nil, err
		}
	}
	target := object(data["monitoredResource"])
	labels := object(target["labels"])
	var kind string
	var parts []string
	switch target["type"] {
	case "gce_instance":
		if !firewallNumericID(uptimeTargetString(labels["instance_id"])) {
			return nil, groupDenied("uptime_target_identity_invalid")
		}
		kind = instanceType
		parts = []string{"projects", uptimeTargetString(labels["project_id"]), "zones", uptimeTargetString(labels["zone"]), "instances", uptimeTargetString(labels["instance_id"])}
	case "k8s_service":
		if !uptimeSegment(uptimeTargetString(labels["namespace_name"])) || !uptimeSegment(uptimeTargetString(labels["service_name"])) {
			return nil, groupDenied("uptime_target_identity_invalid")
		}
		kind = clusterType
		parts = []string{"projects", uptimeTargetString(labels["project_id"]), "locations", uptimeTargetString(labels["location"]), "clusters", uptimeTargetString(labels["cluster_name"])}
	case "servicedirectory_service":
		kind = "servicedirectory.googleapis.com/Service"
		parts = []string{"projects", uptimeTargetString(labels["project_id"]), "locations", uptimeTargetString(labels["location"]), "namespaces", uptimeTargetString(labels["namespace_name"]), "services", uptimeTargetString(labels["service_name"])}
	case "cloud_run_revision":
		// A revision belongs to this service; output-only synthetic revisions are
		// deliberately not traversed because the function is the configured target.
		kind = "run.googleapis.com/Service"
		parts = []string{"projects", uptimeTargetString(labels["project_id"]), "locations", uptimeTargetString(labels["location"]), "services", uptimeTargetString(labels["service_name"])}
	}
	if kind != "" {
		if err := add(kind, parts...); err != nil {
			return nil, err
		}
	}
	checkers, err := cloudNatObjects(data, "internalCheckers")
	if err != nil {
		return nil, err
	}
	for _, checker := range checkers {
		if err := add("compute.googleapis.com/Network", "projects", uptimeTargetString(checker["peerProjectId"]), "global", "networks", uptimeTargetString(checker["network"])); err != nil {
			return nil, err
		}
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
	}
	return refs, nil
}

// Compute's immutable numeric ID is not its resource name. This alias allows
// monitoring references to join network scans without matching a recreated VM.
func uptimeInstanceAlias(id string, data map[string]any) string {
	number, ok := data["id"].(string)
	parts := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")
	if !ok || !firewallNumericID(number) || !strings.HasPrefix(id, "//compute.googleapis.com/") || len(parts) != 6 || parts[0] != "projects" || parts[2] != "zones" || parts[4] != "instances" {
		return ""
	}
	for _, part := range parts {
		if !uptimeSegment(part) {
			return ""
		}
	}
	parts[5] = number
	return "//compute.googleapis.com/" + strings.Join(parts, "/")
}

type UptimeTargets struct{}

func NewUptimeTargets() *UptimeTargets { return &UptimeTargets{} }

func (*UptimeTargets) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, source := range assets {
		if source.ClosedAt != nil || source.Identity.Provider != asset.ProviderGCP || source.Identity.NativeType != uptimeType && source.Identity.NativeType != monitoringGroupType {
			continue
		}
		c := &client{project: text(source.Normalized["project_id"]), number: text(source.Normalized["project_number"])}
		refs, err := c.uptimeReferences(source.Identity.NativeID, source.Normalized)
		evidenceSource, graphSource := "native_uptime_target", "gcp:uptime-targets"
		if source.Identity.NativeType == monitoringGroupType {
			refs, err = c.monitoringGroupReferences(source.Normalized)
			evidenceSource, graphSource = "native_monitoring_group_observation", "gcp:monitoring-group-targets"
			if len(object(source.Normalized[monitoringGroupUnmapped])) != 0 {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: source.Identity.Provider, ConnectionID: source.Identity.ConnectionID, ControllerID: source.ID, NativeType: monitoringGroupType, NativeID: source.Identity.NativeID, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"reason": "monitoring_group_members_unmapped", "types": source.Normalized[monitoringGroupUnmapped], "interval": source.Normalized["_monitoring_group_member_interval"]}})
			}
		}
		if err != nil {
			return result, err
		}
		kinds := make([]string, 0, len(refs))
		for kind := range refs {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		for _, kind := range kinds {
			for _, id := range refs[kind] {
				var target *asset.Asset
				for i := range assets {
					candidate := &assets[i]
					if candidate.ClosedAt != nil || candidate.Identity.Provider != source.Identity.Provider || candidate.Identity.Partition != source.Identity.Partition || candidate.Identity.ConnectionID != source.Identity.ConnectionID || candidate.Identity.NativeType != kind {
						continue
					}
					match := c.canonicalName(candidate.Identity.NativeID) == id
					if kind == instanceType {
						match = c.canonicalName(uptimeInstanceAlias(candidate.Identity.NativeID, candidate.Normalized)) == id
					}
					if !match {
						continue
					}
					if target != nil {
						return result, groupDenied("uptime_target_identity_ambiguous")
					}
					target = candidate
				}
				evidence := map[string]any{"target_native_id": id, "source": evidenceSource}
				if source.Identity.NativeType == monitoringGroupType && kind != monitoringGroupType {
					evidence["member_interval"] = source.Normalized["_monitoring_group_member_interval"]
					evidence["member_configuration"] = source.Normalized["_monitoring_group_member_configuration"]
				}
				if target == nil {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: source.Identity.Provider, ConnectionID: source.Identity.ConnectionID, ControllerID: source.ID, NativeType: kind, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				} else {
					result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: source.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: graphSource, Confidence: 1, Evidence: evidence})
				}
			}
		}
	}
	return result, nil
}
