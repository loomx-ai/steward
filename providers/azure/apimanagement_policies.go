package azure

import (
	"context"
	"encoding/xml"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

var apimNamedValuePattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

func apimPolicyExpression(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "@(") || strings.Contains(value, "@{")
}

func (c *client) apimReferenceCollection(ctx context.Context, owner, name string, indexes map[string][]serviceChild) ([]serviceChild, error) {
	_, namespaceKind, _ := parseID(owner)
	key := owner + "/" + strings.ToLower(name)
	if values, exists := indexes[key]; exists {
		return values, nil
	}
	parent, err := c.apimResource(ctx, owner)
	if err != nil {
		return nil, err
	}
	if err := apimReady(namespaceKind, parent); err != nil {
		return nil, err
	}
	mapping, exists := findType(namespaceKind + "/" + name)
	if !exists {
		return nil, serviceDenied("unsupported_apim_reference_collection")
	}
	values, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: owner, NativeType: namespaceKind}, parent, []string{mapping.NativeType})
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if err := apimReady(value.kind, value.data); err != nil {
			return nil, err
		}
	}
	after, err := c.apimResource(ctx, owner)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(apimSnapshot(namespaceKind, parent)) != c.privateConfiguration(apimSnapshot(namespaceKind, after)) {
		return nil, serviceDenied("apim_reference_parent_changed")
	}
	indexes[key] = values
	return values, nil
}

// Both selectors may be expressions. A literal connection ID is scoped to its
// provider; dynamic selectors retain all current possibilities for review.
func (c *client) apimAuthorizationReferences(ctx context.Context, root string, attributes map[string]string, indexes map[string][]serviceChild) ([]string, error) {
	provider, authorization := attributes["provider-id"], attributes["authorization-id"]
	if provider == "" || authorization == "" {
		return nil, serviceDenied("invalid_apim_policy_authorization")
	}
	var providers []string
	if apimPolicyExpression(provider) {
		values, err := c.apimReferenceCollection(ctx, root, "authorizationProviders", indexes)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			providers = append(providers, value.id)
		}
	} else {
		id, err := apimReferenceID(root, "authorizationProviders", provider)
		if err != nil {
			return nil, err
		}
		if redisParentID(id) != root {
			return nil, serviceDenied("apim_authorization_provider_owner_changed")
		}
		providers = []string{id}
	}
	var refs []string
	for _, provider := range providers {
		if apimPolicyExpression(authorization) {
			values, err := c.apimReferenceCollection(ctx, provider, "authorizations", indexes)
			if err != nil {
				return nil, err
			}
			for _, value := range values {
				refs = append(refs, value.id)
			}
		} else {
			id, err := apimReferenceID(provider, "authorizations", authorization)
			if err != nil {
				return nil, err
			}
			if redisParentID(id) != provider {
				return nil, serviceDenied("apim_policy_authorization_owner_changed")
			}
			refs = append(refs, id)
		}
	}
	return refs, nil
}

// Resolve named values by displayName, never by guessing their resource ID.
// Expression-valued identifiers may select any current resource in the named
// collection. Reviewing the policy as a prerequisite preserves that uncertainty.
// No policy expression, external XML link or named-value secret is executed.
func (c *client) apimResolvedReferences(ctx context.Context, kind, id string, raw map[string]any, indexes map[string][]serviceChild, external map[string][]string) ([]string, error) {
	refs, err := apimReferences(kind, id, raw)
	if err != nil {
		return nil, err
	}
	if indexes == nil {
		indexes = map[string][]serviceChild{}
	}
	if external == nil {
		external = map[string][]string{}
	}
	if err := c.apimExternalReferences(ctx, kind, raw, indexes, external); err != nil {
		return nil, err
	}
	namespace := apimNamespaceID(id)
	collection := func(name string) ([]serviceChild, error) {
		owner := namespace
		if name == "authorizationProviders" {
			owner = apimRootID(id)
		}
		return c.apimReferenceCollection(ctx, owner, name, indexes)
	}
	named := func(value string) error {
		matches := apimNamedValuePattern.FindAllStringSubmatch(value, -1)
		if len(matches) == 0 {
			return nil
		}
		values, err := collection("namedValues")
		if err != nil {
			return err
		}
		for _, match := range matches {
			for _, candidate := range values {
				if text(object(candidate.data["properties"])["displayName"]) == match[1] {
					refs = append(refs, candidate.id)
				}
			}
		}
		return nil
	}
	add := func(name, value string) error {
		if apimPolicyExpression(value) {
			values, err := collection(name)
			if err != nil {
				return err
			}
			for _, candidate := range values {
				refs = append(refs, candidate.id)
			}
			return nil
		}
		owner := namespace
		if name == "authorizationProviders" {
			owner = apimRootID(id)
		}
		ref, err := apimReferenceID(owner, name, value)
		if err == nil {
			refs = append(refs, ref)
		}
		return err
	}
	thumbprint := func(value string) error {
		if value == "" {
			return serviceDenied("invalid_apim_certificate_thumbprint")
		}
		values, err := collection("certificates")
		if err != nil {
			return err
		}
		for _, candidate := range values {
			if apimPolicyExpression(value) || strings.EqualFold(text(object(candidate.data["properties"])["thumbprint"]), value) {
				refs = append(refs, candidate.id)
			}
		}
		return nil
	}
	props := object(raw["properties"])
	switch strings.ToLower(last(kind)) {
	case "backends", "loggers":
		var visit func(any) error
		visit = func(value any) error {
			switch value := value.(type) {
			case string:
				return named(value)
			case map[string]any:
				for _, nested := range value {
					if err := visit(nested); err != nil {
						return err
					}
				}
			case []any:
				for _, nested := range value {
					if err := visit(nested); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := visit(props["credentials"]); err != nil {
			return nil, err
		}
		if strings.EqualFold(last(kind), "backends") {
			credentials := object(props["credentials"])
			if credentials["certificateIds"] == nil && credentials["certificate"] != nil {
				values, ok := credentials["certificate"].([]any)
				if !ok {
					return nil, serviceDenied("invalid_apim_certificate_thumbprints")
				}
				for _, value := range values {
					if err := thumbprint(text(value)); err != nil {
						return nil, err
					}
				}
			}
			cluster := object(object(props["properties"])["serviceFabricCluster"])
			if text(cluster["clientCertificateId"]) == "" && cluster["clientCertificatethumbprint"] != nil {
				if err := thumbprint(text(cluster["clientCertificatethumbprint"])); err != nil {
					return nil, err
				}
			}
		}
	case "policies", "policyfragments":
		value, ok := props["value"].(string)
		if !ok || value == "" {
			return nil, serviceDenied("missing_apim_policy_definition")
		}
		if strings.HasSuffix(text(props["format"]), "-link") {
			return nil, serviceDenied("apim_policy_requires_inline_read")
		}
		decoder := xml.NewDecoder(strings.NewReader(value))
		depth, roots := 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, serviceDenied("invalid_apim_policy_xml")
			}
			switch token := token.(type) {
			case xml.StartElement:
				if depth == 0 {
					roots++
				}
				depth++
				attributes := map[string]string{}
				for _, attr := range token.Attr {
					attributes[attr.Name.Local] = attr.Value
					if err := named(attr.Value); err != nil {
						return nil, err
					}
					name := ""
					switch token.Name.Local + "/" + attr.Name.Local {
					case "set-backend-service/backend-id":
						name = "backends"
					case "include-fragment/fragment-id":
						name = "policyFragments"
					case "authentication-certificate/certificate-id":
						name = "certificates"
					case "authentication-certificate/thumbprint":
						if err := thumbprint(attr.Value); err != nil {
							return nil, err
						}
					case "log-to-eventhub/logger-id":
						name = "loggers"
					case "content/schema-id":
						name = "schemas"
					case "get-authorization-context/provider-id":
						name = "authorizationProviders"
					case "authentication-managed-identity/client-id":
						ref, err := c.apimExternalReference(ctx, apimIdentityType, attr.Value, indexes)
						if err != nil {
							return nil, err
						}
						addReference(external, apimIdentityType, ref)
					}
					if name != "" {
						if err := add(name, attr.Value); err != nil {
							return nil, err
						}
					}
				}
				if token.Name.Local == "get-authorization-context" {
					values, err := c.apimAuthorizationReferences(ctx, apimRootID(id), attributes, indexes)
					if err != nil {
						return nil, err
					}
					refs = append(refs, values...)
				}
			case xml.EndElement:
				depth--
			case xml.CharData:
				if depth == 0 && strings.TrimSpace(string(token)) != "" {
					return nil, serviceDenied("invalid_apim_policy_xml")
				}
				if err := named(string(token)); err != nil {
					return nil, err
				}
			case xml.Directive:
				return nil, serviceDenied("invalid_apim_policy_xml")
			}
		}
		if roots != 1 || depth != 0 {
			return nil, serviceDenied("invalid_apim_policy_xml")
		}
	}
	for _, values := range external {
		for _, value := range values {
			if id, _, err := parseID(value); err == nil {
				refs = append(refs, id)
			}
		}
	}
	refs = slices.DeleteFunc(refs, func(ref string) bool { return ref == id })
	slices.Sort(refs)
	return slices.Compact(refs), nil
}
