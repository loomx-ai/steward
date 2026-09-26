package gcp

import (
	"slices"
	"testing"
)

// Triggers that Cloud Run functions and other services create are removed
// through their owner; user triggers stay deletable.
func TestEventarcServiceManagedTriggersAreProtected(t *testing.T) {
	managed := map[string]any{"labels": map[string]any{"goog-managed-by": "cloudfunctions"}}
	if reason := protectionReason(eventarcTriggerType, managed); reason != "trigger_managed_by_service" {
		t.Fatalf("managed trigger reason = %q", reason)
	}
	if reason := protectionReason(eventarcTriggerType, map[string]any{"labels": map[string]any{"team": "a"}}); reason != "" {
		t.Fatalf("user trigger reason = %q", reason)
	}
}

// An enrollment depends on its message bus and the pipeline it delivers to.
func TestEventarcEnrollmentReferencesBusAndPipeline(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	const location = "projects/sample-project/locations/us-central1/"
	refs := references(c, map[string]any{
		"name": location + "enrollments/paid", "messageBus": location + "messageBuses/orders", "destination": location + "pipelines/fulfil",
	})
	if !slices.Equal(refs["eventarc.googleapis.com/MessageBus"], []string{"//eventarc.googleapis.com/" + location + "messageBuses/orders"}) ||
		!slices.Equal(refs["eventarc.googleapis.com/Pipeline"], []string{"//eventarc.googleapis.com/" + location + "pipelines/fulfil"}) {
		t.Fatalf("references = %v", refs)
	}
}
