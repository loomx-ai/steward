package asset_test

import (
	"strings"
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

func TestAzureIdentityDistinguishesOpaqueURLIDs(t *testing.T) {
	parent := "/subscriptions/11111111-2222-4333-8444-555555555555/resourcegroups/group/providers/microsoft.insights/components/component"
	identity := asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: "Microsoft.Insights/components", NativeID: parent}
	uppercase := identity
	uppercase.NativeType, uppercase.NativeID = strings.ToUpper(identity.NativeType), strings.ToUpper(parent)
	if identity.Key() != uppercase.Key() {
		t.Fatal("ARM casing created a second asset")
	}
	for _, row := range []struct{ collection, suffix, other string }{
		{"exportconfiguration", "/exportconfiguration/uGOoki0jQsyEs3IdQ83Q4QsNr4%3D", "/exportconfiguration/ugoOki0jQsyEs3IdQ83Q4QsNr4%3D"},
		{"analyticsItems", "/analyticsitems/item?id=OpaqueID", "/analyticsitems/item?id=opaqueid"},
	} {
		identity.NativeType = "Microsoft.Insights/components/" + row.collection
		identity.NativeID = "https://management.azure.com" + parent + row.suffix
		other := identity
		other.NativeID = "https://management.azure.com" + parent + row.other
		if identity.Key() == other.Key() {
			t.Fatal("case-sensitive URL identifiers collided")
		}
		other = identity
		other.NativeType = strings.ToUpper(identity.NativeType)
		if identity.Key() != other.Key() {
			t.Fatal("Azure native type casing changed URL identity")
		}
		other.ConnectionID = "another-connection"
		if identity.Key() == other.Key() {
			t.Fatal("URL identity crossed the selected connection")
		}
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
