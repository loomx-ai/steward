package alicloud

import (
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestOSSBucketSpecAndEnricherExposeACLForResourceQueries(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	var found bool
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != ossBucketNativeType {
			continue
		}
		found = true
		enrich := compiled.Definition.Discovery.Enrich
		if enrich == nil || enrich.Operation != "AlibabaCloud.OSS.GetBucketInfo" ||
			enrich.IdentityPath != "Name" || enrich.MaxBatchSize != 1 {
			t.Fatalf("OSS discovery enrich = %+v", enrich)
		}
		var foundACL bool
		for _, property := range compiled.ResourceKind.Properties {
			if property.Path != "acl" {
				continue
			}
			foundACL = true
			if property.Type != "string" ||
				!reflect.DeepEqual(property.Enum, []any{"private", "public-read", "public-read-write"}) {
				t.Fatalf("OSS ACL property = %+v", property)
			}
			break
		}
		if !foundACL {
			t.Fatal("OSS ACL property metadata is missing")
		}
	}
	if !found {
		t.Fatal("OSS bucket resource spec not found")
	}

	items := []contracts.InventoryItem{{
		NativeType: ossBucketNativeType,
		NativeID:   "public-assets",
		Normalized: map[string]any{"name": "public-assets"},
	}}
	details := map[string]map[string]any{
		"public-assets": {
			"Name": "public-assets",
			"AccessControlList": map[string]any{
				"Grant": "public-read",
			},
		},
	}
	enriched := enrichOSSBucketProperties(items, details)
	if len(enriched) != 1 || enriched[0].Normalized["acl"] != "public-read" {
		t.Fatalf("enriched OSS bucket = %+v", enriched)
	}
}
