package alicloud_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestBPStudioActionReleasesApplicationThenDeletesRecord(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "read-deployed",
			Data: map[string]any{"Data": map[string]any{
				"ApplicationId": "app-a", "Status": "Deployed_Success",
			}},
		},
		{RequestID: "release-request"},
		{
			RequestID: "read-destroyed",
			Data: map[string]any{"Data": map[string]any{
				"ApplicationId": "app-a", "Status": "Destroyed_Success",
			}},
		},
		{
			RequestID: "019FD619-5EB1-5204-9E71-C13A957ABF56",
			Data: map[string]any{
				"Message":   "",
				"RequestId": "019FD619-5EB1-5204-9E71-C13A957ABF56",
				"Code":      200,
			},
		},
	}}
	hook, err := alicloud.NewBPStudioHook(provider, "connection-a", "cn-shanghai")
	if err != nil {
		t.Fatal(err)
	}
	request := complexDeleteRequest(alicloud.BPStudioApplicationNativeType, "app-a")
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "release-request" ||
		result.Data["phase"] != "release" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "delete_requested" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 4 ||
		provider.invocations[0].Operation != "AlibabaCloud.BPStudio.GetApplication" ||
		provider.invocations[1].Operation != "AlibabaCloud.BPStudio.ReleaseApplication" ||
		provider.invocations[2].Operation != "AlibabaCloud.BPStudio.GetApplication" ||
		provider.invocations[3].Operation != "AlibabaCloud.BPStudio.DeleteApplication" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	deleteInvocation := provider.invocations[3]
	if deleteInvocation.Scope["region"] != "cn-hangzhou" ||
		!reflect.DeepEqual(deleteInvocation.Parameters, map[string]any{
			"ApplicationId": "app-a",
			"Force":         true,
		}) {
		t.Fatalf("delete invocation=%+v", deleteInvocation)
	}
}

func TestBPStudioActionTreatsCode8004ReadbackErrorAsDeleted(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{{}},
		errors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorProviderFailure,
			Code:      "8004",
			Message:   "application does not exist",
			RequestID: "read-deleted",
		}}},
	}
	hook, err := alicloud.NewBPStudioHook(provider, "connection-a", "cn-hangzhou")
	if err != nil {
		t.Fatal(err)
	}
	wait, err := hook.Wait(
		context.Background(),
		complexDeleteRequest(alicloud.BPStudioApplicationNativeType, "app-a"),
		contracts.ActionResult{Data: map[string]any{"phase": "delete"}},
	)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.BPStudio.GetApplication" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestBPStudioActionDoesNotTreatMessageAloneAsDeleted(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-failed",
		Data: map[string]any{
			"Code":      "8005",
			"Message":   "bp.java.8004",
			"RequestId": "read-failed",
		},
	}}}
	hook, err := alicloud.NewBPStudioHook(provider, "connection-a", "cn-hangzhou")
	if err != nil {
		t.Fatal(err)
	}
	_, err = hook.Wait(
		context.Background(),
		complexDeleteRequest(alicloud.BPStudioApplicationNativeType, "app-a"),
		contracts.ActionResult{Data: map[string]any{"phase": "delete"}},
	)
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) || providerError.Provider.Code != "8005" {
		t.Fatalf("error=%#v", err)
	}
}

func TestBPStudioActionDeletesUndeployedApplicationWithoutRelease(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "read-verified",
			Data: map[string]any{
				"Code": 200,
				"Data": map[string]any{
					"ApplicationId": "FU5Y6G68NS9KTZLK",
					"Status":        "Verified_Success",
					"ResourceList":  []any{},
				},
			},
		},
		{
			RequestID: "019FD619-5EB1-5204-9E71-C13A957ABF56",
			Data: map[string]any{
				"Message":   "",
				"RequestId": "019FD619-5EB1-5204-9E71-C13A957ABF56",
				"Code":      200,
			},
		},
		{
			RequestID: "read-deleted",
			Data: map[string]any{
				"Code":      "8004",
				"Message":   "bp.java.8004",
				"RequestId": "read-deleted",
			},
		},
	}}
	hook, err := alicloud.NewBPStudioHook(provider, "connection-a", "cn-shanghai")
	if err != nil {
		t.Fatal(err)
	}
	request := complexDeleteRequest(
		alicloud.BPStudioApplicationNativeType,
		"FU5Y6G68NS9KTZLK",
	)
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "019FD619-5EB1-5204-9E71-C13A957ABF56" ||
		result.Data["phase"] != "delete" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 3 ||
		provider.invocations[0].Operation != "AlibabaCloud.BPStudio.GetApplication" ||
		provider.invocations[1].Operation != "AlibabaCloud.BPStudio.DeleteApplication" ||
		provider.invocations[2].Operation != "AlibabaCloud.BPStudio.GetApplication" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
	deleteInvocation := provider.invocations[1]
	if deleteInvocation.Scope["region"] != "cn-hangzhou" ||
		!reflect.DeepEqual(deleteInvocation.Parameters, map[string]any{
			"ApplicationId": "FU5Y6G68NS9KTZLK",
			"Force":         true,
		}) {
		t.Fatalf("delete invocation=%+v", deleteInvocation)
	}
}

func TestCloudFirewallActionProtectsActiveSubscription(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-request",
		Data: map[string]any{
			"InstanceId": "cfw-a", "InstanceStatus": "normal",
			"Version": 3, "Expire": int64(4102444800000),
		},
	}}}
	hook, err := alicloud.NewCloudFirewallHook(
		provider,
		"connection-a",
		"cn-hangzhou",
	)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := hook.Preflight(
		context.Background(),
		complexDeleteRequest(alicloud.CloudFirewallInstanceNativeType, "cfw-a"),
	)
	if err != nil || preflight.Allowed ||
		preflight.Evidence["version"] != 3 {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation !=
			"AlibabaCloud.CloudFirewall.DescribeUserBuyVersion" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestCloudFirewallPreflightTreatsDeletingAsAbsent(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "read-deleting-request",
		Data: map[string]any{
			"InstanceId": "cfw-deleting", "InstanceStatus": "Deleting",
			"Version": 10,
		},
	}}}
	hook, err := alicloud.NewCloudFirewallHook(
		provider,
		"connection-a",
		"cn-hangzhou",
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Preflight(
		context.Background(),
		complexDeleteRequest(alicloud.CloudFirewallInstanceNativeType, "cfw-deleting"),
	)
	if err != nil || !result.Absent || result.Allowed ||
		result.Evidence["state"] != "Deleting" {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 1 {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func TestENSActionSelectsPrepaidReleaseAPI(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		{
			RequestID: "read-request",
			Data: map[string]any{
				"Instances": map[string]any{"Instance": []any{
					map[string]any{"InstanceId": "ens-a", "Status": "Running"},
				}},
			},
		},
		{
			RequestID: "renewal-request",
			Data: map[string]any{
				"InstanceRenewAttributes": map[string]any{
					"InstanceRenewAttribute": []any{
						map[string]any{"InstanceId": "ens-a"},
					},
				},
			},
		},
		{RequestID: "release-request"},
	}}
	hook, err := alicloud.NewENSHook(provider, "connection-a", "cn-hangzhou")
	if err != nil {
		t.Fatal(err)
	}
	result, err := hook.Execute(
		context.Background(),
		complexDeleteRequest(alicloud.ENSInstanceNativeType, "ens-a"),
	)
	if err != nil || result.ProviderRequestID != "release-request" ||
		result.Data["charge_type"] != "prepaid" {
		t.Fatalf("execute=%+v err=%v", result, err)
	}
	if len(provider.invocations) != 3 ||
		provider.invocations[2].Operation != "AlibabaCloud.ENS.ReleasePrePaidInstance" {
		t.Fatalf("invocations=%+v", provider.invocations)
	}
}

func complexDeleteRequest(nativeType, nativeID string) contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset: asset.Asset{
			ID: asset.AssetID(nativeID),
			Identity: asset.Identity{
				Provider:   asset.ProviderAliCloud,
				NativeType: nativeType,
				NativeID:   nativeID,
			},
		},
		Action: "delete", IdempotencyKey: "step-a",
	}
}
