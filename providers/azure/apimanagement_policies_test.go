package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func apimPolicyScenario(t *testing.T, policyKind string) (*dnsScenario, *Runtime, []asset.Asset, asset.Asset, asset.Asset) {
	t.Helper()
	s, r, assets := apimScenario(t)
	policy := cdnAsset(t, assets, policyKind)
	namespaceKind := apimServiceType
	if strings.Contains(policyKind, "/workspaces/") {
		namespaceKind = apimWorkspaceType
	}
	named := cdnAsset(t, assets, namespaceKind+"/namedValues")
	object(s.records[named.Identity.NativeID]["properties"])["displayName"] = "HeaderSecret"
	object(s.records[named.Identity.NativeID]["properties"])["value"] = "private-credential-for-policy-test"
	object(s.records[policy.Identity.NativeID]["properties"])["value"] = `<policies><inbound><set-header name="X-Secret" exists-action="override"><value>{{HeaderSecret}}</value></set-header><set-backend-service backend-id="stewardtest"/><authentication-certificate certificate-id="stewardtest"/><include-fragment fragment-id="stewardtest"/><log-to-eventhub logger-id="stewardtest">private-policy-text</log-to-eventhub><!-- <set-backend-service backend-id="comment-only"/> {{comment-only}} --></inbound></policies>`
	for i := range assets {
		if assets[i].ID == policy.ID || assets[i].ID == named.ID {
			assets[i] = dnsAsset(t, r, s.records[assets[i].Identity.NativeID])
		}
	}
	return s, r, assets, cdnAsset(t, assets, policyKind), cdnAsset(t, assets, namespaceKind+"/namedValues")
}

func TestAPIMAuthorizationConnectionIsAReviewedPolicyDependency(t *testing.T) {
	for _, policyKind := range []string{apimAPIType + "/policies", apimWorkspaceType + "/policies"} {
		t.Run(policyKind, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			policy := cdnAsset(t, assets, policyKind)
			authorization := cdnAsset(t, assets, apimServiceType+"/authorizationProviders/authorizations")
			object(s.records[policy.Identity.NativeID]["properties"])["value"] = `<policies><inbound><get-authorization-context authorization-id="stewardtest" provider-id="stewardtest" context-variable-name="authorization"/></inbound></policies>`
			for i := range assets {
				if assets[i].ID == policy.ID {
					assets[i] = dnsAsset(t, r, s.records[policy.Identity.NativeID])
					policy = assets[i]
				}
			}
			if !slices.Contains(stringValues(policy.Normalized[referenceKey(authorization.Identity.NativeType)]), authorization.Identity.NativeID) {
				t.Fatal("authorization selector did not reach the native connection")
			}
			request, input := dnsRequest(t, r, assets, authorization)
			if !slices.ContainsFunc(request.PrerequisiteDeletions, func(value contracts.ActionImpact) bool { return value.Asset.ID == policy.ID }) {
				t.Fatal("connection deletion did not review the consuming policy")
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", authorization)
			if _, err := driver.Execute(t.Context(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("live policy did not prevent authorization deletion")
			}
			solved, _ := plan.Solve(input)
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						selected = value
					}
				}
				stepRequest := servicePlanRequest(solved, assets, selected)
				encoded, _ := json.Marshal(stepRequest)
				if json.Unmarshal(encoded, &stepRequest) != nil {
					t.Fatal("request restoration failed")
				}
				driver, _ := r.ResolveAction(t.Context(), "connection", selected)
				result, err := driver.Execute(t.Context(), stepRequest)
				if err != nil {
					t.Fatal("reviewed authorization delete", err)
				}
				streamAnalyticsAfterDelete(s)
				waited, err := driver.Wait(t.Context(), stepRequest, result)
				if err != nil || !waited.Done {
					t.Fatal("authorization absence", waited, err)
				}
			}
			if s.deletes[len(s.deletes)-1] != authorization.Identity.NativeID {
				t.Fatal("policy was not removed before its authorization")
			}
		})
	}
}

func TestAPIMAuthorizationExpressionsAndOwnerBoundaries(t *testing.T) {
	for _, mode := range []string{"literal", "authorization-expression", "provider-expression", "both-expressions", "foreign-authorization", "foreign-provider", "missing-authorization", "missing-provider", "denied-index"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			policy := cdnAsset(t, assets, apimAPIType+"/policies")
			authorization := cdnAsset(t, assets, apimServiceType+"/authorizationProviders/authorizations")
			other := batchClone(s.records[authorization.Identity.NativeID])
			other["id"], other["name"] = authorization.Identity.NativeID+"-other", "stewardtest-other"
			s.add(other, apimVersion)
			path := redisParentID(authorization.Identity.NativeID) + "/authorizations"
			s.lists[path] = append(s.lists[path], other)
			provider, selector := "stewardtest", "stewardtest"
			switch mode {
			case "authorization-expression", "denied-index":
				selector = "@(context.Variables[&quot;authorization&quot;])"
				if mode == "denied-index" {
					s.status[path] = 403
				}
			case "provider-expression":
				provider = "@(context.Variables[&quot;provider&quot;])"
			case "both-expressions":
				provider, selector = "@(context.Variables[&quot;provider&quot;])", "@(context.Variables[&quot;authorization&quot;])"
			case "foreign-authorization":
				selector = strings.Replace(authorization.Identity.NativeID, "/authorizationproviders/stewardtest/", "/authorizationproviders/other/", 1)
			case "foreign-provider":
				provider = strings.Replace(redisParentID(authorization.Identity.NativeID), "/service/stewardtest/", "/service/other/", 1)
			case "missing-authorization":
				selector = ""
			case "missing-provider":
				provider = ""
			}
			object(s.records[policy.Identity.NativeID]["properties"])["value"] = `<policies><inbound><get-authorization-context provider-id="` + provider + `" authorization-id="` + selector + `"/></inbound></policies>`
			c, _ := r.resolve(t.Context(), "connection")
			refs, err := c.apimResolvedReferences(t.Context(), policy.Identity.NativeType, policy.Identity.NativeID, s.records[policy.Identity.NativeID], nil, nil)
			if slices.Contains([]string{"literal", "authorization-expression", "provider-expression", "both-expressions"}, mode) {
				if err != nil || !slices.Contains(refs, authorization.Identity.NativeID) || !slices.Contains(refs, redisParentID(authorization.Identity.NativeID)) {
					t.Fatal("authorization dependency missing", refs, err)
				}
				expectedOther := mode == "authorization-expression" || mode == "both-expressions"
				if slices.Contains(refs, text(other["id"])) != expectedOther {
					t.Fatal("authorization expression did not preserve the actual possibilities", refs)
				}
			} else if err == nil {
				t.Fatal("invalid authorization scope or incomplete index accepted", refs)
			}
		})
	}
}

func TestAPIMCertificateThumbprintsResolveStoredCertificates(t *testing.T) {
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		for _, mode := range []string{"policy", "expression", "backend", "service-fabric", "explicit-ids-win", "unknown", "denied"} {
			t.Run(last(namespace)+"/"+mode, func(t *testing.T) {
				s, r, assets := apimScenario(t)
				certificate := cdnAsset(t, assets, namespace+"/certificates")
				object(s.records[certificate.Identity.NativeID]["properties"])["thumbprint"] = "CA06F56B258B7A0D4F2B05470939478651151984"
				other := batchClone(s.records[certificate.Identity.NativeID])
				other["id"], other["name"] = certificate.Identity.NativeID+"-other", "stewardtest-other"
				object(other["properties"])["thumbprint"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
				s.add(other, apimVersion)
				path := redisParentID(certificate.Identity.NativeID) + "/certificates"
				s.lists[path] = append(s.lists[path], other)
				target := cdnAsset(t, assets, namespace+"/policies")
				props := map[string]any{"value": `<policies><inbound><authentication-certificate thumbprint="ca06f56b258b7a0d4f2b05470939478651151984"/></inbound></policies>`}
				switch mode {
				case "expression":
					props["value"] = `<policies><inbound><authentication-certificate thumbprint="@(context.Variables[&quot;thumbprint&quot;])"/></inbound></policies>`
				case "unknown":
					props["value"] = `<policies><inbound><authentication-certificate thumbprint="BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"/></inbound></policies>`
				case "backend", "service-fabric", "explicit-ids-win":
					target = cdnAsset(t, assets, namespace+"/backends")
					props = map[string]any{"credentials": map[string]any{"certificate": []any{"ca06f56b258b7a0d4f2b05470939478651151984"}}}
					if mode == "service-fabric" {
						props = map[string]any{"properties": map[string]any{"serviceFabricCluster": map[string]any{"clientCertificatethumbprint": "ca06f56b258b7a0d4f2b05470939478651151984"}}}
					} else if mode == "explicit-ids-win" {
						object(props["credentials"])["certificateIds"] = []any{"stewardtest-other"}
					}
				case "denied":
					s.status[path] = 403
				}
				c, _ := r.resolve(t.Context(), "connection")
				refs, err := c.apimResolvedReferences(t.Context(), target.Identity.NativeType, target.Identity.NativeID, map[string]any{"properties": props}, nil, nil)
				if mode == "denied" {
					if err == nil {
						t.Fatal("incomplete certificate index accepted")
					}
					return
				}
				if err != nil || slices.Contains(refs, certificate.Identity.NativeID) != (mode != "unknown" && mode != "explicit-ids-win") || slices.Contains(refs, text(other["id"])) != (mode == "expression" || mode == "explicit-ids-win") {
					t.Fatal("certificate thumbprint guessed a resource ID or ignored selector precedence", refs, err)
				}
			})
		}
	}
}

func TestAPIMPoliciesResolveNamedValuesAndTypedReferencesWithoutLeakingBodies(t *testing.T) {
	for _, kind := range []string{apimServiceType + "/policies", apimWorkspaceType + "/policies", apimAPIType + "/operations/policies", apimWorkspaceType + "/apis/policies"} {
		t.Run(kind, func(t *testing.T) {
			_, r, _, policy, named := apimPolicyScenario(t, kind)
			refs := stringValues(policy.Normalized["_apim_references"])
			for _, collection := range []string{"namedvalues", "backends", "certificates", "policyfragments", "loggers"} {
				id := apimNamespaceID(policy.Identity.NativeID) + "/" + collection + "/stewardtest"
				if !slices.Contains(refs, id) {
					t.Fatal("policy reference missing", collection)
				}
			}
			for _, ref := range refs {
				if strings.Contains(ref, "comment-only") || strings.Contains(ref, "headersecret") {
					t.Fatal("comments or display names became resource identities")
				}
			}
			batch, err := r.List(context.Background(), productRequest(r, kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("policy inventory", err)
			}
			encoded, _ := json.Marshal([]any{batch, policy, named})
			for _, secret := range []string{"private-credential-for-policy-test", "private-policy-text", "<policies>", "{{HeaderSecret}}"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("policy or named-value secret escaped inventory")
				}
			}
		})
	}
}

func TestAPIMPolicyDependenciesOrderCleanupAndPermitReviewedServiceCascade(t *testing.T) {
	for _, targetKind := range []string{apimServiceType + "/namedValues", apimServiceType + "/backends", apimServiceType + "/policyFragments", apimServiceType} {
		t.Run(targetKind, func(t *testing.T) {
			s, r, assets, policy, _ := apimPolicyScenario(t, apimAPIType+"/policies")
			target := cdnAsset(t, assets, targetKind)
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal(err, solved.Blockers)
			}
			if targetKind != apimServiceType {
				if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != policy.ID {
					t.Fatal("policy was not reviewed before its referenced resource", request.PrerequisiteDeletions)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("policy reference did not block target deletion")
				}
			}
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						selected = value
					}
				}
				stepRequest := servicePlanRequest(solved, assets, selected)
				driver, _ := r.ResolveAction(context.Background(), "connection", selected)
				result, err := driver.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatal("policy-dependent delete", selected.Identity.NativeID, err)
				}
				streamAnalyticsAfterDelete(s)
				waited, err := driver.Wait(context.Background(), stepRequest, result)
				if err != nil || !waited.Done {
					t.Fatal("policy-dependent absence", waited, err)
				}
			}
			if s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("dependency order changed", s.deletes)
			}
		})
	}
}

func TestAPIMPolicyExpressionsAndReferenceFailures(t *testing.T) {
	for _, mode := range []string{"expression", "named-identifier", "wrong-display-name", "named-values-denied", "invalid-xml", "external-link", "foreign-arm-id"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, policy, named := apimPolicyScenario(t, apimServiceType+"/policies")
			raw := s.records[policy.Identity.NativeID]
			props := object(raw["properties"])
			backend := cdnAsset(t, assets, apimServiceType+"/backends")
			second := maps.Clone(s.records[backend.Identity.NativeID])
			second["id"], second["name"], second["_apim_header_etag"] = redisParentID(backend.Identity.NativeID)+"/backends/second", "second", `"second-backend"`
			s.add(second, apimVersion)
			s.lists[redisParentID(backend.Identity.NativeID)+"/backends"] = append(s.lists[redisParentID(backend.Identity.NativeID)+"/backends"], second)
			switch mode {
			case "expression":
				props["value"] = `<policies><inbound><set-backend-service backend-id="@(context.Variables[&quot;backend&quot;])"/></inbound></policies>`
			case "named-identifier":
				props["value"] = `<policies><inbound><set-backend-service backend-id="{{HeaderSecret}}"/></inbound></policies>`
			case "wrong-display-name":
				props["value"] = `<policies><inbound><set-header name="Header"><value>{{stewardtest}}</value></set-header></inbound></policies>`
			case "named-values-denied":
				s.status[redisParentID(named.Identity.NativeID)+"/namedvalues"] = 403
			case "invalid-xml":
				props["value"] = `<policies><inbound>`
			case "external-link":
				props["format"], props["value"] = "xml-link", "https://policy.example.test/policy.xml?secret=never-fetch"
			case "foreign-arm-id":
				props["value"] = `<policies><inbound><set-backend-service backend-id="` + strings.Replace(backend.Identity.NativeID, "/service/stewardtest/", "/service/foreign/", 1) + `"/></inbound></policies>`
			}
			c, _ := r.resolve(context.Background(), "connection")
			refs, err := c.apimResolvedReferences(context.Background(), policy.Identity.NativeType, policy.Identity.NativeID, raw, nil, nil)
			if slices.Contains([]string{"named-values-denied", "invalid-xml", "external-link", "foreign-arm-id"}, mode) {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("unverified policy dependency accepted", refs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-display-name" {
				if slices.Contains(refs, named.Identity.NativeID) {
					t.Fatal("named value resource name used instead of displayName")
				}
				return
			}
			if !slices.Contains(refs, backend.Identity.NativeID) || !slices.Contains(refs, text(second["id"])) {
				t.Fatal("expression guessed a single backend", refs)
			}
			if mode == "named-identifier" && !slices.Contains(refs, named.Identity.NativeID) {
				t.Fatal("named backend reference hid the named value")
			}
		})
	}
}

func TestAPIMSameKindDependenciesRequireDistinctNativeReferences(t *testing.T) {
	_, r, assets := apimScenario(t)
	c, _ := r.resolve(context.Background(), "connection")
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		for _, suffix := range []string{"apis", "backends", "policyFragments"} {
			kind := namespace + "/" + suffix
			t.Run(kind, func(t *testing.T) {
				original := cdnAsset(t, assets, kind)
				id := original.Identity.NativeID
				other := redisParentID(id) + "/" + strings.ToLower(suffix) + "/other"
				props := map[string]any{}
				switch suffix {
				case "apis":
					other, id = id, id+";rev=2"
					props["apiRevision"] = "2"
				case "backends":
					props["pool"] = map[string]any{"services": []any{map[string]any{"id": other}, map[string]any{"id": id}}}
				case "policyFragments":
					props["value"] = `<fragment><include-fragment fragment-id="other"/><include-fragment fragment-id="stewardtest"/></fragment>`
				}
				refs, err := c.apimResolvedReferences(context.Background(), kind, id, map[string]any{"id": id, "name": last(id), "type": kind, "properties": props}, nil, nil)
				if err != nil || !slices.Contains(refs, other) || slices.Contains(refs, id) {
					t.Fatal("explicit same-kind reference changed", refs, err)
				}
			})
		}
	}
}

func TestAPIMAPIBackendReferenceParticipatesInNativeIncomingIndex(t *testing.T) {
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		t.Run(namespace, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			api := cdnAsset(t, assets, namespace+"/apis")
			backend := cdnAsset(t, assets, namespace+"/backends")
			object(s.records[api.Identity.NativeID]["properties"])["backendId"] = backend.Identity.NativeID
			api = dnsAsset(t, r, s.records[api.Identity.NativeID])
			if !slices.Contains(stringValues(api.Normalized[referenceKey(backend.Identity.NativeType)]), backend.Identity.NativeID) {
				t.Fatal("API backend reference missing from inventory")
			}
			c, _ := r.resolve(t.Context(), "connection")
			index, err := c.apimIncomingIndex(t.Context(), backend.Identity)
			if err != nil || !slices.ContainsFunc(index[backend.Identity.NativeID], func(value serviceChild) bool { return value.id == api.Identity.NativeID }) {
				t.Fatal("API backend reference missing from native delete prerequisites", err)
			}
		})
	}
}
