package alicloud

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDTSResourceCenterInventoryEnrichesNetworkTopology(t *testing.T) {
	t.Parallel()

	const (
		dtsInstanceID = "dtsl04v3889257091l"
		vSwitchID     = "vsw-bp1kvm32j2lsqwxaleaak"
		rdsInstanceID = "rm-bp170g8124829xo44"
	)
	client := &runtimeResourceCenterClient{
		page: ResourcePage{RequestID: "resource-center-search", Resources: []ResourceRecord{{
			RegionID: "cn-hangzhou", ResourceType: dtsInstanceNativeType,
			ResourceID: dtsInstanceID, ResourceName: "migration-job",
		}}},
		configurationPage: ResourceConfigurationPage{
			RequestID: "resource-center-configuration",
			Resources: []ResourceRecord{{
				RegionID: "cn-hangzhou", ResourceType: dtsInstanceNativeType,
				ResourceID: dtsInstanceID, ResourceName: "migration-job",
				Configuration: map[string]any{
					"DtsInstanceId": dtsInstanceID,
					"Status":        "Running",
					"Type":          "subscribe",
				},
			}},
		},
	}
	factory := &runtimeFactory{
		client: client,
		invokeResult: contracts.InvocationResult{
			RequestID: "describe-dts-jobs",
			Data: map[string]any{"DtsJobList": []any{map[string]any{
				"DtsInstanceID": dtsInstanceID,
				"DtsJobId":      "l04v3889257091l",
				"Reserved":      `{"network":{"vSwitchId":"` + vSwitchID + `"},"duplicate":"` + vSwitchID + `"}`,
				"SourceEndpoint": map[string]any{
					"InstanceType": "RDS", "InstanceID": rdsInstanceID,
				},
				"DestinationEndpoint": map[string]any{
					"InstanceType": "ECS", "InstanceID": "i-unrelated",
				},
				"ReverseJob": map[string]any{
					"DestinationEndpoint": map[string]any{
						"instancetype": "rds", "instanceid": rdsInstanceID,
					},
				},
			}}},
		},
	}
	runtime, err := newRuntime(
		&credentialSource{wantConnection: "connection-a", value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		}},
		factory,
	)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	dtsKind := runtime.resourceKindByNativeType[dtsInstanceNativeType]
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source:       "resource-center",
		ResourceKind: &dtsKind,
	}
	batch, err := runtime.List(context.Background(), request)
	if err != nil {
		t.Fatalf("list Resource Center DTS inventory: %v", err)
	}
	if !reflect.DeepEqual(client.request.ResourceTypes, []string{dtsInstanceNativeType}) ||
		len(batch.Items) != 1 || batch.Items[0].NativeID != dtsInstanceID {
		t.Fatalf("DTS Resource Center batch = %+v, request = %+v", batch, client.request)
	}

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), request, batch.Items)
	if err != nil {
		t.Fatalf("enrich Resource Center DTS inventory: %v", err)
	}
	if factory.invocation.Operation != "AlibabaCloud.DTS.DescribeDtsJobs" {
		t.Fatalf("DTS detail operation = %q", factory.invocation.Operation)
	}
	wantParameters := map[string]any{
		"RegionId": "cn-hangzhou", "Type": "instance", "Params": dtsInstanceID,
		"JobType": "SUBSCRIBE", "PageNumber": 1, "PageSize": 20, "WithoutDbList": true,
	}
	if !reflect.DeepEqual(factory.invocation.Parameters, wantParameters) {
		t.Fatalf("DTS detail parameters = %#v, want %#v", factory.invocation.Parameters, wantParameters)
	}
	if len(enriched) != 1 || enriched[0].Normalized["dtsJobId"] != "l04v3889257091l" ||
		!reflect.DeepEqual(enriched[0].Normalized["vSwitchIds"], []string{vSwitchID}) ||
		!reflect.DeepEqual(enriched[0].Normalized["rdsInstanceIds"], []string{rdsInstanceID}) {
		t.Fatalf("enriched DTS inventory = %+v", enriched)
	}
	references := make(map[string]struct{}, len(enriched[0].NetworkReferences))
	for _, reference := range enriched[0].NetworkReferences {
		references[reference] = struct{}{}
	}
	if _, ok := references[vSwitchID]; !ok {
		t.Fatalf("DTS network references omit %s: %+v", vSwitchID, enriched[0].NetworkReferences)
	}
	if _, ok := references[rdsInstanceID]; !ok {
		t.Fatalf("DTS network references omit %s: %+v", rdsInstanceID, enriched[0].NetworkReferences)
	}
}

func TestDTSSpecUsesInstanceIdentityAndReviewedRelationships(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	var found bool
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != dtsInstanceNativeType {
			continue
		}
		found = true
		list := compiled.Definition.Discovery.List
		enrich := compiled.Definition.Discovery.Enrich
		if list == nil || list.IdentityPath != "DtsInstanceID" || enrich == nil ||
			enrich.IdentityPath != "DtsInstanceID" || enrich.MaxBatchSize != 1 ||
			enrich.Parameters["Type"] != "instance" || enrich.Parameters["Params"] != "resources.nativeId" ||
			enrich.Parameters["JobType"] != "resource.normalized.jobType" {
			t.Fatalf("DTS discovery spec = %+v", compiled.Definition.Discovery)
		}
		deleteAction := compiled.Definition.Actions["delete"]
		if deleteAction.Parameters["DtsInstanceId"] != "resource.nativeId" ||
			deleteAction.Read == nil || deleteAction.Read.IdentityPath != "DtsInstanceID" ||
			deleteAction.Read.Parameters["Type"] != "instance" ||
			deleteAction.Read.Parameters["Params"] != "resource.nativeId" ||
			deleteAction.Read.Parameters["JobType"] != "resource.normalized.jobType" {
			t.Fatalf("DTS delete action = %+v", deleteAction)
		}
		wantRelationships := map[string]string{
			"ACS::VPC::VSwitch":    "vSwitchIds",
			"ACS::RDS::DBInstance": "rdsInstanceIds",
		}
		if len(compiled.Definition.Relationships) != len(wantRelationships) {
			t.Fatalf("DTS relationships = %+v", compiled.Definition.Relationships)
		}
		for _, relationship := range compiled.Definition.Relationships {
			if relationship.Type != "uses" || wantRelationships[relationship.TargetType] != relationship.TargetIDPath {
				t.Fatalf("unexpected DTS relationship = %+v", relationship)
			}
		}
	}
	if !found {
		t.Fatal("DTS resource spec not found")
	}
}

func TestDTSTopologyConnectsReservedVSwitchAndRDSEndpoint(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	const connectionID = asset.ConnectionID("connection-dts-topology")
	repository := &topologyGraphRepository{assets: []asset.Asset{
		alibabaTopologyAsset("asset-dts", connectionID, dtsInstanceNativeType, "dts-a", map[string]any{
			"vSwitchIds": []string{"vsw-a"}, "rdsInstanceIds": []string{"rm-a"},
		}),
		alibabaTopologyAsset("asset-rds", connectionID, "ACS::RDS::DBInstance", "rm-a", map[string]any{
			"vSwitchIds": []string{"vsw-b"},
		}),
		alibabaTopologyAsset("asset-vswitch-a", connectionID, vSwitchNativeType, "vsw-a", nil),
		alibabaTopologyAsset("asset-vswitch-b", connectionID, vSwitchNativeType, "vsw-b", nil),
	}}
	result, err := governance.NewService(repository, repository).RebuildGraph(
		context.Background(), "scope-dts", connectionID, "graph-dts", bundle, nil,
	)
	if err != nil {
		t.Fatalf("rebuild DTS topology: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("DTS topology unresolved references = %+v", result.Unresolved)
	}
	want := map[string]graph.RelationshipType{
		"asset-dts|asset-vswitch-a": graph.RelationshipUses,
		"asset-dts|asset-rds":       graph.RelationshipUses,
		"asset-rds|asset-vswitch-b": graph.RelationshipUses,
	}
	got := make(map[string]graph.RelationshipType, len(result.Relationships))
	for _, relationship := range result.Relationships {
		got[string(relationship.SourceAssetID)+"|"+string(relationship.TargetAssetID)] = relationship.Type
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DTS topology relationships = %+v, want %+v", got, want)
	}
}

func TestNATGatewaySpecAllowsSlowAsynchronousDeletion(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != "ACS::NAT::NatGateway" {
			continue
		}
		deleteAction := compiled.Definition.Actions["delete"]
		if deleteAction.PollIntervalSeconds != 5 ||
			time.Duration(deleteAction.DeletionCheckTimeoutSeconds)*time.Second != 10*time.Minute {
			t.Fatalf("NAT gateway delete timing = %+v", deleteAction)
		}
		return
	}
	t.Fatal("NAT gateway resource spec not found")
}
