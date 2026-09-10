package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Product collections can contain resources omitted by the generic ARM group
// list. Keep the native example bodies, including group-only budget indexes.
func TestMonitorManagedGroupNativeMembership(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		for _, mode := range []string{"indexed", "unindexed", "late", "denied", "missing-index", "changed-between-passes", "generic-disagrees"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				s, r, values, id := monitorManagedScenario(t, f)
				s.members = []any{} // Native Monitor still lists the actual member.
				if budget, _ := monitorBudgetKind(kind); budget != "" {
					f.groupOnly = true
				}
				contributor, err := r.ClusterLifecycle(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unindexed" {
					graph, err := contributor.Contribute(t.Context(), "scope", values[:2])
					if err != nil || len(graph.Unresolved) != 1 || graph.Unresolved[0].NativeID != id {
						t.Fatal("native member omitted from deletion review", graph, err)
					}
					return
				}
				if mode == "late" {
					raw := f.objects[id]
					delete(f.objects, id)
					request, _ := aksRequest(t, r, values[:2])
					f.objects[id] = raw
					driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := driver.Execute(t.Context(), request); err == nil || s.deletes != 0 || len(f.deletes) != 0 {
						t.Fatal("unreviewed native member reached controller DELETE", err)
					}
					return
				}
				if mode != "indexed" {
					if mode == "generic-disagrees" {
						s.members = []any{f.objects[id]}
					}
					reads := 0
					f.override = func(req *http.Request) (*http.Response, bool) {
						if !strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/"+strings.ToLower(kind)) {
							return nil, false
						}
						// Group-only budgets test the exact group collection.
						if f.groupOnly && strings.ToLower(req.URL.Path) == f.collection {
							return nil, false
						}
						reads++
						if mode == "generic-disagrees" {
							changeManagedMonitor(kind, f.objects[id])
							return nil, false
						}
						if mode == "changed-between-passes" {
							if reads == 1 {
								return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
							}
							return nil, false
						}
						status := 403
						if mode == "missing-index" {
							status = 404
						}
						return jsonResponse(status, map[string]any{}, nil), true
					}
					if graph, err := contributor.Contribute(t.Context(), "scope", values); err == nil || len(graph.Bindings) != 0 {
						t.Fatal("failed/changing native membership acquired ownership", graph, err)
					}
					return
				}
				request, _ := aksRequest(t, r, values)
				var member bool
				for _, impact := range request.LifecycleImpacts {
					member = member || impact.Asset.Identity.NativeID == id && impact.Delete
				}
				if !member {
					t.Fatal("known native member lost its reviewed impact")
				}
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil || s.deletes != 1 || len(f.deletes) != 0 {
					t.Fatal("native omitted member cannot follow its controller", result, err)
				}
				encoded, _ := json.Marshal(request)
				if json.Unmarshal(encoded, &request) != nil {
					t.Fatal("invalid recovered managed request")
				}
				s.clusterGone, s.groupGone = true, true
				driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("group absence hid a natively discovered member", wait, err)
				}
				delete(f.objects, id)
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal("native member absence did not finish recovery", wait, err)
				}
			})
		}
	}
}
