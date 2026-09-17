package azure

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func recoveryItemRecordedRetention(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	wire, err := os.ReadFile("fixtures/recoveryservices/vm-item-delete-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Interaction int
		Response    struct{ Body struct{ String string } }
	}
	if err = json.Unmarshal(wire, &rows); err != nil {
		t.Fatal(err)
	}
	var before, after []any
	for _, row := range rows {
		if row.Interaction != 119 && row.Interaction != 127 {
			continue
		}
		var raw map[string]any
		if err = json.Unmarshal([]byte(row.Response.Body.String), &raw); err != nil {
			t.Fatal(err)
		}
		if row.Interaction == 119 {
			before = array(raw["value"])
		} else {
			after = array(raw["value"])
		}
	}
	for _, value := range after {
		current := object(value)
		if object(current["properties"])["isScheduledForDeferredDelete"] != true {
			continue
		}
		for _, prior := range before {
			if object(prior)["id"] == current["id"] {
				return object(prior), current
			}
		}
	}
	t.Fatal("missing recorded same-ID retention transition")
	return nil, nil
}

func TestRecoveryItemRecordedRetentionTransition(t *testing.T) {
	before, after := recoveryItemRecordedRetention(t)
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		t.Fatal("state comparison made a service request")
		return nil, nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	beforeWire, _ := json.Marshal(before)
	afterWire, _ := json.Marshal(after)
	retained, err := c.recoveryItemRetainedOutcome(c.recoveryItemState(before), after)
	if err != nil || !retained {
		t.Fatal("unchanged recorded transition rejected", err)
	}
	unchangedBefore, _ := json.Marshal(before)
	unchangedAfter, _ := json.Marshal(after)
	if string(beforeWire) != string(unchangedBefore) || string(afterWire) != string(unchangedAfter) {
		t.Fatal("normalization mutated evidence")
	}
	for _, mode := range []string{"stopped-with-retain", "cleared-policy-active", "changed-policy", "changed-source", "changed-unknown", "invalid-time", "invalid-flag", "incomplete-state", "already-retained"} {
		t.Run(mode, func(t *testing.T) {
			current := batchClone(after)
			planned := before
			p := object(current["properties"])
			switch mode {
			case "stopped-with-retain":
				current = batchClone(before)
				object(current["properties"])["protectionState"] = "ProtectionStopped"
			case "cleared-policy-active":
				p["isScheduledForDeferredDelete"] = false
			case "changed-policy":
				p["policyId"] = "/subscriptions/other/policies/other"
			case "changed-source":
				p["sourceResourceId"] = "/subscriptions/other/source"
			case "changed-unknown":
				p["unknownAuthoredField"] = "changed"
			case "invalid-time":
				p["deferredDeleteTimeInUTC"] = "not-a-date"
			case "invalid-flag":
				p["isScheduledForDeferredDelete"] = "true"
			case "incomplete-state":
				p["protectionState"] = "Protected"
			case "already-retained":
				planned = after
			}
			retained, err := c.recoveryItemRetainedOutcome(c.recoveryItemState(planned), current)
			if mode == "stopped-with-retain" {
				if err != nil || retained {
					t.Fatal("stop-with-retain treated as deletion", err)
				}
			} else if err == nil || retained {
				t.Fatal("changed retention context accepted")
			}
		})
	}
}
