package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestOwnedSubresourcesAreDeletedBeforeTheirParent(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ parentType, childType, childID, source string }{
		{"ACS::ALB::LoadBalancer", "ACS::ALB::Listener", "lsn-a", "alb:ListListeners"},
		{"ACS::NLB::LoadBalancer", "ACS::NLB::Listener", "lsn-b", "nlb:ListListeners"},
		{"ACS::SLB::LoadBalancer", "ACS::SLB::VServerGroup", "rsp-a", "slb:DescribeVServerGroups"},
		{"ACS::Ons::Instance", "ACS::Ons::Topic", "MQ_INST_a/orders", "ons:OnsTopicList"},
		{"ACS::Ons::Instance", "ACS::Ons::Group", "MQ_INST_a/GID_orders", "ons:OnsGroupList"},
		{"ACS::AliKafka::Instance", "ACS::AliKafka::Topic", "alikafka-a/orders", "alikafka:GetTopicList"},
		{"ACS::AliKafka::Instance", "ACS::AliKafka::ConsumerGroup", "alikafka-a/orders", "alikafka:GetConsumerList"},
		{"ACS::RocketMQ::Instance", "ACS::RocketMQ::Topic", "rmq-a/orders", "rocketmq:ListTopics"},
		{"ACS::RocketMQ::Instance", "ACS::RocketMQ::ConsumerGroup", "rmq-a/GID_orders", "rocketmq:ListConsumerGroups"},
		{"ACS::CR::Instance", "ACS::CR::Namespace", "crn-a", "cr:ListNamespace"},
		{"ACS::VPN::VpnGateway", "ACS::VPN::SslVpnServer", "vss-a", "vpc:DescribeSslVpnServers"},
		{"ACS::VPN::SslVpnServer", "ACS::VPN::SslVpnClientCert", "vsc-a", "vpc:DescribeSslVpnClientCerts"},
		{"ACS::VPN::VpnGateway", "ACS::VPN::IpsecServer", "iss-a", "vpc:ListIpsecServers"},
	} {
		t.Run(test.childType, func(t *testing.T) {
			t.Parallel()
			parent := nasAsset("parent", test.parentType, "lb-a", nil)
			child := nasAsset("child", test.childType, test.childID, map[string]any{"loadBalancerId": "lb-a", "instanceId": "lb-a", "vpnGatewayId": "lb-a", "sslVpnServerId": "lb-a"})
			other := nasAsset("other", test.childType, "other", map[string]any{"loadBalancerId": "lb-unscanned", "instanceId": "lb-unscanned", "vpnGatewayId": "lb-unscanned", "sslVpnServerId": "lb-unscanned"})
			contribution, err := hooks.NewSubresourceOwnership().Contribute(context.Background(), "scope-hangzhou", []asset.Asset{child, other, parent})
			if err != nil {
				t.Fatal(err)
			}
			if len(contribution.Bindings) != 1 {
				t.Fatalf("contribution = %+v", contribution)
			}
			binding := contribution.Bindings[0]
			if binding.ControllerAssetID != parent.ID || binding.ManagedAssetID != child.ID ||
				binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDirect ||
				binding.EvidenceSource != test.source {
				t.Fatalf("binding = %+v", binding)
			}
			cleanupPlan, err := plan.Solve(plan.Input{
				CleanupTaskID: "cleanup", ResolvedAssetIDs: []asset.AssetID{parent.ID},
				Assets: []asset.Asset{parent, child}, LifecycleBindings: contribution.Bindings,
				Revision: plan.RevisionBinding{InventoryRevision: "i", GraphRevision: "g", SpecBundleRevision: "b", SpecHash: "s"},
			})
			if err != nil || len(cleanupPlan.Blockers) != 0 || len(cleanupPlan.Steps) != 2 {
				t.Fatalf("plan = %+v, err = %v", cleanupPlan, err)
			}
			childStep := requireCleanupStepForAsset(t, cleanupPlan.Steps, child.ID)
			parentStep := requireCleanupStepForAsset(t, cleanupPlan.Steps, parent.ID)
			if len(parentStep.DependsOn) != 1 || parentStep.DependsOn[0] != childStep.ID {
				t.Fatalf("order: child=%+v parent=%+v", childStep, parentStep)
			}
		})
	}
}

func TestRepositoriesBelongToTheNamespaceTheyName(t *testing.T) {
	t.Parallel()

	namespace := nasAsset("namespace", "ACS::CR::Namespace", "crn-a", map[string]any{"instanceId": "cri-a", "namespaceName": "apps"})
	sameName := nasAsset("same-name", "ACS::CR::Namespace", "crn-b", map[string]any{"instanceId": "cri-b", "namespaceName": "apps"})
	repository := nasAsset("repository", "ACS::CR::Repository", "crr-a", map[string]any{"instanceId": "cri-a", "namespaceName": "apps"})
	contribution, err := hooks.NewSubresourceOwnership().Contribute(context.Background(), "scope-hangzhou", []asset.Asset{repository, sameName, namespace})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 || contribution.Bindings[0].ControllerAssetID != namespace.ID ||
		len(contribution.Relationships) != 1 || contribution.Relationships[0].TargetAssetID != namespace.ID ||
		contribution.Relationships[0].Type != graph.RelationshipMemberOf {
		t.Fatalf("contribution = %+v", contribution)
	}
}

func TestLogstoresAreDeletedWithTheirProject(t *testing.T) {
	t.Parallel()

	project := nasAsset("project", "ACS::SLS::Project", "app-logs", nil)
	logstore := nasAsset("logstore", "ACS::SLS::LogStore", "app-logs/internal-operation_log", map[string]any{"project": "app-logs"})
	contribution, err := hooks.NewSubresourceOwnership().Contribute(context.Background(), "scope-hangzhou", []asset.Asset{logstore, project})
	if err != nil || len(contribution.Bindings) != 1 {
		t.Fatalf("contribution = %+v, err = %v", contribution, err)
	}
	binding := contribution.Bindings[0]
	if binding.CleanupPolicy != graph.CleanupDelegate || !binding.DirectCleanupAllowed || binding.Evidence["delete_by_default"] != true {
		t.Fatalf("binding = %+v", binding)
	}
	revision := plan.RevisionBinding{InventoryRevision: "i", GraphRevision: "g", SpecBundleRevision: "b", SpecHash: "s"}
	withProject, err := plan.Solve(plan.Input{
		CleanupTaskID: "cleanup", ResolvedAssetIDs: []asset.AssetID{project.ID},
		Assets: []asset.Asset{project, logstore}, LifecycleBindings: contribution.Bindings, Revision: revision,
	})
	if err != nil || len(withProject.Blockers) != 0 || len(withProject.ImpactItems) != 1 ||
		withProject.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
		t.Fatalf("project plan = %+v, err = %v", withProject, err)
	}
	verify := requireCleanupStepForAsset(t, withProject.Steps, logstore.ID)
	if verify.Action != "verify_managed_absent" {
		t.Fatalf("logstore step = %+v", verify)
	}
	alone, err := plan.Solve(plan.Input{
		CleanupTaskID: "cleanup", ResolvedAssetIDs: []asset.AssetID{logstore.ID},
		Assets: []asset.Asset{project, logstore}, LifecycleBindings: contribution.Bindings, Revision: revision,
	})
	if err != nil || len(alone.Blockers) != 0 || len(alone.Steps) != 1 || alone.Steps[0].Action != "delete" {
		t.Fatalf("logstore plan = %+v, err = %v", alone, err)
	}
}
