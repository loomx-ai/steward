package azure

import (
	"strings"
	"time"
)

// Native delete can clear the policy link and populate deferred-delete fields.
// Compare the policy separately so clearing it is accepted only for a verified
// retained outcome; do not discard unknown authored properties from this hash.
func recoveryItemConfiguration(raw map[string]any) map[string]any {
	snapshot := hybridComputeChildSnapshot(raw)
	p := object(snapshot["properties"])
	for _, key := range []string{"policyId", "policyName", "protectionState", "protectionStatus", "protectedItemHealthStatus", "lastBackupStatus", "lastBackupTime", "lastBackupErrorDetail", "lastRecoveryPoint", "isScheduledForDeferredDelete", "deferredDeleteTimeInUTC", "deferredDeleteTimeRemaining", "isDeferredDeleteScheduleUpcoming", "softDeleteRetentionPeriod", "softDeleteRetentionPeriodInDays"} {
		delete(p, key)
	}
	return snapshot
}

func (c *client) recoveryItemState(raw map[string]any) map[string]any {
	p := object(raw["properties"])
	return map[string]any{"configuration": c.privateConfiguration(recoveryItemConfiguration(raw)), "policy_id": text(p["policyId"]), "policy_name": text(p["policyName"]), "retained": p["isScheduledForDeferredDelete"] == true, "state": text(p["protectionState"]), "protected_type": text(p["protectedItemType"])}
}

func (c *client) recoveryItemRetainedOutcome(planned, current map[string]any) (bool, error) {
	after := object(current["properties"])
	if after == nil || planned["retained"] != false {
		return false, serviceDenied("invalid_recovery_item_active_baseline")
	}
	if planned["configuration"] != c.privateConfiguration(recoveryItemConfiguration(current)) {
		return false, serviceDenied("recovery_item_configuration_changed")
	}
	if flag, exists := after["isScheduledForDeferredDelete"]; exists {
		if _, ok := flag.(bool); !ok {
			return false, serviceDenied("invalid_recovery_item_retention_flag")
		}
	}
	retained := after["isScheduledForDeferredDelete"] == true
	if retained {
		if after["protectionState"] != "ProtectionStopped" {
			return false, serviceDenied("incomplete_recovery_item_retention_transition")
		}
		// Azure records the deletion time here, not a guaranteed future purge time.
		// Neither an elapsed date nor an elapsed remaining duration proves purge.
		if value, exists := after["deferredDeleteTimeInUTC"]; exists {
			at, err := time.Parse(time.RFC3339Nano, text(value))
			if err != nil || at.IsZero() {
				return false, serviceDenied("invalid_recovery_item_deferred_time")
			}
		}
	}
	for key, reviewKey := range map[string]string{"policyId": "policy_id", "policyName": "policy_name"} {
		if value, exists := after[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return false, serviceDenied("invalid_recovery_item_policy")
			}
		}
		old, now := text(planned[reviewKey]), text(after[key])
		if !strings.EqualFold(old, now) && !(retained && now == "") {
			return false, serviceDenied("recovery_item_policy_changed")
		}
	}
	return retained, nil
}
