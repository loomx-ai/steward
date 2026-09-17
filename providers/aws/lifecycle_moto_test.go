package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// motoTerminatingCloudControl stands in for Cloud Control (which Moto does not
// implement) by terminating the instance through Moto's native EC2 API.
type motoTerminatingCloudControl struct {
	scriptedCloudControl
	ec2 *awsec2.Client
}

func (c *motoTerminatingCloudControl) GetResource(ctx context.Context, _, identifier string) (CloudControlResource, string, error) {
	output, err := c.ec2.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{InstanceIds: []string{identifier}})
	if err != nil || len(output.Reservations) == 0 || output.Reservations[0].Instances[0].State.Name == ec2types.InstanceStateNameTerminated {
		return CloudControlResource{}, "", &APIError{Code: "ResourceNotFoundException", StatusCode: 400}
	}
	return CloudControlResource{Identifier: identifier, Properties: `{"InstanceId":"` + identifier + `"}`}, "get", nil
}

func (c *motoTerminatingCloudControl) DeleteResource(ctx context.Context, _, identifier, _ string) (CloudControlProgress, string, error) {
	if _, err := c.ec2.TerminateInstances(ctx, &awsec2.TerminateInstancesInput{InstanceIds: []string{identifier}}); err != nil {
		return CloudControlProgress{}, "", err
	}
	return CloudControlProgress{RequestToken: "terminate", Status: "SUCCESS"}, "delete", nil
}

func TestMotoInstanceDeletionRetainsReviewedVolume(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	ec2 := awsec2.NewFromConfig(config)
	images, err := ec2.DescribeImages(ctx, &awsec2.DescribeImagesInput{Owners: []string{"amazon"}})
	if err != nil || len(images.Images) == 0 {
		t.Fatalf("Moto images=%v err=%v", images, err)
	}
	run, err := ec2.RunInstances(ctx, &awsec2.RunInstancesInput{
		ImageId: images.Images[0].ImageId, MinCount: awssdk.Int32(1), MaxCount: awssdk.Int32(1), InstanceType: ec2types.InstanceTypeT3Micro,
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{
			{DeviceName: awssdk.String("/dev/sda1"), Ebs: &ec2types.EbsBlockDevice{VolumeSize: awssdk.Int32(8), DeleteOnTermination: awssdk.Bool(true)}},
			{DeviceName: awssdk.String("/dev/sdb"), Ebs: &ec2types.EbsBlockDevice{VolumeSize: awssdk.Int32(10), DeleteOnTermination: awssdk.Bool(true)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := awssdk.ToString(run.Instances[0].InstanceId)
	items := []contracts.InventoryItem{{NativeType: "AWS::EC2::Instance", NativeID: instanceID, Normalized: map[string]any{}}}
	clients := newNativeClients(config)
	if err := enrichLifecycleFacts(ctx, clients, items); err != nil {
		t.Fatal(err)
	}
	volumes, _, err := decodeAttachments[ebsAttachment](items[0].Normalized[ebsAttachmentsField])
	if err != nil || len(volumes) != 2 || !volumes[0].DeleteOnTermination || !volumes[1].DeleteOnTermination {
		t.Fatalf("enriched volumes=%+v err=%v", volumes, err)
	}
	var root, data ebsAttachment
	for _, volume := range volumes {
		if volume.DeviceName == "/dev/sda1" {
			root = volume
		} else {
			data = volume
		}
	}

	// The graph contributor produces one delegate binding per attachment.
	instanceAsset := asset.Asset{ID: "instance", Location: "us-east-1", Normalized: items[0].Normalized, Identity: asset.Identity{Provider: asset.ProviderAWS, ConnectionID: "connection-moto", NativeType: "AWS::EC2::Instance", NativeID: instanceID}}
	rootAsset := asset.Asset{ID: "root", Location: "us-east-1", Identity: asset.Identity{Provider: asset.ProviderAWS, ConnectionID: "connection-moto", NativeType: "AWS::EC2::Volume", NativeID: root.VolumeID}}
	dataAsset := asset.Asset{ID: "data", Location: "us-east-1", Identity: asset.Identity{Provider: asset.ProviderAWS, ConnectionID: "connection-moto", NativeType: "AWS::EC2::Volume", NativeID: data.VolumeID}}
	contribution, err := NewLifecycle().Contribute(ctx, "scope", []asset.Asset{instanceAsset, rootAsset, dataAsset})
	if err != nil || len(contribution.Bindings) != 2 || contribution.Bindings[0].CleanupPolicy != graph.CleanupDelegate {
		t.Fatalf("contribution=%+v err=%v", contribution, err)
	}

	base := &CloudControlAction{client: &motoTerminatingCloudControl{ec2: ec2}}
	driver, err := newInstanceAction(base, clients.Lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: instanceAsset, Action: "delete", IdempotencyKey: "step-instance",
		LifecycleImpacts: []contracts.ActionImpact{{Asset: rootAsset, ControllerID: "instance", Delete: true}, {Asset: dataAsset, ControllerID: "instance", Delete: false}},
	}
	request.Asset.Normalized["cloudControlIdentifier"] = instanceID
	preflight, err := driver.Preflight(ctx, request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if retained, _ := preflight.Evidence["retain_attachments"].([]string); len(retained) != 1 || retained[0] != data.VolumeID {
		t.Fatalf("retain evidence = %+v", preflight.Evidence)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	readback, err := driver.Readback(ctx, request)
	if err != nil || readback.Exists {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	remaining, err := ec2.DescribeVolumes(ctx, &awsec2.DescribeVolumesInput{Filters: []ec2types.Filter{{Name: awssdk.String("volume-id"), Values: []string{root.VolumeID, data.VolumeID}}}})
	if err != nil || len(remaining.Volumes) != 1 || awssdk.ToString(remaining.Volumes[0].VolumeId) != data.VolumeID {
		t.Fatalf("remaining volumes=%+v err=%v", remaining, err)
	}
}

func TestMotoInstanceDeletionRejectsUnreviewedAttachment(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	ec2 := awsec2.NewFromConfig(config)
	images, _ := ec2.DescribeImages(ctx, &awsec2.DescribeImagesInput{Owners: []string{"amazon"}})
	run, err := ec2.RunInstances(ctx, &awsec2.RunInstancesInput{ImageId: images.Images[0].ImageId, MinCount: awssdk.Int32(1), MaxCount: awssdk.Int32(1)})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := awssdk.ToString(run.Instances[0].InstanceId)
	items := []contracts.InventoryItem{{NativeType: "AWS::EC2::Instance", NativeID: instanceID, Normalized: map[string]any{}}}
	clients := newNativeClients(config)
	if err := enrichLifecycleFacts(ctx, clients, items); err != nil {
		t.Fatal(err)
	}
	// A volume attached after the plan was reviewed must stop termination.
	volume, err := ec2.CreateVolume(ctx, &awsec2.CreateVolumeInput{AvailabilityZone: run.Instances[0].Placement.AvailabilityZone, Size: awssdk.Int32(5)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ec2.AttachVolume(ctx, &awsec2.AttachVolumeInput{InstanceId: awssdk.String(instanceID), VolumeId: volume.VolumeId, Device: awssdk.String("/dev/sdf")}); err != nil {
		t.Fatal(err)
	}
	driver, _ := newInstanceAction(&CloudControlAction{client: &motoTerminatingCloudControl{ec2: ec2}}, clients.Lifecycle)
	request := contracts.ActionRequest{Asset: asset.Asset{ID: "instance", Normalized: items[0].Normalized, Identity: asset.Identity{Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Instance", NativeID: instanceID}}, Action: "delete", IdempotencyKey: "step"}
	request.Asset.Normalized["cloudControlIdentifier"] = instanceID
	volumes, _, _ := decodeAttachments[ebsAttachment](items[0].Normalized[ebsAttachmentsField])
	for _, planned := range volumes {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: "instance", Delete: true, Asset: asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Volume", NativeID: planned.VolumeID}}})
	}
	if _, err := driver.Execute(ctx, request); err == nil {
		t.Fatal("unreviewed attachment must block termination")
	}
	described, _ := ec2.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if described.Reservations[0].Instances[0].State.Name == ec2types.InstanceStateNameTerminated {
		t.Fatal("instance was terminated despite the attachment mismatch")
	}
}

func TestMotoVPNGatewayDetachesBeforeDelete(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	ec2 := awsec2.NewFromConfig(config)
	vpc, err := ec2.CreateVpc(ctx, &awsec2.CreateVpcInput{CidrBlock: awssdk.String("10.20.0.0/16")})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := ec2.CreateVpnGateway(ctx, &awsec2.CreateVpnGatewayInput{Type: ec2types.GatewayTypeIpsec1})
	if err != nil {
		t.Fatal(err)
	}
	gatewayID := awssdk.ToString(gateway.VpnGateway.VpnGatewayId)
	if _, err := ec2.AttachVpnGateway(ctx, &awsec2.AttachVpnGatewayInput{VpcId: vpc.Vpc.VpcId, VpnGatewayId: gateway.VpnGateway.VpnGatewayId}); err != nil {
		t.Fatal(err)
	}
	network := &networkSDK{client: ec2}
	cloud := &scriptedCloudControl{}
	driver := &vpnGatewayAction{CloudControlAction: &CloudControlAction{client: cloud}, network: network}
	request := contracts.ActionRequest{Asset: asset.Asset{Normalized: map[string]any{"cloudControlIdentifier": gatewayID}, Identity: asset.Identity{Provider: asset.ProviderAWS, NativeType: "AWS::EC2::VPNGateway", NativeID: gatewayID}}, Action: "delete", IdempotencyKey: "step"}
	if _, err := driver.Execute(ctx, request); err != nil {
		t.Fatal(err)
	}
	remaining, err := network.VPNGatewayVPCs(ctx, gatewayID)
	if err != nil || len(remaining) != 0 || len(cloud.deletes) != 1 {
		t.Fatalf("remaining=%v deletes=%v err=%v", remaining, cloud.deletes, err)
	}
}
