package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

var fleetTestKinds = []string{fleetType, fleetMemberType, fleetNamespaceType, fleetRunType, fleetStrategyType, fleetProfileType, fleetGateType}

func fleetTestBody(t *testing.T, kind, name string) map[string]any {
	t.Helper()
	prefix := fleetKind(kind).prefix
	if kind == fleetStrategyType {
		prefix = "UpdateStrategies"
	}
	raw := object(object(object(fleetExample(t, prefix+"_Get.json")["responses"])["200"])["body"])
	parent := strings.ToLower(resourceID(fleetType, "fleet1"))
	id := parent
	if kind != fleetType {
		id += "/" + strings.ToLower(last(kind)) + "/" + name
	} else {
		id = strings.ToLower(resourceID(kind, name))
	}
	raw["id"], raw["type"], raw["name"] = id, kind, name
	p := object(raw["properties"])
	// Original examples retain placeholder subscriptions, a singular
	// virtualNetwork segment, and references to different example fleets.
	// Compose actual native identities only here, after source verification.
	switch kind {
	case fleetType:
		subnet := resourceID(vnetType, "network") + "/subnets/agents"
		for _, field := range []string{"agentProfile", "apiServerAccessProfile"} {
			object(object(p["hubProfile"])[field])["subnetId"] = subnet
		}
		raw["identity"] = map[string]any{"type": "None"}
	case fleetMemberType:
		delete(p, "meshProperties") // The base fixture is not enrolled in a mesh.
		p["clusterResourceId"] = resourceID(aksType, "cluster1")
	case fleetProfileType:
		p["updateStrategyId"] = parent + "/updateStrategies/strategy1"
	case fleetRunType:
		if p["updateStrategyId"] != nil {
			p["updateStrategyId"] = parent + "/updateStrategies/strategy1"
		}
		if p["autoUpgradeProfileId"] != nil {
			p["autoUpgradeProfileId"] = parent + "/autoUpgradeProfiles/profile1"
		}
	case fleetGateType:
		object(p["target"])["id"] = parent + "/updateRuns/run1"
	}
	return raw
}

func TestFleetNativeOperationBoundaries(t *testing.T) {
	c := directClient(nil)
	group := c.root() + "/resourcegroups/test"
	parent := strings.ToLower(resourceID(fleetType, "fleet1"))
	for _, kind := range fleetTestKinds {
		t.Run(last(kind), func(t *testing.T) {
			scope, name := parent, "resource1"
			if kind == fleetType {
				scope = group
			} else if kind == fleetGateType {
				name = rbacTestRoleName
			}
			for _, method := range []string{"GET", "DELETE", "POST"} {
				request, err := c.fleetRequest(kind, scope, name, method)
				allowed := method == "GET" || method == "DELETE" && kind != fleetGateType || method == "POST" && kind == fleetRunType
				if (err == nil) != allowed {
					t.Fatal("native mutation capability changed", method, err)
				}
				if !allowed {
					continue
				}
				u, _ := url.Parse(request.URL)
				path := scope + "/" + last(kind) + "/" + name
				if kind == fleetType {
					path = group + "/providers/" + fleetType + "/" + name
				} else if method == "POST" {
					path += "/stop"
				}
				if !strings.EqualFold(u.Path, path) || u.Query().Get("api-version") != fleetAPIVersion(kind, method) || len(u.Query()) != 1 {
					t.Fatal("native route binding changed", request)
				}
			}
			for _, invalid := range []string{strings.Replace(scope, testSubscription, rbacOtherSubscription, 1), scope + "/..", scope + "?filter=1", scope + " ", c.root()} {
				if _, err := c.fleetRequest(kind, invalid, name, "GET"); err == nil {
					t.Fatal("invalid scope accepted", invalid)
				}
			}
			for _, bad := range []string{"../other", "bad/name", "a%2Fb", "UPPER", "a b", strings.Repeat("a", 64)} {
				if _, err := c.fleetRequest(kind, scope, bad, "GET"); err == nil {
					t.Fatal("invalid selector accepted", bad)
				}
			}
			if _, err := c.fleetRequest(kind, scope, "", "DELETE"); err == nil {
				t.Fatal("collection deletion accepted")
			}
			if _, err := c.fleetRequest(kind, scope, name, "PATCH"); err == nil {
				t.Fatal("unselected mutation accepted")
			}
		})
	}
	for _, scope := range []string{c.root(), group} {
		if _, err := c.fleetRequest(fleetType, scope, "", "GET"); err != nil {
			t.Fatal("native root collection unavailable", err)
		}
	}
}

func TestFleetQueryGuardUsesNativeProviderAndCollection(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	for _, path := range []string{
		root + "/resourcegroups/fleets/providers/Microsoft.ContainerService/managedClusters/fleets",
		root + "/resourcegroups/test/providers/Microsoft.ContainerService/managedClusters/fleets",
		resourceID(fleetType, "fleet1") + "/providers/Microsoft.Insights/diagnosticSettings",
	} {
		u, _ := url.Parse(apiURL(path, "2024-02-01"))
		if err := fleetListQuery(u); err != nil {
			t.Fatal("Fleet pagination guard leaked to another native family", path, err)
		}
	}
	for _, suffix := range []string{"&%24filter=x", "&%24skiptoken=a&%24skiptoken=b", "&api-version=2024-02-01", "&unknown=x", "&%24skiptoken="} {
		u, _ := url.Parse(apiURL(root+"/providers/"+fleetType, fleetVersion) + suffix)
		if fleetListQuery(u) == nil {
			t.Fatal("ambiguous or filtered continuation accepted", suffix)
		}
	}
}

func TestFleetNativeMetadataAndPrivateConfiguration(t *testing.T) {
	c := directClient(nil)
	for _, kind := range fleetTestKinds {
		name := "resource1"
		if kind == fleetGateType {
			name = rbacTestRoleName
		}
		raw := fleetTestBody(t, kind, name)
		if err := fleetValidate(kind, raw); err != nil {
			t.Fatal("valid native configuration rejected", kind, err)
		}
		before := c.privateConfiguration(fleetSnapshot(kind, raw))
		p := object(raw["properties"])
		raw["eTag"] = "changed after child deletion"
		object(raw["systemData"])["lastModifiedAt"] = "later"
		p["provisioningState"] = "Deleting"
		if kind != fleetGateType {
			p["status"] = map[string]any{"lastOperationId": "later"}
		}
		if before != c.privateConfiguration(fleetSnapshot(kind, raw)) {
			t.Fatal("native progress changed authored configuration", kind)
		}
		p["futurePrivateSetting"] = map[string]any{"value": "private-value"}
		if before == c.privateConfiguration(fleetSnapshot(kind, raw)) {
			t.Fatal("private authored property ignored", kind)
		}
		for _, mode := range []string{"id", "name", "type", "property-alias", "properties", "etag-alias"} {
			raw := fleetTestBody(t, kind, name)
			switch mode {
			case "id":
				raw["id"] = text(raw["id"]) + " "
			case "name":
				raw["name"] = "another"
			case "type":
				raw["type"] = aksType
			case "property-alias":
				object(raw["properties"])["ProvisioningState"] = "Succeeded"
			case "properties":
				raw["properties"] = []any{}
			case "etag-alias":
				raw["etag"] = "aliased"
			}
			if fleetValidate(kind, raw) == nil {
				t.Fatal("malformed native resource accepted", kind, mode)
			}
		}
	}
	member := fleetTestBody(t, fleetMemberType, "member1")
	external := strings.Replace(resourceID(aksType, "shared"), testSubscription, rbacOtherSubscription, 1)
	object(member["properties"])["clusterResourceId"] = external
	refs, err := fleetReferences(fleetMemberType, member)
	if err != nil || !slices.Equal(refs[aksType], []string{strings.ToLower(external)}) || len(refs[fleetType]) != 0 {
		t.Fatal("cross-subscription member reference lost", refs, err)
	}
	profile := fleetTestBody(t, fleetProfileType, "profile1")
	object(profile["properties"])["updateStrategyId"] = strings.Replace(text(object(profile["properties"])["updateStrategyId"]), "/fleet1/", "/another/", 1)
	if fleetValidate(fleetProfileType, profile) == nil {
		t.Fatal("foreign fleet strategy accepted")
	}
	gate := fleetTestBody(t, fleetGateType, rbacTestRoleName)
	object(object(object(gate["properties"])["target"])["updateRunProperties"])["name"] = "another-run"
	if fleetValidate(fleetGateType, gate) == nil {
		t.Fatal("contradictory gate target accepted")
	}
}

func TestFleetNamespacePlacementKeepsDynamicTargetsExplicit(t *testing.T) {
	for _, mode := range []string{"native-affinity", "hub-only", "default-placement", "fixed", "empty-fixed", "bad-propagation", "bad-container", "null-container", "hidden-selector", "alias", "duplicate", "wrong-name", "conflicting", "bad-policy", "malformed-type", "null-names", "unknown-delete", "padded-delete"} {
		t.Run(mode, func(t *testing.T) {
			raw := fleetTestBody(t, fleetNamespaceType, "ns1")
			p := object(raw["properties"])
			policy := map[string]any{"placementType": "PickFixed", "clusterNames": []any{"member-b", "member-a"}}
			propagation := map[string]any{"type": "Placement", "placementProfile": map[string]any{"defaultClusterResourcePlacement": map[string]any{"policy": policy}}}
			if mode != "native-affinity" {
				p["propagationPolicy"] = propagation
			}
			switch mode {
			case "hub-only":
				delete(p, "propagationPolicy")
			case "default-placement":
				delete(propagation, "placementProfile")
			case "empty-fixed":
				policy["clusterNames"] = []any{}
			case "bad-propagation":
				p["propagationPolicy"] = nil
			case "bad-container":
				propagation["placementProfile"] = []any{}
			case "null-container":
				propagation["placementProfile"] = nil
			case "hidden-selector":
				propagation["placementProfile"] = map[string]any{"DefaultClusterResourcePlacement": map[string]any{"policy": policy}}
			case "alias":
				policy["ClusterNames"] = policy["clusterNames"]
			case "duplicate":
				policy["clusterNames"] = []any{"member-a", "member-a"}
			case "wrong-name":
				policy["clusterNames"] = []any{"member/another"}
			case "conflicting":
				policy["affinity"] = map[string]any{}
			case "bad-policy":
				policy["placementType"] = "NewUnknownPolicy"
			case "malformed-type":
				policy["placementType"] = []any{}
			case "null-names":
				policy["placementType"], policy["clusterNames"] = "PickAll", nil
			case "unknown-delete":
				p["deletePolicy"] = "Sometimes"
			case "padded-delete":
				p["deletePolicy"] = " Keep "
			}
			want := slices.Contains([]string{"native-affinity", "hub-only", "default-placement", "fixed", "empty-fixed"}, mode)
			if err := fleetValidate(fleetNamespaceType, raw); (err == nil) != want {
				t.Fatal("placement boundary changed", mode, err)
			}
			if !want {
				return
			}
			names, dynamic, err := fleetNamespaceMembers(raw)
			if err != nil || dynamic != (mode == "native-affinity" || mode == "default-placement") || mode == "fixed" && !slices.Equal(names, []string{"member-a", "member-b"}) || mode != "fixed" && len(names) != 0 {
				t.Fatal("namespace targets changed", names, dynamic, err)
			}
			before := directClient(nil).privateConfiguration(fleetSnapshot(fleetNamespaceType, raw))
			p["deletePolicy"] = "Delete"
			if fleetValidate(fleetNamespaceType, raw) != nil || before == directClient(nil).privateConfiguration(fleetSnapshot(fleetNamespaceType, raw)) {
				t.Fatal("namespace deletion policy not bound")
			}
		})
	}
}

func TestFleetNativeIndexBoundaries(t *testing.T) {
	for _, kind := range fleetTestKinds {
		for _, mode := range []string{"paged", "partial-detail", "denied", "missing", "read-missing", "read-forbidden", "read-async", "wrong-id", "duplicate", "foreign-member", "foreign-page", "wrong-version", "filtered-page", "repeat-page", "alias", "partial", "missing-array", "private-drift", "malformed-row"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				name1, name2 := "resource1", "resource2"
				if kind == fleetGateType {
					name1, name2 = rbacTestRoleName, rbacTestAssignmentName
				}
				first, second := fleetTestBody(t, kind, name1), fleetTestBody(t, kind, name2)
				object(first["properties"])["privateSetting"] = "original"
				id1, id2 := text(first["id"]), text(second["id"])
				scope := fleetParent(id1, kind)
				if kind == fleetType {
					scope = "/subscriptions/" + testSubscription
				}
				lists, gets := 0, 0
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" || r.URL.Query().Get("api-version") != fleetAPIVersion(kind, "GET") || r.URL.Host != "management.azure.com" {
						t.Fatal("unrequested native operation", r.Method, r.URL)
					}
					path := strings.ToLower(r.URL.Path)
					if path == id1 || path == id2 {
						gets++
						if mode == "read-missing" || mode == "read-forbidden" {
							status, code := 404, "ResourceNotFound"
							if mode == "read-forbidden" {
								status, code = 403, "AuthorizationFailed"
							}
							return jsonResponse(status, map[string]any{"error": map[string]any{"code": code}}, nil), nil
						}
						body := first
						if path == id2 {
							body = second
						}
						var current map[string]any
						payload, _ := json.Marshal(body)
						json.Unmarshal(payload, &current)
						if mode == "private-drift" {
							object(current["properties"])["privateSetting"] = "changed"
						} else if mode == "partial-detail" {
							object(current["properties"])["additionalSetting"] = "only in GET"
						} else if mode == "wrong-id" {
							current["id"] = strings.Replace(path, "/fleet1/", "/another/", 1)
							if kind == fleetType {
								current["id"] = strings.ToLower(resourceID(fleetType, "another"))
							}
						}
						header := http.Header{}
						if mode == "read-async" {
							header.Set("Location", "https://management.azure.com/operations/pending")
						}
						return jsonResponse(200, current, header), nil
					}
					collection := scope + "/" + strings.ToLower(last(kind))
					if kind == fleetType {
						collection = scope + "/providers/" + strings.ToLower(fleetType)
					}
					if path != collection {
						t.Fatal("collection escaped", r.URL)
					}
					lists++
					if mode == "denied" || mode == "missing" {
						status, code := 403, "AuthorizationFailed"
						if mode == "missing" {
							status, code = 404, "ResourceNotFound"
						}
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": code}}, nil), nil
					}
					next := *r.URL
					q := next.Query()
					q.Set("$skiptoken", "opaque-continuation")
					next.RawQuery = q.Encode()
					body := map[string]any{"value": []any{first}, "nextLink": next.String()}
					if lists == 2 {
						body = map[string]any{"value": []any{second}}
					}
					switch mode {
					case "duplicate":
						body = map[string]any{"value": []any{first, first}}
					case "foreign-member":
						first["id"] = strings.Replace(id1, testSubscription, rbacOtherSubscription, 1)
					case "foreign-page":
						body["nextLink"] = "https://foreign.example/list?api-version=" + fleetVersion
					case "wrong-version":
						body["nextLink"] = strings.Replace(next.String(), fleetAPIVersion(kind, "GET"), "2025-03-01", 1)
					case "filtered-page":
						body["nextLink"] = next.String() + "&%24filter=group%20eq%20%27one%27"
					case "repeat-page":
						body["nextLink"] = r.URL.String()
					case "alias":
						body["NextLink"] = "hidden-page"
					case "partial":
						return jsonResponse(202, body, nil), nil
					case "missing-array":
						delete(body, "value")
					case "malformed-row":
						body["value"] = []any{"invalid"}
					}
					return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": []string{"fleet-native"}}), nil
				})
				index, _, err := c.fleetIndex(t.Context(), kind, scope)
				want := mode == "paged" || mode == "partial-detail"
				if (err == nil) != want || want && (len(index) != 2 || gets != 2 || lists != 2) {
					t.Fatal("native Fleet observation mismatch", mode, len(index), gets, lists, err)
				}
				if !want && isNotFound(err) {
					t.Fatal("incomplete index was converted to target absence")
				}
			})
		}
	}
}
