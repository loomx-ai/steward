package resourcequery

import (
	"fmt"
	"strings"
)

// SQL compiles the expression into a condition on the assets table that holds
// for exactly the assets Match accepts, for the "sqlite" or "postgres" driver.
// Every predicate evaluates to true or false, never NULL, so NOT negates it
// the way it does in memory. Values and JSON paths are passed as arguments.
func (e *Expression) SQL(driver string) (string, []any, error) {
	if e == nil || e.root == nil {
		return "", nil, nil
	}
	compiled, err := sqlCompiler{postgres: driver == "postgres"}.node(e.root)
	return compiled.sql, compiled.args, err
}

// fragment is SQL text with the arguments of its placeholders, in order.
type fragment struct {
	sql  string
	args []any
}

func sql(text string, args ...any) fragment { return fragment{sql: text, args: args} }

// compose replaces each %s of format, in order, with a part.
func compose(format string, parts ...fragment) fragment {
	pieces := strings.Split(format, "%s")
	if len(pieces) != len(parts)+1 {
		panic(fmt.Sprintf("resource query SQL %q takes %d parts, got %d", format, len(pieces)-1, len(parts)))
	}
	result := fragment{sql: pieces[0]}
	for index, part := range parts {
		result.sql += part.sql + pieces[index+1]
		result.args = append(result.args, part.args...)
	}
	return result
}

type sqlCompiler struct{ postgres bool }

func (c sqlCompiler) node(current node) (fragment, error) {
	switch typed := current.(type) {
	case logicalNode:
		left, err := c.node(typed.left)
		if err != nil {
			return fragment{}, err
		}
		right, err := c.node(typed.right)
		if err != nil {
			return fragment{}, err
		}
		return compose("(%s "+strings.ToUpper(typed.operator)+" %s)", left, right), nil
	case notNode:
		child, err := c.node(typed.child)
		if err != nil {
			return fragment{}, err
		}
		return compose("(NOT %s)", child), nil
	case predicateNode:
		field, err := c.field(typed.field)
		if err != nil {
			return fragment{}, err
		}
		return c.predicate(field, typed), nil
	default:
		return fragment{}, fmt.Errorf("unsupported resource query node")
	}
}

func truth(condition fragment) fragment {
	return compose("(CASE WHEN %s THEN 1 ELSE 0 END = 1)", condition)
}

func (c sqlCompiler) predicate(field sqlField, predicate predicateNode) fragment {
	switch predicate.operator {
	case OperatorIsNull:
		return compose("(NOT %s)", field.present())
	case OperatorIsNotNull:
		return field.present()
	case OperatorContains:
		return field.contains("%"+escapeLike(strings.ToLower(predicate.values[0].String))+"%", c.comparable(field, ValueString))
	case OperatorIn, OperatorNotIn:
		value := c.comparable(field, predicate.values[0].Kind)
		placeholders := make([]string, len(predicate.values))
		arguments := make([]any, len(predicate.values))
		for index, expected := range predicate.values {
			placeholders[index] = "?"
			arguments[index] = sqlValue(expected)
		}
		matched := truth(compose("%s IN (%s)", value, sql(strings.Join(placeholders, ", "), arguments...)))
		if predicate.operator == OperatorNotIn {
			return compose("(%s AND NOT %s)", field.present(), matched)
		}
		return matched
	default:
		value := c.comparable(field, predicate.values[0].Kind)
		if c.postgres && predicate.values[0].Kind == ValueString {
			// Compare bytes, as Go does, whatever the database collation.
			value = compose(`%s COLLATE "C"`, value)
		}
		return truth(compose("%s "+string(predicate.operator)+" %s", value, sql("?", sqlValue(predicate.values[0]))))
	}
}

// comparable is the field's value as the kind of the expected value, or NULL
// when the two cannot be compared, mirroring compare.
func (c sqlCompiler) comparable(field sqlField, kind ValueKind) fragment {
	switch field.shape {
	case shapeBoolColumn:
		switch kind {
		case ValueString:
			return sql("(CASE WHEN " + field.column + " THEN 'true' ELSE 'false' END)")
		case ValueBoolean:
			return field.booleanValue()
		}
		return sql("NULL")
	case shapeTextColumn, shapeOptionalText:
		text := field.stringValue()
		switch kind {
		case ValueNumber:
			return compose("(CASE WHEN %s THEN %s END)", c.numericText(text), c.castNumber(text))
		case ValueBoolean:
			return c.booleanText(text)
		}
		return text
	}
	isString, text := field.is(jsonString), field.stringValue()
	switch kind {
	case ValueNumber:
		return compose("(CASE WHEN %s THEN %s WHEN %s AND %s THEN %s END)",
			field.is(jsonNumber), field.numberValue(), isString, c.numericText(text), c.castNumber(text))
	case ValueBoolean:
		return compose("(CASE WHEN %s THEN %s WHEN %s THEN %s END)",
			field.is(jsonBoolean), field.booleanValue(), isString, c.booleanText(text))
	}
	return compose("(CASE WHEN %s THEN %s WHEN %s THEN %s WHEN %s THEN (CASE WHEN %s = 1 THEN 'true' ELSE 'false' END) END)",
		isString, text, field.is(jsonNumber), field.numberText(), field.is(jsonBoolean), field.booleanValue())
}

// numericText holds when text is a JSON number literal, the strings
// numberValue parses in memory.
func (c sqlCompiler) numericText(text fragment) fragment {
	if c.postgres {
		return compose(`(%s ~ '^-{0,1}(0|[1-9][0-9]*)(\.[0-9]+){0,1}([eE][+-]{0,1}[0-9]+){0,1}$')`, text)
	}
	// json_valid also accepts surrounding whitespace, which the GLOBs reject.
	return compose("(CASE WHEN json_valid(%s) THEN json_type(%s) END IN ('integer', 'real') AND %s GLOB '[-0-9]*' AND %s GLOB '*[0-9]')", text, text, text, text)
}

func (c sqlCompiler) castNumber(text fragment) fragment {
	if c.postgres {
		return compose("CAST(%s AS NUMERIC)", text)
	}
	return compose("CAST(%s AS REAL)", text)
}

// booleanText is 1 or 0 for the strings strconv.ParseBool accepts.
func (c sqlCompiler) booleanText(text fragment) fragment {
	return compose(
		"(CASE WHEN %s IN ('1', 't', 'T', 'TRUE', 'true', 'True') THEN 1 WHEN %s IN ('0', 'f', 'F', 'FALSE', 'false', 'False') THEN 0 END)",
		text, text,
	)
}

func sqlValue(value Value) any {
	if value.Kind == ValueBoolean {
		if value.Boolean {
			return 1
		}
		return 0
	}
	return value.Any()
}

type jsonType int

const (
	jsonString jsonType = iota
	jsonNumber
	jsonBoolean
)

type fieldShape int

const (
	shapeJSON         fieldShape = iota // a value inside the payload
	shapeTextColumn                     // a text column that always has a value
	shapeOptionalText                   // a payload string that is absent when empty
	shapeBoolColumn                     // a boolean column
)

// sqlField reads one queried field the way assetField does in memory.
type sqlField struct {
	postgres bool
	shape    fieldShape
	column   string   // shapeTextColumn, shapeBoolColumn
	key      string   // shapeOptionalText
	path     []string // shapeJSON
}

func (c sqlCompiler) field(field string) (sqlField, error) {
	result := sqlField{postgres: c.postgres}
	column := func(name string) (sqlField, error) {
		result.shape, result.column = shapeTextColumn, name
		return result, nil
	}
	canonical := canonicalField(field)
	switch canonical {
	case "type", "resourcetype":
		return column("assets.native_type")
	case "provider":
		return column("assets.provider")
	case "id":
		return column("assets.id")
	case "resourceid":
		return column("assets.native_id")
	case "resourcekindid":
		return column("assets.resource_kind_id")
	case "dirty":
		result.shape, result.column = shapeBoolColumn, "assets.dirty"
		return result, nil
	case "name", "state":
		result.shape, result.key = shapeOptionalText, canonical
		return result, nil
	case "region", "location":
		result.shape, result.key = shapeOptionalText, "location"
		return result, nil
	}
	result.shape = shapeJSON
	switch {
	case strings.HasPrefix(canonical, "properties."):
		result.path = append([]string{"normalized"}, strings.Split(field[len("properties."):], ".")...)
	case strings.HasPrefix(canonical, "tags."):
		result.path = []string{"tags", field[len("tags."):]}
	default:
		return sqlField{}, fmt.Errorf("unsupported resource query field %q", field)
	}
	return result, nil
}

// value is the JSON value at the field's path; SQLite addresses it with a
// JSON path argument, PostgreSQL with one key argument per segment.
func (f sqlField) value() fragment {
	if f.postgres {
		placeholders := make([]string, len(f.path))
		arguments := make([]any, len(f.path))
		for index, segment := range f.path {
			placeholders[index], arguments[index] = "?", segment
		}
		return sql("jsonb_extract_path(assets.payload, "+strings.Join(placeholders, ", ")+")", arguments...)
	}
	return sql("?", sqlitePath(f.path))
}

func sqlitePath(segments []string) string {
	return `$."` + strings.Join(segments, `"."`) + `"`
}

func (f sqlField) jsonType() fragment {
	if f.postgres {
		return compose("jsonb_typeof(%s)", f.value())
	}
	return compose("json_type(assets.payload, %s)", f.value())
}

func (f sqlField) optionalText() fragment {
	if f.postgres {
		return sql("NULLIF(assets.payload ->> '" + f.key + "', '')")
	}
	return sql("NULLIF(json_extract(assets.payload, '$." + f.key + "'), '')")
}

func (f sqlField) present() fragment {
	switch f.shape {
	case shapeTextColumn, shapeBoolColumn:
		return sql("(1 = 1)")
	case shapeOptionalText:
		return compose("(%s IS NOT NULL)", f.optionalText())
	}
	return compose("(COALESCE(%s, 'null') <> 'null')", f.jsonType())
}

// is tests the JSON type of a payload field; it is NULL for a missing field.
func (f sqlField) is(kind jsonType) fragment {
	names := map[jsonType]string{jsonString: "'string'", jsonNumber: "'number'", jsonBoolean: "'boolean'"}
	if !f.postgres {
		names = map[jsonType]string{jsonString: "'text'", jsonNumber: "'integer', 'real'", jsonBoolean: "'true', 'false'"}
	}
	return compose("(%s IN ("+names[kind]+"))", f.jsonType())
}

// stringValue is the field's text when it is a string.
func (f sqlField) stringValue() fragment {
	switch f.shape {
	case shapeTextColumn, shapeBoolColumn:
		return sql(f.column)
	case shapeOptionalText:
		return f.optionalText()
	}
	if f.postgres {
		return compose("(%s #>> '{}')", f.value())
	}
	return compose("json_extract(assets.payload, %s)", f.value())
}

// numberValue and numberText read the field when it is a JSON number.
func (f sqlField) numberValue() fragment {
	if f.postgres {
		return compose("CAST(%s #>> '{}' AS NUMERIC)", f.value())
	}
	return compose("json_extract(assets.payload, %s)", f.value())
}

func (f sqlField) numberText() fragment {
	if f.postgres {
		return compose("(%s #>> '{}')", f.value())
	}
	// The number exactly as stored, which is how Go formats it.
	return compose("(assets.payload -> %s)", f.value())
}

// booleanValue is 1 or 0 when the field is a boolean.
func (f sqlField) booleanValue() fragment {
	if f.shape == shapeBoolColumn {
		return sql("(CASE WHEN " + f.column + " THEN 1 ELSE 0 END)")
	}
	if f.postgres {
		return compose("(CASE WHEN %s #>> '{}' = 'true' THEN 1 ELSE 0 END)", f.value())
	}
	return compose("(CASE WHEN %s = 'true' THEN 1 ELSE 0 END)", f.jsonType())
}

// contains holds when the text of the value, or of any value or object key
// nested in it, matches the lowercased LIKE pattern; text is the field as a
// string for fields outside the payload.
func (f sqlField) contains(pattern string, text fragment) fragment {
	if f.shape != shapeJSON {
		return truth(compose(`%s AND LOWER(%s) LIKE %s ESCAPE '\'`, f.present(), text, sql("?", pattern)))
	}
	if f.postgres {
		return compose(`EXISTS (
			SELECT 1 FROM jsonb_path_query(%s, 'strict $.**') AS node(value)
			WHERE (jsonb_typeof(node.value) IN ('string', 'number', 'boolean') AND LOWER(node.value #>> '{}') LIKE %s ESCAPE '\')
				OR EXISTS (
					SELECT 1 FROM jsonb_object_keys(CASE WHEN jsonb_typeof(node.value) = 'object' THEN node.value ELSE '{}'::jsonb END) AS member(key)
					WHERE LOWER(member.key) LIKE %s ESCAPE '\'
				)
		)`, f.value(), sql("?", pattern), sql("?", pattern))
	}
	return compose(`EXISTS (
		SELECT 1 FROM json_tree(assets.payload, %s) AS node
		WHERE LOWER(CASE node.type
				WHEN 'text' THEN node.atom
				WHEN 'integer' THEN assets.payload -> node.fullkey
				WHEN 'real' THEN assets.payload -> node.fullkey
				WHEN 'true' THEN 'true'
				WHEN 'false' THEN 'false'
			END) LIKE %s ESCAPE '\'
			OR (node.parent IS NOT NULL AND typeof(node.key) = 'text' AND LOWER(node.key) LIKE %s ESCAPE '\')
	)`, f.value(), sql("?", pattern), sql("?", pattern))
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}
