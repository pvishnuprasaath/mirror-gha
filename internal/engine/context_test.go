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

func TestNewContext_ExposesRunIdentity(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})

	for _, key := range []string{"run_id", "run_number", "run_attempt"} {
		val, ok := ctx.GitHub[key].(string)
		if !ok || val == "" {
			t.Errorf("GitHub[%q] = %#v, want a non-empty string placeholder value", key, ctx.GitHub[key])
		}
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

func TestResolvePath_Inputs(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.Env["INPUT_WHO-TO-GREET"] = "mirror-gha"

	got, err := ctx.resolvePath([]string{"inputs", "who-to-greet"})
	if err != nil {
		t.Fatalf("resolvePath(inputs.who-to-greet) error = %v", err)
	}
	if got != "mirror-gha" {
		t.Errorf("resolvePath(inputs.who-to-greet) = %v, want %q", got, "mirror-gha")
	}

	got, err = ctx.resolvePath([]string{"inputs", "not-set"})
	if err != nil {
		t.Fatalf("resolvePath(inputs.not-set) error = %v", err)
	}
	if got != "" {
		t.Errorf("resolvePath(inputs.not-set) = %v, want empty string", got)
	}
}

func TestResolvePath_Secrets(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.Secrets["GITHUB_TOKEN"] = "fake-token-value"

	got, err := ctx.resolvePath([]string{"secrets", "GITHUB_TOKEN"})
	if err != nil {
		t.Fatalf("resolvePath(secrets.GITHUB_TOKEN) error = %v", err)
	}
	if got != "fake-token-value" {
		t.Errorf("resolvePath(secrets.GITHUB_TOKEN) = %v, want %q", got, "fake-token-value")
	}
}

func TestResolvePath_GithubEventNestedMap(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{
		"pull_request": map[string]interface{}{
			"number": float64(42),
			"head":   map[string]interface{}{"ref": "feature-branch"},
		},
	}

	got, err := ctx.resolvePath([]string{"github", "event", "pull_request", "number"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != float64(42) {
		t.Errorf("resolvePath() = %v, want 42", got)
	}

	got, err = ctx.resolvePath([]string{"github", "event", "pull_request", "head", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "feature-branch" {
		t.Errorf("resolvePath() = %v, want feature-branch", got)
	}
}

func TestResolvePath_GithubEventCaseInsensitiveKeys(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{"Ref": "refs/heads/main"}

	got, err := ctx.resolvePath([]string{"github", "event", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "refs/heads/main" {
		t.Errorf("resolvePath() = %v, want refs/heads/main", got)
	}
}

func TestResolvePath_GithubEventArrayIndex(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{
		"commits": []interface{}{
			map[string]interface{}{"message": "first"},
			map[string]interface{}{"message": "second"},
		},
	}

	got, err := ctx.resolvePath([]string{"github", "event", "commits", "1", "message"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "second" {
		t.Errorf("resolvePath() = %v, want second", got)
	}
}

func TestResolvePath_GithubEventMissingPathReturnsNil(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{"ref": "refs/heads/main"}

	got, err := ctx.resolvePath([]string{"github", "event", "pull_request", "number"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v, want nil error for a missing path", err)
	}
	if got != nil {
		t.Errorf("resolvePath() = %v, want nil for a missing path", got)
	}
}

func TestResolvePath_GithubEventWhenEventFieldAbsent(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	got, err := ctx.resolvePath([]string{"github", "event", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != nil {
		t.Errorf("resolvePath() = %v, want nil when github.event was never set", got)
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
