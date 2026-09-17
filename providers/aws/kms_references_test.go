package aws

import (
	"context"
	"encoding/json"
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

func TestKMSPolicyWithSoleAdministratorProtectsThePrincipal(t *testing.T) {
	sole := map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Sid": "Admin", "Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::123456789012:role/platform/KeyAdmin"}, "Action": "kms:*", "Resource": "*"},
		map[string]any{"Sid": "Use", "Effect": "Allow", "Principal": map[string]any{"AWS": []any{"arn:aws:iam::123456789012:user/app"}}, "Action": []any{"kms:Encrypt", "kms:Decrypt"}, "Resource": "*"},
	}}
	rootDelegated := map[string]any{"Statement": []any{
		map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::123456789012:root"}, "Action": "kms:*", "Resource": "*"},
		map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::123456789012:role/platform/KeyAdmin"}, "Action": "kms:*", "Resource": "*"},
	}}
	conditional := map[string]any{"Statement": []any{
		map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::123456789012:role/platform/KeyAdmin"}, "Action": "kms:PutKeyPolicy", "Resource": "*"},
		map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "arn:aws:iam::123456789012:user/breakglass"}, "Action": "kms:*", "Resource": "*", "Condition": map[string]any{"Bool": map[string]any{"aws:MultiFactorAuthPresent": "true"}}},
	}}
	twoAdmins := map[string]any{"Statement": map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": []any{"arn:aws:iam::123456789012:role/platform/KeyAdmin", "arn:aws:iam::123456789012:user/breakglass"}}, "Action": "kms:*", "Resource": "*"}}
	wire, _ := json.Marshal(conditional)
	for name, tc := range map[string]struct {
		policy any
		want   bool
	}{
		"sole role administrator":  {sole, true},
		"account root delegation":  {rootDelegated, false},
		"conditional second admin": {string(wire), true},
		"two unconditional admins": {twoAdmins, false},
		"unreadable policy":        {"{not json", false},
	} {
		t.Run(name, func(t *testing.T) {
			assets := []asset.Asset{
				awsAsset("key", "AWS::KMS::Key", "k1", map[string]any{"KeyPolicy": tc.policy}),
				awsAsset("admin", "AWS::IAM::Role", "KeyAdmin", nil),
				awsAsset("app", "AWS::IAM::User", "app", nil),
			}
			contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, relationship := range contribution.Relationships {
				if relationship.Source != kmsReferenceSource {
					continue
				}
				if relationship.SourceAssetID != "key" || relationship.TargetAssetID != "admin" || relationship.Type != graph.RelationshipUses {
					t.Fatal("unexpected key policy relationship", relationship)
				}
				found = true
			}
			if found != tc.want {
				t.Fatal("sole administrator edge", found, tc.want)
			}
		})
	}
}

func TestKMSPolicyAdministratorsFormAnAlternativeGroup(t *testing.T) {
	policy := map[string]any{"Statement": map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": []any{"arn:aws:iam::123456789012:role/platform/KeyAdmin", "arn:aws:iam::123456789012:user/breakglass"}}, "Action": "kms:*", "Resource": "*"}}
	assets := []asset.Asset{
		awsAsset("key", "AWS::KMS::Key", "k1", map[string]any{"KeyPolicy": policy}),
		awsAsset("admin", "AWS::IAM::Role", "KeyAdmin", nil),
		awsAsset("breakglass", "AWS::IAM::User", "breakglass", nil),
	}
	contribution, err := NewLifecycle().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[asset.AssetID]string{}
	for _, relationship := range contribution.Relationships {
		if relationship.Source == kmsReferenceSource {
			targets[relationship.TargetAssetID], _ = relationship.Evidence[graph.RelationshipEvidenceAlternativeGroup].(string)
		}
	}
	if len(targets) != 2 || targets["admin"] == "" || targets["admin"] != targets["breakglass"] {
		t.Fatal("administrator group", targets)
	}
	// An administrator outside the inventory may still manage the key.
	contribution, err = NewLifecycle().Contribute(context.Background(), "scope", assets[:2])
	if err != nil {
		t.Fatal(err)
	}
	for _, relationship := range contribution.Relationships {
		if relationship.Source == kmsReferenceSource {
			t.Fatal("partially scanned administrators produced an edge", relationship)
		}
	}
}
