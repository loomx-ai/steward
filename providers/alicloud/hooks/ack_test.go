package hooks_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

type ackClient struct {
	fixture      ackFixture
	resourcesErr error
	nodesErr     error
}

type ackFixture struct {
	RequestID string                     `json:"request_id"`
	Resources []alicloud.ClusterResource `json:"resources"`
	Nodes     []alicloud.ClusterNode     `json:"nodes"`
}

func (c *ackClient) DescribeClusterResources(context.Context, string, bool) ([]alicloud.ClusterResource, string, error) {
	return c.fixture.Resources, c.fixture.RequestID, c.resourcesErr
}

func (c *ackClient) DescribeClusterNodes(context.Context, string) ([]alicloud.ClusterNode, string, error) {
	return c.fixture.Nodes, c.fixture.RequestID, c.nodesErr
}

func TestACKLifecycleToleratesClusterDeletedDuringGraphBuild(t *testing.T) {
	t.Parallel()

	notFound := &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorNotFound}}
	for _, test := range []struct {
		name   string
		client *ackClient
	}{
		{name: "resources", client: &ackClient{resourcesErr: notFound}},
		{name: "nodes", client: &ackClient{nodesErr: notFound}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			contribution, err := hooks.NewACK(test.client, "cn-hangzhou").Contribute(
				context.Background(),
				"scope-root",
				[]asset.Asset{ackAsset("cluster-a", alicloud.ACKClusterNativeType, "c-a")},
			)
			if err != nil {
				t.Fatalf("deleted cluster contribution failed: %v", err)
			}
			if len(contribution.Relationships) != 0 || len(contribution.Bindings) != 0 || len(contribution.Unresolved) != 0 {
				t.Fatalf("deleted cluster contribution = %+v", contribution)
			}
		})
	}
}

func (c *ackClient) DeleteCluster(context.Context, alicloud.DeleteClusterRequest) (alicloud.DeleteClusterResponse, error) {
	panic("graph contribution must not invoke delete")
}

func TestACKLifecycleUsesProviderEvidenceAndNeverInfersFromNamesOrTags(t *testing.T) {
	t.Parallel()

	client := &ackClient{fixture: readACKFixture(t)}
	hook := hooks.NewACK(client, "cn-hangzhou")
	assets := []asset.Asset{
		ackAsset("cluster-a", alicloud.ACKClusterNativeType, "c-a"),
		ackAsset("nat-a", "ACS::VPC::NatGateway", "ngw-a"),
		ackAsset("vpc-a", "ACS::VPC::VPC", "vpc-a"),
		ackAsset("disk-a", "ACS::ECS::Disk", "d-unknown"),
		ackAsset("node-a", "ACS::ECS::Instance", "i-node"),
		{ID: "lookalike", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection-a", NativeType: "ACS::ECS::Instance", NativeID: "i-lookalike"}, Name: "c-a-worker", Tags: map[string]string{"ack-cluster-id": "c-a"}},
	}
	contribution, err := hook.Contribute(context.Background(), "scope-root", assets)
	if err != nil {
		t.Fatalf("build ACK lifecycle graph: %v", err)
	}
	bindings := make(map[asset.AssetID]graph.LifecycleBinding)
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	assertBinding(t, bindings["nat-a"], graph.OwnershipExclusive, graph.CleanupDelegate)
	assertBinding(t, bindings["node-a"], graph.OwnershipExclusive, graph.CleanupDelegate)
	assertBinding(t, bindings["vpc-a"], graph.OwnershipShared, graph.CleanupRetain)
	assertBinding(t, bindings["disk-a"], graph.OwnershipUnknown, graph.CleanupRetain)
	if _, exists := bindings["lookalike"]; exists {
		t.Fatalf("name/tag similarity created lifecycle authority: %+v", bindings["lookalike"])
	}
	for _, binding := range bindings {
		if binding.ControllerAssetID != "cluster-a" || binding.Authority != graph.AuthorityAuthoritative || binding.EvidenceSource != "ack:DescribeClusterResources" && binding.EvidenceSource != "ack:DescribeClusterNodes" {
			t.Fatalf("binding lacks Provider authority evidence: %+v", binding)
		}
	}
}

func assertBinding(t *testing.T, binding graph.LifecycleBinding, ownership graph.Ownership, policy graph.CleanupPolicy) {
	t.Helper()
	if binding.Ownership != ownership || binding.CleanupPolicy != policy {
		t.Fatalf("binding=%+v want ownership=%s policy=%s", binding, ownership, policy)
	}
}

func ackAsset(id asset.AssetID, nativeType, nativeID string) asset.Asset {
	return asset.Asset{ID: id, Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection-a", NativeType: nativeType, NativeID: nativeID}, ScopeID: "scope-region", Location: "cn-hangzhou"}
}

func readACKFixture(t *testing.T) ackFixture {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "fixtures", "ack-cluster-resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture ackFixture
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
