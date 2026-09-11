package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var communicationTestKinds = []string{communicationType, communicationSMTPType, communicationPhoneType, communicationReservationType, communicationRoomType, communicationEmailType, communicationDomainType, communicationSenderType, communicationSuppressionType, communicationAddressType}

type communicationFixture struct {
	runtime      *Runtime
	resources    map[string]map[string]any
	kinds        map[string]string
	ids          map[string]string
	participants map[string][]any
	group        map[string]any
	locks        []any
	omitted      map[string]bool
	calls        map[string]int
	override     func(*http.Request) (*http.Response, bool)
}

func newCommunicationFixture(t *testing.T) *communicationFixture {
	t.Helper()
	f := &communicationFixture{resources: map[string]map[string]any{}, kinds: map[string]string{}, ids: map[string]string{}, participants: map[string][]any{}, locks: []any{}, omitted: map[string]bool{}, calls: map[string]int{}}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/test"
	f.group = map[string]any{"id": group, "type": groupType, "name": "test", "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	comm := communicationTestAccount(t)
	commID := text(comm["id"])
	emailID := strings.ToLower(resourceID(communicationEmailType, "email1"))
	domainID := emailID + "/domains/example.org"
	listID := domainID + "/suppressionlists/aaaa1111-bbbb-2222-3333-aaaa11112222"
	f.ids = map[string]string{communicationType: commID, communicationSMTPType: commID + "/smtpusernames/smtp1", communicationEmailType: emailID, communicationDomainType: domainID, communicationSenderType: domainID + "/senderusernames/news", communicationSuppressionType: listID, communicationAddressType: listID + "/suppressionlistaddresses/11112222-3333-4444-5555-aaaabbbbcccc"}
	for kind, op := range map[string]string{communicationType: "CommunicationServices_Get", communicationSMTPType: "SmtpUsernames_Get", communicationEmailType: "EmailServices_Get", communicationDomainType: "Domains_Get", communicationSenderType: "SenderUsernames_Get", communicationSuppressionType: "SuppressionLists_Get", communicationAddressType: "SuppressionListAddresses_Get"} {
		raw := communicationBody(t, op)
		if kind == communicationType {
			raw = comm
		}
		id := f.ids[kind]
		raw["id"], raw["name"] = id, last(id)
		if kind == communicationType || kind == communicationEmailType || kind == communicationDomainType {
			raw["systemData"] = map[string]any{"createdAt": "2025-03-27T09:48:53.6436183Z"}
		}
		object(raw["properties"])["futurePrivateSetting"] = "private-native-configuration"
		f.resources[id], f.kinds[id] = raw, kind
	}
	object(comm["properties"])["linkedDomains"] = []any{domainID}
	origin, _ := communicationAccountEndpoint(comm)
	for kind, op := range map[string]string{communicationPhoneType: "PhoneNumbers_GetByNumber", communicationReservationType: "PhoneNumbers_GetReservation", communicationRoomType: "Rooms_Get"} {
		raw := communicationBody(t, op)
		path := "/rooms/" + text(raw["id"])
		if kind == communicationPhoneType {
			path = "/phoneNumbers/" + text(raw["phoneNumber"])
		}
		if kind == communicationReservationType {
			path = "/availablePhoneNumbers/reservations/" + text(raw["id"])
		}
		id := origin + path
		raw["futurePrivateSetting"] = "private-native-configuration"
		f.ids[kind], f.resources[id], f.kinds[id] = id, raw, kind
		if kind == communicationRoomType {
			f.participants[id] = array(communicationBody(t, "Participants_List")["value"])
		}
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		key := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
		if req.URL.Host == "management.azure.com" {
			key = strings.ToLower(req.URL.Path)
		}
		f.calls[req.Method+" "+key]++
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		if req.Method != "GET" {
			t.Fatal("unrequested Communication operation", req.Method, req.URL.Host, req.URL.Path)
		}
		version := communicationARMVersion
		if req.URL.Host != "management.azure.com" {
			version = communicationVersion(req.URL.Path)
		}
		if key == group || key == root+"/resourcegroups" {
			version = resourcesVersion
		}
		if key == root+"/providers/microsoft.authorization/locks" {
			version = locksVersion
		}
		if req.URL.Query().Get("api-version") != version || len(req.URL.Query()) != 1 {
			t.Fatal("Communication native request changed", req.URL.Host, req.URL.Path, req.URL.RawQuery)
		}
		if key == group {
			return jsonResponse(200, f.group, nil), nil
		}
		if key == root+"/resourcegroups" {
			return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), nil
		}
		if key == root+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if raw := f.resources[key]; raw != nil {
			return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"communication-native-read"}}), nil
		}
		if strings.HasSuffix(req.URL.Path, "/participants") {
			room := strings.TrimSuffix(key, "/participants")
			if f.resources[room] == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": f.participants[room]}, nil), nil
		}
		if _, _, _, _, err := communicationDataIdentity(key); err == nil {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), nil
		}
		if _, typ, err := parseID(key); err == nil && communicationKind(typ) != "" {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		values := []any{}
		for _, id := range slices.Sorted(maps.Keys(f.resources)) {
			kind := f.kinds[id]
			collection := id[:strings.LastIndex(id, "/")]
			if kind == communicationType || kind == communicationEmailType {
				collection = root + "/providers/" + strings.ToLower(kind)
			}
			if key != collection || f.omitted[id] {
				continue
			}
			raw := batchClone(f.resources[id])
			if kind == communicationReservationType {
				delete(raw, "phoneNumbers")
			}
			values = append(values, raw)
		}
		field := "value"
		if req.URL.Path == "/phoneNumbers" {
			field = "phoneNumbers"
		}
		if req.URL.Path == "/availablePhoneNumbers/reservations" {
			field = "reservations"
		}
		return jsonResponse(200, map[string]any{field: values}, http.Header{"X-Ms-Request-Id": {"communication-native-index"}}), nil
	})
	return f
}

func (f *communicationFixture) request(kind string) contracts.InventoryRequest {
	resourceKind := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: communicationInventorySource, ResourceKind: &resourceKind, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}}
}

func TestCommunicationRegisteredInventory(t *testing.T) {
	for _, kind := range communicationTestKinds {
		t.Run(last(kind), func(t *testing.T) {
			f := newCommunicationFixture(t)
			batch, err := f.runtime.List(t.Context(), f.request(kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("Communication native inventory failed", batch, err)
			}
			item := batch.Items[0]
			if item.NativeID != f.ids[kind] || item.NativeType != kind || item.Location != "global" || item.Scope.Kind != asset.ScopeGlobal || item.Actionable == nil || !*item.Actionable {
				t.Fatal("native inventory context changed", item)
			}
			if f.calls["GET "+item.NativeID] < 2 {
				t.Fatal("resource own GET not repeated", item.NativeID)
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			members, err := c.communicationRecorded(item.NativeID, kind, item.Normalized)
			if err != nil {
				t.Fatal("inventory cannot authenticate native plan", err)
			}
			if kind == communicationType && len(members) != 4 || kind == communicationEmailType && len(members) != 4 || kind == communicationDomainType && len(members) != 3 || kind == communicationSuppressionType && len(members) != 1 {
				t.Fatal("native manifest lost descendants", kind, len(members))
			}
			if kind == communicationType && !slices.Contains(item.NetworkReferences, f.ids[communicationDomainType]) {
				t.Fatal("linked email domain lost")
			}
			if isCommunicationDataType(kind) && !slices.Contains(item.NetworkReferences, f.ids[communicationType]) {
				t.Fatal("data account reference lost")
			}
			payload, _ := json.Marshal(batch)
			for _, secret := range []string{"private-native-configuration", "futurePrivateSetting", "newuser1@", "rawId", "recordValue", "updatedFirstName"} {
				if strings.Contains(string(payload), secret) {
					t.Fatal("private Communication inventory persisted", secret)
				}
			}
			request := f.request(kind)
			request.KnownNativeIDs, request.KnownNativeMetadata = []string{item.NativeID}, map[string]map[string]any{item.NativeID: item.Normalized}
			f.omitted[item.NativeID] = true
			if strings.Contains(kind, communicationEmailType) {
				f.omitted[f.ids[communicationEmailType]] = true
			} else {
				f.omitted[f.ids[communicationType]] = true
			}
			rescanned, err := f.runtime.List(t.Context(), request)
			if err != nil || len(rescanned.Items) != 1 || len(rescanned.AbsentNativeIDs) != 0 {
				t.Fatal("omitted known native ID was lost", rescanned, err)
			}
		})
	}
}

func TestCommunicationKnownAbsenceNeedsEveryNamedRead(t *testing.T) {
	for _, mode := range []string{"leaf-absent", "missing-root-live-phone", "root-and-children-absent", "phone-forbidden", "forged-endpoint", "missing-proof", "unrelated-metadata", "duplicate-known"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			kind := communicationType
			if mode == "leaf-absent" {
				kind = communicationRoomType
			}
			request := f.request(kind)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			item := batch.Items[0]
			request.KnownNativeIDs, request.KnownNativeMetadata = []string{item.NativeID}, map[string]map[string]any{item.NativeID: item.Normalized}
			f.calls = map[string]int{}
			if mode == "leaf-absent" {
				delete(f.resources, item.NativeID)
			} else {
				for id, typ := range f.kinds {
					if communicationRootKind(typ) == communicationType {
						delete(f.resources, id)
					}
				}
			}
			switch mode {
			case "missing-root-live-phone":
				f.resources[f.ids[communicationPhoneType]] = communicationBody(t, "PhoneNumbers_GetByNumber")
			case "phone-forbidden":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Scheme+"://"+req.URL.Host+req.URL.Path == f.ids[communicationPhoneType] {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return nil, false
				}
			case "forged-endpoint":
				item.Normalized["_communication_endpoint"] = "https://foreign.communication.azure.com"
			case "missing-proof":
				delete(item.Normalized, communicationProof)
			case "unrelated-metadata":
				request.KnownNativeMetadata[item.NativeID+"-other"] = item.Normalized
			case "duplicate-known":
				request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
			}
			result, err := f.runtime.List(t.Context(), request)
			if mode == "leaf-absent" || mode == "root-and-children-absent" {
				if err != nil || !result.Complete || !slices.Equal(result.AbsentNativeIDs, []string{item.NativeID}) || len(result.Items) != 0 {
					t.Fatal("named absence reconciliation failed", result, err)
				}
				if mode == "root-and-children-absent" {
					for id, typ := range f.kinds {
						if communicationRootKind(typ) == communicationType && f.calls["GET "+id] < 2 {
							t.Fatal("root 404 substituted for named child absence", typ)
						}
					}
				}
			} else if err == nil || result.Complete || len(result.AbsentNativeIDs) != 0 {
				t.Fatal("unproven absence succeeded", result, err)
			}
		})
	}
}

func TestCommunicationInventoryProtectionAndDrift(t *testing.T) {
	for _, mode := range []string{"account-tag", "group-tag", "managed-group", "lock", "purchase-submitted", "creation-missing", "parent-drift", "participant-drift", "phone-drift", "collection-403", "duplicate-room", "missing-items"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			kind := communicationRoomType
			expected := ""
			switch mode {
			case "account-tag":
				f.resources[f.ids[communicationType]]["tags"] = map[string]any{"steward:protected": "true"}
				expected = "azure_protected_tag"
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
				expected = "azure_protected_tag"
			case "managed-group":
				f.group["managedBy"] = resourceID(aksType, "cluster")
				expected = "azure_managed_resource_group"
			case "lock":
				f.locks = []any{map[string]any{"id": f.ids[communicationType] + "/providers/Microsoft.Authorization/locks/l", "properties": map[string]any{"level": "CanNotDelete"}}}
				expected = "azure_management_lock"
			case "purchase-submitted":
				kind = communicationReservationType
				f.resources[f.ids[kind]]["status"] = "submitted"
				expected = "azure_communication_purchase_in_progress"
			case "creation-missing":
				delete(f.resources[f.ids[communicationType]], "systemData")
				expected = "azure_communication_creation_unverified"
			default:
				f.override = func(req *http.Request) (*http.Response, bool) {
					key := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
					if req.URL.Host == "management.azure.com" {
						key = strings.ToLower(req.URL.Path)
					}
					switch mode {
					case "parent-drift":
						if key == f.ids[communicationType] && f.calls["GET "+key] >= 2 {
							raw := batchClone(f.resources[key])
							object(raw["properties"])["futurePrivateSetting"] = "changed"
							return jsonResponse(200, raw, nil), true
						}
					case "participant-drift":
						if strings.HasSuffix(key, "/participants") && f.calls["GET "+key] >= 2 {
							return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
						}
					case "phone-drift":
						if key == f.ids[communicationPhoneType] {
							raw := batchClone(f.resources[key])
							raw["purchaseDate"] = "2026-01-01T00:00:00Z"
							return jsonResponse(200, raw, nil), true
						}
					case "collection-403":
						if req.URL.Path == "/rooms" {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
						}
					case "duplicate-room":
						if req.URL.Path == "/rooms" {
							return jsonResponse(200, map[string]any{"value": []any{f.resources[f.ids[communicationRoomType]], f.resources[f.ids[communicationRoomType]]}}, nil), true
						}
					case "missing-items":
						if req.URL.Path == "/rooms" {
							return jsonResponse(200, map[string]any{}, nil), true
						}
					}
					return nil, false
				}
			}
			result, err := f.runtime.List(t.Context(), f.request(kind))
			if expected != "" {
				if err != nil || len(result.Items) != 1 || result.Items[0].Actionable == nil || *result.Items[0].Actionable || result.Items[0].Normalized["cleanup_protection_reason"] != expected {
					t.Fatal("native protection lost", result, err)
				}
			} else if err == nil || result.Complete {
				t.Fatal("unstable native observation succeeded", result, err)
			}
		})
	}
}

func TestCommunicationInventoryCursorBindsFullContext(t *testing.T) {
	for _, mode := range []string{"resume", "private-parent", "private-roster", "changed-scope", "changed-connection", "forged-cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			id := f.ids[communicationRoomType] + "2"
			raw := batchClone(f.resources[f.ids[communicationRoomType]])
			raw["id"] = last(id)
			f.resources[id], f.kinds[id], f.participants[id] = raw, communicationRoomType, []any{}
			request := f.request(communicationRoomType)
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 {
				t.Fatal("first bounded page failed", first, err)
			}
			request.Cursor = first.NextCursor
			switch mode {
			case "private-parent":
				object(f.resources[f.ids[communicationType]]["properties"])["futurePrivateSetting"] = "changed"
			case "private-roster":
				object(f.participants[f.ids[communicationRoomType]][0])["role"] = "Consumer"
			case "changed-scope":
				request.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}
			case "changed-connection":
				request.ConnectionID = "changed"
				f.runtime.credentials = credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil })
			case "forged-cursor":
				request.Cursor = "forged"
			}
			second, err := f.runtime.List(t.Context(), request)
			if mode == "resume" {
				if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
					t.Fatal("bounded native continuation failed", second, err)
				}
			} else if err == nil || second.Complete {
				t.Fatal("changed cursor boundary succeeded", second, err)
			}
		})
	}
}
