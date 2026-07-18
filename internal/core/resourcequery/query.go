package resourcequery

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	MaxLength     = 4096
	MaxPredicates = 64
	MaxDepth      = 16
)

type Operator string

const (
	OperatorEqual     Operator = "="
	OperatorNotEqual  Operator = "!="
	OperatorGreater   Operator = ">"
	OperatorGreaterEq Operator = ">="
	OperatorLess      Operator = "<"
	OperatorLessEq    Operator = "<="
	OperatorIn        Operator = "in"
	OperatorNotIn     Operator = "not_in"
	OperatorContains  Operator = "contains"
	OperatorIsNull    Operator = "is_null"
	OperatorIsNotNull Operator = "is_not_null"
)

type ValueKind int

const (
	ValueString ValueKind = iota
	ValueNumber
	ValueBoolean
)

type Value struct {
	Kind    ValueKind
	String  string
	Number  float64
	Boolean bool
}

func (v Value) Any() any {
	switch v.Kind {
	case ValueNumber:
		return v.Number
	case ValueBoolean:
		return v.Boolean
	default:
		return v.String
	}
}

type node interface {
	match(asset.Asset) bool
	predicateCount() int
	depth() int
}

type logicalNode struct {
	operator string
	left     node
	right    node
}

func (n logicalNode) match(value asset.Asset) bool {
	if n.operator == "and" {
		return n.left.match(value) && n.right.match(value)
	}
	return n.left.match(value) || n.right.match(value)
}

func (n logicalNode) predicateCount() int { return n.left.predicateCount() + n.right.predicateCount() }
func (n logicalNode) depth() int          { return 1 + max(n.left.depth(), n.right.depth()) }

type notNode struct{ child node }

func (n notNode) match(value asset.Asset) bool { return !n.child.match(value) }
func (n notNode) predicateCount() int          { return n.child.predicateCount() }
func (n notNode) depth() int                   { return 1 + n.child.depth() }

type predicateNode struct {
	field    string
	operator Operator
	values   []Value
	position int
}

func (n predicateNode) predicateCount() int { return 1 }
func (n predicateNode) depth() int          { return 1 }

func (n predicateNode) match(value asset.Asset) bool {
	actual, exists := assetField(value, n.field)
	switch n.operator {
	case OperatorIsNull:
		return !exists || actual == nil
	case OperatorIsNotNull:
		return exists && actual != nil
	}
	if !exists || actual == nil {
		return false
	}
	if n.operator == OperatorContains {
		return strings.Contains(strings.ToLower(fmt.Sprint(actual)), strings.ToLower(n.values[0].String))
	}
	if n.operator == OperatorIn || n.operator == OperatorNotIn {
		matched := false
		for _, expected := range n.values {
			if comparison, comparable := compare(actual, expected); comparable && comparison == 0 {
				matched = true
				break
			}
		}
		if n.operator == OperatorNotIn {
			return !matched
		}
		return matched
	}
	comparison, comparable := compare(actual, n.values[0])
	if !comparable {
		return false
	}
	switch n.operator {
	case OperatorEqual:
		return comparison == 0
	case OperatorNotEqual:
		return comparison != 0
	case OperatorGreater:
		return comparison > 0
	case OperatorGreaterEq:
		return comparison >= 0
	case OperatorLess:
		return comparison < 0
	case OperatorLessEq:
		return comparison <= 0
	default:
		return false
	}
}

type Expression struct {
	source string
	root   node
}

func (e *Expression) Empty() bool { return e == nil || e.root == nil }

func (e *Expression) String() string {
	if e == nil {
		return ""
	}
	return e.source
}

func (e *Expression) Match(value asset.Asset) bool {
	return e == nil || e.root == nil || e.root.match(value)
}

func (e *Expression) Validate(kinds []asset.ResourceKind) error {
	if e == nil || e.root == nil {
		return nil
	}
	kindByType := make(map[string]asset.ResourceKind, len(kinds))
	for _, kind := range kinds {
		kindByType[kind.NativeType] = kind
	}
	constrainedTypes, constrained := conjunctiveTypes(e.root)
	if constrained {
		for nativeType := range constrainedTypes {
			if _, exists := kindByType[nativeType]; !exists {
				return &ValidationError{Message: fmt.Sprintf("resource type %q is not supported", nativeType)}
			}
		}
	}
	allowedProperties := make(map[string]map[Operator]struct{})
	propertyTypes := make(map[string]map[string]struct{})
	for _, kind := range kinds {
		if constrained {
			if _, selected := constrainedTypes[kind.NativeType]; !selected {
				continue
			}
		}
		for _, property := range kind.Properties {
			if allowedProperties[property.Path] == nil {
				allowedProperties[property.Path] = make(map[Operator]struct{})
			}
			if propertyTypes[property.Path] == nil {
				propertyTypes[property.Path] = make(map[string]struct{})
			}
			propertyTypes[property.Path][strings.ToLower(strings.TrimSpace(property.Type))] = struct{}{}
			operators := property.Operators
			if len(operators) == 0 {
				operators = defaultOperatorsForType(property.Type)
			}
			for _, operator := range operators {
				allowedProperties[property.Path][normalizeOperator(operator)] = struct{}{}
			}
		}
	}
	return validateNode(e.root, kindByType, allowedProperties, propertyTypes)
}

type ValidationError struct {
	Position int
	Message  string
}

func (e *ValidationError) Error() string { return e.Message }

func validateNode(
	current node,
	kinds map[string]asset.ResourceKind,
	properties map[string]map[Operator]struct{},
	propertyTypes map[string]map[string]struct{},
) error {
	switch typed := current.(type) {
	case logicalNode:
		if err := validateNode(typed.left, kinds, properties, propertyTypes); err != nil {
			return err
		}
		return validateNode(typed.right, kinds, properties, propertyTypes)
	case notNode:
		return validateNode(typed.child, kinds, properties, propertyTypes)
	case predicateNode:
		canonical := canonicalField(typed.field)
		if canonical == "type" || canonical == "resourcetype" {
			if !operatorAllowed(typed.operator, OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn) {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("operator %q is not supported for type", typed.operator)}
			}
			for _, value := range typed.values {
				if value.Kind != ValueString {
					return &ValidationError{Position: typed.position, Message: "type requires a quoted resource type"}
				}
				if _, exists := kinds[value.String]; !exists {
					return &ValidationError{Position: typed.position, Message: fmt.Sprintf("resource type %q is not supported", value.String)}
				}
			}
			return nil
		}
		if strings.HasPrefix(canonical, "properties.") {
			property := typed.field[len("properties."):]
			operators, exists := properties[property]
			if !exists {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("property %q is not defined by the selected resource type", property)}
			}
			if _, allowed := operators[typed.operator]; !allowed {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("operator %q is not supported for property %q", typed.operator, property)}
			}
			if !valuesMatchAnyPropertyType(typed.values, propertyTypes[property]) {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("value type does not match property %q", property)}
			}
			return nil
		}
		if strings.HasPrefix(canonical, "tags.") {
			if !operatorAllowed(typed.operator, OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn, OperatorContains, OperatorIsNull, OperatorIsNotNull) {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("operator %q is not supported for tags", typed.operator)}
			}
			return nil
		}
		if isBuiltinField(canonical) {
			allowed := []Operator{OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn, OperatorContains, OperatorIsNull, OperatorIsNotNull}
			if canonical == "dirty" {
				allowed = []Operator{OperatorEqual, OperatorNotEqual}
			}
			if !operatorAllowed(typed.operator, allowed...) {
				return &ValidationError{Position: typed.position, Message: fmt.Sprintf("operator %q is not supported for field %q", typed.operator, typed.field)}
			}
			if canonical == "dirty" && len(typed.values) > 0 && typed.values[0].Kind != ValueBoolean {
				return &ValidationError{Position: typed.position, Message: "dirty requires a boolean value"}
			}
			return nil
		}
		return &ValidationError{Position: typed.position, Message: fmt.Sprintf("field %q is not supported", typed.field)}
	default:
		return errors.New("unsupported resource query node")
	}
}

func normalizeOperator(value string) Operator {
	return Operator(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), " ", "_"))
}

func operatorAllowed(value Operator, allowed ...Operator) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func defaultOperatorsForType(propertyType string) []string {
	switch strings.ToLower(strings.TrimSpace(propertyType)) {
	case "number", "integer", "datetime":
		return []string{"=", "!=", ">", ">=", "<", "<=", "in", "not_in", "is_null", "is_not_null"}
	case "boolean":
		return []string{"=", "!=", "is_null", "is_not_null"}
	case "object", "array":
		return []string{"contains", "is_null", "is_not_null"}
	default:
		return []string{"=", "!=", "in", "not_in", "contains", "is_null", "is_not_null"}
	}
}

func valuesMatchAnyPropertyType(values []Value, types map[string]struct{}) bool {
	if len(values) == 0 || len(types) == 0 {
		return true
	}
	for propertyType := range types {
		if propertyType == "" || propertyType == "any" {
			return true
		}
		matches := true
		for _, value := range values {
			switch propertyType {
			case "number", "integer":
				matches = matches && value.Kind == ValueNumber
			case "boolean":
				matches = matches && value.Kind == ValueBoolean
			default:
				matches = matches && value.Kind == ValueString
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func conjunctiveTypes(current node) (map[string]struct{}, bool) {
	switch typed := current.(type) {
	case logicalNode:
		if typed.operator != "and" {
			return nil, false
		}
		left, leftOK := conjunctiveTypes(typed.left)
		right, rightOK := conjunctiveTypes(typed.right)
		if !leftOK && !rightOK {
			return nil, false
		}
		result := make(map[string]struct{})
		for value := range left {
			result[value] = struct{}{}
		}
		for value := range right {
			result[value] = struct{}{}
		}
		return result, true
	case predicateNode:
		canonical := canonicalField(typed.field)
		if (canonical != "type" && canonical != "resourcetype") || (typed.operator != OperatorEqual && typed.operator != OperatorIn) {
			return nil, false
		}
		result := make(map[string]struct{}, len(typed.values))
		for _, value := range typed.values {
			if value.Kind == ValueString {
				result[value.String] = struct{}{}
			}
		}
		return result, len(result) > 0
	default:
		return nil, false
	}
}

func canonicalField(field string) string { return strings.ToLower(strings.TrimSpace(field)) }

func isBuiltinField(field string) bool {
	switch field {
	case "type", "resourcetype", "provider", "name", "state", "region", "location", "dirty", "id", "resourceid", "resourcekindid":
		return true
	default:
		return false
	}
}

func assetField(value asset.Asset, field string) (any, bool) {
	switch canonicalField(field) {
	case "type", "resourcetype":
		return value.Identity.NativeType, true
	case "provider":
		return string(value.Identity.Provider), true
	case "name":
		return value.Name, true
	case "state":
		return value.State, true
	case "region", "location":
		return value.Location, true
	case "dirty":
		return value.Dirty, true
	case "id":
		return string(value.ID), true
	case "resourceid":
		return value.Identity.NativeID, true
	case "resourcekindid":
		return string(value.ResourceKindID), true
	}
	canonical := canonicalField(field)
	if strings.HasPrefix(canonical, "properties.") {
		return nestedValue(value.Normalized, strings.Split(field[len("properties."):], "."))
	}
	if strings.HasPrefix(canonical, "tags.") {
		result, exists := value.Tags[field[len("tags."):]]
		return result, exists
	}
	return nil, false
}

func nestedValue(values map[string]any, path []string) (any, bool) {
	var current any = values
	for _, segment := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func compare(actual any, expected Value) (int, bool) {
	if expected.Kind == ValueNumber {
		actualNumber, ok := numberValue(actual)
		if !ok {
			return 0, false
		}
		switch {
		case actualNumber < expected.Number:
			return -1, true
		case actualNumber > expected.Number:
			return 1, true
		default:
			return 0, true
		}
	}
	if expected.Kind == ValueBoolean {
		actualBoolean, ok := booleanValue(actual)
		if !ok || actualBoolean != expected.Boolean {
			return -1, ok
		}
		return 0, true
	}
	actualString := fmt.Sprint(actual)
	return strings.Compare(actualString, expected.String), true
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case jsonNumber:
		result, err := strconv.ParseFloat(string(typed), 64)
		return result, err == nil
	case string:
		result, err := strconv.ParseFloat(typed, 64)
		return result, err == nil
	default:
		value := reflect.ValueOf(value)
		if value.IsValid() && value.Kind() >= reflect.Int && value.Kind() <= reflect.Float64 {
			result, err := strconv.ParseFloat(fmt.Sprint(value.Interface()), 64)
			return result, err == nil
		}
		return 0, false
	}
}

type jsonNumber string

func booleanValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		result, err := strconv.ParseBool(typed)
		return result, err == nil
	default:
		return false, false
	}
}

func SupportedFields(kinds []asset.ResourceKind) []string {
	seen := map[string]struct{}{
		"type": {}, "provider": {}, "name": {}, "state": {}, "region": {}, "dirty": {}, "id": {}, "resourceId": {}, "resourceKindId": {},
	}
	for _, kind := range kinds {
		for _, property := range kind.Properties {
			seen["properties."+property.Path] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for field := range seen {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}
