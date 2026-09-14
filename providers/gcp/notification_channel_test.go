package gcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const notificationChannelName = "projects/sample-project/notificationChannels/9876"
const notificationChannelID = "//monitoring.googleapis.com/" + notificationChannelName

func notificationChannelFixture() map[string]any {
	return map[string]any{"name": notificationChannelName, "type": "email", "displayName": "Operations", "description": "PRIVATE_CHANNEL_DESCRIPTION", "enabled": false, "verificationStatus": "UNVERIFIED", "labels": map[string]any{"email_address": "PRIVATE_CHANNEL_CONTACT", "ordinary": "PRIVATE_CHANNEL_LABEL"}, "userLabels": map[string]any{"environment": "test"}, "creationRecord": map[string]any{"mutateTime": "2026-09-01T00:00:00Z", "mutatedBy": "PRIVATE_CHANNEL_CREATOR"}, "mutationRecords": []any{map[string]any{"mutateTime": "2026-09-02T00:00:00Z", "mutatedBy": "PRIVATE_CHANNEL_MUTATOR"}}, "futureNativeField": map[string]any{"value": "bound"}}
}

func TestNotificationChannelNativeInventory(t *testing.T) {
	for _, mode := range []string{"normal", "list-empty", "list-paged", "list-denied", "list-null", "list-token", "list-partial", "list-duplicate", "get-denied", "get-partial", "gone", "detail-drift"} {
		t.Run(mode, func(t *testing.T) {
			r, request, _, state, deletes := monitoringScenario(t, notificationChannelType)
			*state = mode
			query := productRequest(r, notificationChannelType, "global")
			batch, err := r.List(t.Context(), query)
			if mode == "list-paged" {
				if err != nil || batch.Complete || batch.NextCursor == "" || len(batch.Items) != 0 {
					t.Fatal(batch, err)
				}
				query.Cursor = batch.NextCursor
				batch, err = r.List(t.Context(), query)
			}
			success := mode == "normal" || mode == "list-paged" || mode == "list-empty"
			if !success {
				if err == nil || len(batch.Items) != 0 {
					t.Fatal(batch, err)
				}
				return
			}
			want := 1
			if mode == "list-empty" {
				want = 0
			}
			if err != nil || !batch.Complete || len(batch.Items) != want {
				t.Fatal(batch, err)
			}
			if want == 0 {
				return
			}
			item := batch.Items[0]
			raw, _ := json.Marshal(item)
			if strings.Contains(string(raw), "PRIVATE_CHANNEL") || item.Normalized[notificationChannelReview] == "" || item.Tags["environment"] != "test" || len(item.Tags) != 1 || item.Actionable == nil || *item.Actionable || len(item.NetworkReferences) != 0 {
				t.Fatal(string(raw))
			}
			request.Asset.Normalized = item.Normalized
			assertGCPPropertyQuery(t, r, []asset.Asset{request.Asset}, notificationChannelType, "9876", `properties.enabled = false AND properties.verificationStatus = "UNVERIFIED"`)
			if _, err := r.ResolveAction(t.Context(), "connection", request.Asset); err == nil || *deletes != 0 {
				t.Fatal("read-only channel exposed delete", err, *deletes)
			}
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.notificationChannels.get", Parameters: map[string]any{"name": notificationChannelName}})
			raw, _ = json.Marshal(result)
			if err != nil || strings.Contains(string(raw), "PRIVATE_CHANNEL") {
				t.Fatal(string(raw), err)
			}
		})
	}
}

func TestNotificationChannelVisibleConfigurationAndShape(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	base := notificationChannelConfiguration(notificationChannelID, notificationChannelFixture())
	for _, mode := range []string{"normal", "alias", "enabled-omitted", "status-omitted", "verified", "unknown-type", "changed-label", "changed-description", "changed-history", "changed-unknown", "foreign", "missing-type", "name-null", "labels-null", "labels-invalid", "user-labels-invalid", "enabled-null", "status-invalid", "record-null", "records-null", "records-invalid", "mutator-invalid", "partial"} {
		t.Run(mode, func(t *testing.T) {
			data := notificationChannelFixture()
			valid := true
			changed := true
			switch mode {
			case "normal":
				changed = false
			case "alias":
				data["name"] = strings.Replace(notificationChannelName, "sample-project", "123456", 1)
				changed = false
			case "enabled-omitted":
				delete(data, "enabled")
			case "status-omitted":
				delete(data, "verificationStatus")
			case "verified":
				data["verificationStatus"] = "VERIFIED"
			case "unknown-type":
				data["type"] = "future_descriptor_type"
			case "changed-label":
				object(data["labels"])["ordinary"] = "different"
			case "changed-description":
				data["description"] = "different"
			case "changed-history":
				object(array(data["mutationRecords"])[0])["mutateTime"] = "2026-09-03T00:00:00Z"
			case "changed-unknown":
				data["futureNativeField"] = false
			default:
				valid = false
				switch mode {
				case "foreign":
					data["name"] = "projects/foreign-project/notificationChannels/9876"
				case "missing-type":
					delete(data, "type")
				case "name-null":
					data["name"] = nil
				case "labels-null":
					data["labels"] = nil
				case "labels-invalid":
					data["labels"] = map[string]any{"address": true}
				case "user-labels-invalid":
					data["userLabels"] = []any{"label"}
				case "enabled-null":
					data["enabled"] = nil
				case "status-invalid":
					data["verificationStatus"] = "FUTURE_STATUS"
				case "record-null":
					data["creationRecord"] = nil
				case "records-null":
					data["mutationRecords"] = nil
				case "records-invalid":
					data["mutationRecords"] = []any{"record"}
				case "mutator-invalid":
					object(data["creationRecord"])["mutatedBy"] = false
				case "partial":
					data["unreachable"] = []any{"project"}
				}
			}
			if err := c.notificationChannelData(notificationChannelID, data); (err == nil) != valid {
				t.Fatal(mode, err)
			}
			if valid && (notificationChannelConfiguration(notificationChannelID, data) != base) != changed {
				t.Fatal("visible configuration proof lost change", mode)
			}
		})
	}
	compiler := infraFixtureSchemas(t, "fixtures/notification-channel/native-schemas.json", "20260903", "9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019")
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/NotificationChannel")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(notificationChannelFixture()); err != nil {
		t.Fatal(err)
	}
}
