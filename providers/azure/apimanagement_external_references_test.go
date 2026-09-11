package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const apimTestClientID = "ceaa6b06-c00f-43ef-99ac-f53d1fe876a0"

func apimExternalScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset, map[string]any, map[string]any) {
	t.Helper()
	s, r, assets := apimScenario(t)
	group := "/subscriptions/" + testSubscription + "/resourcegroups/certificate-in-another-region"
	vault := nativeResource(apimVaultType, "actual-vault-resource", "eastus2", map[string]any{"vaultUri": "https://rpbvtkeyvaultintegration.vault-int.azure-int.net/"})
	identity := nativeResource(apimIdentityType, "certificate-reader", "eastus2", map[string]any{"clientId": apimTestClientID, "principalId": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "tenantId": testTenant})
	for _, raw := range []map[string]any{vault, identity} {
		kind := text(raw["type"])
		raw["id"] = group + "/providers/" + strings.ToLower(kind) + "/" + text(raw["name"])
		raw["systemData"] = map[string]any{"createdAt": "2026-01-27T15:35:05Z"}
		mapping, _ := findType(kind)
		s.add(raw, mapping.Version)
		path := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
		s.lists[path] = []any{raw}
	}
	return s, r, assets, vault, identity
}

func TestAPIMExternalReferencesReachGraphUsingNativeIdentities(t *testing.T) {
	for _, kind := range []string{apimServiceType, apimServiceType + "/namedValues", apimServiceType + "/certificates", apimWorkspaceType + "/namedValues", apimWorkspaceType + "/certificates", apimAPIType + "/policies"} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets, vault, identity := apimExternalScenario(t)
			target := cdnAsset(t, assets, kind)
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			if kind == apimServiceType {
				props["hostnameConfigurations"] = []any{map[string]any{"type": "Proxy", "hostName": "api.example.test", "keyVaultId": "https://rpbvtkeyvaultintegration.vault-int.azure-int.net/secrets/PRIVATE_SECRET_NAME/version", "identityClientId": apimTestClientID}}
			} else if last(kind) == "policies" {
				props["value"] = `<policies><inbound><authentication-managed-identity resource="https://vault.azure.net" client-id="` + strings.ToUpper(apimTestClientID) + `"/></inbound></policies>`
			} else {
				// The original 2024 certificate GET supplies this Key Vault shape.
				props["keyVault"] = object(apimExample(t, "ApiManagementGetCertificateWithKeyVault.json")["properties"])["keyVault"]
			}
			target = dnsAsset(t, r, raw)
			values := []asset.Asset{target, dnsAsset(t, r, vault), dnsAsset(t, r, identity)}
			store := batchReferenceGraph{assets: values}
			result, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "apim-external", r.bundle, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, dependency := range []map[string]any{vault, identity} {
				if last(kind) == "policies" && text(dependency["type"]) == apimVaultType {
					continue
				}
				id := text(dependency["id"])
				if !slices.Contains(stringValues(target.Normalized[referenceKey(text(dependency["type"]))]), id) || !slices.ContainsFunc(result.Relationships, func(ref graph.Relationship) bool {
					return ref.SourceAssetID == target.ID && ref.TargetAssetID == asset.AssetID(id) && ref.Type == graph.RelationshipUses
				}) {
					t.Fatal("external reference did not reach the native resource and application graph", id)
				}
			}
			encoded, _ := json.Marshal([]any{target, result})
			for _, secret := range []string{"PRIVATE_SECRET_NAME", "msitestingCert", "<policies>", "/secrets/"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("external dependency exposed a secret path or policy")
				}
			}
		})
	}
}

func TestAPIMPrivateEndpointProtectionAppliesToDirectAndServiceDeletion(t *testing.T) {
	for _, kind := range []string{apimServiceType, apimServiceType + "/privateEndpointConnections"} {
		for _, mode := range []string{"valid", "locked", "protected", "managed-group", "moved", "recreated", "forbidden", "missing", "partial", "retargeted", "reread"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				s, r, assets := apimScenario(t)
				target := cdnAsset(t, assets, kind)
				connection := cdnAsset(t, assets, apimServiceType+"/privateEndpointConnections")
				id, _ := apimPrivateEndpointID(s.records[connection.Identity.NativeID])
				raw := s.records[id]
				request := contracts.ActionRequest{Asset: target, Action: "delete"}
				if kind == apimServiceType {
					request, _ = dnsRequest(t, r, assets, target)
				}
				switch mode {
				case "locked":
					s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "protected":
					raw["tags"] = map[string]any{"steward:protected": "true"}
				case "managed-group":
					group := strings.Join(strings.Split(id, "/")[:5], "/")
					s.records[group] = map[string]any{"id": group, "managedBy": id}
				case "moved":
					raw["id"] = strings.Replace(id, "/network-rg/", "/moved-network-rg/", 1)
				case "recreated":
					object(raw["properties"])["resourceGuid"] = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
				case "forbidden":
					s.status[id] = 403
				case "missing":
					s.status[id] = 404
				case "partial":
					s.status[id] = 206
				case "retargeted":
					object(object(s.records[connection.Identity.NativeID]["properties"])["privateEndpoint"])["id"] = id + "-other"
				case "reread":
					original, reads := s.handle, 0
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
							reads++
							if reads == 2 {
								object(raw["properties"])["resourceGuid"] = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
							}
						}
						return original(req)
					}
				}
				driver, err := r.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				_, err = driver.Execute(t.Context(), request)
				if mode == "valid" {
					if err != nil || !slices.Equal(s.deletes, []string{target.Identity.NativeID}) || s.gone[id] {
						t.Fatal("reviewed deletion failed or deleted the external endpoint", err, s.deletes)
					}
				} else if err == nil || len(s.deletes) != 0 {
					t.Fatal("changed or protected external endpoint did not prevent deletion", err, s.deletes)
				}
			})
		}
	}
}

func TestAPIMExternalReferenceIndexBoundaries(t *testing.T) {
	for _, kind := range []string{apimVaultType, apimIdentityType} {
		for _, mode := range []string{"paged", "empty", "duplicate", "ambiguous", "foreign-subscription", "wrong-type", "wrong-name", "changed-selector", "recreated", "missing-detail", "denied-detail", "denied-list", "partial-page", "foreign-page"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				s, r, _, vault, identity := apimExternalScenario(t)
				selected, selector := vault, "https://rpbvtkeyvaultintegration.vault-int.azure-int.net"
				if kind == apimIdentityType {
					selected, selector = identity, apimTestClientID
				}
				id := text(selected["id"])
				path := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
				original := s.handle
				switch mode {
				case "empty":
					s.lists[path] = []any{}
				case "duplicate":
					s.lists[path] = []any{selected, selected}
				case "ambiguous":
					other := batchClone(selected)
					other["id"], other["name"] = id+"-other", text(selected["name"])+"-other"
					s.add(other, s.version[id])
					s.lists[path] = append(s.lists[path], other)
				case "foreign-subscription":
					selected["id"] = strings.Replace(id, testSubscription, testTenant, 1)
				case "wrong-type":
					selected["type"] = vmType
				case "wrong-name":
					selected["name"] = "different"
				case "changed-selector", "recreated":
					s.lists[path] = []any{batchClone(selected)}
					if mode == "recreated" {
						object(selected["systemData"])["createdAt"] = "2026-01-28T15:35:05Z"
					} else if kind == apimIdentityType {
						object(selected["properties"])["clientId"] = testTenant
					} else {
						object(selected["properties"])["vaultUri"] = "https://different.vault.azure.net/"
					}
				case "missing-detail":
					s.status[id] = 404
				case "denied-detail":
					s.status[id] = 403
				case "denied-list":
					s.status[path] = 403
				case "paged", "partial-page", "foreign-page":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, path) {
							if mode == "partial-page" {
								return jsonResponse(200, map[string]any{"nextLink": ""}, nil), true
							}
							if mode == "foreign-page" {
								return jsonResponse(200, map[string]any{"value": []any{selected}, "nextLink": "https://outside.example.test/list"}, nil), true
							}
							if req.URL.Query().Get("$skiptoken") == "next" {
								return jsonResponse(200, map[string]any{"value": []any{selected}}, nil), true
							}
							return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": "https://management.azure.com" + path + "?api-version=" + s.version[path] + "&$skiptoken=next"}, nil), true
						}
						return original(req)
					}
				}
				c, _ := r.resolve(t.Context(), "connection")
				ref, err := c.apimExternalReference(t.Context(), kind, selector, map[string][]serviceChild{})
				if mode == "paged" {
					if err != nil || ref != id {
						t.Fatal("paginated resource was not resolved", ref, err)
					}
				} else if mode == "empty" {
					if kind == apimIdentityType {
						selector = "client-id:" + selector
					}
					if err != nil || ref != selector {
						t.Fatal("unknown external reference lost its unresolved selector", ref, err)
					}
				} else if err == nil {
					t.Fatal("unverified external target accepted", ref)
				}
				if len(s.deletes) != 0 {
					t.Fatal("dependency discovery mutated resources")
				}
			})
		}
	}
}

func TestAPIMKeyVaultURLAndIdentitySelectorsDoNotFetchSecretEndpoints(t *testing.T) {
	s, r, assets, _, _ := apimExternalScenario(t)
	target := cdnAsset(t, assets, apimServiceType+"/namedValues")
	c, _ := r.resolve(t.Context(), "connection")
	for _, value := range []string{"http://vault.example.test/secrets/name", "https://user:password@vault.example.test/secrets/name", "https://vault.example.test/secrets/name?sig=secret", "https://vault.example.test/secrets/name#fragment", "https://vault.example.test:8443/secrets/name", "https://vault.example.test/keys/name", "https://vault.example.test/secrets/name%2Fother", "https://vault.example.test/secrets/../name", "https://vault.example.test/secrets/"} {
		object(s.records[target.Identity.NativeID]["properties"])["keyVault"] = map[string]any{"secretIdentifier": value}
		if _, err := r.inventoryItem(t.Context(), c, s.records[target.Identity.NativeID], nil, nil); err == nil {
			t.Fatal("invalid typed Key Vault URL accepted")
		}
	}
	for _, value := range []any{42, "not-a-client-id", "@(context.Variables[\"identity\"])"} {
		object(s.records[target.Identity.NativeID]["properties"])["keyVault"] = map[string]any{"identityClientId": value}
		if _, err := r.inventoryItem(t.Context(), c, s.records[target.Identity.NativeID], nil, nil); err == nil {
			t.Fatal("invalid managed identity selector accepted")
		}
	}
	object(s.records[target.Identity.NativeID]["properties"])["keyVault"] = map[string]any{"identityClientId": nil, "secretIdentifier": "https://unlisted.vault.azure.net/secrets/PRIVATE_SECRET_NAME/version"}
	item, err := r.inventoryItem(t.Context(), c, s.records[target.Identity.NativeID], nil, nil)
	if err != nil || !slices.Equal(stringValues(item.Normalized[referenceKey(apimVaultType)]), []string{"https://unlisted.vault.azure.net"}) || len(stringValues(item.Normalized[referenceKey(apimIdentityType)])) != 0 {
		t.Fatal("unresolved vault or system identity was guessed", err)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "PRIVATE_SECRET_NAME") {
		t.Fatal("unresolved external reference leaked secret name")
	}
	target.Normalized = item.Normalized
	store := batchReferenceGraph{assets: []asset.Asset{target}}
	result, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "apim-unresolved", r.bundle, nil)
	if err != nil || !slices.ContainsFunc(result.Unresolved, func(ref graph.UnresolvedReference) bool {
		return ref.NativeType == apimVaultType && ref.NativeID == "https://unlisted.vault.azure.net"
	}) {
		t.Fatal("unresolved vault reference was dropped by graph construction", err)
	}
}

func TestAPIMMalformedExternalBindingObjectsAreRejected(t *testing.T) {
	s, r, assets, _, _ := apimExternalScenario(t)
	c, _ := r.resolve(t.Context(), "connection")
	for _, kind := range []string{apimServiceType, apimServiceType + "/namedValues", apimWorkspaceType + "/certificates"} {
		for _, value := range []any{"malformed", 42, []any{42}} {
			target := cdnAsset(t, assets, kind)
			raw := batchClone(s.records[target.Identity.NativeID])
			key := "keyVault"
			if kind == apimServiceType {
				key = "hostnameConfigurations"
			}
			object(raw["properties"])[key] = value
			if _, err := r.inventoryItem(t.Context(), c, raw, nil, nil); err == nil {
				t.Fatal("malformed external binding object was ignored", kind)
			}
		}
	}
}

func TestAPIMLoggerManagedIdentityUsesClientIDAndSystemAssignedSentinel(t *testing.T) {
	for _, kind := range []string{apimServiceType + "/loggers", apimWorkspaceType + "/loggers"} {
		for _, selector := range []string{apimTestClientID, "SystemAssigned", "systemAssigned"} {
			t.Run(kind+"/"+selector, func(t *testing.T) {
				s, r, assets, _, identity := apimExternalScenario(t)
				logger := cdnAsset(t, assets, kind)
				raw := s.records[logger.Identity.NativeID]
				object(raw["properties"])["credentials"] = map[string]any{"identityClientId": selector, "endpointAddress": "namespace.servicebus.windows.net", "name": "hub"}
				logger = dnsAsset(t, r, raw)
				refs := stringValues(logger.Normalized[referenceKey(apimIdentityType)])
				if slices.Contains(refs, text(identity["id"])) != (selector == apimTestClientID) {
					t.Fatal("logger identity selector was guessed", refs)
				}
			})
		}
	}
}
