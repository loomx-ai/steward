package resourcequery

import (
	"fmt"
	"strings"
)

func (e *Expression) SQL(driver string) (string, []any, error) {
	if e == nil || e.root == nil {
		return "", nil, nil
	}
	compiler := sqlCompiler{driver: driver}
	query, err := compiler.node(e.root)
	return query, compiler.arguments, err
}

type sqlCompiler struct {
	driver    string
	arguments []any
}

func (c *sqlCompiler) node(current node) (string, error) {
	switch typed := current.(type) {
	case logicalNode:
		left, err := c.node(typed.left)
		if err != nil {
			return "", err
		}
		right, err := c.node(typed.right)
		if err != nil {
			return "", err
		}
		return "(" + left + " " + strings.ToUpper(typed.operator) + " " + right + ")", nil
	case notNode:
		child, err := c.node(typed.child)
		if err != nil {
			return "", err
		}
		return "(NOT " + child + ")", nil
	case predicateNode:
		return c.predicate(typed)
	default:
		return "", fmt.Errorf("unsupported resource query node")
	}
}

func (c *sqlCompiler) predicate(predicate predicateNode) (string, error) {
	field, pathArguments, err := c.field(predicate.field)
	if err != nil {
		return "", err
	}
	c.arguments = append(c.arguments, pathArguments...)
	switch predicate.operator {
	case OperatorIsNull:
		return "(" + field + " IS NULL)", nil
	case OperatorIsNotNull:
		return "(" + field + " IS NOT NULL)", nil
	case OperatorContains:
		c.arguments = append(c.arguments, "%"+escapeLike(strings.ToLower(predicate.values[0].String))+"%")
		return "(LOWER(" + c.text(field) + ") LIKE ? ESCAPE '\\')", nil
	case OperatorIn, OperatorNotIn:
		placeholders := make([]string, 0, len(predicate.values))
		for _, value := range predicate.values {
			placeholders = append(placeholders, "?")
			c.arguments = append(c.arguments, sqlValue(value))
		}
		operator := "IN"
		if predicate.operator == OperatorNotIn {
			operator = "NOT IN"
		}
		return "(" + c.comparable(field, predicate.values[0]) + " " + operator + " (" + strings.Join(placeholders, ", ") + "))", nil
	default:
		c.arguments = append(c.arguments, sqlValue(predicate.values[0]))
		return "(" + c.comparable(field, predicate.values[0]) + " " + string(predicate.operator) + " ?)", nil
	}
}

func (c *sqlCompiler) field(field string) (string, []any, error) {
	switch canonicalField(field) {
	case "type", "resourcetype":
		return "assets.native_type", nil, nil
	case "provider":
		return "assets.provider", nil, nil
	case "dirty":
		return "assets.dirty", nil, nil
	case "id":
		return "assets.id", nil, nil
	case "resourceid":
		return "assets.native_id", nil, nil
	case "resourcekindid":
		return "assets.resource_kind_id", nil, nil
	case "name":
		return c.jsonField([]string{"name"})
	case "state":
		return c.jsonField([]string{"state"})
	case "region", "location":
		return c.jsonField([]string{"location"})
	}
	canonical := canonicalField(field)
	if strings.HasPrefix(canonical, "properties.") {
		return c.jsonField(append([]string{"normalized"}, strings.Split(field[len("properties."):], ".")...))
	}
	if strings.HasPrefix(canonical, "tags.") {
		return c.jsonField(append([]string{"tags"}, strings.Split(field[len("tags."):], ".")...))
	}
	return "", nil, fmt.Errorf("unsupported resource query field %q", field)
}

func (c *sqlCompiler) jsonField(path []string) (string, []any, error) {
	if c.driver == "postgres" {
		placeholders := make([]string, len(path))
		arguments := make([]any, len(path))
		for index, segment := range path {
			placeholders[index] = "?"
			arguments[index] = segment
		}
		return "jsonb_extract_path_text(assets.payload::jsonb, " + strings.Join(placeholders, ", ") + ")", arguments, nil
	}
	return "json_extract(assets.payload, ?)", []any{"$." + strings.Join(path, ".")}, nil
}

func (c *sqlCompiler) comparable(field string, value Value) string {
	switch value.Kind {
	case ValueNumber:
		if c.driver == "postgres" {
			return "CAST(NULLIF(" + c.text(field) + ", '') AS DOUBLE PRECISION)"
		}
		return "CAST(" + field + " AS REAL)"
	case ValueBoolean:
		if c.driver == "postgres" && strings.Contains(field, "jsonb_extract_path_text") {
			return "CAST(" + field + " AS BOOLEAN)"
		}
	}
	return field
}

func (c *sqlCompiler) text(field string) string {
	if c.driver == "postgres" {
		return field
	}
	return "CAST(" + field + " AS TEXT)"
}

func sqlValue(value Value) any { return value.Any() }

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}
