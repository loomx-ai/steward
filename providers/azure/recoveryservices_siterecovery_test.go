package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func siteRecoveryTestItem(vault string) (string, map[string]any) {
	id := vault + "/replicationfabrics/cloud1/replicationprotectioncontainers/cloud_6d224fc6/replicationprotecteditems/f8491e4f-817a-40dd-a90c-af773978c75b"
	return id, map[string]any{"id": id, "name": last(id), "type": siteRecoveryItem, "properties": map[string]any{"friendlyName": "vm1", "protectedItemType": "HyperVVirtualMachine", "protectionState": "Protected", "replicationHealth": "Normal", "activeLocation": "Primary", "policyId": vault + "/replicationpolicies/protectionprofile1", "providerSpecificDetails": map[string]any{"instanceType": "HyperVReplicaAzure", "privateFutureSetting": "must-not-leak"}}}
}

func TestSiteRecoveryOfficialExamples(t *testing.T) {
	for _, name := range []string{"ReplicationProtectedItems_Get", "ReplicationProtectedItems_List"} {
		body := recoveryServicesExample(t, name)
		values := array(body["value"])
		if values == nil {
			values = []any{body}
		}
		for _, value := range values {
			raw := object(value)
			c := &client{subscription: strings.Split(text(raw["id"]), "/")[2]}
			id, err := c.recoveryServicesIdentity(text(raw["id"]), siteRecoveryItem)
			if err != nil {
				t.Fatal(name, err)
			}
			if err = c.recoveryServicesMetadata(raw, id, siteRecoveryItem); err != nil {
				t.Fatal(name, err)
			}
		}
	}
}

func TestSiteRecoveryMetadataBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	id, raw := siteRecoveryTestItem(strings.ToLower(resourceID(recoveryServicesVault, "vault")))
	if err := c.recoveryServicesMetadata(raw, id, siteRecoveryItem); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(id, "/replicationfabrics/cloud1", "", 1), strings.Replace(id, "replicationprotecteditems", "protecteditems", 1), id + "/extra/name"} {
		if _, err := c.recoveryServicesIdentity(bad, siteRecoveryItem); err == nil {
			t.Fatal("accepted invalid Site Recovery identity", bad)
		}
	}
	for _, mode := range []string{"type", "state", "item-type", "policy"} {
		changed := batchClone(raw)
		props := object(changed["properties"])
		switch mode {
		case "type":
			changed["type"] = recoveryServicesItem
		case "state":
			delete(props, "protectionState")
		case "item-type":
			props["protectedItemType"] = ""
		case "policy":
			props["policyId"] = map[string]any{"id": "x"}
		}
		if err := c.recoveryServicesMetadata(changed, id, siteRecoveryItem); err == nil {
			t.Fatal("accepted invalid Site Recovery metadata", mode)
		}
	}
}

func TestSiteRecoveryRegisteredInventory(t *testing.T) {
	for _, mode := range []string{"valid", "paged", "foreign-vault", "filtered", "known-absent", "known-omitted"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryServicesFixture(t)
			id, raw := siteRecoveryTestItem(f.vault)
			f.objects[id] = raw
			collection := f.vault + "/replicationprotecteditems"
			req := recoveryServicesRequest(f, siteRecoveryItem)
			switch mode {
			case "paged", "foreign-vault", "filtered":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if !strings.EqualFold(q.URL.Path, collection) {
						return nil, false
					}
					if q.URL.Query().Get("api-version") != siteRecoveryVersion {
						t.Fatal("wrong Site Recovery API version")
					}
					first := apiURL(collection, siteRecoveryVersion)
					switch {
					case mode == "foreign-vault":
						other := strings.Replace(id, "/vaults/vault/", "/vaults/other/", 1)
						changed := batchClone(raw)
						changed["id"] = other
						return jsonResponse(200, map[string]any{"value": []any{changed}}, nil), true
					case mode == "filtered":
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": first + "&$filter=" + url.QueryEscape("protectionStatus eq 'Protected'")}, nil), true
					case q.URL.Query().Get("skipToken") == "":
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": first + "&skipToken=cloud1"}, nil), true
					default:
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					}
				}
			case "known-absent":
				req.KnownNativeIDs = []string{id}
				delete(f.objects, id)
			case "known-omitted":
				req.KnownNativeIDs = []string{id}
				f.omitted[id] = true
			}
			batch, err := f.runtime.List(t.Context(), req)
			if mode == "foreign-vault" || mode == "filtered" {
				if err == nil {
					t.Fatal("unsafe Site Recovery collection accepted")
				}
				return
			}
			if err != nil || !batch.Complete {
				t.Fatal(err)
			}
			if mode == "known-absent" {
				if len(batch.Items) != 0 || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != id {
					t.Fatal("own absence not reconciled", batch)
				}
				return
			}
			if len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("Site Recovery item lost", len(batch.Items))
			}
			item := batch.Items[0]
			n := item.Normalized
			if item.NativeID != id || item.NativeType != siteRecoveryItem || item.State != "Protected" || item.Location != "eastus" || item.Actionable == nil || *item.Actionable || n["cleanup_protected"] != true || n["cleanup_protection_reason"] != "site_recovery_replication_read_only" {
				t.Fatal("invalid Site Recovery authority", item)
			}
			if n["vaultId"] != f.vault || n["protectionContainerId"] != redisParentID(id) || n["replicationFabricId"] != f.vault+"/replicationfabrics/cloud1" || n["protectedItemType"] != "HyperVVirtualMachine" {
				t.Fatal("Site Recovery hierarchy lost", n)
			}
			if refs, _ := n[referenceKey(recoveryServicesVault)].([]string); len(refs) != 1 || refs[0] != f.vault {
				t.Fatal("vault reference lost")
			}
			if strings.Contains(string(must(json.Marshal(item.Raw))), "must-not-leak") {
				t.Fatal("provider-specific replication settings leaked")
			}
			req.Scope.NativeID = "westus"
			if batch, err = f.runtime.List(t.Context(), req); err != nil || len(batch.Items) != 0 {
				t.Fatal("foreign region leaked", err)
			}
		})
	}
}

func TestSiteRecoveryHasNoCleanupAction(t *testing.T) {
	f := newRecoveryServicesFixture(t)
	kind := f.runtime.resourceKind(siteRecoveryItem)
	if kind.NativeType != siteRecoveryItem || kind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatal("Site Recovery replication exposed a cleanup action", kind)
	}
}
