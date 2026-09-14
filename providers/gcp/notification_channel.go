package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const notificationChannelType = "monitoring.googleapis.com/NotificationChannel"
const notificationChannelReview = "_notification_channel_configuration"

// This binds the configuration visible to the API, including mutation history.
// Google only partially returns sensitive labels; this cannot prove equality of
// hidden values and is not authority to delete the channel.
func notificationChannelConfiguration(id string, data map[string]any) string {
	value := cloneParameters(data)
	value["name"] = id
	return firewallDigest(value)
}

func (c *client) notificationChannelData(id string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	name, ok := data["name"].(string)
	if !ok || !strings.HasPrefix(name, "projects/") || c.canonicalName("//monitoring.googleapis.com/"+name) != id {
		return groupDenied("notification_channel_identity_changed")
	}
	kind, _ := findType(notificationChannelType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "type", "displayName", "description", "verificationStatus"}, []string{"enabled"}, nil, nil); err != nil {
		return err
	}
	if text(data["type"]) == "" {
		return groupDenied("notification_channel_type_missing")
	}
	switch text(data["verificationStatus"]) {
	case "", "VERIFICATION_STATUS_UNSPECIFIED", "UNVERIFIED", "VERIFIED":
	default:
		return groupDenied("notification_channel_verification_invalid")
	}
	for _, field := range []string{"labels", "userLabels"} {
		if err := uptimeStringMap(data, field); err != nil {
			return err
		}
	}
	records, err := cloudNatObjects(data, "mutationRecords")
	if err != nil {
		return err
	}
	if raw, present := data["creationRecord"]; present {
		record := object(raw)
		if record == nil {
			return groupDenied("notification_channel_creation_invalid")
		}
		records = append(records, record)
	}
	for _, record := range records {
		if err := cloudNatScalars(record, []string{"mutateTime", "mutatedBy"}, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *client) notificationChannelRead(ctx context.Context, id string) (map[string]any, error) {
	kind, _ := findType(notificationChannelType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := c.notificationChannelData(id, live); err != nil {
		return nil, err
	}
	return live, nil
}

func (c *client) notificationChannelInventory(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	if err := c.notificationChannelData(id, listed); err != nil {
		return nil, err
	}
	live, err := c.notificationChannelRead(ctx, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if notificationChannelConfiguration(id, listed) != notificationChannelConfiguration(id, live) {
		return nil, groupDenied("notification_channel_configuration_changed")
	}
	return live, nil
}

func redactNotificationChannelPayload(data map[string]any) {
	if !strings.Contains(text(data["name"]), "/notificationChannels/") {
		return
	}
	// Channel labels include contact addresses, URLs and tokens with arbitrary
	// descriptor-defined keys. Even partially masked native values stay private.
	for _, field := range []string{"labels", "description"} {
		if _, present := data[field]; present {
			data[field] = "[REDACTED]"
		}
	}
	records := append([]any{data["creationRecord"]}, array(data["mutationRecords"])...)
	for _, raw := range records {
		record := object(raw)
		if _, present := record["mutatedBy"]; present {
			record["mutatedBy"] = "[REDACTED]"
		}
	}
}
