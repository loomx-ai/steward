package graph_test

import (
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/graph"
)

func TestResolverSelectsHighestExclusiveController(t *testing.T) {
	bindings := []graph.LifecycleBinding{
		{ControllerAssetID: "ack", ManagedAssetID: "ecs", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
		{ControllerAssetID: "ros", ManagedAssetID: "ack", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
	}
	result, err := graph.ResolveAuthority("ecs", bindings)
	if err != nil || result.ControllerAssetID != "ros" || len(result.Chain) != 2 {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestResolverBlocksCycles(t *testing.T) {
	cycle := []graph.LifecycleBinding{
		{ControllerAssetID: "a", ManagedAssetID: "b", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
		{ControllerAssetID: "b", ManagedAssetID: "a", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
	}
	if _, err := graph.ResolveAuthority("a", cycle); !errors.Is(err, graph.ErrLifecycleCycle) {
		t.Fatalf("expected lifecycle cycle, got %v", err)
	}
}

func TestResolverBlocksConflictingAuthoritativeControllers(t *testing.T) {
	bindings := []graph.LifecycleBinding{
		{ControllerAssetID: "ack-a", ManagedAssetID: "ecs", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
		{ControllerAssetID: "ack-b", ManagedAssetID: "ecs", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1},
	}
	if _, err := graph.ResolveAuthority("ecs", bindings); !errors.Is(err, graph.ErrLifecycleConflict) {
		t.Fatalf("expected lifecycle conflict, got %v", err)
	}
}

func TestResolverRejectsLowConfidenceAuthority(t *testing.T) {
	bindings := []graph.LifecycleBinding{
		{ControllerAssetID: "ack", ManagedAssetID: "ecs", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 0.7},
	}
	if _, err := graph.ResolveAuthority("ecs", bindings); !errors.Is(err, graph.ErrLifecycleConfidence) {
		t.Fatalf("expected low confidence block, got %v", err)
	}
}
