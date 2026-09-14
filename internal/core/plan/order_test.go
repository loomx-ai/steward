package plan

import "testing"

func TestOrderStepsRejectsInvalidPersistedDAG(t *testing.T) {
	for _, steps := range [][]CleanupTaskStep{
		{{ID: "a", AssetID: "a", DependsOn: []StepID{"missing"}}},
		{{ID: "a", AssetID: "a", DependsOn: []StepID{"b"}}, {ID: "b", AssetID: "b", DependsOn: []StepID{"a"}}},
		{{ID: "a", AssetID: "a"}, {ID: "a", AssetID: "b"}},
		{{ID: "a", AssetID: "a"}, {ID: "b", AssetID: "a"}},
		{{ID: "a"}},
	} {
		if _, err := OrderSteps(steps); err == nil {
			t.Fatal("invalid persisted DAG accepted", steps)
		}
	}
}
