package cleanup

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestImpactOutcomeUpdatesAreScopedToTheirCleanupStep(t *testing.T) {
	values := []plan.ImpactItem{
		{ID: "impact-a", CleanupTaskID: "cln-a", DelegatedTo: "step-a", Expected: plan.ExpectedDelegatedDelete},
		{ID: "impact-b", CleanupTaskID: "cln-a", DelegatedTo: "step-b", Expected: plan.ExpectedRetainShared},
	}

	tests := []struct {
		name string
		run  func([]plan.ImpactItem, plan.StepID) []plan.ImpactItem
		want plan.ImpactResult
	}{
		{name: "success", run: initialImpactResults, want: plan.ImpactDelegated},
		{name: "failure", run: failedImpactResults, want: plan.ImpactCleanupFailed},
		{name: "dirty", run: ignoredDirtyImpactResults, want: plan.ImpactIgnoredDirty},
		{name: "unsupported", run: unsupportedImpactResults, want: plan.ImpactStillPresent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updates := test.run(values, "step-a")
			if len(updates) != 1 || updates[0].ID != "impact-a" || updates[0].Result != test.want {
				t.Fatalf("updates=%+v", updates)
			}
		})
	}
}
