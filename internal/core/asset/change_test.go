package asset

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiffAssetsReportsUserVisibleAttributes(t *testing.T) {
	before := Asset{
		Name: "api-01", State: "Running", Tags: map[string]string{"env": "dev", "team": "core"},
		Normalized: map[string]any{"instance_type": "ecs.g7.large", "network": map[string]any{"vpc_id": "vpc-1"}, "count": float64(2)},
	}
	after := Asset{
		Name: "api-01", State: "Stopped", Tags: map[string]string{"env": "prod"},
		Normalized: map[string]any{"instance_type": "ecs.g7.large", "network": map[string]any{"vpc_id": "vpc-2"}, "count": json.Number("2")},
	}
	got := DiffAssets(before, after)
	want := []FieldChange{
		{Path: "network.vpc_id", Before: "vpc-1", After: "vpc-2"},
		{Path: "state", Before: "Running", After: "Stopped"},
		{Path: "tags.env", Before: "dev", After: "prod"},
		{Path: "tags.team", Before: "core", After: nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffAssets() = %#v, want %#v", got, want)
	}
	if changes := DiffAssets(before, before); len(changes) != 0 {
		t.Fatalf("identical assets produced changes: %#v", changes)
	}
}

func TestMergeAssetChangeDescribesTheWholeScan(t *testing.T) {
	modified := func(path string, before, after any) AssetChange {
		return AssetChange{ID: "chg-1", Type: ChangeModified, Fields: []FieldChange{{Path: path, Before: before, After: after}}}
	}
	merged, keep := MergeAssetChange(modified("state", "Running", "Stopping"), modified("state", "Stopping", "Stopped"))
	if !keep || len(merged.Fields) != 1 || merged.Fields[0].Before != "Running" || merged.Fields[0].After != "Stopped" || merged.ID != "chg-1" {
		t.Fatalf("modified+modified = %#v keep=%v", merged, keep)
	}
	if _, keep := MergeAssetChange(modified("state", "Running", "Stopped"), modified("state", "Stopped", "Running")); keep {
		t.Fatal("a change reverted within the scan must not be kept")
	}
	if _, keep := MergeAssetChange(AssetChange{Type: ChangeAdded}, AssetChange{Type: ChangeRemoved}); keep {
		t.Fatal("added then removed within one scan must cancel out")
	}
	if merged, keep := MergeAssetChange(AssetChange{Type: ChangeAdded}, modified("state", "a", "b")); !keep || merged.Type != ChangeAdded || merged.Fields != nil {
		t.Fatalf("added+modified = %#v keep=%v", merged, keep)
	}
	if merged, keep := MergeAssetChange(modified("state", "a", "b"), AssetChange{Type: ChangeRemoved}); !keep || merged.Type != ChangeRemoved {
		t.Fatalf("modified+removed = %#v keep=%v", merged, keep)
	}
	if _, keep := MergeAssetChange(AssetChange{Type: ChangeRemoved}, AssetChange{Type: ChangeAdded}); keep {
		t.Fatal("an asset seen again in the same scan never left")
	}
}
