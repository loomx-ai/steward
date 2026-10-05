package alicloud

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// gatedDataWorksFactory parks every project lookup until release is closed and
// answers each resource group with its own project and network.
type gatedDataWorksFactory struct {
	*topologyRuntimeFactory
	release chan struct{}

	mu       sync.Mutex
	calls    int
	inFlight int
}

func (f *gatedDataWorksFactory) Invoke(_ context.Context, _ contracts.Credential, _ string, _ catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	group := invocation.Parameters["ResourceGroupId"].(string)
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if invocation.Operation == "AlibabaCloud.DataWorks.ListResourceGroupAssociateProjects" {
		f.mu.Lock()
		f.inFlight++
		f.mu.Unlock()
		<-f.release
		return contracts.InvocationResult{RequestID: "projects-" + group, Data: map[string]any{"ProjectIdList": []any{"project-" + group}}}, nil
	}
	return contracts.InvocationResult{RequestID: "networks-" + group, Data: map[string]any{"PagingInfo": map[string]any{
		"NetworkList": []any{map[string]any{"Id": "net-" + group, "ResourceGroupId": group, "VpcId": "vpc-" + group, "VswitchId": "vsw-" + group}},
		"TotalCount":  float64(1),
	}}}, nil
}

func TestDataWorksEnrichmentReadsGroupsConcurrentlyWithOneCredential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
			Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
		}}
		factory := &gatedDataWorksFactory{topologyRuntimeFactory: &topologyRuntimeFactory{}, release: make(chan struct{})}
		runtime, err := newRuntime(source, factory)
		if err != nil {
			t.Fatal(err)
		}
		groups := []string{"rg-a", "rg-b", "rg-c"}
		items := make([]contracts.InventoryItem, len(groups))
		for index, group := range groups {
			items[index] = contracts.InventoryItem{NativeType: DataWorksResourceGroupNativeType, NativeID: group}
		}
		var enriched []contracts.InventoryItem
		go func() {
			enriched, err = runtime.enrichDataWorksResourceGroups(context.Background(), contracts.InventoryRequest{
				ConnectionID: "connection-a",
				Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
			}, items)
		}()
		synctest.Wait()
		if factory.inFlight != len(groups) {
			t.Fatalf("project lookups in flight = %d, want %d", factory.inFlight, len(groups))
		}
		close(factory.release)
		synctest.Wait()
		if err != nil {
			t.Fatal(err)
		}
		for index, group := range groups {
			normalized := enriched[index].Normalized
			if !reflect.DeepEqual(normalized[NormalizedDataWorksProjectIDsField], []any{"project-" + group}) ||
				normalized[NormalizedDataWorksProjectRequestIDField] != "projects-"+group ||
				normalized["vpc_id"] != "vpc-"+group || normalized["vswitch_id"] != "vsw-"+group {
				t.Fatalf("item %d (%s) = %#v", index, group, normalized)
			}
		}
		if factory.calls != 2*len(groups) || source.calls != 1 {
			t.Fatalf("calls = %d, credential resolves = %d; want %d and 1", factory.calls, source.calls, 2*len(groups))
		}
	})
}
