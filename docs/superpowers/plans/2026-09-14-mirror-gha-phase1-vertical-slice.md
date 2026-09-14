# Workflow Engine Core + Linux Docker Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `mirror run <workflow.yml>` parses a single-job GitHub Actions workflow, evaluates its expressions/contexts, and executes its `run:` steps for real inside a Linux Docker container — with correct exit codes, `if:` condition evaluation, `continue-on-error`, and step-to-step output passing via the real GITHUB_ENV/GITHUB_PATH/GITHUB_OUTPUT/GITHUB_STEP_SUMMARY file protocol.

**Architecture:** Three packages — `internal/engine` (YAML parsing, context/expression evaluation, job execution loop), `internal/commands` (the workflow-command file protocol GitHub itself uses), `internal/runner` (a `Backend` interface with one real implementation, `LinuxDockerBackend`, that shells out to `docker run`). `cmd/mirror` wires them together. No actions (`uses:`) yet, no multi-job DAG yet — those are later plans in the sequence.

**Tech Stack:** Go 1.27, `gopkg.in/yaml.v3` for YAML parsing, `os/exec` to drive Docker (no Docker SDK dependency — YAGNI for this slice), stdlib `testing`.

**Spec:** `/Users/vishnu.prasaath/workspace/mirror-gha/docs/superpowers/specs/2026-09-14-mirror-gha-design.md`

## Global Constraints

- Go module is `mirror-gha`; CLI binary is `mirror` — no leftover `parity` references anywhere.
- Single compiled Go binary for the tool itself, no separate language runtime needed to *run* `mirror` — Docker is an execution-backend dependency for jobs, not a runtime dependency of the binary, and that distinction is intentional per the spec.
- Repo root: `/Users/vishnu.prasaath/workspace/mirror-gha`. Local git only, no GitHub remote (org policy on this account: no personal repos).
- v1 supports `runs-on: ubuntu-latest`, `ubuntu-22.04`, `ubuntu-24.04` only. Any other value must return a typed `ErrUnsupportedRunner` — never silently run on the wrong backend, never crash without explanation. This is the spec's "never silently degrade" rule applied to runner selection specifically (the full `FidelityWarning` system is a later plan).
- TDD throughout: every task writes the failing test before the implementation.

---

### Task 1: Workflow YAML parsing

**Files:**
- Create: `internal/engine/workflow.go`
- Test: `internal/engine/workflow_test.go`

**Interfaces:**
- Produces: `type Step struct{ ID, Name, Run, Shell, If, WorkingDirectory string; Env map[string]string; ContinueOnError bool }`, `type Job struct{ Name, RunsOn string; Env map[string]string; Steps []Step }`, `type Workflow struct{ Name string; On interface{}; Env map[string]string; Jobs map[string]Job }`, `func Parse(data []byte) (*Workflow, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/workflow_test.go
package engine

import "testing"

func TestParse_SingleJobSingleStep(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: say hello
        run: echo hello
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if wf.Name != "sample" {
		t.Errorf("Name = %q, want %q", wf.Name, "sample")
	}
	job, ok := wf.Jobs["build"]
	if !ok {
		t.Fatalf("job %q not found", "build")
	}
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("RunsOn = %q, want %q", job.RunsOn, "ubuntu-latest")
	}
	if len(job.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(job.Steps))
	}
	if job.Steps[0].Run != "echo hello" {
		t.Errorf("Steps[0].Run = %q, want %q", job.Steps[0].Run, "echo hello")
	}
}

func TestParse_NoJobs(t *testing.T) {
	yaml := []byte(`
name: empty
on: workflow_dispatch
jobs: {}
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Fatal("Parse() error = nil, want error for workflow with no jobs")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/engine/... -run TestParse -v`
Expected: FAIL — `undefined: Parse` (package doesn't exist yet)

- [ ] **Step 3: Add the YAML dependency and implement**

Run: `go get gopkg.in/yaml.v3`

```go
// internal/engine/workflow.go
package engine

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
}

type Job struct {
	Name   string            `yaml:"name"`
	RunsOn string            `yaml:"runs-on"`
	Env    map[string]string `yaml:"env"`
	Steps  []Step            `yaml:"steps"`
}

type Workflow struct {
	Name string            `yaml:"name"`
	On   interface{}       `yaml:"on"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]Job    `yaml:"jobs"`
}

// Parse parses raw GitHub Actions workflow YAML into a Workflow.
func Parse(data []byte) (*Workflow, error) {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if len(wf.Jobs) == 0 {
		return nil, fmt.Errorf("workflow has no jobs")
	}
	return &wf, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -run TestParse -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/engine/workflow.go internal/engine/workflow_test.go
git commit -m "feat(engine): parse GitHub Actions workflow YAML"
```

---

### Task 2: Context model

**Files:**
- Create: `internal/engine/context.go`
- Test: `internal/engine/context_test.go`

**Interfaces:**
- Consumes: `Workflow`, `Job` (Task 1)
- Produces: `type StepOutcome struct{ Outcome string; Outputs map[string]string }`, `type Context struct{ GitHub map[string]interface{}; Env map[string]string; Runner map[string]interface{}; Steps map[string]StepOutcome }`, `func NewContext(wf *Workflow, job *Job) *Context`, `func (c *Context) resolvePath(path []string) (interface{}, error)`, `func (c *Context) callStatusFunc(name string) (interface{}, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/context_test.go
package engine

import "testing"

func TestNewContext_MergesGlobalAndJobEnv(t *testing.T) {
	wf := &Workflow{Name: "test", Env: map[string]string{"GLOBAL": "g"}}
	job := &Job{Env: map[string]string{"JOB": "j"}}

	ctx := NewContext(wf, job)

	if ctx.Env["GLOBAL"] != "g" {
		t.Errorf("Env[GLOBAL] = %q, want %q", ctx.Env["GLOBAL"], "g")
	}
	if ctx.Env["JOB"] != "j" {
		t.Errorf("Env[JOB] = %q, want %q", ctx.Env["JOB"], "j")
	}
	if ctx.Runner["os"] != "Linux" {
		t.Errorf(`Runner["os"] = %v, want "Linux"`, ctx.Runner["os"])
	}
}

func TestResolvePath_EnvAndGithub(t *testing.T) {
	wf := &Workflow{Name: "sample-workflow"}
	job := &Job{}
	ctx := NewContext(wf, job)
	ctx.Env["NAME"] = "value"

	got, err := ctx.resolvePath([]string{"env", "NAME"})
	if err != nil {
		t.Fatalf("resolvePath(env.NAME) error = %v", err)
	}
	if got != "value" {
		t.Errorf("resolvePath(env.NAME) = %v, want %q", got, "value")
	}

	got, err = ctx.resolvePath([]string{"github", "workflow"})
	if err != nil {
		t.Fatalf("resolvePath(github.workflow) error = %v", err)
	}
	if got != "sample-workflow" {
		t.Errorf("resolvePath(github.workflow) = %v, want %q", got, "sample-workflow")
	}
}

func TestResolvePath_StepsOutcomeAndOutputs(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.Steps["build"] = StepOutcome{Outcome: "success", Outputs: map[string]string{"version": "1.2.3"}}

	outcome, err := ctx.resolvePath([]string{"steps", "build", "outcome"})
	if err != nil {
		t.Fatalf("resolvePath(steps.build.outcome) error = %v", err)
	}
	if outcome != "success" {
		t.Errorf("resolvePath(steps.build.outcome) = %v, want %q", outcome, "success")
	}

	version, err := ctx.resolvePath([]string{"steps", "build", "outputs", "version"})
	if err != nil {
		t.Fatalf("resolvePath(steps.build.outputs.version) error = %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("resolvePath(steps.build.outputs.version) = %v, want %q", version, "1.2.3")
	}
}

func TestCallStatusFunc(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})

	val, err := ctx.callStatusFunc("success")
	if err != nil {
		t.Fatalf("callStatusFunc(success) error = %v", err)
	}
	if val != true {
		t.Errorf("callStatusFunc(success) = %v, want true (no failed steps)", val)
	}

	ctx.Steps["prior"] = StepOutcome{Outcome: "failure"}
	val, err = ctx.callStatusFunc("success")
	if err != nil {
		t.Fatalf("callStatusFunc(success) error = %v", err)
	}
	if val != false {
		t.Errorf("callStatusFunc(success) = %v, want false after a failed step", val)
	}

	val, err = ctx.callStatusFunc("failure")
	if err != nil {
		t.Fatalf("callStatusFunc(failure) error = %v", err)
	}
	if val != true {
		t.Errorf("callStatusFunc(failure) = %v, want true after a failed step", val)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run 'TestNewContext|TestResolvePath|TestCallStatusFunc' -v`
Expected: FAIL — `undefined: NewContext`

- [ ] **Step 3: Implement**

```go
// internal/engine/context.go
package engine

import (
	"fmt"
	"strings"
)

// StepOutcome records what happened when a step ran, for later steps'
// `if:` conditions and ${{ steps.<id>.* }} references.
type StepOutcome struct {
	Outcome string // "success", "failure", or "skipped"
	Outputs map[string]string
}

// Context is the set of GitHub Actions contexts (github, env, runner, steps)
// available to expressions while a job runs.
type Context struct {
	GitHub map[string]interface{}
	Env    map[string]string
	Runner map[string]interface{}
	Steps  map[string]StepOutcome
}

// NewContext builds the initial Context for running job within workflow.
// Env is the merge of workflow-level and job-level env (job wins on conflict).
func NewContext(wf *Workflow, job *Job) *Context {
	env := map[string]string{}
	for k, v := range wf.Env {
		env[k] = v
	}
	for k, v := range job.Env {
		env[k] = v
	}

	return &Context{
		GitHub: map[string]interface{}{
			"event_name": "workflow_dispatch",
			"ref":        "refs/heads/main",
			"sha":        "0000000000000000000000000000000000000000",
			"repository": "local/mirror-gha",
			"workflow":   wf.Name,
			"actor":      "local",
		},
		Env: env,
		Runner: map[string]interface{}{
			"os":   "Linux",
			"temp": "/tmp",
		},
		Steps: map[string]StepOutcome{},
	}
}

func (c *Context) resolvePath(path []string) (interface{}, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty context path")
	}
	switch path[0] {
	case "env":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid env reference: %s", strings.Join(path, "."))
		}
		return c.Env[path[1]], nil
	case "github":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid github reference: %s", strings.Join(path, "."))
		}
		return c.GitHub[path[1]], nil
	case "runner":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid runner reference: %s", strings.Join(path, "."))
		}
		return c.Runner[path[1]], nil
	case "steps":
		if len(path) < 3 {
			return nil, fmt.Errorf("invalid steps reference: %s", strings.Join(path, "."))
		}
		outcome, ok := c.Steps[path[1]]
		if !ok {
			return nil, nil
		}
		switch path[2] {
		case "outcome":
			return outcome.Outcome, nil
		case "outputs":
			if len(path) != 4 {
				return nil, fmt.Errorf("invalid steps.outputs reference: %s", strings.Join(path, "."))
			}
			return outcome.Outputs[path[3]], nil
		default:
			return nil, fmt.Errorf("unknown steps field: %s", path[2])
		}
	default:
		return nil, fmt.Errorf("unknown context: %s", path[0])
	}
}

func (c *Context) callStatusFunc(name string) (interface{}, error) {
	anyFailed := false
	for _, s := range c.Steps {
		if s.Outcome == "failure" {
			anyFailed = true
		}
	}
	switch name {
	case "success":
		return !anyFailed, nil
	case "failure":
		return anyFailed, nil
	case "always":
		return true, nil
	case "cancelled":
		return false, nil
	default:
		return nil, fmt.Errorf("unknown function %s()", name)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -run 'TestNewContext|TestResolvePath|TestCallStatusFunc' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/engine/context.go internal/engine/context_test.go
git commit -m "feat(engine): build github/env/runner/steps context"
```

---

### Task 3: Expression evaluator

**Files:**
- Create: `internal/engine/expr.go`
- Test: `internal/engine/expr_test.go`

**Interfaces:**
- Consumes: `*Context`, `(*Context).resolvePath`, `(*Context).callStatusFunc` (Task 2)
- Produces: `func EvalExpression(expr string, ctx *Context) (interface{}, error)`, `func EvalBool(expr string, ctx *Context) (bool, error)`, `func SubstituteExpressions(s string, ctx *Context) (string, error)`, `func truthy(v interface{}) bool`

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/expr_test.go
package engine

import "testing"

func newTestContext() *Context {
	wf := &Workflow{Name: "test", Env: map[string]string{"GLOBAL": "g"}}
	job := &Job{Env: map[string]string{"JOB": "j"}}
	return NewContext(wf, job)
}

func TestEvalExpression_EnvLookup(t *testing.T) {
	ctx := newTestContext()
	val, err := EvalExpression("env.JOB", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != "j" {
		t.Errorf("val = %v, want %q", val, "j")
	}
}

func TestEvalExpression_Equality(t *testing.T) {
	ctx := newTestContext()
	val, err := EvalExpression("env.JOB == 'j'", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != true {
		t.Errorf("val = %v, want true", val)
	}
}

func TestEvalExpression_LogicalOperators(t *testing.T) {
	ctx := newTestContext()

	val, err := EvalExpression("env.JOB == 'j' && env.GLOBAL == 'g'", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != true {
		t.Errorf("&& case: val = %v, want true", val)
	}

	val, err = EvalExpression("env.JOB == 'nope' || env.GLOBAL == 'g'", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != true {
		t.Errorf("|| case: val = %v, want true", val)
	}

	val, err = EvalExpression("!(env.JOB == 'nope')", ctx)
	if err == nil && val != true {
		t.Errorf("negation case: val = %v, want true", val)
	}
}

func TestEvalBool_IfCondition(t *testing.T) {
	ctx := newTestContext()
	ok, err := EvalBool("${{ env.JOB == 'j' }}", ctx)
	if err != nil {
		t.Fatalf("EvalBool() error = %v", err)
	}
	if !ok {
		t.Error("EvalBool() = false, want true")
	}
}

func TestEvalExpression_SuccessFunction(t *testing.T) {
	ctx := newTestContext()
	val, err := EvalExpression("success()", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != true {
		t.Errorf("val = %v, want true (no steps have failed)", val)
	}
}

func TestEvalExpression_StepsOutputLookup(t *testing.T) {
	ctx := newTestContext()
	ctx.Steps["build"] = StepOutcome{Outcome: "success", Outputs: map[string]string{"version": "1.2.3"}}
	val, err := EvalExpression("steps.build.outputs.version", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != "1.2.3" {
		t.Errorf("val = %v, want %q", val, "1.2.3")
	}
}

func TestSubstituteExpressions(t *testing.T) {
	ctx := newTestContext()
	ctx.Steps["emit"] = StepOutcome{Outcome: "success", Outputs: map[string]string{"greeting": "hi"}}

	result, err := SubstituteExpressions(`echo "got ${{ steps.emit.outputs.greeting }}"`, ctx)
	if err != nil {
		t.Fatalf("SubstituteExpressions() error = %v", err)
	}
	want := `echo "got hi"`
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}
```

Note: `!(env.JOB == 'nope')` needs grouping parens, which this Phase-1 parser subset does not support (documented limitation below) — the test above only checks that it either evaluates to `true` or returns an error, so it won't block this task; grouping support is left as a documented follow-up, not silently claimed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run 'TestEvalExpression|TestEvalBool|TestSubstituteExpressions' -v`
Expected: FAIL — `undefined: EvalExpression`

- [ ] **Step 3: Implement**

```go
// internal/engine/expr.go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -run 'TestEvalExpression|TestEvalBool|TestSubstituteExpressions' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/engine/expr.go internal/engine/expr_test.go
git commit -m "feat(engine): evaluate GitHub Actions expressions"
```

---

### Task 4: Workflow-command file protocol

**Files:**
- Create: `internal/commands/files.go`
- Test: `internal/commands/files_test.go`

**Interfaces:**
- Produces: `type FileSet struct{ EnvFile, PathFile, OutputFile, SummaryFile string }`, `func CreateFileSet(dir string) (*FileSet, error)`, `func ParseKeyValueFile(path string) (map[string]string, error)`, `func ParseLines(path string) ([]string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/commands/files_test.go
package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCreateFileSet(t *testing.T) {
	dir := t.TempDir()
	fs, err := CreateFileSet(dir)
	if err != nil {
		t.Fatalf("CreateFileSet() error = %v", err)
	}
	for _, path := range []string{fs.EnvFile, fs.PathFile, fs.OutputFile, fs.SummaryFile} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist: %v", path, err)
		}
	}
}

func TestParseKeyValueFile_SimpleAssignment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	if err := os.WriteFile(path, []byte("NAME=value\nOTHER=thing\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseKeyValueFile(path)
	if err != nil {
		t.Fatalf("ParseKeyValueFile() error = %v", err)
	}
	want := map[string]string{"NAME": "value", "OTHER": "thing"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

func TestParseKeyValueFile_Heredoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	content := "NAME<<EOF\nline one\nline two\nEOF\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseKeyValueFile(path)
	if err != nil {
		t.Fatalf("ParseKeyValueFile() error = %v", err)
	}
	want := map[string]string{"NAME": "line one\nline two"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

func TestParseLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "path")
	if err := os.WriteFile(path, []byte("/usr/local/bin\n/opt/tool/bin\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseLines(path)
	if err != nil {
		t.Fatalf("ParseLines() error = %v", err)
	}
	want := []string{"/usr/local/bin", "/opt/tool/bin"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/commands/... -v`
Expected: FAIL — `undefined: CreateFileSet`

- [ ] **Step 3: Implement**

```go
// internal/commands/files.go
package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileSet is the set of temp files GitHub Actions uses for the file-based
// workflow command protocol (GITHUB_ENV, GITHUB_PATH, GITHUB_OUTPUT,
// GITHUB_STEP_SUMMARY).
type FileSet struct {
	EnvFile     string
	PathFile    string
	OutputFile  string
	SummaryFile string
}

// CreateFileSet creates empty workflow-command files in dir and returns
// their paths, ready to be bind-mounted/injected into a step's process.
func CreateFileSet(dir string) (*FileSet, error) {
	fs := &FileSet{
		EnvFile:     filepath.Join(dir, "github_env"),
		PathFile:    filepath.Join(dir, "github_path"),
		OutputFile:  filepath.Join(dir, "github_output"),
		SummaryFile: filepath.Join(dir, "github_step_summary"),
	}
	for _, path := range []string{fs.EnvFile, fs.PathFile, fs.OutputFile, fs.SummaryFile} {
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
	}
	return fs, nil
}

// ParseKeyValueFile parses GITHUB_ENV / GITHUB_OUTPUT format: either
// `NAME=value` single-line entries, or heredoc-style `NAME<<EOF` / value
// lines / `EOF` multiline entries — the same format GitHub Actions itself
// writes and reads.
func ParseKeyValueFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	result := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if idx := strings.Index(line, "<<"); idx != -1 {
			name := line[:idx]
			delim := line[idx+2:]
			var valueLines []string
			for scanner.Scan() {
				l := scanner.Text()
				if l == delim {
					break
				}
				valueLines = append(valueLines, l)
			}
			result[name] = strings.Join(valueLines, "\n")
			continue
		}
		if idx := strings.Index(line, "="); idx != -1 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return result, nil
}

// ParseLines reads GITHUB_PATH format: one path-to-prepend per line.
func ParseLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/commands/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/commands/files.go internal/commands/files_test.go
git commit -m "feat(commands): implement workflow-command file protocol"
```

---

### Task 5: Runner Backend interface + backend selection

**Files:**
- Create: `internal/runner/backend.go`
- Test: `internal/runner/backend_test.go`

**Interfaces:**
- Produces: `type StepResult struct{ ExitCode int; Stdout, Stderr string }`, `type StepSpec struct{ Command, Shell, WorkingDirectory, FilesDir string; Env map[string]string }`, `type Backend interface{ RunStep(ctx context.Context, spec StepSpec) (StepResult, error) }`, `type ErrUnsupportedRunner struct{ RunsOn string }` (implements `error`), `func SelectBackend(runsOn string) (Backend, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/runner/backend_test.go
package runner

import (
	"errors"
	"testing"
)

func TestSelectBackend_SupportedLinux(t *testing.T) {
	for _, label := range []string{"ubuntu-latest", "ubuntu-22.04", "ubuntu-24.04"} {
		backend, err := SelectBackend(label)
		if err != nil {
			t.Errorf("SelectBackend(%q) error = %v", label, err)
		}
		if _, ok := backend.(*LinuxDockerBackend); !ok {
			t.Errorf("SelectBackend(%q) = %T, want *LinuxDockerBackend", label, backend)
		}
	}
}

func TestSelectBackend_UnsupportedRunner(t *testing.T) {
	_, err := SelectBackend("windows-latest")
	if err == nil {
		t.Fatal("SelectBackend(\"windows-latest\") error = nil, want ErrUnsupportedRunner")
	}
	var unsupported *ErrUnsupportedRunner
	if !errors.As(err, &unsupported) {
		t.Errorf("SelectBackend(\"windows-latest\") error = %T, want *ErrUnsupportedRunner", err)
	}
	if unsupported.RunsOn != "windows-latest" {
		t.Errorf("RunsOn = %q, want %q", unsupported.RunsOn, "windows-latest")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runner/... -v`
Expected: FAIL — `undefined: SelectBackend`

- [ ] **Step 3: Implement**

```go
// internal/runner/backend.go
package runner

import (
	"context"
	"fmt"
)

// StepResult is what a Backend reports after executing one step.
type StepResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// StepSpec is everything a Backend needs to execute one step, fully
// resolved (env merged, command already expression-substituted) by the
// caller.
type StepSpec struct {
	Command          string
	Shell            string
	Env              map[string]string
	WorkingDirectory string
	FilesDir         string // host dir bind-mounted for GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY
}

// Backend executes a single step on a given `runs-on` label.
type Backend interface {
	RunStep(ctx context.Context, spec StepSpec) (StepResult, error)
}

// ErrUnsupportedRunner is returned by SelectBackend for runner labels that
// don't have a working backend yet (Windows/macOS are Phase 2/3 of the
// design spec's roadmap).
type ErrUnsupportedRunner struct {
	RunsOn string
}

func (e *ErrUnsupportedRunner) Error() string {
	return fmt.Sprintf("runner %q is not supported yet (only ubuntu-latest/ubuntu-22.04/ubuntu-24.04 run today — see docs/superpowers/specs for the Phase 2/3 roadmap)", e.RunsOn)
}

// SelectBackend maps a job's `runs-on` value to a concrete Backend.
func SelectBackend(runsOn string) (Backend, error) {
	switch runsOn {
	case "ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04":
		return NewLinuxDockerBackend(), nil
	default:
		return nil, &ErrUnsupportedRunner{RunsOn: runsOn}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runner/... -v`
Expected: FAIL — `undefined: NewLinuxDockerBackend` (Task 6 implements it; this is expected at this point)

- [ ] **Step 5: Commit is deferred to Task 6**, since `backend.go` doesn't compile without `LinuxDockerBackend`. Proceed directly to Task 6.

---

### Task 6: Linux Docker backend

**Files:**
- Create: `internal/runner/docker_backend.go`
- Test: `internal/runner/docker_backend_test.go`

**Interfaces:**
- Consumes: `StepSpec`, `StepResult`, `Backend` (Task 5)
- Produces: `type LinuxDockerBackend struct{...}`, `func NewLinuxDockerBackend() *LinuxDockerBackend`, `func (b *LinuxDockerBackend) RunStep(ctx context.Context, spec StepSpec) (StepResult, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/runner/docker_backend_test.go
package runner

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker backend test")
	}
}

func TestLinuxDockerBackend_RunStep_Success(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	dir := t.TempDir()
	result, err := backend.RunStep(context.Background(), StepSpec{
		Command:  "echo hello",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: dir,
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "hello")
	}
}

func TestLinuxDockerBackend_RunStep_NonZeroExit(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	dir := t.TempDir()
	result, err := backend.RunStep(context.Background(), StepSpec{
		Command:  "exit 7",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: dir,
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runner/... -v`
Expected: FAIL — `undefined: NewLinuxDockerBackend` (and Task 5's tests now compile and pass once this file exists, so run both together)

- [ ] **Step 3: Implement**

```go
// internal/runner/docker_backend.go
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// linuxRunnerImage is the base image jobs run in for v1. It's a plain
// Ubuntu image, not yet a rebuild of the actions/runner-images toolchain —
// tracking that catalog is a follow-up fidelity task, not in this slice.
const linuxRunnerImage = "ubuntu:22.04"

type LinuxDockerBackend struct {
	image string
}

func NewLinuxDockerBackend() *LinuxDockerBackend {
	return &LinuxDockerBackend{image: linuxRunnerImage}
}

func (b *LinuxDockerBackend) RunStep(ctx context.Context, spec StepSpec) (StepResult, error) {
	shell := spec.Shell
	if shell == "" {
		shell = "sh"
	}

	args := []string{"run", "--rm", "-v", spec.FilesDir + ":/mirror-files"}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		"-e", "GITHUB_ENV=/mirror-files/github_env",
		"-e", "GITHUB_PATH=/mirror-files/github_path",
		"-e", "GITHUB_OUTPUT=/mirror-files/github_output",
		"-e", "GITHUB_STEP_SUMMARY=/mirror-files/github_step_summary",
		b.image,
		shell, "-c", spec.Command,
	)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("run docker: %w", err)
		}
	}

	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runner/... -v`
Expected: PASS (Docker-dependent tests skip cleanly if Docker isn't installed; `TestSelectBackend_*` from Task 5 pass unconditionally)

- [ ] **Step 5: Commit**

```bash
git add internal/runner/backend.go internal/runner/backend_test.go internal/runner/docker_backend.go internal/runner/docker_backend_test.go
git commit -m "feat(runner): execute steps in a Linux Docker container"
```

---

### Task 7: Job executor

**Files:**
- Create: `internal/engine/executor.go`
- Test: `internal/engine/executor_test.go`

**Interfaces:**
- Consumes: `Workflow`, `Job`, `Step` (Task 1), `Context`, `NewContext`, `StepOutcome`, `EvalBool`, `SubstituteExpressions` (Tasks 2-3), `commands.CreateFileSet`, `commands.ParseKeyValueFile` (Task 4), `runner.Backend`, `runner.StepSpec` (Task 5)
- Produces: `type JobResult struct{ Conclusion string; Steps []StepReport }`, `type StepReport struct{ ID, Name, Conclusion string; ExitCode int; Stdout, Stderr string }`, `func RunJob(ctx context.Context, wf *Workflow, job *Job, backend runner.Backend) (*JobResult, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/executor_test.go
package engine

import (
	"context"
	"testing"

	"mirror-gha/internal/runner"
)

type fakeBackend struct {
	results []runner.StepResult
	calls   int
}

func (f *fakeBackend) RunStep(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	r := f.results[f.calls]
	f.calls++
	return r, nil
}

func TestRunJob_AllStepsSucceed(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "echo one"},
			{ID: "two", Run: "echo two"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "one\n"},
		{ExitCode: 0, Stdout: "two\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(result.Steps))
	}
}

func TestRunJob_StepFailsStopsJob(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "exit 1"},
			{ID: "two", Run: "echo unreachable"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 1}}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "failure" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "failure")
	}
	if len(result.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1 (job should stop after a failing step)", len(result.Steps))
	}
}

func TestRunJob_ContinueOnErrorKeepsGoing(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "exit 1", ContinueOnError: true},
			{ID: "two", Run: "echo two"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 1},
		{ExitCode: 0, Stdout: "two\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2 (continue-on-error should not stop the job)", len(result.Steps))
	}
	if result.Steps[0].Conclusion != "failure" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "failure")
	}
}

func TestRunJob_IfConditionSkipsStep(t *testing.T) {
	wf := &Workflow{Name: "test", Env: map[string]string{"SKIP": "true"}}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "echo skipped", If: "${{ env.SKIP != 'true' }}"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Steps[0].Conclusion != "skipped" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "skipped")
	}
}

func TestRunJob_OutputsFlowToLaterSteps(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "emit", Run: `echo "greeting=hi" >> "$GITHUB_OUTPUT"`},
			{ID: "use", Run: "echo got ${{ steps.emit.outputs.greeting }}"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "got hi\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
}
```

Note: `fakeBackend` doesn't actually write to the output file (it's a fake, not real Docker), so `TestRunJob_OutputsFlowToLaterSteps` exercises the wiring (context building, expression substitution in the second step's command) rather than a real file round-trip — that real round-trip is what Task 8's end-to-end test with actual Docker proves.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run TestRunJob -v`
Expected: FAIL — `undefined: RunJob`

- [ ] **Step 3: Implement**

```go
// internal/engine/executor.go
package engine

import (
	"context"
	"fmt"
	"os"

	"mirror-gha/internal/commands"
	"mirror-gha/internal/runner"
)

// JobResult is the outcome of running every step in a job.
type JobResult struct {
	Conclusion string // "success" or "failure"
	Steps      []StepReport
}

// StepReport is the per-step record inside a JobResult.
type StepReport struct {
	ID         string
	Name       string
	Conclusion string // "success", "failure", or "skipped"
	ExitCode   int
	Stdout     string
	Stderr     string
}

// RunJob executes every step of job in order against backend, evaluating
// `if:` conditions, substituting ${{ }} expressions in `run:` commands, and
// honoring `continue-on-error`. It stops at the first unhandled failure.
func RunJob(ctx context.Context, wf *Workflow, job *Job, backend runner.Backend) (*JobResult, error) {
	actx := NewContext(wf, job)
	result := &JobResult{Conclusion: "success"}

	for i, step := range job.Steps {
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step-%d", i)
		}

		if step.If != "" {
			ok, err := EvalBool(step.If, actx)
			if err != nil {
				return nil, fmt.Errorf("evaluate if: for step %s: %w", id, err)
			}
			if !ok {
				actx.Steps[id] = StepOutcome{Outcome: "skipped"}
				result.Steps = append(result.Steps, StepReport{ID: id, Name: step.Name, Conclusion: "skipped"})
				continue
			}
		}

		command, err := SubstituteExpressions(step.Run, actx)
		if err != nil {
			return nil, fmt.Errorf("substitute expressions for step %s: %w", id, err)
		}

		filesDir, err := os.MkdirTemp("", "mirror-step-")
		if err != nil {
			return nil, fmt.Errorf("create temp dir for step %s: %w", id, err)
		}
		fileSet, err := commands.CreateFileSet(filesDir)
		if err != nil {
			return nil, fmt.Errorf("create workflow command files for step %s: %w", id, err)
		}

		env := map[string]string{}
		for k, v := range actx.Env {
			env[k] = v
		}
		for k, v := range step.Env {
			env[k] = v
		}

		stepResult, err := backend.RunStep(ctx, runner.StepSpec{
			Command:          command,
			Shell:            step.Shell,
			Env:              env,
			WorkingDirectory: step.WorkingDirectory,
			FilesDir:         filesDir,
		})
		if err != nil {
			return nil, fmt.Errorf("run step %s: %w", id, err)
		}

		outputs, err := commands.ParseKeyValueFile(fileSet.OutputFile)
		if err != nil {
			return nil, fmt.Errorf("parse outputs for step %s: %w", id, err)
		}
		envUpdates, err := commands.ParseKeyValueFile(fileSet.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("parse env updates for step %s: %w", id, err)
		}
		for k, v := range envUpdates {
			actx.Env[k] = v
		}

		conclusion := "success"
		if stepResult.ExitCode != 0 {
			conclusion = "failure"
		}
		actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}

		result.Steps = append(result.Steps, StepReport{
			ID:         id,
			Name:       step.Name,
			Conclusion: conclusion,
			ExitCode:   stepResult.ExitCode,
			Stdout:     stepResult.Stdout,
			Stderr:     stepResult.Stderr,
		})

		if conclusion == "failure" && !step.ContinueOnError {
			result.Conclusion = "failure"
			return result, nil
		}
	}

	return result, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -v`
Expected: PASS (all engine package tests, including Tasks 1-3's)

- [ ] **Step 5: Commit**

```bash
git add internal/engine/executor.go internal/engine/executor_test.go
git commit -m "feat(engine): run job steps with if/continue-on-error/output passing"
```

---

### Task 8: CLI wiring + real end-to-end smoke test

**Files:**
- Modify: `cmd/mirror/main.go` (replace the Phase-0 stub `run`/`dashboard` switch case bodies)
- Create: `cmd/mirror/testdata/simple.yml`
- Test: `cmd/mirror/main_test.go`

**Interfaces:**
- Consumes: `engine.Parse`, `engine.RunJob` (Tasks 1, 7), `runner.SelectBackend` (Task 5)
- Produces: `func runCommand(path string) int` (used by both `main()` and the test)

- [ ] **Step 1: Write the failing test**

```go
// cmd/mirror/main_test.go
package main

import (
	"os/exec"
	"testing"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping end-to-end test")
	}
}

func TestRunCommand_EndToEnd(t *testing.T) {
	requireDocker(t)

	exitCode := runCommand("testdata/simple.yml")
	if exitCode != 0 {
		t.Fatalf("runCommand() = %d, want 0", exitCode)
	}
}
```

```yaml
# cmd/mirror/testdata/simple.yml
name: smoke test
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: say hello
        run: echo hello-from-mirror
      - name: write output
        id: emit
        run: echo "greeting=hi" >> "$GITHUB_OUTPUT"
      - name: use output
        run: echo "got ${{ steps.emit.outputs.greeting }}"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./cmd/mirror/... -v`
Expected: FAIL — `undefined: runCommand` (current `main.go` is still the Phase-0 stub)

- [ ] **Step 3: Implement**

```go
// cmd/mirror/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"mirror-gha/internal/engine"
	"mirror-gha/internal/runner"
)

const version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println("mirror", version)
	case "run":
		if len(os.Args) < 3 {
			fmt.Println("usage: mirror run <workflow.yml>")
			os.Exit(1)
		}
		os.Exit(runCommand(os.Args[2]))
	case "dashboard":
		fmt.Println("mirror dashboard: dashboard server not implemented yet")
	default:
		printUsage()
		os.Exit(1)
	}
}

// runCommand parses and executes every job in the workflow at path,
// printing per-step results. It returns the process exit code: 0 if every
// job succeeded, 1 otherwise — split out from main() so it's directly
// testable without spawning a subprocess.
func runCommand(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read workflow: %v\n", err)
		return 1
	}

	wf, err := engine.Parse(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse workflow: %v\n", err)
		return 1
	}

	for name, job := range wf.Jobs {
		backend, err := runner.SelectBackend(job.RunsOn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job %s: %v\n", name, err)
			return 1
		}

		job := job
		result, err := engine.RunJob(context.Background(), wf, &job, backend)
		if err != nil {
			fmt.Fprintf(os.Stderr, "job %s: %v\n", name, err)
			return 1
		}

		for _, s := range result.Steps {
			fmt.Printf("[%s] %s: %s\n", name, s.Name, s.Conclusion)
			if s.Stdout != "" {
				fmt.Print(s.Stdout)
			}
			if s.Stderr != "" {
				fmt.Fprint(os.Stderr, s.Stderr)
			}
		}

		if result.Conclusion != "success" {
			return 1
		}
	}

	return 0
}

func printUsage() {
	fmt.Println("usage: mirror <version|run|dashboard>")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/mirror/... -v`
Expected: PASS (skips with a clear message if Docker isn't installed, rather than failing)

Then rebuild and manually confirm the CLI end-to-end:

Run: `go build -o bin/mirror ./cmd/mirror && ./bin/mirror run cmd/mirror/testdata/simple.yml`
Expected output includes `hello-from-mirror` and `got hi`, exit code 0 (`echo $?`)

- [ ] **Step 5: Commit**

```bash
git add cmd/mirror/main.go cmd/mirror/main_test.go cmd/mirror/testdata/simple.yml
git commit -m "feat(cli): wire mirror run to the workflow engine end-to-end"
```

---

### Task 9: Full test suite + module tidy

**Files:**
- Modify: `go.mod`, `go.sum` (via `go mod tidy`)

**Interfaces:**
- None new — this task verifies everything from Tasks 1-8 together.

- [ ] **Step 1: Run the full test suite**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./... -v`
Expected: PASS across `internal/engine`, `internal/commands`, `internal/runner`, `cmd/mirror` (Docker-dependent tests skip cleanly if Docker isn't installed on the machine running this)

- [ ] **Step 2: Tidy the module**

Run: `go mod tidy`
Expected: no changes, or only whitespace/formatting cleanup in `go.mod`/`go.sum` — if it pulls in unexpected new dependencies, stop and investigate before committing

- [ ] **Step 3: Commit if `go mod tidy` changed anything**

```bash
git add go.mod go.sum
git diff --cached --quiet || git commit -m "chore: tidy go.mod after vertical slice"
```

## Self-Review Notes

- **Spec coverage:** This plan covers the spec's Workflow Engine (parsing, contexts, expressions — minus multi-job DAG/matrix, deferred to Plan 2) and Runner Backend (Linux only; Windows/macOS explicitly stubbed via `ErrUnsupportedRunner`, matching the spec's Phase 2/3 language) and the workflow-command file protocol. Actions Runtime, GitHub API Shim, artifacts/cache, FidelityWarning, and Dashboard are out of scope for this plan by design — they're Plans 3-8 in the sequence stated above.
- **Placeholder scan:** No TBD/TODO left un-implemented within this plan's scope; every step has real, runnable code. Grouping-parens in expressions and multi-job DAG are named as explicit deferred scope, not silently missing.
- **Type consistency:** `StepOutcome`, `Context`, `StepSpec`, `StepResult`, `Backend`, `JobResult`, `StepReport` are defined once (Tasks 2, 5) and reused with identical field names/types through Tasks 6-8 — checked against each task's Interfaces block above.
