package alicloud

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	ramGroupNativeType  = "ACS::RAM::Group"
	ramPolicyNativeType = "ACS::RAM::Policy"

	// NormalizedRAMGroupUsersField lists the user names in a RAM group.
	NormalizedRAMGroupUsersField = "Users"
	// The principals a custom policy is attached to, by name.
	NormalizedRAMPolicyUsersField  = "AttachedUsers"
	NormalizedRAMPolicyGroupsField = "AttachedGroups"
	NormalizedRAMPolicyRolesField  = "AttachedRoles"

	ramMembershipPageSize = 1000
)

// enrichRAMTopology records which users belong to each RAM group and which
// principals each custom policy is attached to. Reading the membership from
// the group and policy side costs one call per group and per attached policy
// instead of several per user. A failed read fails the batch: an unread
// membership must never look like an unused group or policy.
func (r *Runtime) enrichRAMTopology(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	var region string
	for index := range items {
		item := &items[index]
		name := strings.TrimSpace(item.NativeID)
		if name == "" || (item.NativeType != ramGroupNativeType && item.NativeType != ramPolicyNativeType) {
			continue
		}
		if region == "" {
			resolved, err := productAPIRegion(request)
			if err != nil {
				return nil, err
			}
			region = resolved
		}
		if item.Normalized == nil {
			item.Normalized = make(map[string]any)
		}
		switch item.NativeType {
		case ramGroupNativeType:
			users, err := r.ramGroupUsers(ctx, request, region, name)
			if err != nil {
				return nil, err
			}
			item.Normalized[NormalizedRAMGroupUsersField] = users
		case ramPolicyNativeType:
			if err := r.enrichRAMPolicyAttachments(ctx, request, region, name, item.Normalized); err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}

func (r *Runtime) ramGroupUsers(ctx context.Context, request contracts.InventoryRequest, region, group string) ([]any, error) {
	users := make([]any, 0)
	marker := ""
	for page := 0; page < 1000; page++ {
		parameters := map[string]any{"GroupName": group, "MaxItems": ramMembershipPageSize}
		if marker != "" {
			parameters["Marker"] = marker
		}
		result, err := r.Invoke(ctx, contracts.Invocation{
			ConnectionID: request.ConnectionID,
			Operation:    "AlibabaCloud.RAM.ListUsersForGroup",
			Scope:        map[string]string{"region": region},
			Parameters:   parameters,
		})
		if err != nil {
			if ramEntityVanished(err) {
				return users, nil
			}
			return nil, fmt.Errorf("list the users of RAM group %s: %w", group, err)
		}
		users = append(users, ramNames(result.Data, "Users.User", "UserName")...)
		truncated, _ := result.Data["IsTruncated"].(bool)
		marker = strings.TrimSpace(stringValue(result.Data["Marker"]))
		if !truncated {
			return users, nil
		}
		if marker == "" {
			return nil, fmt.Errorf("RAM ListUsersForGroup for %s is truncated without a marker", group)
		}
	}
	return nil, fmt.Errorf("RAM ListUsersForGroup for %s did not finish paging", group)
}

func (r *Runtime) enrichRAMPolicyAttachments(
	ctx context.Context,
	request contracts.InventoryRequest,
	region, policy string,
	normalized map[string]any,
) error {
	empty := func() {
		normalized[NormalizedRAMPolicyUsersField] = []any{}
		normalized[NormalizedRAMPolicyGroupsField] = []any{}
		normalized[NormalizedRAMPolicyRolesField] = []any{}
	}
	// The listing reports how many principals a policy is attached to; an
	// unattached policy needs no further call.
	if count, ok := integerValue(normalized["attachmentCount"]); ok && count == 0 {
		empty()
		return nil
	}
	result, err := r.Invoke(ctx, contracts.Invocation{
		ConnectionID: request.ConnectionID,
		Operation:    "AlibabaCloud.RAM.ListEntitiesForPolicy",
		Scope:        map[string]string{"region": region},
		Parameters:   map[string]any{"PolicyType": "Custom", "PolicyName": policy},
	})
	if err != nil {
		if ramEntityVanished(err) {
			empty()
			return nil
		}
		return fmt.Errorf("list the principals attached to RAM policy %s: %w", policy, err)
	}
	normalized[NormalizedRAMPolicyUsersField] = ramNames(result.Data, "Users.User", "UserName")
	normalized[NormalizedRAMPolicyGroupsField] = ramNames(result.Data, "Groups.Group", "GroupName")
	normalized[NormalizedRAMPolicyRolesField] = ramNames(result.Data, "Roles.Role", "RoleName")
	return nil
}

func ramNames(data map[string]any, path, field string) []any {
	names := make([]any, 0)
	for _, value := range anySlice(valueAtPath(data, path)) {
		if record, ok := value.(map[string]any); ok {
			if name, ok := nonEmptyString(record[field]); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// ramEntityVanished reports RAM's EntityNotExist.* codes: the group or policy
// was deleted after it was listed and no longer grants anything.
func ramEntityVanished(err error) bool {
	var providerError *contracts.ProviderCallError
	return errors.As(err, &providerError) &&
		strings.HasPrefix(strings.ToLower(providerError.Provider.Code), "entitynotexist.")
}
