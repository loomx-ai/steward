package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCommunicationNotificationHubReference(t *testing.T) {
	const hubType = "Microsoft.NotificationHubs/namespaces/notificationHubs"
	hubID := strings.ToLower(resourceID("Microsoft.NotificationHubs/namespaces", "shared") + "/notificationHubs/push")
	for _, mode := range []string{"present", "omitted", "foreign", "changed", "wrong-type", "malformed", "nested-extension", "non-string", "empty", "null"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			id := hubID
			var native any = strings.Replace(id, "notificationhubs/push", "notificationHubs/Push", 1)
			switch mode {
			case "foreign":
				id = strings.Replace(id, testSubscription, testTenant, 1)
				native = id
			case "wrong-type":
				native = f.ids[communicationType]
			case "malformed":
				native = id + "/../push"
			case "nested-extension":
				native = f.ids[communicationType] + "/providers/Microsoft.NotificationHubs/namespaces/shared/notificationHubs/push"
			case "non-string":
				native = map[string]any{"id": id}
			case "empty":
				native = ""
			case "null":
				native = nil
			}
			props := object(f.resources[f.ids[communicationType]]["properties"])
			props["notificationHubId"] = native
			batch, err := f.runtime.List(t.Context(), f.request(communicationType))
			if slices.Contains([]string{"wrong-type", "malformed", "nested-extension", "non-string"}, mode) {
				if err == nil || batch.Complete {
					t.Fatal("invalid native notification-hub reference accepted", batch, err)
				}
				return
			}
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("Communication inventory failed", batch, err)
			}
			refs := object(batch.Items[0].Normalized["_communication_references"])
			var hubs []string
			encoded, _ := json.Marshal(refs[hubType])
			if err := json.Unmarshal(encoded, &hubs); err != nil {
				t.Fatal(err)
			}
			if mode == "empty" || mode == "null" {
				if len(hubs) != 0 {
					t.Fatal("absent hub invented a reference", refs)
				}
				return
			}
			if !slices.Equal(hubs, []string{id}) {
				t.Fatal("native hub reference omitted", refs)
			}
			values := f.assets(t)
			comm := cdnAsset(t, values, communicationType)
			hub := asset.Asset{ID: "shared-hub", Identity: comm.Identity}
			hub.Identity.NativeID, hub.Identity.NativeType = id, hubType
			if mode != "omitted" {
				values = append(values, hub)
			}
			var request contracts.ActionRequest
			if mode == "changed" {
				request = servicePlanRequest(communicationPlan(t, f, values, comm.ID), values, comm)
				props["notificationHubId"] = id + "-other"
			}
			lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
			contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
			if mode == "changed" {
				if err == nil {
					t.Fatal("retargeted hub accepted by graph review")
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", comm)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := driver.Execute(t.Context(), request); err == nil {
					t.Fatal("retargeted hub accepted by native deletion")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "present" {
				if len(contribution.Unresolved) != 0 || !slices.ContainsFunc(contribution.Relationships, func(ref graph.Relationship) bool {
					return ref.SourceAssetID == comm.ID && ref.TargetAssetID == hub.ID && ref.Type == graph.RelationshipUses
				}) {
					t.Fatal("hub lost its independent reference", contribution)
				}
			} else if !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
				return ref.NativeID == id && ref.NativeType == hubType && ref.ControllerID == comm.ID && ref.Relationship == graph.RelationshipUses
			}) {
				t.Fatal("omitted or foreign hub was silently discarded", contribution.Unresolved)
			}
			for _, binding := range contribution.Bindings {
				if binding.ManagedAssetID == hub.ID || binding.ControllerAssetID == hub.ID {
					t.Fatal("hub reference became owned", binding)
				}
			}
			solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{comm.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || len(solved.Steps) != 4 || len(solved.ImpactItems) != 1 {
				t.Fatal("hub reference changed reviewed account cleanup", err, solved)
			}
			for call := range f.calls {
				if strings.HasPrefix(call, "DELETE ") {
					t.Fatal("reference caused an unrequested native action", call)
				}
			}
		})
	}
}

func TestCommunicationEmailIncomingAccountProof(t *testing.T) {
	for _, kind := range []string{communicationEmailType, communicationDomainType} {
		for _, mode := range []string{"linked", "known-list-omission", "known-unlinked", "known-404", "known-403", "configuration-drift", "new-link-during-scan", "malformed-link", "forged-incoming"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newCommunicationFixture(t)
				request := f.request(kind)
				before, err := f.runtime.List(t.Context(), request)
				if err != nil || len(before.Items) != 1 {
					t.Fatal("initial email inventory", before, err)
				}
				item := before.Items[0]
				domain, account := f.ids[communicationDomainType], f.ids[communicationType]
				incoming := object(item.Normalized["_communication_incoming"])
				if len(incoming) != 1 || len(object(incoming[domain])) != 1 || text(object(incoming[domain])[account]) == "" {
					t.Fatal("email scope lost native incoming account", incoming)
				}
				request.KnownNativeIDs = []string{item.NativeID}
				request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
				f.calls = map[string]int{}
				switch mode {
				case "known-list-omission", "known-unlinked", "known-404", "known-403":
					f.omitted[account] = true
					if mode == "known-unlinked" {
						object(f.resources[account]["properties"])["linkedDomains"] = []any{}
					}
					if mode == "known-404" {
						delete(f.resources, account)
					}
					if mode == "known-403" {
						f.override = func(req *http.Request) (*http.Response, bool) {
							if strings.EqualFold(req.URL.Path, account) {
								return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
							}
							return nil, false
						}
					}
				case "configuration-drift", "new-link-during-scan":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, account) && f.calls["GET "+account] == 2 {
							if mode == "configuration-drift" {
								object(f.resources[account]["properties"])["futurePrivateSetting"] = "changed"
							} else {
								object(f.resources[account]["properties"])["linkedDomains"] = []any{}
							}
						}
						return nil, false
					}
				case "malformed-link":
					object(f.resources[account]["properties"])["linkedDomains"] = []any{account}
				case "forged-incoming":
					object(incoming[domain])[account] = "forged"
				}
				batch, err := f.runtime.List(t.Context(), request)
				wantError := mode == "known-403" || mode == "configuration-drift" || mode == "new-link-during-scan" || mode == "malformed-link" || mode == "forged-incoming"
				if wantError {
					if err == nil {
						t.Fatal("unverified domain connections accepted", mode)
					}
					return
				}
				if err != nil || len(batch.Items) != 1 {
					t.Fatal("native incoming rescan failed", batch, err)
				}
				connections := object(object(batch.Items[0].Normalized["_communication_incoming"])[domain])
				want := 1
				if mode == "known-unlinked" || mode == "known-404" {
					want = 0
				}
				if len(connections) != want || f.calls["GET "+account] < 2 {
					t.Fatal("account index omission bypassed its own GET", connections, f.calls["GET "+account])
				}
			})
		}
	}
}
