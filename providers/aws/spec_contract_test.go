package aws

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type cloudFormationSchemaDigest struct {
	PrimaryIdentifier []string                         `json:"primaryIdentifier"`
	Handlers          map[string]cloudFormationHandler `json:"handlers"`
	Properties        map[string]string                `json:"properties"`
}

type cloudFormationHandler struct {
	Permissions   []string `json:"permissions"`
	HandlerSchema *struct {
		Required []string `json:"required"`
		OneOf    []struct {
			Required []string `json:"required"`
		} `json:"oneOf"`
	} `json:"handlerSchema"`
}

// Normalized reference fields derived by the runtime rather than copied from
// the CloudFormation model.
var derivedReferenceFields = map[string]bool{
	"vpc_id": true, "subnet_ids": true, "security_group_ids": true, "volume_ids": true,
	"network_interface_ids": true, "snapshot_ids": true, "rule_group_arns": true,
	"cluster_name": true, "service_id": true,
}

// knownCloudControlDefects lists specifications that still route through a
// Cloud Control handler the official schema does not provide.
var knownCloudControlDefects = map[string]string{
	"AWS::OpenSearchService::Domain": "no list handler; moves to native ListDomainNames",
}

func loadCloudFormationDigests(t *testing.T) map[string]cloudFormationSchemaDigest {
	t.Helper()
	payload, err := os.ReadFile("catalog/source/cloudformation.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Source struct {
			URI    string `json:"uri"`
			SHA256 string `json:"sha256"`
		} `json:"source"`
		Types map[string]cloudFormationSchemaDigest `json:"types"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	if document.Source.URI != "https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip" || !sha256Pattern.MatchString(document.Source.SHA256) {
		t.Fatalf("CloudFormation schema provenance = %+v", document.Source)
	}
	return document.Types
}

// Every Cloud Control specification is checked against the pinned official
// resource schema: handlers, list inputs, relationship and protection fields.
func TestCloudControlSpecificationsMatchOfficialResourceSchemas(t *testing.T) {
	digests := loadCloudFormationDigests(t)
	runtime, err := newRuntime(&runtimeCredentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	selection := map[string][]asset.ScopeKind{}
	for _, resourceType := range runtime.catalog.ResourceTypes {
		selection[resourceType.NativeType] = resourceType.ScopeKinds
	}
	checked := 0
	for _, compiled := range runtime.bundle.Specs {
		definition := compiled.Definition
		name := definition.Metadata.NativeType
		t.Run(name, func(t *testing.T) {
			if definition.Presentation.DisplayNames["zh-CN"] == "" || definition.Presentation.DisplayNames["en-US"] == "" {
				t.Error("bilingual display names are required")
			}
			if !slices.Equal(selection[name], []asset.ScopeKind{definition.Scope.Kind}) {
				t.Errorf("scope %s differs from catalog selection %v", definition.Scope.Kind, selection[name])
			}
			if definition.Discovery.Source != cloudControlSource {
				return
			}
			checked++
			if definition.Extensions.Hook != cloudControlHook {
				t.Fatal("Cloud Control specification must use the Cloud Control hook")
			}
			digest, ok := digests[name]
			if !ok {
				t.Fatal("no pinned CloudFormation schema")
			}
			if reason, known := knownCloudControlDefects[name]; known {
				t.Skip(reason)
			}
			for _, handler := range []string{"list", "read", "delete"} {
				if _, ok := digest.Handlers[handler]; !ok {
					t.Fatalf("official schema has no %s handler", handler)
				}
			}
			plan, err := cloudControlListPlanFromSpec(definition)
			if err != nil {
				t.Fatal(err)
			}
			if definition.Discovery.List == nil {
				t.Fatal("Cloud Control specification must declare its ListResources request")
			}
			if schema := digest.Handlers["list"].HandlerSchema; schema != nil {
				required := slices.Clone(schema.Required)
				satisfied := len(schema.OneOf) == 0
				for _, option := range schema.OneOf {
					if containsAll(plan.Model, option.Required) {
						satisfied = true
					}
				}
				if !containsAll(plan.Model, required) || !satisfied {
					t.Errorf("list model %v does not provide handler inputs %+v", plan.Model, schema)
				}
			}
			for property := range plan.Model {
				if _, ok := digest.Properties[strings.Split(property, ".")[0]]; !ok {
					t.Errorf("list model property %s is not in the schema", property)
				}
			}
			if plan.usesParent() {
				if definition.Discovery.Parent == nil || definition.Discovery.Parent.Source != cloudControlSource {
					t.Fatal("parent expressions require Cloud Control parent discovery")
				}
				if _, ok := runtime.compiledSpec(definition.Discovery.Parent.NativeType); !ok {
					t.Fatal("parent kind has no specification")
				}
			}
			for _, relationship := range definition.Relationships {
				root := strings.Split(relationship.TargetIDPath, ".")[0]
				if _, ok := digest.Properties[root]; !ok && !derivedReferenceFields[root] {
					t.Errorf("relationship path %s is neither a schema property nor a derived field", relationship.TargetIDPath)
				}
			}
			action, ok := definition.Actions["delete"]
			if !ok {
				t.Fatal("delete action is required")
			}
			if protection := action.DeletionProtection; protection != nil {
				root := protection.Path
				if match := keyedPathPattern.FindStringSubmatch(root); match != nil {
					root = match[1]
				}
				if _, ok := digest.Properties[strings.Split(root, ".")[0]]; !ok {
					t.Errorf("deletion protection path %s is not in the schema", protection.Path)
				}
				if _, err := cloudControlProtectionFromSpec(name, action); err != nil {
					t.Error(err)
				}
			}
			if definition.Scope.Kind == asset.ScopeGlobal {
				if region := homeRegion(name, asset.ScopeGlobal, ""); region != "us-east-1" && region != "us-west-2" {
					t.Errorf("global home region %s", region)
				}
			}
		})
	}
	if checked < 150 {
		t.Fatalf("only %d Cloud Control specifications were checked", checked)
	}
}

func containsAll(model map[string]any, names []string) bool {
	for _, name := range names {
		if _, ok := model[name]; !ok {
			return false
		}
	}
	return true
}

func TestHomeRegionRoutesGlobalServices(t *testing.T) {
	cases := map[string]string{
		"AWS::GlobalAccelerator::Accelerator": "us-west-2", "AWS::NetworkManager::GlobalNetwork": "us-west-2",
		"AWS::IAM::Role": "us-east-1", "AWS::CloudFront::Distribution": "us-east-1",
	}
	for nativeType, want := range cases {
		if got := homeRegion(nativeType, asset.ScopeGlobal, "ap-southeast-1"); got != want {
			t.Errorf("%s home region = %s, want %s", nativeType, got, want)
		}
	}
	if got := homeRegion("AWS::EC2::Instance", asset.ScopeRegion, "eu-west-1"); got != "eu-west-1" {
		t.Fatalf("regional home region = %s", got)
	}
}

var _ = spec.ResourceKindSpec{}
