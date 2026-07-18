package httptransport

import (
	"context"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

type relationshipNeighborhoodRepository struct {
	relationships []graph.Relationship
	bindings      []graph.LifecycleBinding
	queries       [][]asset.AssetID
}

func (r *relationshipNeighborhoodRepository) ListRelationshipsForAsset(
	_ context.Context,
	_ asset.ConnectionID,
	id asset.AssetID,
) ([]graph.Relationship, error) {
	return relationshipsForAssetIDs(r.relationships, []asset.AssetID{id}), nil
}

func (r *relationshipNeighborhoodRepository) ListLifecycleBindingsForAsset(
	_ context.Context,
	_ asset.ConnectionID,
	id asset.AssetID,
) ([]graph.LifecycleBinding, error) {
	return bindingsForAssetIDs(r.bindings, []asset.AssetID{id}), nil
}

func (r *relationshipNeighborhoodRepository) ListRelationshipsByAssetIDs(
	_ context.Context,
	ids []asset.AssetID,
) ([]graph.Relationship, error) {
	r.queries = append(r.queries, slices.Clone(ids))
	return relationshipsForAssetIDs(r.relationships, ids), nil
}

func (r *relationshipNeighborhoodRepository) ListLifecycleBindingsByAssetIDs(
	_ context.Context,
	ids []asset.AssetID,
) ([]graph.LifecycleBinding, error) {
	return bindingsForAssetIDs(r.bindings, ids), nil
}

func TestLoadAssetRelationshipNeighborhoodIncludesPrivateLinkENINetworkContext(t *testing.T) {
	repository := &relationshipNeighborhoodRepository{
		relationships: []graph.Relationship{
			{ID: "endpoint-service", SourceAssetID: "endpoint", TargetAssetID: "service", Type: graph.RelationshipUses},
			{ID: "endpoint-vpc", SourceAssetID: "endpoint", TargetAssetID: "endpoint-vpc", Type: graph.RelationshipMemberOf},
			{ID: "eni-endpoint", SourceAssetID: "eni", TargetAssetID: "endpoint", Type: graph.RelationshipMemberOf},
			{ID: "eni-vpc", SourceAssetID: "eni", TargetAssetID: "eni-vpc", Type: graph.RelationshipMemberOf},
			{ID: "eni-vswitch", SourceAssetID: "eni", TargetAssetID: "eni-vswitch", Type: graph.RelationshipMemberOf},
			{ID: "unrelated-vpc-child", SourceAssetID: "other-instance", TargetAssetID: "endpoint-vpc", Type: graph.RelationshipMemberOf},
		},
		bindings: []graph.LifecycleBinding{{
			ID: "endpoint-manages-eni", ControllerAssetID: "endpoint", ManagedAssetID: "eni",
		}},
	}

	relationships, bindings, err := loadAssetRelationshipNeighborhood(
		context.Background(),
		repository,
		"connection-a",
		"service",
	)
	if err != nil {
		t.Fatalf("load relationship neighborhood: %v", err)
	}

	gotRelationshipIDs := make([]string, len(relationships))
	for index, relationship := range relationships {
		gotRelationshipIDs[index] = string(relationship.ID)
	}
	wantRelationshipIDs := []string{
		"endpoint-service",
		"endpoint-vpc",
		"eni-endpoint",
		"eni-vpc",
		"eni-vswitch",
	}
	if !slices.Equal(gotRelationshipIDs, wantRelationshipIDs) {
		t.Fatalf("relationship IDs = %v, want %v", gotRelationshipIDs, wantRelationshipIDs)
	}
	if len(bindings) != 1 || bindings[0].ID != "endpoint-manages-eni" {
		t.Fatalf("bindings = %+v", bindings)
	}
	for _, query := range repository.queries {
		if slices.Contains(query, asset.AssetID("endpoint-vpc")) ||
			slices.Contains(query, asset.AssetID("eni-vpc")) ||
			slices.Contains(query, asset.AssetID("eni-vswitch")) {
			t.Fatalf("member_of parent should be terminal, queries = %v", repository.queries)
		}
	}
}

func relationshipsForAssetIDs(
	values []graph.Relationship,
	ids []asset.AssetID,
) []graph.Relationship {
	result := make([]graph.Relationship, 0)
	for _, value := range values {
		if slices.Contains(ids, value.SourceAssetID) ||
			slices.Contains(ids, value.TargetAssetID) {
			result = append(result, value)
		}
	}
	return result
}

func bindingsForAssetIDs(
	values []graph.LifecycleBinding,
	ids []asset.AssetID,
) []graph.LifecycleBinding {
	result := make([]graph.LifecycleBinding, 0)
	for _, value := range values {
		if slices.Contains(ids, value.ControllerAssetID) ||
			slices.Contains(ids, value.ManagedAssetID) {
			result = append(result, value)
		}
	}
	return result
}
