package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestMonitorNativeScanWorkerAndExecution(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			f := newMonitorInventoryFixture(t, kind)
			if kind == monitorActionGroupType {
				// Exercise the native Event Hub/ITSM indexes through real scan
				// creation, persistence, graph rebuilding and action recovery.
				f = newMonitorReceiverFixture(t).monitorInventoryFixture
			}
			r := f.runtime
			repository, err := sqlite.Open(filepath.Join(t.TempDir(), "monitor.db"), "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
			if err := repository.PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			if err := repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: asset.ProviderAzure, Type: asset.CredentialAzureServicePrincipal, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			root := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
			if err := repository.PutScope(ctx, root); err != nil {
				t.Fatal(err)
			}
			region := "westus"
			for _, raw := range f.objects {
				if location := monitorResourceRegion(kind, raw); location != "global" {
					region = location
					break
				}
			}
			if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "active-region", ConnectionID: connection.ID, RegionID: region, Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			registry := providerruntime.NewRegistry()
			if err := registry.Register(r); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterBundle(r.Bundle()); err != nil {
				t.Fatal(err)
			}
			creator, err := inventory.NewCreator(repository, registry)
			if err != nil {
				t.Fatal(err)
			}
			handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
			scan := func(failure bool) []asset.Asset {
				t.Helper()
				created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "monitor-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{region, "global"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(kind).ID}})
				if err != nil || len(created.Shards) == 0 {
					t.Fatal("monitor scan creator omitted native resource kind", created, err)
				}
				for _, shard := range created.Shards {
					if shard.Source != productInventorySource || !shard.Authoritative {
						t.Fatal("monitor scan lost native authority", shard)
					}
					if budget, _ := monitorBudgetKind(kind); budget != "" {
						scope, err := repository.GetScope(ctx, shard.ScopeID)
						if err != nil || scope.Kind != asset.ScopeGlobal {
							t.Fatal("budget shard acquired a regional scope", scope, err)
						}
					}
				}
				for _, job := range created.Jobs {
					err := handler.Handle(ctx, job)
					if !failure && err != nil {
						t.Fatal("native monitor worker failed", err)
					}
				}
				for _, shard := range created.Shards {
					stored, err := repository.GetScanShard(ctx, shard.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !failure && (stored.Status != asset.ShardSucceeded || !stored.Authoritative || !stored.Coverage.Authoritative) {
						t.Fatal("monitor worker did not complete native coverage", stored)
					}
					if failure && stored.Status == asset.ShardSucceeded {
						t.Fatal("failed native monitor read authorized absence", stored)
					}
				}
				values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
				if err != nil {
					t.Fatal(err)
				}
				return values
			}
			expected := len(f.objects)
			values := scan(false)
			if len(values) != expected {
				t.Fatal("registered monitor worker lost native identities", kind, len(values), expected)
			}
			first := values[0]
			if kind == monitorActionGroupType {
				for _, value := range values {
					if len(object(value.Normalized["_monitor_references"])) != 0 {
						first = value
					}
				}
				if len(object(first.Normalized["_monitor_references"])) != 3 {
					t.Fatal("receiver dependencies did not survive scan persistence")
				}
			}
			before := first.Normalized[monitorConfigurationProof]
			props := object(f.objects[first.Identity.NativeID]["properties"])
			switch kind {
			case monitorConsumptionBudgetType, monitorCostBudgetType:
				for _, notification := range object(props["notifications"]) {
					object(notification)["contactEmails"] = []any{"PRIVATE_UPDATED_MONITOR@example.invalid"}
				}
			case monitorActionGroupType:
				object(array(props["emailReceivers"])[0])["emailAddress"] = "PRIVATE_UPDATED_MONITOR@example.invalid"
			case insightsWebTestType:
				props["Enabled"] = !props["Enabled"].(bool)
			case monitorSmartAlertType:
				props["state"] = "Disabled"
			default:
				props["enabled"] = !props["enabled"].(bool)
			}
			values = scan(false)
			for _, value := range values {
				if value.ID == first.ID {
					first = value
				}
			}
			if first.Normalized[monitorConfigurationProof] == before || !first.Capabilities.Has(asset.CapabilityActionable) || text(first.Normalized[monitorReferencesProof]) == "" {
				t.Fatal("monitor worker failed to refresh frozen action evidence", first.Identity.NativeID)
			}
			wire, _ := json.Marshal(values)
			if strings.Contains(string(wire), "PRIVATE_UPDATED_MONITOR") || strings.Contains(string(wire), "contactEmails") || strings.Contains(string(wire), "CredentialPassword") || strings.Contains(string(wire), "ticketConfiguration") {
				t.Fatal("private monitor configuration entered persisted assets")
			}
			lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			built, err := governance.NewService(repository, repository).RebuildGraph(ctx, root.ID, connection.ID, "monitor-worker", r.bundle, []governance.Contributor{lifecycle})
			if err != nil || len(built.Bindings) != 0 {
				t.Fatal("independent monitor became an owned member", built, err)
			}
			planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{first.ID}})
			if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != first.ID {
				t.Fatal("native monitor could not be selected independently", planned, err)
			}
			request := servicePlanRequest(planned, values, first)
			request.IdempotencyKey = "stored-monitor-worker"
			driver, err := registry.ResolveAction(ctx, connection.ID, first)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(ctx, request)
			if err != nil || !slices.Equal(f.deletes, []string{first.Identity.NativeID}) {
				t.Fatal("worker-projected monitor failed native deletion", result, err, f.deletes)
			}
			wire, _ = json.Marshal(request)
			if err := json.Unmarshal(wire, &request); err != nil {
				t.Fatal(err)
			}
			driver, err = registry.ResolveAction(ctx, connection.ID, request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
				t.Fatal("stored monitor result did not prove root absence", wait, err)
			}
			values = scan(false)
			if len(values) != expected-1 {
				t.Fatal("native monitor absence did not close exactly one asset", len(values), expected-1)
			}
			// Even when another root really disappears, an incomplete native
			// collection cannot close its saved asset. One surviving resource's
			// denied GET invalidates both regional/global observation passes.
			if len(f.objects) > 1 {
				ids := slices.Sorted(maps.Keys(f.objects))
				delete(f.objects, ids[0])
				denied := ids[1]
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, denied) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
				if got := scan(true); len(got) != expected-1 {
					t.Fatal("failed native monitor scan closed saved assets", len(got), expected-1)
				}
			}
		})
	}
}
