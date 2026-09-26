package aws

import (
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const dependencyEvidenceSource = "aws:cleanup-dependencies"

// childDependency is the documented deletion contract between a parent kind
// and a child kind that names the parent in field.
type childDependency struct {
	parentType, childType, field, kind string
	mode                               childDependencyMode
	// automatic lets the parent's deletion select the prerequisite. Without it
	// the child must be selected separately, otherwise the plan is blocked.
	automatic bool
	// applies narrows the rule to some children, for example routes that
	// were added manually.
	applies func(asset.Asset) bool
}

type childDependencyMode int

const (
	// The service rejects the parent's deletion while the child exists.
	childRequired childDependencyMode = iota
	// Deleting the parent deletes the child; the child can also be deleted
	// directly.
	childCascade
	// Only the parent's deletion removes the child.
	childManaged
)

var childDependencies = []childDependency{
	// DeleteClientVpnEndpoint: "You must disassociate all target networks
	// before you can delete a Client VPN endpoint."
	{parentType: clientVPNEndpointType, childType: clientVPNAssociationType, field: "ClientVpnEndpointId", kind: "aws_client_vpn_target_network", mode: childRequired, automatic: true},
	// Authorization rules and manually added routes are endpoint
	// configuration and are removed with the endpoint.
	{parentType: clientVPNEndpointType, childType: clientVPNRuleType, field: "ClientVpnEndpointId", kind: "aws_client_vpn_authorization_rule", mode: childCascade},
	{parentType: clientVPNEndpointType, childType: clientVPNRouteType, field: "ClientVpnEndpointId", kind: "aws_client_vpn_route", mode: childCascade, applies: func(route asset.Asset) bool {
		return !strings.EqualFold(stringValue(route.Normalized["Origin"]), clientVPNRouteOriginAssociate)
	}},
	// Unsubscribe is independent, and DeleteTopic deletes all subscriptions
	// to the topic.
	{parentType: "AWS::SNS::Topic", childType: "AWS::SNS::Subscription", field: "TopicArn", kind: "aws_sns_subscription", mode: childCascade},
	// DeleteConfigRule fails with ResourceInUseException while a remediation
	// action is associated with the rule.
	{parentType: "AWS::Config::ConfigRule", childType: "AWS::Config::RemediationConfiguration", field: "ConfigRuleName", kind: "aws_config_remediation", mode: childRequired, automatic: true},
	// DeregisterWorkspaceDirectory: "If any WorkSpaces are registered to this
	// directory, you must remove them before you can deregister the directory."
	// Terminating a WorkSpace destroys its user volume, so it is never selected
	// implicitly.
	{parentType: workspaceDirectoryType, childType: "AWS::WorkSpaces::Workspace", field: "DirectoryId", kind: "aws_workspaces_directory_member", mode: childRequired},
	// A usage plan's keys are removed with the plan.
	{parentType: "AWS::ApiGateway::UsagePlan", childType: "AWS::ApiGateway::UsagePlanKey", field: "UsagePlanId", kind: "aws_api_gateway_usage_plan_key", mode: childCascade},
	// DeleteTrustStore fails while a listener's mutual TLS configuration uses
	// the trust store. The listener may be reconfigured instead of deleted, so
	// it is never selected implicitly.
	{parentType: "AWS::ElasticLoadBalancingV2::TrustStore", childType: "AWS::ElasticLoadBalancingV2::Listener", field: "trust_store_arn", kind: "aws_elbv2_trust_store_listener", mode: childRequired},
}

// globalReference is a use of a global resource, such as an IAM role, that a
// regional resource needs while it is deleted. Spec relationships resolve only
// within one scope, so these are matched here across the global scope.
type globalReference struct {
	sourceType, field, targetType, kind string
}

var globalReferences = []globalReference{
	// EMR terminates cluster instances with its service role, and the
	// instances run under the cluster's instance profile until they stop.
	{sourceType: emrClusterType, field: "service_role_name", targetType: "AWS::IAM::Role", kind: "aws_emr_service_role"},
	{sourceType: emrClusterType, field: "instance_profile_name", targetType: "AWS::IAM::InstanceProfile", kind: "aws_emr_instance_profile"},
}

func contributeGlobalReferences(result *governance.Contribution, index assetIndex, source asset.Asset) error {
	for _, reference := range globalReferences {
		if source.Identity.NativeType != reference.sourceType {
			continue
		}
		name := strings.TrimSpace(stringValue(source.Normalized[reference.field]))
		if name == "" {
			continue
		}
		target, found, err := index.find(source, reference.targetType, name)
		if err != nil {
			return err
		}
		evidence := map[string]any{"source": dependencyEvidenceSource, "lifecycle_kind": reference.kind, "field": reference.field, "target_id": name}
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
				Provider: asset.ProviderAWS, ConnectionID: source.Identity.ConnectionID, NativeType: reference.targetType, NativeID: name,
				ControllerID: source.ID, Relationship: graph.RelationshipUses, Evidence: evidence,
			})
			continue
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: source.ID, TargetAssetID: target.ID, Type: graph.RelationshipUses,
			Source: dependencyEvidenceSource, Evidence: evidence, Confidence: 1,
		})
	}
	return nil
}

func contributeChildDependencies(result *governance.Contribution, index assetIndex, assets []asset.Asset) error {
	for _, child := range assets {
		if child.Identity.Provider != asset.ProviderAWS || child.ClosedAt != nil {
			continue
		}
		for _, rule := range childDependencies {
			if child.Identity.NativeType != rule.childType || (rule.applies != nil && !rule.applies(child)) {
				continue
			}
			parentID := strings.TrimSpace(stringValue(child.Normalized[rule.field]))
			if parentID == "" {
				continue
			}
			parent, found, err := index.find(child, rule.parentType, parentID)
			if err != nil {
				return err
			}
			if !found {
				continue // The parent is not inventoried, so it is not deleted.
			}
			addChildDependency(result, rule.kind, rule.mode, rule.automatic, parent, child, map[string]any{"field": rule.field})
		}
		if err := contributeGlobalReferences(result, index, child); err != nil {
			return err
		}
		switch child.Identity.NativeType {
		case "AWS::Config::ConfigRule":
			if err := contributeConformancePackRule(result, index, child); err != nil {
				return err
			}
		case clientVPNRouteType:
			if err := contributeAssociationRoute(result, assets, child); err != nil {
				return err
			}
		case "AWS::EC2::Instance":
			if err := contributeEMRInstance(result, index, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func addChildDependency(result *governance.Contribution, kind string, mode childDependencyMode, automatic bool, parent, child asset.Asset, extra map[string]any) {
	evidence := map[string]any{
		"source": dependencyEvidenceSource, "lifecycle_kind": kind, "resource_type": child.Identity.NativeType,
		"parent_id": parent.Identity.NativeID, "child_id": child.Identity.NativeID,
	}
	for key, value := range extra {
		evidence[key] = value
	}
	if mode == childRequired {
		evidence[graph.RelationshipEvidenceRequiredDeletion] = true
		evidence[graph.RelationshipEvidenceAutomaticSelection] = automatic
		evidence[graph.RelationshipEvidenceAuthority] = string(graph.AuthorityAuthoritative)
		evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: parent.ID, TargetAssetID: child.ID, Type: graph.RelationshipDependsOn,
			Source: dependencyEvidenceSource, Evidence: evidence, Confidence: 1,
		})
		return
	}
	evidence["delete_by_default"] = true
	evidence["retention_supported"] = false
	evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
	if mode == childCascade {
		evidence[graph.LifecycleEvidenceNativeDeleteEffect] = true
	}
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{
		ControllerAssetID: parent.ID, ManagedAssetID: child.ID, Authority: graph.AuthorityAuthoritative,
		Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: mode == childCascade,
		EvidenceSource: dependencyEvidenceSource, Evidence: evidence, Confidence: 1,
	})
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: child.ID, TargetAssetID: parent.ID, Type: graph.RelationshipMemberOf,
		Source: dependencyEvidenceSource, Evidence: evidence, Confidence: 1,
	})
}

// Rules a conformance pack deploys are deleted only with the pack. The pack's
// compliance listing names them.
func contributeConformancePackRule(result *governance.Contribution, index assetIndex, rule asset.Asset) error {
	if stringValue(rule.Normalized[configRuleCreatedByField]) != conformancePackService {
		return nil
	}
	for _, candidates := range index.byTypeID {
		for _, pack := range candidates {
			if pack.Identity.NativeType != "AWS::Config::ConformancePack" || pack.Identity.ConnectionID != rule.Identity.ConnectionID || !sameRegion(pack, rule) {
				continue
			}
			for _, name := range stringSliceValue(pack.Normalized[conformancePackRulesField]) {
				if name == rule.Identity.NativeID {
					addChildDependency(result, "aws_config_conformance_pack_rule", childManaged, false, pack, rule, map[string]any{"field": conformancePackRulesField})
					return nil
				}
			}
		}
	}
	return nil
}

// Routes added with a subnet association are removed by disassociating that
// subnet from the same endpoint.
func contributeAssociationRoute(result *governance.Contribution, assets []asset.Asset, route asset.Asset) error {
	if !strings.EqualFold(stringValue(route.Normalized["Origin"]), clientVPNRouteOriginAssociate) {
		return nil
	}
	endpoint, subnet := stringValue(route.Normalized["ClientVpnEndpointId"]), stringValue(route.Normalized["TargetSubnet"])
	var matched []asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider == asset.ProviderAWS && candidate.ClosedAt == nil && candidate.Identity.NativeType == clientVPNAssociationType &&
			candidate.Identity.ConnectionID == route.Identity.ConnectionID && sameRegion(candidate, route) &&
			stringValue(candidate.Normalized["ClientVpnEndpointId"]) == endpoint && stringValue(candidate.Normalized["TargetNetworkId"]) == subnet {
			matched = append(matched, candidate)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	if len(matched) != 1 {
		return nil
	}
	addChildDependency(result, "aws_client_vpn_association_route", childManaged, false, matched[0], route, map[string]any{"field": "TargetSubnet"})
	return nil
}

// EMR launches its cluster instances and terminates them with the cluster;
// the instances carry the cluster ID in a system tag.
func contributeEMRInstance(result *governance.Contribution, index assetIndex, instance asset.Asset) error {
	clusterID := strings.TrimSpace(instance.Tags[emrClusterTag])
	if clusterID == "" {
		return nil
	}
	cluster, found, err := index.find(instance, emrClusterType, clusterID)
	if err != nil || !found {
		return err
	}
	addChildDependency(result, "aws_emr_cluster_instance", childManaged, false, cluster, instance, map[string]any{"tag": emrClusterTag})
	return nil
}
