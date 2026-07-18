package resourcequery

import (
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestParseMatchesTypedResourceProperties(t *testing.T) {
	expression, err := Parse(`type = "ACS::OSS::Bucket" AND properties.acl IN ("public-read", "public-read-write")`)
	if err != nil {
		t.Fatal(err)
	}
	publicBucket := asset.Asset{
		Identity:   asset.Identity{NativeType: "ACS::OSS::Bucket"},
		Normalized: map[string]any{"acl": "public-read"},
	}
	privateBucket := publicBucket
	privateBucket.Normalized = map[string]any{"acl": "private"}
	instance := publicBucket
	instance.Identity.NativeType = "ACS::ECS::Instance"
	if !expression.Match(publicBucket) {
		t.Fatal("public bucket did not match")
	}
	if expression.Match(privateBucket) || expression.Match(instance) {
		t.Fatal("query matched a private bucket or another resource type")
	}
}

func TestValidateNarrowsPropertiesByResourceType(t *testing.T) {
	kinds := []asset.ResourceKind{
		{NativeType: "ACS::OSS::Bucket", Properties: []asset.ResourceProperty{{Path: "acl", Type: "string"}}},
		{NativeType: "ACS::ECS::Instance", Properties: []asset.ResourceProperty{{Path: "instanceType", Type: "string"}}},
	}
	expression, err := Parse(`type = "ACS::OSS::Bucket" AND properties.instanceType = "ecs.g8i.large"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := expression.Validate(kinds); err == nil || !strings.Contains(err.Error(), "instanceType") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateUsesPropertyTypesAndResourceTypeAlias(t *testing.T) {
	kinds := []asset.ResourceKind{{
		NativeType: "ACS::ECS::Instance",
		Properties: []asset.ResourceProperty{{Path: "cpu", Type: "integer"}},
	}}
	valid, err := Parse(`resourceType = "ACS::ECS::Instance" AND properties.cpu >= 8`)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(kinds); err != nil {
		t.Fatalf("valid typed query failed validation: %v", err)
	}
	invalid, err := Parse(`resourceType = "ACS::ECS::Instance" AND properties.cpu = "eight"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := invalid.Validate(kinds); err == nil || !strings.Contains(err.Error(), "value type") {
		t.Fatalf("value type validation error = %v", err)
	}
}

func TestSQLUsesArgumentsForValuesAndJSONPaths(t *testing.T) {
	expression, err := Parse(`type = "ACS::OSS::Bucket" AND properties.acl = "public-read"`)
	if err != nil {
		t.Fatal(err)
	}
	query, arguments, err := expression.SQL("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "ACS::OSS::Bucket") || strings.Contains(query, "public-read") {
		t.Fatalf("SQL contains a query value: %s", query)
	}
	want := []any{"ACS::OSS::Bucket", "$.normalized.acl", "public-read"}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments = %#v, want %#v", arguments, want)
	}
}

func TestResourceIDIsThePublicQueryField(t *testing.T) {
	fields := SupportedFields(nil)
	if !contains(fields, "resourceId") || contains(fields, "nativeId") {
		t.Fatalf("supported fields = %#v", fields)
	}

	expression, err := Parse(`resourceId = "bucket-public"`)
	if err != nil {
		t.Fatal(err)
	}
	value := asset.Asset{Identity: asset.Identity{NativeID: "bucket-public"}}
	if err := expression.Validate(nil); err != nil {
		t.Fatalf("resourceId validation failed: %v", err)
	}
	if !expression.Match(value) {
		t.Fatal("resourceId did not match the provider resource ID")
	}
	query, arguments, err := expression.SQL("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if query != "(assets.native_id = ?)" || !reflect.DeepEqual(arguments, []any{"bucket-public"}) {
		t.Fatalf("SQL = %q, arguments = %#v", query, arguments)
	}

	legacy, err := Parse(`nativeId = "bucket-public"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Validate(nil); err == nil || !strings.Contains(err.Error(), `field "nativeId" is not supported`) {
		t.Fatalf("legacy nativeId validation error = %v", err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestParseRejectsUnquotedAndTrailingInput(t *testing.T) {
	for _, query := range []string{
		`type = ACS::OSS::Bucket`,
		`name = "bucket"; DROP TABLE assets`,
		`dirty IN (true, "true")`,
	} {
		if _, err := Parse(query); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", query)
		}
	}
}
