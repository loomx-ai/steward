package gcp

import (
	"slices"
	"testing"
)

func TestReferencesIncludeWorkloadServiceAccounts(t *testing.T) {
	c := &client{project: "proj", number: "123456789"}
	want := "//iam.googleapis.com/projects/proj/serviceAccounts/app@proj.iam.gserviceaccount.com"
	defaultCompute := "//iam.googleapis.com/projects/proj/serviceAccounts/123456789-compute@developer.gserviceaccount.com"
	for name, data := range map[string]map[string]any{
		"compute instance": {"serviceAccounts": []any{map[string]any{"email": "app@proj.iam.gserviceaccount.com", "scopes": []any{"https://www.googleapis.com/auth/cloud-platform"}}}},
		"instance template": {"properties": map[string]any{"serviceAccounts": []any{map[string]any{"email": "app@proj.iam.gserviceaccount.com"}}}},
		"gke node pool":     {"config": map[string]any{"serviceAccount": "app@proj.iam.gserviceaccount.com"}},
		"cloud run":         {"template": map[string]any{"serviceAccount": "app@proj.iam.gserviceaccount.com"}},
		"cloud function":    {"serviceConfig": map[string]any{"serviceAccountEmail": "app@proj.iam.gserviceaccount.com"}},
	} {
		refs := references(c, data)["iam.googleapis.com/ServiceAccount"]
		if !slices.Contains(refs, want) {
			t.Fatal(name, refs)
		}
	}
	refs := references(c, map[string]any{"serviceAccounts": []any{map[string]any{"email": "123456789-compute@developer.gserviceaccount.com"}, map[string]any{"email": "other@elsewhere.iam.gserviceaccount.com"}}})["iam.googleapis.com/ServiceAccount"]
	if !slices.Contains(refs, defaultCompute) || slices.Contains(refs, want) {
		t.Fatal("default and foreign service accounts", refs)
	}
}
