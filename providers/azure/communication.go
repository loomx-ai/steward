package azure

import (
	"context"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	communicationType            = "Microsoft.Communication/communicationServices"
	communicationSMTPType        = communicationType + "/smtpUsernames"
	communicationPhoneType       = communicationType + "/phoneNumbers"
	communicationReservationType = communicationType + "/phoneNumberReservations"
	communicationRoomType        = communicationType + "/rooms"
	communicationEmailType       = "Microsoft.Communication/emailServices"
	communicationDomainType      = communicationEmailType + "/domains"
	communicationSenderType      = communicationDomainType + "/senderUsernames"
	communicationSuppressionType = communicationDomainType + "/suppressionLists"
	communicationAddressType     = communicationSuppressionType + "/suppressionListAddresses"
	communicationDataPrefix      = "Azure.Microsoft.Communication.DataPlane."
	communicationARMVersion      = "2026-03-18"
	communicationPhoneVersion    = "2025-06-01"
	communicationRoomVersion     = "2025-03-13"
)

func communicationKind(kind string) string {
	for _, candidate := range []string{communicationType, communicationSMTPType, communicationPhoneType, communicationReservationType, communicationRoomType, communicationEmailType, communicationDomainType, communicationSenderType, communicationSuppressionType, communicationAddressType} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}

func isCommunicationDataType(kind string) bool {
	return slices.Contains([]string{communicationPhoneType, communicationReservationType, communicationRoomType}, communicationKind(kind))
}

var communicationEndpointPattern = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\.communication\.azure\.com$`)
var communicationPhonePattern = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)
var communicationOpaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type communicationAccountContext struct {
	id, endpoint string
	raw          map[string]any
	historical   bool
}

func communicationAccountEndpoint(raw map[string]any) (string, error) {
	host := text(object(raw["properties"])["hostName"])
	if !communicationEndpointPattern.MatchString("https://" + host) {
		return "", serviceDenied("invalid_communication_account_endpoint")
	}
	return "https://" + host, nil
}

func (c *client) communicationAccount(ctx context.Context, id string) (communicationAccountContext, error) {
	parsed, typ, err := parseID(id)
	if err != nil || !strings.EqualFold(typ, communicationType) || !strings.HasPrefix(parsed, c.root()+"/") {
		return communicationAccountContext{}, serviceDenied("invalid_communication_account")
	}
	raw, err := c.linkedResource(ctx, parsed)
	if err != nil {
		return communicationAccountContext{}, err
	}
	if err := communicationARMMetadata(communicationType, raw); err != nil {
		return communicationAccountContext{}, err
	}
	endpoint, err := communicationAccountEndpoint(raw)
	if err != nil {
		return communicationAccountContext{}, err
	}
	return communicationAccountContext{id: parsed, endpoint: endpoint, raw: raw}, nil
}

// Phone numbers, reservations and rooms have native URLs, not ARM IDs. Room
// IDs are opaque and case-sensitive; never lowercase the data-plane path.
func communicationDataIdentity(value string) (id, kind, endpoint string, parameters map[string]any, err error) {
	invalid := func() (string, string, string, map[string]any, error) {
		return "", "", "", nil, serviceDenied("invalid_communication_data_identity")
	}
	u, e := url.Parse(value)
	if e != nil || value != strings.TrimSpace(value) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Port() != "" || !communicationEndpointPattern.MatchString(u.Scheme+"://"+u.Host) {
		return invalid()
	}
	parameters = map[string]any{"endpoint": u.Scheme + "://" + u.Host}
	parts := strings.Split(u.Path, "/")
	switch {
	case len(parts) == 3 && parts[1] == "phoneNumbers" && communicationPhonePattern.MatchString(parts[2]):
		kind, parameters["phoneNumber"] = communicationPhoneType, parts[2]
	case len(parts) == 4 && parts[1] == "availablePhoneNumbers" && parts[2] == "reservations" && uuidPattern.MatchString(parts[3]):
		kind, parameters["reservationId"] = communicationReservationType, parts[3]
	case len(parts) == 3 && parts[1] == "rooms" && len(parts[2]) <= 1024 && communicationOpaqueIDPattern.MatchString(parts[2]):
		kind, parameters["roomId"] = communicationRoomType, parts[2]
	default:
		return invalid()
	}
	return value, kind, text(parameters["endpoint"]), parameters, nil
}

func communicationDataOperation(kind resourceType, id, method string) (catalog.Operation, map[string]any, error) {
	_, typ, _, params, err := communicationDataIdentity(id)
	if err != nil || typ != kind.NativeType {
		return catalog.Operation{}, nil, serviceDenied("communication_data_type_mismatch")
	}
	ids := kind.ReadOperations
	if method == "DELETE" {
		ids = kind.DeleteOperations
	}
	if len(ids) != 1 {
		return catalog.Operation{}, nil, serviceDenied("invalid_communication_data_binding")
	}
	data, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := data.catalog.Operation(ids[0])
	if !ok || op.Call == nil || op.Call.Style != "azure-communication-rest" || op.Call.Method != method {
		return catalog.Operation{}, nil, serviceDenied("invalid_communication_data_operation")
	}
	if _, err := bindAzureREST(op, params); err != nil {
		return catalog.Operation{}, nil, err
	}
	return op, params, nil
}

func communicationVersion(path string) string {
	if path == "/rooms" || strings.HasPrefix(path, "/rooms/") {
		return communicationRoomVersion
	}
	if path == "/phoneNumbers" || strings.HasPrefix(path, "/phoneNumbers/") || path == "/availablePhoneNumbers/reservations" || strings.HasPrefix(path, "/availablePhoneNumbers/reservations/") {
		return communicationPhoneVersion
	}
	return ""
}

func communicationDataPath(path string) bool {
	if slices.Contains([]string{"/phoneNumbers", "/availablePhoneNumbers/reservations", "/rooms"}, path) {
		return true
	}
	if _, _, _, _, err := communicationDataIdentity("https://resource.communication.azure.com" + path); err == nil {
		return true
	}
	parts := strings.Split(path, "/")
	return len(parts) == 4 && (parts[1] == "rooms" && parts[3] == "participants" && len(parts[2]) <= 1024 && communicationOpaqueIDPattern.MatchString(parts[2]) || parts[1] == "phoneNumbers" && parts[2] == "operations" && len(parts[3]) <= 1024 && communicationOpaqueIDPattern.MatchString(parts[3]))
}

func (c *client) communicationRequest(ctx context.Context, account communicationAccountContext, request catalog.RESTRequest) (response, error) {
	id, typ, err := parseID(account.id)
	endpoint, endpointErr := communicationAccountEndpoint(account.raw)
	if err != nil || !strings.EqualFold(typ, communicationType) || id != account.id || !strings.HasPrefix(id, c.root()+"/") || endpointErr != nil || endpoint != account.endpoint || !strings.EqualFold(text(account.raw["id"]), id) {
		return response{}, serviceDenied("communication_account_context_missing")
	}
	if account.historical && request.Method != "GET" {
		return response{}, serviceDenied("communication_historical_context_read_only")
	}
	validate := func(value string) error {
		u, err := url.Parse(value)
		if err != nil || u.Scheme+"://"+u.Host != account.endpoint || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawPath != "" {
			return serviceDenied("communication_request_changed_account")
		}
		query, err := url.ParseQuery(u.RawQuery)
		version := communicationVersion(u.Path)
		if err != nil || version == "" || !communicationDataPath(u.Path) || len(query["api-version"]) != 1 || query.Get("api-version") != version {
			return serviceDenied("communication_request_changed_version")
		}
		return nil
	}
	return c.requestUsing(ctx, request.Method, request.URL, request.Body, request.Headers, validate, c.communicationHTTP, false)
}

func communicationARMMetadata(kind string, raw map[string]any) error {
	id, typ, err := parseID(responseID(kind, text(raw["id"])))
	if err != nil || typ != strings.ToLower(kind) || !validResponseType(kind, text(raw["type"])) {
		return serviceDenied("invalid_communication_arm_identity")
	}
	name := text(raw["name"])
	if !strings.EqualFold(name, last(id)) && !(kind == communicationSenderType && strings.EqualFold(name, last(communicationARMParent(id, kind)))) {
		return serviceDenied("invalid_communication_arm_name")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || props == nil {
		return serviceDenied("communication_properties_missing")
	}
	if kind == communicationType || kind == communicationEmailType || kind == communicationDomainType {
		if resourceRegion(raw) != "global" || text(raw["location"]) == "" || text(props["dataLocation"]) == "" {
			return serviceDenied("invalid_communication_location")
		}
		if !slices.Contains([]string{"Unknown", "Succeeded", "Failed", "Canceled", "Running", "Creating", "Updating", "Deleting", "Moving"}, text(props["provisioningState"])) {
			return serviceDenied("communication_state_unknown")
		}
	}
	return nil
}

func communicationSnapshot(kind string, raw map[string]any) map[string]any {
	result := batchClone(raw)
	if isCommunicationDataType(kind) {
		// Phone purchaseDate, reservation expiresAt and room createdAt bind
		// incarnations. Keep all authored and future fields, including rosters.
		if kind == communicationReservationType {
			delete(result, "status")
		}
		return result
	}
	result["id"] = responseID(kind, text(raw["id"]))
	result["name"] = last(text(result["id"]))
	delete(result, "type")
	delete(result, "etag")
	delete(result, "eTag")
	if kind != communicationType && kind != communicationEmailType && kind != communicationDomainType {
		delete(result, "location")
	}
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	if len(object(result["systemData"])) == 0 {
		delete(result, "systemData")
	}
	delete(object(result["properties"]), "provisioningState")
	if kind == communicationDomainType {
		delete(object(result["properties"]), "verificationStates")
	}
	if kind == communicationSuppressionType {
		delete(object(result["properties"]), "lastUpdatedTimeStamp")
	}
	return result
}

func communicationDataMetadata(kind, id string, raw map[string]any) error {
	_, typ, _, params, err := communicationDataIdentity(id)
	if err != nil || typ != kind || raw["error"] != nil {
		return serviceDenied("invalid_communication_data_response")
	}
	date := ""
	switch kind {
	case communicationPhoneType:
		if raw["phoneNumber"] != params["phoneNumber"] || text(raw["id"]) != strings.TrimPrefix(text(params["phoneNumber"]), "+") || object(raw["capabilities"]) == nil || object(raw["cost"]) == nil || text(raw["countryCode"]) == "" || text(raw["assignmentType"]) == "" || text(raw["phoneNumberType"]) == "" {
			return serviceDenied("invalid_communication_phone")
		}
		date = text(raw["purchaseDate"])
	case communicationReservationType:
		if raw["id"] != params["reservationId"] || object(raw["phoneNumbers"]) == nil || !slices.Contains([]string{"active", "submitted", "completed", "expired"}, text(raw["status"])) {
			return serviceDenied("invalid_communication_reservation")
		}
		date = text(raw["expiresAt"])
	case communicationRoomType:
		if raw["id"] != params["roomId"] {
			return serviceDenied("invalid_communication_room")
		}
		if _, ok := raw["pstnDialOutEnabled"].(bool); !ok {
			return serviceDenied("invalid_communication_room_settings")
		}
		from, e1 := time.Parse(time.RFC3339Nano, text(raw["validFrom"]))
		until, e2 := time.Parse(time.RFC3339Nano, text(raw["validUntil"]))
		if e1 != nil || e2 != nil || !until.After(from) {
			return serviceDenied("invalid_communication_room_validity")
		}
		date = text(raw["createdAt"])
	}
	if _, err := time.Parse(time.RFC3339Nano, date); err != nil {
		return serviceDenied("communication_incarnation_missing")
	}
	return nil
}

func (c *client) communicationDataRead(ctx context.Context, account communicationAccountContext, id, kind string) (response, error) {
	mapping, ok := findType(kind)
	if !ok {
		return response{}, serviceDenied("invalid_communication_kind")
	}
	op, params, err := communicationDataOperation(mapping, id, "GET")
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return response{}, err
	}
	result, err := c.communicationRequest(ctx, account, request)
	if err != nil {
		return result, err
	}
	if result.status != 200 || operationLocation(result.header) != "" {
		return result, serviceDenied("invalid_communication_read_status")
	}
	return result, communicationDataMetadata(kind, id, result.data)
}

// The public projection deliberately excludes recipient addresses, SMTP
// usernames, DNS verification values, room rosters and unknown future fields.
func communicationSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "location", "tags", "phoneNumber", "countryCode", "phoneNumberType", "assignmentType", "purchaseDate", "expiresAt", "status", "createdAt", "validFrom", "validUntil", "pstnDialOutEnabled", "request_id", "status_code"} {
			if entry, ok := value[key]; ok {
				result[key] = entry
			}
		}
		if props, ok := value["properties"].(map[string]any); ok {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "dataLocation", "domainManagement", "userEngagementTracking", "publicNetworkAccess", "disableLocalAuth"} {
				switch entry := props[key].(type) {
				case string, bool:
					public[key] = entry
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value", "phoneNumbers", "reservations"} {
			if entry, ok := value[key]; ok {
				result[key] = communicationSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = communicationSafeValue(entry)
		}
		return result
	default:
		return nil
	}
}

func communicationParameters(parameters map[string]any, endpoint string) map[string]any {
	params := maps.Clone(parameters)
	if params == nil {
		params = map[string]any{}
	}
	params["endpoint"] = endpoint
	return params
}

func communicationARMParent(id, kind string) string {
	if kind == communicationType || kind == communicationEmailType || isCommunicationDataType(kind) {
		return ""
	}
	parts := strings.Split(id, "/")
	if len(parts) < 11 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "/")
}
