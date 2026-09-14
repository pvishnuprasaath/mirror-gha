package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"mirror-gha/internal/commands"
	"mirror-gha/internal/runner"
)

// JobResult is the outcome of running every step in a job.
type JobResult struct {
	Conclusion string // "success", "failure", or "skipped"
	Steps      []StepReport
	Outputs    map[string]string
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

// JobRunOptions carries the parts of a job's execution context that come
// from the surrounding workflow (a multi-job DAG's `needs:` outcomes, one
// matrix combination, any locally-supplied `vars`, and the host workspace
// directory to mount) rather than the job definition itself.
type JobRunOptions struct {
	Needs                    map[string]JobOutcome
	Matrix                   MatrixCombination
	Vars                     map[string]string
	WorkspaceDir             string // host directory bind-mounted as the job's workspace
	LocalRepositoryOverrides map[string]string
}

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
		conclusion, outputs, stdout, stderr, err := runCompositeSteps(ctx, p, actx, plan.Composite)
		if err != nil {
			return StepReport{}, fmt.Errorf("composite step %s: %w", id, err)
		}
		actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}
		return StepReport{ID: id, Name: step.Name, Conclusion: conclusion, Stdout: stdout, Stderr: stderr}, nil
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
	// Many real-world actions (including GitHub's own
	// actions/hello-world-javascript-action) still emit outputs via
	// the deprecated stdout-based workflow commands rather than the
	// $GITHUB_OUTPUT file — real GitHub Actions parses both, so this
	// does too. The file-based outputs above win on key conflicts,
	// since that's the current, non-deprecated mechanism.
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

func effectiveShell(step Step, job *Job, wf *Workflow) string {
	if step.Shell != "" {
		return step.Shell
	}
	if job.Defaults != nil && job.Defaults.Run.Shell != "" {
		return job.Defaults.Run.Shell
	}
	if wf.Defaults != nil && wf.Defaults.Run.Shell != "" {
		return wf.Defaults.Run.Shell
	}
	return ""
}

func effectiveWorkingDirectory(step Step, job *Job, wf *Workflow) string {
	if step.WorkingDirectory != "" {
		return step.WorkingDirectory
	}
	if job.Defaults != nil && job.Defaults.Run.WorkingDirectory != "" {
		return job.Defaults.Run.WorkingDirectory
	}
	if wf.Defaults != nil && wf.Defaults.Run.WorkingDirectory != "" {
		return wf.Defaults.Run.WorkingDirectory
	}
	return ""
}
