package azure

import (
	"context"
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestCognitiveNativeLocationUsesDetailResponse(t *testing.T) {
	for _, kind := range []string{cognitiveProjectType, cognitivePlanType, cognitivePECType, cognitiveApplicationType} {
		for _, location := range []string{"", "West US"} {
			t.Run(kind+"/"+location, func(t *testing.T) {
				s, r, assets := cognitiveScenario(t)
				target := cdnAsset(t, assets, kind)
				raw := s.records[target.Identity.NativeID]
				delete(raw, "location")
				if location != "" {
					raw["location"] = location
				}
				batch, err := r.List(context.Background(), productRequest(r, kind))
				if err != nil || !batch.Complete || len(batch.Items) != 1 {
					t.Fatal("native location discovery", err)
				}
				item := batch.Items[0]
				want := "eastus"
				if location != "" {
					want = "westus"
				}
				if item.Location != want || item.Normalized["_cognitive_native_location"] != cognitiveNativeLocation(raw) {
					t.Fatal("native and inherited locations were conflated", item.Location, item.Normalized["_cognitive_native_location"])
				}
				target.Location, target.Normalized = item.Location, item.Normalized
				if err := cognitiveIncarnation(target, raw); err != nil {
					t.Fatal("unchanged native location rejected", err)
				}
				if kind == cognitivePlanType {
					driver, _ := r.ResolveAction(context.Background(), "connection", target)
					if ready, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err != nil || !ready.Allowed {
						t.Fatal("inherited location prevented native cleanup", ready, err)
					}
				}
				raw["location"] = "northeurope"
				if err := cognitiveIncarnation(target, raw); err == nil {
					t.Fatal("changed native child location accepted")
				}
			})
		}
	}
}

func TestCognitiveMalformedReferenceNamesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		kind, field, nested string
	}{
		{cognitiveDeploymentType, "parentDeploymentName", ""},
		{cognitiveDeploymentType, "spilloverDeploymentName", ""},
		{cognitiveDeploymentType, "raiPolicyName", ""},
		{cognitivePolicyType, "customBlocklists", "blocklistName"},
		{cognitiveToolType, "projectScopes", "project"},
		{cognitiveType, "associatedProjects", "array"},
		{cognitiveType, "defaultProject", ""},
	} {
		for _, value := range []any{23, " project-one "} {
			t.Run(tc.field+"/"+text(value), func(t *testing.T) {
				s, r, assets := cognitiveScenario(t)
				target := cdnAsset(t, assets, tc.kind)
				props := object(s.records[target.Identity.NativeID]["properties"])
				switch tc.nested {
				case "":
					props[tc.field] = value
				case "array":
					props[tc.field] = []any{value}
				default:
					props[tc.field] = []any{map[string]any{tc.nested: value}}
				}
				if _, err := r.List(context.Background(), productRequest(r, tc.kind)); err == nil {
					t.Fatal("malformed native reference was silently normalized")
				}
			})
		}
	}
}

func TestCognitiveKeyVaultConnectionWaitsForAllConnections(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	account := cdnAsset(t, assets, cognitiveType)
	keyVaultID := account.Identity.NativeID + "/connections/secret-store"
	raw := map[string]any{"id": keyVaultID, "type": cognitiveConnectionType, "properties": map[string]any{"category": "AzureKeyVault", "authType": "ManagedIdentity", "target": "https://vault.vault.azure.net"}}
	s.add(raw, "2026-05-01")
	s.lists[account.Identity.NativeID+"/connections"] = append(s.lists[account.Identity.NativeID+"/connections"], raw)
	target := dnsAsset(t, r, raw)
	assets = append(assets, target)
	request, input := dnsRequest(t, r, assets, target)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) > 0 {
		t.Fatal("Key Vault connection plan", err, solved.Blockers)
	}
	for _, kind := range []string{cognitiveConnectionType, cognitiveProjectConnectionType} {
		dependent := cdnAsset(t, assets, kind)
		if !slices.ContainsFunc(request.PrerequisiteDeletions, func(impact contracts.ActionImpact) bool { return impact.Asset.ID == dependent.ID }) {
			t.Fatal("Key Vault connection omitted a secret consumer", kind)
		}
		input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{dependent.Identity.NativeID}}}
		retained, err := plan.Solve(input)
		if err != nil || len(retained.Blockers) == 0 {
			t.Fatal("retained secret consumer permitted Key Vault connection deletion", err)
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("Key Vault connection bypassed surviving secret consumers")
	}
	// Model successful prerequisite readback; the full account scenario exercises
	// individual native actions. The external Key Vault is never a deletion impact.
	for _, step := range solved.Steps {
		if step.AssetID != target.ID {
			s.gone[string(step.AssetID)] = true
		}
	}
	object(s.records[account.Identity.NativeID]["properties"])["associatedProjects"] = []any{}
	object(s.records[account.Identity.NativeID]["properties"])["defaultProject"] = ""
	if _, err := driver.Execute(context.Background(), request); err != nil || !slices.Equal(s.deletes, []string{keyVaultID}) {
		t.Fatal("Key Vault connection cleanup after prerequisite absence", err, s.deletes)
	}
}

func TestCognitiveConnectionPrivateEndpointEffectsRemainBlocked(t *testing.T) {
	for _, kind := range []string{cognitiveConnectionType, cognitiveProjectConnectionType} {
		for _, tc := range []struct {
			field string
			value any
		}{{"peRequirement", "Required"}, {"peStatus", "Active"}, {"peRequirement", "FutureMode"}, {"peStatus", 23}} {
			t.Run(kind+"/"+tc.field+"/"+text(tc.value), func(t *testing.T) {
				s, r, assets := cognitiveScenario(t)
				target := cdnAsset(t, assets, kind)
				raw := s.records[target.Identity.NativeID]
				object(raw["properties"])[tc.field] = tc.value
				target = dnsAsset(t, r, raw)
				if target.Normalized["cleanup_protection_reason"] == nil || target.Normalized["cleanup_protected"] != true || target.Normalized["cleanup_controller_only"] == true {
					t.Fatal("unmodeled private endpoint effects were treated as owned cleanup")
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
					t.Fatal("unmodeled connection private endpoint effects authorized deletion")
				}
				assets[slices.IndexFunc(assets, func(value asset.Asset) bool { return value.ID == target.ID })] = target
				_, input := dnsRequest(t, r, assets, cdnAsset(t, assets, cognitiveType))
				// The application projects cleanup_protected into plan protections.
				input.Protections = []plan.ProtectionPolicy{{AssetID: target.ID, Protected: true}}
				result, err := plan.Solve(input)
				if err != nil || len(result.Blockers) == 0 {
					t.Fatal("parent plan bypassed protected connection", err)
				}
			})
		}
	}
}

func TestCognitiveDeleteFailuresAndRestartedOperationIdentity(t *testing.T) {
	for _, kind := range []string{cognitivePlanType} {
		for _, mode := range []string{"async", "forbidden", "conflict", "partial-delete", "failed", "canceled", "partial-poll", "wrong-receipt", "wrong-operation", "wrong-id", "wrong-name", "wrong-resource", "wrong-region", "wrong-subscription", "wrong-provider"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := cognitiveScenario(t)
				target := cdnAsset(t, assets, kind)
				// Native product discovery inherits the proxy's root location.
				target.Location = resourceRegion(s.records[redisParentID(target.Identity.NativeID)])
				request := contracts.ActionRequest{Action: "delete", Asset: target}
				version := "2026-05-01"
				operation := "/subscriptions/" + testSubscription + "/providers/Microsoft.CognitiveServices/locations/" + target.Location + "/operationStatuses/delete-link"
				endpoint := apiURL(operation, version)
				state := "InProgress"
				polls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
						if mode == "forbidden" || mode == "conflict" || mode == "partial-delete" {
							status := 403
							if mode == "conflict" {
								status = 409
							} else if mode == "partial-delete" {
								status = 206
							}
							return jsonResponse(status, map[string]any{}, nil), true
						}
						returned := endpoint
						switch mode {
						case "wrong-region":
							returned = apiURL(strings.Replace(operation, "/locations/"+target.Location+"/", "/locations/unrelated/", 1), version)
						case "wrong-subscription":
							returned = strings.Replace(endpoint, testSubscription, testTenant, 1)
						case "wrong-provider":
							returned = strings.Replace(endpoint, "Microsoft.CognitiveServices", "Microsoft.Other", 1)
						}
						return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {returned}}), true
					}
					if strings.EqualFold(req.URL.Path, operation) {
						polls++
						body := map[string]any{"id": operation, "name": last(operation), "resourceId": target.Identity.NativeID, "status": state}
						switch mode {
						case "partial-poll":
							return jsonResponse(206, body, nil), true
						case "wrong-id":
							body["id"] = operation + "other"
						case "wrong-name":
							body["name"] = "another-operation"
						case "wrong-resource":
							body["resourceId"] = target.Identity.NativeID + "other"
						}
						return jsonResponse(200, body, nil), true
					}
					return nil, false
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				result, err := driver.Execute(context.Background(), request)
				if slices.Contains([]string{"forbidden", "conflict", "partial-delete", "wrong-region", "wrong-subscription", "wrong-provider"}, mode) {
					if err == nil || len(s.deletes) != 1 || polls != 0 {
						t.Fatal("invalid native delete response accepted", err)
					}
					return
				}
				if err != nil {
					t.Fatal("native asynchronous delete", err)
				}
				payload, _ := json.Marshal(request)
				json.Unmarshal(payload, &request)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
				switch mode {
				case "wrong-receipt":
					result.Data["cognitive_operation_binding"] = "another-resource"
				case "wrong-operation":
					u, _ := url.Parse(result.ProviderOperationID)
					u.Path += "other"
					result.ProviderOperationID = u.String()
				case "failed":
					state = "Failed"
				case "canceled":
					state = "Canceled"
				}
				wait, err := driver.Wait(context.Background(), request, result)
				if mode != "async" {
					if err == nil || wait.Done || ((mode == "wrong-receipt" || mode == "wrong-operation") && polls != 0) {
						t.Fatal("invalid resumed Cognitive operation accepted", mode, err)
					}
					return
				}
				if err != nil || wait.Done || polls != 1 {
					t.Fatal("pending Cognitive operation", err)
				}
				state = "Succeeded"
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatal("successful Cognitive operation hid a live target", err)
				}
				s.gone[target.Identity.NativeID] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("completed resumed Cognitive operation", err)
				}
			})
		}
	}
}

func TestCognitivePagedConnectionsBindAncestorsAndIncludeDatastores(t *testing.T) {
	for _, mode := range []string{"complete", "cycle", "drop-include", "false-include", "duplicate-include", "add-filter", "ancestor-change", "version", "host", "subscription", "collection", "parent-change", "parent-location", "ancestor-location", "partial", "denied", "missing-array", "duplicate", "foreign-child", "wrong-region"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cognitiveScenario(t)
			target := cdnAsset(t, assets, cognitiveProjectConnectionType)
			parentID := redisParentID(target.Identity.NativeID)
			raw := s.records[target.Identity.NativeID]
			payload, _ := json.Marshal(raw)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = target.Identity.NativeID+"2", last(target.Identity.NativeID)+"2"
			s.add(second, "2026-05-01")
			collection := parentID + "/connections"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				if mode == "partial" || mode == "denied" {
					status := 206
					if mode == "denied" {
						status = 403
					}
					return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
				}
				if mode == "missing-array" {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if mode == "duplicate" {
					return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
				}
				if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
				}
				next := apiURL(collection, "2026-05-01") + "&includeAll=true&%24skiptoken=second"
				switch mode {
				case "drop-include":
					next = strings.Replace(next, "&includeAll=true", "", 1)
				case "false-include":
					next = strings.Replace(next, "includeAll=true", "includeAll=false", 1)
				case "duplicate-include":
					next += "&includeAll=false"
				case "add-filter":
					next += "&category=AzureBlob"
				case "version":
					next = strings.Replace(next, "2026-05-01", "1900-01-01", 1)
				case "host":
					next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
				case "subscription":
					next = strings.Replace(next, testSubscription, testTenant, 1)
				case "collection":
					next = strings.Replace(next, "/connections", "/sharedprivatelinkresources", 1)
				case "foreign-child":
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": strings.Replace(target.Identity.NativeID, "/account-one/", "/unrelated-account/", 1), "type": cognitiveProjectConnectionType}}}, nil), true
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
			}
			request := productRequest(r, cognitiveProjectConnectionType)
			if mode == "wrong-region" {
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "unrelatedregion"}
			}
			first, err := r.List(context.Background(), request)
			if !slices.Contains([]string{"complete", "cycle", "parent-change", "ancestor-change", "parent-location", "ancestor-location", "wrong-region"}, mode) {
				if err == nil {
					t.Fatal("incomplete Cognitive collection accepted", mode)
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatal("first native Cognitive page", err)
			}
			if mode == "ancestor-change" {
				object(s.records[redisRootID(parentID)]["properties"])["customSubDomainName"] = "changed"
			}
			if mode == "parent-change" {
				object(s.records[parentID]["properties"])["replicaCount"] = 7
			}
			if mode == "parent-location" {
				s.records[parentID]["location"] = "westus"
			}
			if mode == "ancestor-location" {
				s.records[redisRootID(parentID)]["location"] = "westus"
			}
			request.Cursor = first.NextCursor
			final, err := r.List(context.Background(), request)
			if slices.Contains([]string{"cycle", "parent-change", "ancestor-change", "parent-location", "ancestor-location"}, mode) {
				if err == nil {
					t.Fatal("changed Cognitive continuation accepted")
				}
				return
			}
			count := 2
			if mode == "wrong-region" {
				count = 0
			}
			if err != nil || !final.Complete || len(first.Items)+len(final.Items) != count {
				t.Fatal("native Cognitive paging", err)
			}
		})
	}
}

func TestCognitiveAncestorProtectionAndPrivateDrift(t *testing.T) {
	for _, mode := range []string{"child-config", "child-private", "child-property-tag", "child-state", "child-location", "parent-config", "parent-private", "parent-tag", "parent-state", "parent-location", "project-location", "root-config", "root-private", "root-internal-id", "root-created", "root-default", "root-project-index", "root-state", "root-identity", "root-partial", "root-forbidden", "root-lock", "root-managed-group", "missing-ancestor", "wrong-ancestor", "missing-private", "missing-native-location", "missing-ancestor-location"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cognitiveScenario(t)
			target := cdnAsset(t, assets, cognitiveAgentType)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if ready, err := driver.Preflight(context.Background(), request); err != nil || !ready.Allowed {
				t.Fatal("invalid baseline", ready, err)
			}
			child := s.records[target.Identity.NativeID]
			parentID := redisParentID(target.Identity.NativeID)
			parent := s.records[parentID]
			rootID := redisRootID(target.Identity.NativeID)
			root := s.records[rootID]
			switch mode {
			case "child-config":
				object(child["properties"])["displayName"] = "changed"
			case "child-private":
				object(child["properties"])["connectionString"] = "never-expose-cognitive-secret"
			case "child-property-tag":
				object(child["properties"])["tags"] = map[string]any{"steward:protected": "true"}
			case "child-state":
				object(child["properties"])["provisioningState"] = "Updating"
			case "child-location":
				child["location"] = "westus"
			case "parent-location":
				parent["location"] = "westus"
			case "project-location":
				s.records[redisParentID(parentID)]["location"] = "westus"
			case "parent-config":
				object(parent["properties"])["displayName"] = "changed"
			case "parent-private":
				object(parent["properties"])["password"] = "never-expose-cognitive-secret"
			case "parent-tag":
				parent["tags"] = map[string]any{"steward:protected": "true"}
			case "parent-state":
				object(parent["properties"])["provisioningState"] = "Moving"
			case "root-config":
				object(root["properties"])["customSubDomainName"] = "changed"
			case "root-private":
				object(root["properties"])["migrationToken"] = "never-expose-cognitive-secret"
			case "root-internal-id":
				object(root["properties"])["internalId"] = "replacement"
			case "root-created":
				object(root["properties"])["dateCreated"] = "2026-09-01"
			case "root-default":
				object(root["properties"])["defaultProject"] = "unreviewed-project"
			case "root-project-index":
				object(root["properties"])["associatedProjects"] = []any{"unreviewed-project"}
			case "root-state":
				object(root["properties"])["provisioningState"] = "Accepted"
			case "root-identity":
				root["id"] = rootID + "wrong"
			case "root-partial":
				s.status[rootID] = 206
			case "root-forbidden":
				s.status[rootID] = 403
			case "root-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": rootID + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "root-managed-group":
				group := strings.Join(strings.Split(rootID, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": rootID}
			case "missing-ancestor":
				delete(object(request.Asset.Normalized["_cognitive_ancestors"]), rootID)
			case "wrong-ancestor":
				object(request.Asset.Normalized["_cognitive_ancestors"])[rootID] = map[string]any{"configuration": "forged"}
			case "missing-private":
				delete(request.Asset.Normalized, "_cognitive_private_configuration")
			case "missing-native-location":
				delete(request.Asset.Normalized, "_cognitive_native_location")
			case "missing-ancestor-location":
				delete(object(object(request.Asset.Normalized["_cognitive_ancestors"])[parentID]), "native_location")
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("Cognitive mutation bypassed changed or protected resource", mode, err)
			}
		})
	}
}

func TestCognitiveRetainedChildrenAndNativeFinalAbsence(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	target := cdnAsset(t, assets, cognitiveType)
	request, input := dnsRequest(t, r, assets, target)
	children := append(slices.Clone(request.LifecycleImpacts), request.PrerequisiteDeletions...)
	if len(children) < 15 {
		t.Fatal("account lost its reviewed dependency tree", len(children))
	}
	for _, child := range children {
		input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{child.Asset.Identity.NativeID}}}
		solved, err := plan.Solve(input)
		if err != nil || len(solved.Blockers) == 0 {
			t.Fatal("retained Cognitive child permitted account deletion", child.Asset.Identity.NativeType, err)
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatal("unreviewed Cognitive children deleted")
	}
	for _, child := range children {
		s.gone[child.Asset.Identity.NativeID] = true
	}
	s.gone[target.Identity.NativeID] = true
	// The read-only native view must be absent too, regardless of account absence.
	view := cdnAsset(t, assets, cognitivePerimeterType)
	s.gone[view.Identity.NativeID] = false
	ready, err := driver.Preflight(context.Background(), request)
	if err != nil || ready.Absent {
		t.Fatal("read-only view was not verified", ready, err)
	}
	s.status[view.Identity.NativeID] = 403
	if _, err := driver.Preflight(context.Background(), request); err == nil {
		t.Fatal("unreadable managed view was hidden")
	}
	delete(s.status, view.Identity.NativeID)
	s.gone[view.Identity.NativeID] = true
	ready, err = driver.Preflight(context.Background(), request)
	if err != nil || !ready.Absent || !ready.Allowed {
		t.Fatal("Cognitive final absence", ready, err)
	}
}

func TestCognitiveConnectionScopeResolution(t *testing.T) {
	for _, mode := range []string{"account-id", "project-id", "ambiguous-name", "missing-name", "wrong-account", "wrong-project", "wrong-type", "cross-subscription", "whitespace", "non-string"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cognitiveScenario(t)
			host := cdnAsset(t, assets, cognitiveProjectHostType)
			c, _ := r.resolve(context.Background(), "connection")
			value := any(cdnAsset(t, assets, cognitiveConnectionType).Identity.NativeID)
			switch mode {
			case "project-id":
				value = cdnAsset(t, assets, cognitiveProjectConnectionType).Identity.NativeID
			case "ambiguous-name":
				value = "connection-one"
			case "missing-name":
				value = "missing-connection"
			case "wrong-account":
				value = strings.Replace(value.(string), "/account-one/", "/foreign-account/", 1)
			case "wrong-project":
				value = strings.Replace(cdnAsset(t, assets, cognitiveProjectConnectionType).Identity.NativeID, "/project-one/", "/foreign-project/", 1)
			case "wrong-type":
				value = host.Identity.NativeID
			case "cross-subscription":
				value = strings.Replace(value.(string), testSubscription, testTenant, 1)
			case "whitespace":
				value = " " + value.(string)
			case "non-string":
				value = 123
			}
			if mode == "missing-name" {
				for _, parent := range []string{redisRootID(host.Identity.NativeID), redisParentID(host.Identity.NativeID)} {
					s.status[parent+"/connections/missing-connection"] = 404
				}
			}
			refs, err := c.cognitiveReferenceIDs(context.Background(), host.Identity.NativeID, host.Identity.NativeType, map[string]any{"properties": map[string]any{"storageConnections": []any{value}}})
			if mode == "account-id" || mode == "project-id" {
				if err != nil || len(refs) != 1 || refs[0] != value {
					t.Fatal("valid scoped connection", refs, err)
				}
			} else if err == nil {
				t.Fatal("ambiguous or foreign connection accepted", mode)
			}
		})
	}
}
func TestCognitiveSharedAssociationTargetProtection(t *testing.T) {
	for _, mode := range []string{"config", "secret", "protected", "lock", "managed-group", "unreadable", "partial", "identity", "retarget", "reread"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cognitiveScenario(t)
			target := cdnAsset(t, assets, cognitiveAssociationType)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if ready, err := driver.Preflight(context.Background(), request); err != nil || !ready.Allowed {
				t.Fatal("invalid shared association baseline", ready, err)
			}
			account := cdnAsset(t, assets, cognitiveType)
			id := account.Identity.NativeID
			raw := s.records[id]
			switch mode {
			case "config":
				raw["sku"] = map[string]any{"name": "changed"}
			case "secret":
				object(raw["properties"])["migrationToken"] = "private-target-migration-token"
			case "protected":
				raw["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "ReadOnly"}}}
			case "managed-group":
				group := strings.Join(strings.Split(id, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": id}
			case "unreadable":
				s.status[id] = 403
			case "partial":
				s.status[id] = 206
			case "identity":
				raw["id"] = id + "wrong"
			case "retarget":
				object(s.records[target.Identity.NativeID]["properties"])["accountId"] = id + "wrong"
			case "reread":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						reads++
						if reads > 1 {
							raw["sku"] = map[string]any{"name": "changed"}
						}
					}
					return nil, false
				}
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("association target protection bypassed", mode, err)
			}
		})
	}
}
func TestCognitiveDependentOutboundRuleNeedsReviewedNetwork(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	rule := cdnAsset(t, assets, cognitiveOutboundType)
	parent := cdnAsset(t, assets, cognitiveNetworkType)
	raw := map[string]any{"id": redisParentID(rule.Identity.NativeID) + "/outboundrules/dependency-one", "type": cognitiveOutboundType, "name": "dependency-one", "properties": map[string]any{"type": "FQDN", "category": "Dependency", "destination": "dependency.example", "status": "Active", "parentRuleNames": []any{last(rule.Identity.NativeID)}}}
	s.add(raw, "2026-05-01")
	collection := redisParentID(rule.Identity.NativeID) + "/outboundrules"
	s.lists[collection] = append(s.lists[collection], raw)
	object(object(s.records[parent.Identity.NativeID]["properties"])["managedNetwork"])["outboundRules"] = map[string]any{last(rule.Identity.NativeID): s.records[rule.Identity.NativeID]["properties"], "dependency-one": raw["properties"]}
	driver, _ := r.ResolveAction(context.Background(), "connection", rule)
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: rule}); err == nil || len(s.deletes) != 0 {
		t.Fatal("unreviewed dependent outbound rule affected")
	}
	member := dnsAsset(t, r, raw)
	member.Location = "eastus"
	member.Capabilities = nil
	assets = append(assets, member)
	parent = dnsAsset(t, r, s.records[parent.Identity.NativeID])
	parent.Location = "eastus"
	for i := range assets {
		if assets[i].ID == parent.ID {
			assets[i] = parent
		}
	}
	request, input := dnsRequest(t, r, assets, parent)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 || len(request.LifecycleImpacts) != 2 {
		t.Fatal("network did not review both rules", err, solved.Blockers, len(request.LifecycleImpacts))
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{parent.ID: {"retain_resources": []string{member.Identity.NativeID}}}
	retained, err := plan.Solve(input)
	if err != nil || len(retained.Blockers) == 0 {
		t.Fatal("retained derived rule allowed network deletion", err)
	}
}

func TestCognitiveNetworkCascadeProtectsPrivateEndpointTargets(t *testing.T) {
	for _, mode := range []string{"allowed", "protected", "locked", "changed", "private-changed", "unreadable", "target-group-managed"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cognitiveScenario(t)
			network := cdnAsset(t, assets, cognitiveNetworkType)
			rule := cdnAsset(t, assets, cognitiveOutboundType)
			id := "/subscriptions/" + testSubscription + "/resourcegroups/target-rg/providers/microsoft.storage/storageaccounts/targetstore"
			target := map[string]any{"id": id, "type": storageType, "name": "targetstore", "location": "eastus", "properties": map[string]any{"creationTime": "2025-01-01"}}
			mapping, _ := findType(storageType)
			s.add(target, mapping.Version)
			raw := s.records[rule.Identity.NativeID]
			raw["properties"] = map[string]any{"type": "PrivateEndpoint", "category": "Required", "status": "Active", "destination": map[string]any{"serviceResourceId": id, "subresourceTarget": "blob"}}
			object(object(s.records[network.Identity.NativeID]["properties"])["managedNetwork"])["outboundRules"] = map[string]any{last(rule.Identity.NativeID): raw["properties"]}
			rule = dnsAsset(t, r, raw)
			rule.Location = "eastus"
			rule.Capabilities = nil
			network = dnsAsset(t, r, s.records[network.Identity.NativeID])
			network.Location = "eastus"
			for i := range assets {
				if assets[i].ID == rule.ID {
					assets[i] = rule
				}
				if assets[i].ID == network.ID {
					assets[i] = network
				}
			}
			request, _ := dnsRequest(t, r, assets, network)
			for _, prerequisite := range request.PrerequisiteDeletions {
				s.gone[prerequisite.Asset.Identity.NativeID] = true
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", network)
			if ready, err := driver.Preflight(context.Background(), request); err != nil || !ready.Allowed {
				t.Fatal("invalid network cascade baseline", ready, err)
			}
			switch mode {
			case "protected":
				target["tags"] = map[string]any{"steward:protected": "true"}
			case "locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "changed":
				target["sku"] = map[string]any{"name": "changed"}
			case "private-changed":
				object(target["properties"])["connectionString"] = "never-expose-target-secret"
			case "unreadable":
				s.status[id] = 403
			case "target-group-managed":
				group := strings.Join(strings.Split(id, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": id}
			}
			result, err := driver.Execute(context.Background(), request)
			if mode != "allowed" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("network bypassed target protection", mode, err)
				}
				return
			}
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != network.Identity.NativeID || s.gone[id] {
				t.Fatal("unexpected network/target deletion", err, s.deletes)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("network disappearance hid a live required rule", err)
			}
			s.gone[rule.Identity.NativeID] = true
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done || s.gone[id] {
				t.Fatal("network final rule absence", err)
			}
		})
	}
}
