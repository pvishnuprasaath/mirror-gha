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
}

func TestEvalExpression_Negation(t *testing.T) {
	ctx := newTestContext()
	val, err := EvalExpression("!(env.JOB == 'nope')", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != true {
		t.Errorf("val = %v, want true", val)
	}
}

func TestEvalExpression_Comparisons(t *testing.T) {
	ctx := newTestContext()
	cases := []struct {
		expr string
		want interface{}
	}{
		{"1 < 2", true},
		{"2 <= 2", true},
		{"3 > 2", true},
		{"2 >= 3", false},
		{"null == null", true},
	}
	for _, c := range cases {
		val, err := EvalExpression(c.expr, ctx)
		if err != nil {
			t.Fatalf("EvalExpression(%q) error = %v", c.expr, err)
		}
		if val != c.want {
			t.Errorf("EvalExpression(%q) = %v, want %v", c.expr, val, c.want)
		}
	}
}

func TestEvalExpression_LogicalOperatorsReturnOperand(t *testing.T) {
	ctx := newTestContext()

	val, err := EvalExpression("'' || 'default'", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != "default" {
		t.Errorf("val = %v, want %q (|| should return the operand, not a coerced bool)", val, "default")
	}

	val, err = EvalExpression("false && 'unreached'", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != false {
		t.Errorf("val = %v, want false (&& should short-circuit and return the falsy left operand)", val)
	}
}

func TestEvalExpression_CaseInsensitivePropertyAccess(t *testing.T) {
	ctx := newTestContext()
	// env.JOB was set via newTestContext with an uppercase key; GHA's
	// expression language matches property names case-insensitively.
	val, err := EvalExpression("env.job", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	if val != "j" {
		t.Errorf("val = %v, want %q", val, "j")
	}
}

func TestEvalExpression_BuiltinFunctions(t *testing.T) {
	ctx := newTestContext()
	cases := []struct {
		expr string
		want interface{}
	}{
		{"contains('hello world', 'world')", true},
		{"startsWith('hello', 'he')", true},
		{"endsWith('hello', 'lo')", true},
		{"format('{0} and {1}', 'a', 'b')", "a and b"},
		{"join(fromJSON('[\"a\",\"b\",\"c\"]'), '-')", "a-b-c"},
		{"toJSON('hi')", `"hi"`},
	}
	for _, c := range cases {
		val, err := EvalExpression(c.expr, ctx)
		if err != nil {
			t.Fatalf("EvalExpression(%q) error = %v", c.expr, err)
		}
		if val != c.want {
			t.Errorf("EvalExpression(%q) = %v, want %v", c.expr, val, c.want)
		}
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
