package cleanup_test

import (
	"context"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestVPCInterconnectsAreDeletedBeforeEitherEndpointVPC(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC)
	peer := planningAsset(
		"peer-connection", "connection-peer-order", "ACS::VPC::PeerConnection", "pcc-a", now,
	)
	routerInterface := planningAsset(
		"router-interface", "connection-peer-order", "ACS::VPC::RouterInterface",
		"ri-bp1kbigq0y1gw1qerdswm", now,
	)
	requester := planningAsset(
		"requester-vpc", "connection-peer-order", "ACS::VPC::VPC", "vpc-requester", now,
	)
	accepter := planningAsset(
		"accepter-vpc", "connection-peer-order", "ACS::VPC::VPC", "vpc-accepter", now,
	)
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-peer-order",
		[]asset.Asset{peer, routerInterface, requester, accepter},
		[]graph.Relationship{
			{
				ID: "peer-requester", SourceAssetID: peer.ID, TargetAssetID: requester.ID,
				Type: graph.RelationshipMemberOf, Source: "product_api", Confidence: 1,
				GraphRevision: "graph-peer-order", ObservedAt: now,
			},
			{
				ID: "peer-accepter", SourceAssetID: peer.ID, TargetAssetID: accepter.ID,
				Type: graph.RelationshipMemberOf, Source: "product_api", Confidence: 1,
				GraphRevision: "graph-peer-order", ObservedAt: now,
			},
			{
				ID: "router-interface-requester", SourceAssetID: routerInterface.ID,
				TargetAssetID: requester.ID, Type: graph.RelationshipMemberOf,
				Source: "product_api", Confidence: 1,
				GraphRevision: "graph-peer-order", ObservedAt: now,
			},
			{
				ID: "router-interface-accepter", SourceAssetID: routerInterface.ID,
				TargetAssetID: accepter.ID, Type: graph.RelationshipMemberOf,
				Source: "product_api", Confidence: 1,
				GraphRevision: "graph-peer-order", ObservedAt: now,
			},
		},
		nil,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-peer", Hash: "spec-peer",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-peer-order" }),
	)
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(peer.ID), assetSelector(routerInterface.ID),
			assetSelector(requester.ID), assetSelector(accepter.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	peerStep := cleanupStepForAsset(aggregate.Steps, peer.ID)
	routerInterfaceStep := cleanupStepForAsset(aggregate.Steps, routerInterface.ID)
	for _, endpoint := range []asset.AssetID{requester.ID, accepter.ID} {
		endpointStep := cleanupStepForAsset(aggregate.Steps, endpoint)
		if len(endpointStep.DependsOn) != 2 {
			t.Fatalf("endpoint %s dependencies=%v", endpoint, endpointStep.DependsOn)
		}
		for _, required := range []plan.StepID{peerStep.ID, routerInterfaceStep.ID} {
			found := false
			for _, dependency := range endpointStep.DependsOn {
				found = found || dependency == required
			}
			if !found {
				t.Fatalf("endpoint %s dependencies=%v, missing=%s", endpoint, endpointStep.DependsOn, required)
			}
		}
	}
}

func TestGatewayEndpointIsDeletedBeforeRouteTableAndVPC(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 17, 0, 0, 0, time.UTC)
	endpoint := planningAsset(
		"gateway-endpoint", "connection-gateway-endpoint-order", "ACS::VPC::GatewayEndpoint",
		"vpce-bp1khxwul8setja1pb32z", now,
	)
	routeTable := planningAsset(
		"route-table", "connection-gateway-endpoint-order", "ACS::VPC::RouteTable", "vtb-a", now,
	)
	vpc := planningAsset(
		"vpc", "connection-gateway-endpoint-order", "ACS::VPC::VPC",
		"vpc-bp1x56m37b4rwa2fzmnap", now,
	)
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-gateway-endpoint-order",
		[]asset.Asset{endpoint, routeTable, vpc},
		[]graph.Relationship{
			{
				ID: "endpoint-route-table", SourceAssetID: endpoint.ID, TargetAssetID: routeTable.ID,
				Type: graph.RelationshipUses, Source: "product_api", Confidence: 1,
				GraphRevision: "graph-gateway-endpoint-order", ObservedAt: now,
			},
			{
				ID: "route-table-vpc", SourceAssetID: routeTable.ID, TargetAssetID: vpc.ID,
				Type: graph.RelationshipMemberOf, Source: "product_api", Confidence: 1,
				GraphRevision: "graph-gateway-endpoint-order", ObservedAt: now,
			},
		},
		nil,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-gateway-endpoint", Hash: "spec-gateway-endpoint",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-gateway-endpoint-order" }),
	)
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(endpoint.ID), assetSelector(routeTable.ID), assetSelector(vpc.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpointStep := cleanupStepForAsset(aggregate.Steps, endpoint.ID)
	routeTableStep := cleanupStepForAsset(aggregate.Steps, routeTable.ID)
	vpcStep := cleanupStepForAsset(aggregate.Steps, vpc.ID)
	if len(routeTableStep.DependsOn) != 1 || routeTableStep.DependsOn[0] != endpointStep.ID {
		t.Fatalf("route table dependencies=%v, endpoint step=%s", routeTableStep.DependsOn, endpointStep.ID)
	}
	if len(vpcStep.DependsOn) != 1 || vpcStep.DependsOn[0] != routeTableStep.ID {
		t.Fatalf("VPC dependencies=%v, route table step=%s", vpcStep.DependsOn, routeTableStep.ID)
	}
}

func TestFailedSecurityGroupDependencyPreventsVPCDeleteCall(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 5, 13, 30, 0, 0, time.UTC)
	securityGroup := planningAsset(
		"security-group",
		"connection-dependency",
		"ACS::ECS::SecurityGroup",
		"sg-a",
		now,
	)
	vpc := planningAsset(
		"vpc",
		"connection-dependency",
		"ACS::VPC::VPC",
		"vpc-a",
		now,
	)
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-dependency",
		[]asset.Asset{securityGroup, vpc},
		[]graph.Relationship{{
			ID:            "security-group-vpc",
			SourceAssetID: securityGroup.ID,
			TargetAssetID: vpc.ID,
			Type:          graph.RelationshipMemberOf,
			Source:        "resource_center_configuration",
			Confidence:    1,
			GraphRevision: "graph-dependency",
			ObservedAt:    now,
		}},
		nil,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud,
			Revision: "bundle-a",
			Hash:     "spec-a",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-dependency" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-dependency" }),
	)
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(securityGroup.ID),
			assetSelector(vpc.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Steps) != 2 ||
		aggregate.Steps[0].AssetID != securityGroup.ID ||
		aggregate.Steps[1].AssetID != vpc.ID ||
		len(aggregate.Steps[1].DependsOn) != 1 ||
		aggregate.Steps[1].DependsOn[0] != aggregate.Steps[0].ID {
		t.Fatalf("dependency plan = %+v", aggregate.Steps)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID:  "cln-dependency",
		RequestedBy:    "operator",
		IdempotencyKey: "dependency-execution",
		Confirmation: cleanup.ExecutionConfirmation{
			Acknowledged: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	securityGroupDriver := &scriptedActionDriver{executeErrors: []error{
		&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorDependencyViolation,
			Code:     "DependencyViolation.NetworkInterface",
			Message:  "security group still has a network interface",
		}},
	}}
	vpcDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			if value.ID == securityGroup.ID {
				return securityGroupDriver, nil
			}
			return vpcDriver, nil
		}),
	)

	securityGroupJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		aggregate.Task.ID,
		securityGroup.ID,
	)
	vpcJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		aggregate.Task.ID,
		vpc.ID,
	)
	if err := handler.Handle(ctx, securityGroupJob); err != nil {
		t.Fatalf("security group action: %v", err)
	}
	if err := handler.Handle(ctx, vpcJob); err != nil {
		t.Fatalf("settle VPC failed dependency: %v", err)
	}
	if vpcDriver.executeCalls != 0 {
		t.Fatalf("VPC provider delete calls = %d, want 0", vpcDriver.executeCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].AssetID != securityGroup.ID ||
		actions[0].Status != execution.ActionFailed {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
}

func TestRetainedDependencySkipsDependentDeleteCall(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	networkInterface := planningAsset(
		"network-interface",
		"connection-retained-dependency",
		"ACS::ECS::NetworkInterface",
		"eni-primary",
		now,
	)
	vSwitch := planningAsset(
		"vswitch",
		"connection-retained-dependency",
		"ACS::VPC::VSwitch",
		"vsw-a",
		now,
	)
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-retained-dependency",
		[]asset.Asset{networkInterface, vSwitch},
		[]graph.Relationship{{
			ID:            "network-interface-vswitch",
			SourceAssetID: networkInterface.ID,
			TargetAssetID: vSwitch.ID,
			Type:          graph.RelationshipMemberOf,
			Source:        "resource_center_configuration",
			Confidence:    1,
			GraphRevision: "graph-retained-dependency",
			ObservedAt:    now,
		}},
		nil,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-retained-dependency" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-retained-dependency" }),
	)
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(networkInterface.ID), assetSelector(vSwitch.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-retained-dependency", RequestedBy: "operator",
		IdempotencyKey: "retained-dependency-execution",
		Confirmation:   cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	networkInterfaceDriver := &scriptedActionDriver{executeErrors: []error{
		&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorUnsupported,
			Code:     "CleanupUnsupported.PrimaryNetworkInterface",
			Message:  "primary network interfaces cannot be deleted directly",
			Summary: map[string]any{
				"skip_reason": string(asset.SkipProductUnsupported),
			},
		}},
	}}
	vSwitchDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			if value.ID == networkInterface.ID {
				return networkInterfaceDriver, nil
			}
			return vSwitchDriver, nil
		}),
	)

	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(
		t, repositories, aggregate.Task.ID, networkInterface.ID,
	)); err != nil {
		t.Fatalf("network interface action: %v", err)
	}
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(
		t, repositories, aggregate.Task.ID, vSwitch.ID,
	)); err != nil {
		t.Fatalf("VSwitch action: %v", err)
	}
	if vSwitchDriver.executeCalls != 0 {
		t.Fatalf("VSwitch provider delete calls = %d, want 0", vSwitchDriver.executeCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 2 {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	actionByAsset := make(map[asset.AssetID]execution.ActionAttempt, len(actions))
	for _, action := range actions {
		actionByAsset[action.AssetID] = action
	}
	if actionByAsset[networkInterface.ID].Status != execution.ActionSkipped ||
		actionByAsset[vSwitch.ID].Status != execution.ActionSkipped ||
		actionByAsset[vSwitch.ID].SkipReason != string(asset.SkipDependencyRetained) {
		t.Fatalf("actions=%+v", actions)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
}

func TestFailedAncestorStopsIndirectDependencyWithoutRetryLoop(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 10, 30, 0, 0, time.UTC)
	leaf := planningAsset("leaf", "connection-chain", "ACS::Test::Leaf", "leaf-a", now)
	middle := planningAsset("middle", "connection-chain", "ACS::Test::Middle", "middle-a", now)
	root := planningAsset("root", "connection-chain", "ACS::Test::Root", "root-a", now)
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-chain",
		[]asset.Asset{leaf, middle, root},
		[]graph.Relationship{
			{
				ID: "leaf-middle", SourceAssetID: leaf.ID, TargetAssetID: middle.ID,
				Type: graph.RelationshipMemberOf, Source: "test", Confidence: 1,
				GraphRevision: "graph-chain", ObservedAt: now,
			},
			{
				ID: "middle-root", SourceAssetID: middle.ID, TargetAssetID: root.ID,
				Type: graph.RelationshipMemberOf, Source: "test", Confidence: 1,
				GraphRevision: "graph-chain", ObservedAt: now,
			},
		},
		nil,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-chain" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-chain" }),
	)
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(leaf.ID), assetSelector(middle.ID), assetSelector(root.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Steps) != 3 ||
		aggregate.Steps[0].AssetID != leaf.ID ||
		aggregate.Steps[1].AssetID != middle.ID ||
		aggregate.Steps[2].AssetID != root.ID {
		t.Fatalf("dependency chain = %+v", aggregate.Steps)
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-chain", RequestedBy: "operator",
		IdempotencyKey: "chain-execution",
		Confirmation:   cleanup.ExecutionConfirmation{Acknowledged: true},
	}); err != nil {
		t.Fatal(err)
	}

	leafDriver := &scriptedActionDriver{executeErrors: []error{
		&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorDependencyViolation,
			Code:     "DependencyViolation",
			Message:  "leaf cleanup failed",
		}},
	}}
	otherDriver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			if value.ID == leaf.ID {
				return leafDriver, nil
			}
			return otherDriver, nil
		}),
	)

	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-chain", leaf.ID)); err != nil {
		t.Fatalf("leaf action: %v", err)
	}
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-chain", root.ID)); err != nil {
		t.Fatalf("settle indirect failed dependency: %v", err)
	}
	if otherDriver.executeCalls != 0 {
		t.Fatalf("blocked provider calls = %d, want 0", otherDriver.executeCalls)
	}
}
