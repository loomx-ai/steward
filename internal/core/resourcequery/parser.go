package resourcequery

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type ParseError struct {
	Position int
	Message  string
}

func (e *ParseError) Error() string { return e.Message }

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenIdentifier
	tokenString
	tokenNumber
	tokenEqual
	tokenNotEqual
	tokenGreater
	tokenGreaterEqual
	tokenLess
	tokenLessEqual
	tokenLeftParen
	tokenRightParen
	tokenComma
)

type token struct {
	kind     tokenKind
	text     string
	position int
}

func Parse(source string) (*Expression, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, nil
	}
	if len(source) > MaxLength {
		return nil, &ParseError{Position: MaxLength, Message: fmt.Sprintf("query cannot exceed %d characters", MaxLength)}
	}
	tokens, err := lex(source)
	if err != nil {
		return nil, err
	}
	parser := parser{tokens: tokens}
	root, err := parser.parseOr()
	if err != nil {
		return nil, err
	}
	if current := parser.peek(); current.kind != tokenEOF {
		return nil, parseFailure(current, fmt.Sprintf("unexpected %q", current.text))
	}
	if root.predicateCount() > MaxPredicates {
		return nil, &ParseError{Message: fmt.Sprintf("query cannot contain more than %d conditions", MaxPredicates)}
	}
	if root.depth() > MaxDepth {
		return nil, &ParseError{Message: fmt.Sprintf("query cannot be nested deeper than %d levels", MaxDepth)}
	}
	return &Expression{source: source, root: root}, nil
}

type parser struct {
	tokens []token
	index  int
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = logicalNode{operator: "or", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.keyword("AND") {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = logicalNode{operator: "and", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseUnary() (node, error) {
	if p.keyword("NOT") {
		p.next()
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notNode{child: child}, nil
	}
	if p.peek().kind == tokenLeftParen {
		p.next()
		child, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokenRightParen {
			return nil, parseFailure(p.peek(), "expected closing parenthesis")
		}
		p.next()
		return child, nil
	}
	return p.parsePredicate()
}

func (p *parser) parsePredicate() (node, error) {
	field := p.next()
	if field.kind != tokenIdentifier || isKeyword(field.text) {
		return nil, parseFailure(field, "expected a field name")
	}
	operator, err := p.parseOperator()
	if err != nil {
		return nil, err
	}
	predicate := predicateNode{field: field.text, operator: operator, position: field.position}
	if operator == OperatorIsNull || operator == OperatorIsNotNull {
		return predicate, nil
	}
	if operator == OperatorIn || operator == OperatorNotIn {
		if p.peek().kind != tokenLeftParen {
			return nil, parseFailure(p.peek(), "expected a parenthesized value list")
		}
		p.next()
		for {
			value, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			predicate.values = append(predicate.values, value)
			if p.peek().kind != tokenComma {
				break
			}
			p.next()
		}
		for _, value := range predicate.values[1:] {
			if value.Kind != predicate.values[0].Kind {
				return nil, parseFailure(field, "IN values must use the same type")
			}
		}
		if p.peek().kind != tokenRightParen {
			return nil, parseFailure(p.peek(), "expected closing parenthesis after value list")
		}
		p.next()
		return predicate, nil
	}
	value, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if operator == OperatorContains && value.Kind != ValueString {
		return nil, parseFailure(field, "contains requires a quoted string")
	}
	predicate.values = []Value{value}
	return predicate, nil
}

func (p *parser) parseOperator() (Operator, error) {
	current := p.next()
	switch current.kind {
	case tokenEqual:
		return OperatorEqual, nil
	case tokenNotEqual:
		return OperatorNotEqual, nil
	case tokenGreater:
		return OperatorGreater, nil
	case tokenGreaterEqual:
		return OperatorGreaterEq, nil
	case tokenLess:
		return OperatorLess, nil
	case tokenLessEqual:
		return OperatorLessEq, nil
	}
	if current.kind != tokenIdentifier {
		return "", parseFailure(current, "expected an operator")
	}
	switch strings.ToUpper(current.text) {
	case "IN":
		return OperatorIn, nil
	case "CONTAINS":
		return OperatorContains, nil
	case "NOT":
		if !p.keyword("IN") {
			return "", parseFailure(p.peek(), "expected IN after NOT")
		}
		p.next()
		return OperatorNotIn, nil
	case "IS":
		negated := false
		if p.keyword("NOT") {
			p.next()
			negated = true
		}
		if !p.keyword("NULL") {
			return "", parseFailure(p.peek(), "expected NULL after IS")
		}
		p.next()
		if negated {
			return OperatorIsNotNull, nil
		}
		return OperatorIsNull, nil
	default:
		return "", parseFailure(current, fmt.Sprintf("unsupported operator %q", current.text))
	}
}

func (p *parser) parseValue() (Value, error) {
	current := p.next()
	switch current.kind {
	case tokenString:
		return Value{Kind: ValueString, String: current.text}, nil
	case tokenNumber:
		value, err := strconv.ParseFloat(current.text, 64)
		if err != nil {
			return Value{}, parseFailure(current, "number is invalid")
		}
		return Value{Kind: ValueNumber, Number: value}, nil
	case tokenIdentifier:
		switch strings.ToUpper(current.text) {
		case "TRUE":
			return Value{Kind: ValueBoolean, Boolean: true}, nil
		case "FALSE":
			return Value{Kind: ValueBoolean, Boolean: false}, nil
		default:
			return Value{}, parseFailure(current, "string values must be quoted")
		}
	default:
		return Value{}, parseFailure(current, "expected a value")
	}
}

func (p *parser) peek() token {
	if p.index >= len(p.tokens) {
		return token{kind: tokenEOF}
	}
	return p.tokens[p.index]
}

func (p *parser) next() token {
	current := p.peek()
	if p.index < len(p.tokens) {
		p.index++
	}
	return current
}

func (p *parser) keyword(value string) bool {
	current := p.peek()
	return current.kind == tokenIdentifier && strings.EqualFold(current.text, value)
}

func parseFailure(value token, message string) error {
	return &ParseError{Position: value.position, Message: message}
}

func isKeyword(value string) bool {
	switch strings.ToUpper(value) {
	case "AND", "OR", "NOT", "IN", "IS", "NULL", "CONTAINS", "TRUE", "FALSE":
		return true
	default:
		return false
	}
}

func lex(source string) ([]token, error) {
	tokens := make([]token, 0, len(source)/4)
	for index := 0; index < len(source); {
		character := rune(source[index])
		if unicode.IsSpace(character) {
			index++
			continue
		}
		position := index
		switch source[index] {
		case '(':
			tokens = append(tokens, token{kind: tokenLeftParen, text: "(", position: position})
			index++
		case ')':
			tokens = append(tokens, token{kind: tokenRightParen, text: ")", position: position})
			index++
		case ',':
			tokens = append(tokens, token{kind: tokenComma, text: ",", position: position})
			index++
		case '=':
			tokens = append(tokens, token{kind: tokenEqual, text: "=", position: position})
			index++
		case '!':
			if index+1 >= len(source) || source[index+1] != '=' {
				return nil, &ParseError{Position: position, Message: "expected !="}
			}
			tokens = append(tokens, token{kind: tokenNotEqual, text: "!=", position: position})
			index += 2
		case '>', '<':
			kind := tokenGreater
			if source[index] == '<' {
				kind = tokenLess
			}
			text := source[index : index+1]
			index++
			if index < len(source) && source[index] == '=' {
				text += "="
				index++
				if kind == tokenGreater {
					kind = tokenGreaterEqual
				} else {
					kind = tokenLessEqual
				}
			}
			tokens = append(tokens, token{kind: kind, text: text, position: position})
		case '\'', '"':
			quote := source[index]
			index++
			var value strings.Builder
			closed := false
			for index < len(source) {
				if source[index] == quote {
					index++
					closed = true
					break
				}
				if source[index] == '\\' {
					index++
					if index >= len(source) {
						break
					}
					switch source[index] {
					case 'n':
						value.WriteByte('\n')
					case 'r':
						value.WriteByte('\r')
					case 't':
						value.WriteByte('\t')
					default:
						value.WriteByte(source[index])
					}
					index++
					continue
				}
				value.WriteByte(source[index])
				index++
			}
			if !closed {
				return nil, &ParseError{Position: position, Message: "unterminated string"}
			}
			tokens = append(tokens, token{kind: tokenString, text: value.String(), position: position})
		default:
			if isNumberStart(source, index) {
				index = scanNumber(source, index)
				tokens = append(tokens, token{kind: tokenNumber, text: source[position:index], position: position})
				continue
			}
			if !identifierCharacter(source[index], true) {
				return nil, &ParseError{Position: position, Message: fmt.Sprintf("unexpected character %q", source[index])}
			}
			for index < len(source) && identifierCharacter(source[index], false) {
				index++
			}
			tokens = append(tokens, token{kind: tokenIdentifier, text: source[position:index], position: position})
		}
	}
	tokens = append(tokens, token{kind: tokenEOF, position: len(source)})
	return tokens, nil
}

func identifierCharacter(value byte, first bool) bool {
	if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value == '_' {
		return true
	}
	return !first && (value >= '0' && value <= '9' || value == '-' || value == '.')
}

func isNumberStart(source string, index int) bool {
	return source[index] >= '0' && source[index] <= '9' || source[index] == '-' && index+1 < len(source) && source[index+1] >= '0' && source[index+1] <= '9'
}

func scanNumber(source string, index int) int {
	if source[index] == '-' {
		index++
	}
	for index < len(source) && source[index] >= '0' && source[index] <= '9' {
		index++
	}
	if index < len(source) && source[index] == '.' {
		index++
		for index < len(source) && source[index] >= '0' && source[index] <= '9' {
			index++
		}
	}
	return index
}
