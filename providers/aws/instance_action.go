package aws

import (
	"context"
	"errors"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const instanceAttachmentWait = 10 * time.Second

// instanceAction wraps the Cloud Control instance delete with EBS volume and
// network interface DeleteOnTermination changes chosen in the reviewed plan.
type instanceAction struct {
	*CloudControlAction
	ec2 LifecycleEC2API
}

type attachmentOutcome struct {
	nativeType, nativeID string
	deviceName           string
	attachmentID         string
	delete               bool
	liveDelete           bool
}

func attachmentDenied(reason string) error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorConflict, Code: "AttachmentPlanMismatch", Message: "AWS instance attachments differ from the reviewed plan: " + reason,
	}}
}

// plannedOutcomes compares frozen attachments, live attachments and the
// reviewed impacts. It returns the per-resource outcome or a denial reason.
func plannedOutcomes(request contracts.ActionRequest, live map[string]any) ([]attachmentOutcome, string) {
	plannedVolumes, _, err := decodeAttachments[ebsAttachment](request.Asset.Normalized[ebsAttachmentsField])
	if err != nil {
		return nil, "invalid_planned_attachments"
	}
	plannedInterfaces, _, err := decodeAttachments[interfaceAttachment](request.Asset.Normalized[networkInterfaceAttachments])
	if err != nil {
		return nil, "invalid_planned_attachments"
	}
	liveVolumes, liveInterfaces := liveInstanceAttachments(live)
	if len(liveVolumes) != len(plannedVolumes) || len(liveInterfaces) != len(plannedInterfaces) {
		return nil, "attached_resources_changed"
	}
	var outcomes []attachmentOutcome
	for index, planned := range plannedVolumes {
		current := liveVolumes[index]
		if current.VolumeID != planned.VolumeID || current.DeviceName != planned.DeviceName {
			return nil, "attached_resources_changed"
		}
		outcomes = append(outcomes, attachmentOutcome{nativeType: "AWS::EC2::Volume", nativeID: planned.VolumeID, deviceName: planned.DeviceName, delete: planned.DeleteOnTermination, liveDelete: current.DeleteOnTermination})
	}
	for index, planned := range plannedInterfaces {
		current := liveInterfaces[index]
		if current.NetworkInterfaceID != planned.NetworkInterfaceID || current.AttachmentID != planned.AttachmentID {
			return nil, "attached_resources_changed"
		}
		outcomes = append(outcomes, attachmentOutcome{nativeType: "AWS::EC2::NetworkInterface", nativeID: planned.NetworkInterfaceID, attachmentID: planned.AttachmentID, delete: planned.DeleteOnTermination, liveDelete: current.DeleteOnTermination})
	}
	for index := range outcomes {
		outcome := &outcomes[index]
		plannedDelete := outcome.delete
		if plannedDelete {
			found := false
			for _, impact := range request.LifecycleImpacts {
				identity := impact.Asset.Identity
				if impact.ControllerID == request.Asset.ID && identity.Provider == asset.ProviderAWS && identity.NativeType == outcome.nativeType &&
					identity.NativeID == outcome.nativeID && identity.ConnectionID == request.Asset.Identity.ConnectionID && identity.Partition == request.Asset.Identity.Partition {
					if found {
						return nil, "ambiguous_lifecycle_impact"
					}
					found, outcome.delete = true, impact.Delete
				}
			}
			if !found {
				return nil, "attached_resource_missing_from_plan"
			}
		}
		// Live policy may only have moved toward the reviewed retention.
		if outcome.liveDelete != plannedDelete && (outcome.delete || outcome.liveDelete) {
			return nil, "attached_resources_changed"
		}
	}
	return outcomes, ""
}

func (a *instanceAction) liveInstance(ctx context.Context, id string) (map[string]any, bool, error) {
	found, err := describeInstanceDocuments(ctx, a.ec2, []string{id})
	if err != nil {
		return nil, false, err
	}
	instance, ok := found[id]
	if !ok || stringValue(nestedValue(instance, "State", "Name")) == "terminated" {
		return nil, false, nil
	}
	return instance, true, nil
}

func (a *instanceAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	result, err := a.CloudControlAction.Preflight(ctx, request)
	if err != nil || result.Absent || !result.Allowed {
		return result, err
	}
	live, exists, err := a.liveInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !exists {
		return contracts.PreflightResult{Absent: true, Reason: "instance is terminated"}, nil
	}
	outcomes, reason := plannedOutcomes(request, live)
	if reason != "" {
		return contracts.PreflightResult{Allowed: false, Reason: "attached resources differ from the reviewed plan", Evidence: map[string]any{"attachment_check": reason}}, nil
	}
	retained := []string{}
	for _, outcome := range outcomes {
		if outcome.liveDelete && !outcome.delete {
			retained = append(retained, outcome.nativeID)
		}
	}
	result.Evidence["retain_attachments"] = retained
	return result, nil
}

func (a *instanceAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	live, exists, err := a.liveInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if exists {
		outcomes, reason := plannedOutcomes(request, live)
		if reason != "" {
			return contracts.ActionResult{}, attachmentDenied(reason)
		}
		changed := false
		for _, outcome := range outcomes {
			if !outcome.liveDelete || outcome.delete {
				continue
			}
			changed = true
			if err := a.retain(ctx, request.Asset.Identity.NativeID, outcome); err != nil {
				return contracts.ActionResult{}, err
			}
		}
		if changed {
			// Terminate only after live readback shows every retained policy.
			live, exists, err = a.liveInstance(ctx, request.Asset.Identity.NativeID)
			if err != nil {
				return contracts.ActionResult{}, err
			}
			if exists {
				outcomes, reason = plannedOutcomes(request, live)
				if reason != "" {
					return contracts.ActionResult{}, attachmentDenied(reason)
				}
				for _, outcome := range outcomes {
					if outcome.liveDelete != outcome.delete {
						return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
							Category: execution.ErrorRetryable, Code: "RetentionNotApplied", Message: "AWS has not applied DeleteOnTermination for " + outcome.nativeID,
						}, RetryAfter: instanceAttachmentWait}
					}
				}
			}
		}
	}
	result, err := a.CloudControlAction.Execute(ctx, request)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (a *instanceAction) retain(ctx context.Context, instanceID string, outcome attachmentOutcome) error {
	switch outcome.nativeType {
	case "AWS::EC2::Volume":
		execution.LogCloudAPIRequest(ctx, "ec2", "ModifyInstanceAttribute", rawCloudPayload(map[string]any{"InstanceId": instanceID, "DeviceName": outcome.deviceName, "DeleteOnTermination": false}))
		_, err := a.ec2.ModifyInstanceAttribute(ctx, &awsec2.ModifyInstanceAttributeInput{
			InstanceId: awssdk.String(instanceID),
			BlockDeviceMappings: []ec2types.InstanceBlockDeviceMappingSpecification{{
				DeviceName: awssdk.String(outcome.deviceName), Ebs: &ec2types.EbsInstanceBlockDeviceSpecification{VolumeId: awssdk.String(outcome.nativeID), DeleteOnTermination: awssdk.Bool(false)},
			}},
		})
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "ec2", "ModifyInstanceAttribute", err)
			return NormalizeError(err)
		}
	case "AWS::EC2::NetworkInterface":
		execution.LogCloudAPIRequest(ctx, "ec2", "ModifyNetworkInterfaceAttribute", rawCloudPayload(map[string]any{"NetworkInterfaceId": outcome.nativeID, "AttachmentId": outcome.attachmentID, "DeleteOnTermination": false}))
		_, err := a.ec2.ModifyNetworkInterfaceAttribute(ctx, &awsec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: awssdk.String(outcome.nativeID),
			Attachment:         &ec2types.NetworkInterfaceAttachmentChanges{AttachmentId: awssdk.String(outcome.attachmentID), DeleteOnTermination: awssdk.Bool(false)},
		})
		if err != nil {
			execution.LogCloudAPIFailure(ctx, "ec2", "ModifyNetworkInterfaceAttribute", err)
			return NormalizeError(err)
		}
	}
	return nil
}

// Wait finishes only when the instance is terminated and every attachment
// reached its reviewed outcome: deleted resources absent, retained present.
func (a *instanceAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	wait, err := a.CloudControlAction.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		return wait, err
	}
	pending, err := a.attachmentOutcomesPending(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if len(pending) > 0 {
		data := wait.Data
		if data == nil {
			data = map[string]any{}
		}
		data["pending_attachments"] = pending
		return contracts.WaitResult{Done: false, RetryAfter: instanceAttachmentWait, State: "waiting_for_attached_resources", Data: data}, nil
	}
	return wait, nil
}

func (a *instanceAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	readback, err := a.CloudControlAction.Readback(ctx, request)
	if err != nil || readback.Exists {
		return readback, err
	}
	pending, err := a.attachmentOutcomesPending(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if len(pending) > 0 {
		return contracts.ReadbackResult{Exists: true, State: "attached_resources_pending", Data: map[string]any{"pending_attachments": pending}}, nil
	}
	return readback, nil
}

func (a *instanceAction) attachmentOutcomesPending(ctx context.Context, request contracts.ActionRequest) ([]string, error) {
	plannedVolumes, _, err := decodeAttachments[ebsAttachment](request.Asset.Normalized[ebsAttachmentsField])
	if err != nil {
		return nil, err
	}
	plannedInterfaces, _, err := decodeAttachments[interfaceAttachment](request.Asset.Normalized[networkInterfaceAttachments])
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for _, volume := range plannedVolumes {
		expected["AWS::EC2::Volume\x00"+volume.VolumeID] = volume.DeleteOnTermination
	}
	for _, value := range plannedInterfaces {
		expected["AWS::EC2::NetworkInterface\x00"+value.NetworkInterfaceID] = value.DeleteOnTermination
	}
	for _, impact := range request.LifecycleImpacts {
		key := impact.Asset.Identity.NativeType + "\x00" + impact.Asset.Identity.NativeID
		if _, ok := expected[key]; ok && impact.ControllerID == request.Asset.ID {
			expected[key] = impact.Delete
		}
	}
	var pending []string
	for _, volume := range plannedVolumes {
		exists, err := a.volumeExists(ctx, volume.VolumeID)
		if err != nil {
			return nil, err
		}
		if pendingOutcome(exists, expected["AWS::EC2::Volume\x00"+volume.VolumeID]) {
			if !expected["AWS::EC2::Volume\x00"+volume.VolumeID] {
				return nil, attachmentDenied("retained volume " + volume.VolumeID + " no longer exists")
			}
			pending = append(pending, volume.VolumeID)
		}
	}
	for _, value := range plannedInterfaces {
		exists, err := a.interfaceExists(ctx, value.NetworkInterfaceID)
		if err != nil {
			return nil, err
		}
		if pendingOutcome(exists, expected["AWS::EC2::NetworkInterface\x00"+value.NetworkInterfaceID]) {
			if !expected["AWS::EC2::NetworkInterface\x00"+value.NetworkInterfaceID] {
				return nil, attachmentDenied("retained network interface " + value.NetworkInterfaceID + " no longer exists")
			}
			pending = append(pending, value.NetworkInterfaceID)
		}
	}
	return pending, nil
}

func pendingOutcome(exists, shouldDelete bool) bool {
	return exists == shouldDelete
}

func (a *instanceAction) volumeExists(ctx context.Context, id string) (bool, error) {
	output, err := a.ec2.DescribeVolumes(ctx, &awsec2.DescribeVolumesInput{VolumeIds: []string{id}})
	if nativeNotFound(err, "InvalidVolume.NotFound") {
		return false, nil
	}
	if err != nil {
		return false, NormalizeError(err)
	}
	for _, volume := range output.Volumes {
		if awssdk.ToString(volume.VolumeId) == id && volume.State != ec2types.VolumeStateDeleted {
			return true, nil
		}
	}
	return false, nil
}

func (a *instanceAction) interfaceExists(ctx context.Context, id string) (bool, error) {
	output, err := a.ec2.DescribeNetworkInterfaces(ctx, &awsec2.DescribeNetworkInterfacesInput{NetworkInterfaceIds: []string{id}})
	if nativeNotFound(err, "InvalidNetworkInterfaceID.NotFound") {
		return false, nil
	}
	if err != nil {
		return false, NormalizeError(err)
	}
	return len(output.NetworkInterfaces) > 0, nil
}

var errInstanceActionUnavailable = errors.New("AWS instance lifecycle client is required")

func newInstanceAction(base *CloudControlAction, ec2 LifecycleEC2API) (*instanceAction, error) {
	if base == nil || ec2 == nil {
		return nil, errInstanceActionUnavailable
	}
	return &instanceAction{CloudControlAction: base, ec2: ec2}, nil
}
