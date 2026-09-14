package gcp

import (
	"slices"
	"strings"

	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
)

// Parse native CEL without evaluating it or expanding macros. Looking at calls
// rather than text avoids invented dependencies in comments and string literals.
func routePolicySetReferences(data map[string]any) ([]string, error) {
	if err := routePolicyData(data, text(data["name"])); err != nil {
		return nil, err
	}
	p, err := parser.NewParser()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, value := range array(data["terms"]) {
		term := object(value)
		expressions := append([]any{term["match"]}, array(term["actions"])...)
		for _, value := range expressions {
			expression := text(object(value)["expression"])
			if strings.TrimSpace(expression) == "" {
				continue
			}
			parsed, issues := p.Parse(common.NewTextSource(expression))
			if len(issues.GetErrors()) != 0 || parsed == nil || ast.ExceedsDepth(parsed, 250) {
				return nil, groupDenied("route_policy_cel_invalid")
			}
			invalid := false
			ast.PostOrderVisit(parsed.Expr(), ast.NewExprVisitor(func(node ast.Expr) {
				if node.Kind() != ast.CallKind {
					return
				}
				call := node.AsCall()
				function := strings.TrimPrefix(call.FunctionName(), ".")
				if function != "prefixSets" && function != "communitySets" {
					return
				}
				args := call.Args()
				if call.IsMemberFunction() || len(args) != 1 || args[0].Kind() != ast.LiteralKind {
					invalid = true
					return
				}
				name, ok := args[0].AsLiteral().Value().(string)
				if !ok || !routePolicySegment.MatchString(name) {
					invalid = true
					return
				}
				names[name] = true
			}))
			// A non-literal target cannot safely authorize a concrete dependency. Keep
			// the scan incomplete instead of silently dropping the unknown reference.
			if invalid {
				return nil, groupDenied("route_policy_named_set_reference_unresolved")
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}
