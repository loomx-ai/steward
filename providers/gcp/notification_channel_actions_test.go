package gcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestNotificationChannelDeleteReviewRestartAndSettlement(t *testing.T) {
	for _, mode := range []string{"normal", "gone", "get-denied", "delete-denied", "delete-missing", "delete-error", "delete-operation", "referenced", "race", "changed-label", "changed-history", "changed-unknown", "protected", "email", "missing-review", "wrong-identity", "force"} {
		t.Run(mode, func(t *testing.T) {
			r, request, data, state, deletes := monitoringScenario(t, notificationChannelType, "pubsub")
			*state = mode
			switch mode {
			case "changed-label":
				object((*data)["labels"])["ordinary"] = "changed"
			case "changed-history":
				object(array((*data)["mutationRecords"])[0])["mutateTime"] = "2026-09-03T00:00:00Z"
			case "changed-unknown":
				(*data)["futureNativeField"] = true
			case "protected":
				(*data)["userLabels"] = map[string]any{"steward_protected": "true"}
				request.Asset.Normalized[notificationChannelReview] = notificationChannelConfiguration(notificationChannelID, *data)
			case "email":
				(*data)["type"] = "email"
				request.Asset.Normalized[notificationChannelReview] = notificationChannelConfiguration(notificationChannelID, *data)
			case "missing-review":
				delete(request.Asset.Normalized, notificationChannelReview)
			case "force":
				request.Parameters = map[string]any{"force": true}
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-identity" {
				request.Asset.Identity.ConnectionID = "foreign"
			}
			result, err := driver.Execute(t.Context(), *request)
			if mode != "normal" && mode != "gone" {
				if err == nil {
					t.Fatal("unreviewed deletion accepted", result)
				}
				if !strings.HasPrefix(mode, "delete-") && mode != "referenced" && *deletes != 0 {
					t.Fatal("write before review", *deletes)
				}
				return
			}
			if err != nil || result.ProviderOperationID != "" {
				t.Fatal(result, err)
			}
			if mode == "gone" {
				if *deletes != 0 {
					t.Fatal("deleted absent channel")
				}
				return
			}
			if result.Data["phase"] != "notification_channel_delete" || *deletes != 1 {
				t.Fatal(result, *deletes)
			}
			b, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if err := json.Unmarshal(b, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), *request, restored)
			if err != nil || wait.Done {
				t.Fatal(wait, err)
			}
			*state = "gone"
			wait, err = driver.Wait(t.Context(), *request, restored)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, restored)
			if err != nil || !settled.Settled {
				t.Fatal(settled, err)
			}
			settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, contracts.ActionResult{})
			if err != nil || settled.Settled {
				t.Fatal("lost response released scope", settled, err)
			}
			for _, changed := range []string{"key", "review", "phase", "operation"} {
				q := *request
				copy := restored
				copy.Data = cloneParameters(restored.Data)
				switch changed {
				case "key":
					q.IdempotencyKey = "changed"
				case "review":
					q.Asset.Normalized = cloneParameters(q.Asset.Normalized)
					q.Asset.Normalized[notificationChannelReview] = strings.Repeat("0", 64)
				case "phase":
					copy.Data["phase"] = "uptime_delete"
				case "operation":
					copy.ProviderOperationID = "operations/fake"
				}
				if _, err := driver.Wait(t.Context(), q, copy); err == nil {
					t.Fatal("changed receipt accepted", changed)
				}
			}
		})
	}
}

func TestNotificationChannelDeleteCannotBypassReview(t *testing.T) {
	r, request, _, _, deletes := monitoringScenario(t, notificationChannelType, "pubsub")
	for _, force := range []bool{false, true} {
		if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.notificationChannels.delete", Parameters: map[string]any{"name": notificationChannelName, "force": force}}); err == nil {
			t.Fatal("unreviewed Invoke accepted")
		}
	}
	if *deletes != 0 {
		t.Fatal("unreviewed write")
	}
	r, request, _, _, deletes = monitoringScenario(t, notificationChannelType)
	if _, err := r.ResolveAction(t.Context(), "connection", request.Asset); err == nil || *deletes != 0 {
		t.Fatal("email budget boundary bypassed")
	}
}

func TestNotificationChannelLivePolicyPreventsDelete(t *testing.T) {
	for _, mode := range []string{"present", "disabled", "strategy", "late-policy", "denied", "changed", "get-denied", "get-missing", "get-changed", "stale-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, deletes := channelDependencyFixture(t, "pubsub")
			s.mode = mode
			if mode == "disabled" {
				s.data["enabled"] = false
			}
			if mode == "strategy" {
				delete(s.data, "notificationChannels")
				s.data["alertStrategy"] = map[string]any{"notificationChannelStrategy": []any{map[string]any{"notificationChannelNames": []any{notificationChannelName}}}}
			}
			if mode == "stale-prerequisite" {
				s.request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.policy, ControllerID: s.request.Asset.ID, Delete: true}}
			}
			driver, err := s.r.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(t.Context(), s.request); err == nil || *deletes != 0 || s.policyDeletes != 0 {
				t.Fatal("live or unknown policy allowed channel deletion", err, *deletes)
			}
		})
	}
}
