package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

// Field names and shapes follow the official DescribeDisks, DescribeDBInstanceAttribute
// (MongoDB), GetBucketEncryption, GetQueueAttributes and DescribeVaults responses.
func TestConfigurationTopologyLinksEncryptedResourcesToKMSKeys(t *testing.T) {
	t.Parallel()

	mns := configurationTopologyAsset("queue", "ACS::MessageService::Queue", "orders", "cn-hangzhou", nil)
	mns.Normalized["KmsKeyId"] = "key-hz1"
	assets := []asset.Asset{
		configurationTopologyAsset("key", "ACS::KMS::Key", "key-hz1", "cn-hangzhou", nil),
		configurationTopologyAsset("key-sh", "ACS::KMS::Key", "key-sh1", "cn-shanghai", nil),
		configurationTopologyAsset("disk", "ACS::ECS::Disk", "d-a", "cn-hangzhou", map[string]any{"Encrypted": true, "KMSKeyId": "key-hz1"}),
		configurationTopologyAsset("mongo", "ACS::MongoDB::DBInstance", "dds-a", "cn-hangzhou", map[string]any{"EncryptionKey": "key-hz1"}),
		configurationTopologyAsset("bucket", "ACS::OSS::Bucket", "logs", "cn-hangzhou", map[string]any{
			"ServerSideEncryptionRule": map[string]any{"SSEAlgorithm": "KMS", "KMSMasterKeyID": "key-hz1"},
		}),
		mns,
		configurationTopologyAsset("vault", "ACS::HBR::Vault", "v-a", "cn-shanghai", map[string]any{"KmsKeyId": "acs:kms:cn-shanghai:1234567890:key/key-sh1"}),
		configurationTopologyAsset("unscanned", "ACS::ECS::Disk", "d-b", "cn-hangzhou", map[string]any{"KMSKeyId": "key-unknown"}),
		configurationTopologyAsset("plain", "ACS::ECS::Disk", "d-c", "cn-hangzhou", map[string]any{"Encrypted": false, "KMSKeyId": ""}),
	}
	contribution, err := hooks.NewConfigurationTopology().Contribute(context.Background(), "scope-a", assets)
	if err != nil {
		t.Fatal(err)
	}
	got := map[asset.AssetID]asset.AssetID{}
	for _, relationship := range contribution.Relationships {
		if relationship.Type != graph.RelationshipUses || relationship.Evidence["relationship"] != "kms_key" {
			t.Fatalf("unexpected relationship %+v", relationship)
		}
		if got[relationship.SourceAssetID] != "" {
			t.Fatalf("duplicate KMS relationship for %s", relationship.SourceAssetID)
		}
		got[relationship.SourceAssetID] = relationship.TargetAssetID
	}
	want := map[asset.AssetID]asset.AssetID{"disk": "key", "mongo": "key", "bucket": "key", "queue": "key", "vault": "key-sh"}
	if len(got) != len(want) {
		t.Fatalf("KMS relationships = %v", got)
	}
	for source, target := range want {
		if got[source] != target {
			t.Errorf("%s uses %s, want %s", source, got[source], target)
		}
	}
}
