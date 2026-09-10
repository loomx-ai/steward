package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func apimReferenceID(namespace, collection string, value any) (string, error) {
	supplied, ok := value.(string)
	if !ok || supplied == "" || supplied != strings.TrimSpace(supplied) {
		return "", serviceDenied("invalid_apim_reference")
	}
	if strings.HasPrefix(supplied, "https://management.azure.com/") {
		u, err := url.Parse(supplied)
		if err != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return "", serviceDenied("invalid_apim_reference_url")
		}
		supplied = u.Path
	}
	if !strings.HasPrefix(strings.ToLower(supplied), "/subscriptions/") {
		if collection == "" {
			return "", serviceDenied("apim_reference_requires_arm_id")
		}
		if !strings.HasPrefix(supplied, "/") {
			supplied = "/" + collection + "/" + supplied
		}
		if !strings.HasPrefix(strings.ToLower(supplied), "/"+strings.ToLower(collection)+"/") {
			return "", serviceDenied("apim_reference_collection_changed")
		}
		supplied = namespace + supplied
	}
	id, kind, err := parseID(supplied)
	if err != nil {
		return "", serviceDenied("invalid_apim_reference_id")
	}
	if collection != "" && (!isAPIMType(kind) || !strings.EqualFold(last(kind), collection) || apimRootID(id) != apimRootID(namespace)) {
		return "", serviceDenied("apim_reference_owner_changed")
	}
	return id, nil
}
func apimReferences(kind, id string, raw map[string]any) ([]string, error) {
	if !isAPIMType(kind) {
		return nil, nil
	}
	namespace := apimNamespaceID(id)
	root := apimRootID(id)
	refs := apimAncestorIDs(id)
	if strings.EqualFold(kind, apimGatewayConnectionType) {
		source, err := apimGatewaySourceID(id, raw)
		if err != nil {
			return nil, err
		}
		return append(refs, source, apimRootID(source)), nil
	}
	if target := apimAssociationTarget(id, kind); target != "" {
		return append(refs, target), nil
	}
	if isAPIMAPI(kind) && apimBaseAPI(id) != id {
		refs = append(refs, apimBaseAPI(id))
	}
	add := func(namespace, collection string, value any) error {
		if value == nil || value == "" {
			return nil
		}
		ref, err := apimReferenceID(namespace, collection, value)
		if err != nil {
			return err
		}
		if ref != id {
			refs = append(refs, ref)
		}
		if _, typ, _ := parseID(ref); strings.EqualFold(typ, subnetType) {
			refs = append(refs, redisParentID(ref))
		}
		return nil
	}
	properties := object(raw["properties"])
	if strings.EqualFold(kind, apimGatewayType) && properties["backend"] != nil {
		backend := object(properties["backend"])
		if backend == nil {
			return nil, serviceDenied("invalid_apim_gateway_backend")
		}
		if backend["subnet"] != nil {
			subnet, subnetKind, err := parseID(text(object(backend["subnet"])["id"]))
			if err != nil || !strings.EqualFold(subnetKind, subnetType) {
				return nil, serviceDenied("invalid_apim_gateway_subnet")
			}
			if err := add("", "", subnet); err != nil {
				return nil, err
			}
		}
	}
	for ref := range object(object(raw["identity"])["userAssignedIdentities"]) {
		if err := add("", "", ref); err != nil {
			return nil, err
		}
	}
	if strings.EqualFold(last(kind), "subscriptions") {
		if err := add(root, "users", properties["ownerId"]); err != nil {
			return nil, err
		}
		if scope := text(properties["scope"]); scope != "/apis" && !strings.EqualFold(scope, namespace+"/apis") {
			collection := "products"
			if strings.Contains(strings.ToLower(scope), "/apis/") {
				collection = "apis"
			}
			if err := add(namespace, collection, properties["scope"]); err != nil {
				return nil, err
			}
		}
	}
	if strings.EqualFold(last(kind), "certificateAuthorities") {
		if err := add(root, "certificates", last(id)); err != nil {
			return nil, err
		}
	}
	if strings.EqualFold(last(kind), "operationLinks") {
		if err := add(namespace, "operations", properties["operationId"]); err != nil {
			return nil, err
		}
	}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				targetNamespace, collection := namespace, ""
				switch key {
				case "backendId":
					collection = "backends"
				case "apiVersionSetId":
					collection = "apiVersionSets"
				case "loggerId":
					collection = "loggers"
				case "certificateId", "clientCertificateId":
					collection = "certificates"
				case "apiId":
					collection = "apis"
				case "groupId":
					collection = "groups"
				case "productId":
					collection = "products"
				case "authorizationServerId":
					targetNamespace, collection = root, "authorizationServers"
				case "openidProviderId":
					targetNamespace, collection = root, "openidConnectProviders"
				case "resourceId", "subnetResourceId", "publicIpAddressId":
				case "privateEndpoint":
					if child != nil {
						if object(child) == nil {
							return serviceDenied("invalid_apim_private_endpoint")
						}
						if err := add("", "", object(child)["id"]); err != nil {
							return err
						}
					}
					continue
				case "value", "schema", "document", "content", "body", "parameters", "headers", "query", "credentials", "authenticationSettings", "oauth2", "serviceFabricCluster":
					continue
				default:
					if err := walk(child); err != nil {
						return err
					}
					continue
				}
				if err := add(targetNamespace, collection, child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(properties); err != nil {
		return nil, err
	}
	if strings.EqualFold(last(kind), "backends") {
		if pool := properties["pool"]; pool != nil {
			members, ok := object(pool)["services"].([]any)
			if !ok {
				return nil, serviceDenied("invalid_apim_backend_pool")
			}
			for _, member := range members {
				if text(object(member)["id"]) == "" {
					return nil, serviceDenied("invalid_apim_backend_pool_member")
				}
				if err := add(namespace, "backends", object(member)["id"]); err != nil {
					return nil, err
				}
			}
		}
		if err := add(namespace, "certificates", object(object(properties["properties"])["serviceFabricCluster"])["clientCertificateId"]); err != nil {
			return nil, err
		}
		if values := object(properties["credentials"])["certificateIds"]; values != nil {
			certificates, ok := values.([]any)
			if !ok {
				return nil, serviceDenied("invalid_apim_certificate_references")
			}
			for _, certificate := range certificates {
				if err := add(namespace, "certificates", certificate); err != nil {
					return nil, err
				}
			}
		}
	}
	if isAPIMAPI(kind) {
		if err := walk(object(properties["authenticationSettings"])); err != nil {
			return nil, err
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}
func apimPrivateEndpointID(raw map[string]any) (string, error) {
	target := object(object(raw["properties"])["privateEndpoint"])
	id, kind, err := parseID(text(target["id"]))
	if err != nil || !strings.EqualFold(kind, privateEndpointType) {
		return "", serviceDenied("invalid_apim_private_endpoint_target")
	}
	return id, nil
}
func (c *client) apimVerifyTarget(ctx context.Context, planned asset.Asset, raw map[string]any, locks []any) error {
	if planned.Identity.NativeType == apimIssueType {
		index, err := c.apimIssues(ctx, apimRootID(planned.Identity.NativeID))
		if err != nil {
			return err
		}
		current, exists := index[planned.Identity.NativeID]
		if !exists || apimETag(apimIssueType, current.data) != apimETag(apimIssueType, raw) || c.privateConfiguration(apimSnapshot(apimIssueType, current.data)) != c.privateConfiguration(apimSnapshot(apimIssueType, raw)) {
			return serviceDenied("apim_issue_projection_target_changed")
		}
		return nil
	}
	if strings.EqualFold(planned.Identity.NativeType, apimGatewayConnectionType) {
		configuration, err := c.apimGatewaySourceConfiguration(ctx, planned.Identity.NativeID, raw)
		if err != nil {
			return err
		}
		if expected := text(planned.Normalized["_apim_gateway_source_configuration"]); expected == "" || expected != configuration {
			return serviceDenied("apim_gateway_source_changed")
		}
		return nil
	}
	if planned.Identity.NativeType != apimServiceType+"/privateEndpointConnections" {
		return nil
	}
	id, err := apimPrivateEndpointID(raw)
	if err != nil {
		return err
	}
	live, err := c.linkedResource(ctx, id)
	if err != nil {
		return err
	}
	expected := text(planned.Normalized["_apim_private_endpoint_configuration"])
	if expected == "" || expected != c.privateConfiguration(searchTargetSnapshot(live)) {
		return serviceDenied("apim_private_endpoint_changed")
	}
	if err := c.linkedResourceProtection(ctx, id, live, locks); err != nil {
		return err
	}
	current, err := c.linkedResource(ctx, id)
	if err != nil {
		return err
	}
	if expected != c.privateConfiguration(searchTargetSnapshot(current)) {
		return serviceDenied("apim_private_endpoint_changed")
	}
	return nil
}
