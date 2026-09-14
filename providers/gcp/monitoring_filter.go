package gcp

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"text/scanner"
)

// This is a dependency analysis, not a time-series query evaluator. Resource,
// metadata and other metric labels cannot prove that a policy has no reference
// to a check: they may change independently of its configuration. Their values
// are therefore existential. Unknown metric selectors remain unresolved.
// Native grammar: https://cloud.google.com/monitoring/api/v3/filters
// In particular OR binds more tightly than AND, and AND can be implicit.
type monitoringFilter struct {
	op, field, value, function string
	args                       []string
	left, right                *monitoringFilter
}

type monitoringFilterParser struct {
	scan    scanner.Scanner
	token   rune
	value   string
	invalid bool
	nodes   int
}

// Do not interpret Go-specific octal, hex or eight-digit escapes as native
// filter string semantics. Unknown escape dialects remain unresolved.
func monitoringString(raw string) (string, error) {
	value, err := strconv.Unquote(raw)
	if err != nil {
		return "", err
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		switch raw[i] {
		case '"', '\\', 'b', 'f', 'n', 'r', 't':
		case 'u':
			i += 4
		default:
			return "", errors.New("unsupported Monitoring string escape")
		}
	}
	return value, nil
}

func parseMonitoringFilter(value string) (*monitoringFilter, bool) {
	if len(value) == 0 || len(value) > 65536 {
		return nil, false
	}
	p := &monitoringFilterParser{}
	p.scan.Init(strings.NewReader(value))
	p.scan.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanInts | scanner.ScanFloats
	p.scan.Error = func(*scanner.Scanner, string) { p.invalid = true }
	p.next()
	result := p.and(0)
	return result, result != nil && !p.invalid && p.token == scanner.EOF
}
func (p *monitoringFilterParser) next() { p.token = p.scan.Scan(); p.value = p.scan.TokenText() }
func (p *monitoringFilterParser) and(depth int) *monitoringFilter {
	result := p.or(depth)
	for !p.invalid && p.token != scanner.EOF && p.token != ')' {
		if p.value == "AND" {
			p.next()
		}
		next := p.or(depth)
		result = &monitoringFilter{op: "AND", left: result, right: next}
	}
	return result
}
func (p *monitoringFilterParser) or(depth int) *monitoringFilter {
	result := p.atom(depth)
	for !p.invalid && p.value == "OR" {
		p.next()
		result = &monitoringFilter{op: "OR", left: result, right: p.atom(depth)}
	}
	return result
}
func (p *monitoringFilterParser) atom(depth int) *monitoringFilter {
	p.nodes++
	if depth > 64 || p.nodes > 512 {
		p.invalid = true
		return nil
	}
	if p.token == '(' {
		p.next()
		result := p.and(depth + 1)
		if p.token != ')' {
			p.invalid = true
		} else {
			p.next()
		}
		return result
	}
	result := &monitoringFilter{}
	if p.token != scanner.Ident || p.value == "AND" || p.value == "OR" || p.value == "NOT" {
		p.invalid = true
		return nil
	}
	result.field = p.value
	p.next()
	for p.token == '.' && !p.invalid {
		p.next()
		key := p.value
		if p.token == scanner.String {
			var err error
			key, err = monitoringString(key)
			if err != nil {
				p.invalid = true
			}
			// A quoted key is a single path component, even if it contains dots.
			if strings.Contains(key, ".") {
				key = strconv.Quote(key)
			}
		} else if p.token != scanner.Ident {
			p.invalid = true
		}
		result.field += "." + key
		p.next()
	}
	known := result.field == "project" || result.field == "group.id" || result.field == "metric.type" || result.field == "resource.type"
	for _, prefix := range []string{"metric.labels.", "metric.label.", "resource.labels.", "metadata.system_labels.", "metadata.user_labels."} {
		known = known || strings.HasPrefix(result.field, prefix) && len(result.field) > len(prefix)
	}
	if !known {
		p.invalid = true
		return nil
	}
	switch p.token {
	case '=', ':', '!', '<', '>':
		result.op = p.value
		p.next()
		if p.token == '=' && result.op != "=" && result.op != ":" {
			result.op += "="
			p.next()
		}
		if result.op == "!" {
			p.invalid = true
		}
	default:
		p.invalid = true
	}
	if p.invalid {
		return nil
	}
	if p.token == scanner.Ident && p.value != "true" && p.value != "false" {
		result.function = p.value
		p.next()
		for p.token == '.' {
			p.next()
			if p.token != scanner.Ident {
				p.invalid = true
				return nil
			}
			result.function += "." + p.value
			p.next()
		}
		if p.token != '(' {
			// Native exists syntax, e.g. resource.labels:zone.
			if result.op == ":" {
				result.value, result.function = result.function, ""
				return result
			}
			p.invalid = true
			return nil
		}
		p.next()
		for !p.invalid {
			if len(result.args) >= 100 {
				p.invalid = true
				break
			}
			if p.token != scanner.String && p.value != "true" && p.value != "false" {
				p.invalid = true
				break
			}
			result.args = append(result.args, p.value)
			p.next()
			if p.token != ',' {
				break
			}
			p.next()
		}
		if p.token != ')' {
			p.invalid = true
		} else {
			p.next()
		}
		if result.op != "=" && result.op != "!=" {
			p.invalid = true
		}
		switch result.function {
		case "starts_with", "ends_with", "monitoring.regex.full_match":
			if len(result.args) != 1 {
				p.invalid = true
			}
		case "has_substring":
			if len(result.args) < 1 || len(result.args) > 2 || len(result.args) == 2 && result.args[1] != "true" && result.args[1] != "false" {
				p.invalid = true
			}
		case "one_of":
			if len(result.args) == 0 {
				p.invalid = true
			}
		default:
			p.invalid = true
		}
		for i, arg := range result.args {
			if result.function == "has_substring" && i == 1 {
				continue
			}
			decoded, err := monitoringString(arg)
			if err != nil {
				p.invalid = true
			}
			result.args[i] = decoded
		}
		if result.function == "monitoring.regex.full_match" && len(result.args) == 1 {
			if _, err := regexp.Compile("\\A(?:" + result.args[0] + ")\\z"); err != nil {
				p.invalid = true
			}
		}
	} else {
		if p.token == '-' {
			result.value = "-"
			p.next()
		}
		if p.token != scanner.String && p.token != scanner.Int && p.token != scanner.Float && p.value != "true" && p.value != "false" {
			p.invalid = true
			return nil
		}
		if p.token == scanner.String {
			if _, err := monitoringString(p.value); err != nil {
				p.invalid = true
			}
		}
		result.value += p.value
		p.next()
	}
	return result
}

type monitoringReference int

const (
	monitoringNoReference monitoringReference = iota
	monitoringHasReference
	monitoringUnresolvedReference
)

func monitoringAnd(a, b monitoringReference) monitoringReference {
	if a == monitoringNoReference || b == monitoringNoReference {
		return monitoringNoReference
	}
	if a == monitoringUnresolvedReference || b == monitoringUnresolvedReference {
		return monitoringUnresolvedReference
	}
	return monitoringHasReference
}
func monitoringOr(a, b monitoringReference) monitoringReference {
	if a == monitoringHasReference || b == monitoringHasReference {
		return monitoringHasReference
	}
	if a == monitoringUnresolvedReference || b == monitoringUnresolvedReference {
		return monitoringUnresolvedReference
	}
	return monitoringNoReference
}
func monitoringBool(value bool) monitoringReference {
	if value {
		return monitoringHasReference
	}
	return monitoringNoReference
}

// A finite metric-type envelope avoids assuming that two different equality
// comparisons can match the same series. Nil means unbounded, not an empty set.
func (f *monitoringFilter) metricTypes() map[string]bool {
	if f.op == "AND" || f.op == "OR" {
		a, b := f.left.metricTypes(), f.right.metricTypes()
		if f.op == "OR" {
			if a == nil || b == nil {
				return nil
			}
			for key := range b {
				a[key] = true
			}
			return a
		}
		if a == nil {
			return b
		}
		if b == nil {
			return a
		}
		for key := range a {
			if !b[key] {
				delete(a, key)
			}
		}
		return a
	}
	if f.field != "metric.type" || f.op != "=" {
		return nil
	}
	if f.function == "one_of" {
		result := map[string]bool{}
		for _, value := range f.args {
			result[value] = true
		}
		return result
	}
	if f.function != "" {
		return nil
	}
	value, err := monitoringString(f.value)
	if err != nil {
		return nil
	}
	return map[string]bool{value: true}
}
func (f *monitoringFilter) match(metric, check string) monitoringReference {
	if f.op == "AND" {
		return monitoringAnd(f.left.match(metric, check), f.right.match(metric, check))
	}
	if f.op == "OR" {
		return monitoringOr(f.left.match(metric, check), f.right.match(metric, check))
	}
	var actual string
	switch f.field {
	case "metric.type":
		actual = metric
	case "metric.labels.check_id", "metric.label.check_id":
		actual = check
	default:
		return monitoringHasReference
	}
	if actual == "" {
		return monitoringUnresolvedReference
	}
	matched := false
	if f.function != "" {
		switch f.function {
		case "starts_with":
			matched = strings.HasPrefix(actual, f.args[0])
		case "ends_with":
			matched = strings.HasSuffix(actual, f.args[0])
		case "has_substring":
			needle := f.args[0]
			if len(f.args) == 2 && f.args[1] == "true" {
				matched = regexp.MustCompile("(?i:" + regexp.QuoteMeta(needle) + ")").MatchString(actual)
			}
			if len(f.args) < 2 || f.args[1] != "true" {
				matched = strings.Contains(actual, needle)
			}
		case "one_of":
			for _, value := range f.args {
				matched = matched || actual == value
			}
		case "monitoring.regex.full_match":
			matched = regexp.MustCompile("\\A(?:" + f.args[0] + ")\\z").MatchString(actual)
		}
	} else {
		expected, err := monitoringString(f.value)
		if err != nil {
			return monitoringUnresolvedReference
		}
		switch f.op {
		case "=", "!=":
			matched = actual == expected
		case ":":
			matched = strings.Contains(actual, expected)
		default:
			return monitoringUnresolvedReference
		}
	}
	if f.op == "!=" {
		matched = !matched
	}
	return monitoringBool(matched)
}
func monitoringFilterReference(filter, check string) monitoringReference {
	expr, ok := parseMonitoringFilter(filter)
	if !ok {
		return monitoringUnresolvedReference
	}
	types := expr.metricTypes()
	if types == nil {
		// A check-id exclusion can still prove absence in an unbounded metric query.
		return monitoringAnd(monitoringUnresolvedReference, expr.match("", check))
	}
	result := monitoringNoReference
	for kind := range types {
		if strings.HasPrefix(kind, "monitoring.googleapis.com/uptime_check/") {
			result = monitoringOr(result, expr.match(kind, check))
		}
	}
	return result
}

func alertPolicyUptimeReference(data map[string]any, check string) monitoringReference {
	result := monitoringNoReference
	for _, raw := range array(data["conditions"]) {
		condition := object(raw)
		for _, key := range []string{"conditionThreshold", "conditionAbsent"} {
			body := object(condition[key])
			for _, field := range []string{"filter", "denominatorFilter"} {
				if value, ok := body[field].(string); ok && value != "" {
					result = monitoringOr(result, monitoringFilterReference(value, check))
				}
			}
		}
		for _, key := range []string{"conditionMatchedLog", "conditionMonitoringQueryLanguage", "conditionPrometheusQueryLanguage", "conditionSql"} {
			if _, ok := condition[key]; ok {
				result = monitoringOr(result, monitoringUnresolvedReference)
			}
		}
	}
	return result
}
