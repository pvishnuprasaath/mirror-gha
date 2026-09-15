package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"mirror-gha/internal/runner"
)

// WorkflowResult is the outcome of running every job in a workflow, keyed
// by job name. A matrixed job's Steps is the concatenation of every
// combination's steps, in combination-index order (not completion order,
// for deterministic output) — its Outputs reflect whichever combination's
// completion happens to write last under real concurrent execution, a
// genuine race that matches real GitHub Actions' own documented
// nondeterminism for matrixed job outputs, not an approximation of it.
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
// workspaceDir is bind-mounted into every job as its workspace (see
// runner.Backend.StartJob) — typically the directory `mirror run` was
// invoked from, or an explicit override.
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector, workspaceDir string, localRepositoryOverrides map[string]string, vars map[string]string, extraEnv map[string]string) (*WorkflowResult, error) {
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspaceDir must not be empty")
	}

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

		maxParallel := 4
		if job.Strategy != nil && job.Strategy.MaxParallel > 0 {
			maxParallel = job.Strategy.MaxParallel
		}
		if len(combos) < maxParallel {
			maxParallel = len(combos)
		}
		if maxParallel < 1 {
			maxParallel = 1
		}

		jobConcurrency, err := job.Concurrency()
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", name, err)
		}

		comboSteps := make([][]StepReport, len(combos))
		comboFailed := make([]bool, len(combos))
		comboSkipped := make([]bool, len(combos))

		var mu sync.Mutex
		conclusion := "success"
		var lastOutputs map[string]string
		stopStartingNew := false
		var firstErr error

		scheduler := newConcurrencyScheduler()
		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup

		for i, combo := range combos {
			wg.Add(1)
			go func(i int, combo MatrixCombination) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				mu.Lock()
				skip := stopStartingNew
				mu.Unlock()
				if skip {
					comboSkipped[i] = true
					return
				}

				runCtx := ctx
				var cancel context.CancelFunc
				if job.TimeoutMinutes > 0 {
					runCtx, cancel = context.WithTimeout(ctx, time.Duration(job.TimeoutMinutes*float64(time.Minute)))
					defer cancel()
				}

				var release func()
				if jobConcurrency != nil {
					comboCtx := NewContext(wf, &job)
					comboCtx.Matrix = combo
					comboCtx.Vars = vars
					group, err := SubstituteExpressions(jobConcurrency.Group, comboCtx)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("job %s%s: concurrency group: %w", name, MatrixSuffix(combo), err)
						}
						mu.Unlock()
						return
					}
					runCtx, release = scheduler.acquire(runCtx, group, jobConcurrency.CancelInProgress)
				} else {
					release = func() {}
				}
				defer release()

				backend, err := selectBackend(job.RunsOn)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("job %s%s: %w", name, MatrixSuffix(combo), err)
					}
					mu.Unlock()
					return
				}

				jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
					Needs:                    outcomes,
					Matrix:                   combo,
					WorkspaceDir:             workspaceDir,
					LocalRepositoryOverrides: localRepositoryOverrides,
					Vars:                     vars,
					ExtraEnv:                 extraEnv,
					JobID:                    name,
				})

				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("job %s%s: %w", name, MatrixSuffix(combo), err)
					}
					return
				}
				comboSteps[i] = jr.Steps
				lastOutputs = jr.Outputs
				if jr.Conclusion != "success" {
					comboFailed[i] = true
					if failFast {
						stopStartingNew = true
					}
				}
			}(i, combo)
		}
		wg.Wait()

		if firstErr != nil {
			return nil, firstErr
		}

		var allSteps []StepReport
		for i, combo := range combos {
			if comboSkipped[i] {
				allSteps = append(allSteps, StepReport{
					Name:       name + MatrixSuffix(combo),
					Conclusion: "skipped",
				})
				continue
			}
			allSteps = append(allSteps, comboSteps[i]...)
			if comboFailed[i] {
				conclusion = "failure"
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
