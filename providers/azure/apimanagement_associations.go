package azure

import (
	"context"
	"maps"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func isAPIMAPI(kind string) bool {
	return strings.EqualFold(kind, apimAPIType) || strings.EqualFold(kind, apimWorkspaceType+"/apis")
}

func apimAssociationCollection(kind string) string {
	switch strings.ToLower(kind) {
	case strings.ToLower(apimServiceType + "/products/apis"), strings.ToLower(apimServiceType + "/gateways/apis"):
		return "apis"
	case strings.ToLower(apimServiceType + "/products/groups"):
		return "groups"
	case strings.ToLower(apimServiceType + "/groups/users"), strings.ToLower(apimWorkspaceType + "/groups/users"):
		return "users"
	case strings.ToLower(apimServiceType + "/notifications/recipientUsers"), strings.ToLower(apimWorkspaceType + "/notifications/recipientUsers"):
		return "users"
	case strings.ToLower(apimServiceType + "/apis/tags"), strings.ToLower(apimServiceType + "/apis/operations/tags"), strings.ToLower(apimServiceType + "/products/tags"):
		return "tags"
	}
	return ""
}

func apimAssociationTarget(id, kind string) string {
	if collection := apimAssociationCollection(kind); collection != "" {
		return apimRootID(id) + "/" + collection + "/" + last(id)
	}
	return ""
}

func isAPIMAssociation(kind string) bool {
	return apimAssociationCollection(kind) != "" || isAPIMRecipient(kind)
}

func isAPIMNotification(kind string) bool {
	return strings.EqualFold(kind, apimServiceType+"/notifications") || strings.EqualFold(kind, apimWorkspaceType+"/notifications")
}

func isAPIMRecipient(kind string) bool {
	end := strings.LastIndex(kind, "/")
	return end >= 0 && isAPIMNotification(kind[:end]) &&
		(strings.EqualFold(last(kind), "recipientUsers") || strings.EqualFold(last(kind), "recipientEmails"))
}

func apimNotificationRecipients(raw map[string]any, kinds []string, children []serviceChild) error {
	if !isAPIMNotification(text(raw["type"])) || object(raw["properties"])["recipients"] == nil {
		return nil
	}
	recipients, ok := object(raw["properties"])["recipients"].(map[string]any)
	if !ok || recipients == nil {
		return serviceDenied("invalid_apim_notification_recipients")
	}
	id, _, _ := parseID(text(raw["id"]))
	for _, kind := range kinds {
		field := "emails"
		if strings.EqualFold(last(kind), "recipientUsers") {
			field = "users"
		}
		value, present := recipients[field]
		if !present {
			continue
		}
		values, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_apim_notification_recipients")
		}
		expected := map[string]bool{}
		for _, value := range values {
			member, memberKind, err := parseID(text(value))
			if field == "users" {
				if err != nil || !strings.EqualFold(memberKind, apimServiceType+"/users") || apimRootID(member) != apimRootID(id) {
					return serviceDenied("invalid_apim_notification_user")
				}
				member = id + "/recipientusers/" + last(member)
			} else if err != nil || !strings.EqualFold(memberKind, kind) || redisParentID(member) != id {
				return serviceDenied("invalid_apim_notification_email")
			}
			if expected[member] {
				return serviceDenied("duplicate_apim_notification_recipient")
			}
			expected[member] = true
		}
		for _, child := range children {
			if !strings.EqualFold(child.kind, kind) {
				continue
			}
			if !expected[child.id] {
				return serviceDenied("apim_notification_recipients_disagree")
			}
			delete(expected, child.id)
		}
		if len(expected) != 0 {
			return serviceDenied("apim_notification_recipients_disagree")
		}
	}
	return nil
}

func resourceReadMethod(kind string) string {
	if isAPIMAssociation(kind) && !strings.EqualFold(last(kind), "tags") {
		return "HEAD"
	}
	return "GET"
}

// Legacy association lists expose either the association path or the member
// entity path. Bind that member to the requested parent only after validating
// both its native ID and name. Never select one side of a conflicting identity.
func apimAssociationRow(parent, kind string, raw map[string]any) (map[string]any, error) {
	id, actual, err := parseID(text(raw["id"]))
	name := text(raw["name"])
	association := parent + "/" + strings.ToLower(last(kind)) + "/" + last(id)
	target := apimAssociationTarget(association, kind)
	_, targetKind, _ := parseID(target)
	if isAPIMRecipient(kind) {
		if err != nil || id != association || !strings.EqualFold(name, last(id)) || !strings.EqualFold(actual, kind) || !strings.EqualFold(text(raw["type"]), kind) || object(raw["properties"]) == nil {
			return nil, serviceDenied("invalid_apim_recipient_identity")
		}
		if strings.EqualFold(last(kind), "recipientUsers") {
			user, err := apimReferenceID(apimRootID(id), "users", object(raw["properties"])["userId"])
			if err != nil || user != target {
				return nil, serviceDenied("apim_recipient_user_changed")
			}
		} else if !strings.EqualFold(text(object(raw["properties"])["email"]), name) {
			return nil, serviceDenied("apim_recipient_email_changed")
		}
		result := maps.Clone(raw)
		result["id"], result["name"], result["type"] = association, last(id), kind
		return result, nil
	}
	if err != nil || !isAPIMAssociation(kind) || !strings.EqualFold(name, last(id)) || object(raw["properties"]) == nil ||
		(!strings.EqualFold(text(raw["type"]), kind) && !strings.EqualFold(text(raw["type"]), targetKind)) {
		return nil, serviceDenied("invalid_apim_association_identity")
	}
	// The workspace GroupUser contract also returns workspace/users member
	// paths, although User_Get identifies users at service scope.
	workspaceUser := strings.EqualFold(kind, apimWorkspaceType+"/groups/users") && strings.EqualFold(id, apimNamespaceID(parent)+"/users/"+last(id))
	if id != association && id != target && !workspaceUser || id == association && !strings.EqualFold(actual, kind) {
		return nil, serviceDenied("apim_association_owner_changed")
	}
	result := maps.Clone(raw)
	result["id"], result["name"], result["type"] = association, last(id), kind
	return result, nil
}

// Ordinary resources use GET. HEAD-only APIM links use their actual native
// existence operation, a complete parent collection, and the member's GET.
func (c *client) readResource(ctx context.Context, endpoint string) (result response, err error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return response{}, err
	}
	id, kind, err := parseID(u.Path)
	if err != nil || !isAPIMAssociation(kind) {
		return c.request(ctx, "GET", endpoint)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["api-version"]) != 1 || query.Get("api-version") != apimVersion {
		return response{}, serviceDenied("invalid_apim_association_version")
	}
	method := resourceReadMethod(kind)
	first, err := c.request(ctx, method, endpoint)
	if err != nil {
		return first, err
	}
	defer func() { err = contracts.DependencyReadError(err) }()
	wantStatus := 204
	if strings.EqualFold(kind, apimServiceType+"/gateways/apis") || method == "GET" {
		wantStatus = 200
	}
	if first.status != wantStatus || method == "HEAD" && len(first.data) != 0 || len(first.header.Values("ETag")) > 1 {
		return response{}, serviceDenied("invalid_apim_association_response")
	}
	collection := strings.TrimSuffix(id, "/"+last(id))
	mapping, _ := findType(kind)
	parentKind, _ := findType(kind[:strings.LastIndex(kind, "/")])
	_, parameters, err := c.resourceOperation(parentKind, redisParentID(id), "GET")
	if err != nil {
		return response{}, err
	}
	metadata, err := providerData()
	if err != nil {
		return response{}, err
	}
	list, _ := metadata.catalog.Operation(mapping.ListOperations[0])
	bound, err := bindAzureREST(list, parameters)
	if err != nil {
		return response{}, err
	}
	rows, err := c.listAllURL(ctx, bound.URL, collection)
	if err != nil {
		return response{}, err
	}
	seen := map[string]bool{}
	var raw map[string]any
	var members []serviceChild
	for _, value := range rows {
		row, err := apimAssociationRow(redisParentID(id), kind, object(value))
		if err != nil {
			return response{}, err
		}
		key := text(row["id"])
		if seen[key] {
			return response{}, serviceDenied("duplicate_apim_association")
		}
		seen[key] = true
		members = append(members, serviceChild{kind: kind, id: key, data: row})
		if key == id {
			raw = row
		}
	}
	if raw == nil {
		return response{}, serviceDenied("apim_association_missing_from_list")
	}
	if isAPIMRecipient(kind) {
		parent, err := c.apimResource(ctx, redisParentID(id))
		if err != nil {
			return response{}, err
		}
		if err := apimNotificationRecipients(parent, []string{kind}, members); err != nil {
			return response{}, err
		}
	}
	if method == "GET" {
		detail, err := apimAssociationRow(redisParentID(id), kind, first.data)
		if err != nil {
			return response{}, err
		}
		if text(detail["id"]) != id {
			return response{}, serviceDenied("apim_association_identity_changed")
		}
		if err := apimListedIncarnation(kind, raw, detail); err != nil {
			return response{}, err
		}
		raw = detail
	}
	target := apimAssociationTarget(id, kind)
	targetConfiguration := ""
	if target != "" {
		live, err := c.apimResource(ctx, target)
		if err != nil {
			return response{}, err
		}
		_, targetKind, _ := parseID(target)
		if !isAPIMRecipient(kind) {
			listed := maps.Clone(raw)
			listed["id"], listed["name"], listed["type"] = target, last(target), targetKind
			if err := apimListedIncarnation(targetKind, listed, live); err != nil {
				return response{}, err
			}
		}
		targetConfiguration = c.privateConfiguration(map[string]any{"configuration": apimSnapshot(targetKind, live), "etag": apimETag(targetKind, live)})
	}
	second, err := c.request(ctx, method, endpoint)
	if err != nil {
		return response{}, err
	}
	if second.status != wantStatus || method == "HEAD" && len(second.data) != 0 || first.header.Get("ETag") != second.header.Get("ETag") {
		return response{}, serviceDenied("apim_association_changed")
	}
	if method == "GET" {
		after, err := apimAssociationRow(redisParentID(id), kind, second.data)
		if err != nil {
			return response{}, err
		}
		if c.privateConfiguration(apimSnapshot(kind, after)) != c.privateConfiguration(apimSnapshot(kind, raw)) {
			return response{}, serviceDenied("apim_association_changed")
		}
	}
	if targetConfiguration != "" {
		raw["_apim_association_target_configuration"] = targetConfiguration
	}
	second.data = raw
	if err := apimResponseETag("GET", &second); err != nil {
		return response{}, err
	}
	// A synthesized inventory row is not a claim that HEAD returned a body.
	// The native HTTP calls and request IDs remain in the cloud API log.
	second.status = 200
	return second, nil
}
