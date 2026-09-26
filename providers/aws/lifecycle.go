package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsautoscaling "github.com/aws/aws-sdk-go-v2/service/autoscaling"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awseks "github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	lifecycleEvidenceSource        = "aws:lifecycle"
	ebsAttachmentsField            = "ebs_attachments"
	networkInterfaceAttachments    = "network_interface_attachments"
	autoScalingInstancesField      = "auto_scaling_instance_ids"
	nodegroupAutoScalingGroupField = "auto_scaling_group_names"
	requesterManagedField          = "requester_managed"
	describeBatchSize              = 200
)

type LifecycleEC2API interface {
	DescribeInstances(context.Context, *awsec2.DescribeInstancesInput, ...func(*awsec2.Options)) (*awsec2.DescribeInstancesOutput, error)
	ModifyInstanceAttribute(context.Context, *awsec2.ModifyInstanceAttributeInput, ...func(*awsec2.Options)) (*awsec2.ModifyInstanceAttributeOutput, error)
	DescribeNetworkInterfaces(context.Context, *awsec2.DescribeNetworkInterfacesInput, ...func(*awsec2.Options)) (*awsec2.DescribeNetworkInterfacesOutput, error)
	ModifyNetworkInterfaceAttribute(context.Context, *awsec2.ModifyNetworkInterfaceAttributeInput, ...func(*awsec2.Options)) (*awsec2.ModifyNetworkInterfaceAttributeOutput, error)
	DescribeVolumes(context.Context, *awsec2.DescribeVolumesInput, ...func(*awsec2.Options)) (*awsec2.DescribeVolumesOutput, error)
}

type AutoScalingNativeAPI interface {
	DescribeAutoScalingGroups(context.Context, *awsautoscaling.DescribeAutoScalingGroupsInput, ...func(*awsautoscaling.Options)) (*awsautoscaling.DescribeAutoScalingGroupsOutput, error)
}

type EKSNativeAPI interface {
	DescribeNodegroup(context.Context, *awseks.DescribeNodegroupInput, ...func(*awseks.Options)) (*awseks.DescribeNodegroupOutput, error)
}

// ebsAttachment and interfaceAttachment are the live deletion policies that
// decide whether terminating an instance deletes a separately inventoried
// resource.
type ebsAttachment struct {
	VolumeID            string `json:"volume_id"`
	DeviceName          string `json:"device_name"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
}

type interfaceAttachment struct {
	NetworkInterfaceID  string `json:"network_interface_id"`
	AttachmentID        string `json:"attachment_id"`
	DeviceIndex         int32  `json:"device_index"`
	DeleteOnTermination bool   `json:"delete_on_termination"`
}

func liveInstanceAttachments(instance map[string]any) ([]ebsAttachment, []interfaceAttachment) {
	var volumes []ebsAttachment
	for _, raw := range anySlice(instance["BlockDeviceMappings"]) {
		mapping, _ := raw.(map[string]any)
		ebs, _ := mapping["Ebs"].(map[string]any)
		if id := stringValue(ebs["VolumeId"]); id != "" {
			volumes = append(volumes, ebsAttachment{VolumeID: id, DeviceName: stringValue(mapping["DeviceName"]), DeleteOnTermination: ebs["DeleteOnTermination"] == true})
		}
	}
	var interfaces []interfaceAttachment
	for _, raw := range anySlice(instance["NetworkInterfaces"]) {
		value, _ := raw.(map[string]any)
		attachment, _ := value["Attachment"].(map[string]any)
		if id := stringValue(value["NetworkInterfaceId"]); id != "" {
			index := int32(0)
			if number, ok := attachment["DeviceIndex"].(interface{ Int64() (int64, error) }); ok {
				parsed, _ := number.Int64()
				index = int32(parsed)
			}
			interfaces = append(interfaces, interfaceAttachment{NetworkInterfaceID: id, AttachmentID: stringValue(attachment["AttachmentId"]), DeviceIndex: index, DeleteOnTermination: attachment["DeleteOnTermination"] == true})
		}
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].VolumeID < volumes[j].VolumeID })
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].NetworkInterfaceID < interfaces[j].NetworkInterfaceID })
	return volumes, interfaces
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func attachmentDocuments[T any](values []T) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		document, err := nativeDocument(value)
		if err == nil {
			result = append(result, document)
		}
	}
	return result
}

func lifecycleFactKind(nativeType string) bool {
	switch nativeType {
	case "AWS::EC2::Instance", "AWS::EC2::NetworkInterface", "AWS::AutoScaling::AutoScalingGroup", "AWS::EKS::Nodegroup", "AWS::S3::Bucket",
		"AWS::Config::ConfigRule", "AWS::Config::ConformancePack", secretType:
		return true
	}
	return false
}

// enrichLifecycleFacts adds native attachment and membership facts that Cloud
// Control models omit. A failed read fails the batch rather than dropping the
// policy that authorizes a cascade.
func enrichLifecycleFacts(ctx context.Context, clients *NativeClients, items []contracts.InventoryItem) error {
	byType := map[string][]int{}
	for index, item := range items {
		byType[item.NativeType] = append(byType[item.NativeType], index)
	}
	if indexes := byType["AWS::EC2::Instance"]; len(indexes) > 0 {
		if err := enrichInstances(ctx, clients.Lifecycle, items, indexes); err != nil {
			return err
		}
	}
	if indexes := byType["AWS::EC2::NetworkInterface"]; len(indexes) > 0 {
		if err := enrichNetworkInterfaces(ctx, clients.Lifecycle, items, indexes); err != nil {
			return err
		}
	}
	if indexes := byType["AWS::AutoScaling::AutoScalingGroup"]; len(indexes) > 0 {
		if err := enrichAutoScalingGroups(ctx, clients.AutoScaling, items, indexes); err != nil {
			return err
		}
	}
	if indexes := byType["AWS::Config::ConfigRule"]; len(indexes) > 0 {
		if err := enrichConfigRules(ctx, clients.Config, items, indexes); err != nil {
			return err
		}
	}
	if indexes := byType["AWS::Config::ConformancePack"]; len(indexes) > 0 {
		if err := enrichConformancePacks(ctx, clients.Config, items, indexes); err != nil {
			return err
		}
	}
	if indexes := byType[secretType]; len(indexes) > 0 {
		if err := enrichSecrets(ctx, clients.Secrets, items[indexes[0]].Location, items, indexes); err != nil {
			return err
		}
	}
	for _, index := range byType["AWS::S3::Bucket"] {
		if err := enrichBucketContents(ctx, clients.S3, &items[index]); err != nil {
			return err
		}
	}
	for _, index := range byType["AWS::EKS::Nodegroup"] {
		cluster := stringValue(items[index].Normalized["ClusterName"])
		name := stringValue(items[index].Normalized["NodegroupName"])
		if cluster == "" || name == "" {
			return fmt.Errorf("AWS EKS node group %s lacks cluster or name", items[index].NativeID)
		}
		execution.LogCloudAPIRequest(ctx, "eks", "DescribeNodegroup", rawCloudPayload(map[string]any{"clusterName": cluster, "nodegroupName": name}))
		output, err := clients.EKS.DescribeNodegroup(ctx, &awseks.DescribeNodegroupInput{ClusterName: awssdk.String(cluster), NodegroupName: awssdk.String(name)})
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "eks", "DescribeNodegroup", err)
			if nativeNotFound(err, "ResourceNotFoundException") {
				continue
			}
			return NormalizeError(err)
		}
		var groups []string
		if output.Nodegroup != nil && output.Nodegroup.Resources != nil {
			for _, group := range output.Nodegroup.Resources.AutoScalingGroups {
				if name := awssdk.ToString(group.Name); name != "" {
					groups = append(groups, name)
				}
			}
		}
		sort.Strings(groups)
		items[index].Normalized[nodegroupAutoScalingGroupField] = groups
	}
	return nil
}

func enrichInstances(ctx context.Context, client LifecycleEC2API, items []contracts.InventoryItem, indexes []int) error {
	for start := 0; start < len(indexes); start += describeBatchSize {
		batch := indexes[start:min(start+describeBatchSize, len(indexes))]
		ids := make([]string, 0, len(batch))
		for _, index := range batch {
			ids = append(ids, items[index].NativeID)
		}
		found, err := describeInstanceDocuments(ctx, client, ids)
		if err != nil {
			return err
		}
		for _, index := range batch {
			instance, ok := found[items[index].NativeID]
			if !ok {
				continue
			}
			volumes, interfaces := liveInstanceAttachments(instance)
			items[index].Normalized[ebsAttachmentsField] = attachmentDocuments(volumes)
			items[index].Normalized[networkInterfaceAttachments] = attachmentDocuments(interfaces)
			if state := stringValue(nestedValue(instance, "State", "Name")); state != "" {
				items[index].State = state
				items[index].Normalized["state"] = state
			}
		}
	}
	return nil
}

func describeInstanceDocuments(ctx context.Context, client LifecycleEC2API, ids []string) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	token := ""
	for {
		input := &awsec2.DescribeInstancesInput{InstanceIds: ids}
		if token != "" {
			input.NextToken = awssdk.String(token)
		}
		execution.LogCloudAPIRequest(ctx, "ec2", "DescribeInstances", rawCloudPayload(map[string]any{"InstanceIds": ids, "NextToken": token}))
		output, err := client.DescribeInstances(ctx, input)
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "ec2", "DescribeInstances", err)
			if nativeNotFound(err, "InvalidInstanceID.NotFound") {
				if len(ids) == 1 {
					return result, nil
				}
				// One terminated-and-purged instance fails the whole batch;
				// resolve the rest individually instead of dropping their facts.
				for _, id := range ids {
					single, err := describeInstanceDocuments(ctx, client, []string{id})
					if err != nil {
						return nil, err
					}
					for key, value := range single {
						result[key] = value
					}
				}
				return result, nil
			}
			return nil, NormalizeError(err)
		}
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				document, err := nativeDocument(instance)
				if err != nil {
					return nil, err
				}
				result[awssdk.ToString(instance.InstanceId)] = document
			}
		}
		if token = awssdk.ToString(output.NextToken); token == "" {
			return result, nil
		}
	}
}

func enrichNetworkInterfaces(ctx context.Context, client LifecycleEC2API, items []contracts.InventoryItem, indexes []int) error {
	for start := 0; start < len(indexes); start += describeBatchSize {
		batch := indexes[start:min(start+describeBatchSize, len(indexes))]
		ids := make([]string, 0, len(batch))
		for _, index := range batch {
			ids = append(ids, items[index].NativeID)
		}
		execution.LogCloudAPIRequest(ctx, "ec2", "DescribeNetworkInterfaces", rawCloudPayload(map[string]any{"NetworkInterfaceIds": ids}))
		output, err := client.DescribeNetworkInterfaces(ctx, &awsec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: ids})
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "ec2", "DescribeNetworkInterfaces", err)
			return NormalizeError(err)
		}
		found := map[string]map[string]any{}
		for _, value := range output.NetworkInterfaces {
			document, err := nativeDocument(value)
			if err != nil {
				return err
			}
			found[awssdk.ToString(value.NetworkInterfaceId)] = document
		}
		for _, index := range batch {
			live, ok := found[items[index].NativeID]
			if !ok {
				continue
			}
			normalized := items[index].Normalized
			normalized[requesterManagedField] = live["RequesterManaged"] == true
			normalized["interface_type"] = stringValue(live["InterfaceType"])
			normalized["description"] = stringValue(live["Description"])
			normalized["requester_id"] = stringValue(live["RequesterId"])
			normalized["owner_id"] = stringValue(live["OwnerId"])
			if attachment, ok := live["Attachment"].(map[string]any); ok {
				normalized["attachment"] = attachment
			}
			// AWS services own requester-managed interfaces; only their
			// controllers can delete them.
			if live["RequesterManaged"] == true {
				actionable := false
				items[index].Actionable = &actionable
			}
		}
	}
	return nil
}

func enrichAutoScalingGroups(ctx context.Context, client AutoScalingNativeAPI, items []contracts.InventoryItem, indexes []int) error {
	for start := 0; start < len(indexes); start += 50 {
		batch := indexes[start:min(start+50, len(indexes))]
		names := make([]string, 0, len(batch))
		for _, index := range batch {
			names = append(names, items[index].NativeID)
		}
		members := map[string][]string{}
		token := ""
		for {
			input := &awsautoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: names}
			if token != "" {
				input.NextToken = awssdk.String(token)
			}
			execution.LogCloudAPIRequest(ctx, "autoscaling", "DescribeAutoScalingGroups", rawCloudPayload(map[string]any{"AutoScalingGroupNames": names, "NextToken": token}))
			output, err := client.DescribeAutoScalingGroups(ctx, input)
			if err != nil {
				execution.LogCloudAPIFailure(ctx, "autoscaling", "DescribeAutoScalingGroups", err)
				return NormalizeError(err)
			}
			for _, group := range output.AutoScalingGroups {
				name := awssdk.ToString(group.AutoScalingGroupName)
				members[name] = []string{}
				for _, instance := range group.Instances {
					members[name] = append(members[name], awssdk.ToString(instance.InstanceId))
				}
			}
			if token = awssdk.ToString(output.NextToken); token == "" {
				break
			}
		}
		for _, index := range batch {
			if instances, ok := members[items[index].NativeID]; ok {
				sort.Strings(instances)
				items[index].Normalized[autoScalingInstancesField] = instances
			}
		}
	}
	return nil
}

// Lifecycle derives AWS controller/member bindings and deletion order from
// inventoried native facts. It performs no API calls.
type Lifecycle struct{}

func NewLifecycle() *Lifecycle { return &Lifecycle{} }

type assetIndex struct {
	byTypeID map[string][]asset.Asset
}

func indexAWSAssets(assets []asset.Asset) assetIndex {
	index := assetIndex{byTypeID: map[string][]asset.Asset{}}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAWS || value.ClosedAt != nil {
			continue
		}
		index.byTypeID[value.Identity.NativeType+"\x00"+value.Identity.NativeID] = append(index.byTypeID[value.Identity.NativeType+"\x00"+value.Identity.NativeID], value)
	}
	return index
}

// find resolves a native identity in the controller's connection and partition.
func (i assetIndex) find(controller asset.Asset, nativeType, nativeID string) (asset.Asset, bool, error) {
	var matched []asset.Asset
	for _, candidate := range i.byTypeID[nativeType+"\x00"+nativeID] {
		if candidate.Identity.ConnectionID == controller.Identity.ConnectionID && candidate.Identity.Partition == controller.Identity.Partition && sameRegion(candidate, controller) {
			matched = append(matched, candidate)
		}
	}
	if len(matched) > 1 {
		return asset.Asset{}, false, fmt.Errorf("ambiguous AWS %s identity %s", nativeType, nativeID)
	}
	if len(matched) == 0 {
		return asset.Asset{}, false, nil
	}
	return matched[0], true, nil
}

func sameRegion(left, right asset.Asset) bool {
	return strings.TrimSpace(left.Location) == "" || strings.TrimSpace(right.Location) == "" || left.Location == right.Location
}

func (*Lifecycle) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	index := indexAWSAssets(assets)
	result := governance.Contribution{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAWS || value.ClosedAt != nil {
			continue
		}
		var err error
		switch value.Identity.NativeType {
		case "AWS::EC2::Instance":
			err = contributeInstanceAttachments(&result, index, value)
		case "AWS::AutoScaling::AutoScalingGroup":
			err = contributeManagedMembers(&result, index, value, autoScalingInstancesField, "AWS::EC2::Instance", "aws_auto_scaling_group_instance")
		case "AWS::EKS::Nodegroup":
			err = contributeManagedMembers(&result, index, value, nodegroupAutoScalingGroupField, "AWS::AutoScaling::AutoScalingGroup", "aws_eks_nodegroup_auto_scaling_group")
		case "AWS::EC2::NetworkInterface":
			err = contributeRequesterManagedInterface(&result, index, value)
		case "AWS::EC2::EIP":
			err = contributeAddressOrder(&result, index, value, assets)
		}
		if err != nil {
			return governance.Contribution{}, err
		}
	}
	if err := contributeChildDependencies(&result, index, assets); err != nil {
		return governance.Contribution{}, err
	}
	contributeKMSReferences(&result, assets)
	contributeKMSPolicyAdministrators(&result, assets)
	return result, nil
}

func decodeAttachments[T any](value any) ([]T, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	var result []T
	payload, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(payload, &result)
	}
	if err != nil {
		return nil, true, fmt.Errorf("invalid AWS attachment facts: %w", err)
	}
	return result, true, nil
}

func contributeInstanceAttachments(result *governance.Contribution, index assetIndex, instance asset.Asset) error {
	volumes, known, err := decodeAttachments[ebsAttachment](instance.Normalized[ebsAttachmentsField])
	if err != nil {
		return err
	}
	interfaces, interfacesKnown, err := decodeAttachments[interfaceAttachment](instance.Normalized[networkInterfaceAttachments])
	if err != nil {
		return err
	}
	if !known && !interfacesKnown {
		return nil
	}
	type attachment struct {
		nativeType, nativeID, kind string
		deleteOnTermination        bool
		evidence                   map[string]any
	}
	var attachments []attachment
	for _, volume := range volumes {
		attachments = append(attachments, attachment{"AWS::EC2::Volume", volume.VolumeID, "aws_ebs_delete_on_termination", volume.DeleteOnTermination, map[string]any{"device_name": volume.DeviceName}})
	}
	for _, value := range interfaces {
		attachments = append(attachments, attachment{"AWS::EC2::NetworkInterface", value.NetworkInterfaceID, "aws_eni_delete_on_termination", value.DeleteOnTermination, map[string]any{"attachment_id": value.AttachmentID, "device_index": value.DeviceIndex}})
	}
	for _, item := range attachments {
		evidence := map[string]any{"source": "ec2:DescribeInstances", "instance_id": instance.Identity.NativeID, "resource_type": item.nativeType, "resource_id": item.nativeID, "delete_on_termination": item.deleteOnTermination, "lifecycle_kind": item.kind}
		for key, value := range item.evidence {
			evidence[key] = value
		}
		managed, found, err := index.find(instance, item.nativeType, item.nativeID)
		if err != nil {
			return err
		}
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
				BlocksCleanup: item.deleteOnTermination, Provider: asset.ProviderAWS, ConnectionID: instance.Identity.ConnectionID,
				NativeType: item.nativeType, NativeID: item.nativeID, ControllerID: instance.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence,
			})
			continue
		}
		if item.deleteOnTermination {
			evidence["delete_by_default"] = true
			evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
			evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: instance.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative,
				Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate,
				EvidenceSource: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
			})
		} else {
			evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: managed.ID, TargetAssetID: instance.ID, Type: graph.RelationshipAttachedTo,
			Source: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
		})
	}
	return nil
}

// Auto Scaling groups replace terminated members and EKS replaces its managed
// groups, so members are cleaned only through their controller.
func contributeManagedMembers(result *governance.Contribution, index assetIndex, controller asset.Asset, field, memberType, kind string) error {
	members, ok := controller.Normalized[field]
	if !ok {
		return nil
	}
	for _, id := range stringSliceValue(members) {
		evidence := map[string]any{"source": field, "controller_id": controller.Identity.NativeID, "member_id": id, "lifecycle_kind": kind, "retention_supported": false}
		managed, found, err := index.find(controller, memberType, id)
		if err != nil {
			return err
		}
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
				BlocksCleanup: true, Provider: asset.ProviderAWS, ConnectionID: controller.Identity.ConnectionID,
				NativeType: memberType, NativeID: id, ControllerID: controller.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence,
			})
			continue
		}
		evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: controller.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative,
			Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate,
			EvidenceSource: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
		})
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: managed.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf,
			Source: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
		})
	}
	return nil
}

var requesterDescriptions = []struct {
	pattern    *regexp.Regexp
	nativeType string
	identity   func([]string, asset.Asset) string
}{
	{regexp.MustCompile(`^Interface for NAT Gateway (nat-[0-9a-f]+)$`), "AWS::EC2::NatGateway", func(m []string, _ asset.Asset) string { return m[1] }},
	{regexp.MustCompile(`^VPC Endpoint Interface (vpce-[0-9a-f]+)$`), "AWS::EC2::VPCEndpoint", func(m []string, _ asset.Asset) string { return m[1] }},
	{regexp.MustCompile(`^EFS mount target for fs-[0-9a-f]+ \((fsmt-[0-9a-f]+)\)$`), "AWS::EFS::MountTarget", func(m []string, _ asset.Asset) string { return m[1] }},
	{regexp.MustCompile(`^AWS Lambda VPC ENI-(.+)-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`), "AWS::Lambda::Function", func(m []string, _ asset.Asset) string { return m[1] }},
	{regexp.MustCompile(`^ELB ((?:app|net|gwy)/[^/]+/[0-9a-f]+)$`), "AWS::ElasticLoadBalancingV2::LoadBalancer", func(m []string, value asset.Asset) string {
		region := strings.TrimSpace(value.Location)
		owner := stringValue(value.Normalized["owner_id"])
		if region == "" || owner == "" {
			return ""
		}
		return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s", region, owner, m[1])
	}},
}

func contributeRequesterManagedInterface(result *governance.Contribution, index assetIndex, eni asset.Asset) error {
	if eni.Normalized[requesterManagedField] != true {
		return nil
	}
	description := stringValue(eni.Normalized["description"])
	for _, rule := range requesterDescriptions {
		match := rule.pattern.FindStringSubmatch(description)
		if match == nil {
			continue
		}
		controllerID := rule.identity(match, eni)
		evidence := map[string]any{"source": "ec2:DescribeNetworkInterfaces", "description": description, "interface_type": eni.Normalized["interface_type"], "controller_type": rule.nativeType, "controller_native_id": controllerID, "lifecycle_kind": "aws_requester_managed_interface", "retention_supported": false}
		if controllerID == "" {
			return nil
		}
		controller, found, err := index.find(eni, rule.nativeType, controllerID)
		if err != nil || !found {
			return err
		}
		// Lambda releases its interfaces asynchronously after function deletion.
		evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = rule.nativeType != "AWS::Lambda::Function"
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: controller.ID, ManagedAssetID: eni.ID, Authority: graph.AuthorityAuthoritative,
			Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate,
			EvidenceSource: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
		})
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: eni.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf,
			Source: lifecycleEvidenceSource, Evidence: evidence, Confidence: 1,
		})
		return nil
	}
	return nil
}

// An associated Elastic IP cannot be released until its NAT gateway or
// instance releases the association.
func contributeAddressOrder(result *governance.Contribution, index assetIndex, eip asset.Asset, assets []asset.Asset) error {
	allocation := stringValue(eip.Normalized["AllocationId"])
	if instanceID := stringValue(eip.Normalized["InstanceId"]); instanceID != "" {
		if instance, found, err := index.find(eip, "AWS::EC2::Instance", instanceID); err != nil {
			return err
		} else if found {
			result.Relationships = append(result.Relationships, addressRelationship(eip, instance, "InstanceId"))
		}
	}
	if allocation == "" {
		return nil
	}
	for _, candidate := range assets {
		if candidate.Identity.Provider != asset.ProviderAWS || candidate.Identity.NativeType != "AWS::EC2::NatGateway" || candidate.ClosedAt != nil ||
			candidate.Identity.ConnectionID != eip.Identity.ConnectionID || !sameRegion(candidate, eip) {
			continue
		}
		allocations := append([]string{stringValue(candidate.Normalized["AllocationId"])}, stringSliceValue(candidate.Normalized["SecondaryAllocationIds"])...)
		for _, id := range allocations {
			if id == allocation {
				result.Relationships = append(result.Relationships, addressRelationship(eip, candidate, "AllocationId"))
			}
		}
	}
	return nil
}

func addressRelationship(eip, holder asset.Asset, field string) graph.Relationship {
	return graph.Relationship{
		SourceAssetID: eip.ID, TargetAssetID: holder.ID, Type: graph.RelationshipAttachedTo, Source: lifecycleEvidenceSource, Confidence: 1,
		Evidence: map[string]any{"source": field, "allocation_id": stringValue(eip.Normalized["AllocationId"]), graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource},
	}
}

func stringSliceValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

// A bucket that still holds object versions or delete markers cannot be deleted
// by Cloud Control. Recording it at scan time shows the blocker in the plan;
// the delete guard checks the live bucket again before deletion.
func enrichBucketContents(ctx context.Context, client S3NativeAPI, item *contracts.InventoryItem) error {
	outcome, err := s3BucketGuard(client)(ctx, item.NativeID)
	if err != nil {
		var providerError *contracts.ProviderCallError
		if errors.As(err, &providerError) && (providerError.Provider.Code == "PermanentRedirect" || providerError.Provider.Code == "AuthorizationHeaderMalformed") {
			return nil // A bucket in another region is checked by its own regional scan.
		}
		return err
	}
	if outcome.pending {
		return nil
	}
	empty, _ := outcome.evidence["bucket_empty"].(bool)
	item.Normalized["bucket_empty"] = empty
	if !empty {
		item.Normalized["cleanup_protected"] = true
		item.Normalized["cleanup_protection_reason"] = "s3_bucket_not_empty"
	}
	return nil
}
