package cleanup

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func identityAsset(id string, provider asset.Provider, nativeType, nativeID string, normalized map[string]any) asset.Asset {
	return asset.Asset{ID: asset.AssetID(id), Normalized: normalized, Identity: asset.Identity{Provider: provider, ConnectionID: "connection", NativeType: nativeType, NativeID: nativeID}}
}

func TestConnectionIdentityProtections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  asset.Provider
		principal string
		assets    []asset.Asset
		protected []asset.AssetID
	}{
		{"aws user", asset.ProviderAWS, "arn:aws:iam::123456789012:user/ops/steward", []asset.Asset{
			identityAsset("user", asset.ProviderAWS, "AWS::IAM::User", "steward", map[string]any{"Groups": []any{"Cleaners"}, "ManagedPolicyArns": []any{"arn:aws:iam::123456789012:policy/steward"}}),
			identityAsset("group", asset.ProviderAWS, "AWS::IAM::Group", "Cleaners", nil),
			identityAsset("policy", asset.ProviderAWS, "AWS::IAM::ManagedPolicy", "arn:aws:iam::123456789012:policy/steward", nil),
			identityAsset("other-user", asset.ProviderAWS, "AWS::IAM::User", "someone", nil),
			identityAsset("other-policy", asset.ProviderAWS, "AWS::IAM::ManagedPolicy", "arn:aws:iam::123456789012:policy/other", nil),
		}, []asset.AssetID{"group", "policy", "user"}},
		{"aws assumed role", asset.ProviderAWS, "arn:aws:sts::123456789012:assumed-role/StewardRole/session-1", []asset.Asset{
			identityAsset("role", asset.ProviderAWS, "AWS::IAM::Role", "StewardRole", map[string]any{"ManagedPolicyArns": []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"}}),
			identityAsset("profile", asset.ProviderAWS, "AWS::IAM::InstanceProfile", "steward-host", map[string]any{"Roles": []any{"StewardRole"}}),
			identityAsset("user", asset.ProviderAWS, "AWS::IAM::User", "StewardRole", nil),
		}, []asset.AssetID{"profile", "role"}},
		{"aws root", asset.ProviderAWS, "arn:aws:iam::123456789012:root", []asset.Asset{
			identityAsset("user", asset.ProviderAWS, "AWS::IAM::User", "root", nil),
		}, nil},
		{"alicloud ram role", asset.ProviderAliCloud, "acs:ram::1234567890:assumed-role/steward-role/session", []asset.Asset{
			identityAsset("role", asset.ProviderAliCloud, "ACS::RAM::Role", "steward-role", nil),
		}, []asset.AssetID{"role"}},
		{"gcp service account", asset.ProviderGCP, "Steward@project.iam.gserviceaccount.com", []asset.Asset{
			identityAsset("sa", asset.ProviderGCP, "iam.googleapis.com/ServiceAccount", "//iam.googleapis.com/projects/project/serviceAccounts/steward@project.iam.gserviceaccount.com", nil),
			identityAsset("key", asset.ProviderGCP, "iam.googleapis.com/ServiceAccountKey", "//iam.googleapis.com/projects/project/serviceAccounts/steward@project.iam.gserviceaccount.com/keys/abc", nil),
			identityAsset("other", asset.ProviderGCP, "iam.googleapis.com/ServiceAccount", "//iam.googleapis.com/projects/project/serviceAccounts/other-steward@project.iam.gserviceaccount.com", nil),
		}, []asset.AssetID{"key", "sa"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := asset.CloudConnection{ID: "connection", Name: "prod", Provider: tc.provider, Principal: tc.principal}
			other := connection
			other.ID = "other"
			got := connectionIdentityProtections([]asset.CloudConnection{connection, other}, tc.assets)
			if len(got) != len(tc.protected) {
				t.Fatal(got)
			}
			for _, id := range tc.protected {
				if policy := got[id]; !policy.Protected || policy.Source != connectionIdentitySource || policy.Evidence["connection_id"] != asset.ConnectionID("connection") {
					t.Fatal(id, policy)
				}
			}
		})
	}
}
