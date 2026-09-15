package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"mirror-gha/third_party/ghaexpr"
)

// EvalExpression evaluates a GitHub Actions expression string (without the
// ${{ }} wrapper) against ctx. Parsing is delegated to ghaexpr (a surgical
// extraction of rhysd/actionlint's expression lexer/parser — see
// third_party/ghaexpr/NOTICE.md), so the full GitHub Actions expression
// grammar is supported: literals (including null), dotted/indexed context
// lookups, parenthesized grouping, ==/!=/</<=/>/>=, short-circuiting &&/||
// (returning the operand, not a coerced bool, matching real GitHub Actions
// semantics), unary !, and function calls. This package supplies the
// evaluation semantics — what a variable/property/function resolves to —
// on top of that parsed tree.
//
// Supported functions: success()/failure()/always()/cancelled(), contains(),
// startsWith(), endsWith(), format(), join(), toJSON(), fromJSON(),
// hashFiles() (see hashfiles.go — a real SHA-256-over-matched-files
// implementation, not an approximation, though its glob support is a
// hand-rolled subset of @actions/glob: literal segments/*/**/?, no "!"
// negation or brace expansion).
func EvalExpression(expr string, ctx *Context) (interface{}, error) {
	node, err := parseExpr(expr)
	if err != nil {
		return nil, err
	}
	return evalNode(node, ctx)
}

// EvalBool evaluates an `if:` condition. GitHub Actions treats a bare
// expression the same as one wrapped in ${{ }} for `if:`.
func EvalBool(expr string, ctx *Context) (bool, error) {
	val, err := EvalExpression(unwrap(expr), ctx)
	if err != nil {
		return false, err
	}
	return truthy(val), nil
}

var wrappedExprPattern = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// SubstituteExpressions replaces every ${{ ... }} occurrence in s with its
// evaluated, stringified value.
func SubstituteExpressions(s string, ctx *Context) (string, error) {
	var evalErr error
	result := wrappedExprPattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := wrappedExprPattern.FindStringSubmatch(match)[1]
		val, err := EvalExpression(strings.TrimSpace(inner), ctx)
		if err != nil {
			evalErr = err
			return match
		}
		return toStr(val)
	})
	if evalErr != nil {
		return "", evalErr
	}
	return result, nil
}

func unwrap(expr string) string {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "${{") && strings.HasSuffix(expr, "}}") {
		return strings.TrimSpace(expr[3 : len(expr)-2])
	}
	return expr
}

// parseExpr parses expr (bare, no ${{ }} wrapper) into ghaexpr's AST. The
// lexer requires a trailing "}}" as its end-of-input marker — that's how
// it's designed to lex the interior of a ${{ }} block — so it's added back
// here rather than exposed as part of this package's public contract.
func parseExpr(expr string) (ghaexpr.ExprNode, error) {
	src := strings.TrimSpace(expr) + "}}"
	node, err := ghaexpr.NewExprParser().Parse(ghaexpr.NewExprLexer(src))
	if err != nil {
		return nil, fmt.Errorf("parse expression %q: %w", expr, err)
	}
	return node, nil
}

func truthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case nil:
		return false
	default:
		return true
	}
}

func toStr(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}

func toNumber(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func evalNode(node ghaexpr.ExprNode, ctx *Context) (interface{}, error) {
	switch n := node.(type) {
	case *ghaexpr.NullNode:
		return nil, nil
	case *ghaexpr.BoolNode:
		return n.Value, nil
	case *ghaexpr.IntNode:
		return float64(n.Value), nil
	case *ghaexpr.FloatNode:
		return n.Value, nil
	case *ghaexpr.StringNode:
		return n.Value, nil
	case *ghaexpr.VariableNode, *ghaexpr.ObjectDerefNode, *ghaexpr.IndexAccessNode:
		path, err := pathFromNode(node)
		if err != nil {
			return nil, err
		}
		return ctx.resolvePath(path)
	case *ghaexpr.ArrayDerefNode:
		return nil, fmt.Errorf("array dereference ('.*') is not supported yet")
	case *ghaexpr.NotOpNode:
		val, err := evalNode(n.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return !truthy(val), nil
	case *ghaexpr.CompareOpNode:
		left, err := evalNode(n.Left, ctx)
		if err != nil {
			return nil, err
		}
		right, err := evalNode(n.Right, ctx)
		if err != nil {
			return nil, err
		}
		return compareValues(n.Kind, left, right)
	case *ghaexpr.LogicalOpNode:
		left, err := evalNode(n.Left, ctx)
		if err != nil {
			return nil, err
		}
		switch n.Kind {
		case ghaexpr.LogicalOpNodeKindAnd:
			if !truthy(left) {
				return left, nil
			}
			return evalNode(n.Right, ctx)
		case ghaexpr.LogicalOpNodeKindOr:
			if truthy(left) {
				return left, nil
			}
			return evalNode(n.Right, ctx)
		default:
			return nil, fmt.Errorf("unknown logical operator")
		}
	case *ghaexpr.FuncCallNode:
		args := make([]interface{}, len(n.Args))
		for i, a := range n.Args {
			val, err := evalNode(a, ctx)
			if err != nil {
				return nil, err
			}
			args[i] = val
		}
		return callFunction(n.Callee, args, ctx)
	default:
		return nil, fmt.Errorf("unsupported expression node %T", node)
	}
}

// pathFromNode unwraps a chain of ObjectDerefNode/IndexAccessNode (string
// literal index only) down to a base VariableNode, producing the same
// []string path Context.resolvePath expects (e.g. `steps.emit.outputs.x`
// -> ["steps", "emit", "outputs", "x"]). Context names are matched
// case-insensitively per the GitHub Actions expression language; property
// names are not, since env/output keys are.
func pathFromNode(node ghaexpr.ExprNode) ([]string, error) {
	switch n := node.(type) {
	case *ghaexpr.VariableNode:
		return []string{strings.ToLower(n.Name)}, nil
	case *ghaexpr.ObjectDerefNode:
		base, err := pathFromNode(n.Receiver)
		if err != nil {
			return nil, err
		}
		return append(base, n.Property), nil
	case *ghaexpr.IndexAccessNode:
		base, err := pathFromNode(n.Operand)
		if err != nil {
			return nil, err
		}
		idx, ok := n.Index.(*ghaexpr.StringNode)
		if !ok {
			return nil, fmt.Errorf("only string-literal index access (e.g. foo['bar']) is supported")
		}
		return append(base, idx.Value), nil
	default:
		return nil, fmt.Errorf("unsupported context path expression")
	}
}

func compareValues(kind ghaexpr.CompareOpNodeKind, left, right interface{}) (interface{}, error) {
	if kind.IsEqualityOp() {
		eq := toStr(left) == toStr(right)
		if kind == ghaexpr.CompareOpNodeKindNotEq {
			return !eq, nil
		}
		return eq, nil
	}

	lf, lok := toNumber(left)
	rf, rok := toNumber(right)
	if !lok || !rok {
		return nil, fmt.Errorf("operator %s requires numeric operands", kind)
	}
	switch kind {
	case ghaexpr.CompareOpNodeKindLess:
		return lf < rf, nil
	case ghaexpr.CompareOpNodeKindLessEq:
		return lf <= rf, nil
	case ghaexpr.CompareOpNodeKindGreater:
		return lf > rf, nil
	case ghaexpr.CompareOpNodeKindGreaterEq:
		return lf >= rf, nil
	default:
		return nil, fmt.Errorf("unknown compare operator")
	}
}

func callFunction(name string, args []interface{}, ctx *Context) (interface{}, error) {
	switch strings.ToLower(name) {
	case "success", "failure", "always", "cancelled":
		if len(args) != 0 {
			return nil, fmt.Errorf("%s() takes no arguments", name)
		}
		return ctx.callStatusFunc(strings.ToLower(name))
	case "contains":
		if len(args) != 2 {
			return nil, fmt.Errorf("contains() takes 2 arguments")
		}
		return strings.Contains(toStr(args[0]), toStr(args[1])), nil
	case "startswith":
		if len(args) != 2 {
			return nil, fmt.Errorf("startsWith() takes 2 arguments")
		}
		return strings.HasPrefix(toStr(args[0]), toStr(args[1])), nil
	case "endswith":
		if len(args) != 2 {
			return nil, fmt.Errorf("endsWith() takes 2 arguments")
		}
		return strings.HasSuffix(toStr(args[0]), toStr(args[1])), nil
	case "format":
		if len(args) == 0 {
			return nil, fmt.Errorf("format() takes at least 1 argument")
		}
		return formatGHA(toStr(args[0]), args[1:]), nil
	case "join":
		if len(args) < 1 || len(args) > 2 {
			return nil, fmt.Errorf("join() takes 1 or 2 arguments")
		}
		sep := ", "
		if len(args) == 2 {
			sep = toStr(args[1])
		}
		return joinValue(args[0], sep), nil
	case "tojson":
		if len(args) != 1 {
			return nil, fmt.Errorf("toJSON() takes 1 argument")
		}
		b, err := json.Marshal(args[0])
		if err != nil {
			return nil, fmt.Errorf("toJSON: %w", err)
		}
		return string(b), nil
	case "fromjson":
		if len(args) != 1 {
			return nil, fmt.Errorf("fromJSON() takes 1 argument")
		}
		var v interface{}
		if err := json.Unmarshal([]byte(toStr(args[0])), &v); err != nil {
			return nil, fmt.Errorf("fromJSON: %w", err)
		}
		return v, nil
	case "hashfiles":
		if len(args) == 0 {
			return nil, fmt.Errorf("hashFiles() takes at least 1 argument")
		}
		workspace, _ := ctx.GitHub["workspace"].(string)
		if workspace == "" {
			return nil, fmt.Errorf("hashFiles() requires github.workspace to be set")
		}
		patterns := make([]string, len(args))
		for i, a := range args {
			patterns[i] = toStr(a)
		}
		return hashFiles(workspace, patterns)
	default:
		return nil, fmt.Errorf("unsupported function %q", name)
	}
}

func formatGHA(tmpl string, args []interface{}) string {
	result := tmpl
	for i, a := range args {
		result = strings.ReplaceAll(result, fmt.Sprintf("{%d}", i), toStr(a))
	}
	return result
}

func joinValue(v interface{}, sep string) string {
	items, ok := v.([]interface{})
	if !ok {
		return toStr(v)
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = toStr(item)
	}
	return strings.Join(parts, sep)
}
