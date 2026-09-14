package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// EvalExpression evaluates a GitHub Actions expression string (without the
// ${{ }} wrapper) against ctx. This Phase-1 subset supports: string/bool/
// number literals, dotted context lookups (env.X, github.X, runner.X,
// steps.<id>.outcome, steps.<id>.outputs.<name>), the operators
// == != && || !, and the status functions success()/failure()/always()/
// cancelled(). Parenthesized grouping is not yet supported — a documented
// gap, not a silent one; expressions needing it return a parse error.
func EvalExpression(expr string, ctx *Context) (interface{}, error) {
	p := &exprParser{input: strings.TrimSpace(expr), ctx: ctx}
	val, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return nil, fmt.Errorf("unexpected trailing input at %d: %q", p.pos, p.input[p.pos:])
	}
	return val, nil
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

var exprPattern = regexp.MustCompile(`\$\{\{(.*?)\}\}`)

// SubstituteExpressions replaces every ${{ ... }} occurrence in s with its
// evaluated, stringified value.
func SubstituteExpressions(s string, ctx *Context) (string, error) {
	var evalErr error
	result := exprPattern.ReplaceAllStringFunc(s, func(match string) string {
		inner := exprPattern.FindStringSubmatch(match)[1]
		val, err := EvalExpression(strings.TrimSpace(inner), ctx)
		if err != nil {
			evalErr = err
			return match
		}
		return fmt.Sprintf("%v", val)
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

type exprParser struct {
	input string
	pos   int
	ctx   *Context
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.input) && p.input[p.pos] == ' ' {
		p.pos++
	}
}

func (p *exprParser) peekOp(op string) bool {
	p.skipSpace()
	return strings.HasPrefix(p.input[p.pos:], op)
}

func (p *exprParser) consumeOp(op string) {
	p.skipSpace()
	p.pos += len(op)
}

func (p *exprParser) parseOr() (interface{}, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peekOp("||") {
		p.consumeOp("||")
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = truthy(left) || truthy(right)
	}
	return left, nil
}

func (p *exprParser) parseAnd() (interface{}, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for p.peekOp("&&") {
		p.consumeOp("&&")
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		left = truthy(left) && truthy(right)
	}
	return left, nil
}

func (p *exprParser) parseEquality() (interface{}, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if p.peekOp("==") {
			p.consumeOp("==")
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = fmt.Sprintf("%v", left) == fmt.Sprintf("%v", right)
			continue
		}
		if p.peekOp("!=") {
			p.consumeOp("!=")
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = fmt.Sprintf("%v", left) != fmt.Sprintf("%v", right)
			continue
		}
		break
	}
	return left, nil
}

func (p *exprParser) parseUnary() (interface{}, error) {
	p.skipSpace()
	if p.pos < len(p.input) && p.input[p.pos] == '!' {
		p.pos++
		val, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return !truthy(val), nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (interface{}, error) {
	p.skipSpace()
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("unexpected end of expression")
	}

	c := p.input[p.pos]

	if c == '\'' {
		end := strings.IndexByte(p.input[p.pos+1:], '\'')
		if end == -1 {
			return nil, fmt.Errorf("unterminated string literal")
		}
		val := p.input[p.pos+1 : p.pos+1+end]
		p.pos = p.pos + 1 + end + 1
		return val, nil
	}

	if strings.HasPrefix(p.input[p.pos:], "true") {
		p.pos += 4
		return true, nil
	}
	if strings.HasPrefix(p.input[p.pos:], "false") {
		p.pos += 5
		return false, nil
	}

	if c >= '0' && c <= '9' || c == '-' {
		start := p.pos
		p.pos++
		for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9' || p.input[p.pos] == '.') {
			p.pos++
		}
		n, err := strconv.ParseFloat(p.input[start:p.pos], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number literal %q: %w", p.input[start:p.pos], err)
		}
		return n, nil
	}

	if isIdentStart(c) {
		start := p.pos
		for p.pos < len(p.input) && isIdentPart(p.input[p.pos]) {
			p.pos++
		}
		ident := p.input[start:p.pos]

		if p.pos < len(p.input) && p.input[p.pos] == '(' {
			p.pos++
			p.skipSpace()
			if p.pos >= len(p.input) || p.input[p.pos] != ')' {
				return nil, fmt.Errorf("status functions take no arguments: %s(...)", ident)
			}
			p.pos++
			return p.ctx.callStatusFunc(ident)
		}

		path := []string{ident}
		for p.pos < len(p.input) && p.input[p.pos] == '.' {
			p.pos++
			start := p.pos
			for p.pos < len(p.input) && isIdentPart(p.input[p.pos]) {
				p.pos++
			}
			path = append(path, p.input[start:p.pos])
		}
		return p.ctx.resolvePath(path)
	}

	return nil, fmt.Errorf("unexpected character %q at position %d", c, p.pos)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}
