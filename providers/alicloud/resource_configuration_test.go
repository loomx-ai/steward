package alicloud

import (
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestProjectResourceCenterConfigurationPlacesOSSBucketInItsRegion(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	bucket := contracts.InventoryItem{
		NativeType: "ACS::OSS::Bucket",
		Location:   "cn-beijing",
		Scope: contracts.InventoryScope{
			Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global",
		},
		Normalized: map[string]any{},
		Raw: map[string]any{
			"RegionId": "cn-hangzhou",
			"Configuration": map[string]any{
				"Name": "bucket-a",
			},
		},
	}
	if err := runtime.projectResourceCenterConfiguration(&bucket); err != nil {
		t.Fatalf("project OSS bucket configuration: %v", err)
	}
	if bucket.Scope.Kind != asset.ScopeRegion ||
		bucket.Scope.NativeID != "cn-hangzhou" ||
		bucket.Scope.Location != "cn-hangzhou" {
		t.Fatalf("OSS bucket scope = %+v", bucket.Scope)
	}
}

func TestProjectResourceCenterConfigurationProjectsReviewedRelationshipIDs(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	loadBalancer := contracts.InventoryItem{
		NativeType: "ACS::ALB::LoadBalancer",
		Normalized: map[string]any{},
		Raw: map[string]any{"Configuration": map[string]any{
			"VpcId": "vpc-a",
			"ZoneMappings": []any{
				map[string]any{"VSwitchId": "vsw-b"},
				map[string]any{"VSwitchId": "vsw-a"},
			},
			"SecurityGroupIds": []any{"sg-b", "sg-a"},
		}},
	}
	if err := runtime.projectResourceCenterConfiguration(&loadBalancer); err != nil {
		t.Fatalf("project ALB configuration: %v", err)
	}
	if !reflect.DeepEqual(loadBalancer.Normalized["vSwitchIds"], []string{"vsw-a", "vsw-b"}) ||
		!reflect.DeepEqual(loadBalancer.Normalized["securityGroupIds"], []string{"sg-a", "sg-b"}) {
		t.Fatalf("ALB relationship IDs = %#v", loadBalancer.Normalized)
	}

	fileSystem := contracts.InventoryItem{
		NativeType: "ACS::NAS::FileSystem",
		Normalized: map[string]any{},
		Raw: map[string]any{"Configuration": map[string]any{
			"VpcId": "vpc-a",
			"MountTargets": []any{
				map[string]any{"VswId": "vsw-nas"},
			},
		}},
	}
	if err := runtime.projectResourceCenterConfiguration(&fileSystem); err != nil {
		t.Fatalf("project NAS configuration: %v", err)
	}
	if fileSystem.Normalized["vSwitchIds"] != "vsw-nas" {
		t.Fatalf("NAS VswId alias = %#v", fileSystem.Normalized)
	}

	gateway := contracts.InventoryItem{
		NativeType: "ACS::MSE::Gateway",
		Normalized: map[string]any{},
		Raw: map[string]any{"Configuration": map[string]any{
			"VpcId":           "vpc-a",
			"VSwitchId":       "vsw-primary",
			"BackupVSwitchId": "vsw-backup",
		}},
	}
	if err := runtime.projectResourceCenterConfiguration(&gateway); err != nil {
		t.Fatalf("project MSE gateway configuration: %v", err)
	}
	if !reflect.DeepEqual(
		gateway.Normalized["vSwitchIds"],
		[]string{"vsw-backup", "vsw-primary"},
	) {
		t.Fatalf("MSE primary and backup VSwitch IDs = %#v", gateway.Normalized)
	}

	peerConnection := contracts.InventoryItem{
		NativeType: vpcPeerConnectionNativeType,
		Normalized: map[string]any{},
		Raw: map[string]any{"Configuration": map[string]any{
			"VpcId":          "vpc-requester",
			"AcceptingVpcId": "vpc-accepter",
		}},
	}
	if err := runtime.projectResourceCenterConfiguration(&peerConnection); err != nil {
		t.Fatalf("project VPC peer connection configuration: %v", err)
	}
	if peerConnection.Normalized["vpcId"] != "vpc-requester" ||
		peerConnection.Normalized["acceptingVpcId"] != "vpc-accepter" {
		t.Fatalf("VPC peer endpoint IDs = %#v", peerConnection.Normalized)
	}
}

func TestProjectResourceCenterConfigurationAliasesARMSTraceAppID(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	traceApp := contracts.InventoryItem{
		NativeType: "ACS::ARMS::TraceApp",
		Normalized: map[string]any{},
		Raw: map[string]any{"Configuration": map[string]any{
			"TraceAppId": float64(6331564),
			"Pid":        "h9y3-example",
			"Type":       "TRACE",
		}},
	}
	if err := runtime.projectResourceCenterConfiguration(&traceApp); err != nil {
		t.Fatalf("project ARMS trace application configuration: %v", err)
	}
	if traceApp.Normalized["appId"] != float64(6331564) {
		t.Fatalf("ARMS AppId alias = %#v", traceApp.Normalized)
	}
}

func TestProjectResourceCenterConfigurationMarksOnlyServiceManagedKMSKeysNotActionable(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	for _, test := range []struct {
		name           string
		creator        any
		wantManaged    bool
		wantCreator    string
		wantActionable *bool
	}{
		{
			name: "cloud service", creator: "Ecs", wantManaged: true,
			wantCreator: "Ecs", wantActionable: boolPointer(false),
		},
		{name: "account owner", creator: "1234567890123456"},
		{name: "creator missing"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			configuration := map[string]any{"KeyId": "key-a"}
			if test.creator != nil {
				configuration["Creator"] = test.creator
			}
			item := contracts.InventoryItem{
				NativeType: KMSKeyNativeType,
				Normalized: map[string]any{},
				Raw:        map[string]any{"Configuration": configuration},
			}
			if err := runtime.projectResourceCenterConfiguration(&item); err != nil {
				t.Fatalf("project KMS configuration: %v", err)
			}
			if !reflect.DeepEqual(item.Actionable, test.wantActionable) {
				t.Fatalf("KMS actionable = %#v, want %#v", item.Actionable, test.wantActionable)
			}
			if managed, _ := item.Normalized[NormalizedServiceManagedField].(bool); managed != test.wantManaged {
				t.Fatalf("KMS normalized = %#v", item.Normalized)
			}
			if creator, _ := item.Normalized["_service_creator"].(string); creator != test.wantCreator {
				t.Fatalf("KMS creator = %q, want %q", creator, test.wantCreator)
			}
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}
