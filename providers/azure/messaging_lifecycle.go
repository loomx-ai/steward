package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// Autoforwarding points to a queue or topic in the same namespace. The native
// property contains an entity path, so use both native GETs to resolve its kind.
// https://learn.microsoft.com/azure/service-bus-messaging/service-bus-auto-forwarding
func (c *client) serviceBusForwardReferences(ctx context.Context, kind, id string, raw map[string]any, refs map[string][]string) error {
	if kind != serviceBusQueueType && kind != serviceBusSubscriptionType {
		return nil
	}
	parts := strings.Split(id, "/")
	namespace := strings.Join(parts[:9], "/")
	for _, field := range []string{"forwardTo", "forwardDeadLetteredMessagesTo"} {
		value := object(raw["properties"])[field]
		if value == nil || value == "" {
			continue
		}
		name, ok := value.(string)
		if !ok {
			return fmt.Errorf("Azure Service Bus forwarding target is malformed")
		}
		if strings.Contains(name, "://") {
			u, err := url.Parse(name)
			if err != nil || (u.Scheme != "sb" && u.Scheme != "https") || !strings.EqualFold(u.Host, parts[8]+".servicebus.windows.net") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
				return fmt.Errorf("Azure Service Bus forwarding target belongs to another namespace")
			}
			name = strings.TrimPrefix(u.Path, "/")
		}
		if name == "" || strings.ContainsAny(name, "%?#\\:\x00\r\n\t ") {
			return fmt.Errorf("Azure Service Bus forwarding target is malformed")
		}
		for _, segment := range strings.Split(name, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return fmt.Errorf("Azure Service Bus forwarding entity path is malformed")
			}
		}
		// ARM represents hierarchical entity paths using a tilde.
		name = strings.ReplaceAll(name, "/", "~")
		foundID, foundType := "", ""
		for _, target := range []string{serviceBusQueueType, serviceBusTopicType} {
			targetID := strings.ToLower(namespace + "/" + last(target) + "/" + name)
			definition, _ := findType(target)
			endpoint, err := c.resourceURL(definition, targetID)
			if err != nil {
				return err
			}
			live, err := c.request(ctx, "GET", endpoint)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if !validResourceResponse(live, targetID, target) || foundID != "" || targetID == id {
				return fmt.Errorf("Azure Service Bus forwarding target is ambiguous or invalid")
			}
			foundID, foundType = targetID, target
		}
		if foundID != "" {
			addReference(refs, foundType, foundID)
		}
	}
	return nil
}

const (
	serviceBusNamespaceType    = "Microsoft.ServiceBus/namespaces"
	serviceBusQueueType        = serviceBusNamespaceType + "/queues"
	serviceBusTopicType        = serviceBusNamespaceType + "/topics"
	serviceBusSubscriptionType = serviceBusTopicType + "/subscriptions"
	serviceBusRuleType         = serviceBusSubscriptionType + "/rules"
	serviceBusRecoveryType     = serviceBusNamespaceType + "/disasterRecoveryConfigs"
	serviceBusMigrationType    = serviceBusNamespaceType + "/migrationConfigurations"
	eventHubNamespaceType      = "Microsoft.EventHub/namespaces"
	eventHubType               = eventHubNamespaceType + "/eventhubs"
	eventHubConsumerGroupType  = eventHubType + "/consumergroups"
	eventHubRecoveryType       = eventHubNamespaceType + "/disasterRecoveryConfigs"
)

func messagingManagedConfiguration(kind string) bool {
	return kind == serviceBusNamespaceType+"/networkRuleSets" || kind == eventHubNamespaceType+"/networkRuleSets" ||
		kind == eventHubNamespaceType+"/networkSecurityPerimeterConfigurations" ||
		kind == serviceBusRecoveryType+"/authorizationRules" || kind == eventHubRecoveryType+"/authorizationRules"
}

func messagingDeletionReason(kind string, raw map[string]any) string {
	properties := object(raw["properties"])
	if (kind == serviceBusNamespaceType+"/authorizationRules" || kind == eventHubNamespaceType+"/authorizationRules") && strings.EqualFold(last(strings.TrimRight(text(raw["id"]), "/")), "RootManageSharedAccessKey") {
		// The provider's permission reference explicitly excludes the default
		// namespace rule from independent DELETE.
		// https://learn.microsoft.com/azure/role-based-access-control/permissions/integration
		return "azure_messaging_default_authorization_rule"
	}
	if kind == serviceBusRecoveryType || kind == eventHubRecoveryType {
		// Azure requires failover or breaking the pairing before deleting an
		// active alias. Failing over is not part of an ordinary cleanup action.
		// https://learn.microsoft.com/azure/event-hubs/resource-manager-exceptions
		if strings.EqualFold(text(properties["role"]), "Secondary") && messagingNamespaceValueValid(properties["partnerNamespace"]) && text(properties["partnerNamespace"]) != "" {
			return "azure_messaging_recovery_secondary"
		}
		if !recoveryUnpaired(properties) && !recoveryPrimary(properties) {
			return "azure_messaging_recovery_requires_unpairing"
		}
	}
	if kind == serviceBusMigrationType {
		if !replicationCountValid(properties) || !messagingNamespaceValueValid(properties["targetNamespace"]) {
			return "azure_messaging_migration_in_progress"
		}
		switch strings.ToLower(text(properties["migrationState"])) {
		case "active", "initiating", "syncing", "reverting":
		default:
			return "azure_messaging_migration_in_progress"
		}
		if text(properties["targetNamespace"]) == "" && !migrationReady(properties) {
			return "azure_messaging_migration_in_progress"
		}
	}
	return ""
}

func replicationCountValid(properties map[string]any) bool {
	value := properties["pendingReplicationOperationsCount"]
	switch value := value.(type) {
	case nil:
		return true
	case json.Number:
		number, err := value.Int64()
		return err == nil && number >= 0
	case float64:
		return value >= 0 && !math.IsInf(value, 0) && math.Trunc(value) == value
	case int:
		return value >= 0
	case int64:
		return value >= 0
	default:
		return false
	}
}

// Entity DELETE can be replicated into the other namespace. Inspect the native
// namespace configuration even when deleting only a queue or consumer group.
// https://learn.microsoft.com/azure/service-bus-messaging/service-bus-geo-dr
// https://learn.microsoft.com/azure/event-hubs/event-hubs-geo-dr
// Schema registry metadata also replicates, though registered schemas do not:
// https://learn.microsoft.com/azure/reliability/reliability-event-hubs
func messagingReplicatedEntity(kind string) bool {
	for _, namespace := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		if !strings.HasPrefix(kind, namespace+"/") {
			continue
		}
		suffix := strings.TrimPrefix(kind, namespace+"/")
		return !strings.HasPrefix(suffix, "disasterRecoveryConfigs") && suffix != "migrationConfigurations" &&
			suffix != "privateEndpointConnections" && suffix != "networkRuleSets" && suffix != "networkSecurityPerimeterConfigurations"
	}
	return false
}

func (c *client) messagingReplicationContext(ctx context.Context, kind, id string) (reason, creation string, err error) {
	if !messagingReplicatedEntity(kind) {
		return "", "", nil
	}
	parts := strings.Split(id, "/")
	if len(parts) < 11 {
		return "", "", fmt.Errorf("invalid Azure messaging entity namespace")
	}
	namespaceID := strings.Join(parts[:9], "/")
	_, namespaceType, err := parseID(namespaceID)
	if err != nil {
		return "", "", err
	}
	definition, _ := findType(namespaceType)
	endpoint, err := c.resourceURL(definition, namespaceID)
	if err != nil {
		return "", "", err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return "", "", err
	}
	if !validResourceResponse(live, namespaceID, definition.NativeType) {
		return "", "", fmt.Errorf("Azure messaging namespace identity mismatch")
	}
	types := []string{definition.NativeType + "/disasterRecoveryConfigs"}
	if definition.NativeType == serviceBusNamespaceType {
		types = append(types, serviceBusMigrationType)
	}
	children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: namespaceID, NativeType: definition.NativeType}, live.data, types)
	if err != nil {
		return "", "", err
	}
	for _, child := range children {
		properties := object(child.data["properties"])
		if child.kind == serviceBusMigrationType {
			if text(properties["targetNamespace"]) != "" || !migrationReady(properties) {
				return "azure_messaging_replication_requires_unpairing", creationGeneration(live.data), nil
			}
		} else if !recoveryUnpaired(properties) {
			return "azure_messaging_replication_requires_unpairing", creationGeneration(live.data), nil
		}
	}
	if namespaceType == strings.ToLower(serviceBusNamespaceType) {
		incoming, err := c.incomingMigrations(ctx)
		if err != nil {
			return "", "", err
		}
		if len(incoming[namespaceID]) > 0 {
			return "azure_messaging_replication_requires_unpairing", creationGeneration(live.data), nil
		}
	}
	return "", creationGeneration(live.data), nil
}

func recoveryUnpaired(properties map[string]any) bool {
	return messagingNamespaceValueValid(properties["partnerNamespace"]) && text(properties["partnerNamespace"]) == "" && strings.EqualFold(text(properties["role"]), "PrimaryNotReplicating") && strings.EqualFold(text(properties["provisioningState"]), "Succeeded") && replicationIdle(properties)
}

func messagingNamespaceValueValid(value any) bool {
	_, stringValue := value.(string)
	return value == nil || stringValue
}

func replicationIdle(properties map[string]any) bool {
	value, present := properties["pendingReplicationOperationsCount"]
	if !present || value == nil {
		return true
	}
	switch value := value.(type) {
	case json.Number:
		number, err := value.Int64()
		return err == nil && number == 0
	case float64:
		return value == 0
	case int:
		return value == 0
	case int64:
		return value == 0
	default:
		return false
	}
}

func recoveryPrimary(properties map[string]any) bool {
	state := text(properties["provisioningState"])
	return messagingNamespaceValueValid(properties["partnerNamespace"]) && text(properties["partnerNamespace"]) != "" && strings.EqualFold(text(properties["role"]), "Primary") && replicationCountValid(properties) && (strings.EqualFold(state, "Accepted") || strings.EqualFold(state, "Succeeded"))
}
