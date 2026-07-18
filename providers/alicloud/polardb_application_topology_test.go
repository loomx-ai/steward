package alicloud

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestPolarDBApplicationInventoryEnrichesVSwitchTopology(t *testing.T) {
	t.Parallel()

	const (
		applicationID = "pa-bp1251doe3y4cpw47"
		clusterID     = "pc-bp1polarcluster"
		vpcID         = "vpc-bp1nbeu0yiv0ssvy4oe8f"
		vSwitchID     = "vsw-bp14eqttir8f2l5fnbg2a"
	)
	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "id", "access_key_secret": "secret",
		},
	}}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.PolarDB.DescribeApplications": {
			RequestID: "describe-applications",
			Data: map[string]any{
				"PageNumber": 1, "PageRecordCount": 1, "TotalRecordCount": 1,
				"Items": map[string]any{"Applications": []any{map[string]any{
					"ApplicationId":   applicationID,
					"Description":     "Supabase application",
					"Status":          "Activated",
					"ApplicationType": "polardb_supabase",
					"DBClusterId":     clusterID,
					"RegionId":        "cn-hangzhou",
				}}},
			},
		},
		"AlibabaCloud.PolarDB.DescribeApplicationAttribute": {
			RequestID: "describe-application-attribute",
			Data: map[string]any{
				"ApplicationId":   applicationID,
				"Description":     "Supabase application",
				"Status":          "Activated",
				"ApplicationType": "supabase",
				"DBClusterId":     clusterID,
				"VPCId":           vpcID,
				"VSwitchId":       vSwitchID,
				"ZoneId":          "cn-hangzhou-k",
			},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	kind, exists := runtime.resourceKindByNativeType[polarDBApplicationNativeType]
	if !exists {
		t.Fatalf("PolarDB application resource kind %q is missing", polarDBApplicationNativeType)
	}
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source:       "product-api",
		ResourceKind: &kind,
		Limit:        100,
	}
	batch, err := runtime.List(context.Background(), request)
	if err != nil {
		t.Fatalf("list PolarDB applications: %v", err)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != applicationID {
		t.Fatalf("PolarDB application inventory = %+v", batch)
	}

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), request, batch.Items)
	if err != nil {
		t.Fatalf("enrich PolarDB application topology: %v", err)
	}
	if len(enriched) != 1 || enriched[0].Normalized["vpcId"] != vpcID ||
		enriched[0].Normalized["vSwitchId"] != vSwitchID ||
		enriched[0].Normalized["dbClusterId"] != clusterID ||
		enriched[0].Normalized["zoneId"] != "cn-hangzhou-k" {
		t.Fatalf("enriched PolarDB application = %+v", enriched)
	}
	if !reflect.DeepEqual(
		enriched[0].NetworkReferences,
		[]string{clusterID, vpcID, vSwitchID},
	) {
		t.Fatalf("PolarDB application network references = %+v", enriched[0].NetworkReferences)
	}
	if len(factory.calls) != 2 ||
		factory.calls[0].Operation != "AlibabaCloud.PolarDB.DescribeApplications" ||
		factory.calls[0].Parameters["RegionId"] != "cn-hangzhou" ||
		factory.calls[1].Operation != "AlibabaCloud.PolarDB.DescribeApplicationAttribute" ||
		factory.calls[1].Parameters["ApplicationId"] != applicationID {
		t.Fatalf("PolarDB application calls = %+v", factory.calls)
	}
}

func TestPolarDBApplicationSpecBuildsCleanupDependencies(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != polarDBApplicationNativeType {
			continue
		}
		list := compiled.Definition.Discovery.List
		enrich := compiled.Definition.Discovery.Enrich
		if list == nil || list.Operation != "AlibabaCloud.PolarDB.DescribeApplications" ||
			list.IdentityPath != "ApplicationId" || enrich == nil ||
			enrich.Operation != "AlibabaCloud.PolarDB.DescribeApplicationAttribute" ||
			enrich.ItemsPath != "$" || enrich.IdentityPath != "ApplicationId" ||
			enrich.MaxBatchSize != 1 {
			t.Fatalf("PolarDB application discovery = %+v", compiled.Definition.Discovery)
		}
		deleteAction := compiled.Definition.Actions["delete"]
		if deleteAction.Operation != "AlibabaCloud.PolarDB.DeleteApplication" ||
			deleteAction.Parameters["ApplicationId"] != "resource.nativeId" ||
			deleteAction.Read == nil ||
			deleteAction.Read.Operation != "AlibabaCloud.PolarDB.DescribeApplications" ||
			deleteAction.Read.Parameters["ApplicationIds"] != "resource.nativeId" {
			t.Fatalf("PolarDB application delete action = %+v", deleteAction)
		}
		wantRelationships := map[string]struct {
			kind string
			path string
		}{
			"ACS::VPC::VPC":           {kind: "member_of", path: "vpcId"},
			"ACS::VPC::VSwitch":       {kind: "uses", path: "vSwitchId"},
			"ACS::PolarDB::DBCluster": {kind: "uses", path: "dbClusterId"},
		}
		if len(compiled.Definition.Relationships) != len(wantRelationships) {
			t.Fatalf("PolarDB application relationships = %+v", compiled.Definition.Relationships)
		}
		for _, relationship := range compiled.Definition.Relationships {
			want, ok := wantRelationships[relationship.TargetType]
			if !ok || relationship.Type != want.kind || relationship.TargetIDPath != want.path {
				t.Fatalf("unexpected PolarDB application relationship = %+v", relationship)
			}
		}
		return
	}
	t.Fatal("PolarDB application resource spec not found")
}

func TestPolarDBApplicationTopologyConnectsOccupiedVSwitch(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	const connectionID = asset.ConnectionID("connection-polardb-application")
	repository := &topologyGraphRepository{assets: []asset.Asset{
		alibabaTopologyAsset("asset-application", connectionID, polarDBApplicationNativeType, "pa-bp1251doe3y4cpw47", map[string]any{
			"vpcId": "vpc-a", "vSwitchId": "vsw-bp14eqttir8f2l5fnbg2a", "dbClusterId": "pc-a",
		}),
		alibabaTopologyAsset("asset-vpc", connectionID, vpcNativeType, "vpc-a", nil),
		alibabaTopologyAsset("asset-vswitch", connectionID, vSwitchNativeType, "vsw-bp14eqttir8f2l5fnbg2a", nil),
		alibabaTopologyAsset("asset-cluster", connectionID, "ACS::PolarDB::DBCluster", "pc-a", nil),
	}}
	result, err := governance.NewService(repository, repository).RebuildGraph(
		context.Background(), "scope-polardb-application", connectionID,
		"graph-polardb-application", bundle, nil,
	)
	if err != nil {
		t.Fatalf("rebuild PolarDB application topology: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("PolarDB application topology unresolved references = %+v", result.Unresolved)
	}
	want := map[string]graph.RelationshipType{
		"asset-application|asset-vpc":     graph.RelationshipMemberOf,
		"asset-application|asset-vswitch": graph.RelationshipUses,
		"asset-application|asset-cluster": graph.RelationshipUses,
	}
	got := make(map[string]graph.RelationshipType, len(result.Relationships))
	for _, relationship := range result.Relationships {
		got[string(relationship.SourceAssetID)+"|"+string(relationship.TargetAssetID)] = relationship.Type
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PolarDB application relationships = %+v, want %+v", got, want)
	}
}
