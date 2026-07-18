package spec_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func compilerCatalog() catalog.Catalog {
	return catalog.Catalog{
		Provider: asset.ProviderAliCloud,
		Source:   catalog.Source{Checksum: "catalog-sha"},
		Operations: []catalog.Operation{
			{
				Name: "DescribeInstances",
				Call: &catalog.OperationCall{
					Product: "Ecs", Version: "2014-05-26", Style: "RPC",
					Method: "POST", Path: "/", Endpoint: "ecs.{region}.aliyuncs.com",
				},
			},
			{
				Name: "DeleteInstance", Destructive: true,
				Call: &catalog.OperationCall{
					Product: "Ecs", Version: "2014-05-26", Style: "RPC",
					Method: "POST", Path: "/", Endpoint: "ecs.{region}.aliyuncs.com",
				},
			},
			{
				Name: "ModifyInstanceAttribute", Destructive: true,
				Call: &catalog.OperationCall{
					Product: "Ecs", Version: "2014-05-26", Style: "RPC",
					Method: "POST", Path: "/", Endpoint: "ecs.{region}.aliyuncs.com",
				},
			},
		},
		ResourceTypes: []catalog.ResourceType{
			{NativeType: "ACS::ECS::Instance", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
			{NativeType: "ACS::VPC::VPC", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
		},
	}
}

func validSource() []byte {
	return []byte(`schema: steward.io/resource-kind
kind: ResourceKind
metadata:
  provider: alicloud
  nativeType: ACS::ECS::Instance
  class: compute.instance
scope:
  kind: region
discovery:
  source: resource-center
  detail:
    operation: DescribeInstances
    itemsPath: Instances.Instance
    identityPath: InstanceId
fields:
  name: InstanceName
relationships:
  - type: member_of
    targetType: ACS::VPC::VPC
    targetIdPath: VpcAttributes.VpcId
actions:
  delete:
    operation: DeleteInstance
    parameters:
      InstanceId: resource.nativeId
      Force: true
    idempotency: readback
    waiter: instance_absent
extensions:
  hook: alicloud.ecs.instance
`)
}

func validHooks() spec.HookRegistry {
	return spec.HookRegistry{"alicloud.ecs.instance": {spec.HookPreflight, spec.HookWaiter}}
}

func directProductSource() []byte {
	return []byte(`schema: steward.io/resource-kind
kind: ResourceKind
metadata:
  provider: alicloud
  nativeType: ACS::ECS::Instance
scope:
  kind: region
discovery:
  source: product-api
  list:
    operation: DescribeInstances
    parameters:
      RegionId: scope.location
    itemsPath: Instances.Instance
    identityPath: InstanceId
    supportedRegions:
      - cn-hangzhou
      - cn-shanghai
    pagination:
      type: token
      tokenParameter: NextToken
      tokenPath: NextToken
      pageSizeParameter: MaxResults
actions:
  delete:
    operation: DeleteInstance
    parameters:
      InstanceId: resource.nativeId
      Force: false
    idempotency: readback
    waiter: absent
    read:
      operation: DescribeInstances
      parameters:
        RegionId: scope.location
        InstanceIds: resource.nativeIdsJson
      itemsPath: Instances.Instance
      identityPath: InstanceId
`)
}

func TestCompileDirectProductAPISpecWithoutCodeHook(t *testing.T) {
	t.Parallel()

	compiled, err := spec.Compile(directProductSource(), compilerCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.ResourceKind.Capabilities.Has(asset.CapabilityDetailed) ||
		!compiled.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("direct product API capabilities = %v", compiled.ResourceKind.Capabilities)
	}
	regions := compiled.Definition.Discovery.List.SupportedRegions
	if len(regions) != 2 || regions[0] != "cn-hangzhou" || regions[1] != "cn-shanghai" {
		t.Fatalf("direct product API supported regions = %v", regions)
	}

	unknownExpression := strings.Replace(
		string(directProductSource()),
		"resource.nativeId",
		"resource.unknown",
		1,
	)
	if _, err := spec.Compile([]byte(unknownExpression), compilerCatalog(), nil); err == nil {
		t.Fatal("unknown runtime parameter expression must fail compilation")
	}

	providerToken := strings.Replace(
		string(directProductSource()),
		"idempotency: readback",
		"idempotency: provider_token",
		1,
	)
	if _, err := spec.Compile([]byte(providerToken), compilerCatalog(), nil); err == nil {
		t.Fatal("provider_token without a catalog idempotency parameter must fail compilation")
	}

	duplicateRegion := strings.Replace(
		string(directProductSource()),
		"      - cn-shanghai",
		"      - cn-hangzhou",
		1,
	)
	if _, err := spec.Compile([]byte(duplicateRegion), compilerCatalog(), nil); err == nil {
		t.Fatal("duplicate supported region must fail compilation")
	}
}

func TestCompileFieldsSupportsShorthandAndQueryablePropertyMetadata(t *testing.T) {
	t.Parallel()

	source := strings.Replace(
		string(validSource()),
		"fields:\n  name: InstanceName",
		`fields:
  name: InstanceName
  internetChargeType:
    path: InternetChargeType
    type: string
    displayNames:
      zh-CN: 公网计费类型
      en-US: Internet charge type
    enum:
      - PayByTraffic
      - PayByBandwidth
    operators:
      - "="
      - in`,
		1,
	)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Definition.Fields["name"].Path != "InstanceName" ||
		compiled.Definition.Fields["name"].Type != spec.PropertyAny {
		t.Fatalf("shorthand field = %+v", compiled.Definition.Fields["name"])
	}
	properties := compiled.ResourceKind.Properties
	if len(properties) != 2 || properties[0].Path != "internetChargeType" ||
		properties[0].Type != "string" ||
		properties[0].DisplayNames["zh-CN"] != "公网计费类型" ||
		len(properties[0].Enum) != 2 ||
		len(properties[0].Operators) != 2 {
		t.Fatalf("compiled properties = %+v", properties)
	}
	if got := compiled.ResourceKind.FieldDisplayNames["internetChargeType"]["en-US"]; got != "Internet charge type" {
		t.Fatalf("field display name = %q", got)
	}
}

func TestCompileInventoryBackedFanoutParent(t *testing.T) {
	t.Parallel()

	source := strings.Replace(
		string(directProductSource()),
		"  list:",
		`  parent:
    source: resource-center
    nativeType: ACS::VPC::VPC
  list:`,
		1,
	)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := compiled.Definition.Discovery.Parent
	if parent == nil ||
		parent.Source != "resource-center" ||
		parent.NativeType != "ACS::VPC::VPC" {
		t.Fatalf("inventory-backed discovery parent = %+v", parent)
	}

	unknownNativeType := strings.Replace(
		source,
		"ACS::VPC::VPC",
		"ACS::VPC::Unknown",
		1,
	)
	if _, err := spec.Compile([]byte(unknownNativeType), compilerCatalog(), nil); err == nil {
		t.Fatal("inventory-backed parent with unknown native type must fail compilation")
	}
}

func TestCompileActionDeletionProtection(t *testing.T) {
	t.Parallel()

	source := strings.Replace(
		string(directProductSource()),
		"    idempotency: readback",
		`    deletionProtection:
      path: DeletionProtection
      enabledValues: ["true"]
      disable:
        operation: ModifyInstanceAttribute
        parameters:
          InstanceId: resource.nativeId
          DeletionProtection: false
    idempotency: readback`,
		1,
	)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	protection := compiled.Definition.Actions["delete"].DeletionProtection
	if protection == nil ||
		protection.Disable.Operation != "ModifyInstanceAttribute" ||
		protection.Path != "DeletionProtection" {
		t.Fatalf("deletion protection = %+v", protection)
	}

	unknownDisable := strings.Replace(
		source,
		"operation: ModifyInstanceAttribute",
		"operation: DisableUnknownProtection",
		1,
	)
	if _, err := spec.Compile([]byte(unknownDisable), compilerCatalog(), nil); err == nil {
		t.Fatal("unknown deletion protection disable operation must fail compilation")
	}
}

func TestCompileProviderDefinedInventorySourceWithAPIProvenance(t *testing.T) {
	t.Parallel()

	source := strings.Replace(
		string(directProductSource()),
		"source: product-api",
		"source: cen-topology",
		1,
	)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Definition.Discovery.Source != "cen-topology" ||
		compiled.Definition.Discovery.List == nil ||
		!compiled.ResourceKind.Capabilities.Has(asset.CapabilityDetailed) {
		t.Fatalf("provider-defined inventory source = %+v", compiled)
	}
}

func TestCompileValidSpecIsDeterministicAndInfersCapabilities(t *testing.T) {
	t.Parallel()

	first, err := spec.Compile(validSource(), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile valid spec: %v", err)
	}
	second, err := spec.Compile(validSource(), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile valid spec again: %v", err)
	}
	if first.Hash == "" || len(first.Hash) != 64 || first.Hash != second.Hash || first.Revision != second.Revision {
		t.Fatalf("non-deterministic hash/revision: first=%+v second=%+v", first, second)
	}
	for _, capability := range []asset.Capability{
		asset.CapabilityIndexed,
		asset.CapabilityDetailed,
		asset.CapabilityRelated,
		asset.CapabilityActionable,
	} {
		if !first.ResourceKind.Capabilities.Has(capability) {
			t.Fatalf("missing inferred capability %q in %v", capability, first.ResourceKind.Capabilities)
		}
	}
}

func TestCompilerRejectsUnknownAndUnsafeDeleteOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		catalog catalog.Catalog
		source  string
	}{
		{name: "unknown operation", catalog: compilerCatalog(), source: strings.ReplaceAll(string(validSource()), "DeleteInstance", "DestroyInstance")},
		{
			name: "not classified destructive",
			catalog: func() catalog.Catalog {
				c := compilerCatalog()
				c.Operations[1].Destructive = false
				return c
			}(),
			source: string(validSource()),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := spec.Compile([]byte(tt.source), tt.catalog, validHooks()); err == nil {
				t.Fatal("unsafe delete operation must fail compilation")
			}
		})
	}
}

func TestCompilerRejectsStructurallyInvalidSpecs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "unknown field", mutate: func(source string) string {
			return strings.Replace(source, "kind: ResourceKind", "kind: ResourceKind\nunknown: true", 1)
		}},
		{name: "invalid field path", mutate: func(source string) string {
			return strings.Replace(source, "InstanceName", "name[0]", 1)
		}},
		{name: "unsupported scope", mutate: func(source string) string {
			return strings.Replace(source, "kind: region", "kind: project", 1)
		}},
		{name: "missing waiter and readback", mutate: func(source string) string {
			return strings.Replace(source, "    waiter: instance_absent\n", "", 1)
		}},
		{name: "unregistered hook", mutate: func(source string) string {
			return strings.Replace(source, "alicloud.ecs.instance", "unknown-hook", 1)
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := tt.mutate(string(validSource()))
			if _, err := spec.Compile([]byte(source), compilerCatalog(), validHooks()); err == nil {
				t.Fatalf("invalid spec %q must be rejected", tt.name)
			}
		})
	}
}

func TestCompilerRejectsHookWithoutRequiredCapability(t *testing.T) {
	t.Parallel()

	hooks := spec.HookRegistry{"alicloud.ecs.instance": {spec.HookPreflight}}
	if _, err := spec.Compile(validSource(), compilerCatalog(), hooks); err == nil {
		t.Fatal("waiter reference must require a waiter-capable hook")
	}
}

func TestCompilerRejectsFailureStatesWithoutTerminalWaiter(t *testing.T) {
	t.Parallel()

	source := strings.Replace(
		string(directProductSource()),
		"    waiter: absent\n",
		"    waiter: absent\n    failureStates:\n      - DELETE_FAILED\n",
		1,
	)
	if _, err := spec.Compile([]byte(source), compilerCatalog(), nil); err == nil {
		t.Fatal("failureStates without a terminal waiter must be rejected")
	}
}

func TestCompilerRejectsNonTerminalSpecShape(t *testing.T) {
	t.Parallel()

	source := strings.Replace(string(validSource()), "  kind: region", "  kinds: [region]", 1)
	if _, err := spec.Compile([]byte(source), compilerCatalog(), validHooks()); err == nil {
		t.Fatal("non-terminal scope.kinds shape must not be accepted")
	}
}

func TestCompileBundleRejectsDuplicateNativeTypes(t *testing.T) {
	t.Parallel()

	_, err := spec.CompileBundle([][]byte{validSource(), validSource()}, compilerCatalog(), validHooks())
	if err == nil {
		t.Fatal("duplicate native type in bundle must be rejected")
	}
}

func TestCompilerBuildsPureGovernanceRulesAndRejectsTrailingExpressions(t *testing.T) {
	t.Parallel()

	governance := `governance:
  rules:
    - id: stopped-instance
      title: Stopped instance
      severity: high
      condition: state == "Stopped"
`
	source := strings.Replace(string(validSource()), "actions:\n", governance+"actions:\n", 1)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile governance rule: %v", err)
	}
	if len(compiled.Rules) != 1 || compiled.Rules[0].ID != "stopped-instance" || compiled.Rules[0].Operator != spec.RuleEqual || !compiled.ResourceKind.Capabilities.Has(asset.CapabilityGoverned) {
		t.Fatalf("compiled governance rule = %+v kind=%+v", compiled.Rules, compiled.ResourceKind)
	}
	unsafe := strings.Replace(source, `state == "Stopped"`, `state == "Stopped" || true`, 1)
	if _, err := spec.Compile([]byte(unsafe), compilerCatalog(), validHooks()); err == nil {
		t.Fatal("trailing rule expression must be rejected at bundle compilation")
	}
}

func TestCompilerCarriesLocalizedNamesAndFlatIcon(t *testing.T) {
	t.Parallel()

	presentation := `presentation:
  displayNames:
    zh-CN: 云服务器
    en-US: Elastic Compute Service
  fieldDisplayNames:
    vpc_id:
      zh-CN: 所属专有网络
      en-US: VPC
  icon: network
`
	source := strings.Replace(string(validSource()), "scope:\n", presentation+"scope:\n", 1)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile localized presentation metadata: %v", err)
	}
	if compiled.ResourceKind.DisplayNames["zh-CN"] != "云服务器" || compiled.ResourceKind.DisplayNames["en-US"] != "Elastic Compute Service" {
		t.Fatalf("localized names = %+v", compiled.ResourceKind.DisplayNames)
	}
	labels := compiled.ResourceKind.FieldDisplayNames["vpc_id"]
	if labels["zh-CN"] != "所属专有网络" || labels["en-US"] != "VPC" {
		t.Fatalf("field display names = %+v", compiled.ResourceKind.FieldDisplayNames)
	}
	compiled.Definition.Presentation.FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"
	if compiled.ResourceKind.FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("compiled resource kind field display names were mutated: %+v", compiled.ResourceKind.FieldDisplayNames)
	}
	if compiled.Definition.Presentation.Icon != "network" || compiled.ResourceKind.Icon != "network" {
		t.Fatalf("presentation = %+v resource kind = %+v", compiled.Definition.Presentation, compiled.ResourceKind)
	}
}

func TestCompilerCarriesFlatIcon(t *testing.T) {
	t.Parallel()

	presentation := `presentation:
  icon: disk
`
	source := strings.Replace(string(validSource()), "scope:\n", presentation+"scope:\n", 1)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile icon presentation: %v", err)
	}
	if compiled.Definition.Presentation.Icon != "disk" || compiled.ResourceKind.Icon != "disk" {
		t.Fatalf("presentation = %+v resource kind icon = %q", compiled.Definition.Presentation, compiled.ResourceKind.Icon)
	}
}

func TestCompilerCarriesValidatedConsoleLinkTemplate(t *testing.T) {
	t.Parallel()

	presentation := `presentation:
  consoleLinkTemplate: "https://ecs.console.aliyun.com/server/region/{regionId}?instanceId={nativeId}"
`
	source := strings.Replace(string(validSource()), "scope:\n", presentation+"scope:\n", 1)
	compiled, err := spec.Compile([]byte(source), compilerCatalog(), validHooks())
	if err != nil {
		t.Fatalf("compile console link template: %v", err)
	}
	want := "https://ecs.console.aliyun.com/server/region/{regionId}?instanceId={nativeId}"
	if compiled.Definition.Presentation.ConsoleLinkTemplate != want ||
		compiled.ResourceKind.ConsoleLinkTemplate != want {
		t.Fatalf(
			"presentation = %+v resource kind = %+v",
			compiled.Definition.Presentation,
			compiled.ResourceKind,
		)
	}
}

func TestCompilerCarriesValidatedConsoleLinkTemplateFilters(t *testing.T) {
	t.Parallel()

	for _, template := range []string{
		"https://cloudsso.console.aliyun.com/{regionId}/groups/{nativeId|suffix::}/info",
		"https://eci.console.aliyun.com/#/eci/{regionId}/image/{nativeId|replacePrefix:imc-,eci-}/{nativeId}/productevents",
	} {
		presentation := fmt.Sprintf(
			"presentation:\n  consoleLinkTemplate: %q\n",
			template,
		)
		source := strings.Replace(
			string(validSource()),
			"scope:\n",
			presentation+"scope:\n",
			1,
		)
		compiled, err := spec.Compile(
			[]byte(source),
			compilerCatalog(),
			validHooks(),
		)
		if err != nil {
			t.Fatalf("compile console link template %q: %v", template, err)
		}
		if compiled.ResourceKind.ConsoleLinkTemplate != template {
			t.Fatalf(
				"console link template = %q, want %q",
				compiled.ResourceKind.ConsoleLinkTemplate,
				template,
			)
		}
	}
}

func TestCompilerCarriesNormalizedFieldConsoleLinkPlaceholder(t *testing.T) {
	t.Parallel()

	template := "https://ecs.console.aliyun.com/server/{nativeId}?name={name}"
	presentation := fmt.Sprintf(
		"presentation:\n  consoleLinkTemplate: %q\n",
		template,
	)
	source := strings.Replace(
		string(validSource()),
		"scope:\n",
		presentation+"scope:\n",
		1,
	)
	compiled, err := spec.Compile(
		[]byte(source),
		compilerCatalog(),
		validHooks(),
	)
	if err != nil {
		t.Fatalf("compile normalized-field console link template: %v", err)
	}
	if compiled.ResourceKind.ConsoleLinkTemplate != template {
		t.Fatalf(
			"console link template = %q, want %q",
			compiled.ResourceKind.ConsoleLinkTemplate,
			template,
		)
	}
}

func TestCompilerRejectsUnsafeConsoleLinkTemplates(t *testing.T) {
	t.Parallel()

	for _, template := range []string{
		"http://ecs.console.aliyun.com/{nativeId}",
		"https://ecs.console.aliyun.com/{resourceId}",
		"https://ecs.console.aliyun.com/{nativeId",
		"https://ecs.console.aliyun.com/{nativeId|unknown:x}",
		"https://ecs.console.aliyun.com/{regionId|suffix::}",
		"https://ecs.console.aliyun.com/{nativeId|replacePrefix:imc-}",
	} {
		presentation := fmt.Sprintf(
			"presentation:\n  consoleLinkTemplate: %q\n",
			template,
		)
		source := strings.Replace(
			string(validSource()),
			"scope:\n",
			presentation+"scope:\n",
			1,
		)
		if _, err := spec.Compile(
			[]byte(source),
			compilerCatalog(),
			validHooks(),
		); err == nil {
			t.Fatalf("unsafe console link template %q must fail", template)
		}
	}
}

func TestCompilerRejectsRemovedTopologyGroupingContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		presentation string
	}{
		{name: "topology", presentation: "presentation:\n  topology:\n    icon: network\n"},
		{name: "group kind", presentation: "presentation:\n  groupKind: network\n"},
		{name: "group identity fields", presentation: "presentation:\n  groupIdentityFields:\n    - normalized.vpc_id\n"},
		{name: "parent scope", presentation: "presentation:\n  parentScope: region\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := strings.Replace(string(validSource()), "scope:\n", test.presentation+"scope:\n", 1)
			if _, err := spec.Compile([]byte(source), compilerCatalog(), validHooks()); err == nil {
				t.Fatalf("removed presentation contract %q must fail", test.name)
			}
		})
	}
}

func TestCompilerRejectsInvalidFieldDisplayNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		presentation string
	}{
		{name: "unsupported locale", presentation: "presentation:\n  fieldDisplayNames:\n    vpc_id:\n      fr-FR: VPC\n"},
		{name: "whitespace-only label", presentation: "presentation:\n  fieldDisplayNames:\n    vpc_id:\n      zh-CN: '   '\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := strings.Replace(string(validSource()), "scope:\n", test.presentation+"scope:\n", 1)
			if _, err := spec.Compile([]byte(source), compilerCatalog(), validHooks()); err == nil {
				t.Fatalf("invalid field display names %q must fail", test.name)
			}
		})
	}
}
