package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestDeploymentStackDenyObservation(t *testing.T) {
	t.Run("subscription", func(t *testing.T) { testDeploymentStackDenyObservation(t, false) })
	t.Run("resource_group", func(t *testing.T) { testDeploymentStackDenyObservation(t, true) })
}
func testDeploymentStackDenyObservation(t *testing.T, groupScoped bool) {
	for _, mode := range []string{"removed", "unchanged", "changed", "added", "omitted_present", "listed_missing", "forbidden", "changed_between_reads", "changed_job", "tampered_baseline", "tampered_execution", "duplicate_identity", "root_recreated"} {
		t.Run(mode, func(t *testing.T) {
			c, req := stackDeletePlanFixture(t, groupScoped)
			req.IdempotencyKey = "deny-observation-job"
			member := req.LifecycleImpacts[0].Asset
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			original := denyTestBody(c.root(), testTenant)
			id := strings.ToLower(text(original["id"]))
			added := denyTestBody(c.root(), "12345678-1234-4234-8234-123456789abc")
			addedID := strings.ToLower(text(added["id"]))
			final := false
			calls, lists, owns := 0, 0, 0
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				if q.Method != "GET" {
					t.Fatal("deny observation mutated state", q.Method, q.URL)
				}
				if final {
					calls++
				}
				switch path := strings.ToLower(q.URL.Path); path {
				case req.Asset.Identity.NativeID:
					if final && mode != "root_recreated" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if final {
						object(root["systemData"])["createdAt"] = "2026-09-16T02:00:00Z"
					}
					return jsonResponse(200, root, nil), nil
				case member.Identity.NativeID:
					return jsonResponse(404, map[string]any{}, nil), nil
				case c.root() + "/providers/microsoft.authorization/denyassignments":
					if q.URL.Query().Has("$filter") {
						t.Fatal("deny scope filtered")
					}
					if final {
						lists++
						if mode == "forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
					}
					rows := []any{original}
					if final {
						switch mode {
						case "removed", "omitted_present":
							rows = []any{}
						case "added":
							rows = append(rows, added)
						}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case id:
					if final {
						owns++
						if mode == "removed" || mode == "listed_missing" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						if mode == "changed" || mode == "changed_between_reads" && owns > 1 {
							object(original["properties"])["description"] = "changed-private-description"
						}
					}
					return jsonResponse(200, original, nil), nil
				case addedID:
					return jsonResponse(200, added, nil), nil
				default:
					t.Fatalf("unexpected deny observation request %s", q.URL)
					return nil, nil
				}
			})
			review, err := c.deploymentStackMemberReview(root)
			if err != nil {
				t.Fatal(err)
			}
			req.Asset.Normalized[deploymentStackReviewKey] = review
			req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
			baseline, err := c.deploymentStackCaptureDenies(t.Context(), req)
			if err != nil {
				t.Fatal("capture", err)
			}
			wire, err := json.Marshal(baseline)
			if err != nil || json.Unmarshal(wire, &baseline) != nil {
				t.Fatal("persist baseline", err)
			}
			if strings.Contains(string(wire), "private-") {
				t.Fatal("baseline persisted private deny fields")
			}
			execution, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 204})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "changed_job":
				req.IdempotencyKey = "other-job"
			case "tampered_baseline":
				object(baseline["assignments"])[text(original["id"])] = strings.Repeat("0", 64)
			case "tampered_execution":
				execution["binding"] = "changed"
			case "duplicate_identity":
				object(baseline["assignments"])[strings.ToUpper(text(original["id"]))] = object(baseline["assignments"])[text(original["id"])]
			}
			final = true
			out, err := c.deploymentStackObserveDenyChanges(t.Context(), req, "eastus", execution, baseline)
			success := mode == "removed" || mode == "unchanged" || mode == "changed" || mode == "added" || mode == "omitted_present"
			if !success {
				if err == nil || out.Execution.Operation.Data != nil || len(out.Removed)+len(out.Unchanged)+len(out.Changed)+len(out.Added) != 0 {
					t.Fatal("partial deny evidence escaped", out, err)
				}
				if (strings.HasPrefix(mode, "tampered_") || mode == "changed_job" || mode == "duplicate_identity") && calls != 0 {
					t.Fatal("invalid evidence reached HTTP", calls)
				}
				return
			}
			if err != nil || !out.Execution.ResourcesReconciled || lists != 2 || owns != 2 {
				t.Fatal("deny observation failed", out, err, lists, owns)
			}
			switch mode {
			case "removed":
				if !slices.Equal(out.Removed, []string{id}) || len(out.Unchanged)+len(out.Changed)+len(out.Added) != 0 {
					t.Fatal(out)
				}
			case "unchanged", "omitted_present":
				if !slices.Equal(out.Unchanged, []string{id}) || len(out.Removed)+len(out.Changed)+len(out.Added) != 0 {
					t.Fatal(out)
				}
			case "changed":
				if !slices.Equal(out.Changed, []string{id}) || len(out.Removed)+len(out.Unchanged)+len(out.Added) != 0 {
					t.Fatal(out)
				}
			case "added":
				if !slices.Equal(out.Added, []string{addedID}) || !slices.Equal(out.Unchanged, []string{id}) {
					t.Fatal(out)
				}
			}
		})
	}
}
