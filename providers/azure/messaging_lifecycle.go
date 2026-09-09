package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
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
	if kind == serviceBusRecoveryType || kind == eventHubRecoveryType {
		// Azure requires failover or breaking the pairing before deleting an
		// active alias. Failing over is not part of an ordinary cleanup action.
		// https://learn.microsoft.com/azure/event-hubs/resource-manager-exceptions
		if text(properties["partnerNamespace"]) != "" || !strings.EqualFold(text(properties["role"]), "PrimaryNotReplicating") || !replicationIdle(properties) {
			return "azure_messaging_recovery_requires_unpairing"
		}
	}
	if kind == serviceBusMigrationType {
		if !strings.EqualFold(text(properties["migrationState"]), "Active") || !replicationIdle(properties) {
			return "azure_messaging_migration_in_progress"
		}
	}
	return ""
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
