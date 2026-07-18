package spec

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

//go:embed resource-kind.schema.json
var resourceKindSchema []byte

var (
	compiledSchema     *jsonschema.Schema
	compiledSchemaErr  error
	compiledSchemaOnce sync.Once
)

func Compile(source []byte, providerCatalog catalog.Catalog, hooks HookRegistry) (CompiledSpec, error) {
	definition, raw, err := decodeStrict(source)
	if err != nil {
		return CompiledSpec{}, err
	}
	if err := validateDocument(raw); err != nil {
		return CompiledSpec{}, err
	}
	capabilities, err := validateSemantics(definition, providerCatalog, hooks)
	if err != nil {
		return CompiledSpec{}, err
	}
	compiledRules, err := compileRules(definition.Governance)
	if err != nil {
		return CompiledSpec{}, err
	}
	canonical, err := json.Marshal(struct {
		Definition      ResourceKindSpec `json:"definition"`
		CatalogChecksum string           `json:"catalog_checksum"`
	}{definition, providerCatalog.Source.Checksum})
	if err != nil {
		return CompiledSpec{}, fmt.Errorf("canonicalize spec: %w", err)
	}
	hash := sha256.Sum256(canonical)
	digest := hex.EncodeToString(hash[:])
	resourceKind, err := providerCatalog.ResourceKind(definition.Metadata.NativeType, digest, capabilities)
	if err != nil {
		return CompiledSpec{}, err
	}
	if definition.Metadata.Class != "" {
		resourceKind.Class = definition.Metadata.Class
	}
	resourceKind.DisplayNames = cloneStringMap(definition.Presentation.DisplayNames)
	resourceKind.FieldDisplayNames = compiledFieldDisplayNames(definition)
	resourceKind.Properties = compiledProperties(definition)
	if definition.Presentation.Icon != "" {
		resourceKind.Icon = definition.Presentation.Icon
	}
	resourceKind.ConsoleLinkTemplate = definition.Presentation.ConsoleLinkTemplate
	resourceKind.SummaryFields = sortedKeys(definition.Fields)
	return CompiledSpec{
		Definition:      definition,
		ResourceKind:    resourceKind,
		Rules:           compiledRules,
		CatalogChecksum: providerCatalog.Source.Checksum,
		Revision:        digest,
		Hash:            digest,
	}, nil
}

var (
	ruleConditionPattern          = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)*)\s*(==|!=|>=|<=|>|<)\s*(.+)$`)
	resourceNormalizedPathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)*$`)
	consoleLinkPlaceholderPattern = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)(?:\|([A-Za-z][A-Za-z0-9]*)(?::([^{}]*))?)?\}`)
)

func compileRules(governance *GovernanceSpec) ([]CompiledRule, error) {
	if governance == nil {
		return nil, nil
	}
	compiled := make([]CompiledRule, 0, len(governance.Rules))
	seen := make(map[string]struct{}, len(governance.Rules))
	for _, rule := range governance.Rules {
		if _, exists := seen[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate governance rule %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		matches := ruleConditionPattern.FindStringSubmatch(strings.TrimSpace(rule.Condition))
		if len(matches) != 4 {
			return nil, fmt.Errorf("governance rule %q has unsupported condition %q", rule.ID, rule.Condition)
		}
		var expected any
		decoder := json.NewDecoder(strings.NewReader(matches[3]))
		decoder.UseNumber()
		if err := decoder.Decode(&expected); err != nil {
			return nil, fmt.Errorf("governance rule %q expected value must be a JSON literal: %w", rule.ID, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("governance rule %q condition contains a trailing expression", rule.ID)
		}
		compiled = append(compiled, CompiledRule{
			ID: rule.ID, Title: rule.Title, Description: rule.Description, Severity: rule.Severity,
			FieldPath: strings.Split(matches[1], "."), Operator: RuleOperator(matches[2]), Expected: expected,
		})
	}
	sort.Slice(compiled, func(i, j int) bool { return compiled[i].ID < compiled[j].ID })
	return compiled, nil
}

func CompileBundle(sources [][]byte, providerCatalog catalog.Catalog, hooks HookRegistry) (Bundle, error) {
	if len(sources) == 0 {
		return Bundle{}, fmt.Errorf("spec bundle is empty")
	}
	bundle := Bundle{Provider: providerCatalog.Provider}
	seen := make(map[string]struct{}, len(sources))
	for index, source := range sources {
		compiled, err := Compile(source, providerCatalog, hooks)
		if err != nil {
			return Bundle{}, fmt.Errorf("compile spec %d: %w", index, err)
		}
		nativeType := compiled.ResourceKind.NativeType
		if _, exists := seen[nativeType]; exists {
			return Bundle{}, fmt.Errorf("duplicate native type %q in spec bundle", nativeType)
		}
		seen[nativeType] = struct{}{}
		bundle.Specs = append(bundle.Specs, compiled)
	}
	sort.Slice(bundle.Specs, func(i, j int) bool {
		return bundle.Specs[i].ResourceKind.NativeType < bundle.Specs[j].ResourceKind.NativeType
	})
	digests := make([]string, 0, len(bundle.Specs))
	for _, compiled := range bundle.Specs {
		digests = append(digests, compiled.Hash)
	}
	canonical, err := json.Marshal(struct {
		Provider        asset.Provider `json:"provider"`
		CatalogChecksum string         `json:"catalog_checksum"`
		SpecHashes      []string       `json:"spec_hashes"`
	}{providerCatalog.Provider, providerCatalog.Source.Checksum, digests})
	if err != nil {
		return Bundle{}, fmt.Errorf("canonicalize spec bundle: %w", err)
	}
	hash := sha256.Sum256(canonical)
	bundle.Hash = hex.EncodeToString(hash[:])
	bundle.Revision = bundle.Hash
	for index := range bundle.Specs {
		bundle.Specs[index].ResourceKind.BundleRevision = bundle.Revision
	}
	return bundle, nil
}

func decodeStrict(source []byte) (ResourceKindSpec, any, error) {
	var definition ResourceKindSpec
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	decoder.KnownFields(true)
	if err := decoder.Decode(&definition); err != nil {
		return ResourceKindSpec{}, nil, fmt.Errorf("decode spec YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ResourceKindSpec{}, nil, fmt.Errorf("spec must contain exactly one YAML document")
		}
		return ResourceKindSpec{}, nil, fmt.Errorf("decode trailing YAML document: %w", err)
	}
	var yamlValue any
	if err := yaml.Unmarshal(source, &yamlValue); err != nil {
		return ResourceKindSpec{}, nil, fmt.Errorf("decode raw spec YAML: %w", err)
	}
	normalized, err := normalizeYAML(yamlValue)
	if err != nil {
		return ResourceKindSpec{}, nil, err
	}
	return definition, normalized, nil
}

func validateDocument(document any) error {
	compiledSchemaOnce.Do(func() {
		schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(resourceKindSchema))
		if err != nil {
			compiledSchemaErr = fmt.Errorf("decode embedded resource kind schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		if err := compiler.AddResource("resource-kind.schema.json", schemaDocument); err != nil {
			compiledSchemaErr = fmt.Errorf("register embedded resource kind schema: %w", err)
			return
		}
		compiledSchema, compiledSchemaErr = compiler.Compile("resource-kind.schema.json")
	})
	if compiledSchemaErr != nil {
		return compiledSchemaErr
	}
	if err := compiledSchema.Validate(document); err != nil {
		return fmt.Errorf("validate resource kind schema: %w", err)
	}
	return nil
}

func validateSemantics(definition ResourceKindSpec, providerCatalog catalog.Catalog, hooks HookRegistry) (asset.CapabilitySet, error) {
	if definition.Metadata.Provider != providerCatalog.Provider {
		return nil, fmt.Errorf("spec provider %q does not match catalog provider %q", definition.Metadata.Provider, providerCatalog.Provider)
	}
	resourceType, exists := providerCatalog.ResourceType(definition.Metadata.NativeType)
	if !exists {
		return nil, fmt.Errorf("native type %q is not present in catalog", definition.Metadata.NativeType)
	}
	if !containsScope(resourceType.ScopeKinds, definition.Scope.Kind) {
		return nil, fmt.Errorf("scope kind %q is not supported by catalog resource %q", definition.Scope.Kind, resourceType.NativeType)
	}
	for locale, displayName := range definition.Presentation.DisplayNames {
		if locale != "zh-CN" && locale != "en-US" {
			return nil, fmt.Errorf("presentation locale %q is not supported", locale)
		}
		if strings.TrimSpace(displayName) == "" {
			return nil, fmt.Errorf("presentation locale %q requires a display name", locale)
		}
	}
	for field, labels := range definition.Presentation.FieldDisplayNames {
		if strings.TrimSpace(field) == "" {
			return nil, fmt.Errorf("presentation field display name requires a field path")
		}
		for locale, displayName := range labels {
			if locale != "zh-CN" && locale != "en-US" {
				return nil, fmt.Errorf(
					"presentation field %q locale %q is not supported",
					field,
					locale,
				)
			}
			if strings.TrimSpace(displayName) == "" {
				return nil, fmt.Errorf(
					"presentation field %q locale %q requires a display name",
					field,
					locale,
				)
			}
		}
	}
	if err := validateConsoleLinkTemplate(
		definition.Presentation.ConsoleLinkTemplate,
		definition.Fields,
	); err != nil {
		return nil, err
	}
	capabilities := asset.CapabilitySet{asset.CapabilityIndexed}
	if definition.Discovery.List != nil {
		if definition.Discovery.Parent != nil {
			if err := validateDiscoveryParent(*definition.Discovery.Parent, providerCatalog); err != nil {
				return nil, err
			}
		}
		if err := validateProductAPI(*definition.Discovery.List, providerCatalog, "discovery list", false, false, definition.Discovery.Parent != nil); err != nil {
			return nil, err
		}
		capabilities = append(capabilities, asset.CapabilityDetailed)
	} else if definition.Discovery.Source == "product-api" {
		return nil, fmt.Errorf("product-api discovery requires a list operation")
	} else if definition.Discovery.Parent != nil {
		return nil, fmt.Errorf("discovery parent requires a list operation")
	}
	if definition.Discovery.Enrich != nil {
		if err := validateProductAPI(*definition.Discovery.Enrich, providerCatalog, "discovery enrich", true, true, false); err != nil {
			return nil, err
		}
		if definition.Discovery.Enrich.Pagination != nil {
			return nil, fmt.Errorf("discovery enrich does not support pagination")
		}
	}
	if definition.Discovery.Detail != nil {
		if err := requireOperation(providerCatalog, definition.Discovery.Detail.Operation, "detail"); err != nil {
			return nil, err
		}
		capabilities = append(capabilities, asset.CapabilityDetailed)
	}
	if len(definition.Relationships) > 0 {
		capabilities = append(capabilities, asset.CapabilityRelated)
		for _, relationship := range definition.Relationships {
			if _, ok := providerCatalog.ResourceType(relationship.TargetType); !ok {
				return nil, fmt.Errorf("relationship target native type %q is not present in catalog", relationship.TargetType)
			}
		}
	}
	if definition.Governance != nil && len(definition.Governance.Rules) > 0 {
		capabilities = append(capabilities, asset.CapabilityGoverned)
	}
	if len(definition.Actions) > 0 {
		capabilities = append(capabilities, asset.CapabilityActionable)
	}
	var hookCapabilities HookCapabilitySet
	if definition.Extensions.Hook != "" {
		registered, ok := hooks[definition.Extensions.Hook]
		if !ok {
			return nil, fmt.Errorf("hook %q is not registered", definition.Extensions.Hook)
		}
		hookCapabilities = registered
	}
	for name, action := range definition.Actions {
		operation, ok := providerCatalog.Operation(action.Operation)
		if !ok {
			return nil, fmt.Errorf("action %q references unknown operation %q", name, action.Operation)
		}
		if name == "delete" && !operation.Destructive {
			return nil, fmt.Errorf("delete action operation %q is not in the destructive allowlist", action.Operation)
		}
		if definition.Extensions.Hook == "" {
			if err := validateOperationCall(operation, fmt.Sprintf("action %q", name)); err != nil {
				return nil, err
			}
		}
		if strings.TrimSpace(action.Idempotency) == "" {
			return nil, fmt.Errorf("action %q requires an idempotency strategy", name)
		}
		for _, parameter := range action.RequiredParams {
			configured, ok := action.Parameters[parameter]
			if !ok {
				return nil, fmt.Errorf(
					"action %q required parameter %q is not configurable",
					name,
					parameter,
				)
			}
			if expression, ok := configured.(string); ok &&
				(strings.HasPrefix(expression, "resource.") ||
					strings.HasPrefix(expression, "scope.")) {
				return nil, fmt.Errorf(
					"action %q required parameter %q is managed by the resource spec",
					name,
					parameter,
				)
			}
		}
		if definition.Extensions.Hook == "" {
			switch action.Idempotency {
			case "readback":
			case "provider_token":
				if operation.Call.IdempotencyParameter == "" {
					return nil, fmt.Errorf(
						"action %q uses provider_token but operation %q has no idempotency parameter",
						name,
						action.Operation,
					)
				}
			default:
				return nil, fmt.Errorf(
					"action %q has unsupported spec idempotency strategy %q",
					name,
					action.Idempotency,
				)
			}
		}
		if action.Waiter == "" && action.Readback == "" && action.Read == nil {
			return nil, fmt.Errorf("action %q requires a waiter or readback", name)
		}
		if action.Read != nil {
			if err := validateProductAPI(*action.Read, providerCatalog, fmt.Sprintf("action %q readback", name), true, true, false); err != nil {
				return nil, err
			}
		}
		if protection := action.DeletionProtection; protection != nil {
			if protection.Read == nil && action.Read == nil {
				return nil, fmt.Errorf(
					"action %q deletion protection requires a read or action readback",
					name,
				)
			}
			if protection.Read != nil {
				if err := validateProductAPI(
					*protection.Read,
					providerCatalog,
					fmt.Sprintf("action %q deletion protection read", name),
					true,
					true,
					false,
				); err != nil {
					return nil, err
				}
			}
			disableOperation, ok := providerCatalog.Operation(protection.Disable.Operation)
			if !ok {
				return nil, fmt.Errorf(
					"action %q deletion protection references unknown operation %q",
					name,
					protection.Disable.Operation,
				)
			}
			if definition.Extensions.Hook == "" {
				if err := validateOperationCall(
					disableOperation,
					fmt.Sprintf("action %q deletion protection disable", name),
				); err != nil {
					return nil, err
				}
			}
			if err := validateParameterExpressions(
				protection.Disable.Parameters,
				true,
				true,
				false,
				fmt.Sprintf("action %q deletion protection disable", name),
			); err != nil {
				return nil, err
			}
		}
		if len(action.Preconditions) > 0 && action.Read == nil {
			return nil, fmt.Errorf("action %q preconditions require a product API readback", name)
		}
		for index, precondition := range action.Preconditions {
			if strings.TrimSpace(precondition.Path) == "" ||
				len(precondition.AllowedValues) == 0 ||
				strings.TrimSpace(precondition.Reason) == "" {
				return nil, fmt.Errorf(
					"action %q precondition %d requires path, allowedValues, and reason",
					name,
					index,
				)
			}
		}
		if action.Waiter == "absent" && action.Read == nil {
			return nil, fmt.Errorf("action %q absent waiter requires a product API readback", name)
		}
		if action.Waiter == "terminal" {
			if action.Read == nil || len(action.TerminalStates) == 0 {
				return nil, fmt.Errorf("action %q terminal waiter requires readback and terminalStates", name)
			}
		} else if len(action.TerminalStates) > 0 || len(action.FailureStates) > 0 {
			return nil, fmt.Errorf("action %q terminalStates and failureStates require the terminal waiter", name)
		}
		if action.Waiter != "" &&
			action.Waiter != "absent" &&
			action.Waiter != "terminal" &&
			!hookCapabilities.Has(HookWaiter) {
			return nil, fmt.Errorf("action %q waiter %q requires a waiter-capable hook", name, action.Waiter)
		}
		if action.Readback != "" && !hookCapabilities.Has(HookReadback) {
			return nil, fmt.Errorf("action %q readback %q requires a readback-capable hook", name, action.Readback)
		}
		if definition.Extensions.Hook == "" && action.Read == nil {
			return nil, fmt.Errorf("action %q without a hook requires a product API readback", name)
		}
		if err := validateParameterExpressions(action.Parameters, true, true, false, fmt.Sprintf("action %q", name)); err != nil {
			return nil, err
		}
	}
	return capabilities, nil
}

func validateConsoleLinkTemplate(template string, fields map[string]FieldSpec) error {
	if template == "" {
		return nil
	}
	if strings.TrimSpace(template) != template {
		return fmt.Errorf("presentation console link template cannot contain surrounding whitespace")
	}
	for _, match := range consoleLinkPlaceholderPattern.FindAllStringSubmatch(template, -1) {
		switch match[1] {
		case "nativeId", "regionId", "parentId":
		default:
			if _, exists := fields[match[1]]; !exists {
				return fmt.Errorf(
					"presentation console link template uses unsupported placeholder %q",
					match[1],
				)
			}
		}
		switch match[2] {
		case "":
		case "suffix":
			if match[1] != "nativeId" || match[3] == "" {
				return fmt.Errorf(
					"presentation console link template suffix filter requires nativeId and a separator",
				)
			}
		case "replacePrefix":
			if match[1] != "nativeId" {
				return fmt.Errorf(
					"presentation console link template replacePrefix filter only supports nativeId",
				)
			}
			prefixes := strings.Split(match[3], ",")
			if len(prefixes) != 2 || prefixes[0] == "" || prefixes[1] == "" {
				return fmt.Errorf(
					"presentation console link template replacePrefix filter requires old and new prefixes",
				)
			}
		default:
			return fmt.Errorf(
				"presentation console link template uses unsupported filter %q",
				match[2],
			)
		}
	}
	resolved := consoleLinkPlaceholderPattern.ReplaceAllString(template, "value")
	if strings.ContainsAny(resolved, "{}") {
		return fmt.Errorf("presentation console link template contains a malformed placeholder")
	}
	parsed, err := url.Parse(resolved)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("presentation console link template must be an absolute HTTPS URL")
	}
	return nil
}

func validateDiscoveryParent(
	parent ParentDiscoverySpec,
	providerCatalog catalog.Catalog,
) error {
	source := strings.TrimSpace(parent.Source)
	if source == "" {
		if strings.TrimSpace(parent.NativeType) != "" {
			return fmt.Errorf("product-api discovery parent cannot define a native type")
		}
		return validateProductAPI(
			parent.ProductAPISpec,
			providerCatalog,
			"discovery parent",
			false,
			false,
			false,
		)
	}
	nativeType := strings.TrimSpace(parent.NativeType)
	if nativeType == "" {
		return fmt.Errorf("discovery parent source %q requires a native type", source)
	}
	if _, exists := providerCatalog.ResourceType(nativeType); !exists {
		return fmt.Errorf(
			"discovery parent source %q references unknown native type %q",
			source,
			nativeType,
		)
	}
	api := parent.ProductAPISpec
	if strings.TrimSpace(api.Operation) != "" ||
		len(api.Parameters) > 0 ||
		strings.TrimSpace(api.ItemsPath) != "" ||
		strings.TrimSpace(api.IdentityPath) != "" ||
		strings.TrimSpace(api.StatePath) != "" ||
		api.MaxBatchSize != 0 ||
		len(api.SupportedRegions) > 0 ||
		api.Pagination != nil {
		return fmt.Errorf(
			"discovery parent source %q cannot define product API fields",
			source,
		)
	}
	return nil
}

func validateProductAPI(
	api ProductAPISpec,
	providerCatalog catalog.Catalog,
	usage string,
	resourceExpressions bool,
	normalizedExpressions bool,
	parentExpressions bool,
) error {
	operation, ok := providerCatalog.Operation(api.Operation)
	if !ok {
		return fmt.Errorf("%s references unknown operation %q", usage, api.Operation)
	}
	if operation.Call == nil {
		return fmt.Errorf("%s operation %q has no direct product API call metadata", usage, api.Operation)
	}
	if err := validateOperationCall(operation, usage); err != nil {
		return err
	}
	if strings.TrimSpace(api.ItemsPath) == "" || strings.TrimSpace(api.IdentityPath) == "" {
		return fmt.Errorf("%s requires items and identity paths", usage)
	}
	if err := validateParameterExpressions(
		api.Parameters,
		resourceExpressions,
		normalizedExpressions,
		parentExpressions,
		usage,
	); err != nil {
		return err
	}
	seenRegions := make(map[string]struct{}, len(api.SupportedRegions))
	for _, region := range api.SupportedRegions {
		trimmed := strings.TrimSpace(region)
		if trimmed == "" || trimmed != region {
			return fmt.Errorf("%s has invalid supported region %q", usage, region)
		}
		if _, exists := seenRegions[region]; exists {
			return fmt.Errorf("%s has duplicate supported region %q", usage, region)
		}
		seenRegions[region] = struct{}{}
	}
	if api.Pagination == nil {
		return nil
	}
	switch api.Pagination.Type {
	case "token":
		if api.Pagination.TokenParameter == "" || api.Pagination.TokenPath == "" {
			return fmt.Errorf("%s token pagination requires tokenParameter and tokenPath", usage)
		}
	case "page-number":
		if api.Pagination.PageParameter == "" ||
			api.Pagination.PageSizeParameter == "" ||
			api.Pagination.TotalPath == "" {
			return fmt.Errorf("%s page-number pagination requires pageParameter, pageSizeParameter, and totalPath", usage)
		}
	case "offset":
		if api.Pagination.OffsetParameter == "" ||
			api.Pagination.PageSizeParameter == "" ||
			api.Pagination.TotalPath == "" {
			return fmt.Errorf("%s offset pagination requires offsetParameter, pageSizeParameter, and totalPath", usage)
		}
	default:
		return fmt.Errorf("%s has unsupported pagination type %q", usage, api.Pagination.Type)
	}
	return nil
}

func validateOperationCall(operation catalog.Operation, usage string) error {
	call := operation.Call
	if call == nil {
		return fmt.Errorf("%s operation %q has no direct product API call metadata", usage, operation.Name)
	}
	if strings.TrimSpace(call.Product) == "" ||
		strings.TrimSpace(call.Version) == "" ||
		strings.TrimSpace(call.Method) == "" ||
		strings.TrimSpace(call.Path) == "" ||
		strings.TrimSpace(call.Endpoint) == "" {
		return fmt.Errorf("%s operation %q has incomplete direct product API call metadata", usage, operation.Name)
	}
	if call.Style != "RPC" && call.Style != "ROA" {
		return fmt.Errorf("%s operation %q has unsupported API style %q", usage, operation.Name, call.Style)
	}
	if call.ParameterPosition != "" &&
		call.ParameterPosition != "query" &&
		call.ParameterPosition != "body" &&
		call.ParameterPosition != "header" {
		return fmt.Errorf(
			"%s operation %q has unsupported parameter position %q",
			usage,
			operation.Name,
			call.ParameterPosition,
		)
	}
	for region, endpoint := range call.EndpointOverrides {
		if strings.TrimSpace(region) == "" ||
			strings.TrimSpace(endpoint) == "" ||
			strings.Contains(endpoint, "{") {
			return fmt.Errorf(
				"%s operation %q has an invalid endpoint override for region %q",
				usage,
				operation.Name,
				region,
			)
		}
	}
	for site, endpoint := range call.SiteEndpoints {
		if (site != asset.ConnectionSiteCN && site != asset.ConnectionSiteINTL) ||
			strings.TrimSpace(endpoint) == "" ||
			strings.Contains(endpoint, "{") {
			return fmt.Errorf(
				"%s operation %q has an invalid endpoint for site %q",
				usage,
				operation.Name,
				site,
			)
		}
	}
	endpointParameters := map[string]struct{}{}
	if len(call.EndpointParameters) > 0 &&
		(len(call.EndpointOverrides) > 0 || len(call.SiteEndpoints) > 0) {
		return fmt.Errorf(
			"%s operation %q cannot combine endpoint parameters with endpoint overrides",
			usage,
			operation.Name,
		)
	}
	for _, parameter := range call.EndpointParameters {
		parameter = strings.TrimSpace(parameter)
		if parameter == "" || !strings.Contains(call.Endpoint, "{"+parameter+"}") {
			return fmt.Errorf(
				"%s operation %q has invalid endpoint parameter %q",
				usage,
				operation.Name,
				parameter,
			)
		}
		if _, duplicate := endpointParameters[parameter]; duplicate {
			return fmt.Errorf(
				"%s operation %q has duplicate endpoint parameter %q",
				usage,
				operation.Name,
				parameter,
			)
		}
		endpointParameters[parameter] = struct{}{}
	}
	hostParameters := map[string]struct{}{}
	for _, parameter := range call.HostParameters {
		parameter = strings.TrimSpace(parameter)
		if parameter == "" {
			return fmt.Errorf(
				"%s operation %q has an empty host parameter",
				usage,
				operation.Name,
			)
		}
		if _, duplicate := hostParameters[parameter]; duplicate {
			return fmt.Errorf(
				"%s operation %q has duplicate host parameter %q",
				usage,
				operation.Name,
				parameter,
			)
		}
		if _, duplicate := endpointParameters[parameter]; duplicate {
			return fmt.Errorf(
				"%s operation %q reuses endpoint parameter %q as a host parameter",
				usage,
				operation.Name,
				parameter,
			)
		}
		hostParameters[parameter] = struct{}{}
	}
	return nil
}

func validateParameterExpressions(
	parameters map[string]any,
	resourceExpressions bool,
	normalizedExpressions bool,
	parentExpressions bool,
	usage string,
) error {
	for parameter, value := range parameters {
		text, ok := value.(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(text, "scope.") &&
			!strings.HasPrefix(text, "request.") &&
			!strings.HasPrefix(text, "resource.") &&
			!strings.HasPrefix(text, "resources.") &&
			!strings.HasPrefix(text, "parent.") {
			continue
		}
		switch text {
		case "scope.location":
		case "parent.nativeId":
			if !parentExpressions {
				return fmt.Errorf("%s parameter %q cannot reference %q", usage, parameter, text)
			}
		case "resource.nativeId", "resource.nativeIdsJson",
			"resources.nativeId", "resources.nativeIds", "resources.nativeIdsJson", "resources.count":
			if !resourceExpressions {
				return fmt.Errorf("%s parameter %q cannot reference %q", usage, parameter, text)
			}
		default:
			if normalizedExpressions &&
				strings.HasPrefix(text, "resource.normalized.") &&
				resourceNormalizedPathPattern.MatchString(
					strings.TrimPrefix(text, "resource.normalized."),
				) {
				continue
			}
			return fmt.Errorf("%s parameter %q has unsupported expression %q", usage, parameter, text)
		}
	}
	return nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneLocalizedFields(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]map[string]string, len(values))
	for field, labels := range values {
		cloned[field] = maps.Clone(labels)
	}
	return cloned
}

func requireOperation(providerCatalog catalog.Catalog, name, usage string) error {
	if _, ok := providerCatalog.Operation(name); !ok {
		return fmt.Errorf("%s references unknown operation %q", usage, name)
	}
	return nil
}

func containsScope(scopes []asset.ScopeKind, want asset.ScopeKind) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compiledFieldDisplayNames(definition ResourceKindSpec) map[string]map[string]string {
	result := cloneLocalizedFields(definition.Presentation.FieldDisplayNames)
	for field, property := range definition.Fields {
		if len(property.DisplayNames) == 0 {
			continue
		}
		if result == nil {
			result = make(map[string]map[string]string)
		}
		if result[field] == nil {
			result[field] = make(map[string]string)
		}
		for locale, displayName := range property.DisplayNames {
			result[field][locale] = displayName
		}
	}
	return result
}

func compiledProperties(definition ResourceKindSpec) []asset.ResourceProperty {
	fields := sortedKeys(definition.Fields)
	properties := make([]asset.ResourceProperty, 0, len(fields))
	for _, field := range fields {
		fieldDefinition := definition.Fields[field]
		propertyType := fieldDefinition.Type
		if propertyType == "" {
			propertyType = PropertyAny
		}
		displayNames := cloneStringMap(fieldDefinition.DisplayNames)
		if len(displayNames) == 0 {
			displayNames = cloneStringMap(definition.Presentation.FieldDisplayNames[field])
		}
		operators := append([]string(nil), fieldDefinition.Operators...)
		if len(operators) == 0 {
			operators = defaultPropertyOperators(propertyType)
		}
		properties = append(properties, asset.ResourceProperty{
			Path:         field,
			Type:         string(propertyType),
			DisplayNames: displayNames,
			Enum:         append([]any(nil), fieldDefinition.Enum...),
			Operators:    operators,
		})
	}
	return properties
}

func defaultPropertyOperators(propertyType PropertyType) []string {
	switch propertyType {
	case PropertyNumber, PropertyInteger, PropertyDatetime:
		return []string{"=", "!=", ">", ">=", "<", "<=", "in", "not_in", "is_null", "is_not_null"}
	case PropertyBoolean:
		return []string{"=", "!=", "is_null", "is_not_null"}
	case PropertyObject, PropertyArray:
		return []string{"contains", "is_null", "is_not_null"}
	default:
		return []string{"=", "!=", "in", "not_in", "contains", "is_null", "is_not_null"}
	}
}

func normalizeYAML(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("YAML object key %v is not a string", key)
			}
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}
