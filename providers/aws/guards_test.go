package aws

import (
	"context"
	"net/http"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsbackup "github.com/aws/aws-sdk-go-v2/service/backup"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type fakeKMS struct{ metadata *kmstypes.KeyMetadata }

func (f fakeKMS) DescribeKey(context.Context, *awskms.DescribeKeyInput, ...func(*awskms.Options)) (*awskms.DescribeKeyOutput, error) {
	return &awskms.DescribeKeyOutput{KeyMetadata: f.metadata}, nil
}

type fakeBackup struct {
	output *awsbackup.DescribeBackupVaultOutput
}

func (f fakeBackup) DescribeBackupVault(context.Context, *awsbackup.DescribeBackupVaultInput, ...func(*awsbackup.Options)) (*awsbackup.DescribeBackupVaultOutput, error) {
	return f.output, nil
}

type fakeS3 struct {
	output *awss3.ListObjectVersionsOutput
}

func (f fakeS3) ListObjectVersions(context.Context, *awss3.ListObjectVersionsInput, ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error) {
	return f.output, nil
}

func guardRequest(nativeType, id string) contracts.ActionRequest {
	return contracts.ActionRequest{Asset: asset.Asset{Normalized: map[string]any{"cloudControlIdentifier": id}, Identity: asset.Identity{Provider: asset.ProviderAWS, NativeType: nativeType, NativeID: id}}, Action: "delete", IdempotencyKey: "step"}
}

func TestDeleteGuardsBlockServiceOwnedOrNonEmptyResources(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cases := []struct {
		name    string
		driver  *guardedAction
		request contracts.ActionRequest
	}{
		{"aws-managed-key", &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{}}, guard: kmsKeyGuard(fakeKMS{&kmstypes.KeyMetadata{KeyId: awssdk.String("k"), KeyManager: kmstypes.KeyManagerTypeAws, KeyState: kmstypes.KeyStateEnabled}})}, guardRequest("AWS::KMS::Key", "k")},
		{"vault-with-recovery-points", &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{}}, guard: backupVaultGuard(fakeBackup{&awsbackup.DescribeBackupVaultOutput{BackupVaultName: awssdk.String("vault"), NumberOfRecoveryPoints: 3}})}, guardRequest("AWS::Backup::BackupVault", "vault")},
		{"locked-vault", &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{}}, guard: backupVaultGuard(fakeBackup{&awsbackup.DescribeBackupVaultOutput{BackupVaultName: awssdk.String("vault"), Locked: awssdk.Bool(true), LockDate: &past}})}, guardRequest("AWS::Backup::BackupVault", "vault")},
		{"bucket-with-delete-markers", &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{}}, guard: s3BucketGuard(fakeS3{&awss3.ListObjectVersionsOutput{DeleteMarkers: []s3types.DeleteMarkerEntry{{Key: awssdk.String("old")}}}})}, guardRequest("AWS::S3::Bucket", "logs")},
		{"bucket-with-noncurrent-versions", &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{}}, guard: s3BucketGuard(fakeS3{&awss3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{{Key: awssdk.String("old"), IsLatest: awssdk.Bool(false)}}}})}, guardRequest("AWS::S3::Bucket", "logs")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			preflight, err := tc.driver.Preflight(context.Background(), tc.request)
			if err != nil || preflight.Allowed || preflight.Reason == "" {
				t.Fatalf("preflight=%+v err=%v", preflight, err)
			}
			if _, err := tc.driver.Execute(context.Background(), tc.request); err == nil {
				t.Fatal("execute ignored the guard")
			}
			if len(tc.driver.client.(*scriptedCloudControl).deletes) != 0 {
				t.Fatal("guarded resource reached DeleteResource")
			}
		})
	}
}

func TestMotoKMSAndBackupGuards(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	clients := newNativeClients(config)
	key, err := awskms.NewFromConfig(config).CreateKey(ctx, &awskms.CreateKeyInput{})
	if err != nil {
		t.Fatal(err)
	}
	keyID := awssdk.ToString(key.KeyMetadata.KeyId)
	cloud := &scriptedCloudControl{resources: map[string]CloudControlResource{"AWS::KMS::Key|" + keyID: {Identifier: keyID, Properties: `{"KeyId":"` + keyID + `"}`}}}
	driver := &guardedAction{CloudControlAction: &CloudControlAction{client: cloud}, guard: kmsKeyGuard(clients.KMS)}
	request := guardRequest("AWS::KMS::Key", keyID)
	preflight, err := driver.Preflight(ctx, request)
	if err != nil || !preflight.Allowed || preflight.Evidence["key_manager"] != "CUSTOMER" {
		t.Fatalf("customer key preflight=%+v err=%v", preflight, err)
	}
	// A key already scheduled for deletion is the outcome of a KMS delete.
	if _, err := awskms.NewFromConfig(config).ScheduleKeyDeletion(ctx, &awskms.ScheduleKeyDeletionInput{KeyId: awssdk.String(keyID), PendingWindowInDays: awssdk.Int32(7)}); err != nil {
		t.Fatal(err)
	}
	preflight, err = driver.Preflight(ctx, request)
	if err != nil || !preflight.Absent || preflight.Evidence["key_state"] != "PendingDeletion" {
		t.Fatalf("pending key preflight=%+v err=%v", preflight, err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || len(cloud.deletes) != 0 {
		t.Fatalf("pending key execute=%+v deletes=%v err=%v", result, cloud.deletes, err)
	}
	if readback, err := driver.Readback(ctx, request); err != nil || readback.Exists {
		t.Fatalf("pending key readback=%+v err=%v", readback, err)
	}

	backup := awsbackup.NewFromConfig(config)
	if _, err := backup.CreateBackupVault(ctx, &awsbackup.CreateBackupVaultInput{BackupVaultName: awssdk.String("steward-vault")}); err != nil {
		t.Fatal(err)
	}
	vault := &guardedAction{CloudControlAction: &CloudControlAction{client: &scriptedCloudControl{resources: map[string]CloudControlResource{"AWS::Backup::BackupVault|steward-vault": {Identifier: "steward-vault", Properties: `{"BackupVaultName":"steward-vault"}`}}}}, guard: backupVaultGuard(clients.Backup)}
	preflight, err = vault.Preflight(ctx, guardRequest("AWS::Backup::BackupVault", "steward-vault"))
	if err != nil || !preflight.Allowed || preflight.Evidence["recovery_points"] != int64(0) {
		t.Fatalf("empty vault preflight=%+v err=%v", preflight, err)
	}
	missing := &guardedAction{CloudControlAction: vault.CloudControlAction, guard: backupVaultGuard(clients.Backup)}
	if preflight, err := missing.Preflight(ctx, guardRequest("AWS::Backup::BackupVault", "absent-vault")); err != nil || !preflight.Absent {
		t.Fatalf("absent vault preflight=%+v err=%v", preflight, err)
	}
}

func TestMotoS3BucketGuard(t *testing.T) {
	config := motoConfig(t)
	ctx := context.Background()
	clients := newNativeClients(config)
	s3 := awss3.NewFromConfig(config, func(o *awss3.Options) { o.UsePathStyle = true })
	clients.S3 = s3
	bucket := "steward-guard-bucket"
	if _, err := s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: awssdk.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{Bucket: awssdk.String(bucket), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	put, err := s3.PutObject(ctx, &awss3.PutObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String("report.csv")})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String("report.csv")})
	if err != nil {
		t.Fatal(err)
	}
	cloud := &scriptedCloudControl{resources: map[string]CloudControlResource{"AWS::S3::Bucket|" + bucket: {Identifier: bucket, Properties: `{"BucketName":"` + bucket + `"}`}}}
	driver := &guardedAction{CloudControlAction: &CloudControlAction{client: cloud}, guard: s3BucketGuard(clients.S3)}
	request := guardRequest("AWS::S3::Bucket", bucket)
	// No current object remains, but the old version and delete marker do.
	if preflight, err := driver.Preflight(ctx, request); err != nil || preflight.Allowed || preflight.Reason == "" {
		t.Fatalf("versioned bucket preflight=%+v err=%v", preflight, err)
	}
	items := []contracts.InventoryItem{{NativeType: "AWS::S3::Bucket", NativeID: bucket, Normalized: map[string]any{}}}
	if err := enrichLifecycleFacts(ctx, clients, items); err != nil || items[0].Normalized["cleanup_protected"] != true || items[0].Normalized["bucket_empty"] != false {
		t.Fatalf("scan did not surface the non-empty bucket: %+v err=%v", items[0].Normalized, err)
	}
	for _, version := range []*string{put.VersionId, removed.VersionId} {
		if _, err := s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String("report.csv"), VersionId: version}); err != nil {
			t.Fatal(err)
		}
	}
	if preflight, err := driver.Preflight(ctx, request); err != nil || !preflight.Allowed || preflight.Evidence["bucket_empty"] != true {
		t.Fatalf("empty bucket preflight=%+v err=%v", preflight, err)
	}
	items = []contracts.InventoryItem{{NativeType: "AWS::S3::Bucket", NativeID: bucket, Normalized: map[string]any{}}}
	if err := enrichLifecycleFacts(ctx, clients, items); err != nil || items[0].Normalized["cleanup_protected"] != nil || items[0].Normalized["bucket_empty"] != true {
		t.Fatalf("scan protected an empty bucket: %+v err=%v", items[0].Normalized, err)
	}
	if preflight, err := driver.Preflight(ctx, guardRequest("AWS::S3::Bucket", "steward-absent-bucket")); err != nil || !preflight.Absent {
		t.Fatalf("absent bucket preflight=%+v err=%v", preflight, err)
	}
}

// redirectingS3 answers like S3 for a bucket outside the client's region.
type redirectingS3 struct {
	bucketRegion string
	output       *awss3.ListObjectVersionsOutput
}

func (f redirectingS3) ListObjectVersions(_ context.Context, _ *awss3.ListObjectVersionsInput, optFns ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error) {
	options := awss3.Options{Region: "us-east-1"}
	for _, fn := range optFns {
		fn(&options)
	}
	if options.Region == f.bucketRegion {
		return f.output, nil
	}
	header := http.Header{}
	if f.bucketRegion != "" {
		header.Set("X-Amz-Bucket-Region", f.bucketRegion)
	}
	return nil, &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusMovedPermanently, Header: header}},
		Err:      &smithy.GenericAPIError{Code: "PermanentRedirect", Message: "use the bucket's region"},
	}}
}

func TestBucketContentsAreReadInTheBucketRegion(t *testing.T) {
	ctx := context.Background()
	full := &awss3.ListObjectVersionsOutput{Versions: []s3types.ObjectVersion{{Key: awssdk.String("report.csv")}}}
	items := []contracts.InventoryItem{{NativeType: "AWS::S3::Bucket", NativeID: "eu-logs", Normalized: map[string]any{}}}
	if err := enrichLifecycleFacts(ctx, &NativeClients{S3: redirectingS3{bucketRegion: "eu-west-1", output: full}}, items); err != nil || items[0].Normalized["bucket_empty"] != false || items[0].Normalized["cleanup_protected"] != true {
		t.Fatalf("bucket outside us-east-1 not inspected: %+v err=%v", items[0].Normalized, err)
	}
	// Without a region to retry in, the contents stay unknown: never "empty".
	// The unreadable bucket is protected while the rest of the page proceeds.
	items = []contracts.InventoryItem{
		{NativeType: "AWS::S3::Bucket", NativeID: "lost", Normalized: map[string]any{}},
		{NativeType: "AWS::S3::Bucket", NativeID: "eu-logs", Normalized: map[string]any{}},
	}
	if err := enrichLifecycleFacts(ctx, &NativeClients{S3: redirectingS3{}}, items[:1]); err != nil {
		t.Fatal(err)
	}
	if err := enrichLifecycleFacts(ctx, &NativeClients{S3: redirectingS3{bucketRegion: "eu-west-1", output: full}}, items[1:]); err != nil {
		t.Fatal(err)
	}
	lost := items[0].Normalized
	if lost["bucket_empty"] != nil || lost["cleanup_protected"] != true || lost["cleanup_protection_reason"] != "s3_bucket_contents_unknown" || lost["bucket_contents_error"] != "PermanentRedirect" {
		t.Fatalf("unreadable bucket was not protected as unknown: %+v", lost)
	}
	if items[1].Normalized["bucket_empty"] != false {
		t.Fatalf("readable bucket was not inspected: %+v", items[1].Normalized)
	}
	// The delete-time guard stays live and fails closed on the same error.
	if outcome, err := s3BucketGuard(redirectingS3{})(ctx, "lost"); err == nil || outcome.pending || outcome.evidence["bucket_empty"] != nil {
		t.Fatalf("delete guard read an unreadable bucket as deletable: %+v err=%v", outcome, err)
	}
}

// deniedS3 denies the content read of the named buckets, as an SCP would.
type deniedS3 map[string]bool

func (f deniedS3) ListObjectVersions(_ context.Context, input *awss3.ListObjectVersionsInput, _ ...func(*awss3.Options)) (*awss3.ListObjectVersionsOutput, error) {
	if f[awssdk.ToString(input.Bucket)] {
		return nil, &smithy.GenericAPIError{Code: "AccessDenied", Message: "explicit deny in a service control policy"}
	}
	return &awss3.ListObjectVersionsOutput{}, nil
}

func TestDeniedBucketIsProtectedWithoutFailingThePage(t *testing.T) {
	ctx := context.Background()
	s3 := deniedS3{"denied": true}
	items := []contracts.InventoryItem{}
	for _, name := range []string{"a", "denied", "b", "c", "d", "e", "f", "g", "h", "i"} {
		items = append(items, contracts.InventoryItem{NativeType: "AWS::S3::Bucket", NativeID: name, Normalized: map[string]any{}})
	}
	if err := enrichLifecycleFacts(ctx, &NativeClients{S3: s3}, items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.NativeID == "denied" {
			if item.Normalized["cleanup_protection_reason"] != "s3_bucket_contents_unknown" || item.Normalized["bucket_contents_error"] != "AccessDenied" || item.Normalized["bucket_empty"] != nil {
				t.Fatalf("denied bucket: %+v", item.Normalized)
			}
		} else if item.Normalized["bucket_empty"] != true || item.Normalized["cleanup_protected"] != nil {
			t.Fatalf("bucket %s: %+v", item.NativeID, item.Normalized)
		}
	}
	cloud := &scriptedCloudControl{resources: map[string]CloudControlResource{"AWS::S3::Bucket|denied": {Identifier: "denied", Properties: `{"BucketName":"denied"}`}}}
	driver := &guardedAction{CloudControlAction: &CloudControlAction{client: cloud}, guard: s3BucketGuard(s3)}
	if preflight, err := driver.Preflight(ctx, guardRequest("AWS::S3::Bucket", "denied")); err == nil && preflight.Allowed {
		t.Fatalf("delete guard allowed a bucket whose contents are unknown: %+v", preflight)
	}
}
