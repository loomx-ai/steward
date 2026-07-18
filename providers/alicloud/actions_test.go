package alicloud_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

type actionACKClient struct {
	clusterDetail           alicloud.ACKClusterDetail
	detailRequestID         string
	modifyProtectionCalls   int
	modifyProtectionEnabled bool
	deleteResponse          alicloud.DeleteClusterResponse
	deleteRequest           alicloud.DeleteClusterRequest
	deleteCalls             int
}

type invocationProvider struct {
	results     []contracts.InvocationResult
	errors      []error
	invocations []contracts.Invocation
}

func disabledDeletionProtectionData(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	cloned, ok := cloneActionTestValue(data).(map[string]any)
	if !ok {
		t.Fatal("clone deletion protection response")
	}
	if !disableDeletionProtectionValue(cloned) {
		t.Fatalf("deletion protection field not found in response: %#v", data)
	}
	return cloned
}

func cloneActionTestValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneActionTestValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneActionTestValue(item)
		}
		return cloned
	default:
		return value
	}
}

func disableDeletionProtectionValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if config, ok := typed["DeletionProtectionConfig"].(map[string]any); ok {
			config["Enabled"] = false
			return true
		}
		for _, key := range []string{
			"DeletionProtection",
			"DeleteProtection",
			"GroupDeletionProtection",
			"IsDeletionProtection",
			"DeletionLock",
			"InstanceReleaseProtection",
			"DBInstanceReleaseProtection",
		} {
			current, exists := typed[key]
			if !exists {
				continue
			}
			switch current.(type) {
			case bool:
				typed[key] = false
			case float64:
				typed[key] = float64(0)
			case string:
				switch current {
				case "Enabled":
					typed[key] = "Disabled"
				case "on":
					typed[key] = "off"
				default:
					typed[key] = "false"
				}
			}
			return true
		}
		for _, item := range typed {
			if disableDeletionProtectionValue(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if disableDeletionProtectionValue(item) {
				return true
			}
		}
	}
	return false
}

func (p *invocationProvider) Provider() asset.Provider { return asset.ProviderAliCloud }

func (p *invocationProvider) Invoke(_ context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	p.invocations = append(p.invocations, invocation)
	result := p.results[0]
	p.results = p.results[1:]
	var err error
	if len(p.errors) > 0 {
		err = p.errors[0]
		p.errors = p.errors[1:]
	}
	return result, err
}

func (c *actionACKClient) DescribeClusterResources(context.Context, string, bool) ([]alicloud.ClusterResource, string, error) {
	return nil, "describe-request", nil
}

func (c *actionACKClient) DescribeClusterNodes(context.Context, string) ([]alicloud.ClusterNode, string, error) {
	return nil, "nodes-request", nil
}

func (c *actionACKClient) DescribeClusterDetail(_ context.Context, clusterID string) (alicloud.ACKClusterDetail, string, error) {
	if c.clusterDetail.ClusterID == "" {
		c.clusterDetail.ClusterID = clusterID
	}
	return c.clusterDetail, c.detailRequestID, nil
}

func (c *actionACKClient) ModifyClusterDeletionProtection(_ context.Context, _ string, enabled bool) (string, error) {
	c.modifyProtectionCalls++
	c.modifyProtectionEnabled = enabled
	return "modify-protection-request", nil
}

func (c *actionACKClient) DeleteCluster(_ context.Context, request alicloud.DeleteClusterRequest) (alicloud.DeleteClusterResponse, error) {
	c.deleteCalls++
	c.deleteRequest = request
	return c.deleteResponse, nil
}

func TestACKDeleteMapsControllerOptionsAndReturnsProviderIDs(t *testing.T) {
	t.Parallel()

	client := &actionACKClient{deleteResponse: alicloud.DeleteClusterResponse{ClusterID: "c-a", RequestID: "delete-request", TaskID: "task-a"}}
	hook := alicloud.NewACKHook(client)
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset:  asset.Asset{ID: "cluster-a", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType, NativeID: "c-a"}},
		Action: "delete",
		Parameters: map[string]any{
			"retain_all_resources": false,
			"retain_resources":     []any{"vpc-a", "sg-a"},
			"delete_options": []any{
				map[string]any{"resource_type": "SLB", "delete_mode": "delete"},
				map[string]any{"resource_type": "SLS_Data", "delete_mode": "retain"},
			},
		},
		IdempotencyKey: "plan-step-a",
	})
	if err != nil {
		t.Fatalf("delete ACK cluster: %v", err)
	}
	if client.deleteCalls != 1 || client.deleteRequest.ClusterID != "c-a" || client.deleteRequest.RetainAllResources || len(client.deleteRequest.RetainResources) != 2 || len(client.deleteRequest.DeleteOptions) != 2 {
		t.Fatalf("delete request=%+v calls=%d", client.deleteRequest, client.deleteCalls)
	}
	if result.ProviderRequestID != "delete-request" || result.ProviderOperationID != "task-a" {
		t.Fatalf("action result=%+v", result)
	}
	if client.modifyProtectionCalls != 0 {
		t.Fatalf("already unprotected cluster protection calls=%d", client.modifyProtectionCalls)
	}
}

func TestACKDeleteDisablesClusterDeletionProtection(t *testing.T) {
	t.Parallel()

	client := &actionACKClient{
		clusterDetail: alicloud.ACKClusterDetail{
			ClusterID:          "c-protected",
			State:              "running",
			DeletionProtection: true,
		},
		deleteResponse: alicloud.DeleteClusterResponse{
			ClusterID: "c-protected", RequestID: "delete-request", TaskID: "task-a",
		},
	}
	hook := alicloud.NewACKHook(client)
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType,
			NativeID: "c-protected",
		}},
		Action: "delete", IdempotencyKey: "step-ack-protected",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.modifyProtectionCalls != 1 || client.modifyProtectionEnabled ||
		client.deleteCalls != 1 ||
		result.Data["deletion_protection_disabled"] != true {
		t.Fatalf("client=%+v result=%+v", client, result)
	}
}

func TestResourceActionPerformsLivePreflightDeleteAndReadback(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "preflight-request", Data: map[string]any{"Instances": map[string]any{"Instance": []any{map[string]any{"InstanceId": "i-a", "Status": "Stopped"}}}}},
		{RequestID: "protection-request", Data: map[string]any{"Instances": map[string]any{"Instance": []any{map[string]any{"InstanceId": "i-a", "DeletionProtection": false}}}}},
		{RequestID: "delete-request"},
		{},
	}, errors: []error{nil, nil, nil, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorNotFound, Code: "InvalidInstanceId.NotFound"}}}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::ECS::Instance")
	if err != nil {
		t.Fatalf("create ECS action hook: %v", err)
	}
	request := contracts.ActionRequest{
		Asset:  asset.Asset{ID: "asset-a", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", NativeID: "i-a"}},
		Action: "delete", IdempotencyKey: "step-a",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed || preflight.Evidence["provider_request_id"] != "preflight-request" {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete-request" || result.RetryAfter != 2*time.Second {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.RetryAfter != 0 {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[0].Operation != "AlibabaCloud.DescribeInstances" ||
		provider.invocations[1].Operation != "AlibabaCloud.DescribeInstances" ||
		provider.invocations[2].Operation != "AlibabaCloud.DeleteInstance" ||
		provider.invocations[3].Operation != "AlibabaCloud.DescribeInstances" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	if provider.invocations[2].ConnectionID != "connection-a" ||
		provider.invocations[2].Scope["region"] != "cn-hangzhou" ||
		provider.invocations[2].Parameters["InstanceId"] != "i-a" ||
		provider.invocations[2].Parameters["Force"] != true {
		t.Fatalf("delete invocation=%+v", provider.invocations[2])
	}
}

func TestOSSBucketActionUsesCanonicalLocationForDeleteAndReadback(t *testing.T) {
	t.Parallel()

	type bucket struct {
		Name string `json:"Name"`
	}
	type bucketInfo struct {
		Bucket *bucket `json:"Bucket"`
	}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{
				RequestID: "preflight-request",
				Data: map[string]any{
					"BucketInfo": &bucketInfo{Bucket: &bucket{Name: "bucket-a"}},
				},
			},
			{RequestID: "versioning-request", Data: map[string]any{
				"VersioningConfiguration": map[string]any{},
			}},
			{RequestID: "delete-request"},
			{
				RequestID: "readback-request",
				Data: map[string]any{
					"ecCode":    "0015-00000101",
					"hostId":    "bucket-a.oss-cn-hangzhou.aliyuncs.com",
					"requestId": "readback-request",
				},
			},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	if timeout := hook.DeletionCheckTimeout(); timeout != 6*time.Hour {
		t.Fatalf("OSS deletion check timeout=%v", timeout)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "bucket-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::OSS::Bucket",
				NativeID:   "bucket-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed || preflight.Absent {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete-request" ||
		result.Data["versioning_status_before"] != "Unversioned" ||
		result.Data["versioning_suspended"] != nil {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	if provider.invocations[1].Operation != "AlibabaCloud.OSS.GetBucketVersioning" ||
		provider.invocations[2].Operation != "AlibabaCloud.OSS.DeleteBucket" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	for _, invocation := range provider.invocations {
		if invocation.Parameters["bucket"] != "bucket-a" ||
			invocation.Parameters["location"] != "cn-hangzhou" {
			t.Fatalf("invocation=%+v", invocation)
		}
	}
}

func TestOSSBucketActionSuspendsVersioningBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "preflight", Data: map[string]any{
			"BucketInfo": map[string]any{"Bucket": map[string]any{"Name": "bucket-a"}},
		}},
		{RequestID: "get-versioning", Data: map[string]any{
			"VersioningConfiguration": map[string]any{"Status": "Enabled"},
		}},
		{RequestID: "put-versioning"},
		{RequestID: "delete-bucket"},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::OSS::Bucket", NativeID: "bucket-a",
		}},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	if _, err := hook.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete-bucket" ||
		result.Data["versioning_status_before"] != "Enabled" ||
		result.Data["versioning_suspended"] != true ||
		result.Data["put_bucket_versioning_request_id"] != "put-versioning" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wantOperations := []string{
		"AlibabaCloud.OSS.GetBucketInfo",
		"AlibabaCloud.OSS.GetBucketVersioning",
		"AlibabaCloud.OSS.PutBucketVersioning",
		"AlibabaCloud.OSS.DeleteBucket",
	}
	if len(provider.invocations) != len(wantOperations) {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	for index, want := range wantOperations {
		if provider.invocations[index].Operation != want {
			t.Fatalf("invocation[%d]=%+v want=%s", index, provider.invocations[index], want)
		}
	}
	configuration, _ := provider.invocations[2].Parameters["VersioningConfiguration"].(map[string]any)
	if configuration["Status"] != "Suspended" ||
		provider.invocations[2].IdempotencyKey != "step-bucket-a:suspend-versioning" {
		t.Fatalf("PutBucketVersioning invocation=%+v", provider.invocations[2])
	}
}

func TestOSSBucketActionRejectsUnexpectedGetBucketInfoErrorEnvelope(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "preflight-request",
		Data: map[string]any{
			"ecCode":    "0015-00000102",
			"requestId": "preflight-request",
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "bucket-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::OSS::Bucket",
				NativeID:   "bucket-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	})
	if err == nil || !strings.Contains(err.Error(), `ecCode "0015-00000102"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestOSSBucketActionEmptiesBucketBeforeRetryingDelete(t *testing.T) {
	t.Parallel()

	type bucket struct {
		Name string `json:"Name"`
	}
	type bucketInfo struct {
		Bucket *bucket `json:"Bucket"`
	}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{RequestID: "preflight", Data: map[string]any{
				"BucketInfo": &bucketInfo{Bucket: &bucket{Name: "bucket-a"}},
			}},
			{RequestID: "get-versioning", Data: map[string]any{
				"VersioningConfiguration": map[string]any{"Status": "Suspended"},
			}},
			{},
			{RequestID: "list-versions", Data: map[string]any{
				"ListVersionsResult": map[string]any{
					"EncodingType": "url", "IsTruncated": false,
					"Version": []any{map[string]any{
						"Key": "dir%2Fobject.txt", "VersionId": "version-a",
					}, map[string]any{
						"Key": ".dlsdata%2Finternal", "VersionId": "dls-version",
					}},
					"DeleteMarker": []any{map[string]any{
						"Key": "deleted.txt", "VersionId": "marker-a",
					}},
				},
			}},
			{RequestID: "delete-objects"},
			{RequestID: "list-current-objects", Data: map[string]any{
				"ListBucketResult": map[string]any{
					"EncodingType": "url", "IsTruncated": false,
					"Contents": map[string]any{"Key": "current%2Fobject.txt"},
				},
			}},
			{RequestID: "delete-current-objects"},
			{RequestID: "list-current-objects-empty", Data: map[string]any{
				"ListBucketResult": map[string]any{"IsTruncated": false},
			}},
			{RequestID: "list-uploads", Data: map[string]any{
				"ListMultipartUploadsResult": map[string]any{
					"EncodingType": "url", "IsTruncated": false,
					"Upload": []any{map[string]any{
						"Key": "upload%2Fobject.bin", "UploadId": "upload-a",
					}},
				},
			}},
			{RequestID: "abort-upload"},
			{RequestID: "list-live-channels", Data: map[string]any{
				"ListLiveChannelResult": map[string]any{
					"IsTruncated": false,
					"LiveChannel": []any{map[string]any{
						"Name": "channel-a",
					}},
				},
			}},
			{RequestID: "delete-live-channel"},
			{RequestID: "delete-bucket"},
			{RequestID: "readback", Data: map[string]any{
				"ecCode": "0015-00000101",
			}},
		},
		errors: []error{
			nil,
			nil,
			&contracts.ProviderCallError{Provider: execution.ProviderError{
				Category:  execution.ErrorConflict,
				Code:      "BucketNotEmpty",
				Message:   "The bucket has objects. Please delete them first.",
				RequestID: "delete-non-empty",
			}},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "bucket-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::OSS::Bucket",
				NativeID:   "bucket-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "delete_object_versions" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	for _, wantPhase := range []string{
		"delete_current_objects",
		"delete_current_objects",
		"abort_multipart_uploads",
		"delete_live_channels",
		"retry_delete_bucket",
		"bucket_delete_requested",
	} {
		wait, waitErr := hook.Wait(context.Background(), request, result)
		if waitErr != nil || wait.State != wantPhase {
			t.Fatalf("wait=%+v err=%v want phase=%s", wait, waitErr, wantPhase)
		}
		result.Data = wait.Data
		if wantPhase == "bucket_delete_requested" && !wait.Done {
			t.Fatalf("final wait=%+v", wait)
		}
	}
	readback, err := hook.Readback(context.Background(), request)
	if err != nil || readback.Exists {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	wantOperations := []string{
		"AlibabaCloud.OSS.GetBucketInfo",
		"AlibabaCloud.OSS.GetBucketVersioning",
		"AlibabaCloud.OSS.DeleteBucket",
		"AlibabaCloud.OSS.ListObjectVersions",
		"AlibabaCloud.OSS.DeleteMultipleObjects",
		"AlibabaCloud.OSS.ListObjects",
		"AlibabaCloud.OSS.DeleteMultipleObjects",
		"AlibabaCloud.OSS.ListObjects",
		"AlibabaCloud.OSS.ListMultipartUploads",
		"AlibabaCloud.OSS.AbortMultipartUpload",
		"AlibabaCloud.OSS.ListLiveChannel",
		"AlibabaCloud.OSS.DeleteLiveChannel",
		"AlibabaCloud.OSS.DeleteDataLakeBucket",
		"AlibabaCloud.OSS.GetBucketInfo",
	}
	if len(provider.invocations) != len(wantOperations) {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	for index, want := range wantOperations {
		if provider.invocations[index].Operation != want {
			t.Fatalf("invocation[%d]=%+v want=%s", index, provider.invocations[index], want)
		}
	}
	deleteBody, _ := provider.invocations[4].Parameters["Delete"].(map[string]any)
	objects, _ := deleteBody["Object"].([]any)
	firstObject, _ := objects[0].(map[string]any)
	secondObject, _ := objects[1].(map[string]any)
	if len(objects) != 2 || firstObject["Key"] != "dir/object.txt" ||
		firstObject["VersionId"] != "version-a" ||
		secondObject["Key"] != "deleted.txt" || secondObject["VersionId"] != "marker-a" ||
		provider.invocations[9].Parameters["object"] != "upload/object.bin" ||
		provider.invocations[9].Parameters["uploadId"] != "upload-a" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	currentDeleteBody, _ := provider.invocations[6].Parameters["Delete"].(map[string]any)
	currentObjects, _ := currentDeleteBody["Object"].([]any)
	currentObject, _ := currentObjects[0].(map[string]any)
	if len(currentObjects) != 1 || currentObject["Key"] != "current/object.txt" ||
		currentObject["VersionId"] != nil {
		t.Fatalf("current object delete=%+v", provider.invocations[6].Parameters)
	}
	if result.Data["data_lake_storage_detected"] != true ||
		result.Data["data_lake_entries_skipped"] != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestOSSBucketActionPausesHDFSAndDeletesHDFSFilesBeforeOSSObjects(t *testing.T) {
	t.Parallel()

	type bucket struct {
		Name string `json:"Name"`
	}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{RequestID: "preflight", Data: map[string]any{
				"BucketInfo": map[string]any{"Bucket": &bucket{Name: "bucket-a"}},
			}},
			{RequestID: "get-versioning", Data: map[string]any{
				"VersioningConfiguration": map[string]any{"Status": "Suspended"},
			}},
			{},
			{RequestID: "pause-hdfs"},
			{RequestID: "get-safe-mode", Data: map[string]any{
				"response": map[string]any{
					"NamespaceConfiguration": map[string]any{
						"property": map[string]any{
							"name": "namespace.safemode.enable", "value": "true",
						},
					},
				},
			}},
			{RequestID: "list-hdfs", Data: map[string]any{
				"response": map[string]any{
					"isTruncated": false,
					"fileStatuses": map[string]any{
						"status": map[string]any{"path": ".sysinfo"},
					},
				},
			}},
			{RequestID: "delete-hdfs", Data: map[string]any{
				"response": map[string]any{"result": true},
			}},
			{RequestID: "list-hdfs-empty", Data: map[string]any{
				"response": map[string]any{
					"isTruncated": false, "fileStatuses": map[string]any{},
				},
			}},
			{RequestID: "list-objects", Data: map[string]any{
				"ListVersionsResult": map[string]any{
					"IsTruncated": false,
					"Version": map[string]any{
						"Key": ".dlsdata/system", "VersionId": "hdfs-version",
					},
				},
			}},
			{RequestID: "delete-objects"},
			{RequestID: "list-current-objects-empty", Data: map[string]any{
				"ListBucketResult": map[string]any{"IsTruncated": false},
			}},
		},
		errors: []error{
			nil,
			nil,
			&contracts.ProviderCallError{Provider: execution.ProviderError{
				Category:  execution.ErrorPermissionDenied,
				Code:      "AccessDenied",
				Message:   "Data lake storage is disabled.",
				RequestID: "delete-request",
			}},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider:   asset.ProviderAliCloud,
			NativeType: "ACS::OSS::Bucket",
			NativeID:   "bucket-a",
		}},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	if _, err := hook.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "pause_hdfs" ||
		result.Data["trigger_code"] != "AccessDenied" ||
		result.Data["hdfs_cleanup_automatic"] != true ||
		result.Data["manual_action_required"] != nil ||
		result.RetryAfter != 250*time.Millisecond {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "wait_hdfs_paused" || wait.Done ||
		wait.RetryAfter != 5*time.Second ||
		wait.Data["hdfs_safe_mode_requested"] != true {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[3].Operation != "AlibabaCloud.OSS.PutBucketHDFSConfig" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	putRequest, _ := provider.invocations[3].Parameters["request"].(map[string]any)
	putParameters, _ := putRequest["parameters"].(map[string]any)
	configuration, _ := putParameters["NamespaceConfiguration"].(map[string]any)
	property, _ := configuration["property"].(map[string]any)
	if putRequest["requestType"] != "putConfig" ||
		property["name"] != "namespace.safemode.enable" || property["value"] != "true" {
		t.Fatalf("pause parameters=%+v", provider.invocations[3].Parameters)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "delete_hdfs_files" || wait.Done ||
		wait.RetryAfter != 250*time.Millisecond || wait.Data["hdfs_safe_mode"] != true {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "delete_hdfs_files" || wait.Done ||
		wait.Data["hdfs_files_deleted"] != 1 {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if provider.invocations[6].Operation != "AlibabaCloud.OSS.DeleteBucketHDFSFile" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "delete_object_versions" || wait.Done ||
		wait.Data["hdfs_namespace_empty"] != true {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "delete_current_objects" || wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "abort_multipart_uploads" || wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	deleteBody, _ := provider.invocations[9].Parameters["Delete"].(map[string]any)
	objects, _ := deleteBody["Object"].([]any)
	object, _ := objects[0].(map[string]any)
	if len(objects) != 1 || object["Key"] != ".dlsdata/system" {
		t.Fatalf("DeleteMultipleObjects parameters=%+v", provider.invocations[9].Parameters)
	}
}

func TestOSSBucketActionRestoresHDFSWhenCleanupFails(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "list-failed", Data: map[string]any{
			"response": map[string]any{"errCode": "13", "errMsg": "denied"},
		}},
		{RequestID: "restore-hdfs"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "ap-southeast-1", "ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::OSS::Bucket", NativeID: "bucket-a",
		}},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	_, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: map[string]any{
		"phase":                    "delete_hdfs_files",
		"hdfs_safe_mode_requested": true,
	}})
	if err == nil || !strings.Contains(err.Error(), "code=13") {
		t.Fatalf("Wait() error=%v", err)
	}
	if len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "AlibabaCloud.OSS.PutBucketHDFSConfig" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	restoreRequest, _ := provider.invocations[1].Parameters["request"].(map[string]any)
	restoreParameters, _ := restoreRequest["parameters"].(map[string]any)
	configuration, _ := restoreParameters["NamespaceConfiguration"].(map[string]any)
	property, _ := configuration["property"].(map[string]any)
	if property["value"] != "false" {
		t.Fatalf("restore parameters=%+v", provider.invocations[1].Parameters)
	}
}

func TestOSSBucketActionKeepsHDFSPausedUntilBucketIsDeleted(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "delete-bucket",
	}}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "ap-southeast-1", "ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::OSS::Bucket", NativeID: "bucket-a",
		}},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	result := contracts.ActionResult{Data: map[string]any{
		"phase": "retry_delete_bucket", "hdfs_safe_mode": true,
		"data_lake_storage_detected": true,
	}}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "bucket_delete_requested" ||
		len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.OSS.DeleteDataLakeBucket" {
		t.Fatalf("delete=%+v err=%v invocations=%+v", wait, err, provider.invocations)
	}
}

func TestOSSBucketActionCapsDeleteMultipleObjectsAtOneThousand(t *testing.T) {
	t.Parallel()

	versions := make([]any, 1001)
	for index := range versions {
		versions[index] = map[string]any{
			"Key": "object", "VersionId": "version",
		}
	}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{RequestID: "preflight", Data: map[string]any{
				"BucketInfo": map[string]any{"Bucket": map[string]any{"Name": "bucket-a"}},
			}},
			{RequestID: "get-versioning", Data: map[string]any{
				"VersioningConfiguration": map[string]any{"Status": "Suspended"},
			}},
			{},
			{RequestID: "list", Data: map[string]any{
				"ListVersionsResult": map[string]any{
					"IsTruncated": false,
					"Version":     versions,
				},
			}},
			{RequestID: "delete-1"},
			{RequestID: "delete-2"},
		},
		errors: []error{
			nil,
			nil,
			&contracts.ProviderCallError{Provider: execution.ProviderError{
				Category: execution.ErrorConflict,
				Code:     "BucketNotEmpty",
			}},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::OSS::Bucket",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider:   asset.ProviderAliCloud,
			NativeType: "ACS::OSS::Bucket",
			NativeID:   "bucket-a",
		}},
		Action: "delete", IdempotencyKey: "step-bucket-a",
	}
	if _, err := hook.Preflight(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.State != "delete_current_objects" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	for index, want := range []int{1000, 1} {
		invocation := provider.invocations[index+4]
		deleteBody, _ := invocation.Parameters["Delete"].(map[string]any)
		objects, _ := deleteBody["Object"].([]any)
		if invocation.Operation != "AlibabaCloud.OSS.DeleteMultipleObjects" ||
			len(objects) != want {
			t.Fatalf("invocation[%d]=%+v", index+4, invocation)
		}
	}
}

func TestResourceActionPreflightTreatsDeletingAsAbsent(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "preflight-deleting-request",
		Data: map[string]any{"Instances": map[string]any{"Instance": []any{
			map[string]any{"InstanceId": "i-deleting", "Status": "Deleting"},
		}}},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ECS::Instance",
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance",
			NativeID: "i-deleting",
		}},
		Action: "delete", IdempotencyKey: "step-deleting",
	})
	if err != nil || !result.Absent || result.Allowed ||
		result.Evidence["state"] != "Deleting" {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.DescribeInstances" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestACKPreflightTreatsDeletingAsAbsentWithoutListingResources(t *testing.T) {
	t.Parallel()

	client := &actionACKClient{
		clusterDetail: alicloud.ACKClusterDetail{
			ClusterID: "c-deleting", State: "Deleting",
		},
		detailRequestID: "detail-deleting-request",
	}
	hook := alicloud.NewACKHook(client)
	result, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType,
			NativeID: "c-deleting",
		}},
		Action: "delete", IdempotencyKey: "step-ack-deleting",
	})
	if err != nil || !result.Absent || result.Allowed ||
		result.Evidence["state"] != "Deleting" {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
}

func TestCENChildInstanceAttachmentActionDetachesBasicEditionNetwork(t *testing.T) {
	t.Parallel()

	attached := contracts.InvocationResult{
		RequestID: "describe-child-request",
		Data: map[string]any{
			"CenId": "cen-a", "ChildInstanceId": "vpc-a",
			"ChildInstanceType": "VPC", "ChildInstanceRegionId": "cn-hangzhou",
			"Status": "Attached",
		},
	}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			attached,
			{RequestID: "detach-child-request"},
			{},
		},
		errors: []error{
			nil,
			nil,
			&contracts.ProviderCallError{Provider: execution.ProviderError{
				Category: execution.ErrorNotFound, Code: "InvalidParameter.ChildInstance",
			}},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		alicloud.CENChildInstanceAttachmentNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.CENChildInstanceAttachmentNativeType,
				NativeID: "cen-a/vpc-a",
			},
			Normalized: map[string]any{
				"cenId": "cen-a", "childInstanceId": "vpc-a",
				"childInstanceType": "VPC", "regionId": "cn-hangzhou",
			},
		},
		Action: "delete", IdempotencyKey: "step-cen-child-a",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "detach-child-request" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 3 {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	read := provider.invocations[0]
	if read.Operation != "AlibabaCloud.CEN.DescribeCenAttachedChildInstanceAttribute" ||
		read.Parameters["CenId"] != "cen-a" ||
		read.Parameters["ChildInstanceId"] != "vpc-a" ||
		read.Parameters["ChildInstanceType"] != "VPC" ||
		read.Parameters["ChildInstanceRegionId"] != "cn-hangzhou" {
		t.Fatalf("read invocation=%+v", read)
	}
	detach := provider.invocations[1]
	if detach.Operation != "AlibabaCloud.CEN.DetachCenChildInstance" ||
		detach.Parameters["CenId"] != "cen-a" ||
		detach.Parameters["ChildInstanceId"] != "vpc-a" ||
		detach.Parameters["ChildInstanceType"] != "VPC" ||
		detach.Parameters["ChildInstanceRegionId"] != "cn-hangzhou" {
		t.Fatalf("detach invocation=%+v", detach)
	}
}

func TestNASMountTargetActionDeletesByFileSystemAndDomain(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "delete-mount-target-request",
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::NAS::MountTarget",
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "mount-target-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::NAS::MountTarget",
				NativeID:   "31a8e4-w.cn-hangzhou.nas.aliyuncs.com",
			},
			Normalized: map[string]any{"fileSystemId": "31a8e4"},
		},
		Action: "delete", IdempotencyKey: "step-mount-target-a",
	})
	if err != nil || result.ProviderRequestID != "delete-mount-target-request" {
		t.Fatalf("delete NAS mount target result=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.NAS.DeleteMountTarget" ||
		provider.invocations[0].Parameters["FileSystemId"] != "31a8e4" ||
		provider.invocations[0].Parameters["MountTargetDomain"] !=
			"31a8e4-w.cn-hangzhou.nas.aliyuncs.com" {
		t.Fatalf("NAS mount target invocation=%+v", provider.invocations)
	}
}

func TestNLBReadbackWaitsForManagedEIPsButNotUserSuppliedEIPs(t *testing.T) {
	t.Parallel()

	emptyNLB := contracts.InvocationResult{
		RequestID: "nlb-absent",
		Data:      map[string]any{"LoadBalancers": []any{}},
	}
	managedPending := contracts.InvocationResult{
		RequestID: "managed-pending",
		Data: map[string]any{"EipAddresses": map[string]any{"EipAddress": []any{
			map[string]any{
				"AllocationId":   "eip-managed",
				"Name":           "CREATE_BY_NLB.nlb-a",
				"ServiceManaged": float64(1),
				"Status":         "Releasing",
			},
		}}},
	}
	managedAbsent := contracts.InvocationResult{
		RequestID: "managed-absent",
		Data:      map[string]any{"EipAddresses": map[string]any{"EipAddress": []any{}}},
	}
	userSupplied := contracts.InvocationResult{
		RequestID: "user-retained",
		Data: map[string]any{"EipAddresses": map[string]any{"EipAddress": []any{
			map[string]any{
				"AllocationId":   "eip-user",
				"Name":           "user-eip",
				"ServiceManaged": float64(0),
				"Status":         "Available",
			},
		}}},
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		emptyNLB,
		managedPending,
		userSupplied,
		emptyNLB,
		managedAbsent,
		userSupplied,
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::NLB::LoadBalancer",
	)
	if err != nil {
		t.Fatalf("create NLB action hook: %v", err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "nlb-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::NLB::LoadBalancer",
				NativeID:   "nlb-a",
			},
			Normalized: map[string]any{
				alicloud.NormalizedNLBEIPIDsField: []any{"eip-user", "eip-managed"},
			},
		},
		Action: "delete", IdempotencyKey: "step-nlb-a",
	}

	wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{})
	if err != nil || wait.Done || wait.State != "managed_eips_pending" ||
		wait.RetryAfter != 5*time.Second {
		t.Fatalf("managed EIP pending wait = %+v, err=%v", wait, err)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{})
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("managed EIP absent wait = %+v, err=%v", wait, err)
	}
	if len(provider.invocations) != 6 {
		t.Fatalf("NLB managed EIP readback invocations = %+v", provider.invocations)
	}
	for _, index := range []int{1, 2, 4, 5} {
		if provider.invocations[index].Operation != "DescribeEipAddresses" ||
			provider.invocations[index].Parameters["PageSize"] != 1 {
			t.Fatalf("EIP readback invocation %d = %+v", index, provider.invocations[index])
		}
	}
}

func TestResourceActionDetachesAttachedDataDiskBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{RequestID: "execute-read", Data: map[string]any{"Disks": map[string]any{"Disk": []any{
				map[string]any{"DiskId": "d-a", "InstanceId": "i-a", "Type": "data", "Status": "In_use"},
			}}}},
			{RequestID: "detach-request"},
			{RequestID: "attached-read", Data: map[string]any{"Disks": map[string]any{"Disk": []any{
				map[string]any{"DiskId": "d-a", "InstanceId": "i-a", "Status": "In_use"},
			}}}},
			{RequestID: "detached-read", Data: map[string]any{"Disks": map[string]any{"Disk": []any{
				map[string]any{"DiskId": "d-a", "InstanceId": "", "Status": "Available"},
			}}}},
			{RequestID: "delete-request"},
			{},
		},
		errors: []error{nil, nil, nil, nil, nil, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorNotFound, Code: "InvalidDiskId.NotFound",
		}}},
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::ECS::Disk")
	if err != nil {
		t.Fatalf("create disk action hook: %v", err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "disk-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Disk", NativeID: "d-a",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"Type": "data", "InstanceId": "i-a", "DeleteWithInstance": true,
			}},
		},
		Action: "delete", IdempotencyKey: "step-disk-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "detach-request" || result.Data["phase"] != "detach" {
		t.Fatalf("detach result=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "detaching" {
		t.Fatalf("attached wait=%+v err=%v", wait, err)
	}
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" {
		t.Fatalf("detached wait=%+v err=%v", wait, err)
	}
	readback, err := hook.Readback(context.Background(), request)
	if err != nil || readback.Exists || readback.State != "absent" {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	if len(provider.invocations) != 6 ||
		provider.invocations[0].Operation != "DescribeDisks" ||
		provider.invocations[1].Operation != "DetachDisk" ||
		provider.invocations[4].Operation != "DeleteDisk" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	detach := provider.invocations[1]
	if detach.Parameters["DiskId"] != "d-a" ||
		detach.Parameters["InstanceId"] != "i-a" ||
		detach.Parameters["DeleteWithInstance"] != false ||
		detach.IdempotencyKey != "step-disk-a:detach" {
		t.Fatalf("detach invocation=%+v", detach)
	}
	if provider.invocations[4].IdempotencyKey != "step-disk-a" {
		t.Fatalf("delete invocation=%+v", provider.invocations[4])
	}
}

func TestVPCDeletePollingIntervalIsTwoSeconds(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "execute-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{"VpcId": "vpc-a", "Status": "Available"},
			}}},
		},
		{RequestID: "delete-request"},
		{
			RequestID: "wait-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{"VpcId": "vpc-a", "Status": "Available"},
			}}},
		},
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::VPC::VPC")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "vpc-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", NativeID: "vpc-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.RetryAfter != 2*time.Second {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "Available" || wait.RetryAfter != 2*time.Second {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
}

func TestVPCDeleteDetachesDhcpOptionsSetBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "execute-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{
					"VpcId": "vpc-a", "Status": "Available",
					"DhcpOptionsSetId": "dopt-a", "DhcpOptionsSetStatus": "InUse",
				},
			}}},
		},
		{RequestID: "detach-request"},
		{
			RequestID: "pending-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{
					"VpcId": "vpc-a", "Status": "Available",
					"DhcpOptionsSetId": "dopt-a", "DhcpOptionsSetStatus": "Pending",
				},
			}}},
		},
		{
			RequestID: "detached-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{"VpcId": "vpc-a", "Status": "Available"},
			}}},
		},
		{RequestID: "delete-dhcp-request"},
		{RequestID: "dhcp-absent-read-request", Data: map[string]any{"DhcpOptionsSets": []any{}}},
		{RequestID: "delete-vpc-request"},
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::VPC::VPC")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "vpc-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", NativeID: "vpc-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-vpc-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "detach-request" ||
		result.Data["phase"] != "detach_dhcp_options_set" ||
		result.Data["dhcp_options_set_id"] != "dopt-a" {
		t.Fatalf("detach result=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "detaching_dhcp_options_set:Pending" {
		t.Fatalf("pending detach wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "deleting_dhcp_options_set" ||
		wait.Data["dhcp_options_set_delete_request_id"] != "delete-dhcp-request" {
		t.Fatalf("detached wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" ||
		wait.Data["delete_request_id"] != "delete-vpc-request" {
		t.Fatalf("detached wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 7 {
		t.Fatalf("VPC DHCP detach invocations=%+v", provider.invocations)
	}
	detach := provider.invocations[1]
	if detach.Operation != "AlibabaCloud.DetachDhcpOptionsSetFromVpc" ||
		detach.Parameters["RegionId"] != "cn-hangzhou" ||
		detach.Parameters["VpcId"] != "vpc-a" ||
		detach.Parameters["DhcpOptionsSetId"] != "dopt-a" ||
		detach.IdempotencyKey != "step-vpc-a:detach-dhcp-options-set" {
		t.Fatalf("VPC DHCP detach invocation=%+v", detach)
	}
	if deleteDHCP := provider.invocations[4]; deleteDHCP.Operation != "AlibabaCloud.DeleteDhcpOptionsSet" ||
		deleteDHCP.Parameters["DhcpOptionsSetId"] != "dopt-a" ||
		deleteDHCP.IdempotencyKey != "step-vpc-a:delete-dhcp-options-set:dopt-a" {
		t.Fatalf("DHCP options set delete invocation=%+v", deleteDHCP)
	}
	if listDHCP := provider.invocations[5]; listDHCP.Operation != "AlibabaCloud.ListDhcpOptionsSets" ||
		listDHCP.Parameters["DhcpOptionsSetId.1"] != "dopt-a" ||
		listDHCP.Parameters["MaxResults"] != 1 {
		t.Fatalf("DHCP options set readback invocation=%+v", listDHCP)
	}
	if deleteVPC := provider.invocations[6]; deleteVPC.Operation != "DeleteVpc" ||
		deleteVPC.IdempotencyKey != "step-vpc-a" {
		t.Fatalf("VPC delete invocation=%+v", deleteVPC)
	}
}

func TestVPCDeleteContinuesDeletingScannedDhcpOptionsSetAfterDetach(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "detached-vpc-read-request",
			Data: map[string]any{"Vpcs": map[string]any{"Vpc": []any{
				map[string]any{"VpcId": "vpc-a", "Status": "Available"},
			}}},
		},
		{RequestID: "delete-dhcp-request"},
		{RequestID: "dhcp-absent-read-request", Data: map[string]any{"DhcpOptionsSets": []any{}}},
		{RequestID: "delete-vpc-request"},
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::VPC::VPC")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "vpc-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", NativeID: "vpc-a",
			},
			Normalized: map[string]any{"dhcpOptionsSetId": "dopt-a"},
		},
		Action: "delete", IdempotencyKey: "step-vpc-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "delete_dhcp_options_set" ||
		result.Data["dhcp_options_set_id"] != "dopt-a" {
		t.Fatalf("resumed DHCP delete result=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" ||
		wait.Data["delete_request_id"] != "delete-vpc-request" {
		t.Fatalf("resumed DHCP delete wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[1].Operation != "AlibabaCloud.DeleteDhcpOptionsSet" ||
		provider.invocations[2].Operation != "AlibabaCloud.ListDhcpOptionsSets" ||
		provider.invocations[3].Operation != "DeleteVpc" {
		t.Fatalf("resumed DHCP delete invocations=%+v", provider.invocations)
	}
}

func TestNASFileSystemDeleteRemovesLifecyclePoliciesFirst(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "list-lifecycle-policies",
			Data: map[string]any{
				"TotalCount": 2,
				"LifecyclePolicies": []any{
					map[string]any{
						"FileSystemId":        "31a8e4a",
						"LifecyclePolicyName": "policy-b",
					},
					map[string]any{
						"FileSystemId":      "31a8e4a",
						"LifecyclePolicyId": "lc-a",
					},
				},
			},
		},
		{RequestID: "delete-policy-a"},
		{RequestID: "delete-policy-b"},
		{RequestID: "delete-file-system"},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-guangzhou",
		alicloud.NASFileSystemNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "file-system-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.NASFileSystemNativeType,
				NativeID:   "31a8e4a",
			},
		},
		Action: "delete", IdempotencyKey: "step-nas-a",
	})
	if err != nil || result.ProviderRequestID != "delete-file-system" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[0].Operation != "AlibabaCloud.NAS.DescribeLifecyclePolicies" ||
		provider.invocations[1].Operation != "AlibabaCloud.NAS.DeleteLifecyclePolicy" ||
		provider.invocations[2].Operation != "AlibabaCloud.NAS.DeleteLifecyclePolicy" ||
		provider.invocations[3].Operation != "AlibabaCloud.NAS.DeleteFileSystem" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	firstDelete := provider.invocations[1]
	if firstDelete.Parameters["LifecyclePolicyId"] != "lc-a" ||
		firstDelete.Parameters["FileSystemId"] != "31a8e4a" ||
		firstDelete.IdempotencyKey != "step-nas-a:delete-lifecycle-policy:lc-a" {
		t.Fatalf("first lifecycle delete=%+v", firstDelete)
	}
	secondDelete := provider.invocations[2]
	if secondDelete.Parameters["LifecyclePolicyName"] != "policy-b" ||
		secondDelete.Parameters["FileSystemId"] != "31a8e4a" ||
		secondDelete.IdempotencyKey != "step-nas-a:delete-lifecycle-policy:policy-b" {
		t.Fatalf("second lifecycle delete=%+v", secondDelete)
	}
}

func TestExtremeNASFileSystemDeleteSkipsUnsupportedLifecyclePolicyAPI(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{RequestID: "delete-file-system"}}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-qingdao", alicloud.NASFileSystemNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "extreme-file-system",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.NASFileSystemNativeType,
				NativeID: "extreme-0649d14b",
			},
			Normalized: map[string]any{"configuration": map[string]any{"FileSystemType": "extreme"}},
		},
		Action: "delete", IdempotencyKey: "step-extreme-nas",
	})
	if err != nil || result.ProviderRequestID != "delete-file-system" ||
		len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.NAS.DeleteFileSystem" {
		t.Fatalf("result=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
}

func TestNASManagedNetworkInterfaceDirectDeleteIsSkipped(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-qingdao", "ACS::ECS::NetworkInterface",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "nas-eni",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::NetworkInterface",
				NativeID: "eni-a",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"Type": "Secondary", "Description": "created by NAS",
				"NetworkInterfaceName": "extreme-0649d14b-svr-1-account-dat",
			}},
		},
		Action: "delete", IdempotencyKey: "step-nas-eni",
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Code != "CleanupUnsupported.NASManagedNetworkInterface" ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		len(provider.invocations) != 0 {
		t.Fatalf("providerError=%+v invocations=%+v err=%v", providerError, provider.invocations, err)
	}
}

func TestARMSEnvironmentDeleteWaitsForFeaturesBeforeDeletingEnvironment(t *testing.T) {
	t.Parallel()

	featureList := func(metricStatus, logsStatus string) contracts.InvocationResult {
		return contracts.InvocationResult{
			RequestID: "list-features",
			Data: map[string]any{"Code": 200, "Success": true, "Data": []any{
				map[string]any{
					"EnvironmentId": "env-a", "Name": "metric-agent", "Status": metricStatus,
				},
				map[string]any{
					"EnvironmentId": "env-a", "Name": "logging", "Status": logsStatus,
				},
				map[string]any{
					"EnvironmentId": "env-a", "Name": "unused", "Status": "UnInstall",
				},
			}},
		}
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		featureList("Success", "UnInstalling"),
		{RequestID: "delete-metric", Data: map[string]any{"Code": 200, "Success": true}},
		featureList("UnInstalling", "UnInstalling"),
		featureList("UnInstall", "UnInstall"),
		{RequestID: "delete-environment", Data: map[string]any{"Code": 200, "Success": true}},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "us-east-1", alicloud.ARMSEnvironmentNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "environment-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.ARMSEnvironmentNativeType,
				NativeID: "env-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-arms-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete-metric" ||
		result.RetryAfter != 5*time.Second || result.Data["phase"] != "feature_uninstall" {
		t.Fatalf("execute ARMS environment=%+v err=%v", result, err)
	}
	firstWait, err := hook.Wait(context.Background(), request, result)
	if err != nil || firstWait.Done || firstWait.State != "features_uninstalling" ||
		firstWait.RetryAfter != 5*time.Second {
		t.Fatalf("first ARMS environment wait=%+v err=%v", firstWait, err)
	}
	secondWait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !secondWait.Done || secondWait.State != "delete_requested" ||
		secondWait.Data["environment_delete_request_id"] != "delete-environment" {
		t.Fatalf("second ARMS environment wait=%+v err=%v", secondWait, err)
	}
	if len(provider.invocations) != 5 {
		t.Fatalf("ARMS invocations=%+v", provider.invocations)
	}
	wantOperations := []string{
		"AlibabaCloud.ARMS.ListEnvironmentFeatures",
		"AlibabaCloud.ARMS.DeleteEnvironmentFeature",
		"AlibabaCloud.ARMS.ListEnvironmentFeatures",
		"AlibabaCloud.ARMS.ListEnvironmentFeatures",
		"AlibabaCloud.ARMS.DeleteEnvironment",
	}
	for index, operation := range wantOperations {
		if provider.invocations[index].Operation != operation ||
			provider.invocations[index].Parameters["RegionId"] != "us-east-1" ||
			provider.invocations[index].Parameters["EnvironmentId"] != "env-a" {
			t.Errorf("ARMS invocation %d=%+v", index, provider.invocations[index])
		}
	}
	featureDelete := provider.invocations[1]
	if featureDelete.Parameters["FeatureName"] != "metric-agent" ||
		featureDelete.IdempotencyKey != "step-arms-a:delete-feature:metric-agent" {
		t.Fatalf("ARMS feature delete=%+v", featureDelete)
	}
}

func TestARMSEnvironmentFeatureUninstallFailureStopsEnvironmentDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		Data: map[string]any{"Code": 200, "Data": []any{
			map[string]any{
				"EnvironmentId": "env-a", "Name": "metric-agent", "Status": "UnInstallFailed",
			},
		}},
	}}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "us-east-1", alicloud.ARMSEnvironmentNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ARMSEnvironmentNativeType,
			NativeID: "env-a",
		}},
		Action: "delete", IdempotencyKey: "step-arms-a",
	}
	_, err = hook.Wait(context.Background(), request, contracts.ActionResult{
		Data: map[string]any{"phase": "feature_uninstall"},
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) || providerError.Provider.Code != "ARMSFeatureUninstallFailed" {
		t.Fatalf("ARMS failed feature error=%v", err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.ARMS.ListEnvironmentFeatures" {
		t.Fatalf("ARMS failure invocations=%+v", provider.invocations)
	}
}

func TestSlowAsyncDeletesUseResourceSpecificTimeouts(t *testing.T) {
	t.Parallel()

	for _, nativeType := range []string{
		alicloud.NASFileSystemNativeType,
		alicloud.CENTransitRouterNativeType,
		alicloud.CENTransitRouterPeerAttachmentNativeType,
		alicloud.ARMSEnvironmentNativeType,
		"ACS::ESS::ScalingGroup",
		"ACS::NLB::LoadBalancer",
		"ACS::PrivateLink::VpcEndpointService",
	} {
		hook, err := alicloud.NewActionHook(
			&invocationProvider{},
			"connection-a",
			"cn-hangzhou",
			nativeType,
		)
		if err != nil {
			t.Fatalf("create %s action hook: %v", nativeType, err)
		}
		if timeout := hook.DeletionCheckTimeout(); timeout != 10*time.Minute {
			t.Errorf("%s deletion check timeout = %s, want 10m", nativeType, timeout)
		}
	}
	imageHook, err := alicloud.NewActionHook(
		&invocationProvider{},
		"connection-a",
		"cn-hangzhou",
		alicloud.ECSImageNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	if timeout := imageHook.DeletionCheckTimeout(); timeout != 2*time.Minute {
		t.Errorf("image visibility/delete timeout = %s, want 2m", timeout)
	}
}

func TestCENPeerAttachmentDeleteForcesRelatedDependencyCleanup(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "delete-peer-attachment"},
		{
			RequestID: "read-peer-attachment",
			Data:      map[string]any{"TransitRouterAttachments": []any{}},
		},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-beijing",
		"ACS::CEN::TransitRouterPeerAttachment",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "peer-attachment-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::CEN::TransitRouterPeerAttachment",
				NativeID:   "tr-attach-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-cen-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 2 ||
		provider.invocations[0].Operation != "AlibabaCloud.CEN.DeleteTransitRouterPeerAttachment" ||
		provider.invocations[0].Parameters["TransitRouterAttachmentId"] != "tr-attach-a" ||
		provider.invocations[0].Parameters["Force"] != true {
		t.Fatalf("delete invocation=%+v", provider.invocations)
	}
	if len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "AlibabaCloud.CEN.ListTransitRouterPeerAttachments" ||
		provider.invocations[1].Parameters["TransitRouterAttachmentId"] != "tr-attach-a" ||
		provider.invocations[1].Parameters["MaxResults"] != 20 {
		t.Fatalf("readback invocation=%+v", provider.invocations)
	}
}

func TestResourceActionUsesPrometheusUninstallForCloudProductInstance(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "019FD4D8-44E2-54C0-8A46-07972EAC1A2A",
		Data:      map[string]any{"Data": "success", "Code": 200},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-huhehaote",
		alicloud.PrometheusNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "asset-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.PrometheusNativeType,
				NativeID:   "77d9b5cc04cbccd4",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"ClusterType": "cloud-product-prometheus",
			}},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || result.ProviderRequestID != "019FD4D8-44E2-54C0-8A46-07972EAC1A2A" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.UninstallPromCluster" ||
		provider.invocations[0].Parameters["RegionId"] != "cn-huhehaote" ||
		provider.invocations[0].Parameters["ClusterId"] != "77d9b5cc04cbccd4" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestResourceActionSkipsServiceManagedKMSKeyBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{RequestID: "must-not-be-used"}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-huhehaote",
		alicloud.KMSKeyNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "asset-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.KMSKeyNativeType,
				NativeID:   "key-a",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"Creator": "Ecs",
			}},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Code != "CleanupUnsupported.ServiceManagedKMSKey" ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProductUnsupported) {
		t.Fatalf("provider error=%+v err=%v", providerError, err)
	}
	if len(provider.invocations) != 0 {
		t.Fatalf("delete was invoked: %+v", provider.invocations)
	}
}

func TestResourceActionSkipsPrimaryNetworkInterfaceBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{RequestID: "must-not-be-used"}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ECS::NetworkInterface",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "primary-eni",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ECS::NetworkInterface",
				NativeID:   "eni-primary",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"Type": "Primary",
			}},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Code != "CleanupUnsupported.PrimaryNetworkInterface" ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProductUnsupported) {
		t.Fatalf("provider error=%+v err=%v", providerError, err)
	}
	if len(provider.invocations) != 0 {
		t.Fatalf("delete was invoked: %+v", provider.invocations)
	}
}

func TestResourceActionSkipsBackupPlanBoundVaultBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{RequestID: "must-not-be-used"}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"ap-northeast-1",
		"ACS::HBR::Vault",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "bound-vault",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::HBR::Vault",
				NativeID:   "v-bound",
			},
			Normalized: map[string]any{"configuration": map[string]any{
				"BackupPlanStatistics": []any{map[string]any{"LocalFile": 1}},
			}},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Code != "CleanupUnsupported.BackupPlanBoundVault" ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProductUnsupported) {
		t.Fatalf("provider error=%+v err=%v", providerError, err)
	}
	if len(provider.invocations) != 0 {
		t.Fatalf("delete was invoked: %+v", provider.invocations)
	}
}

func TestDataWorksResourceGroupActionReportsDeletedResponseAsNotFound(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "019FD70A-5046-852F-9724-36C106E6F34F",
		Data: map[string]any{
			"Code":    "704203",
			"Message": "资源组订单释放失败: now resource group status is DELETED, not NORMAL",
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"us-west-1",
		alicloud.DataWorksResourceGroupNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "resource-group",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.DataWorksResourceGroupNativeType,
				NativeID:   "Serverless_res_group_210724245979777_716775571456864",
			},
		},
		Action: "delete", IdempotencyKey: "step-dataworks",
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorNotFound ||
		providerError.Provider.Code != "704203" ||
		providerError.Provider.RequestID != "019FD70A-5046-852F-9724-36C106E6F34F" ||
		providerError.Provider.Summary["operation"] != "AlibabaCloud.DataWorks.DeleteResourceGroup" {
		t.Fatalf("provider error=%+v err=%v", providerError, err)
	}
	if len(provider.invocations) != 1 {
		t.Fatalf("delete invocations=%+v", provider.invocations)
	}
}

func TestDataWorksProjectActionDoesNotRepeatDeleteWhileDeletionIsInProgress(t *testing.T) {
	t.Parallel()

	deletingProject := contracts.InvocationResult{
		RequestID: "get-project-request",
		Data: map[string]any{"Project": map[string]any{
			"Name": "workspace-a", "Status": "Deleting",
		}},
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		deletingProject,
		deletingProject,
		deletingProject,
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"ap-northeast-2",
		alicloud.DataWorksProjectNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), dataWorksProjectActionRequest())
	if err != nil ||
		result.RetryAfter != 2*time.Second ||
		result.Data["phase"] != "deletion_in_progress" ||
		len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.DataWorks.GetProject" {
		t.Fatalf("result=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
	wait, err := hook.Wait(context.Background(), dataWorksProjectActionRequest(), result)
	if err != nil ||
		!wait.Done ||
		wait.State != "Deleting" ||
		len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "AlibabaCloud.DataWorks.GetProject" {
		t.Fatalf("wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	readback, err := hook.Readback(context.Background(), dataWorksProjectActionRequest())
	if err != nil ||
		readback.Exists ||
		readback.State != "Deleting" ||
		len(provider.invocations) != 3 ||
		provider.invocations[2].Operation != "AlibabaCloud.DataWorks.GetProject" {
		t.Fatalf("readback=%+v invocations=%+v err=%v", readback, provider.invocations, err)
	}
}

func TestDataWorksProjectActionTreatsAlreadyDeletingResponseAsAccepted(t *testing.T) {
	t.Parallel()

	alreadyDeleting := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category:  execution.ErrorInvalidRequest,
		Code:      "1101080161",
		Message:   "项目119正在删除中，不可重复删除,请等待完成 | null",
		RequestID: "delete-project-request",
	}}
	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{
				RequestID: "get-project-request",
				Data: map[string]any{"Project": map[string]any{
					"Name": "workspace-a", "Status": "Available",
				}},
			},
			{},
		},
		errors: []error{nil, alreadyDeleting},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"ap-northeast-2",
		alicloud.DataWorksProjectNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), dataWorksProjectActionRequest())
	if err != nil ||
		result.RetryAfter != 2*time.Second ||
		result.Data["phase"] != "deletion_in_progress" ||
		result.ProviderRequestID != "delete-project-request" ||
		len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "AlibabaCloud.DataWorks.DeleteProject" {
		t.Fatalf("result=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
}

func dataWorksProjectActionRequest() contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset: asset.Asset{
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.DataWorksProjectNativeType,
				NativeID:   "workspace-a",
			},
			Normalized: map[string]any{"configuration": map[string]any{"ProjectId": 119}},
		},
		Action: "delete", IdempotencyKey: "step-dataworks-project",
	}
}

func TestSLSProjectActionDisablesDeletionProtectionBeforeForceDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "get-project-request",
			Data: map[string]any{
				"projectName":        "protected-project",
				"description":        "Managed by CMS Workspace",
				"deletionProtection": true,
				"recycleBinEnabled":  true,
			},
		},
		{RequestID: "update-project-request"},
		{RequestID: "delete-project-request"},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"us-east-1",
		alicloud.SLSProjectNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "protected-project",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.SLSProjectNativeType,
				NativeID:   "protected-project",
			},
		},
		Action: "delete", IdempotencyKey: "step-sls",
	})
	if err != nil ||
		result.ProviderRequestID != "delete-project-request" ||
		result.RetryAfter != 2*time.Second ||
		result.Data["deletion_protection_disabled"] != true {
		t.Fatalf("delete result=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 3 {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	get := provider.invocations[0]
	if get.Operation != "AlibabaCloud.SLS.GetProject" ||
		get.Parameters["project"] != "protected-project" {
		t.Fatalf("GetProject invocation=%+v", get)
	}
	update := provider.invocations[1]
	if update.Operation != "AlibabaCloud.SLS.UpdateProject" ||
		update.Parameters["project"] != "protected-project" ||
		update.Parameters["description"] != "Managed by CMS Workspace" ||
		update.Parameters["deletionProtection"] != false ||
		update.IdempotencyKey != "step-sls:disable-deletion-protection" {
		t.Fatalf("UpdateProject invocation=%+v", update)
	}
	deleteCall := provider.invocations[2]
	if deleteCall.Operation != "AlibabaCloud.SLS.DeleteProject" ||
		deleteCall.Parameters["project"] != "protected-project" ||
		deleteCall.Parameters["forceDelete"] != true ||
		deleteCall.IdempotencyKey != "step-sls" {
		t.Fatalf("DeleteProject invocation=%+v", deleteCall)
	}
}

func TestSLSProjectActionSkipsProtectionUpdateWhenAlreadyDisabled(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "get-project-request",
			Data: map[string]any{
				"projectName":        "unprotected-project",
				"description":        "",
				"deletionProtection": false,
			},
		},
		{RequestID: "delete-project-request"},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"us-east-1",
		alicloud.SLSProjectNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "unprotected-project",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.SLSProjectNativeType,
				NativeID:   "unprotected-project",
			},
		},
		Action: "delete", IdempotencyKey: "step-sls",
	})
	if err != nil || result.ProviderRequestID != "delete-project-request" {
		t.Fatalf("delete result=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "AlibabaCloud.SLS.DeleteProject" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestSpecActionsConditionallyDisableDeletionProtectionBeforeDelete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		nativeType       string
		nativeID         string
		readOperation    string
		disableOperation string
		readData         map[string]any
		wantParameters   map[string]any
	}{
		{
			name: "ROS stack", nativeType: alicloud.ROSStackNativeType,
			nativeID: "stack-a", readOperation: "AlibabaCloud.ROS.GetStack",
			disableOperation: "AlibabaCloud.ROS.SetDeletionProtection",
			readData: map[string]any{
				"StackId": "stack-a", "Status": "CREATE_COMPLETE",
				"DeletionProtection": "Enabled",
			},
			wantParameters: map[string]any{"DeletionProtection": "Disabled"},
		},
		{
			name: "ALB", nativeType: "ACS::ALB::LoadBalancer",
			nativeID: "alb-a", readOperation: "AlibabaCloud.ALB.ListLoadBalancers",
			disableOperation: "AlibabaCloud.ALB.DisableDeletionProtection",
			readData: map[string]any{"LoadBalancers": []any{map[string]any{
				"LoadBalancerId": "alb-a", "LoadBalancerStatus": "Active",
				"DeletionProtectionConfig": map[string]any{"Enabled": true},
			}}},
			wantParameters: map[string]any{"ResourceId": "alb-a"},
		},
		{
			name: "NLB", nativeType: "ACS::NLB::LoadBalancer",
			nativeID: "nlb-a", readOperation: "AlibabaCloud.NLB.GetLoadBalancerAttribute",
			disableOperation: "AlibabaCloud.NLB.UpdateLoadBalancerProtection",
			readData: map[string]any{
				"LoadBalancerId": "nlb-a", "LoadBalancerStatus": "Active",
				"DeletionProtectionConfig": map[string]any{"Enabled": true},
			},
			wantParameters: map[string]any{"DeletionProtectionEnabled": false},
		},
		{
			name: "CLB", nativeType: "ACS::SLB::LoadBalancer",
			nativeID: "lb-a", readOperation: "AlibabaCloud.SLB.DescribeLoadBalancerAttribute",
			disableOperation: "AlibabaCloud.SLB.SetLoadBalancerDeleteProtection",
			readData: map[string]any{
				"LoadBalancerId": "lb-a", "LoadBalancerStatus": "active",
				"DeleteProtection": "on",
			},
			wantParameters: map[string]any{"DeleteProtection": "off"},
		},
		{
			name: "ECS", nativeType: "ACS::ECS::Instance",
			nativeID: "i-a", readOperation: "AlibabaCloud.DescribeInstances",
			disableOperation: "AlibabaCloud.ModifyInstanceAttribute",
			readData: map[string]any{"Instances": map[string]any{"Instance": []any{
				map[string]any{
					"InstanceId": "i-a", "Status": "Stopped",
					"DeletionProtection": true,
				},
			}}},
			wantParameters: map[string]any{"DeletionProtection": false},
		},
		{
			name: "ESS", nativeType: "ACS::ESS::ScalingGroup",
			nativeID: "asg-a", readOperation: "AlibabaCloud.ESS.DescribeScalingGroups",
			disableOperation: "AlibabaCloud.ESS.SetGroupDeletionProtection",
			readData: map[string]any{"ScalingGroups": map[string]any{"ScalingGroup": []any{
				map[string]any{
					"ScalingGroupId": "asg-a", "LifecycleState": "Active",
					"GroupDeletionProtection": true,
				},
			}}},
			wantParameters: map[string]any{"GroupDeletionProtection": false},
		},
		{
			name: "EIP", nativeType: "ACS::EIP::EipAddress",
			nativeID: "eip-a", readOperation: "DescribeEipAddresses",
			disableOperation: "AlibabaCloud.VPC.DeletionProtection",
			readData: map[string]any{"EipAddresses": map[string]any{"EipAddress": []any{
				map[string]any{
					"AllocationId": "eip-a", "Status": "Available",
					"DeletionProtection": true,
				},
			}}},
			wantParameters: map[string]any{"Type": "EIP", "ProtectionEnable": false},
		},
		{
			name: "shared bandwidth", nativeType: "ACS::CBWP::CommonBandwidthPackage",
			nativeID: "cbwp-a", readOperation: "DescribeCommonBandwidthPackages",
			disableOperation: "AlibabaCloud.VPC.DeletionProtection",
			readData: map[string]any{
				"CommonBandwidthPackages": map[string]any{
					"CommonBandwidthPackage": []any{map[string]any{
						"BandwidthPackageId": "cbwp-a", "Status": "Available",
						"DeletionProtection": true,
					}},
				},
			},
			wantParameters: map[string]any{"Type": "CBWP", "ProtectionEnable": false},
		},
		{
			name: "HBase", nativeType: "ACS::HBase::Cluster",
			nativeID: "hb-a", readOperation: "AlibabaCloud.HBase.DescribeInstance",
			disableOperation: "AlibabaCloud.HBase.ModifyClusterDeletionProtection",
			readData: map[string]any{
				"InstanceId": "hb-a", "Status": "ACTIVATION",
				"IsDeletionProtection": true,
			},
			wantParameters: map[string]any{"Protection": false},
		},
		{
			name: "RDS", nativeType: "ACS::RDS::DBInstance",
			nativeID: "rm-a", readOperation: "AlibabaCloud.RDS.DescribeDBInstances",
			disableOperation: "AlibabaCloud.RDS.ModifyDBInstanceDeletionProtection",
			readData: map[string]any{"Items": map[string]any{"DBInstance": []any{
				map[string]any{
					"DBInstanceId": "rm-a", "DBInstanceStatus": "Running",
					"DeletionProtection": true,
				},
			}}},
			wantParameters: map[string]any{"DeletionProtection": false},
		},
		{
			name: "PolarDB", nativeType: "ACS::PolarDB::DBCluster",
			nativeID: "pc-a", readOperation: "AlibabaCloud.PolarDB.DescribeDBClusters",
			disableOperation: "AlibabaCloud.PolarDB.ModifyDBClusterDeletion",
			readData: map[string]any{"Items": map[string]any{"DBCluster": []any{
				map[string]any{
					"DBClusterId": "pc-a", "DBClusterStatus": "Running",
					"DeletionLock": float64(1),
				},
			}}},
			wantParameters: map[string]any{"Protection": false},
		},
		{
			name: "Redis", nativeType: "ACS::Redis::DBInstance",
			nativeID: "r-a", readOperation: "AlibabaCloud.Redis.DescribeInstanceAttribute",
			disableOperation: "AlibabaCloud.Redis.ModifyInstanceAttribute",
			readData: map[string]any{"Instances": map[string]any{
				"DBInstanceAttribute": []any{map[string]any{
					"InstanceId": "r-a", "InstanceStatus": "Normal",
					"InstanceReleaseProtection": true,
				}},
			}},
			wantParameters: map[string]any{"InstanceReleaseProtection": false},
		},
		{
			name: "MongoDB", nativeType: "ACS::MongoDB::DBInstance",
			nativeID: "dds-a", readOperation: "AlibabaCloud.MongoDB.DescribeDBInstanceAttribute",
			disableOperation: "AlibabaCloud.MongoDB.ModifyDBInstanceAttribute",
			readData: map[string]any{"DBInstances": map[string]any{
				"DBInstanceAttribute": []any{map[string]any{
					"DBInstanceId": "dds-a", "DBInstanceStatus": "Running",
					"DBInstanceReleaseProtection": true,
				}},
			}},
			wantParameters: map[string]any{"DBInstanceReleaseProtection": false},
		},
		{
			name: "Lindorm", nativeType: "ACS::Lindorm::Instance",
			nativeID: "ld-a", readOperation: "AlibabaCloud.Lindorm.GetLindormInstance",
			disableOperation: "AlibabaCloud.Lindorm.UpdateLindormInstanceAttribute",
			readData: map[string]any{
				"InstanceId": "ld-a", "InstanceStatus": "ACTIVATION",
				"DeletionProtection": "true",
			},
			wantParameters: map[string]any{"DeletionProtection": false},
		},
		{
			name: "KMS", nativeType: alicloud.KMSKeyNativeType,
			nativeID: "key-a", readOperation: "AlibabaCloud.KMS.DescribeKey",
			disableOperation: "AlibabaCloud.KMS.SetDeletionProtection",
			readData: map[string]any{"KeyMetadata": map[string]any{
				"KeyId": "key-a", "KeyState": "Enabled",
				"DeletionProtection": "Enabled",
			}},
			wantParameters: map[string]any{"EnableDeletionProtection": false},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loadBalancer := test.nativeType == "ACS::ALB::LoadBalancer" ||
				test.nativeType == "ACS::NLB::LoadBalancer" ||
				test.nativeType == "ACS::SLB::LoadBalancer"

			t.Run("enabled", func(t *testing.T) {
				results := []contracts.InvocationResult{
					{RequestID: "read-protection", Data: test.readData},
					{RequestID: "disable-protection"},
				}
				if loadBalancer {
					results = append(results, contracts.InvocationResult{Data: map[string]any{"Services": []any{}}})
				}
				results = append(results, contracts.InvocationResult{RequestID: "delete-request"})
				provider := &invocationProvider{results: results}
				hook, err := alicloud.NewActionHook(
					provider,
					"connection-a",
					"cn-hangzhou",
					test.nativeType,
				)
				if err != nil {
					t.Fatal(err)
				}
				result, err := hook.Execute(context.Background(), contracts.ActionRequest{
					Asset: asset.Asset{
						ID: asset.AssetID(test.nativeID),
						Identity: asset.Identity{
							Provider: asset.ProviderAliCloud, NativeType: test.nativeType,
							NativeID: test.nativeID,
						},
					},
					Action: "delete", IdempotencyKey: "step-protected",
				})
				if err != nil {
					t.Fatal(err)
				}
				wantInvocations := 3
				if loadBalancer {
					wantInvocations = 4
				}
				if result.Data["deletion_protection_disabled"] != true ||
					len(provider.invocations) != wantInvocations {
					t.Fatalf("result=%+v invocations=%+v", result, provider.invocations)
				}
				if provider.invocations[0].Operation != test.readOperation ||
					provider.invocations[1].Operation != test.disableOperation ||
					provider.invocations[1].IdempotencyKey !=
						"step-protected:disable-deletion-protection" {
					t.Fatalf("invocations=%+v", provider.invocations)
				}
				for name, value := range test.wantParameters {
					if provider.invocations[1].Parameters[name] != value {
						t.Fatalf(
							"disable parameter %s=%#v, want %#v",
							name,
							provider.invocations[1].Parameters[name],
							value,
						)
					}
				}
			})

			t.Run("already disabled", func(t *testing.T) {
				results := []contracts.InvocationResult{
					{
						RequestID: "read-protection",
						Data:      disabledDeletionProtectionData(t, test.readData),
					},
				}
				if loadBalancer {
					results = append(results, contracts.InvocationResult{Data: map[string]any{"Services": []any{}}})
				}
				results = append(results, contracts.InvocationResult{RequestID: "delete-request"})
				provider := &invocationProvider{results: results}
				hook, err := alicloud.NewActionHook(
					provider,
					"connection-a",
					"cn-hangzhou",
					test.nativeType,
				)
				if err != nil {
					t.Fatal(err)
				}
				result, err := hook.Execute(context.Background(), contracts.ActionRequest{
					Asset: asset.Asset{
						ID: asset.AssetID(test.nativeID),
						Identity: asset.Identity{
							Provider: asset.ProviderAliCloud, NativeType: test.nativeType,
							NativeID: test.nativeID,
						},
					},
					Action: "delete", IdempotencyKey: "step-unprotected",
				})
				if err != nil {
					t.Fatal(err)
				}
				wantInvocations := 2
				if loadBalancer {
					wantInvocations = 3
				}
				if result.Data["deletion_protection_disabled"] != nil ||
					len(provider.invocations) != wantInvocations ||
					provider.invocations[0].Operation != test.readOperation ||
					provider.invocations[1].Operation == test.disableOperation {
					t.Fatalf("result=%+v invocations=%+v", result, provider.invocations)
				}
			})
		})
	}
}

func TestKMSKeyActionCapturesScheduledDeletionTimeFromReadback(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "protection-request",
			Data: map[string]any{
				"KeyMetadata": map[string]any{
					"KeyId":              "key-a",
					"DeletionProtection": "Disabled",
				},
			},
		},
		{
			RequestID: "schedule-request",
			Data:      map[string]any{"RequestId": "schedule-request"},
		},
		{
			RequestID: "scheduled-readback-request",
			Data: map[string]any{"KeyMetadata": map[string]any{
				"KeyId":              "key-a",
				"KeyState":           "PendingDeletion",
				"DeletionProtection": "Disabled",
				"DeleteDate":         "2026-08-13T11:54:34Z",
			}},
		},
	}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		alicloud.KMSKeyNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "asset-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.KMSKeyNativeType,
				NativeID:   "key-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "schedule-request" || result.RetryAfter != 0 {
		t.Fatalf("schedule result=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != alicloud.KMSDeletionScheduledState {
		t.Fatalf("schedule wait=%+v err=%v", wait, err)
	}
	readback, err := hook.Readback(context.Background(), request)
	if err != nil || readback.Exists || readback.State != alicloud.KMSDeletionScheduledState ||
		readback.Data["pending_window_days"] != 7 ||
		readback.Data["scheduled_deletion_at"] != "2026-08-13T11:54:34Z" {
		t.Fatalf("schedule readback=%+v err=%v", readback, err)
	}
	if len(provider.invocations) != 3 ||
		provider.invocations[0].Operation != "AlibabaCloud.KMS.DescribeKey" ||
		provider.invocations[1].Operation != "AlibabaCloud.KMS.ScheduleKeyDeletion" ||
		provider.invocations[2].Operation != "AlibabaCloud.KMS.DescribeKey" {
		t.Fatalf("KMS invocations=%+v", provider.invocations)
	}
}

func TestKMSKeyActionTreatsPendingDeletionAsAlreadyScheduled(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "describe-request",
		Data: map[string]any{"KeyMetadata": map[string]any{
			"KeyId":              "key-a",
			"KeyState":           "PendingDeletion",
			"DeletionProtection": "Disabled",
			"DeleteDate":         "2026-08-13T11:54:34Z",
		}},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		alicloud.KMSKeyNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "asset-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.KMSKeyNativeType,
				NativeID:   "key-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil ||
		result.ProviderRequestID != "describe-request" ||
		result.RetryAfter != 0 ||
		result.Data["phase"] != "deletion_in_progress" ||
		result.Data["state"] != "PendingDeletion" ||
		result.Data["scheduled_deletion_at"] != "2026-08-13T11:54:34Z" ||
		len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.KMS.DescribeKey" {
		t.Fatalf("pending deletion result=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
}

func TestGraphDatabaseActionUsesProductReadDeleteAndReadback(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{
				RequestID: "preflight-request",
				Data: map[string]any{"Items": map[string]any{
					"DBInstance": []any{map[string]any{
						"DBInstanceId": "gdb-a", "DBInstanceStatus": "Running",
					}},
				}},
			},
			{RequestID: "delete-request"},
			{},
		},
		errors: []error{
			nil,
			nil,
			&contracts.ProviderCallError{Provider: execution.ProviderError{
				Category: execution.ErrorNotFound,
				Code:     "InvalidDBInstanceId.NotFound",
			}},
		},
	}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::GraphDatabase::DbInstance",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "gdb-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::GraphDatabase::DbInstance",
				NativeID:   "gdb-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete-request" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 3 ||
		provider.invocations[0].Operation !=
			"AlibabaCloud.GDB.DescribeDBInstanceAttribute" ||
		provider.invocations[1].Operation !=
			"AlibabaCloud.GDB.DeleteDBInstance" ||
		provider.invocations[2].Operation !=
			"AlibabaCloud.GDB.DescribeDBInstanceAttribute" ||
		provider.invocations[1].Parameters["DBInstanceId"] != "gdb-a" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestAliKafkaActionReleasesPostpaidInstanceThenDeletesReleasedRecord(t *testing.T) {
	t.Parallel()

	running := map[string]any{"InstanceList": map[string]any{"InstanceVO": []any{
		map[string]any{
			"InstanceId": "alikafka-a", "PaidType": 1,
			"ViewInstanceStatusCode": 2,
		},
	}}}
	released := map[string]any{"InstanceList": map[string]any{"InstanceVO": []any{
		map[string]any{
			"InstanceId": "alikafka-a", "PaidType": 1,
			"ViewInstanceStatusCode": 6,
		},
	}}}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "read-running", Data: running},
		{RequestID: "release-request", Data: map[string]any{"Success": true}},
		{RequestID: "read-released", Data: released},
		{RequestID: "delete-request", Data: map[string]any{"Success": true}},
	}}
	hook, err := alicloud.NewAliKafkaHook(
		provider,
		"connection-a",
		"cn-hangzhou",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "asset-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.AliKafkaInstanceNativeType,
				NativeID:   "alikafka-a",
			},
		},
		Action: "delete", Parameters: map[string]any{"ForceDeleteInstance": false},
		IdempotencyKey: "step-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "release-request" ||
		result.Data["phase"] != "release" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[1].Operation != "AlibabaCloud.AliKafka.ReleaseInstance" ||
		provider.invocations[1].Parameters["ForceDeleteInstance"] != false ||
		provider.invocations[3].Operation != "AlibabaCloud.AliKafka.DeleteInstance" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestAliKafkaActionBlocksPrepaidInstanceBeforeProviderMutation(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-prepaid",
		Data: map[string]any{"InstanceList": map[string]any{"InstanceVO": []any{
			map[string]any{
				"InstanceId": "alikafka-a", "PaidType": 0,
				"ViewInstanceStatusCode": 2,
			},
		}}},
	}}}
	hook, err := alicloud.NewAliKafkaHook(
		provider,
		"connection-a",
		"cn-hangzhou",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.AliKafkaInstanceNativeType,
				NativeID:   "alikafka-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || preflight.Allowed ||
		preflight.Evidence["paid_type"] != 0 {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.AliKafka.GetInstanceList" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestSpecActionReadbackRequiresMatchingResourceIdentity(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		Data: map[string]any{
			"Instances": map[string]any{"Instance": []any{
				map[string]any{"InstanceId": "i-other", "Status": "Running"},
			}},
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ECS::Instance",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "instance-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ECS::Instance",
				NativeID:   "i-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err == nil || preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
}

func TestSpecActionReadbackResolvesBatchIdentityExpression(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		Data: map[string]any{
			"LoadBalancers": []any{
				map[string]any{
					"LoadBalancerId":     "alb-a",
					"LoadBalancerStatus": "Active",
				},
			},
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ALB::LoadBalancer",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "load-balancer-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ALB::LoadBalancer",
				NativeID:   "alb-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	ids, ok := provider.invocations[0].Parameters["LoadBalancerIds"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "alb-a" {
		t.Fatalf("LoadBalancerIds=%#v", provider.invocations[0].Parameters["LoadBalancerIds"])
	}
}

func TestNatIPDeletionReadbackFiltersByNativeID(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "nat-ip-absent",
		Data:      map[string]any{"NatIps": []any{}},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-chengdu",
		"ACS::NAT::NatIp",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "nat-ip-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::NAT::NatIp",
				NativeID:   "vpcnatip-2vc3wxed1s22ll8rt0061",
			},
			Normalized: map[string]any{"natGatewayId": "ngw-2vchrgrjdgphm6w20jqrb"},
		},
		Action: "delete", IdempotencyKey: "step-nat-ip-a",
	}

	wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{})
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("NAT IP deletion readback wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 1 {
		t.Fatalf("NAT IP readback invocations=%+v", provider.invocations)
	}
	invocation := provider.invocations[0]
	ids, ok := invocation.Parameters["NatIpIds"].([]string)
	if invocation.Operation != "ListNatIps" ||
		invocation.Parameters["NatGatewayId"] != "ngw-2vchrgrjdgphm6w20jqrb" ||
		invocation.Parameters["MaxResults"] != 1 ||
		!ok || len(ids) != 1 || ids[0] != "vpcnatip-2vc3wxed1s22ll8rt0061" {
		t.Fatalf("NAT IP readback invocation=%+v", invocation)
	}
}

func TestSpecActionAllowsOnlyDeclaredDeleteOptions(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{RequestID: "delete-request"}}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::ECS::Snapshot")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "snapshot-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Snapshot", NativeID: "s-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
		Parameters: map[string]any{"Force": true},
	}
	if _, err := hook.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute declared option: %v", err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Parameters["SnapshotId"] != "s-a" ||
		provider.invocations[0].Parameters["Force"] != true {
		t.Fatalf("delete invocation=%+v", provider.invocations)
	}

	request.Parameters["DeleteAll"] = true
	if _, err := hook.Execute(context.Background(), request); err == nil {
		t.Fatal("undeclared delete option must be rejected")
	}
}

func TestImageActionMakesPublicCustomImagePrivateBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "describe-request",
			Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
				"ImageId": "m-image", "Status": "Available", "IsPublic": true,
			}}}},
		},
		{RequestID: "visibility-request"},
		{
			RequestID: "visibility-pending-read",
			Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
				"ImageId": "m-image", "Status": "Available", "IsPublic": true,
			}}}},
		},
		{
			RequestID: "visibility-confirmed-read",
			Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
				"ImageId": "m-image", "Status": "Available", "IsPublic": false,
			}}}},
		},
		{
			RequestID: "share-permission-read",
			Data: map[string]any{
				"ImageId": "m-image", "TotalCount": 0,
				"Accounts": map[string]any{"Account": []any{}},
			},
		},
		{RequestID: "delete-request"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-hangzhou", alicloud.ECSImageNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "image-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.ECSImageNativeType, NativeID: "m-image",
			},
		},
		Action: "delete", IdempotencyKey: "step-image-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.invocations) != 2 ||
		provider.invocations[0].Operation != "DescribeImages" ||
		provider.invocations[1].Operation != "ModifyImageSharePermission" ||
		provider.invocations[1].Parameters["IsPublic"] != false ||
		provider.invocations[1].IdempotencyKey != "step-image-a:make-private" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	if result.ProviderRequestID != "visibility-request" ||
		result.Data["phase"] != "image_visibility_change" ||
		result.Data["image_made_private"] != true ||
		result.Data["visibility_request_id"] != "visibility-request" {
		t.Fatalf("result=%+v", result)
	}

	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "image-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.ECSImageNativeType, NativeID: "m-image",
			},
		},
		Action: "delete", IdempotencyKey: "step-image-a",
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	if wait.Done || wait.State != "image_visibility_change_pending" || len(provider.invocations) != 3 {
		t.Fatalf("pending visibility wait=%+v invocations=%+v", wait, provider.invocations)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil {
		t.Fatal(err)
	}
	if !wait.Done || wait.State != "image_delete_requested" ||
		wait.Data["delete_request_id"] != "delete-request" ||
		len(provider.invocations) != 6 ||
		provider.invocations[3].Operation != "DescribeImages" ||
		provider.invocations[4].Operation != "DescribeImageSharePermission" ||
		provider.invocations[5].Operation != "DeleteImage" ||
		provider.invocations[5].IdempotencyKey != "step-image-a" {
		t.Fatalf("confirmed visibility wait=%+v invocations=%+v", wait, provider.invocations)
	}
}

func TestImageActionRemovesSharedAccountsInBatchesBeforeDelete(t *testing.T) {
	t.Parallel()

	imageRead := func(requestID string) contracts.InvocationResult {
		return contracts.InvocationResult{
			RequestID: requestID,
			Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
				"ImageId": "m-image", "Status": "Available", "IsPublic": false,
			}}}},
		}
	}
	shareRead := func(requestID string, accountIDs ...string) contracts.InvocationResult {
		accounts := make([]any, 0, len(accountIDs))
		for _, accountID := range accountIDs {
			accounts = append(accounts, map[string]any{"AliyunId": accountID})
		}
		return contracts.InvocationResult{
			RequestID: requestID,
			Data: map[string]any{
				"ImageId": "m-image", "TotalCount": len(accounts),
				"Accounts": map[string]any{"Account": accounts},
			},
		}
	}
	sharedAccounts := []string{
		"1631399408622851",
		"2000000000000001", "2000000000000002", "2000000000000003",
		"2000000000000004", "2000000000000005", "2000000000000006",
		"2000000000000007", "2000000000000008", "2000000000000009",
		"2000000000000010",
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		imageRead("execute-image-read"),
		shareRead("execute-share-read", sharedAccounts...),
		{RequestID: "remove-first-batch"},
		imageRead("first-wait-image-read"),
		shareRead("first-wait-share-read", sharedAccounts[10]),
		{RequestID: "remove-second-batch"},
		imageRead("second-wait-image-read"),
		shareRead("second-wait-share-read"),
		{RequestID: "delete-request"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-shanghai", alicloud.ECSImageNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "image-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.ECSImageNativeType,
				NativeID: "m-image",
			},
		},
		Action: "delete", IdempotencyKey: "step-image-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "image_share_permission_change" ||
		result.Data["image_share_accounts_removed"] != 10 ||
		result.Data["image_share_accounts_remaining"] != 1 {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	firstModify := provider.invocations[2]
	if firstModify.Operation != "ModifyImageSharePermission" ||
		firstModify.Parameters["RegionId"] != "cn-shanghai" ||
		firstModify.Parameters["ImageId"] != "m-image" ||
		firstModify.Parameters["RemoveAccount.1"] != "1631399408622851" ||
		firstModify.Parameters["RemoveAccount.10"] != "2000000000000009" ||
		firstModify.Parameters["RemoveAccount.11"] != nil {
		t.Fatalf("first share removal=%+v", firstModify)
	}

	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "image_share_permission_change_pending" ||
		wait.Data["image_share_accounts_removed"] != 1 ||
		provider.invocations[5].Parameters["RemoveAccount.1"] != "2000000000000010" {
		t.Fatalf("first wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "image_delete_requested" ||
		len(provider.invocations) != 9 ||
		provider.invocations[7].Operation != "DescribeImageSharePermission" ||
		provider.invocations[8].Operation != "DeleteImage" {
		t.Fatalf("second wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
}

func TestImageActionDoesNotTreatMissingSharedAccountAsMissingImage(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{
				RequestID: "image-read",
				Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
					"ImageId": "m-image", "Status": "Available", "IsPublic": false,
				}}}},
			},
			{
				RequestID: "share-read",
				Data: map[string]any{
					"ImageId": "m-image", "TotalCount": 1,
					"Accounts": map[string]any{"Account": []any{
						map[string]any{"AliyunId": "1631399408622851"},
					}},
				},
			},
			{},
		},
		errors: []error{nil, nil, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorNotFound,
			Code:     "InvalidAccount.NotFound",
			Message:  "The specified account does not exist.",
		}}},
	}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-shanghai", alicloud.ECSImageNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ECSImageNativeType,
			NativeID: "m-image",
		}},
		Action: "delete", IdempotencyKey: "step-image-a",
	})
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) || providerCall.Provider.Code != "InvalidAccount.NotFound" {
		t.Fatalf("execute error=%v", err)
	}
}

func TestImageActionAcceptsAlreadyPrivateVisibilityResponse(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{
				RequestID: "describe-request",
				Data: map[string]any{"Images": map[string]any{"Image": []any{map[string]any{
					"ImageId": "m-image", "Status": "Available", "IsPublic": true,
				}}}},
			},
			{},
		},
		errors: []error{nil, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorPermissionDenied,
			Code:      "Image.NotPublic",
			Message:   "The specified image is not public image.",
			RequestID: "already-private-request",
		}}},
	}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-hangzhou", alicloud.ECSImageNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "image-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: alicloud.ECSImageNativeType, NativeID: "m-image",
			},
		},
		Action: "delete", IdempotencyKey: "step-image-a",
	})
	if err != nil || result.Data["phase"] != "image_visibility_change" ||
		result.ProviderRequestID != "already-private-request" ||
		len(provider.invocations) != 2 ||
		provider.invocations[1].Operation != "ModifyImageSharePermission" {
		t.Fatalf("already-private result=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
}

func TestPrivateLinkEndpointServiceDisconnectsAndDetachesBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "read-service", Data: map[string]any{"Services": []any{map[string]any{
			"ServiceId": "epsrv-a", "ServiceStatus": "Active",
		}}}},
		{RequestID: "list-connections", Data: map[string]any{"Connections": []any{map[string]any{
			"EndpointId": "ep-a", "ConnectionStatus": "Connected",
		}}}},
		{RequestID: "disable-connection"},
		{RequestID: "list-connections", Data: map[string]any{"Connections": []any{map[string]any{
			"EndpointId": "ep-a", "ConnectionStatus": "Disconnected",
		}}}},
		{RequestID: "list-resources", Data: map[string]any{"Resources": []any{map[string]any{
			"ResourceId": "nlb-a", "ResourceType": "nlb", "ZoneId": "cn-qingdao-b",
		}}}},
		{RequestID: "detach-resource"},
		{RequestID: "list-connections", Data: map[string]any{"Connections": []any{map[string]any{
			"EndpointId": "ep-a", "ConnectionStatus": "Disconnected",
		}}}},
		{RequestID: "list-resources", Data: map[string]any{"Resources": []any{}}},
		{RequestID: "delete-service"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-qingdao", "ACS::PrivateLink::VpcEndpointService",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "endpoint-service-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::PrivateLink::VpcEndpointService",
				NativeID: "epsrv-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-endpoint-service",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "disconnect_endpoint_connections" ||
		provider.invocations[2].Operation != "AlibabaCloud.PrivateLink.DisableVpcEndpointConnection" {
		t.Fatalf("execute=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "detach_service_resources" ||
		provider.invocations[5].Operation != "AlibabaCloud.PrivateLink.DetachResourceFromVpcEndpointService" ||
		provider.invocations[5].Parameters["ZoneId"] != "cn-qingdao-b" {
		t.Fatalf("first wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "endpoint_service_delete_requested" ||
		len(provider.invocations) != 9 ||
		provider.invocations[8].Operation != "AlibabaCloud.PrivateLink.DeleteVpcEndpointService" {
		t.Fatalf("second wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
}

func TestNATGatewayDisconnectsOnlyBoundEndpointZoneBeforeDetachAndForceDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "read-nat", Data: map[string]any{
			"NatGateways": map[string]any{"NatGateway": []any{map[string]any{
				"NatGatewayId": "ngw-a", "Status": "Available", "DeletionProtection": false,
			}}},
		}},
		{RequestID: "list-services", Data: map[string]any{"Services": []any{map[string]any{
			"ServiceId": "epsrv-a", "ServiceStatus": "Active",
		}}}},
		{RequestID: "list-resources", Data: map[string]any{"Resources": []any{
			map[string]any{
				"ResourceId": "ngw-a", "ResourceType": "vpcnat", "ZoneId": "cn-zhangjiakou-a",
			},
			map[string]any{
				"ResourceId": "ngw-b", "ResourceType": "vpcnat", "ZoneId": "cn-zhangjiakou-b",
			},
		}}},
		{RequestID: "list-connections", Data: map[string]any{"Connections": []any{map[string]any{
			"EndpointId": "ep-a", "ConnectionStatus": "Connected", "Zones": []any{
				map[string]any{
					"ZoneId": "cn-zhangjiakou-a", "ResourceId": "ngw-a", "ZoneStatus": "Connected",
				},
				map[string]any{
					"ZoneId": "cn-zhangjiakou-b", "ResourceId": "ngw-b", "ZoneStatus": "Connected",
				},
			},
		}}}},
		{RequestID: "disable-zone-a"},
		{RequestID: "list-services-after-disable", Data: map[string]any{"Services": []any{map[string]any{
			"ServiceId": "epsrv-a", "ServiceStatus": "Active",
		}}}},
		{RequestID: "list-resources-after-disable", Data: map[string]any{"Resources": []any{
			map[string]any{
				"ResourceId": "ngw-a", "ResourceType": "vpcnat", "ZoneId": "cn-zhangjiakou-a",
			},
			map[string]any{
				"ResourceId": "ngw-b", "ResourceType": "vpcnat", "ZoneId": "cn-zhangjiakou-b",
			},
		}}},
		{RequestID: "list-connections-after-disable", Data: map[string]any{"Connections": []any{map[string]any{
			"EndpointId": "ep-a", "ConnectionStatus": "Connected", "Zones": []any{
				map[string]any{
					"ZoneId": "cn-zhangjiakou-a", "ResourceId": "ngw-a", "ZoneStatus": "Disconnected",
				},
				map[string]any{
					"ZoneId": "cn-zhangjiakou-b", "ResourceId": "ngw-b", "ZoneStatus": "Connected",
				},
			},
		}}}},
		{RequestID: "detach-ngw-a"},
		{RequestID: "list-services-after-detach", Data: map[string]any{"Services": []any{map[string]any{
			"ServiceId": "epsrv-a", "ServiceStatus": "Active",
		}}}},
		{RequestID: "list-resources-after-detach", Data: map[string]any{"Resources": []any{map[string]any{
			"ResourceId": "ngw-b", "ResourceType": "vpcnat", "ZoneId": "cn-zhangjiakou-b",
		}}}},
		{RequestID: "force-delete-ngw-a"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-zhangjiakou", "ACS::NAT::NatGateway",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "nat-a",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::NAT::NatGateway", NativeID: "ngw-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-nat-a",
	}

	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "disconnect_bound_endpoint_zone" {
		t.Fatalf("execute=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
	disable := provider.invocations[4]
	if disable.Operation != "AlibabaCloud.PrivateLink.DisableVpcEndpointZoneConnection" ||
		disable.Parameters["ServiceId"] != "epsrv-a" ||
		disable.Parameters["EndpointId"] != "ep-a" ||
		disable.Parameters["ZoneId"] != "cn-zhangjiakou-a" {
		t.Fatalf("disable target NAT endpoint zone invocation=%+v", disable)
	}

	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "detach_bound_service_resource" {
		t.Fatalf("detach wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	detach := provider.invocations[8]
	if detach.Operation != "AlibabaCloud.PrivateLink.DetachResourceFromVpcEndpointService" ||
		detach.Parameters["ResourceId"] != "ngw-a" ||
		detach.Parameters["ResourceType"] != "vpcnat" ||
		detach.Parameters["ZoneId"] != "cn-zhangjiakou-a" {
		t.Fatalf("detach target NAT invocation=%+v", detach)
	}

	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "bound_resource_delete_requested" {
		t.Fatalf("delete wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	deleted := provider.invocations[11]
	if deleted.Operation != "DeleteNatGateway" ||
		deleted.Parameters["NatGatewayId"] != "ngw-a" || deleted.Parameters["Force"] != true {
		t.Fatalf("force delete target NAT invocation=%+v", deleted)
	}
	for _, invocation := range provider.invocations {
		if invocation.Operation == "AlibabaCloud.PrivateLink.DisableVpcEndpointConnection" ||
			invocation.Operation == "AlibabaCloud.PrivateLink.DeleteVpcEndpointService" {
			t.Fatalf("NAT cleanup mutated the whole endpoint connection or service: %+v", invocation)
		}
	}
}

func TestROSStackGroupDeletesStackInstancesSeriallyBeforeGroup(t *testing.T) {
	t.Parallel()

	stackGroup := func(requestID string) contracts.InvocationResult {
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{
			"StackGroup": map[string]any{
				"StackGroupName": "test", "Status": "ACTIVE", "PermissionModel": "SERVICE_MANAGED",
			},
		}}
	}
	stackInstances := func(requestID string, instances ...map[string]any) contracts.InvocationResult {
		items := make([]any, len(instances))
		for index := range instances {
			items[index] = instances[index]
		}
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{
			"StackInstances": items, "TotalCount": len(items),
		}}
	}
	first := map[string]any{
		"StackGroupName": "test", "AccountId": "1001", "RegionId": "cn-beijing", "Status": "CURRENT",
	}
	second := map[string]any{
		"StackGroupName": "test", "AccountId": "1002", "RegionId": "cn-shanghai", "Status": "CURRENT",
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		stackGroup("read-group"),
		stackInstances("list-two", second, first),
		{RequestID: "delete-first", Data: map[string]any{"OperationId": "op-first"}},
		{RequestID: "operation-first-running", Data: map[string]any{
			"StackGroupOperation": map[string]any{"OperationId": "op-first", "Status": "RUNNING"},
		}},
		{RequestID: "operation-first-succeeded", Data: map[string]any{
			"StackGroupOperation": map[string]any{"OperationId": "op-first", "Status": "SUCCEEDED"},
		}},
		stackGroup("read-group-after-first"),
		stackInstances("list-one", second),
		{RequestID: "delete-second", Data: map[string]any{"OperationId": "op-second"}},
		{RequestID: "operation-second-succeeded", Data: map[string]any{
			"StackGroupOperation": map[string]any{"OperationId": "op-second", "Status": "SUCCEEDED"},
		}},
		stackGroup("read-group-after-second"),
		stackInstances("list-empty"),
		{RequestID: "delete-group"},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-zhangjiakou", alicloud.ROSStackGroupNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ROSStackGroupNativeType,
			NativeID: "test",
		}},
		Action: "delete", IdempotencyKey: "step-stack-group",
	}

	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "delete_stack_instances" ||
		result.ProviderOperationID != "op-first" {
		t.Fatalf("execute=%+v invocations=%+v err=%v", result, provider.invocations, err)
	}
	firstDelete := provider.invocations[2]
	if firstDelete.Operation != "AlibabaCloud.ROS.DeleteStackInstances" ||
		firstDelete.Parameters["RegionIds"] != `["cn-beijing"]` ||
		firstDelete.Parameters["RetainStacks"] != false {
		t.Fatalf("first stack instance deletion=%+v", firstDelete)
	}
	if firstDelete.Parameters["DeploymentTargets"] != `{"AccountIds":["1001"]}` ||
		firstDelete.Parameters["AccountIds"] != nil {
		t.Fatalf("first service-managed deployment targets=%+v", firstDelete.Parameters)
	}

	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "RUNNING" {
		t.Fatalf("running wait=%+v err=%v", wait, err)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || wait.Done || wait.State != "delete_stack_instances" ||
		wait.Data["operation_id"] != "op-second" {
		t.Fatalf("second instance wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	secondDelete := provider.invocations[7]
	if secondDelete.Operation != "AlibabaCloud.ROS.DeleteStackInstances" ||
		secondDelete.Parameters["RegionIds"] != `["cn-shanghai"]` {
		t.Fatalf("second stack instance deletion=%+v", secondDelete)
	}

	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "stack_group_delete_requested" {
		t.Fatalf("group deletion wait=%+v invocations=%+v err=%v", wait, provider.invocations, err)
	}
	if len(provider.invocations) != 12 ||
		provider.invocations[11].Operation != "AlibabaCloud.ROS.DeleteStackGroup" {
		t.Fatalf("stack group deletion order=%+v", provider.invocations)
	}
	for index, invocation := range provider.invocations[:11] {
		if invocation.Operation == "AlibabaCloud.ROS.DeleteStackGroup" {
			t.Fatalf("stack group deleted before instances at invocation %d: %+v", index, invocation)
		}
	}
}

func TestROSStackGroupReportsStackInstanceOperationFailureReason(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{RequestID: "read-group", Data: map[string]any{"StackGroup": map[string]any{
			"StackGroupName": "test", "Status": "ACTIVE", "PermissionModel": "SERVICE_MANAGED",
		}}},
		{RequestID: "list-instance", Data: map[string]any{
			"StackInstances": []any{map[string]any{
				"StackGroupName": "test", "AccountId": "1001", "RegionId": "cn-beijing",
			}},
			"TotalCount": 1,
		}},
		{RequestID: "delete-instance", Data: map[string]any{"OperationId": "op-failed"}},
		{RequestID: "read-operation", Data: map[string]any{"StackGroupOperation": map[string]any{
			"OperationId": "op-failed", "Status": "FAILED",
		}}},
		{RequestID: "read-operation-results", Data: map[string]any{
			"StackGroupOperationResults": []any{map[string]any{
				"AccountId": "1001", "RegionId": "cn-beijing", "Status": "FAILED",
				"StatusReason": "execution role is not authorized",
			}},
			"TotalCount": 1,
		}},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-zhangjiakou", alicloud.ROSStackGroupNativeType,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.ROSStackGroupNativeType,
			NativeID: "test",
		}},
		Action: "delete", IdempotencyKey: "step-stack-group",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Wait(context.Background(), request, result)
	var providerErr *contracts.ProviderCallError
	if !errors.As(err, &providerErr) || providerErr.Provider.Message != "execution role is not authorized" {
		t.Fatalf("wait error=%+v invocations=%+v", err, provider.invocations)
	}
	if len(provider.invocations) != 5 ||
		provider.invocations[4].Operation != "AlibabaCloud.ROS.ListStackGroupOperationResults" {
		t.Fatalf("operation result invocation=%+v", provider.invocations)
	}
	results, ok := providerErr.Provider.Summary["operation_results"].([]map[string]any)
	if !ok || len(results) != 1 || results[0]["AccountId"] != "1001" {
		t.Fatalf("operation failure summary=%+v", providerErr.Provider.Summary)
	}
}

func TestSlowAsynchronousCleanupSpecsUseTenMinuteDeletionCheckTimeout(t *testing.T) {
	t.Parallel()

	for _, nativeType := range []string{alicloud.ROSStackGroupNativeType, "ACS::OTS::Instance"} {
		nativeType := nativeType
		t.Run(nativeType, func(t *testing.T) {
			t.Parallel()
			hook, err := alicloud.NewActionHook(
				&invocationProvider{}, "connection-a", "cn-zhangjiakou", nativeType,
			)
			if err != nil {
				t.Fatal(err)
			}
			if timeout := hook.DeletionCheckTimeout(); timeout != 10*time.Minute {
				t.Fatalf("%s deletion check timeout=%s, want 10m", nativeType, timeout)
			}
		})
	}
}

func TestSpecActionTerminalWaiterCompletesOnConfiguredState(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-request",
		Data: map[string]any{
			"CapacityReservationSet": map[string]any{
				"CapacityReservationItem": []any{
					map[string]any{
						"PrivatePoolOptionsId": "crp-a",
						"Status":               "Released",
					},
				},
			},
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ECS::CapacityReservation",
	)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := hook.Wait(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "reservation-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ECS::CapacityReservation",
				NativeID:   "crp-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}, contracts.ActionResult{})
	if err != nil || !wait.Done || wait.State != "Released" {
		t.Fatalf("terminal wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Parameters["PrivatePoolOptions.Ids"] != `["crp-a"]` {
		t.Fatalf("readback invocation=%+v", provider.invocations)
	}
}

func TestROSStackWaitsFiveSecondsUntilDeleteTerminalState(t *testing.T) {
	t.Parallel()

	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "stack-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: alicloud.ROSStackNativeType,
				NativeID:   "stack-id-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}
	stackResult := func(state string) contracts.InvocationResult {
		return contracts.InvocationResult{
			RequestID: "read-request",
			Data: map[string]any{
				"Stacks": []any{map[string]any{
					"StackId": "stack-id-a",
					"Status":  state,
				}},
			},
		}
	}

	t.Run("delete checks protection then schedules first query", func(t *testing.T) {
		provider := &invocationProvider{results: []contracts.InvocationResult{
			{
				RequestID: "protection-request",
				Data: map[string]any{
					"StackId":            "stack-id-a",
					"Status":             "CREATE_COMPLETE",
					"DeletionProtection": "Disabled",
				},
			},
			{RequestID: "delete-request"},
		}}
		hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.ROSStackNativeType)
		if err != nil {
			t.Fatal(err)
		}
		result, err := hook.Execute(context.Background(), request)
		if err != nil || result.RetryAfter != 5*time.Second {
			t.Fatalf("execute=%+v err=%v", result, err)
		}
	})

	t.Run("uses thirty minute deletion check timeout", func(t *testing.T) {
		hook, err := alicloud.NewActionHook(
			&invocationProvider{},
			"connection-a",
			"cn-hangzhou",
			alicloud.ROSStackNativeType,
		)
		if err != nil {
			t.Fatal(err)
		}
		if timeout := hook.DeletionCheckTimeout(); timeout != 30*time.Minute {
			t.Fatalf("deletion check timeout = %s, want 30m", timeout)
		}
	})

	t.Run("in progress", func(t *testing.T) {
		provider := &invocationProvider{results: []contracts.InvocationResult{stackResult("DELETE_IN_PROGRESS")}}
		hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.ROSStackNativeType)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{})
		if err != nil || wait.Done || wait.State != "DELETE_IN_PROGRESS" || wait.RetryAfter != 5*time.Second {
			t.Fatalf("wait=%+v err=%v", wait, err)
		}
	})

	t.Run("complete", func(t *testing.T) {
		provider := &invocationProvider{results: []contracts.InvocationResult{stackResult("DELETE_COMPLETE")}}
		hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.ROSStackNativeType)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{})
		if err != nil || !wait.Done || wait.State != "DELETE_COMPLETE" {
			t.Fatalf("wait=%+v err=%v", wait, err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		provider := &invocationProvider{results: []contracts.InvocationResult{stackResult("DELETE_FAILED")}}
		hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.ROSStackNativeType)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{})
		var callErr *contracts.ProviderCallError
		if wait.Done || !errors.As(err, &callErr) ||
			callErr.Provider.Category != execution.ErrorProviderFailure ||
			callErr.Provider.Code != "DELETE_FAILED" {
			t.Fatalf("wait=%+v err=%v", wait, err)
		}
	})
}

func TestSpecActionPreconditionBlocksSubscriptionAPIGatewayInstance(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-request",
		Data: map[string]any{
			"Instances": map[string]any{"InstanceAttribute": []any{
				map[string]any{
					"InstanceId":         "api-gateway-a",
					"Status":             "RUNNING",
					"InstanceChargeType": "PrePaid",
				},
			}},
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ApiGateway::Instance",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "api-gateway-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ApiGateway::Instance",
				NativeID:   "api-gateway-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || preflight.Allowed ||
		preflight.Evidence["path"] != "InstanceChargeType" ||
		preflight.Evidence["actual_value"] != "PrePaid" {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.ApiGateway.DescribeInstances" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestSpecActionPreconditionBlocksSubscriptionTSDBInstance(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-request",
		Data: map[string]any{
			"InstanceId": "ts-a", "InstanceStatus": "ACTIVATION",
			"ChargeType": "PREPAY",
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::TSDB::Instance",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "ts-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::TSDB::Instance",
				NativeID:   "ts-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || preflight.Allowed ||
		preflight.Evidence["path"] != "ChargeType" ||
		preflight.Evidence["actual_value"] != "PREPAY" {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation !=
			"AlibabaCloud.TSDB.DescribeHiTSDBInstance" ||
		provider.invocations[0].Parameters["RegionId"] != "cn-hangzhou" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestSpecActionPreconditionBlocksSystemRouteTable(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-system-route-table",
		Data: map[string]any{
			"RouterTableList": map[string]any{"RouterTableListType": []any{
				map[string]any{
					"RouteTableId":   "vtb-system",
					"RouteTableType": "System",
					"Status":         "Available",
				},
			}},
		},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::VPC::RouteTable",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "route-table-system",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::VPC::RouteTable",
				NativeID:   "vtb-system",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || preflight.Allowed ||
		preflight.Evidence["path"] != "RouteTableType" ||
		preflight.Evidence["actual_value"] != "System" {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "DescribeRouteTableList" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestSpecActionResolvesBatchNativeIDForCloudPhoneDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "delete-request",
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"cn-hangzhou",
		"ACS::ECP::Instance",
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "cloud-phone-a",
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::ECP::Instance",
				NativeID:   "cp-a",
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	})
	if err != nil || result.ProviderRequestID != "delete-request" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.ECP.DeleteInstances" ||
		provider.invocations[0].Parameters["RegionId"] != "cn-hangzhou" ||
		provider.invocations[0].Parameters["Force"] != false {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	ids, ok := provider.invocations[0].Parameters["InstanceId"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "cp-a" {
		t.Fatalf("InstanceId=%#v", provider.invocations[0].Parameters["InstanceId"])
	}
}

func TestACKWaiterTreatsNotFoundAsTerminalWithoutDeletingChildren(t *testing.T) {
	t.Parallel()

	client := &failingACKClient{err: &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorNotFound}}}
	hook := alicloud.NewACKHook(client)
	request := contracts.ActionRequest{
		Asset:  asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.ACKClusterNativeType, NativeID: "c-a"}},
		Action: "delete", IdempotencyKey: "step-a",
	}
	wait, err := hook.Wait(context.Background(), request, contracts.ActionResult{ProviderOperationID: "task-a"})
	if err != nil || !wait.Done || wait.RetryAfter != 0 || client.deleteCalls != 0 {
		t.Fatalf("wait=%+v err=%v deleteCalls=%d", wait, err, client.deleteCalls)
	}
}

type failingACKClient struct {
	err         error
	deleteCalls int
}

func (c *failingACKClient) DescribeClusterResources(context.Context, string, bool) ([]alicloud.ClusterResource, string, error) {
	return nil, "", c.err
}

func (c *failingACKClient) DescribeClusterNodes(context.Context, string) ([]alicloud.ClusterNode, string, error) {
	return nil, "", c.err
}

func (c *failingACKClient) DeleteCluster(context.Context, alicloud.DeleteClusterRequest) (alicloud.DeleteClusterResponse, error) {
	c.deleteCalls++
	return alicloud.DeleteClusterResponse{}, c.err
}
