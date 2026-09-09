package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func migrationScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := messagingScenario(t, serviceBusNamespaceType)
	targetID := strings.ToLower(resourceID(serviceBusNamespaceType, "premium"))
	target := map[string]any{"id": targetID, "name": "premium", "type": serviceBusNamespaceType, "location": "eastus", "properties": map[string]any{"createdAt": "2026-01-02T00:00:00Z", "provisioningState": "Succeeded"}}
	s.add(target, "2024-01-01")
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.servicebus/namespaces"] = append(s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.servicebus/namespaces"], target)
	for _, collection := range []string{"queues", "topics", "authorizationrules", "disasterrecoveryconfigs", "migrationconfigurations", "privateendpointconnections", "networkrulesets"} {
		s.lists[targetID+"/"+collection] = []any{}
		s.version[targetID+"/"+collection] = "2024-01-01"
	}
	targetAsset := dnsAsset(t, r, target)
	queueID := targetID + "/queues/copied-messages"
	queue := map[string]any{"id": queueID, "type": serviceBusQueueType, "tags": map[string]any{"steward/protected": "true"}, "properties": map[string]any{"createdAt": "2026-01-03T00:00:00Z", "messageCount": 57}}
	s.add(queue, "2024-01-01")
	s.lists[targetID+"/queues"] = []any{queue}
	s.lists[queueID+"/authorizationrules"] = []any{}
	targetQueue := dnsAsset(t, r, queue)
	var configuration asset.Asset
	for i, value := range assets {
		if value.Identity.NativeType != serviceBusMigrationType {
			continue
		}
		raw := s.records[value.Identity.NativeID]
		properties := object(raw["properties"])
		properties["targetNamespace"], properties["postMigrationName"], properties["pendingReplicationOperationsCount"] = targetID, "old-standard-name", 0
		canonical := map[string]any{}
		for k, v := range raw {
			canonical[k] = v
		}
		canonical["id"] = value.Identity.NativeID
		configuration = dnsAsset(t, r, canonical)
		assets[i] = configuration
		// Native list identity need not repeat the GET's historic alias.
		s.lists[value.Identity.NativeID[:strings.LastIndex(value.Identity.NativeID, "/")]] = []any{canonical}
	}
	// Re-inventory entities after pairing, as a real product scan would.
	for i, value := range assets {
		if messagingReplicatedEntity(value.Identity.NativeType) {
			canonical := map[string]any{}
			for k, v := range s.records[value.Identity.NativeID] {
				canonical[k] = v
			}
			canonical["id"], canonical["type"] = value.Identity.NativeID, value.Identity.NativeType
			assets[i] = dnsAsset(t, r, canonical)
		}
	}
	return s, r, append(assets, targetAsset, targetQueue), configuration, targetAsset
}

func TestMigrationAbortPrecedesNamespaceDeleteAndSurvivesRestart(t *testing.T) {
	s, r, assets, configuration, target := migrationScenario(t)
	parent := assets[0]
	request, input := dnsRequest(t, r, assets, parent)
	if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != configuration.ID {
		t.Fatalf("migration was not a separate prerequisite: %+v", request)
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.Asset.ID == target.ID || impact.Asset.ID == configuration.ID {
			t.Fatal("target or migration incorrectly delegated to namespace DELETE")
		}
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{parent.ID: {"retain_resources": []string{configuration.Identity.NativeID}}}
	if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
		t.Fatal("namespace delete silently discarded retained migration")
	}
	parentDriver, _ := r.ResolveAction(context.Background(), "connection", parent)
	if _, err := parentDriver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("namespace deleted before aborting migration")
	}
	childRequest := contracts.ActionRequest{Asset: configuration, Action: "delete", IdempotencyKey: "migration-restart"}
	childDriver, _ := r.ResolveAction(context.Background(), "connection", configuration)
	posts := 0
	properties := object(s.records[configuration.Identity.NativeID]["properties"])
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "POST" {
			return nil, false
		}
		posts++
		if strings.ToLower(req.URL.Path) != configuration.Identity.NativeID+"/revert" || req.URL.Query().Get("api-version") != "2024-01-01" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") == "" {
			t.Fatalf("wrong native migration abort: %s", req.URL)
		}
		properties["migrationState"], properties["provisioningState"] = "Reverting", "Accepted"
		return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"migration-revert"}}), true
	}
	result, err := childDriver.Execute(context.Background(), childRequest)
	if err != nil || posts != 1 || result.ProviderRequestID != "migration-revert" || text(result.Data["phase"]) != "revert_migration" || len(s.deletes) != 0 {
		t.Fatalf("abort result=%+v posts=%d deletes=%v error=%v", result, posts, s.deletes, err)
	}
	encoded, _ := json.Marshal(childRequest)
	if err := json.Unmarshal(encoded, &childRequest); err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	childDriver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", childRequest.Asset)
	for i := 0; i < 2; i++ {
		if wait, err := childDriver.Wait(context.Background(), childRequest, result); err != nil || wait.Done || len(s.deletes) != 0 || posts != 1 {
			t.Fatalf("premature migration deletion %+v %v", wait, err)
		}
		properties["migrationState"], properties["provisioningState"] = "Active", "Succeeded"
	}
	properties["targetNamespace"] = ""
	wait, err := childDriver.Wait(context.Background(), childRequest, result)
	if err != nil || wait.Done || text(wait.Data["phase"]) != "delete" || !slices.Equal(s.deletes, []string{configuration.Identity.NativeID}) || posts != 1 {
		t.Fatalf("configuration deletion %+v %v deletes=%v", wait, err, s.deletes)
	}
	result.Data = wait.Data
	if wait, err := childDriver.Wait(context.Background(), childRequest, result); err != nil || !wait.Done {
		t.Fatalf("configuration readback %+v %v", wait, err)
	}
	s.records[parent.Identity.NativeID]["etag"] = "after-migration-deletion"
	object(s.records[parent.Identity.NativeID]["properties"])["updatedAt"] = "2026-09-09T00:00:00Z"
	parentResult, err := parentDriver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 2 || s.deletes[1] != parent.Identity.NativeID {
		t.Fatalf("parent after abort %v deletes=%v", err, s.deletes)
	}
	for _, impact := range request.LifecycleImpacts {
		s.gone[impact.Asset.Identity.NativeID] = true
	}
	if wait, err := parentDriver.Wait(context.Background(), request, parentResult); err != nil || !wait.Done || s.gone[target.Identity.NativeID] {
		t.Fatalf("parent completion did not retain target %+v %v", wait, err)
	}
	queueID := target.Identity.NativeID + "/queues/copied-messages"
	if s.gone[queueID] || object(s.records[queueID]["properties"])["messageCount"] != 57 {
		t.Fatal("migration abort removed copied target entities or messages")
	}
}

func TestMigrationPreparationRejectsDriftAndUnreadableNamespaces(t *testing.T) {
	for _, mode := range []string{"source-recreated", "target-recreated", "source-missing-created", "target-missing-created", "target-changed", "post-migration-name", "source-protected", "target-protected", "target-locked", "source-403", "target-403", "target-404", "target-206", "target-foreign-id", "target-foreign-type", "target-managed-group", "missing-source-proof", "missing-target-proof", "missing-config-proof", "completing", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, configuration, target := migrationScenario(t)
			source := s.records[assets[0].Identity.NativeID]
			targetRaw := s.records[target.Identity.NativeID]
			properties := object(s.records[configuration.Identity.NativeID]["properties"])
			switch mode {
			case "source-recreated":
				object(source["properties"])["createdAt"] = "new"
			case "target-recreated":
				object(targetRaw["properties"])["createdAt"] = "new"
			case "source-missing-created":
				delete(object(source["properties"]), "createdAt")
			case "target-missing-created":
				delete(object(targetRaw["properties"]), "createdAt")
			case "target-changed":
				properties["targetNamespace"] = resourceID(serviceBusNamespaceType, "other")
			case "post-migration-name":
				properties["postMigrationName"] = "different-alias"
			case "source-protected":
				source["tags"] = map[string]any{"steward/protected": "true"}
			case "target-protected":
				targetRaw["tags"] = map[string]any{"steward/protected": "true"}
			case "target-locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": target.Identity.NativeID + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "source-403":
				s.status[assets[0].Identity.NativeID] = 403
			case "target-403":
				s.status[target.Identity.NativeID] = 403
			case "target-404":
				s.status[target.Identity.NativeID] = 404
			case "target-206":
				s.status[target.Identity.NativeID] = 206
			case "target-foreign-id":
				targetRaw["id"] = resourceID(serviceBusNamespaceType, "other")
			case "target-foreign-type":
				targetRaw["type"] = eventHubNamespaceType
			case "target-managed-group":
				group := strings.Join(strings.Split(target.Identity.NativeID, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "type": groupType, "managedBy": "managed"}
			case "missing-source-proof":
				delete(configuration.Normalized, "_migration_source_creation")
			case "missing-target-proof":
				delete(configuration.Normalized, "_migration_target_creation")
			case "missing-config-proof":
				delete(configuration.Normalized, "_migration_configuration")
			case "completing":
				properties["migrationState"] = "Completing"
			case "unknown":
				properties["migrationState"] = "Unknown"
			}
			writes := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					writes++
					return jsonResponse(200, nil, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", configuration)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: configuration, Action: "delete"}); err == nil || writes != 0 {
				t.Fatalf("unsafe migration write count=%d error=%v", writes, err)
			}
		})
	}
}

func TestDefaultNamespaceAuthorizationRuleProtectionIsSpecific(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		s, r, assets := messagingScenario(t, namespace)
		id := assets[0].Identity.NativeID + "/authorizationrules/custom"
		raw := map[string]any{"id": id, "type": namespace + "/authorizationRules", "name": "custom", "properties": map[string]any{"rights": []any{"Listen"}}}
		s.add(raw, "2024-01-01")
		value := dnsAsset(t, r, raw)
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil || !slices.Equal(s.deletes, []string{id}) {
			t.Fatalf("custom authorization rule not independently deletable: %v", err)
		}
		kind, _ := findType(namespace + "/authorizationRules")
		for _, suffix := range []string{"RootManageSharedAccessKey", "ROOTMANAGESHAREDACCESSKEY/"} {
			raw["id"] = assets[0].Identity.NativeID + "/authorizationRules/" + suffix
			if reason := protectionReason(kind, raw); reason != "azure_messaging_default_authorization_rule" || !controllerOnlyReason(reason) || !serviceIntrinsicChild(namespace, kind.NativeType, reason) {
				t.Fatalf("default protection %s", reason)
			}
			raw["tags"] = map[string]any{"steward/protected": "true"}
			if reason := protectionReason(kind, raw); reason != "azure_protected_tag" || serviceIntrinsicChild(namespace, kind.NativeType, reason) {
				t.Fatal("default rule bypassed explicit protection")
			}
			delete(raw, "tags")
		}
	}
}

func TestMigrationRevertOfficialRecordedTransitions(t *testing.T) {
	payload, err := os.ReadFile("fixtures/servicebus-migration-revert-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture := map[string]any{}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	if text(fixture["source_sha256"]) != "99215f7a9e67f4695983b54bed0310eead7d8b19c6611621b3d3cf25b879cfc6" || !strings.Contains(text(fixture["source_uri"]), "/f86c78ae126c4f3c8e110a116dde8e6f0cfdc04e/") || text(fixture["recorded_api_version"]) != "2026-01-01" {
		t.Fatal("recording provenance changed")
	}
	rows := array(fixture["interactions"])
	if len(rows) != 4 {
		t.Fatal("incomplete recorded migration sequence")
	}
	configurationHash := ""
	for i, value := range rows {
		row := object(value)
		body := text(row["response_body"])
		if fmt.Sprintf("%x", sha256.Sum256([]byte(body))) != text(row["response_body_sha256"]) || row["status"] != float64(200) {
			t.Fatal("native recording body changed")
		}
		u, err := url.Parse(text(row["uri"]))
		if err != nil || u.Query().Get("api-version") != "2026-01-01" {
			t.Fatal("wrong recorded API version")
		}
		if i == 2 {
			if text(row["method"]) != "POST" || !strings.HasSuffix(u.Path, "/migrationConfigurations/$default/revert") || body != "" {
				t.Fatal("native Revert response changed")
			}
			continue
		}
		raw := map[string]any{}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		if text(row["method"]) != "GET" || !validResourceResponse(response{status: 200, data: raw}, strings.ToLower(u.Path), serviceBusMigrationType) {
			t.Fatal("native migration identity rejected")
		}
		properties := object(raw["properties"])
		if migrationReady(properties) != (i != 0) || (text(properties["targetNamespace"]) == "") != (i == 3) {
			t.Fatal("native migration state interpreted incorrectly")
		}
		if configurationHash == "" {
			configurationHash = migrationConfiguration(raw)
		}
		if configurationHash != migrationConfiguration(raw) {
			t.Fatal("Revert unexpectedly changed frozen configuration")
		}
	}
}

func TestMigrationPreparationWaitsForSynchronizationAndRechecksFailure(t *testing.T) {
	for _, initial := range []string{"Initiating", "Syncing", "Reverting", "Active"} {
		t.Run(initial, func(t *testing.T) {
			s, r, _, configuration, _ := migrationScenario(t)
			properties := object(s.records[configuration.Identity.NativeID]["properties"])
			properties["migrationState"], properties["provisioningState"], properties["pendingReplicationOperationsCount"] = initial, "Accepted", 1
			driver, _ := r.ResolveAction(context.Background(), "connection", configuration)
			request := contracts.ActionRequest{Asset: configuration, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || text(result.Data["phase"]) != "await_migration" || len(s.deletes) != 0 {
				t.Fatalf("initial migration state %+v %v", result, err)
			}
			posts := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "POST" {
					posts++
					return jsonResponse(200, nil, nil), true
				}
				return nil, false
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done || posts != 0 {
				t.Fatalf("premature revert %+v %v", wait, err)
			}
			properties["migrationState"], properties["provisioningState"], properties["pendingReplicationOperationsCount"] = "Active", "Succeeded", 0
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done || posts != 1 || text(wait.Data["phase"]) != "revert_migration" || len(s.deletes) != 0 {
				t.Fatalf("ready migration %+v %v", wait, err)
			}
			result.Data = wait.Data
			properties["migrationState"] = "Completing"
			if wait, err := driver.Wait(context.Background(), request, result); err == nil || wait.Done || posts != 1 || len(s.deletes) != 0 {
				t.Fatalf("external commit accepted %+v %v", wait, err)
			}
		})
	}
}

func TestMigrationAbortFailureDoesNotDeleteConfiguration(t *testing.T) {
	for _, status := range []int{206, 400, 403, 404, 409, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, r, _, configuration, _ := migrationScenario(t)
			posts := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "POST" {
					posts++
					return jsonResponse(status, nil, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", configuration)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: configuration, Action: "delete"}); err == nil || posts != 1 || len(s.deletes) != 0 {
				t.Fatalf("failed Revert accepted %v", err)
			}
		})
	}
}

func TestMigrationAbortReadbackRequiresSameRetainedTarget(t *testing.T) {
	for _, mode := range []string{"target-404", "target-403", "target-recreated", "configuration-404", "configuration-foreign", "phase-target", "phase-kind", "operation-url", "config-proof", "target-changed", "negative-pending", "fractional-pending", "string-pending"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _, configuration, target := migrationScenario(t)
			request := contracts.ActionRequest{Asset: configuration, Action: "delete"}
			driver, _ := r.ResolveAction(context.Background(), "connection", configuration)
			result := contracts.ActionResult{Data: map[string]any{"phase": "revert_migration", "target": configuration.Identity.NativeID, "operation": "", "polling": "status"}}
			properties := object(s.records[configuration.Identity.NativeID]["properties"])
			properties["targetNamespace"] = ""
			switch mode {
			case "target-404":
				s.status[target.Identity.NativeID] = 404
			case "target-403":
				s.status[target.Identity.NativeID] = 403
			case "target-recreated":
				object(s.records[target.Identity.NativeID]["properties"])["createdAt"] = "new"
			case "configuration-404":
				s.status[configuration.Identity.NativeID] = 404
			case "configuration-foreign":
				s.records[configuration.Identity.NativeID]["id"] = target.Identity.NativeID + "/migrationConfigurations/$default"
			case "phase-target":
				result.Data["target"] = target.Identity.NativeID
			case "phase-kind":
				driver, _ = r.ResolveAction(context.Background(), "connection", target)
			case "operation-url":
				result.Data["operation"] = "https://untrusted.example.com/operation"
			case "config-proof":
				delete(request.Asset.Normalized, "_migration_configuration")
			case "target-changed":
				properties["targetNamespace"] = resourceID(serviceBusNamespaceType, "different")
			case "negative-pending":
				properties["pendingReplicationOperationsCount"] = -1
			case "fractional-pending":
				properties["pendingReplicationOperationsCount"] = 0.5
			case "string-pending":
				properties["pendingReplicationOperationsCount"] = "0"
			}
			if wait, err := driver.Wait(context.Background(), request, result); err == nil || wait.Done || len(s.deletes) != 0 {
				t.Fatalf("unsafe migration resumed %+v %v", wait, err)
			} else {
				var call *contracts.ProviderCallError
				if errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
					t.Fatal("preparation dependency 404 could complete the cleanup worker")
				}
			}
		})
	}
}

func TestMigrationRetainedTarget404CannotCompleteReadback(t *testing.T) {
	s, r, _, configuration, target := migrationScenario(t)
	s.gone[configuration.Identity.NativeID], s.gone[target.Identity.NativeID] = true, true
	driver, _ := r.ResolveAction(context.Background(), "connection", configuration)
	request := contracts.ActionRequest{Asset: configuration, Action: "delete"}
	for _, read := range []func() error{
		func() error { _, err := driver.Preflight(context.Background(), request); return err },
		func() error { _, err := driver.Readback(context.Background(), request); return err },
		func() error {
			_, err := driver.Wait(context.Background(), request, contracts.ActionResult{})
			return err
		},
	} {
		err := read()
		var call *contracts.ProviderCallError
		if err == nil || !errors.As(err, &call) || call.Provider.Category == execution.ErrorNotFound {
			t.Fatalf("retained target loss interpreted as successful deletion %v", err)
		}
	}
}

func TestMigrationProductInventoryPreservesSourceAndTargetDependencies(t *testing.T) {
	_, r, assets, configuration, target := migrationScenario(t)
	request := productRequest(r, serviceBusMigrationType)
	var items []contracts.InventoryItem
	for i := 0; i < 4; i++ {
		batch, err := r.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(items) != 1 || items[0].NativeID != configuration.Identity.NativeID {
		t.Fatalf("migration product inventory %+v", items)
	}
	want := []string{assets[0].Identity.NativeID, target.Identity.NativeID}
	slices.Sort(want)
	got, _ := items[0].Normalized[referenceKey(serviceBusNamespaceType)].([]string)
	if !slices.Equal(got, want) || !slices.Equal(items[0].NetworkReferences, want) {
		t.Fatalf("parent enrichment replaced target namespace reference: normalized=%v references=%v", got, items[0].NetworkReferences)
	}
}
