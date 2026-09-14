package gcp

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Logging shares OR-before-AND precedence with Monitoring, but has different
// tokens, comments, Unicode comparison and NOT/missing-field semantics.
// Keep its grammar separate; substituting true for unknown fields under NOT
// would incorrectly prove some live references absent.
type loggingToken struct{ kind, text string }
type loggingValue struct {
	path    []string
	literal string
	quoted  bool
	call    string
	args    []*loggingValue
}
type loggingExpr struct {
	op           string
	left, right  *loggingExpr
	value, other *loggingValue
}
type loggingParser struct {
	tokens     []loggingToken
	pos, nodes int
	invalid    bool
}

func loggingTokens(input string) ([]loggingToken, bool) {
	if !utf8.ValidString(input) || utf8.RuneCountInString(input) > 20000 {
		return nil, false
	}
	tokens := []loggingToken{}
	for i := 0; i < len(input); {
		c := input[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(input[i:], "--") {
			for i < len(input) && input[i] != '\n' {
				i++
			}
			continue
		}
		if c == '"' {
			start := i
			i++
			for i < len(input) && input[i] != '"' {
				if input[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(input) {
				return nil, false
			}
			i++
			value, err := strconv.Unquote(input[start:i])
			if err != nil || !utf8.ValidString(value) {
				return nil, false
			}
			tokens = append(tokens, loggingToken{"string", value})
			continue
		}
		if strings.ContainsRune("()=!:<>~,*.+-", rune(c)) {
			value := string(c)
			i++
			if i < len(input) && ((c == '=' || c == '!') && input[i] == '~' || (c == '!' || c == '<' || c == '>') && input[i] == '=') {
				value += string(input[i])
				i++
			}
			if value == "!" || value == "~" {
				return nil, false
			}
			tokens = append(tokens, loggingToken{value, value})
			continue
		}
		start := i
		for i < len(input) && !strings.ContainsRune(" \t\r\n()=!:<>~,*.+\"", rune(input[i])) {
			i++
		}
		if start == i {
			return nil, false
		}
		value := input[start:i]
		// The docs do not disambiguate embedded comment markers in bare words.
		// Quoted values are unambiguous and unaffected.
		if strings.Contains(value, "--") {
			return nil, false
		}
		tokens = append(tokens, loggingToken{"word", value})
	}
	tokens = append(tokens, loggingToken{"end", ""})
	return tokens, true
}
func (p *loggingParser) token() loggingToken { return p.tokens[p.pos] }
func (p *loggingParser) take(value string) bool {
	if p.token().text != value || p.token().kind == "string" {
		return false
	}
	p.pos++
	return true
}
func (p *loggingParser) value(depth int) *loggingValue {
	p.nodes++
	if depth > 64 || p.nodes > 512 {
		p.invalid = true
		return nil
	}
	token := p.token()
	if token.kind == "+" || token.kind == "-" {
		p.pos++
		next := p.token()
		if next.kind != "word" {
			p.invalid = true
			return nil
		}
		token.text += next.text
		token.kind = "word"
		p.pos++
	} else {
		if token.kind != "word" && token.kind != "string" && token.kind != "*" {
			p.invalid = true
			return nil
		}
		p.pos++
	}
	result := &loggingValue{literal: token.text, quoted: token.kind == "string"}
	if token.kind == "word" && (token.text == "AND" || token.text == "OR" || token.text == "NOT") {
		p.invalid = true
		return nil
	}
	if token.kind == "word" && p.take("(") {
		result.call = strings.ToLower(token.text)
		if !p.take(")") {
			for !p.invalid {
				result.args = append(result.args, p.value(depth+1))
				if !p.take(",") {
					break
				}
			}
			if !p.take(")") {
				p.invalid = true
			}
		}
		if !p.invalid && !result.validCall() {
			p.invalid = true
		}
		return result
	}
	result.path = []string{token.text}
	for p.take(".") {
		next := p.token()
		if next.kind != "word" && next.kind != "string" {
			p.invalid = true
			return nil
		}
		result.path = append(result.path, next.text)
		result.literal += "." + next.text
		p.pos++
	}
	return result
}
func (v *loggingValue) validCall() bool {
	n := len(v.args)
	switch v.call {
	case "cast":
		return n == 2 || n == 3
	case "regexp_extract":
		if n != 2 || !v.args[1].quoted {
			return false
		}
		re, err := regexp.Compile(v.args[1].literal)
		return err == nil && re.NumSubexp() == 1
	case "log_id", "source", "time_zone":
		return n == 1
	case "sample":
		if n != 2 {
			return false
		}
		fraction, err := strconv.ParseFloat(v.args[1].literal, 64)
		return err == nil && fraction > 0 && fraction <= 1
	case "ip_in_net":
		if n != 2 {
			return false
		}
		_, err := netip.ParsePrefix(v.args[1].literal)
		if err != nil {
			_, err = netip.ParseAddr(v.args[1].literal)
		}
		return err == nil
	case "search":
		return (n == 1 || n == 2) && v.args[n-1].quoted
	default:
		return false
	}
}
func (p *loggingParser) and(depth int, restriction *loggingExpr) *loggingExpr {
	result := p.or(depth, restriction)
	for !p.invalid && p.token().kind != "end" && p.token().kind != ")" {
		p.take("AND")
		result = &loggingExpr{op: "AND", left: result, right: p.or(depth, restriction)}
	}
	return result
}
func (p *loggingParser) or(depth int, restriction *loggingExpr) *loggingExpr {
	result := p.term(depth, restriction)
	for !p.invalid && p.take("OR") {
		result = &loggingExpr{op: "OR", left: result, right: p.term(depth, restriction)}
	}
	return result
}
func (p *loggingParser) term(depth int, restriction *loggingExpr) *loggingExpr {
	p.nodes++
	if depth > 64 || p.nodes > 512 {
		p.invalid = true
		return nil
	}
	if p.take("NOT") || p.take("-") {
		return &loggingExpr{op: "NOT", left: p.term(depth+1, restriction)}
	}
	if p.take("(") {
		result := p.and(depth+1, restriction)
		if !p.take(")") {
			p.invalid = true
		}
		return result
	}
	value := p.value(depth + 1)
	if p.invalid {
		return nil
	}
	if restriction != nil {
		return &loggingExpr{op: restriction.op, value: restriction.value, other: value}
	}
	op := p.token().kind
	switch op {
	case "=", "!=", ":", "<", ">", "<=", ">=", "=~", "!~":
		p.pos++
		result := &loggingExpr{op: op, value: value}
		if p.take("(") {
			expanded := p.and(depth+1, result)
			if !p.take(")") {
				p.invalid = true
			}
			return expanded
		}
		result.other = p.value(depth + 1)
		return result
	default:
		return &loggingExpr{op: "global", value: value}
	}
}
func parseLoggingFilter(filter string) (*loggingExpr, bool) {
	tokens, ok := loggingTokens(filter)
	if !ok {
		return nil, false
	}
	p := &loggingParser{tokens: tokens}
	result := p.and(0, nil)
	if p.invalid || p.token().kind != "end" || result == nil {
		return nil, false
	}
	return result, result.validRegex()
}
func (e *loggingExpr) validRegex() bool {
	if e.left != nil && !e.left.validRegex() || e.right != nil && !e.right.validRegex() {
		return false
	}
	if e.op == "=~" || e.op == "!~" {
		if e.other == nil || !e.other.quoted || len(e.other.path) != 1 {
			return false
		}
		_, err := regexp.Compile(e.other.literal)
		return err == nil
	}
	return true
}
func (v *loggingValue) checkField() bool {
	return v != nil && len(v.path) == 2 && strings.EqualFold(v.path[0], "labels") && v.path[1] == "check_id"
}
func (v *loggingValue) referencesCheck() bool {
	if v == nil {
		return false
	}
	if v.checkField() {
		return true
	}
	switch v.call {
	case "cast", "regexp_extract", "sample", "ip_in_net":
		return len(v.args) > 0 && v.args[0].referencesCheck()
	case "search":
		return len(v.args) == 2 && v.args[0].referencesCheck()
	}
	return false
}
func (e *loggingExpr) referencesCheck() bool {
	return e.value.referencesCheck() || e.other.referencesCheck() || e.left != nil && e.left.referencesCheck() || e.right != nil && e.right.referencesCheck()
}
func loggingNot(value monitoringReference) monitoringReference {
	if value == monitoringUnresolvedReference {
		return value
	}
	return monitoringBool(value == monitoringNoReference)
}

type loggingScalar struct {
	kind, text string
	known      bool
}

func (v *loggingValue) field(check string) loggingScalar {
	if v.checkField() {
		return loggingScalar{kind: "string", text: check, known: true}
	}
	if v.call == "cast" {
		value := v.args[0].field(check)
		if !value.known {
			return value
		}
		kind := strings.ToUpper(v.args[1].literal)
		// Conversion between already cast scalar types has additional rounding
		// and formatting rules. Retain that uncertainty until independently verified.
		if len(v.args) == 3 || value.kind != "string" {
			return loggingScalar{}
		}
		switch kind {
		case "STRING":
			return loggingScalar{kind: "string", text: value.text, known: true}
		case "INT64":
			number, err := strconv.ParseInt(value.text, 10, 64)
			if err != nil {
				return loggingScalar{}
			}
			return loggingScalar{kind: "int", text: strconv.FormatInt(number, 10), known: true}
		case "FLOAT64":
			number, err := loggingFloat(value.text)
			if err != nil {
				return loggingScalar{}
			}
			return loggingScalar{kind: "float", text: strconv.FormatFloat(number, 'g', -1, 64), known: true}
		case "BOOL":
			valid := strings.EqualFold(value.text, "true") || strings.EqualFold(value.text, "false")
			if !valid {
				return loggingScalar{}
			}
			return loggingScalar{kind: "bool", text: strings.ToLower(value.text), known: true}
		default:
			return loggingScalar{}
		}
	}
	if v.call == "regexp_extract" {
		value := v.args[0].field(check)
		if !value.known || value.kind != "string" {
			return loggingScalar{}
		}
		re := regexp.MustCompile(v.args[1].literal)
		indices := re.FindStringSubmatchIndex(value.text)
		// The API docs do not specify failed/optional capture conversion semantics.
		if len(indices) < 4 || indices[2] < 0 {
			return loggingScalar{}
		}
		return loggingScalar{kind: "string", text: value.text[indices[2]:indices[3]], known: true}
	}
	return loggingScalar{}
}

// Logging accepts case-insensitive NaN and Infinity spellings.
func loggingFloat(value string) (float64, error) {
	switch strings.ToLower(value) {
	case "nan":
		value = "NaN"
	case "infinity", "+infinity":
		value = "+Inf"
	case "-infinity":
		value = "-Inf"
	}
	return strconv.ParseFloat(value, 64)
}
func loggingCompare(left loggingScalar, op string, right *loggingValue, check string) monitoringReference {
	if !left.known {
		return monitoringUnresolvedReference
	}
	if op == ":" && !right.quoted && right.literal == "*" {
		return monitoringHasReference
	}
	expected := right.literal
	if right.call != "" {
		value := right.field(check)
		if !value.known {
			return monitoringUnresolvedReference
		}
		expected = value.text
	}
	if op == "=~" || op == "!~" {
		if left.kind != "string" {
			return monitoringUnresolvedReference
		}
		match := regexp.MustCompile(expected).MatchString(left.text)
		if op == "!~" {
			match = !match
		}
		return monitoringBool(match)
	}
	cmp := 0
	switch left.kind {
	case "string":
		a, ok := loggingNativeFold(left.text)
		if !ok {
			return monitoringUnresolvedReference
		}
		b, ok := loggingNativeFold(expected)
		if !ok {
			return monitoringUnresolvedReference
		}
		if op == ":" {
			return monitoringBool(strings.Contains(a, b))
		}
		cmp = strings.Compare(a, b)
	case "int":
		a, err := strconv.ParseInt(left.text, 10, 64)
		if err != nil {
			return monitoringUnresolvedReference
		}
		b, err := strconv.ParseInt(expected, 10, 64)
		if err != nil {
			return monitoringUnresolvedReference
		}
		if a < b {
			cmp = -1
		} else if a > b {
			cmp = 1
		}
	case "float":
		a, err := loggingFloat(left.text)
		if err != nil {
			return monitoringUnresolvedReference
		}
		b, err := loggingFloat(expected)
		if err != nil {
			return monitoringUnresolvedReference
		}
		if a != a || b != b {
			return monitoringUnresolvedReference
		}
		if a < b {
			cmp = -1
		} else if a > b {
			cmp = 1
		}
	case "bool":
		if !strings.EqualFold(expected, "true") && !strings.EqualFold(expected, "false") {
			return monitoringUnresolvedReference
		}
		cmp = strings.Compare(left.text, strings.ToLower(expected))
	default:
		return monitoringUnresolvedReference
	}
	switch op {
	case "=":
		return monitoringBool(cmp == 0)
	case "!=":
		return monitoringBool(cmp != 0)
	case "<":
		return monitoringBool(cmp < 0)
	case "<=":
		return monitoringBool(cmp <= 0)
	case ">":
		return monitoringBool(cmp > 0)
	case ">=":
		return monitoringBool(cmp >= 0)
	default:
		return monitoringUnresolvedReference
	}
}
func (v *loggingValue) global(check string) monitoringReference {
	switch v.call {
	case "sample":
		if v.args[0].checkField() && v.args[1].literal == "1" {
			return monitoringHasReference
		}
	case "ip_in_net":
		value := v.args[0].field(check)
		if value.known {
			address, err := netip.ParseAddr(value.text)
			if err != nil {
				return monitoringNoReference
			}
			// Native docs do not define IPv4-mapped IPv6 or zone handling.
			if address.Is4In6() || address.Zone() != "" {
				return monitoringUnresolvedReference
			}
			prefix, err := netip.ParsePrefix(v.args[1].literal)
			if err == nil {
				if prefix.Addr().Is4In6() {
					return monitoringUnresolvedReference
				}
				return monitoringBool(prefix.Contains(address))
			}
			other, _ := netip.ParseAddr(v.args[1].literal)
			if other.Is4In6() || other.Zone() != "" {
				return monitoringUnresolvedReference
			}
			return monitoringBool(address == other)
		}
	case "":
		// Global restrictions may also match unknown payload fields. A match in
		// check_id proves true; failure there does not prove the global query false.
		actual, ok := loggingNativeFold(check)
		if !ok {
			return monitoringUnresolvedReference
		}
		expected, ok := loggingNativeFold(v.literal)
		if !ok {
			return monitoringUnresolvedReference
		}
		if strings.Contains(actual, expected) {
			return monitoringHasReference
		}
	}
	return monitoringUnresolvedReference
}
func (e *loggingExpr) match(check string) monitoringReference {
	switch e.op {
	case "AND":
		return monitoringAnd(e.left.match(check), e.right.match(check))
	case "OR":
		return monitoringOr(e.left.match(check), e.right.match(check))
	case "NOT":
		return loggingNot(e.left.match(check))
	case "global":
		return e.value.global(check)
	default:
		return loggingCompare(e.value.field(check), e.op, e.other, check)
	}
}
func loggingFilterReference(filter, check string) monitoringReference {
	expr, ok := parseLoggingFilter(filter)
	if !ok || check == "" {
		return monitoringUnresolvedReference
	}
	result := expr.match(check)
	// Retain potentially matching references even when volatile payload fields
	// are unknown. NOT is evaluated before this conservative final projection.
	if result == monitoringUnresolvedReference && expr.referencesCheck() {
		return monitoringHasReference
	}
	return result
}
