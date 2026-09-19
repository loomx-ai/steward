package cleanup

import (
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

const connectionIdentitySource = "connection_identity"

// connectionIdentityProtections protects the principal a connection uses and
// the resources its access directly depends on. Deleting them in the middle of
// a cleanup would revoke Steward's own access and strand the remaining steps.
// Only identities recorded by the connection's own identity validation count.
func connectionIdentityProtections(connections []asset.CloudConnection, assets []asset.Asset) map[asset.AssetID]plan.ProtectionPolicy {
	result := map[asset.AssetID]plan.ProtectionPolicy{}
	protect := func(value asset.Asset, connection asset.CloudConnection, relation string) {
		if _, exists := result[value.ID]; exists {
			return
		}
		result[value.ID] = plan.ProtectionPolicy{
			AssetID: value.ID, Protected: true, Source: connectionIdentitySource,
			Reason:   fmt.Sprintf("the connection %q uses this %s", connection.Name, relation),
			Evidence: map[string]any{"connection_id": connection.ID, "principal": connection.Principal, "relation": relation},
		}
	}
	for _, connection := range connections {
		principal := strings.TrimSpace(connection.Principal)
		if principal == "" {
			continue
		}
		var members []asset.Asset
		for _, value := range assets {
			if value.ClosedAt == nil && value.Identity.ConnectionID == connection.ID && value.Identity.Provider == connection.Provider {
				members = append(members, value)
			}
		}
		switch connection.Provider {
		case asset.ProviderAWS:
			kind, name := callerPrincipal(principal)
			identityType := map[string]string{"user": "AWS::IAM::User", "role": "AWS::IAM::Role"}[kind]
			if identityType == "" {
				continue
			}
			for _, identity := range members {
				if identity.Identity.NativeType != identityType || !strings.EqualFold(identity.Identity.NativeID, name) {
					continue
				}
				protect(identity, connection, kind)
				groups := normalizedStrings(identity.Normalized["Groups"])
				policies := normalizedStrings(identity.Normalized["ManagedPolicyArns"])
				for _, value := range members {
					switch value.Identity.NativeType {
					case "AWS::IAM::Group":
						if slices.ContainsFunc(groups, func(group string) bool { return strings.EqualFold(group, value.Identity.NativeID) }) {
							protect(value, connection, "user group")
						}
					case "AWS::IAM::ManagedPolicy":
						if slices.Contains(policies, value.Identity.NativeID) {
							protect(value, connection, "attached policy")
						}
					case "AWS::IAM::InstanceProfile":
						if kind == "role" && slices.ContainsFunc(normalizedStrings(value.Normalized["Roles"]), func(role string) bool { return strings.EqualFold(role, name) }) {
							protect(value, connection, "instance profile")
						}
					}
				}
			}
		case asset.ProviderAliCloud:
			protectRAMPrincipal(members, connection, principal, protect)
		case asset.ProviderGCP:
			account := "/serviceaccounts/" + strings.ToLower(principal)
			for _, value := range members {
				id := strings.ToLower(value.Identity.NativeID)
				switch value.Identity.NativeType {
				case "iam.googleapis.com/ServiceAccount":
					if strings.HasSuffix(id, account) {
						protect(value, connection, "service account")
					}
				case "iam.googleapis.com/ServiceAccountKey":
					if strings.Contains(id, account+"/keys/") {
						protect(value, connection, "service account key")
					}
				}
			}
		}
	}
	return result
}

// protectRAMPrincipal protects a RAM user or role, the groups the user
// belongs to and the custom policies attached to the principal directly or
// through those groups. Alibaba Cloud records membership on the group
// (Users) and attachments on the policy (AttachedUsers, AttachedGroups,
// AttachedRoles), so the lookup runs from those resources and does not need
// the principal itself to be in the inventory.
func protectRAMPrincipal(members []asset.Asset, connection asset.CloudConnection, principal string, protect func(asset.Asset, asset.CloudConnection, string)) {
	kind, name := callerPrincipal(principal)
	identityType := map[string]string{"user": "ACS::RAM::User", "role": "ACS::RAM::Role"}[kind]
	if identityType == "" {
		return
	}
	for _, identity := range members {
		if identity.Identity.NativeType == identityType && strings.EqualFold(identity.Identity.NativeID, name) {
			protect(identity, connection, kind)
		}
	}
	named := func(values any, want string) bool {
		return slices.ContainsFunc(normalizedStrings(values), func(value string) bool { return strings.EqualFold(value, want) })
	}
	var groups []string
	if kind == "user" {
		for _, value := range members {
			if value.Identity.NativeType == "ACS::RAM::Group" && named(value.Normalized["Users"], name) {
				protect(value, connection, "user group")
				groups = append(groups, value.Identity.NativeID)
			}
		}
	}
	principalField := map[string]string{"user": "AttachedUsers", "role": "AttachedRoles"}[kind]
	for _, value := range members {
		if value.Identity.NativeType != "ACS::RAM::Policy" {
			continue
		}
		if named(value.Normalized[principalField], name) {
			protect(value, connection, "attached policy")
			continue
		}
		for _, group := range groups {
			if named(value.Normalized["AttachedGroups"], group) {
				protect(value, connection, "group policy")
				break
			}
		}
	}
}

// callerPrincipal reads an STS or RAM caller ARN: a user, or the role behind
// an assumed-role session. Account roots and federated users are not assets.
func callerPrincipal(arn string) (string, string) {
	// AWS ARNs carry a partition segment; Alibaba Cloud ARNs start at the service.
	parts := strings.SplitN(arn, ":", 6)
	if strings.HasPrefix(arn, "acs:") {
		parts = append([]string{"acs"}, strings.SplitN(arn, ":", 5)...)
	}
	if len(parts) != 6 {
		return "", ""
	}
	resource := strings.Split(parts[5], "/")
	switch {
	case parts[2] == "iam" && resource[0] == "user" && len(resource) >= 2,
		parts[2] == "ram" && resource[0] == "user" && len(resource) >= 2:
		return "user", resource[len(resource)-1]
	case (parts[2] == "sts" || parts[2] == "ram") && resource[0] == "assumed-role" && len(resource) == 3:
		return "role", resource[1]
	case parts[2] == "iam" && resource[0] == "role" && len(resource) >= 2:
		return "role", resource[len(resource)-1]
	}
	return "", ""
}

func normalizedStrings(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []string:
		result = append(result, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	}
	return result
}
