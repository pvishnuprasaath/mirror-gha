# Composite Actions Runtime + --local-repository Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `uses:` steps resolve a composite action (`runs.using: composite`) and execute its own nested `run:`/`uses:` step list for real — including a genuine `inputs.*` expression context, nested-step output bridging back to the calling step, and recursive nesting — plus a `--local-repository` flag for overriding action resolution during local testing.

**Architecture:** Composite actions are not a new execution mode — they're the same step-execution machinery, called recursively. `RunJob`'s per-step body is extracted into a standalone `runStep` function; a composite action's nested steps run through that exact function again, against a child `Context` (its own step-ID namespace, its own `inputs.*`/`GITHUB_ACTION_PATH`) and a synthetic empty `Job`/`Workflow` (so nested steps never inherit the caller's shell/working-directory defaults) — mirroring act's own `newCompositeRunContext` model exactly. `--local-repository` is an independent, much smaller deliverable: a resolution-time override checked before the existing `FetchRemote` call.

**Tech Stack:** Go 1.27 stdlib only. No new dependencies.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Composite Actions Runtime" and "`--local-repository`" sections.

## Global Constraints

- Composite actions are the last of the three `runs.using` kinds — after this plan, no `runs.using` value used by a real action is rejected as "not supported yet."
- `inputs.<name>` resolves by reversing the existing `INPUT_<TRANSFORMED_NAME>` transform (`"INPUT_" + regexp("[^A-Z0-9-]").ReplaceAllString(strings.ToUpper(name), "_")`) against `Context.Env` — not a separately-stored field, matching act's own derivation. Applies generically to every `Context`, not just composite ones.
- A composite action's nested steps never inherit the calling workflow/job's `defaults.run.shell`/`defaults.run.working-directory` — they get a synthetic empty `Job`/`Workflow`, matching act's finding that a composite's own synthetic `RunContext` has an empty `Job()`.
- Composite-in-composite recursion is capped at depth 10 (`maxCompositeDepth`) — a deliberate mirror-gha-specific safety addition beyond act's own unbounded recursion, since mirror-gha's local-path (`./`) resolution makes a self-referencing composite action a real, easy-to-hit crash risk that act's network-fetch model doesn't share.
- `--local-repository owner/repo[@ref]=local/path` (repeatable) — only the `owner/repo@ref` key form is supported (not act's additional full-URL form; mirror-gha doesn't model arbitrary git hosts yet).
- TDD throughout: every task writes the failing test before the implementation.
- Docker-dependent tests skip gracefully via the existing `requireDocker(t)` pattern (duplicated per-package).

---

### Task 1: `action.yml` composite fields — `ActionStep`, `ActionOutput`

**Files:**
- Modify: `internal/actions/metadata.go`
- Test: `internal/actions/metadata_test.go`

**Interfaces:**
- Produces: `type ActionStep struct{ ID, Name, Run, Uses, Shell, WorkingDirectory string; With, Env map[string]string; If string; ContinueOnError bool }`, `ActionRuns.Steps []ActionStep`, `type ActionOutput struct{ Description, Value string }`, `ActionMetadata.Outputs map[string]ActionOutput` (changed from `map[string]interface{}`)

`ActionMetadata.Outputs` is parsed today but never consumed anywhere in the codebase (confirmed via grep) — this type change is safe.

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/metadata_test.go — add this test
func TestParseMetadata_CompositeRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Composite Action'
inputs:
  who-to-greet:
    default: 'World'
outputs:
  greeting:
    description: 'the greeting'
    value: '${{ steps.greet.outputs.greeting }}'
runs:
  using: 'composite'
  steps:
    - id: greet
      run: 'echo "greeting=Hello, ${{ inputs.who-to-greet }}!" >> "$GITHUB_OUTPUT"'
      shell: 'sh'
    - uses: './some/nested-action'
      with:
        key: 'value'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Runs.Using != "composite" {
		t.Errorf("Runs.Using = %q, want composite", meta.Runs.Using)
	}
	if len(meta.Runs.Steps) != 2 {
		t.Fatalf("Runs.Steps = %d entries, want 2", len(meta.Runs.Steps))
	}
	if meta.Runs.Steps[0].ID != "greet" || meta.Runs.Steps[0].Shell != "sh" {
		t.Errorf("Runs.Steps[0] = %+v, want ID=greet Shell=sh", meta.Runs.Steps[0])
	}
	if meta.Runs.Steps[1].Uses != "./some/nested-action" || meta.Runs.Steps[1].With["key"] != "value" {
		t.Errorf("Runs.Steps[1] = %+v, want Uses=./some/nested-action With[key]=value", meta.Runs.Steps[1])
	}
	output, ok := meta.Outputs["greeting"]
	if !ok {
		t.Fatal(`Outputs["greeting"] not found`)
	}
	if output.Value != "${{ steps.greet.outputs.greeting }}" {
		t.Errorf("Outputs[greeting].Value = %q, want %q", output.Value, "${{ steps.greet.outputs.greeting }}")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/actions/... -run TestParseMetadata_CompositeRuns -v`
Expected: FAIL — `meta.Runs.Steps` undefined, `meta.Outputs["greeting"].Value` undefined (`Outputs` is still `map[string]interface{}`)

- [ ] **Step 3: Implement**

```go
// internal/actions/metadata.go — add above ActionRuns
// ActionStep is one entry in a composite action's `runs.steps:` list.
// Mirrors engine.Step's shape for the fields composite steps actually
// support — deliberately not engine.Step itself: internal/engine already
// imports internal/actions, so the reverse import would cycle.
// engine.Step's TimeoutMinutes isn't here — composite steps don't
// support it.
type ActionStep struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Uses             string            `yaml:"uses"`
	With             map[string]string `yaml:"with"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
}

// ActionOutput is one entry in an action.yml's `outputs:` map. Value is
// only meaningful for composite actions — a `${{ steps.x.outputs.y }}`
// expression evaluated against that composite's own nested step scope.
// JS/Docker actions' outputs are purely descriptive (the action itself
// writes $GITHUB_OUTPUT directly), which is why this field was absent
// before composite actions needed it.
type ActionOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}
```

Replace `ActionRuns` and `ActionMetadata`:

```go
// ActionRuns is an action.yml's `runs:` block.
type ActionRuns struct {
	Using      string            `yaml:"using"`
	Main       string            `yaml:"main"`
	Image      string            `yaml:"image"`
	Entrypoint string            `yaml:"entrypoint"`
	Args       []string          `yaml:"args"`
	Env        map[string]string `yaml:"env"`
	Steps      []ActionStep      `yaml:"steps"`
}

// ActionMetadata is a parsed action.yml/action.yaml.
type ActionMetadata struct {
	Name    string                  `yaml:"name"`
	Inputs  map[string]ActionInput  `yaml:"inputs"`
	Outputs map[string]ActionOutput `yaml:"outputs"`
	Runs    ActionRuns              `yaml:"runs"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/actions/metadata.go internal/actions/metadata_test.go
git commit -m "feat(actions): parse composite action.yml fields (steps/output values)"
```

---

### Task 2: `inputs.*` expression context

**Files:**
- Modify: `internal/engine/context.go`
- Test: `internal/engine/context_test.go`

**Interfaces:**
- Produces: `Context.resolvePath` handles `"inputs"` as a valid context name

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/context_test.go — add this test
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run TestResolvePath_Inputs -v`
Expected: FAIL — `unknown context: inputs`

- [ ] **Step 3: Implement**

```go
// internal/engine/context.go — add near the top, alongside the imports
import (
	"fmt"
	"regexp"
	"strings"
)

// inputContextKeySanitizer mirrors internal/actions.InputEnv's private
// transform (uppercase, non-alphanumeric-and-dash becomes "_") —
// duplicated locally rather than exported/cross-imported, same
// precedent as requireDocker/requireNetwork being duplicated per
// package throughout this project. inputs.<name> is nothing but
// INPUT_<TRANSFORMED_NAME> re-exposed — confirmed against act's
// getEvaluatorInputs (expression.go:481), not separately-stored state.
var inputContextKeySanitizer = regexp.MustCompile(`[^A-Z0-9-]`)

func inputEnvKeyFor(name string) string {
	return "INPUT_" + inputContextKeySanitizer.ReplaceAllString(strings.ToUpper(name), "_")
}
```

In `resolvePath`'s `switch strings.ToLower(path[0])`, add a case (position doesn't matter, but placing it near `"vars"` keeps context cases grouped):

```go
	case "inputs":
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid inputs reference: %s", strings.Join(path, "."))
		}
		return lookupStringCI(c.Env, inputEnvKeyFor(path[1])), nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -v`
Expected: PASS — full engine package suite

- [ ] **Step 5: Commit**

```bash
git add internal/engine/context.go internal/engine/context_test.go
git commit -m "feat(engine): add inputs.* expression context"
```

---

### Task 3: Refactor `RunJob` into a reusable `runStep`

**Files:**
- Modify: `internal/engine/executor.go`
- Modify: `internal/engine/uses_step.go`
- Modify: `internal/engine/workflow_run.go`
- Modify: `internal/engine/executor_test.go`, `internal/engine/uses_step_test.go`, `internal/engine/workflow_run_test.go`

**Interfaces:**
- Produces: `type runStepParams struct{ Workflow *Workflow; Job *Job; RunnerJob runner.Job; WorkspaceDir string; NodeReady *bool; LocalRepositoryOverrides map[string]string; Depth int }`, `func runStep(ctx context.Context, p runStepParams, actx *Context, step Step, id string) (StepReport, error)`, `func prepareUsesStep(ctx context.Context, p runStepParams, stepID string, step Step, actx *Context) (usesStepPlan, error)` (signature changed — was `(ctx, job runner.Job, workspaceDir, stepID string, step Step, actx *Context, nodeReady *bool)`)

This is a pure refactor — no new observable behavior. `LocalRepositoryOverrides` is added to the struct now (so the signature only changes once) but stays unused/nil until Task 5 wires the CLI flag through it.

- [ ] **Step 1: Confirm current behavior is captured by existing tests**

Run: `go test ./internal/engine/... -v 2>&1 | tail -5`
Expected: PASS — this is the baseline the refactor must not break. No new test is written for this task; the existing suite (already covering `if:`, env merging, output parsing, continue-on-error, `uses:` dispatch for node/docker) is the regression net.

- [ ] **Step 2: Implement — extract `runStep`**

Replace `internal/engine/executor.go`'s `RunJob` function and the step loop inside it with:

```go
// runStepParams bundles what varies between a job's own top-level steps
// and a composite action's nested ones — RunnerJob/WorkspaceDir/NodeReady/
// LocalRepositoryOverrides stay the same across an entire job (including
// into any composite action nested inside it), while Workflow/Job/Depth
// change for a composite's synthetic child execution (see
// composite_step.go's runCompositeSteps).
type runStepParams struct {
	Workflow                 *Workflow
	Job                      *Job
	RunnerJob                runner.Job
	WorkspaceDir             string
	NodeReady                *bool
	LocalRepositoryOverrides map[string]string
	Depth                    int
}

// RunJob executes every step of job in order against backend, evaluating
// `if:` conditions, substituting ${{ }} expressions in `run:` commands, and
// honoring `continue-on-error`. It stops starting new steps at the first
// unhandled failure, but always computes job.Outputs from whatever steps
// did run before returning.
//
// All steps run inside the same job-scoped environment (one container for
// the whole job, not one per step) so filesystem state — checked-out
// files, installed packages, PATH changes — persists step to step, the
// same way real GitHub Actions and act both work. opts.WorkspaceDir is
// bind-mounted into that environment and exposed as GITHUB_WORKSPACE /
// github.workspace / the default step working directory.
func RunJob(ctx context.Context, wf *Workflow, job *Job, backend runner.Backend, opts JobRunOptions) (*JobResult, error) {
	if opts.WorkspaceDir == "" {
		return nil, fmt.Errorf("JobRunOptions.WorkspaceDir must not be empty")
	}

	actx := NewContext(wf, job)
	actx.Needs = opts.Needs
	actx.Matrix = opts.Matrix
	actx.Vars = opts.Vars
	result := &JobResult{Conclusion: "success"}

	runnerJob, err := backend.StartJob(ctx, opts.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("start job: %w", err)
	}
	defer runnerJob.Stop(ctx)

	actx.GitHub["workspace"] = runnerJob.WorkspacePath()
	nodeReady := false

	p := runStepParams{
		Workflow:                 wf,
		Job:                      job,
		RunnerJob:                runnerJob,
		WorkspaceDir:             opts.WorkspaceDir,
		NodeReady:                &nodeReady,
		LocalRepositoryOverrides: opts.LocalRepositoryOverrides,
	}

	for i, step := range job.Steps {
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step-%d", i)
		}

		report, err := runStep(ctx, p, actx, step, id)
		if err != nil {
			return nil, err
		}
		result.Steps = append(result.Steps, report)

		if report.Conclusion == "failure" && !step.ContinueOnError {
			result.Conclusion = "failure"
			break
		}
	}

	outputs := map[string]string{}
	for name, expr := range job.Outputs {
		val, err := SubstituteExpressions(expr, actx)
		if err != nil {
			return nil, fmt.Errorf("evaluate job output %q: %w", name, err)
		}
		outputs[name] = val
	}
	result.Outputs = outputs

	return result, nil
}

// runStep executes exactly one step — a run: command, a JS/Docker uses:
// step, or a composite uses: step (which recurses via runCompositeSteps,
// composite_step.go) — against actx, updating actx.Steps[id]/actx.Env as
// a side effect and returning this step's report. Shared by RunJob's
// top-level loop and, recursively, by a composite action's own nested
// step list, so "how a step runs" has exactly one implementation
// regardless of nesting depth.
func runStep(ctx context.Context, p runStepParams, actx *Context, step Step, id string) (StepReport, error) {
	if step.Run != "" && step.Uses != "" {
		return StepReport{}, fmt.Errorf("step %s: cannot set both run: and uses:", id)
	}

	if step.If != "" {
		ok, err := EvalBool(step.If, actx)
		if err != nil {
			return StepReport{}, fmt.Errorf("evaluate if: for step %s: %w", id, err)
		}
		if !ok {
			actx.Steps[id] = StepOutcome{Outcome: "skipped"}
			return StepReport{ID: id, Name: step.Name, Conclusion: "skipped"}, nil
		}
	}

	var command string
	var plan usesStepPlan
	var err error
	if step.Uses != "" {
		plan, err = prepareUsesStep(ctx, p, id, step, actx)
		if err != nil {
			return StepReport{}, fmt.Errorf("prepare uses: step %s: %w", id, err)
		}
	} else {
		command, err = SubstituteExpressions(step.Run, actx)
		if err != nil {
			return StepReport{}, fmt.Errorf("substitute expressions for step %s: %w", id, err)
		}
	}

	if plan.Composite != nil {
		conclusion, outputs, err := runCompositeSteps(ctx, p, actx, plan.Composite)
		if err != nil {
			return StepReport{}, fmt.Errorf("composite step %s: %w", id, err)
		}
		actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}
		return StepReport{ID: id, Name: step.Name, Conclusion: conclusion}, nil
	}

	filesDir, err := os.MkdirTemp(p.RunnerJob.FilesRoot(), "step-")
	if err != nil {
		return StepReport{}, fmt.Errorf("create temp dir for step %s: %w", id, err)
	}
	fileSet, err := commands.CreateFileSet(filesDir)
	if err != nil {
		return StepReport{}, fmt.Errorf("create workflow command files for step %s: %w", id, err)
	}

	env := map[string]string{}
	for k, v := range actx.Env {
		env[k] = v
	}
	for k, v := range step.Env {
		env[k] = v
	}
	env["GITHUB_WORKSPACE"] = p.RunnerJob.WorkspacePath()
	for k, v := range plan.Env {
		env[k] = v
	}

	workingDirectory := p.RunnerJob.WorkspacePath()
	if step.Uses == "" {
		if wd := effectiveWorkingDirectory(step, p.Job, p.Workflow); wd != "" {
			workingDirectory = wd
		}
	}

	stepCtx := ctx
	var cancel context.CancelFunc
	if step.TimeoutMinutes > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMinutes*float64(time.Minute)))
	}

	var stepResult runner.StepResult
	if plan.Docker != nil {
		plan.Docker.Env = env
		plan.Docker.FilesDir = filesDir
		stepResult, err = p.RunnerJob.RunDockerAction(stepCtx, *plan.Docker)
	} else {
		stepResult, err = p.RunnerJob.Exec(stepCtx, runner.StepSpec{
			Command:          command,
			Args:             plan.Args,
			Shell:            effectiveShell(step, p.Job, p.Workflow),
			Env:              env,
			WorkingDirectory: workingDirectory,
			FilesDir:         filesDir,
		})
	}
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return StepReport{}, fmt.Errorf("run step %s: %w", id, err)
	}

	outputs, err := commands.ParseKeyValueFile(fileSet.OutputFile)
	if err != nil {
		return StepReport{}, fmt.Errorf("parse outputs for step %s: %w", id, err)
	}
	for k, v := range commands.ParseLegacyOutputs(stepResult.Stdout) {
		if _, exists := outputs[k]; !exists {
			outputs[k] = v
		}
	}
	envUpdates, err := commands.ParseKeyValueFile(fileSet.EnvFile)
	if err != nil {
		return StepReport{}, fmt.Errorf("parse env updates for step %s: %w", id, err)
	}
	for k, v := range envUpdates {
		actx.Env[k] = v
	}

	conclusion := "success"
	if stepResult.ExitCode != 0 {
		conclusion = "failure"
	}
	actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}

	return StepReport{
		ID:         id,
		Name:       step.Name,
		Conclusion: conclusion,
		ExitCode:   stepResult.ExitCode,
		Stdout:     stepResult.Stdout,
		Stderr:     stepResult.Stderr,
	}, nil
}
```

`JobRunOptions` (defined earlier in the same file) gains one field:

```go
type JobRunOptions struct {
	Needs                    map[string]JobOutcome
	Matrix                   MatrixCombination
	Vars                     map[string]string
	WorkspaceDir             string
	LocalRepositoryOverrides map[string]string
}
```

- [ ] **Step 3: Update `prepareUsesStep`'s signature in `uses_step.go`**

Change the function signature and every reference to `job`/`workspaceDir`/`nodeReady` inside it:

```go
func prepareUsesStep(ctx context.Context, p runStepParams, stepID string, step Step, actx *Context) (usesStepPlan, error) {
```

Inside the function body, replace every occurrence of:
- `job.CopyToContainer(` → `p.RunnerJob.CopyToContainer(`
- `filepath.Join(workspaceDir, ref.LocalPath)` → `filepath.Join(p.WorkspaceDir, ref.LocalPath)`
- `*nodeReady` → `*p.NodeReady`
- `*nodeReady = true` → `*p.NodeReady = true`

(`usesStepPlan` itself is untouched by this task — Task 4 adds its `Composite` field.)

- [ ] **Step 4: Update `RunWorkflow` in `workflow_run.go`**

Add a parameter and thread it through to `JobRunOptions`:

```go
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector, workspaceDir string, localRepositoryOverrides map[string]string) (*WorkflowResult, error) {
```

And in the `RunJob` call inside it:

```go
			jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
				Needs:                    outcomes,
				Matrix:                   combo,
				WorkspaceDir:             workspaceDir,
				LocalRepositoryOverrides: localRepositoryOverrides,
			})
```

- [ ] **Step 5: Update every existing call site to match the new signatures**

In `internal/engine/uses_step_test.go`, every `prepareUsesStep(context.Background(), job, workspaceDir, "...", step, actx, &nodeReady)` call becomes:

```go
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}
	plan, err := prepareUsesStep(context.Background(), p, "greet", step, actx)
```

(construct `p` once per test, right before the `prepareUsesStep` call, replacing the old positional `job, workspaceDir, ..., &nodeReady` arguments — apply this to all four existing tests: `TestPrepareUsesStep_LocalAction`, `TestPrepareUsesStep_RejectsCompositeRuntime`, `TestPrepareUsesStep_RawDockerImage`, `TestPrepareUsesStep_RepoBasedDockerAction`).

In `internal/engine/workflow_run_test.go`, every `RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir())` / `..., failSelector, ...` call gains a trailing `nil` argument:

```go
	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil)
```

(apply to all `RunWorkflow` calls in that file — `succeedSelector` and `failSelector` variants alike.)

`internal/engine/executor_test.go` needs no signature-call changes (it calls `RunJob` directly, whose signature is unchanged — only `JobRunOptions` gained a field, which is additive and doesn't break existing struct literals).

- [ ] **Step 6: Run the full suite to verify the refactor changed nothing observable**

Run: `go build ./... && go test ./... -v 2>&1 | tail -40`
Expected: PASS — every test that passed before this task still passes, with identical behavior (this task adds no new test because it adds no new behavior).

- [ ] **Step 7: Commit**

```bash
git add internal/engine/executor.go internal/engine/uses_step.go internal/engine/workflow_run.go internal/engine/executor_test.go internal/engine/uses_step_test.go internal/engine/workflow_run_test.go
git commit -m "refactor(engine): extract runStep, thread runStepParams through prepareUsesStep"
```

---

### Task 4: Composite action dispatch and nested execution

**Files:**
- Create: `internal/engine/composite_step.go`
- Modify: `internal/engine/uses_step.go`
- Test: `internal/engine/composite_step_test.go`, `internal/engine/uses_step_test.go`

**Interfaces:**
- Consumes: `runStepParams`, `runStep` (Task 3), `ActionStep`, `ActionOutput` (Task 1), `inputs.*` context (Task 2)
- Produces: `type compositeInvocation struct{ Steps []Step; Outputs map[string]actions.ActionOutput; BaseEnv map[string]string }`, `func runCompositeSteps(ctx context.Context, p runStepParams, parentActx *Context, comp *compositeInvocation) (conclusion string, outputs map[string]string, err error)`, `const maxCompositeDepth = 10`, `usesStepPlan.Composite *compositeInvocation`

`TestPrepareUsesStep_RejectsCompositeRuntime` (from the Docker actions sub-project) is now wrong — composite is supported — and must be replaced, not just left in place.

- [ ] **Step 1: Write the failing tests**

Replace `uses_step_test.go`'s `TestPrepareUsesStep_RejectsCompositeRuntime` with a test of the now-genuinely-unsupported case, and add a new composite-dispatch test:

```go
// internal/engine/uses_step_test.go — replace TestPrepareUsesStep_RejectsCompositeRuntime with:
func TestPrepareUsesStep_RejectsUnknownRuntime(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "mystery-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Mystery Action'\nruns:\n  using: 'some-future-runtime'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./mystery-action"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	_, err := prepareUsesStep(context.Background(), p, "one", step, actx)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error for an unknown runs.using")
	}
}

func TestPrepareUsesStep_CompositeAction(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "composite-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Composite Action'
inputs:
  who-to-greet:
    default: 'World'
outputs:
  greeting:
    value: '${{ steps.greet.outputs.greeting }}'
runs:
  using: 'composite'
  steps:
    - id: greet
      shell: 'sh'
      run: 'echo hi'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./composite-action", With: map[string]string{"who-to-greet": "mirror-gha"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	plan, err := prepareUsesStep(context.Background(), p, "greet-step", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Composite == nil {
		t.Fatal("Composite = nil, want non-nil for a composite action")
	}
	if len(plan.Composite.Steps) != 1 || plan.Composite.Steps[0].ID != "greet" {
		t.Errorf("Composite.Steps = %+v, want one step with ID=greet", plan.Composite.Steps)
	}
	if plan.Composite.BaseEnv["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`Composite.BaseEnv["INPUT_WHO-TO-GREET"] = %q, want %q`, plan.Composite.BaseEnv["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
	if plan.Composite.BaseEnv["GITHUB_ACTION_PATH"] != "/mirror-actions/greet-step" {
		t.Errorf(`Composite.BaseEnv["GITHUB_ACTION_PATH"] = %q, want %q`, plan.Composite.BaseEnv["GITHUB_ACTION_PATH"], "/mirror-actions/greet-step")
	}
	out, ok := plan.Composite.Outputs["greeting"]
	if !ok || out.Value != "${{ steps.greet.outputs.greeting }}" {
		t.Errorf(`Composite.Outputs["greeting"] = %+v, want Value=${{ steps.greet.outputs.greeting }}`, out)
	}
}
```

```go
// internal/engine/composite_step_test.go
package engine

import (
	"context"
	"testing"

	"mirror-gha/internal/actions"
)

func TestRunCompositeSteps_NestedOutputBridgesToParent(t *testing.T) {
	requireDocker(t)

	backend := runner.NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	p := runStepParams{RunnerJob: job, WorkspaceDir: t.TempDir()}
	parentActx := NewContext(&Workflow{}, &Job{})

	comp := &compositeInvocation{
		Steps: []Step{
			{ID: "greet", Shell: "sh", Run: `echo "greeting=hi from composite" >> "$GITHUB_OUTPUT"`},
		},
		Outputs: map[string]actions.ActionOutput{
			"greeting": {Value: "${{ steps.greet.outputs.greeting }}"},
		},
		BaseEnv: map[string]string{},
	}

	conclusion, outputs, err := runCompositeSteps(context.Background(), p, parentActx, comp)
	if err != nil {
		t.Fatalf("runCompositeSteps() error = %v", err)
	}
	if conclusion != "success" {
		t.Errorf("conclusion = %q, want success", conclusion)
	}
	if outputs["greeting"] != "hi from composite" {
		t.Errorf(`outputs["greeting"] = %q, want %q`, outputs["greeting"], "hi from composite")
	}
	if _, leaked := parentActx.Steps["greet"]; leaked {
		t.Error(`parentActx.Steps["greet"] exists — nested step IDs must not leak into the parent's own steps context`)
	}
}

func TestRunCompositeSteps_ExceedsMaxDepth(t *testing.T) {
	p := runStepParams{Depth: maxCompositeDepth}
	parentActx := NewContext(&Workflow{}, &Job{})
	comp := &compositeInvocation{}

	_, _, err := runCompositeSteps(context.Background(), p, parentActx, comp)
	if err == nil {
		t.Fatal("runCompositeSteps() error = nil, want error for exceeding max nesting depth")
	}
}
```

`composite_step_test.go` needs `"mirror-gha/internal/runner"` imported too (for `runner.NewLinuxDockerBackend`) — add it alongside `"mirror-gha/internal/actions"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/... -run 'TestPrepareUsesStep_CompositeAction|TestPrepareUsesStep_RejectsUnknownRuntime|TestRunCompositeSteps' -v`
Expected: FAIL — `plan.Composite`/`compositeInvocation`/`runCompositeSteps`/`maxCompositeDepth` undefined

- [ ] **Step 3: Implement**

```go
// internal/engine/composite_step.go
package engine

import (
	"context"
	"fmt"

	"mirror-gha/internal/actions"
)

// compositeInvocation is what prepareUsesStep resolves a composite
// (runs.using: composite) uses: step down to — its own nested step list
// (already converted from actions.ActionStep to engine.Step), its own
// declared outputs (each a value: expression, evaluated against the
// nested scope once the steps finish), and the env this composite
// invocation's own nested steps see (INPUT_*/GITHUB_ACTION_PATH) layered
// on top of the calling step's own env.
type compositeInvocation struct {
	Steps   []Step
	Outputs map[string]actions.ActionOutput
	BaseEnv map[string]string
}

// maxCompositeDepth caps composite-in-composite recursion. act itself has
// no such cap — its action references are content-addressed network
// fetches, making a self-referencing cycle rare. mirror-gha's local-path
// (./) resolution makes a composite action accidentally referencing
// itself a real, easy-to-hit crash (unbounded Go call-stack recursion)
// rather than a theoretical one; no real action nests anywhere near this
// deep, so the cap is cheap insurance, not a functional limitation.
const maxCompositeDepth = 10

// runCompositeSteps executes a composite action's own nested step list
// against a child Context — a fresh Steps map (so nested step IDs never
// collide with or leak into the caller's), Env seeded from the parent's
// env plus this composite's own INPUT_*/GITHUB_ACTION_PATH — and
// evaluates the composite's own declared outputs against that child
// scope once the nested steps finish, mirroring act's
// newCompositeRunContext and its post-execution rc.setOutput bridge.
// Nested steps never inherit the calling workflow/job's
// defaults.run.shell or working-directory (p.Job/p.Workflow are swapped
// for empty synthetic values here) — matches act's finding that a
// composite's synthetic RunContext has an empty Job(), so its Defaults
// always resolve empty.
func runCompositeSteps(ctx context.Context, p runStepParams, parentActx *Context, comp *compositeInvocation) (string, map[string]string, error) {
	if p.Depth+1 > maxCompositeDepth {
		return "", nil, fmt.Errorf("exceeded max composite action nesting depth (%d) — likely a self-referencing action", maxCompositeDepth)
	}

	childActx := &Context{
		GitHub: parentActx.GitHub,
		Env:    map[string]string{},
		Runner: parentActx.Runner,
		Steps:  map[string]StepOutcome{},
		Needs:  parentActx.Needs,
		Matrix: parentActx.Matrix,
		Vars:   parentActx.Vars,
	}
	for k, v := range parentActx.Env {
		childActx.Env[k] = v
	}
	for k, v := range comp.BaseEnv {
		childActx.Env[k] = v
	}

	childParams := runStepParams{
		Workflow:                 &Workflow{},
		Job:                      &Job{},
		RunnerJob:                p.RunnerJob,
		WorkspaceDir:             p.WorkspaceDir,
		NodeReady:                p.NodeReady,
		LocalRepositoryOverrides: p.LocalRepositoryOverrides,
		Depth:                    p.Depth + 1,
	}

	conclusion := "success"
	for i, nestedStep := range comp.Steps {
		nestedID := nestedStep.ID
		if nestedID == "" {
			nestedID = fmt.Sprintf("step-%d", i)
		}
		report, err := runStep(ctx, childParams, childActx, nestedStep, nestedID)
		if err != nil {
			return "", nil, fmt.Errorf("nested step %s: %w", nestedID, err)
		}
		if report.Conclusion == "failure" && !nestedStep.ContinueOnError {
			conclusion = "failure"
			break
		}
	}

	outputs := map[string]string{}
	for name, out := range comp.Outputs {
		val, err := SubstituteExpressions(out.Value, childActx)
		if err != nil {
			return "", nil, fmt.Errorf("evaluate output %q: %w", name, err)
		}
		outputs[name] = val
	}

	return conclusion, outputs, nil
}
```

In `internal/engine/uses_step.go`, add `Composite *compositeInvocation` to `usesStepPlan`:

```go
type usesStepPlan struct {
	Args      []string
	Env       map[string]string
	Docker    *runner.DockerActionSpec
	Composite *compositeInvocation
}
```

Replace the `default:` case of the `switch` in `prepareUsesStep` (currently `return usesStepPlan{}, fmt.Errorf("action %s has runs.using=%q, which isn't supported yet (only JS/node and Docker actions run today)", step.Uses, metadata.Runs.Using)`) with a new `case "composite":` immediately before it, keeping `default:` as the final catch-all for genuinely unknown values:

```go
	case metadata.Runs.Using == "composite":
		if err := p.RunnerJob.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
			return usesStepPlan{}, fmt.Errorf("copy composite action %s into job: %w", step.Uses, err)
		}
		nestedSteps := make([]Step, len(metadata.Runs.Steps))
		for i, as := range metadata.Runs.Steps {
			nestedSteps[i] = Step{
				ID:               as.ID,
				Name:             as.Name,
				Run:              as.Run,
				Uses:             as.Uses,
				With:             as.With,
				Shell:            as.Shell,
				Env:              as.Env,
				If:               as.If,
				ContinueOnError:  as.ContinueOnError,
				WorkingDirectory: as.WorkingDirectory,
			}
		}
		baseEnv := actions.InputEnv(metadata, with)
		baseEnv["GITHUB_ACTION_PATH"] = containerActionPath
		return usesStepPlan{Composite: &compositeInvocation{
			Steps:   nestedSteps,
			Outputs: metadata.Outputs,
			BaseEnv: baseEnv,
		}}, nil

	default:
		return usesStepPlan{}, fmt.Errorf("action %s has runs.using=%q, which isn't supported (expected node*, docker, or composite)", step.Uses, metadata.Runs.Using)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/engine/... -v`
Expected: PASS — full engine package suite

- [ ] **Step 5: Commit**

```bash
git add internal/engine/composite_step.go internal/engine/composite_step_test.go internal/engine/uses_step.go internal/engine/uses_step_test.go
git commit -m "feat(engine): execute composite actions via recursive runStep"
```

---

### Task 5: `--local-repository` CLI flag

**Files:**
- Modify: `cmd/mirror/main.go`
- Modify: `internal/engine/uses_step.go`
- Test: `cmd/mirror/main_test.go`, `internal/engine/uses_step_test.go`

**Interfaces:**
- Consumes: `runStepParams.LocalRepositoryOverrides` (Task 3)
- Produces: `type stringSliceFlag []string` (satisfies `flag.Value`), `func parseLocalRepositoryOverrides(values []string) (map[string]string, error)`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/mirror/main_test.go — add these tests
func TestParseLocalRepositoryOverrides_ValidEntries(t *testing.T) {
	overrides, err := parseLocalRepositoryOverrides([]string{"actions/checkout@v4=/tmp/my-checkout"})
	if err != nil {
		t.Fatalf("parseLocalRepositoryOverrides() error = %v", err)
	}
	if overrides["actions/checkout@v4"] != "/tmp/my-checkout" {
		t.Errorf(`overrides["actions/checkout@v4"] = %q, want %q`, overrides["actions/checkout@v4"], "/tmp/my-checkout")
	}
}

func TestParseLocalRepositoryOverrides_MissingEqualsIsError(t *testing.T) {
	_, err := parseLocalRepositoryOverrides([]string{"actions/checkout@v4"})
	if err == nil {
		t.Fatal("parseLocalRepositoryOverrides() error = nil, want error for a value missing '='")
	}
}
```

```go
// internal/engine/uses_step_test.go — add this test
func TestPrepareUsesStep_LocalRepositoryOverrideSkipsFetch(t *testing.T) {
	workspaceDir := t.TempDir()
	overrideDir := t.TempDir()
	actionYML := "name: 'Overridden Action'\nruns:\n  using: 'node20'\n  main: 'index.js'\n"
	if err := os.WriteFile(filepath.Join(overrideDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "some-owner/some-repo@v1"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true
	p := runStepParams{
		RunnerJob:                job,
		WorkspaceDir:             workspaceDir,
		NodeReady:                &nodeReady,
		LocalRepositoryOverrides: map[string]string{"some-owner/some-repo@v1": overrideDir},
	}

	plan, err := prepareUsesStep(context.Background(), p, "id", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v (should use the override, never fetch over the network)", err)
	}
	wantArgs := []string{"/mirror-node/bin/node", "/mirror-actions/id/index.js"}
	if len(plan.Args) != 2 || plan.Args[0] != wantArgs[0] || plan.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", plan.Args, wantArgs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/mirror/... -run TestParseLocalRepositoryOverrides -v` and `go test ./internal/engine/... -run TestPrepareUsesStep_LocalRepositoryOverrideSkipsFetch -v`
Expected: FAIL — `parseLocalRepositoryOverrides` undefined; the override test would otherwise attempt a real network fetch for `some-owner/some-repo@v1` and fail

- [ ] **Step 3: Implement**

```go
// cmd/mirror/main.go — add near the top, after the var block
// stringSliceFlag implements flag.Value for a repeatable string flag —
// the stdlib flag package has no built-in for this. --local-repository
// can be passed multiple times, once per overridden action reference.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseLocalRepositoryOverrides parses --local-repository values into a
// map keyed by "owner/repo@ref", matching act's own flag syntax
// (--local-repository owner/repo[@ref]=local/path, repeatable) — except
// mirror-gha only supports the owner/repo@ref key form, not act's
// additional full-URL form, since mirror-gha doesn't model arbitrary git
// hosts yet.
func parseLocalRepositoryOverrides(values []string) (map[string]string, error) {
	overrides := map[string]string{}
	for _, v := range values {
		key, path, ok := strings.Cut(v, "=")
		if !ok {
			return nil, fmt.Errorf("--local-repository %q must be in the form owner/repo[@ref]=local/path", v)
		}
		overrides[key] = path
	}
	return overrides, nil
}
```

Update `runMode` to carry the parsed overrides, and `runMain`/`runCommand` to parse and thread them:

```go
type runMode struct {
	list                     bool
	graph                    bool
	dryRun                   bool
	workdir                  string
	localRepositoryOverrides map[string]string
}
```

In `runMain`, register the flag and parse it before dispatching to `runCommand`:

```go
	var localRepos stringSliceFlag
	fs.Var(&localRepos, "local-repository", "override local action resolution: owner/repo[@ref]=local/path (repeatable)")
	...
	if err := fs.Parse(args); err != nil {
		return 1
	}

	overrides, err := parseLocalRepositoryOverrides(localRepos)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return 1
	}

	return runCommand(rest[0], runMode{list: *list, graph: *graph, dryRun: *dryRun, workdir: *workdir, localRepositoryOverrides: overrides})
```

In `runCommand`, thread `mode.localRepositoryOverrides` into the `RunWorkflow` call:

```go
	result, err := engine.RunWorkflow(context.Background(), wf, selectBackend, workspaceDir, mode.localRepositoryOverrides)
```

In `internal/engine/uses_step.go`, insert the override check in the Marketplace-fetch branch (the `else` of `if ref.Local { ... } else { ... }`), before the existing `FetchRemote` call:

```go
	var hostSourceDir string
	if ref.Local {
		hostSourceDir = filepath.Join(p.WorkspaceDir, ref.LocalPath)
	} else {
		overrideKey := ref.Owner + "/" + ref.Repo + "@" + ref.Ref
		if localPath, ok := p.LocalRepositoryOverrides[overrideKey]; ok {
			hostSourceDir = localPath
		} else {
			actionDir, err := actions.FetchRemote(ref.Owner, ref.Repo, ref.Ref, cacheRoot)
			if err != nil {
				return usesStepPlan{}, fmt.Errorf("fetch action %s: %w", step.Uses, err)
			}
			hostSourceDir = actionDir
			if ref.Subpath != "" {
				hostSourceDir = filepath.Join(actionDir, ref.Subpath)
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./... -v 2>&1 | tail -40`
Expected: PASS — full repo suite

- [ ] **Step 5: Commit**

```bash
git add cmd/mirror/main.go internal/engine/uses_step.go cmd/mirror/main_test.go internal/engine/uses_step_test.go
git commit -m "feat(cli): add --local-repository flag for overriding action resolution"
```

---

### Task 6: Real end-to-end verification — composite fixture + `--local-repository` demo

**Files:**
- Create: `examples/workflows/actions/greet-composite-action/action.yml`
- Create: `examples/workflows/uses-composite-action.yml`
- Create: `examples/workflows/uses-local-repository.yml`
- Modify: `examples/README.md`, `docs/usage.md`, `CHANGELOG.md`, `docs/design/specs/2026-09-14-mirror-gha-design.md`

**Interfaces:**
- None new — this task is the real, no-fakes proof that Tasks 1-5 work together end-to-end.

- [ ] **Step 1: Create the composite action fixture**

This fixture nests a `run:` step (using `${{ inputs.who-to-greet }}`) and a `uses:` step calling the existing local JS action fixture from the JS actions sub-project (`examples/workflows/actions/hello-action`), then bridges the nested JS action's own output back up through the composite's own `outputs:` block.

```yaml
# examples/workflows/actions/greet-composite-action/action.yml
name: 'Greet Composite Action'
description: 'A composite action nesting a run: step and a uses: step, for testing runs.using: composite with mirror-gha'
inputs:
  who-to-greet:
    description: 'Who to greet'
    required: true
    default: 'World'
outputs:
  greeting:
    description: 'The greeting produced by the nested JS action'
    value: '${{ steps.nested-greet.outputs.greeting }}'
runs:
  using: 'composite'
  steps:
    - id: announce
      shell: 'sh'
      run: 'echo "Composite action starting for ${{ inputs.who-to-greet }}"'
    - id: nested-greet
      uses: './examples/workflows/actions/hello-action'
      with:
        who-to-greet: '${{ inputs.who-to-greet }}'
```

- [ ] **Step 2: Create the composite-action example workflow**

```yaml
# examples/workflows/uses-composite-action.yml
# Demonstrates uses: with a composite action (runs.using: composite) —
# its own nested run:/uses: steps execute for real (including a nested
# JS action), inputs.* resolves the composite's own declared inputs, and
# its outputs: block bridges the nested JS action's own output back up to
# this step's steps.<id>.outputs.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-composite-action.yml
name: uses composite action
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: ./examples/workflows/actions/greet-composite-action
        with:
          who-to-greet: 'mirror-gha'
      - name: use the output
        run: 'echo "Got greeting: ${{ steps.greet.outputs.greeting }}"'
```

- [ ] **Step 3: Run it for real and verify the output**

Run:
```bash
cd /Users/vishnu.prasaath/workspace/mirror-gha
go build -o bin/mirror ./cmd/mirror
./bin/mirror run examples/workflows/uses-composite-action.yml
```
Expected: all steps report `success`; output includes `Composite action starting for mirror-gha`, `Hello, mirror-gha!` (the nested JS action's own output), and `Got greeting: Hello, mirror-gha!` (the composite's own output, bridged from the nested JS action's `steps.nested-greet.outputs.greeting`).

- [ ] **Step 4: Create the `--local-repository` example workflow**

```yaml
# examples/workflows/uses-local-repository.yml
# Demonstrates a Marketplace-style reference (owner/repo@ref) resolved
# from a local directory instead of fetched over the network, via
# --local-repository. This workflow deliberately references a repo/ref
# that doesn't exist on GitHub (mirror-gha/does-not-exist@v1) — the point
# is proving the override is used INSTEAD of a real fetch, not alongside one.
#
# Try it (from the repo root):
#   mirror run --local-repository mirror-gha/does-not-exist@v1=examples/workflows/actions/hello-action examples/workflows/uses-local-repository.yml
name: uses local-repository override
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: mirror-gha/does-not-exist@v1
        with:
          who-to-greet: 'override-works'
      - name: use the output
        run: 'echo "Got greeting: ${{ steps.greet.outputs.greeting }}"'
```

- [ ] **Step 5: Run it for real and verify the override is used**

Run: `./bin/mirror run --local-repository mirror-gha/does-not-exist@v1=examples/workflows/actions/hello-action examples/workflows/uses-local-repository.yml`
Expected: both steps report `success`; output includes `Hello, override-works!` and `Got greeting: Hello, override-works!` — proving `mirror-gha/does-not-exist@v1` (which cannot possibly have been fetched from GitHub) resolved to the local `hello-action` fixture instead.

Also verify running it *without* the flag fails as expected (proving the override, not some fallback, is what made it work):

Run: `./bin/mirror run examples/workflows/uses-local-repository.yml`
Expected: fails with a fetch error for `mirror-gha/does-not-exist@v1` (repo doesn't exist on GitHub).

- [ ] **Step 6: Re-run the full existing example suite to check for regressions**

Run:
```bash
for f in examples/workflows/*.yml; do
  echo "=== $f ==="
  ./bin/mirror run "$f" || echo "FAILED: $f (expected only for uses-local-repository.yml, which requires --local-repository)"
done
```
Expected: every workflow except `uses-local-repository.yml` prints `success` for all its steps; `uses-local-repository.yml` fails exactly as verified in Step 5 (expected without the flag).

- [ ] **Step 7: Update documentation**

In `examples/README.md`, add to the table:

```markdown
| [`uses-composite-action.yml`](workflows/uses-composite-action.yml) | `uses:` with a composite action ([`actions/greet-composite-action`](workflows/actions/greet-composite-action/)) — nested `run:`/`uses:` steps (including a nested JS action), `inputs.*`, and its own `outputs:` bridging a nested step's output back up |
| [`uses-local-repository.yml`](workflows/uses-local-repository.yml) | `--local-repository owner/repo@ref=local/path` — overrides a Marketplace-style reference to resolve from a local directory instead of fetching over the network |
```

Update its "What's not shown here (yet)" paragraph — composite actions are no longer unsupported; only artifacts, caching, matrix `include`/`exclude`, and Windows/macOS runners remain.

In `docs/usage.md`, add a bullet under "What's supported today" (right after the existing `uses:` Docker actions bullet):

```markdown
- **`uses:` composite actions** — a composite action's own `runs.steps:`
  execute through the exact same step-execution logic every other step
  uses (recursively — a composite can nest another composite, capped at
  10 levels deep as a safety net against a self-referencing local action,
  not a real-world limitation). Its own declared inputs are available to
  its nested steps via the `inputs.*` expression context; its own
  `outputs:` (each a `${{ steps.x.outputs.y }}`-style expression,
  evaluated against the composite's own nested step scope) bridge back up
  as the calling `uses:` step's own output. Nested steps never inherit
  the calling workflow/job's `defaults.run.shell`/`working-directory` —
  matches real GitHub Actions' own behavior here. No `runs.using` value a
  real action might declare is rejected as unsupported anymore.
- **`--local-repository owner/repo[@ref]=local/path`** (repeatable) —
  overrides where a Marketplace-style action reference resolves from,
  for testing an in-progress local edit before publishing/tagging it.
  Checked before the network fetch; a hit skips fetching entirely.
```

Update "What's not supported yet" to remove composite actions from the first bullet — it becomes:

```markdown
- Artifacts (`actions/upload-artifact` / `download-artifact`) and caching
  (`actions/cache`)
```

(drop the "Composite actions..." bullet entirely — every `runs.using` a real-world action might declare now works.)

In `CHANGELOG.md`, add under `### Added`:

```markdown
- **`uses:` composite actions.** A composite action's own `runs.steps:`
  execute through the same `runStep` logic every other step uses —
  recursively, so a composite can nest another composite (capped at 10
  levels deep, a mirror-gha-specific safety net act itself doesn't need,
  since its network-fetch model makes a self-referencing action rare;
  mirror-gha's local-path resolution makes it a real, easy-to-hit crash
  risk instead). Its own inputs are available to its nested steps via a
  new `inputs.*` expression context — derived from `INPUT_*` env vars
  generically, not new storage, matching act's own model. Its own
  `outputs:` (`value: ${{ steps.x.outputs.y }}` expressions) evaluate
  against the composite's own nested step scope and surface as the
  calling `uses:` step's output. Verified for real against a fixture
  nesting a `run:` step and a `uses:` step calling the existing local JS
  action fixture, confirming the full input → nested-step → output chain.
- **`--local-repository owner/repo[@ref]=local/path`** (repeatable),
  matching act's own flag — overrides a Marketplace-style action
  reference to resolve from a local directory, checked before the
  network fetch. Verified for real: a reference to a repo that cannot
  exist on GitHub resolves correctly with the flag and fails without it.
```

In `docs/design/specs/2026-09-14-mirror-gha-design.md`, add `**Implemented.**` right after each of the two new sections' headings:

```markdown
## Composite Actions Runtime

**Implemented.**
```

```markdown
## `--local-repository`

**Implemented.**
```

- [ ] **Step 8: Full verification pass**

Run:
```bash
go build ./... && go test ./... && make fmt-check && go vet ./...
```
Expected: build succeeds, all tests pass (network/Docker-dependent ones skip cleanly if unavailable), `fmt-check` and `vet` produce no output/errors.

- [ ] **Step 9: Commit**

```bash
git add examples/workflows/actions/greet-composite-action examples/workflows/uses-composite-action.yml examples/workflows/uses-local-repository.yml examples/README.md docs/usage.md CHANGELOG.md docs/design/specs/2026-09-14-mirror-gha-design.md
git commit -m "feat: verify composite actions and --local-repository end-to-end"
```

## Self-Review Notes

- **Spec coverage:** Every piece of both spec sections maps to a task — composite metadata shape (Task 1), `inputs.*` context (Task 2), the `runStep` extraction the rest of composite support depends on (Task 3), composite dispatch/nested execution/recursion cap/output bridging (Task 4), `--local-repository` flag + resolution-time override (Task 5), real end-to-end proof of both features together (Task 6).
- **Placeholder scan:** No TBD/TODO; every step has complete, real code.
- **Type consistency:** `ActionStep`/`ActionOutput` (Task 1) fields match exactly how Task 4's composite branch in `prepareUsesStep` reads them (`as.ID`, `as.Name`, `as.Run`, `as.Uses`, `as.With`, `as.Shell`, `as.Env`, `as.If`, `as.ContinueOnError`, `as.WorkingDirectory`) and how the fixture in Task 6 is shaped. `runStepParams`'s fields (Task 3) are used identically in `runStep`, `prepareUsesStep`, and `runCompositeSteps` (Task 4) — `RunnerJob`, `WorkspaceDir`, `NodeReady`, `LocalRepositoryOverrides`, `Depth`, `Workflow`, `Job`. `compositeInvocation`'s fields (Task 4: `Steps`, `Outputs`, `BaseEnv`) match exactly how `prepareUsesStep` constructs it and how `runCompositeSteps` consumes it. `usesStepPlan.Composite` (Task 4) is checked identically in `runStep` (Task 3, written to anticipate it) and set in `prepareUsesStep` (Task 4).
- **Known simplifications carried over from the design spec, restated at point of use:** `maxCompositeDepth = 10` is a mirror-gha-specific addition, not act parity (Task 4, Global Constraints); `--local-repository` supports only the `owner/repo@ref` key form, not act's additional full-URL form (Task 5, Global Constraints).
