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
//
// All steps run inside the same job-scoped environment (one container for
// the whole job, not one per step) so filesystem state — checked-out
// files, installed packages, PATH changes — persists step to step, the
// same way real GitHub Actions and act both work.
func RunJob(ctx context.Context, wf *Workflow, job *Job, backend runner.Backend) (*JobResult, error) {
	actx := NewContext(wf, job)
	result := &JobResult{Conclusion: "success"}

	runnerJob, err := backend.StartJob(ctx)
	if err != nil {
		return nil, fmt.Errorf("start job: %w", err)
	}
	defer runnerJob.Stop(ctx)

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

		stepResult, err := runnerJob.Exec(ctx, runner.StepSpec{
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
