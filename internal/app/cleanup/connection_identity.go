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
		case asset.ProviderAWS, asset.ProviderAliCloud:
			kind, name := callerPrincipal(principal)
			userType, roleType, groupType, policyType := "AWS::IAM::User", "AWS::IAM::Role", "AWS::IAM::Group", "AWS::IAM::ManagedPolicy"
			if connection.Provider == asset.ProviderAliCloud {
				userType, roleType, groupType, policyType = "ACS::RAM::User", "ACS::RAM::Role", "ACS::RAM::Group", "ACS::RAM::Policy"
			}
			identityType := map[string]string{"user": userType, "role": roleType}[kind]
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
					case groupType:
						if slices.ContainsFunc(groups, func(group string) bool { return strings.EqualFold(group, value.Identity.NativeID) }) {
							protect(value, connection, "user group")
						}
					case policyType:
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
