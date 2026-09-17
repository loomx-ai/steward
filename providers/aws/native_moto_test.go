package aws

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsfsx "github.com/aws/aws-sdk-go-v2/service/fsx"
	fsxtypes "github.com/aws/aws-sdk-go-v2/service/fsx/types"
	awsopensearch "github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Emulator evidence: an independent Moto server implements the native EC2,
// OpenSearch and FSx APIs. Moto 5.2.3 rejects the docdb engine and encodes
// DMS timestamps as strings, so DocumentDB and DMS use protocol fixtures. Resources are created with the
// official SDK, then discovered and deleted only through the Steward runtime.
//
//	uvx --from 'moto[server]==5.2.3' moto_server -H 127.0.0.1 -p 5055
//	STEWARD_AWS_MOTO_URL=http://127.0.0.1:5055 go test ./providers/aws -run Moto
func motoConfig(t *testing.T) awssdk.Config {
	t.Helper()
	endpoint := os.Getenv("STEWARD_AWS_MOTO_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_AWS_MOTO_URL to a loopback Moto server")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "" {
		t.Fatal("Moto must use a loopback HTTP origin")
	}
	return awssdk.Config{
		Region: "us-east-1", BaseEndpoint: awssdk.String(endpoint), RetryMaxAttempts: 1,
		Credentials: awscredentials.NewStaticCredentialsProvider("testing", "testing", ""),
	}
}

type motoRuntime struct {
	runtime *Runtime
	factory *runtimeFactory
}

func newMotoRuntime(t *testing.T, config awssdk.Config) motoRuntime {
	t.Helper()
	source := &runtimeCredentialSource{want: "connection-moto", value: contracts.Credential{Values: map[string]string{"access_key_id": "testing", "secret_access_key": "testing"}}}
	factory := &runtimeFactory{native: newNativeClients(config)}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	return motoRuntime{runtime: runtime, factory: factory}
}

func (m motoRuntime) list(t *testing.T, nativeType string) map[string]contracts.InventoryItem {
	t.Helper()
	request := contracts.InventoryRequest{
		ConnectionID: "connection-moto", Source: productAPISource, ResourceKind: kindFor(t, m.runtime, nativeType),
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"}, Limit: 100,
	}
	result := map[string]contracts.InventoryItem{}
	for page := 0; page < 20; page++ {
		batch, err := m.runtime.List(context.Background(), request)
		if err != nil {
			t.Fatalf("list %s: %v", nativeType, err)
		}
		for _, item := range batch.Items {
			result[item.NativeID] = item
		}
		if batch.Complete {
			return result
		}
		request.Cursor = batch.NextCursor
	}
	t.Fatalf("list %s did not complete", nativeType)
	return nil
}

func (m motoRuntime) delete(t *testing.T, item contracts.InventoryItem) contracts.PreflightResult {
	t.Helper()
	value := asset.Asset{Location: item.Location, Normalized: item.Normalized, Identity: asset.Identity{
		Provider: asset.ProviderAWS, ConnectionID: "connection-moto", NativeType: item.NativeType, NativeID: item.NativeID,
	}}
	driver, err := m.runtime.ResolveAction(context.Background(), "connection-moto", value)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "moto-" + item.NativeID}
	preflight, err := driver.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight %s = %+v err=%v", item.NativeID, preflight, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("execute %s: %v", item.NativeID, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		wait, err := driver.Wait(context.Background(), request, result)
		if err != nil {
			t.Fatalf("wait %s: %v", item.NativeID, err)
		}
		if wait.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is still %s", item.NativeID, wait.State)
		}
		time.Sleep(200 * time.Millisecond)
	}
	readback, err := driver.Readback(context.Background(), request)
	if err != nil || readback.Exists {
		t.Fatalf("readback %s = %+v err=%v", item.NativeID, readback, err)
	}
	return preflight
}

func TestMotoEC2ImagesAndSnapshotsLifecycle(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	ec2 := awsec2.NewFromConfig(config)
	volume, err := ec2.CreateVolume(ctx, &awsec2.CreateVolumeInput{AvailabilityZone: awssdk.String("us-east-1a"), Size: awssdk.Int32(8)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ec2.CreateSnapshot(ctx, &awsec2.CreateSnapshotInput{VolumeId: volume.VolumeId, TagSpecifications: []ec2types.TagSpecification{{
		ResourceType: ec2types.ResourceTypeSnapshot, Tags: []ec2types.Tag{{Key: awssdk.String("Name"), Value: awssdk.String("steward-moto")}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	image, err := ec2.RegisterImage(ctx, &awsec2.RegisterImageInput{
		Name: awssdk.String("steward-moto-image"), RootDeviceName: awssdk.String("/dev/xvda"),
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{{DeviceName: awssdk.String("/dev/xvda"), Ebs: &ec2types.EbsBlockDevice{SnapshotId: snapshot.SnapshotId}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newMotoRuntime(t, config)
	images := runtime.list(t, "AWS::EC2::Image")
	imageItem, ok := images[awssdk.ToString(image.ImageId)]
	if !ok {
		t.Fatalf("image %s not discovered in %d images", awssdk.ToString(image.ImageId), len(images))
	}
	// Moto registers the image over its own copy of the root snapshot.
	refs, _ := imageItem.Normalized["snapshot_ids"].([]string)
	if len(refs) != 1 {
		t.Fatalf("image snapshot references = %#v", imageItem.Normalized["snapshot_ids"])
	}
	for id := range images {
		// Moto preloads public AMIs; the self-owned filter must exclude them.
		if images[id].Normalized["OwnerId"] != imageItem.Normalized["OwnerId"] {
			t.Fatalf("foreign image %s listed", id)
		}
	}
	snapshots := runtime.list(t, "AWS::EC2::Snapshot")
	snapshotItem, ok := snapshots[awssdk.ToString(snapshot.SnapshotId)]
	if !ok || snapshotItem.Name != "steward-moto" || snapshotItem.Normalized["VolumeId"] != awssdk.ToString(volume.VolumeId) {
		t.Fatalf("snapshot item = %+v", snapshotItem)
	}
	if runtime.factory.nativeRegion != "us-east-1" {
		t.Fatalf("native region = %s", runtime.factory.nativeRegion)
	}
	runtime.delete(t, imageItem)
	runtime.delete(t, snapshotItem)
	if _, ok := runtime.list(t, "AWS::EC2::Snapshot")[awssdk.ToString(snapshot.SnapshotId)]; ok {
		t.Fatal("deleted snapshot is still listed")
	}
}

func TestMotoOpenSearchAndFSxLifecycle(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	if _, err := awsopensearch.NewFromConfig(config).CreateDomain(ctx, &awsopensearch.CreateDomainInput{DomainName: awssdk.String("steward-search")}); err != nil {
		t.Fatal(err)
	}
	fileSystem, err := awsfsx.NewFromConfig(config).CreateFileSystem(ctx, &awsfsx.CreateFileSystemInput{
		FileSystemType: fsxtypes.FileSystemTypeLustre, StorageCapacity: awssdk.Int32(1200), SubnetIds: []string{"subnet-12345678"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newMotoRuntime(t, config)

	domain, ok := runtime.list(t, "AWS::OpenSearchService::Domain")["steward-search"]
	if !ok {
		t.Fatal("OpenSearch domain not discovered through ListDomainNames")
	}
	runtime.delete(t, domain)

	fileSystemID := awssdk.ToString(fileSystem.FileSystem.FileSystemId)
	fileSystemItem, ok := runtime.list(t, "AWS::FSx::FileSystem")[fileSystemID]
	if !ok {
		t.Fatal("FSx file system not discovered")
	}
	runtime.delete(t, fileSystemItem)
}
