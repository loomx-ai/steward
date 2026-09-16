package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type protectionFixture struct {
	runtime                                                *Runtime
	objects                                                map[string]map[string]any
	omitted                                                map[string]bool
	override                                               func(*http.Request) (*http.Response, bool)
	vault, policy, instance, deletedInstance, deletedVault string
	calls                                                  int
}

func newProtectionFixture(t *testing.T) *protectionFixture {
	t.Helper()
	f := &protectionFixture{objects: map[string]map[string]any{}, omitted: map[string]bool{}}
	f.vault = strings.ToLower(resourceID(dataProtectionVault, "vault"))
	f.policy = f.vault + "/backuppolicies/policy"
	f.instance = f.vault + "/backupinstances/instance"
	f.deletedInstance = f.vault + "/deletedbackupinstances/instance"
	f.deletedVault = "/subscriptions/" + testSubscription + "/providers/microsoft.dataprotection/locations/eastus/deletedvaults/deleted-one"
	add := func(id, kind string, props map[string]any) {
		f.objects[id] = map[string]any{"id": id, "name": last(id), "type": kind, "properties": props}
	}
	add(f.vault, dataProtectionVault, map[string]any{"provisioningState": "Succeeded", "securitySettings": map[string]any{"softDeleteSettings": map[string]any{"state": "On"}}, "privateFutureSetting": "secret-config"})
	f.objects[f.vault]["location"] = "eastus"
	add(f.policy, dataProtectionPolicy, map[string]any{"objectType": "BackupPolicy", "policyRules": []any{}, "privateFutureSetting": "secret-config"})
	props := map[string]any{"objectType": "BackupInstance", "policyInfo": map[string]any{"policyId": f.policy}, "dataSourceInfo": map[string]any{"resourceID": strings.ToLower(resourceID(diskType, "source")), "resourceName": "private-source"}, "currentProtectionState": "ProtectionConfigured", "datasourceAuthCredentials": map[string]any{"secret": "secret-config"}}
	add(f.instance, dataProtectionInstance, props)
	add(f.deletedInstance, dataProtectionDeletedInstance, maps.Clone(props))
	f.objects[f.deletedInstance]["properties"].(map[string]any)["deletionInfo"] = map[string]any{"deletionTime": "2026-09-01T00:00:00Z"}
	add(f.deletedVault, "Microsoft.DataProtection/deletedBackupVaults", map[string]any{"originalBackupVaultId": f.vault, "originalBackupVaultName": "vault", "originalBackupVaultResourcePath": f.vault, "resourceDeletionInfo": map[string]any{"deletionTime": "2026-09-01T00:00:00Z", "scheduledPurgeTime": "2026-10-01T00:00:00Z"}})
	f.objects[f.deletedVault]["id"] = strings.Replace(f.deletedVault, "/deletedvaults/", "/deletedBackupVaults/", 1)
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		f.calls++
		if q.Method != "GET" {
			t.Fatalf("inventory issued mutation %s", q.Method)
		}
		if f.override != nil {
			if res, ok := f.override(q); ok {
				return res, nil
			}
		}
		path := strings.ToLower(q.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/locations" {
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"name": "eastus"}}}, nil), nil
		}
		if q.URL.Query().Get("api-version") != dataProtectionVersion {
			t.Fatal("wrong native version", q.URL)
		}
		if raw := f.objects[path]; raw != nil {
			return jsonResponse(200, raw, nil), nil
		}
		collection := last(path)
		if slices.Contains([]string{"backupvaults", "backuppolicies", "backupinstances", "deletedbackupinstances", "deletedvaults"}, collection) {
			rows := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.objects)) {
				if f.omitted[id] {
					continue
				}
				if collection == "backupvaults" && f.objects[id]["type"] == dataProtectionVault || strings.EqualFold(id[:strings.LastIndex(id, "/")], path) {
					rows = append(rows, f.objects[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	return f
}
func protectionRequest(f *protectionFixture, kind string) contracts.InventoryRequest {
	k := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: dataProtectionSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}}
}
func TestDataProtectionRegisteredInventoryAndRetainedIdentity(t *testing.T) {
	f := newProtectionFixture(t)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	for kind, id := range map[string]string{dataProtectionVault: f.vault, dataProtectionPolicy: f.policy, dataProtectionInstance: f.instance, dataProtectionDeletedInstance: f.deletedInstance, dataProtectionDeletedVault: f.deletedVault} {
		t.Run(kind, func(t *testing.T) {
			req := protectionRequest(f, kind)
			batch, err := f.runtime.List(ctx, req)
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != id {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			if item.Location != "eastus" || item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protected"] != true {
				t.Fatal("inventory overstated action support", item)
			}
			if (kind == dataProtectionDeletedVault || kind == dataProtectionDeletedInstance) && item.State != "soft_deleted" {
				t.Fatal("lost retained state")
			}
			raw, _ := json.Marshal(item)
			if strings.Contains(string(raw), "secret-config") || strings.Contains(string(raw), "private-source") {
				t.Fatal("inventory leaked workload configuration")
			}
			req.Source = inventorySource
			empty, err := f.runtime.List(ctx, req)
			if err != nil || !empty.Complete || len(empty.Items) != 0 {
				t.Fatal("generic index overwrote native inventory", empty, err)
			}
		})
	}
	// A currently active vault with the original name does not subsume a tombstone.
	req := protectionRequest(f, dataProtectionDeletedVault)
	req.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}
	batch, err := f.runtime.List(ctx, req)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID == f.vault {
		t.Fatal("deleted identity collapsed", batch, err)
	}
	payload, _ := json.Marshal(logs)
	if strings.Contains(string(payload), "secret-config") || strings.Contains(string(payload), "private-source") {
		t.Fatal("native logs leaked credentials or workload settings")
	}
}
func TestDataProtectionKnownOwnReadsAndContinuation(t *testing.T) {
	f := newProtectionFixture(t)
	req := protectionRequest(f, dataProtectionVault)
	second := strings.TrimSuffix(f.vault, "vault") + "vault-two"
	raw := maps.Clone(f.objects[f.vault])
	raw["id"] = second
	raw["name"] = "vault-two"
	f.objects[second] = raw
	missing := strings.TrimSuffix(f.vault, "vault") + "missing"
	req.KnownNativeIDs = []string{f.vault, second, missing}
	f.omitted[second] = true
	req.Limit = 1
	first, err := f.runtime.List(t.Context(), req)
	if err != nil || first.Complete || len(first.Items) != 1 || first.NextCursor == "" || len(first.AbsentNativeIDs) != 0 {
		t.Fatal(first, err)
	}
	req.Cursor = first.NextCursor
	next, err := f.runtime.List(t.Context(), req)
	if err != nil || !next.Complete || len(next.Items) != 1 || len(next.AbsentNativeIDs) != 1 || next.AbsentNativeIDs[0] != missing {
		t.Fatal(next, err)
	}
	object(f.objects[second]["properties"])["privateFutureSetting"] = "changed-private-setting"
	if _, err := f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("cursor ignored private configuration drift")
	}
}
func TestDataProtectionRejectsIncompleteAndForeignEvidence(t *testing.T) {
	for _, fault := range []string{"own-forbidden", "parent-missing", "listed-missing", "duplicate", "foreign-policy", "foreign-page", "page-version", "page-filter", "page-cycle", "partial", "own-async", "changed-parent", "bad-origin", "foreign-hint"} {
		t.Run(fault, func(t *testing.T) {
			f := newProtectionFixture(t)
			req := protectionRequest(f, dataProtectionInstance)
			reads := 0
			if fault == "bad-origin" {
				req = protectionRequest(f, dataProtectionDeletedVault)
				object(f.objects[f.deletedVault]["properties"])["originalBackupVaultId"] = strings.Replace(f.vault, testSubscription, testTenant, 1)
			}
			if fault == "foreign-policy" {
				object(object(f.objects[f.instance]["properties"])["policyInfo"])["policyId"] = strings.Replace(f.policy, "/vault/", "/another/", 1)
			}
			if fault == "foreign-hint" {
				req.KnownNativeIDs = []string{strings.Replace(f.instance, testSubscription, testTenant, 1)}
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if path == f.instance {
					if fault == "own-forbidden" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					if fault == "listed-missing" {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					if fault == "own-async" {
						return jsonResponse(200, f.objects[f.instance], http.Header{"Location": {"https://management.azure.com/pending"}}), true
					}
				}
				if path == f.vault {
					reads++
					if fault == "parent-missing" {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					if fault == "changed-parent" && reads > 1 {
						raw := maps.Clone(f.objects[f.vault])
						raw["location"] = "westus"
						return jsonResponse(200, raw, nil), true
					}
				}
				if path == f.vault+"/backupinstances" {
					body := map[string]any{"value": []any{f.objects[f.instance]}}
					switch fault {
					case "duplicate":
						body["value"] = []any{f.objects[f.instance], f.objects[f.instance]}
					case "foreign-page":
						body["nextLink"] = "https://attacker.example" + q.URL.Path + "?api-version=" + dataProtectionVersion
					case "page-version":
						body["nextLink"] = apiURL(q.URL.Path, "2020-01-01")
					case "page-filter":
						body["nextLink"] = apiURL(q.URL.Path, dataProtectionVersion) + "&$filter=hidden"
					case "page-cycle":
						body["nextLink"] = apiURL(q.URL.Path, dataProtectionVersion)
					case "partial":
						return jsonResponse(206, body, nil), true
					default:
						return nil, false
					}
					return jsonResponse(200, body, nil), true
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), req)
			if err == nil || batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("partial evidence accepted", fault, batch, err)
			}
		})
	}
}
func TestDataProtectionOfficialDeletedVaultExamples(t *testing.T) {
	f := newProtectionFixture(t)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DeletedBackupVaults_Get.json", "DeletedBackupVaults_ListByLocation.json"} {
		raw, err := os.ReadFile("fixtures/dataprotection/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var example map[string]any
		if err = json.Unmarshal(raw, &example); err != nil {
			t.Fatal(err)
		}
		body := object(object(object(example["responses"])["200"])["body"])
		if rows := array(body["value"]); len(rows) > 0 {
			body = object(rows[0])
		}
		// Only the credential subscription is changed for transport replay.
		wire, _ := json.Marshal(body)
		wire = []byte(strings.ReplaceAll(string(wire), "00000000-0000-0000-0000-000000000000", testSubscription))
		if err = json.Unmarshal(wire, &body); err != nil {
			t.Fatal(err)
		}
		id, err := c.dataProtectionIdentity(text(body["id"]), dataProtectionDeletedVault)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.dataProtectionMetadata(body, id, dataProtectionDeletedVault); err != nil {
			t.Fatal(err)
		}
		f.override = func(q *http.Request) (*http.Response, bool) {
			if strings.ToLower(q.URL.Path) == id {
				return jsonResponse(200, body, nil), true
			}
			t.Fatalf("wrong deleted-vault request path: %s", q.URL)
			return nil, false
		}
		if _, err := c.dataProtectionRead(t.Context(), id, dataProtectionDeletedVault); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(id, "/deletedvaults/") {
			t.Fatal("request path did not follow native operation")
		}
	}
}

func TestDataProtectionNativePagesAndSeparateDeletionIdentities(t *testing.T) {
	f := newProtectionFixture(t)
	second := strings.TrimSuffix(f.deletedVault, "deleted-one") + "deleted-two"
	raw := maps.Clone(f.objects[f.deletedVault])
	raw["id"] = strings.Replace(second, "/deletedvaults/", "/deletedBackupVaults/", 1)
	raw["name"] = "deleted-two"
	f.objects[second] = raw
	pages := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(q.URL.Path), "/deletedvaults") {
			pages++
			if q.URL.Query().Get("$skiptoken") == "opaque-token" {
				return jsonResponse(200, map[string]any{"value": []any{f.objects[second]}}, nil), true
			}
			return jsonResponse(200, map[string]any{"value": []any{f.objects[f.deletedVault]}, "nextLink": apiURL(q.URL.Path, dataProtectionVersion) + "&$skiptoken=opaque-token"}, nil), true
		}
		return nil, false
	}
	batch, err := f.runtime.List(t.Context(), protectionRequest(f, dataProtectionDeletedVault))
	if err != nil || !batch.Complete || len(batch.Items) != 2 || pages != 4 {
		t.Fatal(batch, err, pages)
	}
	if batch.Items[0].NativeID == batch.Items[1].NativeID || batch.Items[0].Normalized["originalVaultId"] != batch.Items[1].Normalized["originalVaultId"] {
		t.Fatal("same-name deleted vaults lost independent identities")
	}
	delete(f.objects, f.vault)
	req := protectionRequest(f, dataProtectionVault)
	req.KnownNativeIDs = []string{f.vault}
	active, err := f.runtime.List(t.Context(), req)
	if err != nil || len(active.AbsentNativeIDs) != 1 {
		t.Fatal(active, err)
	}
	retained, err := f.runtime.List(t.Context(), protectionRequest(f, dataProtectionDeletedVault))
	if err != nil || len(retained.Items) != 2 {
		t.Fatal("active absence erased retained vaults", retained, err)
	}
}
func TestDataProtectionExampleSourceIntegrity(t *testing.T) {
	raw, err := os.ReadFile("fixtures/dataprotection/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources struct {
		Examples []struct{ File, Source, SHA256 string }
	}
	if err = json.Unmarshal(raw, &sources); err != nil {
		t.Fatal(err)
	}
	if len(sources.Examples) != 4 {
		t.Fatal("missing official examples")
	}
	for _, example := range sources.Examples {
		raw, err := os.ReadFile("fixtures/dataprotection/" + example.File)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != example.SHA256 || !strings.HasPrefix(example.Source, "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/") || strings.Contains(example.Source, "/main/") {
			t.Fatal("unverified or modified native example", example.File)
		}
	}
}
