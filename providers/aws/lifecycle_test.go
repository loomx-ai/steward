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

func awsAsset(id, nativeType, nativeID string, normalized map[string]any) asset.Asset {
	if normalized == nil {
		normalized = map[string]any{}
	}
	return asset.Asset{ID: asset.AssetID(id), Location: "us-east-1", Normalized: normalized, Identity: asset.Identity{
		Provider: asset.ProviderAWS, Partition: "aws", ConnectionID: "connection-a", NativeType: nativeType, NativeID: nativeID,
	}}
}

func bindingFor(contribution map[asset.AssetID]graph.LifecycleBinding, managed string) (graph.LifecycleBinding, bool) {
	binding, ok := contribution[asset.AssetID(managed)]
	return binding, ok
}

func TestLifecycleContributorBindsAWSControllersAndOrdersDependents(t *testing.T) {
	assets := []asset.Asset{
		awsAsset("instance", "AWS::EC2::Instance", "i-1", map[string]any{
			ebsAttachmentsField: []any{
				map[string]any{"volume_id": "vol-root", "device_name": "/dev/xvda", "delete_on_termination": true},
				map[string]any{"volume_id": "vol-data", "device_name": "/dev/sdb", "delete_on_termination": false},
				map[string]any{"volume_id": "vol-missing", "device_name": "/dev/sdc", "delete_on_termination": true},
			},
			networkInterfaceAttachments: []any{map[string]any{"network_interface_id": "eni-primary", "attachment_id": "eni-attach-1", "device_index": 0, "delete_on_termination": true}},
		}),
		awsAsset("root", "AWS::EC2::Volume", "vol-root", nil),
		awsAsset("data", "AWS::EC2::Volume", "vol-data", nil),
		awsAsset("primary", "AWS::EC2::NetworkInterface", "eni-primary", nil),
		awsAsset("asg", "AWS::AutoScaling::AutoScalingGroup", "web", map[string]any{autoScalingInstancesField: []any{"i-1"}}),
		awsAsset("nodegroup", "AWS::EKS::Nodegroup", "prod|ng", map[string]any{nodegroupAutoScalingGroupField: []string{"web"}}),
		awsAsset("nat", "AWS::EC2::NatGateway", "nat-0abc", map[string]any{"AllocationId": "eipalloc-1"}),
		awsAsset("nat-eni", "AWS::EC2::NetworkInterface", "eni-nat", map[string]any{requesterManagedField: true, "description": "Interface for NAT Gateway nat-0abc"}),
		awsAsset("lb", "AWS::ElasticLoadBalancingV2::LoadBalancer", "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/50dc6c495c0c9188", nil),
		awsAsset("lb-eni", "AWS::EC2::NetworkInterface", "eni-lb", map[string]any{requesterManagedField: true, "owner_id": "123456789012", "description": "ELB app/web/50dc6c495c0c9188"}),
		awsAsset("fn", "AWS::Lambda::Function", "resize", nil),
		awsAsset("fn-eni", "AWS::EC2::NetworkInterface", "eni-fn", map[string]any{requesterManagedField: true, "description": "AWS Lambda VPC ENI-resize-2f6d8f2c-0b6f-4c1a-9e7b-3a3a1c2b4d5e"}),
		awsAsset("unknown-eni", "AWS::EC2::NetworkInterface", "eni-other", map[string]any{requesterManagedField: true, "description": "Something else"}),
		awsAsset("eip", "AWS::EC2::EIP", "203.0.113.10|eipalloc-1", map[string]any{"AllocationId": "eipalloc-1"}),
		awsAsset("instance-eip", "AWS::EC2::EIP", "203.0.113.11|eipalloc-2", map[string]any{"AllocationId": "eipalloc-2", "InstanceId": "i-1"}),
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[asset.AssetID]graph.LifecycleBinding{}
	for _, binding := range contribution.Bindings {
		if _, duplicate := bindings[binding.ManagedAssetID]; duplicate {
			t.Fatalf("duplicate binding for %s", binding.ManagedAssetID)
		}
		bindings[binding.ManagedAssetID] = binding
	}
	expect := map[string]string{"root": "instance", "primary": "instance", "instance": "asg", "asg": "nodegroup", "nat-eni": "nat", "lb-eni": "lb", "fn-eni": "fn"}
	for managed, controller := range expect {
		binding, ok := bindingFor(bindings, managed)
		if !ok || binding.ControllerAssetID != asset.AssetID(controller) || binding.CleanupPolicy != graph.CleanupDelegate || binding.Authority != graph.AuthorityAuthoritative {
			t.Errorf("binding for %s = %+v", managed, binding)
		}
	}
	if len(bindings) != len(expect) {
		t.Fatalf("bindings = %+v", bindings)
	}
	if bindings["fn-eni"].Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != false || bindings["asg"].Evidence["retention_supported"] != false {
		t.Fatalf("controller evidence fn=%+v asg=%+v", bindings["fn-eni"].Evidence, bindings["asg"].Evidence)
	}
	ordered := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		if relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] == graph.DeletionOrderTargetBeforeSource {
			ordered[string(relationship.SourceAssetID)+">"+string(relationship.TargetAssetID)] = true
		}
	}
	for _, want := range []string{"data>instance", "eip>nat", "instance-eip>instance"} {
		if !ordered[want] {
			t.Errorf("missing target-first order %s in %v", want, ordered)
		}
	}
	if len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != "vol-missing" || !contribution.Unresolved[0].BlocksCleanup {
		t.Fatalf("unresolved = %+v", contribution.Unresolved)
	}
}

func TestLifecycleContributorRejectsAmbiguousIdentities(t *testing.T) {
	assets := []asset.Asset{
		awsAsset("instance", "AWS::EC2::Instance", "i-1", map[string]any{ebsAttachmentsField: []any{map[string]any{"volume_id": "vol-1", "delete_on_termination": true}}}),
		awsAsset("a", "AWS::EC2::Volume", "vol-1", nil),
		awsAsset("b", "AWS::EC2::Volume", "vol-1", nil),
	}
	if _, err := NewLifecycle().Contribute(context.Background(), "scope", assets); err == nil {
		t.Fatal("duplicate native identities must not authorize a cascade")
	}
}

type fakeLifecycleEC2 struct {
	LifecycleEC2API
	eniDelete bool
	modified  []string
	volumes   map[string]bool
	enis      map[string]bool
}

func (f *fakeLifecycleEC2) DescribeInstances(context.Context, *awsec2.DescribeInstancesInput, ...func(*awsec2.Options)) (*awsec2.DescribeInstancesOutput, error) {
	return &awsec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
		InstanceId: awssdk.String("i-1"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
		NetworkInterfaces: []ec2types.InstanceNetworkInterface{{NetworkInterfaceId: awssdk.String("eni-1"), Attachment: &ec2types.InstanceNetworkInterfaceAttachment{AttachmentId: awssdk.String("eni-attach-1"), DeviceIndex: awssdk.Int32(0), DeleteOnTermination: awssdk.Bool(f.eniDelete)}}},
	}}}}}, nil
}

func (f *fakeLifecycleEC2) ModifyNetworkInterfaceAttribute(_ context.Context, input *awsec2.ModifyNetworkInterfaceAttributeInput, _ ...func(*awsec2.Options)) (*awsec2.ModifyNetworkInterfaceAttributeOutput, error) {
	f.modified = append(f.modified, awssdk.ToString(input.NetworkInterfaceId)+"|"+awssdk.ToString(input.Attachment.AttachmentId))
	if input.Attachment.DeleteOnTermination != nil {
		f.eniDelete = *input.Attachment.DeleteOnTermination
	}
	return &awsec2.ModifyNetworkInterfaceAttributeOutput{}, nil
}

func (f *fakeLifecycleEC2) DescribeNetworkInterfaces(_ context.Context, input *awsec2.DescribeNetworkInterfacesInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeNetworkInterfacesOutput, error) {
	if f.enis[input.NetworkInterfaceIds[0]] {
		return &awsec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: []ec2types.NetworkInterface{{NetworkInterfaceId: awssdk.String(input.NetworkInterfaceIds[0])}}}, nil
	}
	return nil, &APIError{Code: "InvalidNetworkInterfaceID.NotFound", StatusCode: 400}
}

func TestInstanceActionRetainsPrimaryInterfaceBeforeTermination(t *testing.T) {
	ec2 := &fakeLifecycleEC2{eniDelete: true, enis: map[string]bool{"eni-1": true}}
	cloud := &scriptedCloudControl{resources: map[string]CloudControlResource{"AWS::EC2::Instance|i-1": {Identifier: "i-1", Properties: `{"InstanceId":"i-1"}`}}}
	driver, err := newInstanceAction(&CloudControlAction{client: cloud}, ec2)
	if err != nil {
		t.Fatal(err)
	}
	instance := awsAsset("instance", "AWS::EC2::Instance", "i-1", map[string]any{
		"cloudControlIdentifier":    "i-1",
		networkInterfaceAttachments: []any{map[string]any{"network_interface_id": "eni-1", "attachment_id": "eni-attach-1", "device_index": 0, "delete_on_termination": true}},
	})
	request := contracts.ActionRequest{Asset: instance, Action: "delete", IdempotencyKey: "step", LifecycleImpacts: []contracts.ActionImpact{{
		Asset: awsAsset("eni", "AWS::EC2::NetworkInterface", "eni-1", nil), ControllerID: "instance", Delete: false,
	}}}
	if _, err := driver.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(ec2.modified) != 1 || ec2.modified[0] != "eni-1|eni-attach-1" || ec2.eniDelete || len(cloud.deletes) != 1 {
		t.Fatalf("modified=%v eniDelete=%v deletes=%v", ec2.modified, ec2.eniDelete, cloud.deletes)
	}
	// After termination the retained interface must still exist.
	cloud.resources = map[string]CloudControlResource{}
	if readback, err := driver.Readback(context.Background(), request); err != nil || readback.Exists {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	ec2.enis["eni-1"] = false
	if _, err := driver.Readback(context.Background(), request); err == nil {
		t.Fatal("a retained interface that disappeared must fail readback")
	}

	// Drift: the plan expected deletion, but the live policy was switched off.
	drift := &fakeLifecycleEC2{eniDelete: false, enis: map[string]bool{"eni-1": true}}
	driftDriver, _ := newInstanceAction(&CloudControlAction{client: &scriptedCloudControl{}}, drift)
	request.LifecycleImpacts[0].Delete = true
	if _, err := driftDriver.Execute(context.Background(), request); err == nil {
		t.Fatal("changed live deletion policy must stop termination")
	}
}

type pagedSnapshots struct {
	EC2NativeAPI
	count int
	calls []string
}

func (p *pagedSnapshots) DescribeSnapshots(_ context.Context, input *awsec2.DescribeSnapshotsInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeSnapshotsOutput, error) {
	p.calls = append(p.calls, awssdk.ToString(input.NextToken))
	output := &awsec2.DescribeSnapshotsOutput{}
	if awssdk.ToString(input.NextToken) == "" {
		for index := 0; index < p.count; index++ {
			output.Snapshots = append(output.Snapshots, ec2types.Snapshot{SnapshotId: awssdk.String("snap-" + string(rune('a'+index%26)) + string(rune('a'+index/26)))})
		}
		output.NextToken = awssdk.String("page-2")
		return output, nil
	}
	output.Snapshots = []ec2types.Snapshot{{SnapshotId: awssdk.String("snap-last")}}
	return output, nil
}

// Services may return more items than the batch limit; the cursor must resume
// inside the provider page and then continue with its token.
func TestNativeInventorySlicesOversizedProviderPages(t *testing.T) {
	api := &pagedSnapshots{count: 5}
	inventory := &NativeInventory{clients: &NativeClients{EC2: api}, kind: nativeKinds["AWS::EC2::Snapshot"]}
	kind := asset.ResourceKind{NativeType: "AWS::EC2::Snapshot"}
	request := contracts.InventoryRequest{ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"}, Limit: 2}
	var ids []string
	for step := 0; step < 6; step++ {
		batch, err := inventory.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Items) > 2 {
			t.Fatalf("batch exceeded limit: %d", len(batch.Items))
		}
		for _, item := range batch.Items {
			ids = append(ids, item.NativeID)
		}
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(ids) != 6 || ids[5] != "snap-last" {
		t.Fatalf("ids = %v calls = %v", ids, api.calls)
	}
	api.count = 1
	request.Cursor = encodeNativeCursor(nativeCursor{Offset: 4})
	if _, err := inventory.List(context.Background(), request); err == nil {
		t.Fatal("a shrunken provider page must restart the shard")
	}
}
