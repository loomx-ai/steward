package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func channelDependencyFixture(t *testing.T, delivery ...string) (*monitoringDependencyScenario, *map[string]any, *string, *int) {
	t.Helper()
	channelRuntime, request, data, mode, deletes := monitoringScenario(t, notificationChannelType, delivery...)
	s := monitoringDependencyFixture(t)
	policyTransport := s.r.transport
	s.request = *request
	s.request.Asset.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed}
	if len(delivery) > 0 && delivery[0] != "email" {
		s.request.Asset.Capabilities = append(s.request.Asset.Capabilities, asset.CapabilityActionable)
	}
	s.uptimeData, s.uptimeMode, s.uptimeDeletes = data, mode, deletes
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cloudbilling.googleapis.com" {
			return emptyBillingAccountsFixture(t, req), nil
		}
		if req.Method != "GET" && req.Method != "DELETE" || req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal("unexpected channel dependency call", req.Method, req.URL)
		}
		if strings.Contains(req.URL.Path, "/notificationChannels/") {
			return channelRuntime.transport.RoundTrip(req)
		}
		if strings.Contains(req.URL.Path, "/alertPolicies") || req.URL.Path == "/v1/projects/sample-project/dashboards" {
			if s.mode == "channel-changed" {
				(*data)["displayName"] = "changed-during-policy-read"
			}
			return policyTransport.RoundTrip(req)
		}
		t.Fatal("unexpected project expansion for channel consumers", req.URL)
		return nil, nil
	})
	return s, data, mode, deletes
}

func TestNotificationChannelDependencySnapshotFailures(t *testing.T) {
	for _, mode := range []string{"present", "paged", "empty", "denied", "null", "token-null", "partial", "element", "duplicate", "token-loop", "changed", "get-denied", "get-missing", "get-changed", "channel-denied", "channel-missing", "channel-changed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, channelMode, deletes := channelDependencyFixture(t)
			s.mode = mode
			if mode == "empty" {
				s.data = nil
			}
			if mode == "channel-denied" {
				*channelMode = "get-denied"
			}
			if mode == "channel-missing" {
				*channelMode = "gone"
			}
			contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", []asset.Asset{s.request.Asset, s.policy})
			good := mode == "present" || mode == "paged"
			if (err == nil) != good {
				t.Fatal(mode, result, err)
			}
			if good && (len(result.Relationships) == 0) != (mode == "empty") {
				t.Fatal(result)
			}
			if *deletes != 0 || s.policyDeletes != 0 || s.reverses != 0 {
				t.Fatal("dependency discovery wrote or expanded metric scopes")
			}
		})
	}
}

func TestNotificationChannelDependencyGraph(t *testing.T) {
	for _, mode := range []string{"present", "disabled", "alias", "strategy-only", "both", "unrelated", "foreign-channel", "missing", "stale", "closed", "foreign-connection", "duplicate", "closed-channel", "foreign-partition"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, _ := channelDependencyFixture(t)
			switch mode {
			case "disabled":
				s.data["enabled"] = false
			case "alias":
				s.data["notificationChannels"] = []any{"projects/123456/notificationChannels/9876"}
			case "strategy-only", "both":
				s.data["alertStrategy"] = map[string]any{"notificationChannelStrategy": []any{map[string]any{"notificationChannelNames": []any{notificationChannelName}, "renotifyInterval": "1800s"}}}
				if mode == "strategy-only" {
					delete(s.data, "notificationChannels")
				}
			case "unrelated":
				s.data["notificationChannels"] = []any{"projects/sample-project/notificationChannels/other"}
			case "foreign-channel":
				s.data["notificationChannels"] = []any{"projects/foreign-project/notificationChannels/9876"}
			}
			s.policy.Normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, alertPolicyID, s.data)
			values := []asset.Asset{s.request.Asset, s.policy}
			switch mode {
			case "missing":
				values = values[:1]
			case "stale":
				values[1].Normalized[alertPolicyReview] = "stale"
			case "closed":
				now := time.Now()
				values[1].ClosedAt = &now
			case "foreign-connection":
				values[1].Identity.ConnectionID = "foreign"
			case "duplicate":
				values = append(values, s.policy)
			case "closed-channel":
				now := time.Now()
				values[0].ClosedAt = &now
			case "foreign-partition":
				values[0].Identity.Partition = "foreign"
			}
			contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", values)
			if mode == "duplicate" || mode == "foreign-partition" || mode == "stale" {
				if err == nil {
					t.Fatal("invalid identity accepted")
				}
				return
			}
			if err != nil || len(result.Bindings) != 0 {
				t.Fatal(result, err)
			}
			if mode != "closed-channel" {
				result.Unresolved = assertBudgetCoverage(t, result.Unresolved, s.request.Asset.ID)
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "PRIVATE_") {
				t.Fatal("private configuration persisted")
			}
			if mode == "unrelated" || mode == "foreign-channel" || mode == "closed-channel" {
				if len(result.Relationships)+len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
				return
			}
			if mode == "missing" || mode == "stale" || mode == "closed" || mode == "foreign-connection" {
				if len(result.Unresolved) != 1 || !result.Unresolved[0].BlocksCleanup || len(result.Relationships) != 0 {
					t.Fatal(result)
				}
				return
			}
			if len(result.Relationships) != 1 || len(result.Unresolved) != 0 {
				t.Fatal(result)
			}
			edge := result.Relationships[0]
			if edge.SourceAssetID != s.request.Asset.ID || edge.TargetAssetID != s.policy.ID || edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || edge.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource {
				t.Fatal(edge)
			}
		})
	}
}

func TestNotificationChannelStrategyShape(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, mode := range []string{"null", "scalar", "entry-null", "names-null", "name-invalid", "interval-invalid"} {
		t.Run(mode, func(t *testing.T) {
			data := alertPolicyFixture()
			strategy := map[string]any{}
			data["alertStrategy"] = strategy
			entry := map[string]any{"notificationChannelNames": []any{notificationChannelName}}
			strategy["notificationChannelStrategy"] = []any{entry}
			switch mode {
			case "null":
				strategy["notificationChannelStrategy"] = nil
			case "scalar":
				strategy["notificationChannelStrategy"] = "hidden"
			case "entry-null":
				strategy["notificationChannelStrategy"] = []any{nil}
			case "names-null":
				entry["notificationChannelNames"] = nil
			case "name-invalid":
				entry["notificationChannelNames"] = []any{"projects/sample-project/notificationChannels/a/extra"}
			case "interval-invalid":
				entry["renotifyInterval"] = true
			}
			if err := c.alertPolicyData(alertPolicyID, data); err == nil {
				t.Fatal("malformed notification strategy accepted")
			}
		})
	}
}
