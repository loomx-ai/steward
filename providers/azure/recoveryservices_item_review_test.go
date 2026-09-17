package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func recoveryItemReviewFixture(t *testing.T) *recoveryServicesFixture {
	t.Helper()
	f := newRecoveryServicesFixture(t)
	policy := f.vault + "/backuppolicies/policy"
	object(f.objects[f.item]["properties"])["policyId"] = policy
	object(f.objects[f.item]["properties"])["policyName"] = "policy"
	f.objects[policy] = map[string]any{"id": policy, "name": "policy", "type": recoveryBackupPolicy, "properties": map[string]any{"backupManagementType": "AzureWorkload", "privatePolicySetting": "must-not-leak"}}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, f.vault+"/backupResourceGuardProxies") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return nil, false
	}
	return f
}

func TestRecoveryItemStableReview(t *testing.T) {
	for _, mode := range []string{"ready", "item-absent", "retained-policy-cleared", "protected", "immutable", "invalid-immutability", "unregistered", "policy-missing", "policy-foreign", "container-missing", "guard-forbidden", "changed-item", "changed-policy", "changed-guard", "omitted-guard"} {
		t.Run(mode, func(t *testing.T) {
			f := recoveryItemReviewFixture(t)
			policy := f.vault + "/backuppolicies/policy"
			known := map[string]any{}
			switch mode {
			case "item-absent":
				delete(f.objects, f.item)
				known["reference_policy"] = policy
			case "retained-policy-cleared":
				p := object(f.objects[f.item]["properties"])
				p["policyId"], p["policyName"] = "", ""
				p["isScheduledForDeferredDelete"] = true
				p["protectionState"] = "ProtectionStopped"
				known["reference_policy"] = policy
			case "protected":
				f.objects[f.item]["tags"] = map[string]any{"steward:protected": "true"}
			case "immutable":
				object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"immutabilitySettings": map[string]any{"state": "Locked"}}
			case "invalid-immutability":
				object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"immutabilitySettings": map[string]any{"state": 123}}
			case "unregistered":
				object(f.objects[f.container]["properties"])["registrationStatus"] = "NotRegistered"
			case "policy-missing":
				delete(f.objects, policy)
			case "policy-foreign":
				object(f.objects[f.item]["properties"])["policyId"] = strings.Replace(policy, "/vaults/vault/", "/vaults/foreign/", 1)
			case "container-missing":
				delete(f.objects, f.container)
			}
			guard := recoveryServicesExample(t, "ResourceGuardProxy_Get")
			guardID := f.vault + "/backupresourceguardproxies/swaggerexample"
			guard["id"] = guardID
			f.objects[guardID] = guard
			base := f.override
			itemReads, policyReads, guardLists := 0, 0, 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, f.item) {
					itemReads++
					if mode == "changed-item" && itemReads > 1 {
						raw := batchClone(f.objects[f.item])
						object(raw["properties"])["unknownConfig"] = "changed"
						return jsonResponse(200, raw, nil), true
					}
				}
				if strings.EqualFold(q.URL.Path, policy) {
					policyReads++
					if mode == "changed-policy" && policyReads > 1 {
						raw := batchClone(f.objects[policy])
						object(raw["properties"])["unknownConfig"] = "changed"
						return jsonResponse(200, raw, nil), true
					}
				}
				if strings.EqualFold(q.URL.Path, f.vault+"/backupResourceGuardProxies") {
					guardLists++
					if mode == "guard-forbidden" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					if mode == "changed-guard" && guardLists > 1 || mode == "omitted-guard" && guardLists == 1 {
						return jsonResponse(200, map[string]any{"value": []any{guard}}, nil), true
					}
				}
				return base(q)
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			review, raw, err := c.recoveryItemReview(t.Context(), f.item, known)
			switch mode {
			case "ready", "item-absent", "retained-policy-cleared", "protected", "immutable", "unregistered", "omitted-guard":
				if err != nil {
					t.Fatal("review rejected", err)
				}
				if (raw == nil) != (mode == "item-absent") || review["protected"] != (mode == "protected" || mode == "immutable") || review["ready"] != (mode != "unregistered") || review["retained"] != (mode == "retained-policy-cleared") {
					t.Fatal("wrong reviewed context", mode)
				}
				if review["reference_policy"] != policy || review["policy_configuration"] == "" {
					t.Fatal("policy dependency lost")
				}
				if mode == "omitted-guard" && len(object(review["guards"])) != 1 {
					t.Fatal("known guard disappeared with list omission")
				}
				wire, _ := json.Marshal(review)
				if strings.Contains(string(wire), "must-not-leak") {
					t.Fatal("private dependency configuration leaked")
				}
				proof := c.recoveryItemProof(f.item, "connection", review)
				if proof == c.recoveryItemProof(f.item, "other", review) {
					t.Fatal("review not bound to connection")
				}
			default:
				if err == nil {
					t.Fatal("unstable/incomplete review accepted")
				}
			}
		})
	}
}
