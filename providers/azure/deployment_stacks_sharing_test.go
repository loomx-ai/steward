package azure

import (
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Maintainer evidence: https://github.com/Azure/deployment-stacks/issues/15
// These are contract regression scenarios, not recordings or proof of absence
// of other stacks outside the connection's visibility.
func TestDeploymentStackMembershipDoesNotImplyExclusiveOwnership(t *testing.T) {
	for _, scenario := range []string{"same_resource", "parent_and_child"} {
		t.Run(scenario, func(t *testing.T) {
			c, first, member := stackGraphAssets(t)
			second := first
			second.ID = "second-stack"
			second.Identity.NativeID += "second"
			second.Normalized = map[string]any{}
			other := member
			if scenario == "parent_and_child" {
				member.Identity.NativeID = strings.ToLower(resourceID("Microsoft.Sql/servers", "server"))
				member.Identity.NativeType = "Microsoft.Sql/servers"
				other = member
				other.ID = "database"
				other.Identity.NativeID += "/databases/database"
				other.Identity.NativeType = "Microsoft.Sql/servers/databases"
			}
			rawByID := map[string]map[string]any{}
			for _, pair := range []struct {
				stack   *asset.Asset
				managed asset.Asset
			}{{&first, member}, {&second, other}} {
				raw := map[string]any{"id": pair.stack.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": pair.managed.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
				review, err := c.deploymentStackMemberReview(raw)
				if err != nil {
					t.Fatal(err)
				}
				pair.stack.Normalized[deploymentStackReviewKey] = review
				pair.stack.Normalized[deploymentStackProofKey] = c.deploymentStackProof(pair.stack.Identity.NativeID, pair.stack.Identity.ConnectionID, review)
				rawByID[strings.ToLower(pair.stack.Identity.NativeID)] = raw
				rawByID[strings.ToLower(pair.managed.Identity.NativeID)] = map[string]any{"id": pair.managed.Identity.NativeID, "type": pair.managed.Identity.NativeType}
			}
			reads := map[string]int{}
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				if q.Method != "GET" {
					t.Fatal(q.Method)
				}
				id := strings.ToLower(q.URL.Path)
				raw := rawByID[id]
				if raw == nil {
					t.Fatalf("unexpected native read: %s", q.URL.Path)
				}
				reads[id]++
				return jsonResponse(200, raw, nil), nil
			})
			all := []asset.Asset{first, second, member}
			if scenario == "parent_and_child" {
				all = append(all, other)
			}
			for _, pair := range []struct{ stack, managed asset.Asset }{{first, member}, {second, other}} {
				result, err := c.deploymentStackContribution(t.Context(), pair.stack, all)
				if err != nil || len(result.Unresolved) != 0 || len(result.Relationships) != 1 {
					t.Fatal("valid native memberships lost", result, err)
				}
				edge := result.Relationships[0]
				if edge.Type != graph.RelationshipMemberOf || edge.SourceAssetID != pair.managed.ID || edge.TargetAssetID != pair.stack.ID {
					t.Fatal(edge)
				}
				for _, binding := range result.Bindings {
					if binding.Ownership == graph.OwnershipExclusive {
						t.Fatal("flat native membership asserted exclusivity", binding)
					}
				}
			}
			if reads[strings.ToLower(first.Identity.NativeID)] != 2 || reads[strings.ToLower(second.Identity.NativeID)] != 2 {
				t.Fatal("both stack claims must be revalidated", reads)
			}
			expectedReads := 1
			if scenario == "same_resource" {
				expectedReads = 2
			}
			if reads[strings.ToLower(member.Identity.NativeID)] != expectedReads || reads[strings.ToLower(other.Identity.NativeID)] != expectedReads {
				t.Fatal("member own reads missing", reads)
			}
		})
	}
}
