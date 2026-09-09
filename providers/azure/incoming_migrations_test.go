package azure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func migrationTargetScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets, configuration, target := migrationScenario(t)
	for i, value := range assets {
		if strings.HasPrefix(value.Identity.NativeID, target.Identity.NativeID+"/") {
			raw := s.records[value.Identity.NativeID]
			delete(raw, "tags")
			assets[i] = dnsAsset(t, r, raw)
		}
	}
	return s, r, assets, configuration, target
}

func TestIncomingMigrationTargetCleanupRetainsSourceAndSharesPrerequisite(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "target-only", true: "both-namespaces"}[both], func(t *testing.T) {
			s, r, assets, configuration, target := migrationTargetScenario(t)
			request, input := dnsRequest(t, r, assets, target)
			if both {
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, assets[0].ID)
			}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) > 0 {
				t.Fatalf("incoming migration plan %+v %v", result, err)
			}
			payload, _ := json.Marshal(result)
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
			request = servicePlanRequest(result, assets, target)
			if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != configuration.ID {
				t.Fatalf("target omitted source migration prerequisite %+v", request)
			}
			count := 0
			var configurationStep plan.StepID
			for _, step := range result.Steps {
				if step.Action == "delete" && step.AssetID == configuration.ID {
					count++
					configurationStep = step.ID
				}
				if !both && step.AssetID == assets[0].ID && step.Action == "delete" {
					t.Fatal("target cleanup expanded to source namespace deletion")
				}
			}
			if count != 1 {
				t.Fatalf("shared migration has %d deletes", count)
			}
			for _, step := range result.Steps {
				if step.Action == "delete" && (step.AssetID == target.ID || (both && step.AssetID == assets[0].ID)) && !slices.Contains(step.DependsOn, configurationStep) {
					t.Fatal("namespace does not wait for migration deletion")
				}
			}
			input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{configuration.Identity.NativeID}}}
			if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained incoming migration was deleted implicitly")
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
				t.Fatal("target deleted before migration aborted")
			}
			posts := 0
			properties := object(s.records[configuration.Identity.NativeID]["properties"])
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "POST" {
					if strings.ToLower(req.URL.Path) != configuration.Identity.NativeID+"/revert" || req.URL.Query().Get("api-version") != "2024-01-01" {
						t.Fatalf("unexpected incoming migration write %s", req.URL)
					}
					posts++
					properties["targetNamespace"] = ""
					return jsonResponse(200, nil, nil), true
				}
				return nil, false
			}
			prerequisite := contracts.ActionRequest{Asset: configuration, Action: "delete"}
			preDriver, _ := r.ResolveAction(context.Background(), "connection", configuration)
			op, err := preDriver.Execute(context.Background(), prerequisite)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := preDriver.Wait(context.Background(), prerequisite, op)
			if err != nil || wait.Done || text(wait.Data["phase"]) != "delete" {
				t.Fatalf("migration abort readback %+v %v", wait, err)
			}
			op.Data = wait.Data
			if wait, err := preDriver.Wait(context.Background(), prerequisite, op); err != nil || !wait.Done || posts != 1 {
				t.Fatalf("migration did not finish once %+v %v", wait, err)
			}
			if both {
				sourceRequest := servicePlanRequest(result, assets, assets[0])
				sourceDriver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
				if _, err := sourceDriver.Execute(context.Background(), sourceRequest); err != nil {
					t.Fatal(err)
				}
				for _, impact := range sourceRequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
			}
			// The source configuration's frozen target proof must survive a
			// restart after the configuration itself is gone from native lists.
			payload, _ = json.Marshal(request)
			if err := json.Unmarshal(payload, &request); err != nil {
				t.Fatal(err)
			}
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", target)
			op, err = driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			if wait, err := driver.Wait(context.Background(), request, op); err != nil || !wait.Done {
				t.Fatalf("target cascade readback %+v %v", wait, err)
			}
			if !both {
				for _, value := range assets {
					if value.ID != configuration.ID && strings.HasPrefix(value.Identity.NativeID, assets[0].Identity.NativeID) && s.gone[value.Identity.NativeID] {
						t.Fatalf("source resource removed %s", value.Identity.NativeID)
					}
				}
			}
		})
	}
}

func TestIncomingMigrationRequiresMatchingInventoryProof(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-connection", "wrong-partition", "duplicate", "target-changed", "source-recreated", "missing-source-proof", "missing-target-proof", "missing-config-proof", "missing-parent-proof"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, configuration, target := migrationTargetScenario(t)
			for i := range assets {
				if assets[i].ID != configuration.ID {
					continue
				}
				switch mode {
				case "missing":
					assets = append(assets[:i:i], assets[i+1:]...)
				case "wrong-connection":
					assets[i].Identity.ConnectionID = "other"
				case "wrong-partition":
					assets[i].Identity.Partition = "other"
				case "duplicate":
					duplicate := assets[i]
					duplicate.ID = "duplicate"
					assets = append(assets, duplicate)
				case "target-changed":
					assets[i].Normalized["targetNamespace"] = assets[0].Identity.NativeID
				case "source-recreated":
					object(s.records[assets[0].Identity.NativeID]["properties"])["createdAt"] = "new"
				case "missing-source-proof":
					delete(assets[i].Normalized, "_migration_source_creation")
				case "missing-target-proof":
					delete(assets[i].Normalized, "_migration_target_creation")
				case "missing-config-proof":
					delete(assets[i].Normalized, "_migration_configuration")
				}
				break
			}
			if mode == "missing-parent-proof" {
				for i := range assets {
					if assets[i].ID == target.ID {
						delete(assets[i].Normalized, "_arm_creation_generation")
					}
				}
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			contribution, err := contributor.Contribute(context.Background(), "scope", assets)
			if mode == "missing" || mode == "wrong-connection" || mode == "wrong-partition" {
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, unresolved := range contribution.Unresolved {
					found = found || (unresolved.ControllerID == target.ID && unresolved.NativeID == configuration.Identity.NativeID && unresolved.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true)
				}
				if !found {
					t.Fatal("incoming configuration missing from inventory was ignored")
				}
			} else if err == nil {
				t.Fatal("changed migration proof accepted")
			}
		})
	}
}

func TestIncomingMigrationCannotBeHiddenByEmptyTargetLists(t *testing.T) {
	s, r, assets, configuration, target := migrationTargetScenario(t)
	request, _ := dnsRequest(t, r, assets, target)
	for _, value := range assets {
		if value.Identity.NativeType == serviceBusQueueType && strings.HasPrefix(value.Identity.NativeID, target.Identity.NativeID+"/") {
			if value.Normalized["cleanup_protection_reason"] != "azure_messaging_replication_requires_unpairing" {
				t.Fatal("incoming migration not detected in target entity inventory")
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", value)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil || len(s.deletes) > 0 {
				t.Fatal("target entity was deleted during incoming migration")
			}
		}
	}
	s.gone[configuration.Identity.NativeID] = true
	sourceID := strings.ToLower(resourceID(serviceBusNamespaceType, "new-source"))
	source := map[string]any{"id": sourceID, "type": serviceBusNamespaceType, "properties": map[string]any{"createdAt": "new-source"}}
	s.add(source, "2024-01-01")
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.servicebus/namespaces"] = append(s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.servicebus/namespaces"], source)
	newConfig := map[string]any{"id": sourceID + "/migrationconfigurations/$default", "type": serviceBusMigrationType, "properties": map[string]any{"targetNamespace": target.Identity.NativeID, "migrationState": "Active", "provisioningState": "Succeeded"}}
	s.add(newConfig, "2024-01-01")
	s.lists[sourceID+"/migrationconfigurations"] = []any{newConfig}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
		t.Fatal("new incoming migration was ignored after old prerequisite disappeared")
	}
}

func TestIncomingMigrationDiscoveryReadsCompleteNativeNamespaceSet(t *testing.T) {
	for _, mode := range []string{"pages", "list-403", "list-404", "list-206", "duplicate", "foreign-subscription", "foreign-type", "source-403", "source-404", "source-206", "config-list-403", "config-list-404", "config-list-206", "config-get-404", "source-generation-changed", "new-namespace", "target-not-listed", "self-target", "malformed-target"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, configuration, target := migrationTargetScenario(t)
			collection := "/subscriptions/" + testSubscription + "/providers/microsoft.servicebus/namespaces"
			calls := 0
			switch mode {
			case "pages":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) {
						calls++
						if req.URL.Query().Get("$skiptoken") == "" {
							return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(collection, "2024-01-01") + "&$skiptoken=next"}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": s.lists[collection]}, nil), true
					}
					return nil, false
				}
			case "list-403", "list-404", "list-206":
				s.status[collection] = map[string]int{"list-403": 403, "list-404": 404, "list-206": 206}[mode]
			case "duplicate":
				s.lists[collection] = append(s.lists[collection], s.lists[collection][0])
			case "foreign-subscription":
				s.lists[collection] = append(s.lists[collection], map[string]any{"id": strings.Replace(target.Identity.NativeID, testSubscription, "22222222-2222-2222-2222-222222222222", 1), "type": serviceBusNamespaceType})
			case "foreign-type":
				s.lists[collection] = append(s.lists[collection], map[string]any{"id": resourceID(eventHubNamespaceType, "other"), "type": eventHubNamespaceType})
			case "source-403", "source-404", "source-206":
				s.status[assets[0].Identity.NativeID] = map[string]int{"source-403": 403, "source-404": 404, "source-206": 206}[mode]
			case "config-list-403", "config-list-404", "config-list-206":
				s.status[assets[0].Identity.NativeID+"/migrationconfigurations"] = map[string]int{"config-list-403": 403, "config-list-404": 404, "config-list-206": 206}[mode]
			case "config-get-404":
				s.status[configuration.Identity.NativeID] = 404
			case "source-generation-changed":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, configuration.Identity.NativeID) {
						s.records[assets[0].Identity.NativeID]["etag"] = "namespace-deleting"
					}
					return nil, false
				}
			case "new-namespace":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) {
						calls++
						if calls == 2 {
							return jsonResponse(200, map[string]any{"value": append(slices.Clone(s.lists[collection]), map[string]any{"id": resourceID(serviceBusNamespaceType, "just-created"), "type": serviceBusNamespaceType})}, nil), true
						}
					}
					return nil, false
				}
			case "target-not-listed":
				s.lists[collection] = s.lists[collection][:1]
			case "self-target":
				object(s.records[configuration.Identity.NativeID]["properties"])["targetNamespace"] = assets[0].Identity.NativeID
			case "malformed-target":
				object(s.records[configuration.Identity.NativeID]["properties"])["targetNamespace"] = map[string]any{"invalid": "target"}
			}
			c, _ := r.resolve(context.Background(), "connection")
			incoming, err := c.incomingMigrations(context.Background())
			if mode == "pages" {
				if err != nil || len(incoming[target.Identity.NativeID]) != 1 || calls != 4 {
					t.Fatalf("incomplete namespace pagination %+v %v calls=%d", incoming, err, calls)
				}
				return
			}
			if err == nil {
				t.Fatal("incomplete migration discovery succeeded")
			}
			if mode == "source-404" || mode == "config-list-404" || mode == "config-get-404" || mode == "source-generation-changed" || mode == "new-namespace" || mode == "target-not-listed" {
				var provider *contracts.ProviderCallError
				if !errors.As(err, &provider) || provider.Provider.Category != execution.ErrorRetryable {
					t.Fatalf("changing namespace set is not retryable: %v", err)
				}
			}
		})
	}
}
