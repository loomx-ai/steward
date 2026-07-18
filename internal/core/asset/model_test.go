package asset_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestIdentityAllowsGlobalAndRegionalAssets(t *testing.T) {
	connection := asset.ConnectionID("conn-1")
	global, err := asset.NewIdentity("aws", "aws", connection, "aws:iam:role", "Admin")
	if err != nil || global.Key() != "aws\x00aws\x00conn-1\x00aws:iam:role\x00Admin" {
		t.Fatalf("global identity = %#v, %v", global, err)
	}
	regional, err := asset.NewIdentity("alicloud", "public", connection, "ACS::ECS::Instance", "i-1")
	regional.ScopeKey = "region:cn-hangzhou"
	if err != nil || regional.NativeID != "i-1" ||
		regional.Key() != "alicloud\x00public\x00conn-1\x00ACS::ECS::Instance\x00i-1\x00region:cn-hangzhou" {
		t.Fatalf("regional identity = %#v, %v", regional, err)
	}
}

func TestObservationValidationRequiresImmutableProvenance(t *testing.T) {
	observation := asset.Observation{AssetID: "asset-1", Source: "resource-center"}
	if err := observation.Validate(); err == nil {
		t.Fatal("expected missing scan and observation timestamp to fail")
	}
}

func TestCapabilitySetReportsDeclaredCapabilities(t *testing.T) {
	capabilities := asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityDetailed}
	if !capabilities.Has(asset.CapabilityIndexed) {
		t.Fatal("indexed capability should be present")
	}
	if capabilities.Has(asset.CapabilityActionable) {
		t.Fatal("actionable capability should be absent")
	}
}
