package aws

import (
	"context"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsbackup "github.com/aws/aws-sdk-go-v2/service/backup"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
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

type fakeS3 struct{ output *awss3.ListObjectVersionsOutput }

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
	for _, version := range []*string{put.VersionId, removed.VersionId} {
		if _, err := s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: awssdk.String(bucket), Key: awssdk.String("report.csv"), VersionId: version}); err != nil {
			t.Fatal(err)
		}
	}
	if preflight, err := driver.Preflight(ctx, request); err != nil || !preflight.Allowed || preflight.Evidence["bucket_empty"] != true {
		t.Fatalf("empty bucket preflight=%+v err=%v", preflight, err)
	}
	if preflight, err := driver.Preflight(ctx, guardRequest("AWS::S3::Bucket", "steward-absent-bucket")); err != nil || !preflight.Absent {
		t.Fatalf("absent bucket preflight=%+v err=%v", preflight, err)
	}
}
