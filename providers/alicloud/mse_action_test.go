package alicloud_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestMSEClusterDestroySuccessIsTerminal(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{{
		RequestID: "mse-read-request",
		Data: map[string]any{"Data": map[string]any{
			"InstanceId": "mse-cn-811e753ed0b",
			"InitStatus": "DESTROY_SUCCESS",
		}},
	}}}
	hook, err := alicloud.NewActionHook(
		provider,
		"connection-a",
		"me-east-1",
		"ACS::MSE::Cluster",
	)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := hook.Wait(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{ID: "mse-a", Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::MSE::Cluster", NativeID: "mse-cn-811e753ed0b",
		}},
		Action: "delete", IdempotencyKey: "mse-delete-a",
	}, contracts.ActionResult{})
	if err != nil || !wait.Done || wait.State != "DESTROY_SUCCESS" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	if len(provider.invocations) != 1 ||
		provider.invocations[0].Operation != "AlibabaCloud.MSE.QueryClusterDetail" ||
		provider.invocations[0].Parameters["InstanceId"] != "mse-cn-811e753ed0b" {
		t.Fatalf("readback invocation=%+v", provider.invocations)
	}
}
