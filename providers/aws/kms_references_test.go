package aws

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func TestLifecycleContributorLinksEncryptedResourcesToKMSKeys(t *testing.T) {
	const key = "1234abcd-12ab-34cd-56ef-1234567890ab"
	west := awsAsset("key-west", "AWS::KMS::Key", key, nil)
	west.Location = "us-west-2"
	assets := []asset.Asset{
		awsAsset("key", "AWS::KMS::Key", key, nil),
		west,
		awsAsset("alias", "AWS::KMS::Alias", "alias/app", map[string]any{"AliasName": "alias/app", "TargetKeyId": key}),
		awsAsset("db", "AWS::RDS::DBInstance", "db-1", map[string]any{"KmsKeyId": "arn:aws:kms:us-east-1:123456789012:key/" + key, "PerformanceInsightsKMSKeyId": key}),
		awsAsset("bucket", "AWS::S3::Bucket", "logs", map[string]any{"BucketEncryption": map[string]any{"ServerSideEncryptionConfiguration": []any{map[string]any{"ServerSideEncryptionByDefault": map[string]any{"SSEAlgorithm": "aws:kms", "KMSMasterKeyID": "alias/app"}}}}}),
		awsAsset("queue", "AWS::SQS::Queue", "q", map[string]any{"KmsMasterKeyId": "arn:aws:kms:us-east-1:123456789012:alias/app"}),
		awsAsset("replica", "AWS::DynamoDB::Table", "t", map[string]any{"SSESpecification": map[string]any{"KMSMasterKeyId": "arn:aws:kms:us-west-2:123456789012:key/" + key}}),
		awsAsset("managed", "AWS::SNS::Topic", "topic", map[string]any{"KmsMasterKeyId": "alias/aws/sns"}),
		awsAsset("foreign", "AWS::Logs::LogGroup", "lg", map[string]any{"KmsKeyId": "arn:aws:kms:us-east-1:123456789012:key/00000000-0000-0000-0000-000000000000"}),
		awsAsset("period", "AWS::SQS::Queue", "q2", map[string]any{"KmsDataKeyReusePeriodSeconds": key}),
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	edges := map[asset.AssetID]asset.AssetID{}
	for _, relationship := range contribution.Relationships {
		if relationship.Source != kmsReferenceSource {
			continue
		}
		if relationship.Type != graph.RelationshipUses || edges[relationship.SourceAssetID] != "" {
			t.Fatal("unexpected KMS relationship", relationship)
		}
		edges[relationship.SourceAssetID] = relationship.TargetAssetID
	}
	want := map[asset.AssetID]asset.AssetID{"db": "key", "bucket": "key", "queue": "key", "replica": "key-west"}
	if len(edges) != len(want) {
		t.Fatal("KMS edges", edges)
	}
	for source, target := range want {
		if edges[source] != target {
			t.Fatal("KMS edge", source, edges[source], target)
		}
	}
}

func TestKMSReferencesIgnoreAmbiguousAliases(t *testing.T) {
	assets := []asset.Asset{
		awsAsset("key", "AWS::KMS::Key", "k1", nil),
		awsAsset("alias-a", "AWS::KMS::Alias", "a", map[string]any{"AliasName": "alias/app", "TargetKeyId": "k1"}),
		awsAsset("alias-b", "AWS::KMS::Alias", "b", map[string]any{"AliasName": "alias/app", "TargetKeyId": "k1"}),
		awsAsset("db", "AWS::RDS::DBInstance", "db-1", map[string]any{"KmsKeyId": "alias/app"}),
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, relationship := range contribution.Relationships {
		if relationship.Source == kmsReferenceSource {
			t.Fatal("ambiguous alias produced an edge", relationship)
		}
	}
}
