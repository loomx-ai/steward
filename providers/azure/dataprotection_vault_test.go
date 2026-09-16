package azure

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDataProtectionVaultGuardProxyReconciliation(t *testing.T) {
	f := newProtectionFixture(t)
	id := f.vault + "/backupresourceguardproxies/proxy"
	// The target guard may legitimately belong to another subscription. Reading
	// its proxy does not grant permission to follow or mutate that external guard.
	guard := "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/security/providers/Microsoft.DataProtection/resourceGuards/guard"
	raw := map[string]any{"id": id, "name": "proxy", "type": dataProtectionGuardProxy, "properties": map[string]any{"resourceGuardResourceId": guard, "description": "private-guard-description"}}
	listed, exists := true, true
	calls := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if !strings.Contains(strings.ToLower(q.URL.Path), "/backupresourceguardproxies") {
			return nil, false
		}
		calls++
		if q.Method != "GET" || q.URL.Query().Get("api-version") != dataProtectionVaultVersion {
			t.Fatal("unexpected guard operation", q.Method, q.URL.Path)
		}
		if strings.EqualFold(q.URL.Path, id) {
			if exists {
				return jsonResponse(200, raw, nil), true
			}
			return jsonResponse(404, map[string]any{}, nil), true
		}
		rows := []any{}
		if listed {
			rows = append(rows, raw)
		}
		return jsonResponse(200, map[string]any{"value": rows}, nil), true
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	known, err := c.dataProtectionGuardProxies(t.Context(), f.vault, nil)
	if err != nil || len(known) != 1 || calls != 2 {
		t.Fatal("native guard review", known, err, calls)
	}
	if known[id] == guard || known[id] == "private-guard-description" {
		t.Fatal("private guard metadata persisted")
	}
	listed = false
	next, err := c.dataProtectionGuardProxies(t.Context(), f.vault, known)
	if err != nil || next[id] != known[id] {
		t.Fatal("omitted guard erased", next, err)
	}
	exists = false
	next, err = c.dataProtectionGuardProxies(t.Context(), f.vault, known)
	if err != nil || len(next) != 0 {
		t.Fatal("own-read absence not reconciled", next, err)
	}
}

func TestDataProtectionVaultGuardProxyRejectsPartialEvidence(t *testing.T) {
	for _, mode := range []string{"forbidden", "parent-missing", "foreign-vault", "invalid-guard", "filtered-page", "duplicate", "partial-read"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionFixture(t)
			id := f.vault + "/backupresourceguardproxies/proxy"
			raw := map[string]any{"id": id, "name": "proxy", "type": dataProtectionGuardProxy, "properties": map[string]any{"resourceGuardResourceId": "/subscriptions/" + testSubscription + "/resourceGroups/security/providers/Microsoft.DataProtection/resourceGuards/guard"}}
			f.override = func(q *http.Request) (*http.Response, bool) {
				if !strings.Contains(strings.ToLower(q.URL.Path), "/backupresourceguardproxies") {
					return nil, false
				}
				if strings.EqualFold(q.URL.Path, id) {
					if mode == "parent-missing" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ParentResourceNotFound"}}, nil), true
					}
					if mode == "partial-read" {
						return jsonResponse(200, map[string]any{"id": id}, nil), true
					}
					return jsonResponse(200, raw, nil), true
				}
				if mode == "forbidden" {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				row := batchClone(raw)
				if mode == "foreign-vault" {
					row["id"] = strings.Replace(id, "/vault/", "/other/", 1)
				}
				if mode == "invalid-guard" {
					object(row["properties"])["resourceGuardResourceId"] = "https://example.com/guard"
				}
				rows := []any{row}
				if mode == "duplicate" {
					rows = append(rows, row)
				}
				body := map[string]any{"value": rows}
				if mode == "parent-missing" {
					body["value"] = []any{}
				}
				if mode == "filtered-page" {
					body["nextLink"] = apiURL(q.URL.Path, dataProtectionVaultVersion) + "&$filter=hidden"
				}
				return jsonResponse(200, body, nil), true
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.dataProtectionGuardProxies(t.Context(), f.vault, map[string]any{id: "known"}); err == nil || isNotFound(err) {
				t.Fatal("partial security dependency accepted", mode, err)
			}
		})
	}
}

func TestDataProtectionNativeGuardProxyTypeAlias(t *testing.T) {
	wire, err := os.ReadFile("fixtures/dataprotection/GetResourceGuardProxy.json")
	if err != nil {
		t.Fatal(err)
	}
	var sample map[string]any
	if err = json.Unmarshal(wire, &sample); err != nil {
		t.Fatal(err)
	}
	raw := object(object(object(sample["responses"])["200"])["body"])
	id := strings.ToLower(text(raw["id"]))
	c := &client{subscription: strings.Split(id, "/")[2]}
	if err = c.dataProtectionMetadata(raw, id, dataProtectionGuardProxy); err != nil {
		t.Fatal("native type alias rejected", err)
	}
	raw = batchClone(raw)
	raw["id"] = strings.Replace(id, "/backupvaults/", "/vaults/", 1)
	if err = c.dataProtectionMetadata(raw, id, dataProtectionGuardProxy); err == nil {
		t.Fatal("type alias changed identity scope")
	}
}

func TestDataProtectionVaultDependenciesKeepRetainedChildren(t *testing.T) {
	f := newProtectionFixture(t)
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(q.URL.Path), "/backupresourceguardproxies") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return nil, false
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	known, err := c.dataProtectionVaultDependencies(t.Context(), f.vault, nil)
	if err != nil || len(object(known["children"])) != 3 {
		t.Fatal("incomplete native vault closure", known, err)
	}
	for _, id := range []string{f.policy, f.instance, f.deletedInstance} {
		f.omitted[id] = true
	}
	next, err := c.dataProtectionVaultDependencies(t.Context(), f.vault, known)
	if err != nil || c.privateConfiguration(next) != c.privateConfiguration(known) {
		t.Fatal("omitted children erased", next, err)
	}
	delete(f.objects, f.instance)
	next, err = c.dataProtectionVaultDependencies(t.Context(), f.vault, known)
	if err != nil || len(object(next["children"])) != 2 || object(object(next["children"])[f.deletedInstance])["kind"] != dataProtectionDeletedInstance {
		t.Fatal("active deletion erased retained instance", next, err)
	}
	delete(f.objects, f.policy)
	next, err = c.dataProtectionVaultDependencies(t.Context(), f.vault, next)
	if err != nil || len(object(next["children"])) != 1 {
		t.Fatal("retained dependency lost", next, err)
	}
	delete(f.objects, f.deletedInstance)
	next, err = c.dataProtectionVaultDependencies(t.Context(), f.vault, next)
	if err != nil || len(object(next["children"])) != 0 {
		t.Fatal("own-read disappearance not reconciled", next, err)
	}
}
func TestDataProtectionVaultDependencyReviewRejectsDrift(t *testing.T) {
	for _, mode := range []string{"vault", "new-child", "policy-forbidden", "retained-forbidden", "foreign-hint"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionFixture(t)
			passes := 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if strings.HasSuffix(path, "/backupresourceguardproxies") {
					passes++
					if passes == 1 && mode == "vault" {
						object(f.objects[f.vault]["properties"])["privateFutureSetting"] = "changed"
					}
					if passes == 1 && mode == "new-child" {
						raw := batchClone(f.objects[f.policy])
						raw["id"], raw["name"] = f.policy+"-new", "policy-new"
						f.objects[f.policy+"-new"] = raw
					}
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				if mode == "policy-forbidden" && path == f.policy || mode == "retained-forbidden" && path == f.vault+"/deletedbackupinstances" {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				return nil, false
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			known := map[string]any{}
			if mode == "foreign-hint" {
				known["children"] = map[string]any{strings.Replace(f.instance, "/vault/", "/other/", 1): map[string]any{"kind": dataProtectionInstance}}
			}
			if _, err = c.dataProtectionVaultDependencies(t.Context(), f.vault, known); err == nil {
				t.Fatal("changing vault closure accepted", mode)
			}
		})
	}
}

func TestDataProtectionRetainedVaultsKeepDeletionIdentities(t *testing.T) {
	f := newProtectionFixture(t)
	second := strings.Replace(f.deletedVault, "deleted-one", "deleted-two", 1)
	raw := batchClone(f.objects[f.deletedVault])
	raw["id"] = strings.Replace(text(raw["id"]), "deleted-one", "deleted-two", 1)
	raw["name"] = "deleted-two"
	f.objects[second] = raw
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	known, err := c.dataProtectionRetainedVaults(t.Context(), f.vault, "eastus", nil)
	if err != nil || len(known) != 2 {
		t.Fatal("retained identities collapsed", known, err)
	}
	f.omitted[f.deletedVault], f.omitted[second] = true, true
	again, err := c.dataProtectionRetainedVaults(t.Context(), f.vault, "eastus", known)
	if err != nil || c.privateConfiguration(again) != c.privateConfiguration(known) {
		t.Fatal("list omission erased tombstones", again, err)
	}
	delete(f.objects, f.deletedVault)
	again, err = c.dataProtectionRetainedVaults(t.Context(), f.vault, "eastus", known)
	if err != nil || len(again) != 1 || again[second] == nil || f.objects[f.vault] == nil {
		t.Fatal("retained identity affected active vault", again, err)
	}
	// A different active vault must not acquire these historical records.
	other := strings.Replace(f.vault, "/vault", "/other", 1)
	if records, err := c.dataProtectionRetainedVaults(t.Context(), other, "eastus", nil); err != nil || len(records) != 0 {
		t.Fatal("foreign vault acquired retained history", records, err)
	}
}
func TestDataProtectionRetainedVaultsRejectUncertainEvidence(t *testing.T) {
	for _, mode := range []string{"list-forbidden", "own-forbidden", "parent-missing", "changed-origin", "foreign-region"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionFixture(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			known, err := c.dataProtectionRetainedVaults(t.Context(), f.vault, "eastus", nil)
			if err != nil {
				t.Fatal(err)
			}
			f.omitted[f.deletedVault] = true
			if mode == "changed-origin" {
				props := object(f.objects[f.deletedVault]["properties"])
				other := f.vault + "-other"
				props["originalBackupVaultId"], props["originalBackupVaultResourcePath"], props["originalBackupVaultName"] = other, other, "vault-other"
			}
			if mode == "foreign-region" {
				known = map[string]any{strings.Replace(f.deletedVault, "/eastus/", "/westus/", 1): map[string]any{}}
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if mode == "list-forbidden" && strings.HasSuffix(path, "/deletedvaults") || mode == "own-forbidden" && path == f.deletedVault {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if mode == "parent-missing" && path == f.deletedVault {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "SubscriptionNotFound"}}, nil), true
				}
				return nil, false
			}
			if _, err = c.dataProtectionRetainedVaults(t.Context(), f.vault, "eastus", known); err == nil || isNotFound(err) {
				t.Fatal("uncertain retention evidence accepted", mode, err)
			}
		})
	}
}
