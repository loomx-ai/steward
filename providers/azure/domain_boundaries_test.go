package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDomainHostnameWaitRestartsAndRechecks(t *testing.T) {
	for _, mode := range []string{"pending", "clear", "unreviewed-name", "traffic-manager", "returned-binding", "protected", "changed-contact", "apps-403", "forged-phase", "extra-receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := domainScenario(t, true)
			target := cdnAsset(t, assets, domainType)
			request, _ := dnsRequest(t, r, assets, target)
			request.IdempotencyKey = "domain-delayed-index"
			for _, prerequisite := range request.PrerequisiteDeletions {
				s.gone[prerequisite.Asset.Identity.NativeID] = true
			}
			driver, err := r.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || result.Data["domain_phase"] != "hostnames" || len(s.deletes) != 0 {
				t.Fatal("domain did not wait for reviewed hostname indexes", result, err)
			}
			result, request = roundTripDomainAction(t, result, request)
			driver, err = s.runtime(t).ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			switch mode {
			case "clear":
				props["managedHostNames"] = []any{}
			case "unreviewed-name":
				object(array(props["managedHostNames"])[0])["name"] = "new.example.com"
			case "traffic-manager":
				object(array(props["managedHostNames"])[0])["azureResourceType"] = "TrafficManager"
			case "returned-binding":
				for _, prerequisite := range request.PrerequisiteDeletions {
					if isAppBinding(prerequisite.Asset.Identity.NativeType) {
						s.gone[prerequisite.Asset.Identity.NativeID] = false
					}
				}
			case "protected":
				raw["tags"] = map[string]any{"steward/protected": "true"}
			case "changed-contact":
				object(props["contactRegistrant"])["email"] = "changed@example.net"
			case "apps-403":
				s.status["/subscriptions/"+testSubscription+"/providers/microsoft.web/sites"] = 403
			case "forged-phase":
				result.Data["domain_phase"] = "delete"
			case "extra-receipt":
				result.Data["forceHardDeleteDomain"] = true
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if mode != "pending" && mode != "clear" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("hostname wait bypassed a live/review boundary", waited, err)
				}
				return
			}
			if err != nil || waited.Done {
				t.Fatal("hostname wait completed without domain absence", waited, err)
			}
			if mode == "pending" {
				if waited.Data["domain_phase"] != "hostnames" || len(s.deletes) != 0 {
					t.Fatal("hostname wait submitted a premature delete")
				}
				return
			}
			if waited.Data["domain_phase"] != "delete" || len(s.deletes) != 1 {
				t.Fatal("cleared hostname wait did not advance once", waited)
			}
			result.Data = waited.Data
			request.ExecutionResult = &result
			// A saved receipt suppresses retries even when native deletion
			// progress has not yet appeared in the resource GET.
			s.gone[target.Identity.NativeID] = false
			result, request = roundTripDomainAction(t, result, request)
			driver, err = s.runtime(t).ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || len(s.deletes) != 1 {
				t.Fatal("saved domain receipt repeated deletion", err)
			}
			if waited, err := driver.Wait(t.Context(), request, result); err != nil || waited.Done {
				t.Fatal("saved domain receipt replaced native readback", waited, err)
			}
		})
	}
}

func TestDomainLiveMutationBoundaries(t *testing.T) {
	for _, mode := range []string{"contact", "transfer-code", "created", "renewed", "expires", "auto-renew", "privacy", "dns-zone", "nameservers", "future-setting", "protected", "group-protected", "managed-group", "lock", "new-identifier", "known-identifier-omitted", "ownership-collection-404", "apps-403", "wrong-identity", "read-202", "read-200-operation", "in-progress", "managed-hostname", "etag-only", "modification-audit-only"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := domainScenario(t, false)
			target := cdnAsset(t, assets, domainType)
			request, _ := dnsRequest(t, r, assets, target)
			driver, err := r.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			child := request.PrerequisiteDeletions[0].Asset
			s.gone[child.Identity.NativeID] = true
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			group := strings.Join(strings.Split(target.Identity.NativeID, "/")[:5], "/")
			switch mode {
			case "contact":
				object(props["contactRegistrant"])["email"] = "new-owner@example.net"
			case "transfer-code":
				props["authCode"] = "new-authorization"
			case "created":
				props["createdTime"] = "2026-09-11T00:00:00Z"
			case "renewed":
				props["lastRenewedTime"] = "2026-09-11T00:00:00Z"
			case "expires":
				props["expirationTime"] = "2027-09-11T00:00:00Z"
			case "auto-renew":
				props["autoRenew"] = false
			case "privacy":
				props["privacy"] = true
			case "dns-zone":
				props["dnsZoneId"] = resourceID(publicDNSZoneType, "different.example")
			case "nameservers":
				props["nameServers"] = []any{"ns1.changed.example"}
			case "future-setting":
				props["futureAuthoredSetting"] = "changed-private-setting"
			case "protected":
				raw["tags"] = map[string]any{"Steward:Protected": "true"}
			case "group-protected":
				s.records[group] = map[string]any{"id": group, "tags": map[string]any{"steward/protected": "true"}}
			case "managed-group":
				s.records[group] = map[string]any{"id": group, "managedBy": resourceID(aksType, "owner")}
			case "lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": target.Identity.NativeID + "/providers/Microsoft.Authorization/locks/purchase", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "new-identifier":
				id := target.Identity.NativeID + "/domainownershipidentifiers/late"
				late := map[string]any{"id": id, "name": "late", "type": domainOwnershipType, "properties": map[string]any{"ownershipId": "new-private-token"}}
				s.add(late, domainVersion)
				s.lists[target.Identity.NativeID+"/domainownershipidentifiers"] = []any{late}
			case "known-identifier-omitted":
				s.gone[child.Identity.NativeID] = false
				s.lists[target.Identity.NativeID+"/domainownershipidentifiers"] = []any{}
			case "ownership-collection-404":
				s.status[target.Identity.NativeID+"/domainownershipidentifiers"] = 404
			case "apps-403":
				s.status["/subscriptions/"+testSubscription+"/providers/microsoft.web/sites"] = 403
			case "wrong-identity":
				raw["id"] = resourceID(domainType, "someone-else.example")
			case "read-202":
				s.status[target.Identity.NativeID] = 202
			case "read-200-operation":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, target.Identity.NativeID) && req.Method == "GET" {
						header := http.Header{}
						header.Set("Location", "https://management.azure.com/operation")
						return jsonResponse(200, raw, header), true
					}
					return nil, false
				}
			case "in-progress":
				props["provisioningState"] = "InProgress"
			case "managed-hostname":
				props["managedHostNames"] = []any{map[string]any{"name": "unresolved.example.com", "azureResourceType": "TrafficManager"}}
			case "etag-only":
				raw["etag"] = "after-independent-deletion"
			case "modification-audit-only":
				raw["systemData"] = map[string]any{"lastModifiedAt": "2026-09-11T00:00:00Z", "lastModifiedBy": "operation"}
			}
			_, err = driver.Execute(t.Context(), request)
			allowed := mode == "etag-only" || mode == "modification-audit-only"
			if allowed && (err != nil || len(s.deletes) != 1) || !allowed && (err == nil || len(s.deletes) != 0) {
				t.Fatal("domain mutation boundary", mode, len(s.deletes), err)
			}
		})
	}
}

func TestDomainReviewTamperingFailsBeforeNativeCalls(t *testing.T) {
	for _, mode := range []string{"connection", "identity", "partition", "asset-id", "scope", "private-proof", "dependency-proof", "remove-prerequisite", "duplicate-prerequisite", "foreign-prerequisite", "retained-prerequisite", "changed-controller", "changed-kind", "changed-configuration", "forced-delete", "changed-parameters", "dns-reference", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := domainScenario(t, false)
			target := cdnAsset(t, assets, domainType)
			request, _ := dnsRequest(t, r, assets, target)
			driver, err := r.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			_, request = roundTripDomainAction(t, contracts.ActionResult{}, request)
			switch mode {
			case "connection":
				request.Asset.Identity.ConnectionID = "foreign"
			case "identity":
				request.Asset.Identity.NativeID += "x"
			case "partition":
				request.Asset.Identity.Partition = "different"
			case "asset-id":
				request.Asset.ID = "different"
			case "scope":
				request.Asset.Location = "eastus"
			case "private-proof":
				request.Asset.Normalized[domainConfiguration] = "different"
			case "dependency-proof":
				request.Asset.Normalized[domainDependencies] = map[string]any{}
			case "remove-prerequisite":
				request.PrerequisiteDeletions = nil
			case "duplicate-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "foreign-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "retained-prerequisite":
				request.PrerequisiteDeletions[0].Delete = false
			case "changed-controller":
				request.PrerequisiteDeletions[0].ControllerID = "different"
			case "changed-kind":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeType = domainType
			case "changed-configuration":
				request.PrerequisiteDeletions[0].Asset.Normalized[domainConfiguration] = "different"
			case "forced-delete":
				request.Parameters = map[string]any{"forceHardDeleteDomain": true}
			case "changed-parameters":
				object(request.Asset.Normalized["arm_parameters"])["domainName"] = "someone-else.example"
			case "dns-reference":
				request.Asset.Normalized["_domain_dns_zone"] = resourceID(publicDNSZoneType, "unreviewed.example")
			case "receipt":
				request.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"domain_delete_binding": "forged"}}
			}
			calls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				calls++
				return jsonResponse(500, nil, nil), true
			}
			if _, err := driver.Preflight(t.Context(), request); err == nil || calls != 0 {
				t.Fatal("tampered domain review reached native APIs", mode, calls, err)
			}
		})
	}
}

func TestDomainAlreadyDeletingAndResidualReadback(t *testing.T) {
	for _, mode := range []string{"pending", "absent", "read-202", "forbidden", "wrong-id", "changed-token", "receipt-injection"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := domainScenario(t, false)
			target := cdnAsset(t, assets, domainType)
			request, _ := dnsRequest(t, r, assets, target)
			object(s.records[target.Identity.NativeID]["properties"])["provisioningState"] = "Deleting"
			driver, err := r.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || len(s.deletes) != 0 {
				t.Fatal("already-deleting registration was submitted twice", err)
			}
			result, request = roundTripDomainAction(t, result, request)
			s.gone[target.Identity.NativeID] = true
			childID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
			switch mode {
			case "absent":
				s.gone[childID] = true
			case "read-202":
				s.status[childID] = 202
			case "forbidden":
				s.status[childID] = 403
			case "wrong-id":
				s.records[childID]["id"] = childID + "changed"
			case "changed-token":
				object(s.records[childID]["properties"])["ownershipId"] = "new-private-ownership"
			case "receipt-injection":
				result.ProviderOperationID = "https://management.azure.com/foreign-operation"
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if mode == "pending" && (err != nil || waited.Done) || mode == "absent" && (err != nil || !waited.Done) || mode != "pending" && mode != "absent" && err == nil {
				t.Fatal("domain residual/recovery boundary", mode, waited, err)
			}
			if len(s.deletes) != 0 {
				t.Fatal("readback issued a native mutation")
			}
		})
	}
}

func TestDomainOwnershipParentBoundaries(t *testing.T) {
	for _, mode := range []string{"recreated", "contact", "protected", "deleting", "in-progress", "missing", "parent-403", "token", "future-child"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := domainScenario(t, false)
			child, parent := cdnAsset(t, assets, domainOwnershipType), cdnAsset(t, assets, domainType)
			driver, err := r.ResolveAction(t.Context(), "connection", child)
			if err != nil {
				t.Fatal(err)
			}
			props := object(s.records[parent.Identity.NativeID]["properties"])
			switch mode {
			case "recreated":
				props["createdTime"] = "2026-09-11T00:00:00Z"
			case "contact":
				object(props["contactAdmin"])["email"] = "changed@example.net"
			case "protected":
				s.records[parent.Identity.NativeID]["tags"] = map[string]any{"steward/protected": "true"}
			case "deleting":
				props["provisioningState"] = "Deleting"
			case "in-progress":
				props["provisioningState"] = "InProgress"
			case "missing":
				s.gone[parent.Identity.NativeID] = true
			case "parent-403":
				s.status[parent.Identity.NativeID] = 403
			case "token":
				object(s.records[child.Identity.NativeID]["properties"])["ownershipId"] = "changed"
			case "future-child":
				object(s.records[child.Identity.NativeID]["properties"])["futureConfiguration"] = "changed"
			}
			if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: child, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("changed/protected ownership context allowed deletion", mode, err)
			}
		})
	}
}

func TestDomainDNSZoneSelectionAndLiveReferences(t *testing.T) {
	s, r, assets := domainScenario(t, false)
	domain, zone := cdnAsset(t, assets, domainType), cdnAsset(t, assets, publicDNSZoneType)
	_, input := dnsRequest(t, r, assets, domain)
	input.ResolvedAssetIDs = []asset.AssetID{zone.ID}
	blocked, err := plan.Solve(input)
	if err != nil || len(blocked.Blockers) == 0 {
		t.Fatal("DNS hosting deletion automatically released a registration", blocked, err)
	}
	input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, domain.ID)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 3 {
		t.Fatal("explicit registration and DNS plan did not order their independent lifetimes", solved, err)
	}
	request := servicePlanRequest(solved, assets, zone)
	if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != domain.ID {
		t.Fatal("DNS plan lost the authenticated domain prerequisite", request)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", zone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: zone, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatal("unreviewed live domain did not protect the DNS zone", err)
	}
	// The known domain remains a blocker even if its collection omits it.
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.domainregistration/domains"] = []any{}
	if _, err := driver.Execute(t.Context(), request); err == nil {
		t.Fatal("domain collection omission authorized DNS deletion")
	}
	s.gone[domain.Identity.NativeID] = true
	if _, err := driver.Execute(t.Context(), request); err != nil || !slices.Contains(s.deletes, zone.Identity.NativeID) {
		t.Fatal("DNS zone did not delete after its named domain was absent", err)
	}
}

func TestDomainOwnershipCursorBindsPrivateParent(t *testing.T) {
	s, r, assets := domainScenario(t, false)
	child, parent := cdnAsset(t, assets, domainOwnershipType), cdnAsset(t, assets, domainType)
	var second map[string]any
	payload, _ := json.Marshal(s.records[child.Identity.NativeID])
	json.Unmarshal(payload, &second)
	second["id"], second["name"] = parent.Identity.NativeID+"/domainownershipidentifiers/second", "second"
	object(second["properties"])["ownershipId"] = "second-private-token"
	s.add(second, domainVersion)
	collection := parent.Identity.NativeID + "/domainownershipidentifiers"
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if !strings.EqualFold(req.URL.Path, collection) {
			return nil, false
		}
		if req.URL.Query().Get("$skiptoken") == "second" {
			return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
		}
		return jsonResponse(200, map[string]any{"value": []any{s.records[child.Identity.NativeID]}, "nextLink": apiURL(collection, domainVersion) + "&%24skiptoken=second"}, nil), true
	}
	request := productRequest(r, domainOwnershipType)
	first, err := r.List(context.Background(), request)
	if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 {
		t.Fatal("native domain child pagination", first, err)
	}
	request.Cursor = first.NextCursor
	next, err := r.List(context.Background(), request)
	if err != nil || !next.Complete || len(next.Items) != 1 || next.Items[0].NativeID == first.Items[0].NativeID {
		t.Fatal("native domain second page", next, err)
	}
	object(object(s.records[parent.Identity.NativeID]["properties"])["contactAdmin"])["email"] = "changed-without-etag@example.net"
	if _, err := r.List(context.Background(), request); err == nil {
		t.Fatal("domain continuation ignored a changed private parent")
	}
}
