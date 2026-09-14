package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"mirror-gha/internal/runner"
)

// WorkflowResult is the outcome of running every job in a workflow, keyed
// by job name. A matrixed job's Steps is the concatenation of every
// combination's steps, in the order they ran; its Outputs reflect the
// *last* combination to finish — a known GitHub Actions gotcha for
// matrixed job outputs, reproduced deliberately rather than accidentally.
type WorkflowResult struct {
	Jobs  map[string]*JobResult
	Order []string // job names in the order they ran (topological, deterministic)
}

// BackendSelector maps a `runs-on` label to a Backend — the same signature
// as runner.SelectBackend, taken as a parameter so this package doesn't
// depend on a specific backend implementation.
type BackendSelector func(runsOn string) (runner.Backend, error)

// RunWorkflow executes every job in wf in dependency order (`needs:`),
// expanding each job's `strategy.matrix` combinations and propagating each
// job's result/outputs to dependents via the `needs` context. A job whose
// `needs:` didn't all succeed is reported as "skipped", not run.
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector) (*WorkflowResult, error) {
	order, err := TopoSortJobs(wf.Jobs)
	if err != nil {
		return nil, err
	}

	outcomes := map[string]JobOutcome{}
	result := &WorkflowResult{Jobs: map[string]*JobResult{}, Order: order}

	for _, name := range order {
		job := wf.Jobs[name]

		skip := false
		for _, dep := range job.Needs {
			if outcomes[dep].Result != "success" {
				skip = true
				break
			}
		}
		if skip {
			result.Jobs[name] = &JobResult{Conclusion: "skipped"}
			outcomes[name] = JobOutcome{Result: "skipped"}
			continue
		}

		combos, err := ExpandMatrix(job.Strategy)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", name, err)
		}

		failFast := true
		if job.Strategy != nil && job.Strategy.FailFast != nil {
			failFast = *job.Strategy.FailFast
		}

		conclusion := "success"
		var allSteps []StepReport
		var lastOutputs map[string]string
		stopStartingNew := false

		for _, combo := range combos {
			if stopStartingNew {
				allSteps = append(allSteps, StepReport{
					Name:       name + MatrixSuffix(combo),
					Conclusion: "skipped",
				})
				continue
			}

			backend, err := selectBackend(job.RunsOn)
			if err != nil {
				return nil, fmt.Errorf("job %s%s: %w", name, MatrixSuffix(combo), err)
			}

			runCtx := ctx
			var cancel context.CancelFunc
			if job.TimeoutMinutes > 0 {
				runCtx, cancel = context.WithTimeout(ctx, time.Duration(job.TimeoutMinutes*float64(time.Minute)))
			}

			jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
				Needs:  outcomes,
				Matrix: combo,
			})
			if cancel != nil {
				cancel()
			}
			if err != nil {
				return nil, fmt.Errorf("job %s%s: %w", name, MatrixSuffix(combo), err)
			}

			allSteps = append(allSteps, jr.Steps...)
			lastOutputs = jr.Outputs
			if jr.Conclusion != "success" {
				conclusion = "failure"
				if failFast {
					stopStartingNew = true
				}
			}
		}

		jobResult := &JobResult{Conclusion: conclusion, Steps: allSteps, Outputs: lastOutputs}
		result.Jobs[name] = jobResult
		outcomes[name] = JobOutcome{Result: conclusion, Outputs: lastOutputs}
	}

	return result, nil
}

// TopoSortJobs orders jobs so every job appears after everything it
// `needs:`. Job names are visited in sorted order so the result is
// deterministic across runs of the same workflow.
func TopoSortJobs(jobs map[string]Job) ([]string, error) {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := map[string]int{}
	var order []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("circular job dependency involving %q", name)
		}
		job, ok := jobs[name]
		if !ok {
			return fmt.Errorf("unknown job %q", name)
		}
		state[name] = visiting
		for _, dep := range job.Needs {
			if _, ok := jobs[dep]; !ok {
				return fmt.Errorf("job %q needs unknown job %q", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = done
		order = append(order, name)
		return nil
	}

	names := make([]string, 0, len(jobs))
	for n := range jobs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func MatrixSuffix(combo MatrixCombination) string {
	if len(combo) == 0 {
		return ""
	}
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, combo[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
