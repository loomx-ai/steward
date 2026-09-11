package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func communicationBody(t *testing.T, operation string) map[string]any {
	t.Helper()
	return object(object(object(communicationExample(t, operation)["responses"])["200"])["body"])
}

func communicationTestAccount(t *testing.T) map[string]any {
	t.Helper()
	raw := communicationBody(t, "CommunicationServices_Get")
	raw["id"], raw["name"] = strings.ToLower(resourceID(communicationType, "comms")), "comms"
	raw["systemData"] = map[string]any{"createdAt": "2025-03-27T09:48:53.6436183Z"}
	object(raw["properties"])["hostName"] = "comms.unitedstates.communication.azure.com"
	object(raw["properties"])["immutableResourceId"] = "11111111-2222-3333-4444-555555555555"
	return raw
}

func TestCommunicationOAuthAccountAuthority(t *testing.T) {
	for _, mode := range []string{"owned", "trailing-slash", "foreign", "foreign-subscription", "duplicate", "changed-host", "changed-setting", "missing-account", "forbidden-account", "wrong-parent-id", "insecure", "dns-suffix", "userinfo", "encoded-path", "version"} {
		t.Run(mode, func(t *testing.T) {
			r, err := NewRuntime(credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil }))
			if err != nil {
				t.Fatal(err)
			}
			account := communicationTestAccount(t)
			origin, _ := communicationAccountEndpoint(account)
			scopes := map[string]int{}
			dataCalls := 0
			r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "login.microsoftonline.com" {
					if err := req.ParseForm(); err != nil || req.Method != "POST" || req.URL.Path != "/"+testTenant+"/oauth2/v2.0/token" || req.Form.Get("client_id") != testApplication || req.Form.Get("client_secret") != "explicit-secret" || req.Form.Get("grant_type") != "client_credentials" {
						t.Fatal("unexpected credential flow")
					}
					scope := req.Form.Get("scope")
					if scope != armOrigin+"/.default" && scope != "https://communication.azure.com/.default" {
						t.Fatal("unexpected audience", scope)
					}
					scopes[scope]++
					return jsonResponse(200, map[string]any{"access_token": scope, "token_type": "Bearer", "expires_in": 3600}, nil), nil
				}
				if req.URL.Host == "management.azure.com" {
					if req.Header.Get("Authorization") != "Bearer "+armOrigin+"/.default" {
						t.Fatal("data audience reached ARM")
					}
					if req.URL.Path == "/subscriptions/"+testSubscription {
						return jsonResponse(200, map[string]any{"subscriptionId": testSubscription, "tenantId": testTenant, "state": "Enabled"}, nil), nil
					}
					if strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/Microsoft.Communication/communicationServices") {
						rows := []any{account}
						if mode == "foreign-subscription" {
							other := batchClone(account)
							other["id"] = strings.Replace(text(account["id"]), testSubscription, testTenant, 1)
							rows = []any{other}
						}
						if mode == "duplicate" {
							rows = append(rows, account)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), nil
					}
					if strings.EqualFold(req.URL.Path, text(account["id"])) {
						if mode == "missing-account" || mode == "forbidden-account" {
							status := 404
							if mode == "forbidden-account" {
								status = 403
							}
							return jsonResponse(status, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
						}
						current := batchClone(account)
						if mode == "changed-host" {
							object(current["properties"])["hostName"] = "different.communication.azure.com"
						}
						if mode == "changed-setting" {
							object(current["properties"])["disableLocalAuth"] = true
						}
						if mode == "wrong-parent-id" {
							current["id"] = text(account["id"]) + "-other"
						}
						return jsonResponse(200, current, nil), nil
					}
				}
				if req.URL.Scheme+"://"+req.URL.Host == origin && req.URL.Path == "/phoneNumbers" {
					dataCalls++
					if req.Header.Get("Authorization") != "Bearer https://communication.azure.com/.default" || req.URL.Query().Get("api-version") != communicationPhoneVersion {
						t.Fatal("wrong data audience or version")
					}
					return jsonResponse(200, communicationBody(t, "PhoneNumbers_ListPhoneNumbers"), nil), nil
				}
				t.Fatal("unexpected Communication request", req.URL.Host, req.URL.Path)
				return nil, nil
			})
			endpoint := origin
			switch mode {
			case "foreign":
				endpoint = "https://foreign.communication.azure.com"
			case "trailing-slash":
				endpoint += "/"
			case "insecure":
				endpoint = strings.Replace(endpoint, "https", "http", 1)
			case "dns-suffix":
				endpoint += ".example.org"
			case "userinfo":
				endpoint = strings.Replace(endpoint, "https://", "https://user@", 1)
			case "encoded-path":
				endpoint += "/%2f"
			}
			params := map[string]any{"endpoint": endpoint}
			if mode == "version" {
				params["api-version"] = "2020-01-01"
			}
			_, err = r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: communicationDataPrefix + "PhoneNumbers_ListPhoneNumbers", Parameters: params})
			if mode == "owned" || mode == "trailing-slash" {
				if err != nil || dataCalls != 1 || scopes["https://communication.azure.com/.default"] != 1 || scopes[armOrigin+"/.default"] != 1 {
					t.Fatal("valid data audience failed", scopes, dataCalls, err)
				}
			} else if err == nil || dataCalls != 0 || scopes["https://communication.azure.com/.default"] != 0 {
				t.Fatal("unowned endpoint obtained data credentials", scopes, dataCalls, err)
			}
		})
	}
}

func TestCommunicationNativeIdentityAndSnapshots(t *testing.T) {
	arm := map[string]string{"CommunicationServices_Get": communicationType, "EmailServices_Get": communicationEmailType, "Domains_Get": communicationDomainType, "SmtpUsernames_Get": communicationSMTPType, "SenderUsernames_Get": communicationSenderType, "SuppressionLists_Get": communicationSuppressionType, "SuppressionListAddresses_Get": communicationAddressType}
	for op, kind := range arm {
		raw := communicationBody(t, op)
		if err := communicationARMMetadata(kind, raw); err != nil {
			t.Fatal(op, err)
		}
		snapshot := communicationSnapshot(kind, raw)
		changed := batchClone(raw)
		object(changed["properties"])["futurePrivateField"] = "private-drift"
		if serviceParentConfiguration(kind, snapshot) == serviceParentConfiguration(kind, communicationSnapshot(kind, changed)) {
			t.Fatal("private future field was discarded", kind)
		}
	}
	for op, kind := range map[string]string{"PhoneNumbers_GetByNumber": communicationPhoneType, "PhoneNumbers_GetReservation": communicationReservationType, "Rooms_Get": communicationRoomType} {
		raw := communicationBody(t, op)
		path := "/rooms/" + text(raw["id"])
		if kind == communicationPhoneType {
			path = "/phoneNumbers/" + text(raw["phoneNumber"])
		}
		if kind == communicationReservationType {
			path = "/availablePhoneNumbers/reservations/" + text(raw["id"])
		}
		id := "https://comms.unitedstates.communication.azure.com" + path
		if err := communicationDataMetadata(kind, id, raw); err != nil {
			t.Fatal(op, err)
		}
		mapping, _ := findType(kind)
		operation, params, err := communicationDataOperation(mapping, id, "DELETE")
		if err != nil {
			t.Fatal(err)
		}
		request, err := bindAzureREST(operation, params)
		if err != nil || strings.Split(request.URL, "?")[0] != id {
			t.Fatal("native identity rebound", request.URL, err)
		}
		for _, invalid := range []string{id + "?api-version=2025-06-01", id + "/", id + "#fragment", strings.Replace(id, "https://", "https://user@", 1), strings.Replace(id, "communication.azure.com", "communication.azure.com.example.org", 1), strings.Replace(id, path, "/../"+strings.TrimPrefix(path, "/"), 1), strings.Replace(id, path, "/%2e%2e"+path, 1)} {
			if _, _, _, _, err := communicationDataIdentity(invalid); err == nil {
				t.Fatal("invalid data identity accepted", invalid)
			}
		}
		raw["id"] = "another"
		if communicationDataMetadata(kind, id, raw) == nil {
			t.Fatal("changed native identity accepted", kind)
		}
	}
	room := "https://comms.communication.azure.com/rooms/MixedCase"
	if id, _, _, _, err := communicationDataIdentity(room); err != nil || id != room {
		t.Fatal("room ID case changed", id, err)
	}
	secret := map[string]any{"properties": map[string]any{"email": "private-recipient", "username": "private-user", "verificationRecords": map[string]any{"Domain": "private-dns"}, "future": "private-future"}, "value": []any{map[string]any{"rawId": "private-member", "role": "Presenter"}}}
	for _, endpoint := range []string{armOrigin + resourceID(communicationDomainType, "d") + "?api-version=" + communicationARMVersion, "https://comms.communication.azure.com/rooms/r/participants?api-version=" + communicationRoomVersion} {
		payload, _ := json.Marshal(safeAPIPayload(secret, endpoint))
		if strings.Contains(string(payload), "private-") {
			t.Fatal("Communication details leaked", string(payload))
		}
	}
}

func TestCommunicationNativeReleaseReceipt(t *testing.T) {
	header := object(object(object(communicationExample(t, "PhoneNumbers_ReleasePhoneNumber")["responses"])["202"])["headers"])
	origin := "https://comms.communication.azure.com"
	operationID, location := text(header["operation-id"]), text(header["Operation-Location"])
	resolved, err := communicationReleaseOperation(origin, location, operationID)
	if err != nil || resolved != origin+location+"?api-version="+communicationPhoneVersion {
		t.Fatal("native release location rejected", resolved, err)
	}
	for _, altered := range []string{origin + location + "?api-version=2020-01-01", "https://foreign.communication.azure.com" + location, "//foreign.communication.azure.com" + location, location + "/next", location + "?api-version=" + communicationPhoneVersion + "&api-version=" + communicationPhoneVersion, location + "?secret=signed", location + "#fragment", strings.Replace(location, "/operations/", "/operations/%2f", 1), strings.TrimPrefix(location, "/")} {
		if _, err := communicationReleaseOperation(origin, altered, operationID); err == nil {
			t.Fatal("changed release receipt accepted", altered)
		}
	}
}

func TestCommunicationDataPaginationAndParticipants(t *testing.T) {
	for _, mode := range []string{"complete", "foreign", "relative", "wrong-collection", "wrong-version", "duplicate-query", "filter", "cycle", "missing-items", "forbidden", "duplicate-participant", "unknown-role"} {
		t.Run(mode, func(t *testing.T) {
			account := communicationTestAccount(t)
			origin, _ := communicationAccountEndpoint(account)
			page := communicationBody(t, "Participants_List")
			path := "/rooms/99199690362660524/participants"
			first := origin + path + "?api-version=" + communicationRoomVersion
			next := first + "&continuationToken=two"
			switch mode {
			case "foreign":
				next = strings.Replace(next, "comms.unitedstates", "other", 1)
			case "relative":
				next = "string"
			case "wrong-collection":
				next = strings.Replace(next, "/99199690362660524/", "/other/", 1)
			case "wrong-version":
				next = strings.Replace(next, communicationRoomVersion, communicationPhoneVersion, 1)
			case "duplicate-query":
				next += "&api-version=" + communicationRoomVersion
			case "filter":
				next += "&%24filter=limited"
			case "cycle":
				next = first
			case "duplicate-participant":
				page["value"] = []any{array(page["value"])[0], array(page["value"])[0]}
			case "unknown-role":
				object(array(page["value"])[0])["role"] = "UnknownFutureRole"
			}
			calls := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != strings.TrimPrefix(origin, "https://") || req.URL.Path != path {
					t.Fatal("pagination crossed account or room", req.URL.Host, req.URL.Path)
				}
				if mode == "forbidden" {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), nil
				}
				if req.URL.Query().Get("continuationToken") == "two" {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				body := batchClone(page)
				body["nextLink"] = next
				if mode == "missing-items" {
					delete(body, "value")
				}
				return jsonResponse(200, body, nil), nil
			})
			c, _ := r.resolve(t.Context(), "connection")
			values, err := c.communicationRoomParticipants(t.Context(), communicationAccountContext{id: text(account["id"]), endpoint: origin, raw: account}, origin+"/rooms/99199690362660524")
			if mode == "complete" {
				if err != nil || len(values) != 3 || calls != 2 {
					t.Fatal("native roster pagination", len(values), calls, err)
				}
			} else if err == nil || mode != "duplicate-participant" && mode != "unknown-role" && calls != 1 {
				t.Fatal("invalid pagination or roster accepted", calls, err)
			}
		})
	}
}

func TestCommunicationRecordedARMResponses(t *testing.T) {
	bytes, err := os.ReadFile("fixtures/communication/cli-recordings.json")
	var records []struct {
		Method, URL, Body string
		Status            int
	}
	if err != nil || json.Unmarshal(bytes, &records) != nil {
		t.Fatal("recordings missing", err)
	}
	resources := map[string]int{}
	for _, record := range records {
		if record.Method != "GET" || record.Status != 200 {
			continue
		}
		if strings.Contains(strings.ToLower(record.URL), "/operationstatuses/") {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(record.Body), &raw) != nil {
			t.Fatal("invalid recorded body")
		}
		if !strings.HasPrefix(strings.ToLower(text(raw["id"])), "/subscriptions/") {
			continue
		}
		_, kind, err := parseID(text(raw["id"]))
		kind = communicationKind(kind)
		if err != nil || kind == "" {
			t.Fatal("unknown recorded native resource", raw["id"])
		}
		if err := communicationARMMetadata(kind, raw); err != nil {
			t.Fatal("recorded ARM shape rejected", kind, err)
		}
		u, _ := url.Parse(record.URL)
		// CLI sanitizes group/account request segments independently of the
		// response body. Rebind only literal sanitizer markers in this test;
		// production identity validation remains exact.
		parts, actual := strings.Split(strings.ToLower(u.Path), "/"), strings.Split(strings.ToLower(text(raw["id"])), "/")
		if len(parts) != len(actual) {
			t.Fatal("recorded identity shape changed")
		}
		for i, part := range parts {
			if part == "sanitized" {
				parts[i] = actual[i]
			}
		}
		if !validResourceResponse(response{data: raw, status: 200}, strings.Join(parts, "/"), kind) {
			t.Fatal("recorded ARM identity rejected", kind)
		}
		resources[kind]++
	}
	if len(resources) != 5 {
		t.Fatal("CLI native resource coverage changed", resources)
	}
}

func TestCommunicationDataTransportRejectsChangedAccountBeforeOAuth(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("invalid account sent a data request")
		return nil, nil
	})
	c, _ := r.resolve(t.Context(), "connection")
	account := communicationTestAccount(t)
	origin, _ := communicationAccountEndpoint(account)
	for _, target := range []string{"https://foreign.communication.azure.com/rooms/r?api-version=" + communicationRoomVersion, origin + "/rooms/r?api-version=" + communicationPhoneVersion, origin + "/rooms/%2fr?api-version=" + communicationRoomVersion, origin + "/unselected?api-version=" + communicationRoomVersion} {
		_, err := c.communicationRequest(t.Context(), communicationAccountContext{id: text(account["id"]), endpoint: origin, raw: account}, catalog.RESTRequest{Method: "GET", URL: target})
		if err == nil {
			t.Fatal("invalid data transport boundary passed", target)
		}
	}
}
