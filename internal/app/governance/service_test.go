package governance_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type graphRepository struct {
	assets        []asset.Asset
	relationships []graph.Relationship
	bindings      []graph.LifecycleBinding
}

type staticContributor struct{ contribution governance.Contribution }

func (c staticContributor) Contribute(context.Context, asset.ScopeID, []asset.Asset) (governance.Contribution, error) {
	return c.contribution, nil
}

func (r *graphRepository) ListActiveAssetsByConnection(context.Context, asset.ConnectionID, asset.ResourceKindID) ([]asset.Asset, error) {
	result := make([]asset.Asset, 0, len(r.assets))
	for _, value := range r.assets {
		if value.ClosedAt == nil {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *graphRepository) ReplaceGraph(_ context.Context, _ asset.ScopeID, revision string, relationships []graph.Relationship, bindings []graph.LifecycleBinding) error {
	now := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	for index := range r.relationships {
		if r.relationships[index].ClosedAt == nil && r.relationships[index].GraphRevision != revision {
			r.relationships[index].ClosedAt = &now
		}
	}
	for index := range r.bindings {
		if r.bindings[index].ClosedAt == nil && r.bindings[index].GraphRevision != revision {
			r.bindings[index].ClosedAt = &now
		}
	}
	r.relationships = append(r.relationships, relationships...)
	r.bindings = append(r.bindings, bindings...)
	return nil
}

func (r *graphRepository) ListRelationships(context.Context, asset.AssetID) ([]graph.Relationship, error) {
	return nil, nil
}

func (r *graphRepository) ListLifecycleBindings(context.Context, asset.AssetID) ([]graph.LifecycleBinding, error) {
	return nil, nil
}

func TestGraphRebuildResolvesLaterReferencesAndClosesOldRevision(t *testing.T) {
	t.Parallel()

	repository := &graphRepository{assets: []asset.Asset{{
		ID: "ecs-1", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection-1", NativeType: "ACS::ECS::Instance", NativeID: "i-1"},
		ScopeID: "scope-1", Normalized: map[string]any{"VpcAttributes": map[string]any{"VpcId": "vpc-1"}},
	}}}
	compiled := spec.Bundle{Provider: asset.ProviderAliCloud, Revision: "bundle-1", Specs: []spec.CompiledSpec{{
		Definition: spec.ResourceKindSpec{
			Metadata:      spec.Metadata{Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance"},
			Relationships: []spec.RelationshipSpec{{Type: "member_of", TargetType: "ACS::VPC::VPC", TargetIDPath: "VpcAttributes.VpcId"}},
		},
	}}}
	observedAt := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	service := governance.NewService(repository, repository, governance.WithClock(func() time.Time { return observedAt }))

	first, err := service.RebuildGraph(context.Background(), "scope-root", "connection-1", "graph-1", compiled, nil)
	if err != nil {
		t.Fatalf("first graph rebuild: %v", err)
	}
	if len(first.Unresolved) != 1 || len(repository.relationships) != 0 {
		t.Fatalf("first rebuild result=%+v relationships=%+v", first, repository.relationships)
	}
	repository.assets = append(repository.assets, asset.Asset{
		ID: "vpc-1", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection-1", NativeType: "ACS::VPC::VPC", NativeID: "vpc-1"}, ScopeID: "scope-global",
	})
	second, err := service.RebuildGraph(context.Background(), "scope-root", "connection-1", "graph-2", compiled, nil)
	if err != nil {
		t.Fatalf("second graph rebuild: %v", err)
	}
	if len(second.Unresolved) != 0 || len(second.Relationships) != 1 {
		t.Fatalf("second rebuild result=%+v", second)
	}
	created := second.Relationships[0]
	if created.SourceAssetID != "ecs-1" || created.TargetAssetID != "vpc-1" || created.GraphRevision != "graph-2" || created.Confidence != 1 {
		t.Fatalf("resolved relationship=%+v", created)
	}

	repository.assets[0].Normalized = map[string]any{"VpcAttributes": map[string]any{"VpcId": "vpc-2"}}
	repository.assets = append(repository.assets, asset.Asset{
		ID: "vpc-2", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection-1", NativeType: "ACS::VPC::VPC", NativeID: "vpc-2"}, ScopeID: "scope-1",
	})
	if _, err := service.RebuildGraph(context.Background(), "scope-root", "connection-1", "graph-3", compiled, nil); err != nil {
		t.Fatalf("third graph rebuild: %v", err)
	}
	if repository.relationships[0].ClosedAt == nil {
		t.Fatalf("old relationship revision remained active: %+v", repository.relationships)
	}
}

func TestGraphRebuildIgnoresStaticSelfReferences(t *testing.T) {
	t.Parallel()

	repository := &graphRepository{assets: []asset.Asset{{
		ID: "snapshot-1",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-1", NativeType: "ACS::ECS::Snapshot",
			NativeID: "s-1", ScopeKey: "region:cn-hangzhou",
		},
		Normalized: map[string]any{"sourceSnapshotId": "s-1"},
	}}}
	bundle := spec.Bundle{
		Provider: asset.ProviderAliCloud,
		Revision: "bundle-1",
		Specs: []spec.CompiledSpec{{
			Definition: spec.ResourceKindSpec{
				Metadata: spec.Metadata{
					Provider:   asset.ProviderAliCloud,
					NativeType: "ACS::ECS::Snapshot",
				},
				Relationships: []spec.RelationshipSpec{{
					Type: "created_from", TargetType: "ACS::ECS::Snapshot",
					TargetIDPath: "sourceSnapshotId",
				}},
			},
		}},
	}

	result, err := governance.NewService(repository, repository).RebuildGraph(
		context.Background(),
		"scope-root",
		"connection-1",
		"graph-1",
		bundle,
		nil,
	)
	if err != nil {
		t.Fatalf("rebuild graph with self reference: %v", err)
	}
	if len(result.Relationships) != 0 || len(result.Unresolved) != 0 {
		t.Fatalf("self reference was retained: %+v", result)
	}
}

func TestGraphRebuildRejectsContributorReferencesOutsideConnectionAssets(t *testing.T) {
	t.Parallel()

	repository := &graphRepository{assets: []asset.Asset{{
		ID: "controller", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-1", NativeType: "ACS::CS::Cluster", NativeID: "cluster-1"},
	}}}
	service := governance.NewService(repository, repository)
	contributor := staticContributor{contribution: governance.Contribution{Bindings: []graph.LifecycleBinding{{
		ControllerAssetID: "controller", ManagedAssetID: "foreign-asset", Authority: graph.AuthorityAuthoritative,
		Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: "provider", Confidence: 1,
	}}}}

	if _, err := service.RebuildGraph(context.Background(), "scope-root", "connection-1", "graph-invalid", spec.Bundle{Provider: asset.ProviderAliCloud}, []governance.Contributor{contributor}); err == nil {
		t.Fatal("contributor reference outside connection assets was accepted")
	}
	if len(repository.bindings) != 0 {
		t.Fatalf("invalid lifecycle binding was persisted: %+v", repository.bindings)
	}
}

func TestGraphRebuildMergesDuplicateRelationshipEvidenceWithProductPriority(t *testing.T) {
	t.Parallel()

	repository := &graphRepository{assets: []asset.Asset{
		{
			ID: "ecs-1",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-1",
				NativeType: "ACS::ECS::Instance", NativeID: "i-1",
			},
			Normalized: map[string]any{"vpc_id": "vpc-1"},
		},
		{
			ID: "vpc-1",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-1",
				NativeType: "ACS::VPC::VPC", NativeID: "vpc-1",
			},
		},
	}}
	bundle := spec.Bundle{Provider: asset.ProviderAliCloud, Revision: "bundle-1", Specs: []spec.CompiledSpec{{
		Definition: spec.ResourceKindSpec{
			Metadata: spec.Metadata{Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance"},
			Relationships: []spec.RelationshipSpec{{
				Type: "member_of", TargetType: "ACS::VPC::VPC", TargetIDPath: "vpc_id",
			}},
		},
	}}}
	contributor := staticContributor{contribution: governance.Contribution{Relationships: []graph.Relationship{{
		SourceAssetID: "ecs-1", TargetAssetID: "vpc-1", Type: graph.RelationshipMemberOf,
		Source: "hook", Confidence: 0.9, Evidence: map[string]any{"request_id": "hook-request"},
	}}}}

	result, err := governance.NewService(repository, repository).RebuildGraph(
		context.Background(), "scope-root", "connection-1", "graph-1", bundle,
		[]governance.Contributor{contributor},
	)
	if err != nil {
		t.Fatalf("rebuild graph: %v", err)
	}
	if len(result.Relationships) != 1 {
		t.Fatalf("relationships = %#v", result.Relationships)
	}
	relationship := result.Relationships[0]
	if relationship.Source != "product_api" || relationship.Confidence != 1 {
		t.Fatalf("relationship priority = %#v", relationship)
	}
	if got := relationship.Evidence["evidence_sources"]; !reflect.DeepEqual(got, []string{"product_api", "hook"}) {
		t.Fatalf("evidence sources = %#v", got)
	}
	if relationship.Evidence["request_id"] != "hook-request" {
		t.Fatalf("supplemental relationship evidence = %#v", relationship.Evidence)
	}
}
