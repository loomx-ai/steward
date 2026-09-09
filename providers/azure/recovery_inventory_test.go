package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func recoveryInventoryScenario(t *testing.T, namespaceType string, bare bool) (*dnsScenario, *Runtime, []map[string]any) {
	t.Helper()
	s := newDNSScenario()
	primaryID := strings.ToLower(resourceID(namespaceType, "primary-ns"))
	secondaryID := strings.Replace(strings.ToLower(resourceID(namespaceType, "secondary-ns")), "/resourcegroups/test/", "/resourcegroups/peer-group/", 1)
	primary := map[string]any{"id": primaryID, "type": namespaceType, "location": "eastus", "properties": map[string]any{"createdAt": "2026-01-01T00:00:00Z", "provisioningState": "Succeeded"}}
	secondary := map[string]any{"id": secondaryID, "type": namespaceType, "location": "westus", "properties": map[string]any{"createdAt": "2026-01-02T00:00:00Z", "provisioningState": "Succeeded"}}
	primaryPartner, secondaryPartner := secondaryID, primaryID
	if bare {
		primaryPartner, secondaryPartner = "SeCoNdArY-Ns", "PrImArY-Ns"
	}
	primaryAlias := map[string]any{"id": primaryID + "/disasterrecoveryconfigs/alias", "type": namespaceType + "/disasterRecoveryConfigs", "properties": map[string]any{"partnerNamespace": primaryPartner, "role": "Primary", "provisioningState": "Succeeded", "pendingReplicationOperationsCount": 0, "alternateName": "alternate"}}
	secondaryAlias := map[string]any{"id": secondaryID + "/disasterrecoveryconfigs/alias", "type": namespaceType + "/disasterRecoveryConfigs", "properties": map[string]any{"partnerNamespace": secondaryPartner, "role": "Secondary", "provisioningState": "Succeeded", "pendingReplicationOperationsCount": 0, "alternateName": "alternate"}}
	raw := []map[string]any{primary, secondary, primaryAlias, secondaryAlias}
	for _, value := range raw {
		s.add(value, "2024-01-01")
	}
	s.lists["/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(namespaceType)] = []any{primary, secondary}
	return s, s.runtime(t), raw
}

func TestRecoveryInventoryFreezesReciprocalNativeAliasViews(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, bare := range []bool{false, true} {
			t.Run(namespace+map[bool]string{false: "/full-id", true: "/bare-name"}[bare], func(t *testing.T) {
				s, r, raw := recoveryInventoryScenario(t, namespace, bare)
				collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(namespace)
				pages := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) && req.URL.Query().Get("$skiptoken") == "" {
						pages++
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(collection, "2024-01-01") + "&$skiptoken=next"}, nil), true
					}
					return nil, false
				}
				primary, secondary := dnsAsset(t, r, raw[2]), dnsAsset(t, r, raw[3])
				for _, pair := range [][2]asset.Asset{{primary, secondary}, {secondary, primary}} {
					own, peer := pair[0], pair[1]
					if own.Normalized["_recovery_peer_alias"] != peer.Identity.NativeID || own.Normalized["_recovery_partner_namespace"] != strings.Join(strings.Split(peer.Identity.NativeID, "/")[:9], "/") || own.Normalized["_recovery_peer_configuration"] != peer.Normalized["_recovery_configuration"] || own.Normalized["_recovery_peer_namespace_creation"] != peer.Normalized["_recovery_namespace_creation"] || own.Normalized["_recovery_peer_role"] != peer.Normalized["role"] {
						t.Fatalf("alias view lost reciprocal proof %+v", own.Normalized)
					}
				}
				if bare && pages != 4 {
					t.Fatalf("bare names did not use complete native discovery: %d", pages)
				}
				if len(s.deletes) != 0 {
					t.Fatal("inventory changed a recovery pairing")
				}
			})
		}
	}
}

func TestRecoveryInventoryRejectsUnprovenPair(t *testing.T) {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"self", "foreign-subscription", "other-provider", "malformed-name", "malformed-partner", "list-403", "list-206", "list-duplicate", "ambiguous-name", "missing-name", "peer-403", "peer-404", "peer-206", "peer-alias-404", "peer-alias-foreign", "peer-alias-wrong-kind", "nonreciprocal", "same-role", "source-recreated", "peer-recreated", "pair-changed-during-read"} {
			t.Run(namespace+"/"+mode, func(t *testing.T) {
				s, r, raw := recoveryInventoryScenario(t, namespace, true)
				collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(namespace)
				properties, peerProperties := object(raw[2]["properties"]), object(raw[3]["properties"])
				switch mode {
				case "self":
					properties["partnerNamespace"] = "primary-ns"
				case "foreign-subscription":
					properties["partnerNamespace"] = strings.Replace(text(raw[1]["id"]), testSubscription, "22222222-2222-2222-2222-222222222222", 1)
				case "other-provider":
					properties["partnerNamespace"] = strings.Replace(text(raw[1]["id"]), "/providers/", "/providers/unexpected/", 1)
				case "malformed-name":
					properties["partnerNamespace"] = "../../secondary"
				case "malformed-partner":
					properties["partnerNamespace"] = map[string]any{"invalid": "secondary-ns"}
				case "list-403", "list-206":
					s.status[collection] = map[string]int{"list-403": 403, "list-206": 206}[mode]
				case "list-duplicate":
					s.lists[collection] = append(s.lists[collection], raw[1])
				case "ambiguous-name":
					s.lists[collection] = append(s.lists[collection], map[string]any{"id": strings.Replace(text(raw[1]["id"]), "/peer-group/", "/another-group/", 1), "type": namespace})
				case "missing-name":
					properties["partnerNamespace"] = "not-listed"
				case "peer-403", "peer-404", "peer-206":
					s.status[text(raw[1]["id"])] = map[string]int{"peer-403": 403, "peer-404": 404, "peer-206": 206}[mode]
				case "peer-alias-404":
					s.status[text(raw[3]["id"])] = 404
				case "peer-alias-foreign":
					raw[3]["id"] = text(raw[3]["id"]) + "-other"
				case "peer-alias-wrong-kind":
					raw[3]["type"] = serviceBusMigrationType
				case "nonreciprocal":
					peerProperties["partnerNamespace"] = "secondary-ns"
				case "same-role":
					peerProperties["role"] = "Primary"
				case "source-recreated", "peer-recreated", "pair-changed-during-read":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, text(raw[3]["id"])) {
							if mode == "source-recreated" {
								object(raw[0]["properties"])["createdAt"] = "new"
							} else if mode == "peer-recreated" {
								object(raw[1]["properties"])["createdAt"] = "new"
							} else {
								// Replace the server resource; the previously fetched
								// input body must still refer to the reviewed pairing.
								s.records[text(raw[2]["id"])] = map[string]any{"id": raw[2]["id"], "type": raw[2]["type"], "properties": map[string]any{"role": "PrimaryNotReplicating", "partnerNamespace": "", "alternateName": "alternate"}}
							}
						}
						return nil, false
					}
				}
				c, _ := r.resolve(context.Background(), "connection")
				if _, err := r.inventoryItem(context.Background(), c, raw[2], nil, nil); err == nil || len(s.deletes) > 0 {
					t.Fatal("unproven recovery peer accepted")
				}
			})
		}
	}
}

func TestOfficialRecoveryBreakPairingRecordings(t *testing.T) {
	for _, tc := range []struct{ product, version, commit, hash string }{
		{"servicebus", "2026-01-01", "f86c78ae126c4f3c8e110a116dde8e6f0cfdc04e", "90e3d97666677de60244020a8ce67cd2163d0132e13a9cd98b5077a544085ead"},
		{"eventhubs", "2026-07-01-preview", "c683a64f397974bae397d77a204e2ae86a908fa0", "a9d623c696b6a238c258d0fa12e503b9fac8cbf94cf73d2c247f9900098e75d7"},
	} {
		t.Run(tc.product, func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/" + tc.product + "-recovery-break-recording.json")
			if err != nil {
				t.Fatal(err)
			}
			var recording map[string]any
			if err := json.Unmarshal(payload, &recording); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(text(recording["source_uri"]), "https://raw.githubusercontent.com/Azure/azure-cli/"+tc.commit+"/") || text(recording["source_sha256"]) != tc.hash || text(recording["recorded_api_version"]) != tc.version {
				t.Fatal("recovery recording provenance changed")
			}
			rows := array(recording["interactions"])
			if len(rows) != 8 {
				t.Fatal("recovery recording omitted a transition")
			}
			var before map[string]any
			for i, value := range rows {
				row := object(value)
				body := text(row["response_body"])
				u, err := url.Parse(text(row["uri"]))
				if err != nil || u.Query().Get("api-version") != tc.version || row["status"] != float64(200) || fmt.Sprintf("%x", sha256.Sum256([]byte(body))) != text(row["response_body_sha256"]) {
					t.Fatal("recorded native response changed")
				}
				if i == 3 {
					if row["method"] != "POST" || !strings.HasSuffix(strings.ToLower(u.Path), "/breakpairing") || body != "" {
						t.Fatal("native BreakPairing response changed")
					}
					continue
				}
				if row["method"] != "GET" {
					t.Fatal("expected native alias readback")
				}
				var raw map[string]any
				if err := json.Unmarshal([]byte(body), &raw); err != nil {
					t.Fatal(err)
				}
				properties := object(raw["properties"])
				if i == 2 {
					before = raw
				}
				if recoveryUnpaired(properties) != (i == 7) {
					t.Fatalf("alias readiness did not follow native state at interaction %v", row["interaction"])
				}
				if i >= 4 && i <= 6 && text(properties["provisioningState"]) != "Accepted" {
					t.Fatal("HTTP 200 was incorrectly treated as completed unpairing")
				}
				if i == 7 && recoveryConfiguration(raw) != recoveryConfiguration(before) {
					t.Fatal("native BreakPairing changed frozen alias configuration")
				}
			}
		})
	}
}
