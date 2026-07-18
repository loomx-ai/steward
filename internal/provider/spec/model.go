package spec

import (
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/finding"
	"gopkg.in/yaml.v3"
)

const SchemaIdentifier = "steward.io/resource-kind"

type Metadata struct {
	Provider   asset.Provider `yaml:"provider" json:"provider"`
	NativeType string         `yaml:"nativeType" json:"nativeType"`
	Class      string         `yaml:"class" json:"class,omitempty"`
}

type ScopeSpec struct {
	Kind asset.ScopeKind `yaml:"kind" json:"kind"`
}

type PresentationSpec struct {
	DisplayNames        map[string]string            `yaml:"displayNames,omitempty" json:"displayNames,omitempty"`
	FieldDisplayNames   map[string]map[string]string `yaml:"fieldDisplayNames,omitempty" json:"fieldDisplayNames,omitempty"`
	Icon                string                       `yaml:"icon,omitempty" json:"icon,omitempty"`
	ConsoleLinkTemplate string                       `yaml:"consoleLinkTemplate,omitempty" json:"consoleLinkTemplate,omitempty"`
}

type DetailSpec struct {
	Operation    string `yaml:"operation" json:"operation"`
	ItemsPath    string `yaml:"itemsPath" json:"itemsPath"`
	IdentityPath string `yaml:"identityPath" json:"identityPath"`
}

type PaginationSpec struct {
	Type              string `yaml:"type" json:"type"`
	TokenParameter    string `yaml:"tokenParameter,omitempty" json:"tokenParameter,omitempty"`
	TokenPath         string `yaml:"tokenPath,omitempty" json:"tokenPath,omitempty"`
	PageParameter     string `yaml:"pageParameter,omitempty" json:"pageParameter,omitempty"`
	OffsetParameter   string `yaml:"offsetParameter,omitempty" json:"offsetParameter,omitempty"`
	PageSizeParameter string `yaml:"pageSizeParameter,omitempty" json:"pageSizeParameter,omitempty"`
	TotalPath         string `yaml:"totalPath,omitempty" json:"totalPath,omitempty"`
	MaxPageSize       int    `yaml:"maxPageSize,omitempty" json:"maxPageSize,omitempty"`
}

// ProductAPISpec maps a direct cloud product list/read response into one
// resource kind. Parameters are literals or a small set of compiler-validated
// runtime expressions such as scope.location and resource.nativeId.
type ProductAPISpec struct {
	Operation        string          `yaml:"operation" json:"operation,omitempty"`
	Parameters       map[string]any  `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	ItemsPath        string          `yaml:"itemsPath" json:"itemsPath,omitempty"`
	IdentityPath     string          `yaml:"identityPath" json:"identityPath,omitempty"`
	StatePath        string          `yaml:"statePath,omitempty" json:"statePath,omitempty"`
	MaxBatchSize     int             `yaml:"maxBatchSize,omitempty" json:"maxBatchSize,omitempty"`
	SupportedRegions []string        `yaml:"supportedRegions,omitempty" json:"supportedRegions,omitempty"`
	Pagination       *PaginationSpec `yaml:"pagination,omitempty" json:"pagination,omitempty"`
}

// ParentDiscoverySpec describes how a fanout resource discovers its parents.
// An empty source keeps the existing direct product API behavior. Provider
// runtimes can also define an inventory source and identify the parent by its
// native resource type.
type ParentDiscoverySpec struct {
	Source         string `yaml:"source,omitempty" json:"source,omitempty"`
	NativeType     string `yaml:"nativeType,omitempty" json:"nativeType,omitempty"`
	ProductAPISpec `yaml:",inline"`
}

type DiscoverySpec struct {
	Source string               `yaml:"source" json:"source"`
	Parent *ParentDiscoverySpec `yaml:"parent,omitempty" json:"parent,omitempty"`
	List   *ProductAPISpec      `yaml:"list,omitempty" json:"list,omitempty"`
	Enrich *ProductAPISpec      `yaml:"enrich,omitempty" json:"enrich,omitempty"`
	Detail *DetailSpec          `yaml:"detail,omitempty" json:"detail,omitempty"`
}

type ActionPrecondition struct {
	Path          string   `yaml:"path" json:"path"`
	AllowedValues []string `yaml:"allowedValues" json:"allowedValues"`
	Reason        string   `yaml:"reason" json:"reason"`
}

type ActionInvocationSpec struct {
	Operation  string         `yaml:"operation" json:"operation"`
	Parameters map[string]any `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

// ActionDeletionProtectionSpec describes the live protection check and the
// mutation that must run before a destructive action. Read defaults to the
// action readback when both use the same product response.
type ActionDeletionProtectionSpec struct {
	Read          *ProductAPISpec      `yaml:"read,omitempty" json:"read,omitempty"`
	Path          string               `yaml:"path" json:"path"`
	EnabledValues []string             `yaml:"enabledValues" json:"enabledValues"`
	Disable       ActionInvocationSpec `yaml:"disable" json:"disable"`
}

type RelationshipSpec struct {
	Type         string `yaml:"type" json:"type"`
	TargetType   string `yaml:"targetType" json:"targetType"`
	TargetIDPath string `yaml:"targetIdPath" json:"targetIdPath"`
}

type ActionSpec struct {
	Operation                   string                        `yaml:"operation" json:"operation"`
	Parameters                  map[string]any                `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	RequiredParams              []string                      `yaml:"requiredParameters,omitempty" json:"requiredParameters,omitempty"`
	Preconditions               []ActionPrecondition          `yaml:"preconditions,omitempty" json:"preconditions,omitempty"`
	Idempotency                 string                        `yaml:"idempotency" json:"idempotency"`
	Waiter                      string                        `yaml:"waiter,omitempty" json:"waiter,omitempty"`
	PollIntervalSeconds         int                           `yaml:"pollIntervalSeconds,omitempty" json:"pollIntervalSeconds,omitempty"`
	DeletionCheckTimeoutSeconds int                           `yaml:"deletionCheckTimeoutSeconds,omitempty" json:"deletionCheckTimeoutSeconds,omitempty"`
	TerminalStates              []string                      `yaml:"terminalStates,omitempty" json:"terminalStates,omitempty"`
	FailureStates               []string                      `yaml:"failureStates,omitempty" json:"failureStates,omitempty"`
	Readback                    string                        `yaml:"readback,omitempty" json:"readback,omitempty"`
	Read                        *ProductAPISpec               `yaml:"read,omitempty" json:"read,omitempty"`
	DeletionProtection          *ActionDeletionProtectionSpec `yaml:"deletionProtection,omitempty" json:"deletionProtection,omitempty"`
}

type GovernanceRule struct {
	ID          string           `yaml:"id" json:"id"`
	Title       string           `yaml:"title" json:"title"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	Severity    finding.Severity `yaml:"severity" json:"severity"`
	Condition   string           `yaml:"condition" json:"condition"`
}

type GovernanceSpec struct {
	Rules []GovernanceRule `yaml:"rules" json:"rules"`
}

type HookCapability string

const (
	HookDiscovery    HookCapability = "discovery"
	HookDetail       HookCapability = "detail"
	HookRelationship HookCapability = "relationship"
	HookPreflight    HookCapability = "preflight"
	HookAction       HookCapability = "action"
	HookWaiter       HookCapability = "waiter"
	HookReadback     HookCapability = "readback"
	HookLifecycle    HookCapability = "lifecycle"
)

type Extensions struct {
	Hook string `yaml:"hook,omitempty" json:"hook,omitempty"`
}

type PropertyType string

const (
	PropertyAny      PropertyType = "any"
	PropertyString   PropertyType = "string"
	PropertyNumber   PropertyType = "number"
	PropertyInteger  PropertyType = "integer"
	PropertyBoolean  PropertyType = "boolean"
	PropertyDatetime PropertyType = "datetime"
	PropertyObject   PropertyType = "object"
	PropertyArray    PropertyType = "array"
)

// FieldSpec maps a provider response path to a stable normalized property and
// describes how the property can be presented and queried. Scalar YAML values
// remain supported as shorthand for {path: <value>}.
type FieldSpec struct {
	Path         string            `yaml:"path" json:"path"`
	Type         PropertyType      `yaml:"type,omitempty" json:"type,omitempty"`
	DisplayNames map[string]string `yaml:"displayNames,omitempty" json:"displayNames,omitempty"`
	Enum         []any             `yaml:"enum,omitempty" json:"enum,omitempty"`
	Operators    []string          `yaml:"operators,omitempty" json:"operators,omitempty"`
}

func (f *FieldSpec) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return fmt.Errorf("field spec is required")
	}
	if node.Kind == yaml.ScalarNode {
		var path string
		if err := node.Decode(&path); err != nil {
			return err
		}
		f.Path = strings.TrimSpace(path)
		f.Type = PropertyAny
		return nil
	}
	type plain FieldSpec
	var value plain
	if err := node.Decode(&value); err != nil {
		return err
	}
	*f = FieldSpec(value)
	f.Path = strings.TrimSpace(f.Path)
	if f.Type == "" {
		f.Type = PropertyAny
	}
	return nil
}

type ResourceKindSpec struct {
	Schema        string                `yaml:"schema" json:"schema"`
	Kind          string                `yaml:"kind" json:"kind"`
	Metadata      Metadata              `yaml:"metadata" json:"metadata"`
	Presentation  PresentationSpec      `yaml:"presentation,omitempty" json:"presentation,omitempty"`
	Scope         ScopeSpec             `yaml:"scope" json:"scope"`
	Discovery     DiscoverySpec         `yaml:"discovery" json:"discovery"`
	Fields        map[string]FieldSpec  `yaml:"fields,omitempty" json:"fields,omitempty"`
	Relationships []RelationshipSpec    `yaml:"relationships,omitempty" json:"relationships,omitempty"`
	Governance    *GovernanceSpec       `yaml:"governance,omitempty" json:"governance,omitempty"`
	Actions       map[string]ActionSpec `yaml:"actions,omitempty" json:"actions,omitempty"`
	Extensions    Extensions            `yaml:"extensions,omitempty" json:"extensions,omitempty"`
}

type CompiledSpec struct {
	Definition      ResourceKindSpec   `json:"definition"`
	ResourceKind    asset.ResourceKind `json:"resource_kind"`
	Rules           []CompiledRule     `json:"rules,omitempty"`
	CatalogChecksum string             `json:"catalog_checksum"`
	Revision        string             `json:"revision"`
	Hash            string             `json:"hash"`
}

type RuleOperator string

const (
	RuleEqual        RuleOperator = "=="
	RuleNotEqual     RuleOperator = "!="
	RuleGreaterThan  RuleOperator = ">"
	RuleGreaterEqual RuleOperator = ">="
	RuleLessThan     RuleOperator = "<"
	RuleLessEqual    RuleOperator = "<="
)

type CompiledRule struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	Severity    finding.Severity `json:"severity"`
	FieldPath   []string         `json:"field_path"`
	Operator    RuleOperator     `json:"operator"`
	Expected    any              `json:"expected"`
}

type Bundle struct {
	Provider asset.Provider `json:"provider"`
	Specs    []CompiledSpec `json:"specs"`
	Revision string         `json:"revision"`
	Hash     string         `json:"hash"`
}

type HookCapabilitySet []HookCapability

func (set HookCapabilitySet) Has(want HookCapability) bool {
	for _, capability := range set {
		if capability == want {
			return true
		}
	}
	return false
}

type HookRegistry map[string]HookCapabilitySet
