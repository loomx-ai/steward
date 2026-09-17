package aws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// scriptedCloudControl serves ListResources pages keyed by type name, resource
// model and token, and records every request.
type scriptedCloudControl struct {
	pages     map[string]CloudControlPage
	resources map[string]CloudControlResource
	updates   []CloudControlUpdateRequest
	deletes   []string
	statuses  map[string]CloudControlProgress
	lists     []CloudControlListRequest
	getCalls  int
}

func pageKey(typeName, model, token string) string { return typeName + "|" + model + "|" + token }

func (c *scriptedCloudControl) ListResources(_ context.Context, request CloudControlListRequest) (CloudControlPage, error) {
	c.lists = append(c.lists, request)
	page, ok := c.pages[pageKey(request.TypeName, request.ResourceModel, request.NextToken)]
	if !ok {
		return CloudControlPage{}, errors.New("unexpected list " + pageKey(request.TypeName, request.ResourceModel, request.NextToken))
	}
	return page, nil
}

func (c *scriptedCloudControl) GetResource(_ context.Context, typeName, identifier string) (CloudControlResource, string, error) {
	c.getCalls++
	resource, ok := c.resources[typeName+"|"+identifier]
	if !ok {
		return CloudControlResource{}, "get-missing", &APIError{Code: "ResourceNotFoundException", StatusCode: 400, RequestID: "get-missing"}
	}
	return resource, "get-request", nil
}

func (c *scriptedCloudControl) DeleteResource(_ context.Context, _, identifier, token string) (CloudControlProgress, string, error) {
	c.deletes = append(c.deletes, identifier+"|"+token)
	return CloudControlProgress{RequestToken: "delete-token", Status: "IN_PROGRESS"}, "delete-request", nil
}

func (c *scriptedCloudControl) GetResourceRequestStatus(_ context.Context, token string) (CloudControlProgress, string, error) {
	return c.statuses[token], "status-request", nil
}

func (c *scriptedCloudControl) UpdateResource(_ context.Context, request CloudControlUpdateRequest) (CloudControlProgress, string, error) {
	c.updates = append(c.updates, request)
	return CloudControlProgress{RequestToken: "update-token", Status: "IN_PROGRESS"}, "update-request", nil
}

func kindFor(t *testing.T, runtime *Runtime, nativeType string) *asset.ResourceKind {
	t.Helper()
	compiled, ok := runtime.compiledSpec(nativeType)
	if !ok {
		t.Fatalf("no spec for %s", nativeType)
	}
	kind := compiled.ResourceKind
	return &kind
}

func TestCloudControlChildInventoryIteratesEveryParentWithBoundCursor(t *testing.T) {
	client := &scriptedCloudControl{pages: map[string]CloudControlPage{
		pageKey("AWS::EKS::Cluster", "", ""):                           {Resources: []CloudControlResource{{Identifier: "prod", Properties: `{"Name":"prod"}`}}, NextToken: "c2"},
		pageKey("AWS::EKS::Cluster", "", "c2"):                         {Resources: []CloudControlResource{{Identifier: "dev", Properties: `{"Name":"dev"}`}}},
		pageKey("AWS::EKS::Nodegroup", `{"ClusterName":"dev"}`, ""):    {RequestID: "dev-1", Resources: []CloudControlResource{{Identifier: "dev|ng-a", Properties: `{"ClusterName":"dev","NodegroupName":"ng-a"}`}}},
		pageKey("AWS::EKS::Nodegroup", `{"ClusterName":"prod"}`, ""):   {RequestID: "prod-1", NextToken: "n2", Resources: []CloudControlResource{{Identifier: "prod|ng-b", Properties: `{"ClusterName":"prod"}`}}},
		pageKey("AWS::EKS::Nodegroup", `{"ClusterName":"prod"}`, "n2"): {RequestID: "prod-2", Resources: []CloudControlResource{{Identifier: "prod|ng-c", Properties: `{"ClusterName":"prod"}`}}},
	}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	factory := &runtimeFactory{cloudControl: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: cloudControlSource, ResourceKind: kindFor(t, runtime, "AWS::EKS::Nodegroup"),
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "eu-west-1", Location: "eu-west-1"},
	}
	var identifiers []string
	var cursors []string
	for step := 0; step < 5; step++ {
		batch, err := runtime.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			identifiers = append(identifiers, item.NativeID)
		}
		if batch.Complete {
			if batch.NextCursor != "" {
				t.Fatal("complete batch must not carry a cursor")
			}
			break
		}
		cursors = append(cursors, batch.NextCursor)
		request.Cursor = batch.NextCursor
	}
	if strings.Join(identifiers, ",") != "dev|ng-a,prod|ng-b,prod|ng-c" || len(cursors) != 2 || factory.cloudRegion != "eu-west-1" {
		t.Fatalf("identifiers=%v cursors=%v region=%s", identifiers, cursors, factory.cloudRegion)
	}

	// A parent created between pages changes the fingerprint; resuming would
	// silently skip or repeat children, so the shard must restart.
	client.pages[pageKey("AWS::EKS::Cluster", "", "c2")] = CloudControlPage{Resources: []CloudControlResource{{Identifier: "dev"}, {Identifier: "stage"}}}
	request.Cursor = cursors[1]
	if _, err := runtime.List(context.Background(), request); err == nil || !strings.Contains(err.Error(), "parent set changed") {
		t.Fatalf("changed parent set error = %v", err)
	}
}

func TestCloudControlInventoryFailsShardWhenParentListingFails(t *testing.T) {
	client := &scriptedCloudControl{pages: map[string]CloudControlPage{}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	runtime, err := newRuntime(source, &runtimeFactory{cloudControl: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: cloudControlSource, ResourceKind: kindFor(t, runtime, "AWS::EFS::MountTarget"),
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-2", Location: "us-east-2"},
	})
	if err == nil {
		t.Fatal("an unreadable parent set must not produce an empty authoritative child list")
	}
}

func TestCloudControlWAFInventoryIncludesCloudFrontScopeOnlyInUSEast1(t *testing.T) {
	client := &scriptedCloudControl{pages: map[string]CloudControlPage{
		pageKey("AWS::WAFv2::WebACL", `{"Scope":"REGIONAL"}`, ""):   {Resources: []CloudControlResource{{Identifier: "regional|id|REGIONAL"}}},
		pageKey("AWS::WAFv2::WebACL", `{"Scope":"CLOUDFRONT"}`, ""): {Resources: []CloudControlResource{{Identifier: "edge|id|CLOUDFRONT"}}},
	}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	runtime, err := newRuntime(source, &runtimeFactory{cloudControl: client})
	if err != nil {
		t.Fatal(err)
	}
	collect := func(region string) []string {
		request := contracts.InventoryRequest{
			ConnectionID: "connection-a", Source: cloudControlSource, ResourceKind: kindFor(t, runtime, "AWS::WAFv2::WebACL"),
			Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: region, Location: region},
		}
		var result []string
		for {
			batch, err := runtime.List(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				result = append(result, item.NativeID)
			}
			if batch.Complete {
				return result
			}
			request.Cursor = batch.NextCursor
		}
	}
	if got := strings.Join(collect("us-east-1"), ","); got != "edge|id|CLOUDFRONT,regional|id|REGIONAL" {
		t.Fatalf("us-east-1 ACLs = %s", got)
	}
	if got := strings.Join(collect("eu-central-1"), ","); got != "regional|id|REGIONAL" {
		t.Fatalf("eu-central-1 ACLs = %s", got)
	}
}

func TestCloudControlQuickSightInventoryUsesConnectionAccount(t *testing.T) {
	client := &scriptedCloudControl{pages: map[string]CloudControlPage{
		pageKey("AWS::QuickSight::Dashboard", `{"AwsAccountId":"123456789012"}`, ""): {Resources: []CloudControlResource{{Identifier: "123456789012|sales"}}},
	}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	runtime, err := newRuntime(source, &runtimeFactory{cloudControl: client, accountID: "123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: cloudControlSource, ResourceKind: kindFor(t, runtime, "AWS::QuickSight::Dashboard"),
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-west-2", Location: "us-west-2"},
	})
	if err != nil || len(batch.Items) != 1 || !batch.Complete {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
}

func TestCloudControlGlobalInventoryUsesServiceHomeRegion(t *testing.T) {
	client := &scriptedCloudControl{pages: map[string]CloudControlPage{
		pageKey("AWS::GlobalAccelerator::Accelerator", "", ""): {Resources: []CloudControlResource{}},
	}}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	factory := &runtimeFactory{cloudControl: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: cloudControlSource, ResourceKind: kindFor(t, runtime, "AWS::GlobalAccelerator::Accelerator"),
		Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "123456789012/global", Location: "ap-northeast-1"},
	})
	if err != nil || factory.cloudRegion != "us-west-2" {
		t.Fatalf("region=%s err=%v", factory.cloudRegion, err)
	}
}

func protectedActionRequest(nativeType, identifier string) contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset: asset.Asset{Location: "us-east-1", Normalized: map[string]any{"cloudControlIdentifier": identifier}, Identity: asset.Identity{
			Provider: asset.ProviderAWS, ConnectionID: "connection-a", NativeType: nativeType, NativeID: identifier,
		}},
		Action: "delete", IdempotencyKey: "step-1",
	}
}

func TestCloudControlActionDisablesDeletionProtectionBeforeDelete(t *testing.T) {
	client := &scriptedCloudControl{
		resources: map[string]CloudControlResource{"AWS::EC2::Instance|i-1": {Identifier: "i-1", Properties: `{"InstanceId":"i-1","DisableApiTermination":true}`}},
		statuses: map[string]CloudControlProgress{
			"update-token": {RequestToken: "update-token", Status: "SUCCESS"},
			"delete-token": {RequestToken: "delete-token", Status: "SUCCESS"},
		},
	}
	source := &runtimeCredentialSource{want: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret"}}}
	runtime, err := newRuntime(source, &runtimeFactory{cloudControl: client})
	if err != nil {
		t.Fatal(err)
	}
	request := protectedActionRequest("AWS::EC2::Instance", "i-1")
	driver, err := runtime.ResolveAction(context.Background(), "connection-a", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := driver.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed || preflight.Evidence["deletion_protection"] != true || preflight.Evidence["pre_delete_action"] != cloudControlPhaseDisableProtection {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(client.updates) != 1 || len(client.deletes) != 0 {
		t.Fatalf("result=%+v updates=%+v deletes=%v err=%v", result, client.updates, client.deletes, err)
	}
	var patch []map[string]any
	if err := json.Unmarshal([]byte(client.updates[0].PatchDocument), &patch); err != nil || len(patch) != 1 || patch[0]["path"] != "/DisableApiTermination" || patch[0]["value"] != false || client.updates[0].ClientToken != "step-1:disable-deletion-protection" {
		t.Fatalf("update = %+v", client.updates[0])
	}

	// The update succeeded but the live model still reports protection.
	wait, err := driver.Wait(context.Background(), request, contracts.ActionResult{ProviderOperationID: result.ProviderOperationID, Data: result.Data})
	if err == nil || len(client.deletes) != 0 {
		t.Fatalf("delete must wait for protection readback: wait=%+v err=%v", wait, err)
	}

	client.resources["AWS::EC2::Instance|i-1"] = CloudControlResource{Identifier: "i-1", Properties: `{"InstanceId":"i-1","DisableApiTermination":false}`}
	wait, err = driver.Wait(context.Background(), request, contracts.ActionResult{ProviderOperationID: result.ProviderOperationID, Data: result.Data})
	if err != nil || wait.Done || len(client.deletes) != 1 || client.deletes[0] != "i-1|step-1" || wait.Data["phase"] != cloudControlPhaseDelete {
		t.Fatalf("wait=%+v deletes=%v err=%v", wait, client.deletes, err)
	}
	// The executor persists wait data, not the original operation ID.
	wait, err = driver.Wait(context.Background(), request, contracts.ActionResult{ProviderOperationID: result.ProviderOperationID, Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "SUCCESS" {
		t.Fatalf("final wait=%+v err=%v", wait, err)
	}
}

func TestCloudControlProtectionPatchesKeyedLoadBalancerAttribute(t *testing.T) {
	runtime, err := newRuntime(&runtimeCredentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, _ := runtime.compiledSpec("AWS::ElasticLoadBalancingV2::LoadBalancer")
	protection, err := cloudControlProtectionFromSpec("AWS::ElasticLoadBalancingV2::LoadBalancer", compiled.Definition.Actions["delete"])
	if err != nil {
		t.Fatal(err)
	}
	model := map[string]any{"LoadBalancerAttributes": []any{
		map[string]any{"Key": "idle_timeout.timeout_seconds", "Value": "60"},
		map[string]any{"Key": "deletion_protection.enabled", "Value": "true"},
	}}
	enabled, patch, err := protection.evaluate(model)
	if err != nil || !enabled || patch != `[{"op":"replace","path":"/LoadBalancerAttributes/1/Value","value":"false"}]` {
		t.Fatalf("enabled=%v patch=%s err=%v", enabled, patch, err)
	}
	model["LoadBalancerAttributes"] = []any{map[string]any{"Key": "deletion_protection.enabled", "Value": "false"}}
	if enabled, _, _ := protection.evaluate(model); enabled {
		t.Fatal("disabled attribute must not trigger an update")
	}
}

func TestCloudControlReferenceDerivation(t *testing.T) {
	model := map[string]any{"Volumes": []any{map[string]any{"VolumeId": "vol-2"}, map[string]any{"VolumeId": "vol-1"}}, "NetworkInterfaces": []any{map[string]any{"NetworkInterfaceId": "eni-1"}}}
	deriveCloudControlReferences("AWS::EC2::Instance", model)
	volumes, _ := model["volume_ids"].([]string)
	if strings.Join(volumes, ",") != "vol-1,vol-2" || strings.Join(model["network_interface_ids"].([]string), ",") != "eni-1" {
		t.Fatalf("instance references = %+v", model)
	}
	service := map[string]any{"Cluster": "arn:aws:ecs:us-east-1:123456789012:cluster/web"}
	deriveCloudControlReferences("AWS::ECS::Service", service)
	endpoint := map[string]any{"ServiceName": "com.amazonaws.vpce.us-east-1.vpce-svc-0abc"}
	deriveCloudControlReferences("AWS::EC2::VPCEndpoint", endpoint)
	awsService := map[string]any{"ServiceName": "com.amazonaws.us-east-1.s3"}
	deriveCloudControlReferences("AWS::EC2::VPCEndpoint", awsService)
	if service["cluster_name"] != "web" || endpoint["service_id"] != "vpce-svc-0abc" || awsService["service_id"] != nil {
		t.Fatalf("service=%+v endpoint=%+v aws=%+v", service, endpoint, awsService)
	}
}
