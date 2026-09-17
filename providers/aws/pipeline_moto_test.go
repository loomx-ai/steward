package aws

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

// motoCloudControl is a synthetic Cloud Control translation over Moto's native
// EC2 API for instances and volumes. Moto has no Cloud Control service, so this
// adapter is protocol scaffolding: property names follow the CloudFormation
// schemas, while resource state and deletion effects come from the emulator.
type motoCloudControl struct {
	ec2 *awsec2.Client
}

func (c *motoCloudControl) ListResources(ctx context.Context, request CloudControlListRequest) (CloudControlPage, error) {
	page := CloudControlPage{RequestID: "moto-list"}
	switch request.TypeName {
	case "AWS::EC2::Instance":
		output, err := c.ec2.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{Filters: []ec2types.Filter{{Name: awssdk.String("instance-state-name"), Values: []string{"pending", "running", "stopped"}}}})
		if err != nil {
			return page, err
		}
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				page.Resources = append(page.Resources, CloudControlResource{Identifier: awssdk.ToString(instance.InstanceId)})
			}
		}
	case "AWS::EC2::Volume":
		output, err := c.ec2.DescribeVolumes(ctx, &awsec2.DescribeVolumesInput{})
		if err != nil {
			return page, err
		}
		for _, volume := range output.Volumes {
			page.Resources = append(page.Resources, CloudControlResource{Identifier: awssdk.ToString(volume.VolumeId)})
		}
	default:
		return page, &APIError{Code: "TypeNotFoundException", StatusCode: 400}
	}
	return page, nil
}

func (c *motoCloudControl) GetResource(ctx context.Context, typeName, identifier string) (CloudControlResource, string, error) {
	missing := &APIError{Code: "ResourceNotFoundException", StatusCode: 400, RequestID: "moto-missing"}
	var properties map[string]any
	switch typeName {
	case "AWS::EC2::Instance":
		output, err := c.ec2.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{InstanceIds: []string{identifier}})
		if err != nil || len(output.Reservations) == 0 {
			return CloudControlResource{}, "moto-missing", missing
		}
		instance := output.Reservations[0].Instances[0]
		if instance.State.Name == ec2types.InstanceStateNameTerminated {
			return CloudControlResource{}, "moto-missing", missing
		}
		volumes := []any{}
		for _, mapping := range instance.BlockDeviceMappings {
			volumes = append(volumes, map[string]any{"VolumeId": awssdk.ToString(mapping.Ebs.VolumeId), "Device": awssdk.ToString(mapping.DeviceName)})
		}
		groups := []any{}
		for _, group := range instance.SecurityGroups {
			groups = append(groups, awssdk.ToString(group.GroupId))
		}
		properties = map[string]any{"InstanceId": identifier, "SubnetId": awssdk.ToString(instance.SubnetId), "VpcId": awssdk.ToString(instance.VpcId), "SecurityGroupIds": groups, "Volumes": volumes, "ImageId": awssdk.ToString(instance.ImageId)}
	case "AWS::EC2::Volume":
		output, err := c.ec2.DescribeVolumes(ctx, &awsec2.DescribeVolumesInput{VolumeIds: []string{identifier}})
		if err != nil || len(output.Volumes) == 0 {
			return CloudControlResource{}, "moto-missing", missing
		}
		volume := output.Volumes[0]
		properties = map[string]any{"VolumeId": identifier, "AvailabilityZone": awssdk.ToString(volume.AvailabilityZone), "Size": awssdk.ToInt32(volume.Size), "SnapshotId": awssdk.ToString(volume.SnapshotId)}
	default:
		return CloudControlResource{}, "", &APIError{Code: "TypeNotFoundException", StatusCode: 400}
	}
	payload, _ := json.Marshal(properties)
	return CloudControlResource{Identifier: identifier, Properties: string(payload)}, "moto-get", nil
}

func (c *motoCloudControl) DeleteResource(ctx context.Context, typeName, identifier, token string) (CloudControlProgress, string, error) {
	var err error
	switch typeName {
	case "AWS::EC2::Instance":
		_, err = c.ec2.TerminateInstances(ctx, &awsec2.TerminateInstancesInput{InstanceIds: []string{identifier}})
	case "AWS::EC2::Volume":
		_, err = c.ec2.DeleteVolume(ctx, &awsec2.DeleteVolumeInput{VolumeId: awssdk.String(identifier)})
	}
	if err != nil {
		return CloudControlProgress{}, "", err
	}
	return CloudControlProgress{RequestToken: token, Identifier: identifier, Status: "IN_PROGRESS"}, "moto-delete", nil
}

func (c *motoCloudControl) GetResourceRequestStatus(_ context.Context, token string) (CloudControlProgress, string, error) {
	return CloudControlProgress{RequestToken: token, Status: "SUCCESS"}, "moto-status", nil
}

func (c *motoCloudControl) UpdateResource(context.Context, CloudControlUpdateRequest) (CloudControlProgress, string, error) {
	return CloudControlProgress{}, "", errors.New("not used")
}

type awsLifecycleResolver struct{}

func (awsLifecycleResolver) ResolveContributors(context.Context, asset.CloudConnection, []asset.Asset) ([]governance.Contributor, error) {
	return []governance.Contributor{NewLifecycle()}, nil
}

func motoPipelineRuntime(t *testing.T, config awssdk.Config) *Runtime {
	t.Helper()
	source := &runtimeCredentialSource{want: "connection", value: contracts.Credential{Values: map[string]string{"access_key_id": "testing", "secret_access_key": "testing"}}}
	runtime, err := newRuntime(source, &runtimeFactory{cloudControl: &motoCloudControl{ec2: awsec2.NewFromConfig(config)}, native: newNativeClients(config)})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func pipelineRegistry(t *testing.T, runtime *Runtime) *providerruntime.Registry {
	t.Helper()
	registry := providerruntime.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(runtime.Bundle()); err != nil {
		t.Fatal(err)
	}
	return registry
}

func pipelineScan(t *testing.T, repository *sqlite.Repositories, registry *providerruntime.Registry, runtime *Runtime, kinds []string) map[string]asset.Asset {
	t.Helper()
	ctx := t.Context()
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	ids := []asset.ResourceKindID{}
	for _, kind := range kinds {
		ids = append(ids, runtime.resourceKind(kind, asset.ScopeRegion).ID)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "aws-pipeline", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"us-east-1"}, ResourceKindIDs: ids})
	if err != nil || len(created.Shards) != len(kinds) {
		t.Fatalf("scan shards=%d err=%v", len(created.Shards), err)
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil {
			t.Fatal("scan", err)
		}
	}
	for _, shard := range created.Shards {
		stored, err := repository.GetScanShard(ctx, shard.ID)
		if err != nil || stored.Status != asset.ShardSucceeded || !stored.Coverage.Complete {
			t.Fatalf("shard %s status=%s coverage=%+v err=%v", shard.Source, stored.Status, stored.Coverage, err)
		}
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			if err := governance.NewGraphHandler(repository, registry, awsLifecycleResolver{}).Handle(ctx, job); err != nil {
				t.Fatal("graph", err)
			}
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]asset.Asset{}
	for _, value := range values {
		result[value.Identity.NativeID] = value
	}
	return result
}

// Full application evidence against the emulator: scan, graph, plan with an
// explicitly retained attachment, restartable execution and reconciliation.
func TestMotoApplicationPipelineRetainsVolumeAndDeletesImageChain(t *testing.T) {
	config := motoConfig(t)
	ctx := t.Context()
	ec2 := awsec2.NewFromConfig(config)
	images, err := ec2.DescribeImages(ctx, &awsec2.DescribeImagesInput{Owners: []string{"amazon"}})
	if err != nil || len(images.Images) == 0 {
		t.Fatal("Moto public images unavailable", err)
	}
	run, err := ec2.RunInstances(ctx, &awsec2.RunInstancesInput{
		ImageId: images.Images[0].ImageId, MinCount: awssdk.Int32(1), MaxCount: awssdk.Int32(1),
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{
			{DeviceName: awssdk.String("/dev/sda1"), Ebs: &ec2types.EbsBlockDevice{VolumeSize: awssdk.Int32(8), DeleteOnTermination: awssdk.Bool(true)}},
			{DeviceName: awssdk.String("/dev/sdb"), Ebs: &ec2types.EbsBlockDevice{VolumeSize: awssdk.Int32(20), DeleteOnTermination: awssdk.Bool(true)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := awssdk.ToString(run.Instances[0].InstanceId)
	var rootID, dataID string
	for _, mapping := range run.Instances[0].BlockDeviceMappings {
		if awssdk.ToString(mapping.DeviceName) == "/dev/sdb" {
			dataID = awssdk.ToString(mapping.Ebs.VolumeId)
		} else {
			rootID = awssdk.ToString(mapping.Ebs.VolumeId)
		}
	}
	snapshot, err := ec2.CreateSnapshot(ctx, &awsec2.CreateSnapshotInput{VolumeId: awssdk.String(dataID)})
	if err != nil {
		t.Fatal(err)
	}
	image, err := ec2.RegisterImage(ctx, &awsec2.RegisterImageInput{Name: awssdk.String("pipeline"), RootDeviceName: awssdk.String("/dev/xvda"), BlockDeviceMappings: []ec2types.BlockDeviceMapping{{DeviceName: awssdk.String("/dev/xvda"), Ebs: &ec2types.EbsBlockDevice{SnapshotId: snapshot.SnapshotId}}}})
	if err != nil {
		t.Fatal(err)
	}
	imageID, snapshotID := awssdk.ToString(image.ImageId), awssdk.ToString(snapshot.SnapshotId)

	path := filepath.Join(t.TempDir(), "aws-pipeline.db")
	repository, err := sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, err := range []error{
		repository.PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderAWS, Partition: "aws", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}),
		repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: "connection", Provider: asset.ProviderAWS, Type: asset.CredentialAWSAccessKey, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}),
		repository.PutScope(ctx, asset.Scope{ID: "root", ConnectionID: "connection", Kind: asset.ScopeAccount, NativeID: "123456789012", CreatedAt: now, UpdatedAt: now}),
		repository.PutRegion(ctx, asset.ConnectionRegion{ID: "us-east-1", ConnectionID: "connection", RegionID: "us-east-1", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime := motoPipelineRuntime(t, config)
	registry := pipelineRegistry(t, runtime)
	kinds := []string{"AWS::EC2::Instance", "AWS::EC2::Volume", "AWS::EC2::Snapshot", "AWS::EC2::Image"}
	assets := pipelineScan(t, repository, registry, runtime, kinds)
	for _, id := range []string{instanceID, rootID, dataID, snapshotID, imageID} {
		if _, ok := assets[id]; !ok {
			t.Fatalf("scan missed %s in %d assets", id, len(assets))
		}
	}
	bindings, err := repository.ListLifecycleBindingsByConnection(ctx, "connection")
	if err != nil || len(bindings) < 2 {
		t.Fatalf("instance bindings=%+v err=%v", bindings, err)
	}

	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		ConnectionID: "connection", CreatedBy: "operator",
		Selectors:      []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: assets[instanceID].ID}, {Kind: plan.SelectorAsset, AssetID: assets[imageID].ID}, {Kind: plan.SelectorAsset, AssetID: assets[snapshotID].ID}},
		RequestOptions: map[asset.AssetID]map[string]any{assets[instanceID].ID: {"retain_resources": []string{dataID}}},
	})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 3 {
		t.Fatalf("plan status=%s steps=%d blockers=%+v err=%v", task.Task.Status, len(task.Steps), task.Task.Blockers, err)
	}
	expected := map[asset.AssetID]plan.ExpectedOutcome{}
	for _, impact := range task.ImpactItems {
		expected[impact.AssetID] = impact.Expected
	}
	if expected[assets[rootID].ID] != plan.ExpectedDelegatedDelete || expected[assets[dataID].ID] != plan.ExpectedRetainExplicit {
		t.Fatalf("impacts = %+v", task.ImpactItems)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "aws-pipeline", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	completed := map[plan.StepID]bool{}
	restarts := 0
	for range task.Steps {
		var active plan.CleanupTaskStep
		for _, candidate := range task.Steps {
			if !completed[candidate.ID] && !slices.ContainsFunc(candidate.DependsOn, func(id plan.StepID) bool { return !completed[id] }) {
				active = candidate
				break
			}
		}
		if active.ID == "" {
			t.Fatal("no executable step")
		}
		jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
		if err != nil {
			t.Fatal(err)
		}
		var job execution.Job
		for _, candidate := range jobs {
			if candidate.Payload["cleanup_task_step_id"] == string(active.ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("persisted job missing for", active.AssetID)
		}
		for round := 0; round < 20 && !completed[active.ID]; round++ {
			// Each round restarts the worker from the database and a fresh runtime.
			repository, err = sqlite.Open(path, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh := motoPipelineRuntime(t, config)
			restarted := pipelineRegistry(t, fresh)
			resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
			})
			worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, restarted), resolver)
			var retry *cleanup.RetryError
			if err := worker.Handle(ctx, job); err != nil && !errors.As(err, &retry) {
				current, _ := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
				t.Fatalf("worker %s: %v provider=%+v", active.AssetID, err, current.ProviderError)
			}
			restarts++
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
			if err != nil {
				t.Fatal(err)
			}
			if current.ProviderError != nil {
				t.Fatalf("provider error for %s: %+v", active.AssetID, current.ProviderError)
			}
			if current.Status == execution.ActionSucceeded {
				completed[active.ID] = true
			}
		}
		if !completed[active.ID] {
			t.Fatal("step never completed", active.AssetID)
		}
	}
	instance, _ := ec2.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if instance.Reservations[0].Instances[0].State.Name != ec2types.InstanceStateNameTerminated {
		t.Fatal("instance was not terminated")
	}
	volumes, _ := ec2.DescribeVolumes(ctx, &awsec2.DescribeVolumesInput{Filters: []ec2types.Filter{{Name: awssdk.String("volume-id"), Values: []string{rootID, dataID}}}})
	if len(volumes.Volumes) != 1 || awssdk.ToString(volumes.Volumes[0].VolumeId) != dataID {
		t.Fatalf("volume outcomes = %+v", volumes.Volumes)
	}
	active, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, value := range active {
		remaining[value.Identity.NativeID] = true
	}
	if remaining[instanceID] || remaining[rootID] || remaining[imageID] || remaining[snapshotID] || !remaining[dataID] {
		t.Fatalf("reconciled active assets = %v", remaining)
	}
	journal, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(journal) != 3 {
		t.Fatalf("journal=%d err=%v", len(journal), err)
	}
	// A new scan agrees with the execution reconciliation.
	rescanned := pipelineScan(t, repository, registry, runtime, kinds)
	if _, ok := rescanned[dataID]; !ok || rescanned[rootID].ID != "" || rescanned[instanceID].ID != "" {
		t.Fatalf("rescan = %v", rescanned)
	}
	t.Logf("verified 3 AWS cleanup steps with %d worker restarts", restarts)
}
