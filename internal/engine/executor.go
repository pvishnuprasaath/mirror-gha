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
	Needs        map[string]JobOutcome
	Matrix       MatrixCombination
	Vars         map[string]string
	WorkspaceDir string // host directory bind-mounted as the job's workspace
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

	for i, step := range job.Steps {
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step-%d", i)
		}

		if step.Run != "" && step.Uses != "" {
			return nil, fmt.Errorf("step %s: cannot set both run: and uses:", id)
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

		filesDir, err := os.MkdirTemp(runnerJob.FilesRoot(), "step-")
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
		env["GITHUB_WORKSPACE"] = runnerJob.WorkspacePath()

		workingDirectory := effectiveWorkingDirectory(step, job, wf)
		if workingDirectory == "" {
			workingDirectory = runnerJob.WorkspacePath()
		}

		stepCtx := ctx
		var cancel context.CancelFunc
		if step.TimeoutMinutes > 0 {
			stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMinutes*float64(time.Minute)))
		}

		stepResult, err := runnerJob.Exec(stepCtx, runner.StepSpec{
			Command:          command,
			Shell:            effectiveShell(step, job, wf),
			Env:              env,
			WorkingDirectory: workingDirectory,
			FilesDir:         filesDir,
		})
		if cancel != nil {
			cancel()
		}
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
