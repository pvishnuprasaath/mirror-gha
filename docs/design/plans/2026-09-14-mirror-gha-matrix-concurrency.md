# Matrix Concurrency and concurrency: Groups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real, bounded concurrent execution of a job's `strategy.matrix` combinations (honoring `max-parallel`, default 4 when unset), plus `concurrency:` group serialization/`cancel-in-progress` scoped to combinations of the same job.

**Architecture:** `workflow_run.go`'s matrix loop launches one goroutine per combination immediately, each acquiring a semaphore (sized to the effective max-parallel) before actually running `RunJob`. A small standalone `concurrencyScheduler` type enforces `concurrency:` group exclusivity (blocking queue, or cancel-in-progress via a generation-counted per-group `context.CancelFunc`) independently of the semaphore. Each goroutine computes its own fully local result; all shared state (`allSteps`, `lastOutputs`, `stopStartingNew`, the first hard error) is merged under one mutex only after `sync.WaitGroup.Wait()` — never written concurrently, unlike act's own real data race on this exact code path.

**Tech Stack:** Go 1.27 stdlib only — `sync.WaitGroup`, `sync.Mutex`, a semaphore `chan struct{}`, `context.WithCancel`. No new dependency, matching every other sub-project this session.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Matrix Concurrency and `concurrency:` Groups" section.

## Global Constraints

- Independent jobs (no shared `needs:`) stay exactly as sequential as today — this sub-project only adds concurrency within one job's matrix combinations, matching real GitHub Actions' own semantics for `max-parallel`.
- Default max-parallel when unset: 4 (act's own considered choice), further capped by the actual combination count.
- No concurrent mutation of shared state — every combination's goroutine computes a fully local result; merges happen once, under a mutex, after all goroutines finish.
- Fail-fast stops starting new combinations; it never cancels a combination already past its start-check.
- `concurrency:` group name is evaluated **per combination** (it may reference `matrix.*`), using the existing `SubstituteExpressions` mechanism — never a raw, unevaluated template.

---

### Task 1: `Job.Concurrency()`/`Workflow.Concurrency()` parsing

**Files:**
- Modify: `internal/engine/workflow.go`
- Test: `internal/engine/workflow_test.go`

**Interfaces:**
- Produces: `type ConcurrencySpec struct { Group string; CancelInProgress bool }`. `func (j *Job) Concurrency() (*ConcurrencySpec, error)`. `func (wf *Workflow) Concurrency() (*ConcurrencySpec, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/engine/workflow_test.go`:

```go
func TestJob_Concurrency_Absent(t *testing.T) {
	job := &Job{}
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec != nil {
		t.Errorf("Concurrency() = %v, want nil for a job with no concurrency: field", spec)
	}
}

func TestJob_Concurrency_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency: deploy-shared
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "deploy-shared" || spec.CancelInProgress {
		t.Fatalf("Concurrency() = %+v, want Group=deploy-shared CancelInProgress=false", spec)
	}
}

func TestJob_Concurrency_Mapping(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: deploy-${{ matrix.env }}
      cancel-in-progress: true
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "deploy-${{ matrix.env }}" || !spec.CancelInProgress {
		t.Fatalf("Concurrency() = %+v, want Group=deploy-${{ matrix.env }} CancelInProgress=true", spec)
	}
}

func TestWorkflow_Concurrency_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
concurrency: whole-run
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	spec, err := wf.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "whole-run" {
		t.Fatalf("Concurrency() = %+v, want Group=whole-run", spec)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestJob_Concurrency -v`
Expected: FAIL — `job.Concurrency` undefined.

- [ ] **Step 3: Implement**

In `internal/engine/workflow.go`, add (near `PermissionsSpec`/`decodePermissions`):

```go
// ConcurrencySpec is a workflow's or job's `concurrency:` field — either
// a bare group-name string (CancelInProgress defaults false) or a mapping
// with group/cancel-in-progress. The group string may itself contain
// ${{ }} expressions (e.g. referencing matrix.*) — resolving those is the
// caller's job (see RunWorkflow's per-combination evaluation), not this
// type's; this only carries the raw, possibly-templated string.
type ConcurrencySpec struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

func decodeConcurrency(node yaml.Node) (*ConcurrencySpec, error) {
	switch node.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		var group string
		if err := node.Decode(&group); err != nil {
			return nil, fmt.Errorf("concurrency: %w", err)
		}
		return &ConcurrencySpec{Group: group}, nil
	case yaml.MappingNode:
		spec := &ConcurrencySpec{}
		if err := node.Decode(spec); err != nil {
			return nil, fmt.Errorf("concurrency: %w", err)
		}
		return spec, nil
	default:
		return nil, fmt.Errorf("concurrency: must be a string or a mapping")
	}
}
```

Add `RawConcurrency yaml.Node \`yaml:"concurrency"\`` to both the `Job` and `Workflow` structs, and:

```go
// Concurrency resolves the job's `concurrency:` field. Returns nil, nil
// when the job has no concurrency: field at all.
func (j *Job) Concurrency() (*ConcurrencySpec, error) {
	return decodeConcurrency(j.RawConcurrency)
}
```

```go
// Concurrency resolves the workflow's top-level `concurrency:` field.
// mirror-gha parses and validates it but treats it as an intentional
// no-op: real GitHub Actions uses this to serialize/cancel separate
// workflow RUNS sharing a group, a concept with no meaning in a tool
// that only ever executes one run per invocation.
func (wf *Workflow) Concurrency() (*ConcurrencySpec, error) {
	return decodeConcurrency(wf.RawConcurrency)
}
```

- [ ] **Step 4: Run tests, then the full engine suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestJob_Concurrency|TestWorkflow_Concurrency" -v`
Expected: PASS (all 4).

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l internal/engine/workflow.go internal/engine/workflow_test.go && go test ./internal/engine/... 2>&1 | tail -10`
Expected: clean, all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/workflow.go internal/engine/workflow_test.go
git commit -m "feat(engine): parse workflow/job concurrency: field"
```

---

### Task 2: `concurrencyScheduler` — blocking queue and cancel-in-progress, tested in isolation

**Files:**
- Create: `internal/engine/concurrency_scheduler.go`
- Test: `internal/engine/concurrency_scheduler_test.go`

**Interfaces:**
- Produces: `type concurrencyScheduler struct{...}`, `func newConcurrencyScheduler() *concurrencyScheduler`, `func (s *concurrencyScheduler) acquire(ctx context.Context, group string, cancelInProgress bool) (runCtx context.Context, release func())`. Consumed by Task 3's `workflow_run.go` rewrite.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/concurrency_scheduler_test.go`:

```go
package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestConcurrencyScheduler_EmptyGroupNeverBlocks(t *testing.T) {
	s := newConcurrencyScheduler()
	ctx1, release1 := s.acquire(context.Background(), "", false)
	ctx2, release2 := s.acquire(context.Background(), "", false)
	if ctx1 != context.Background() || ctx2 != context.Background() {
		t.Error("acquire(\"\") should return the same context unchanged")
	}
	release1()
	release2()
}

func TestConcurrencyScheduler_BlockingQueueSerializesSameGroup(t *testing.T) {
	s := newConcurrencyScheduler()

	var mu sync.Mutex
	var order []string

	_, release1 := s.acquire(context.Background(), "g", false)

	done := make(chan struct{})
	go func() {
		_, release2 := s.acquire(context.Background(), "g", false)
		mu.Lock()
		order = append(order, "second-acquired")
		mu.Unlock()
		release2()
		close(done)
	}()

	// Give the goroutine a chance to attempt (and be blocked by) acquire.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	order = append(order, "first-released")
	mu.Unlock()
	release1()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never completed after first released")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first-released" || order[1] != "second-acquired" {
		t.Errorf("order = %v, want [first-released, second-acquired] (second acquire must wait for the first release)", order)
	}
}

func TestConcurrencyScheduler_DifferentGroupsNeverBlockEachOther(t *testing.T) {
	s := newConcurrencyScheduler()
	_, release1 := s.acquire(context.Background(), "g1", false)
	defer release1()

	done := make(chan struct{})
	go func() {
		_, release2 := s.acquire(context.Background(), "g2", false)
		release2()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("acquiring a different group blocked — groups must be independent")
	}
}

func TestConcurrencyScheduler_CancelInProgressCancelsPreviousHolder(t *testing.T) {
	s := newConcurrencyScheduler()
	runCtx1, release1 := s.acquire(context.Background(), "g", true)
	defer release1()

	if runCtx1.Err() != nil {
		t.Fatalf("runCtx1 already cancelled before a second acquire happened")
	}

	runCtx2, release2 := s.acquire(context.Background(), "g", true)
	defer release2()

	select {
	case <-runCtx1.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("acquiring the same group again with cancel-in-progress must cancel the previous holder's context")
	}
	if runCtx2.Err() != nil {
		t.Errorf("runCtx2 should not be cancelled, got %v", runCtx2.Err())
	}
}

func TestConcurrencyScheduler_StaleReleaseDoesNotClobberNewerHolder(t *testing.T) {
	s := newConcurrencyScheduler()
	_, release1 := s.acquire(context.Background(), "g", true)
	_, release2 := s.acquire(context.Background(), "g", true) // cancels release1's ctx

	release1() // a delayed release from the already-superseded first holder

	// A third acquire must still correctly cancel the SECOND holder (not
	// be confused by the stale first release already having run).
	runCtx3, release3 := s.acquire(context.Background(), "g", true)
	defer release3()
	_ = runCtx3
	release2()
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestConcurrencyScheduler -v`
Expected: FAIL — `newConcurrencyScheduler` undefined.

- [ ] **Step 3: Implement**

Create `internal/engine/concurrency_scheduler.go`:

```go
package engine

import (
	"context"
	"sync"
)

// concurrencyScheduler enforces concurrency: group exclusivity among
// matrix combinations of the same job. act has no equivalent at all
// (confirmed via source — the field doesn't exist there), so this is
// mirror-gha's own design rather than a port.
//
// cancel-in-progress: false acquires a per-group 1-buffered channel as a
// mutex — a second combination in the same group blocks until the first
// releases (a real queue). cancel-in-progress: true instead cancels the
// group's currently-running combination's context before starting the
// new one, using a per-group generation counter so a delayed release
// from an already-superseded holder can never incorrectly clear a newer
// holder's state — a real correctness hazard a naive "one cancel func per
// group" implementation would hit under genuine concurrency.
type concurrencyScheduler struct {
	mu     sync.Mutex
	slots  map[string]chan struct{}
	gen    map[string]int
	cancel map[string]context.CancelFunc
}

func newConcurrencyScheduler() *concurrencyScheduler {
	return &concurrencyScheduler{
		slots:  map[string]chan struct{}{},
		gen:    map[string]int{},
		cancel: map[string]context.CancelFunc{},
	}
}

// acquire blocks (cancelInProgress false) or preempts the group's
// currently-running combination (cancelInProgress true). group == ""
// means no concurrency: field applies — ctx is returned unchanged and
// release is a no-op. The caller must always call release when done,
// exactly once, regardless of which path was taken.
func (s *concurrencyScheduler) acquire(ctx context.Context, group string, cancelInProgress bool) (runCtx context.Context, release func()) {
	if group == "" {
		return ctx, func() {}
	}

	if !cancelInProgress {
		s.mu.Lock()
		ch, ok := s.slots[group]
		if !ok {
			ch = make(chan struct{}, 1)
			s.slots[group] = ch
		}
		s.mu.Unlock()

		select {
		case ch <- struct{}{}:
		case <-ctx.Done():
			return ctx, func() {}
		}
		return ctx, func() { <-ch }
	}

	s.mu.Lock()
	if prevCancel, ok := s.cancel[group]; ok {
		prevCancel()
	}
	s.gen[group]++
	myGen := s.gen[group]
	newCtx, cancel := context.WithCancel(ctx)
	s.cancel[group] = cancel
	s.mu.Unlock()

	return newCtx, func() {
		s.mu.Lock()
		if s.gen[group] == myGen {
			delete(s.cancel, group)
			delete(s.gen, group)
		}
		s.mu.Unlock()
		cancel()
	}
}
```

- [ ] **Step 4: Run tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestConcurrencyScheduler -v -race`
Expected: PASS (all 5) — run with `-race` specifically since this is concurrency-sensitive code; a real data race here would otherwise be easy to miss.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/concurrency_scheduler.go internal/engine/concurrency_scheduler_test.go
git commit -m "feat(engine): add concurrencyScheduler for concurrency: group semantics"
```

---

### Task 3: Real bounded concurrent matrix execution in `RunWorkflow`

**Files:**
- Modify: `internal/engine/workflow_run.go`
- Modify: `internal/engine/workflow_run_test.go`

**Interfaces:**
- Consumes: `Job.Concurrency()` (Task 1), `concurrencyScheduler`/`acquire` (Task 2), `SubstituteExpressions` (existing, `internal/engine/expr.go`).

- [ ] **Step 1: Fix the existing fail-fast test to keep testing what it was written to test**

In `internal/engine/workflow_run_test.go`, `TestRunWorkflow_MatrixFailFastStopsRemainingCombinations`'s job currently has:

```go
				Strategy: &Strategy{
					Matrix: map[string]interface{}{
						"version": []interface{}{"1", "2", "3"},
					},
				},
```

Change it to force strictly sequential execution (its 3 combinations would otherwise all launch in a single concurrent wave under the new default max-parallel of 4, and none would be observably "skipped" — a real behavior change from this sub-project, not a bug in the test being fixed incidentally):

```go
				Strategy: &Strategy{
					MaxParallel: 1,
					Matrix: map[string]interface{}{
						"version": []interface{}{"1", "2", "3"},
					},
				},
```

- [ ] **Step 2: Write the failing tests for real concurrency and group semantics**

Add to `internal/engine/workflow_run_test.go`:

```go
func TestRunWorkflow_MatrixCombinationsRunConcurrently(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"build": {
				RunsOn: "ubuntu-latest",
				Strategy: &Strategy{
					Matrix: map[string]interface{}{
						"n": []interface{}{"1", "2", "3", "4"},
					},
				},
				Steps: []Step{{ID: "s", Run: "sleep"}},
			},
		},
	}

	start := time.Now()
	result, err := RunWorkflow(context.Background(), wf, sleepySucceedSelector, t.TempDir(), nil, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["build"].Conclusion != "success" {
		t.Fatalf("Conclusion = %q, want success", result.Jobs["build"].Conclusion)
	}
	// 4 combinations x 150ms each, default max-parallel 4 -> all run at
	// once. A generous threshold (well under the 600ms strictly-sequential
	// total) proves genuine concurrency without being timing-brittle.
	if elapsed > 400*time.Millisecond {
		t.Errorf("elapsed = %s, want well under 600ms (4x150ms sequential) — combinations should run concurrently", elapsed)
	}
}

func TestRunWorkflow_ConcurrencyGroupSerializesSameGroupCombinations(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"build": {
				RunsOn: "ubuntu-latest",
				Strategy: &Strategy{
					Matrix: map[string]interface{}{
						"n": []interface{}{"1", "2", "3"},
					},
				},
				Steps: []Step{{ID: "s", Run: "sleep"}},
			},
		},
	}
	wf.Jobs["build"] = setJobConcurrency(t, wf.Jobs["build"], "shared-group", false)

	start := time.Now()
	result, err := RunWorkflow(context.Background(), wf, sleepySucceedSelector, t.TempDir(), nil, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["build"].Conclusion != "success" {
		t.Fatalf("Conclusion = %q, want success", result.Jobs["build"].Conclusion)
	}
	// All 3 combinations share one static concurrency: group, so despite
	// max-parallel allowing all 3 at once, they must serialize to roughly
	// 3x150ms sequential — proving the group actually blocked them.
	if elapsed < 400*time.Millisecond {
		t.Errorf("elapsed = %s, want at least ~450ms (3x150ms serialized by the shared concurrency: group)", elapsed)
	}
}

// setJobConcurrency is a test helper that parses a minimal workflow just
// to get a real RawConcurrency yaml.Node for the given group/
// cancel-in-progress values, then copies that node onto job — there's no
// public constructor for ConcurrencySpec's underlying yaml.Node, matching
// how container:/environment: dual-shape fields are already exercised via
// real YAML parsing elsewhere in this test file rather than hand-built.
func setJobConcurrency(t *testing.T, job Job, group string, cancelInProgress bool) Job {
	t.Helper()
	doc := "name: t\non: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n    concurrency:\n      group: " + group + "\n      cancel-in-progress: " + boolStr(cancelInProgress) + "\n    steps: []\n"
	wf, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job.RawConcurrency = wf.Jobs["x"].RawConcurrency
	return job
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
```

Extend the existing `alwaysSucceedJob`/`alwaysSucceedBackend` (defined earlier in this same file, confirmed current shape: `alwaysSucceedJob{dir, workspaceDir string}` with `Exec` returning `runner.StepResult{ExitCode: 0, Stdout: "ok\n"}` unconditionally) with an opt-in sleep, rather than duplicating them. Change `alwaysSucceedJob`:

```go
type alwaysSucceedJob struct {
	dir          string
	workspaceDir string
	sleep        time.Duration
}
```

Change its `Exec`:

```go
func (j *alwaysSucceedJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	if j.sleep > 0 {
		time.Sleep(j.sleep)
	}
	return runner.StepResult{ExitCode: 0, Stdout: "ok\n"}, nil
}
```

Add a sleepy backend and selector after `alwaysSucceedBackend`'s existing definition:

```go
// sleepySucceedBackend is alwaysSucceedBackend with a configurable sleep
// per Exec call — used to prove real concurrent execution via wall-clock
// timing (N combinations completing in much less than N x sleep time).
type sleepySucceedBackend struct{ sleep time.Duration }

func (b sleepySucceedBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *runner.ContainerSpec, services map[string]runner.ContainerSpec) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &alwaysSucceedJob{dir: dir, workspaceDir: hostWorkspaceDir, sleep: b.sleep}, nil
}

func sleepySucceedSelector(runsOn string) (runner.Backend, error) {
	return sleepySucceedBackend{sleep: 150 * time.Millisecond}, nil
}
```

Add `"time"` to the file's imports.

- [ ] **Step 3: Run tests to verify the new ones fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestRunWorkflow_MatrixCombinationsRunConcurrently|TestRunWorkflow_ConcurrencyGroupSerializes" -v`
Expected: FAIL (or hang/timeout under the old strictly-sequential code, since 4 combinations x 150ms sequential = 600ms, over the 400ms threshold in the first test) — if it hangs, that itself confirms sequential execution; Ctrl-C and continue, the implementation step fixes this.

- [ ] **Step 4: Rewrite `RunWorkflow`'s matrix loop**

Replace the matrix-handling block inside `RunWorkflow` (from `combos, err := ExpandMatrix(job.Strategy)` through the `jobResult := &JobResult{...}` line, inclusive) in `internal/engine/workflow_run.go` with:

```go
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
		comboOutputs := make([]map[string]string, len(combos))
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
				comboOutputs[i] = jr.Outputs
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
```

Add `"sync"` to the file's imports.

Update the package doc comment on `WorkflowResult` (currently says Outputs reflect "the last combination to finish... reproduced deliberately") to:

```go
// WorkflowResult is the outcome of running every job in a workflow, keyed
// by job name. A matrixed job's Steps is the concatenation of every
// combination's steps, in combination-index order (not completion order,
// for deterministic output) — its Outputs reflect whichever combination's
// completion happens to write last under real concurrent execution, a
// genuine race that matches real GitHub Actions' own documented
// nondeterminism for matrixed job outputs, not an approximation of it.
type WorkflowResult struct {
```

- [ ] **Step 5: Run the tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestRunWorkflow" -v -race`
Expected: PASS, including both new concurrency tests and the fixed fail-fast test. Run with `-race` — this is exactly the kind of change a race detector exists for.

- [ ] **Step 6: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... -race 2>&1 | tail -25`
Expected: clean build, no unformatted files, no vet errors, every package `ok` under `-race`.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/workflow_run.go internal/engine/workflow_run_test.go
git commit -m "feat(engine): real concurrent matrix execution honoring max-parallel and concurrency: groups"
```

---

### Task 4: Real end-to-end verification and docs

**Files:**
- Create: `examples/workflows/matrix-concurrency.yml`
- Modify: `docs/usage.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the example workflow**

Create `examples/workflows/matrix-concurrency.yml`:

```yaml
# Demonstrates real concurrent matrix execution (strategy.max-parallel)
# and concurrency: group serialization within one job's matrix.
#
# Try it:
#   time mirror run examples/workflows/matrix-concurrency.yml
name: matrix concurrency
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      max-parallel: 3
      matrix:
        version: ["1", "2", "3"]
    steps:
      - name: simulate real work
        run: sleep 2 && echo "combination ${{ matrix.version }} done"
```

- [ ] **Step 2: Run it for real, timing it**

Run: `export PATH="/opt/homebrew/bin:$PATH" && time go run ./cmd/mirror run examples/workflows/matrix-concurrency.yml 2>&1 | tail -20`
Expected: all 3 combinations succeed; real wall-clock time is close to ~2 seconds (3 combinations running concurrently, each sleeping 2s) rather than ~6 seconds (what strictly sequential execution would take) — this is the real proof that concurrency actually reduces wall-clock time for genuine Docker container execution, not just in the unit tests' fake backend.

If the timing doesn't show real concurrency (i.e., it takes ~6s), root-cause it for real — likely candidates: `maxParallel` computed incorrectly, the semaphore channel not actually being used correctly to let goroutines run in parallel, or Docker itself serializing container starts for an unrelated reason (check `docker ps` during the run to confirm 3 containers are genuinely running simultaneously).

- [ ] **Step 3: Update docs/usage.md**

Add a new bullet to "What's supported today" (near the existing `strategy.matrix` bullet):

```
- **Real concurrent matrix execution** — a job's matrix combinations run
  concurrently, bounded by `strategy.max-parallel` (default 4 when
  unset, further capped by the actual combination count — matching
  act's own considered default, chosen to respect a single local Docker
  daemon's real resource limits rather than GitHub's own effectively
  unbounded cloud-runner default). Independent jobs (no shared `needs:`)
  still run sequentially — real GitHub Actions has no equivalent
  parallelism knob for jobs either, only for matrix combinations, so
  this isn't a gap. Fail-fast stops *starting* new combinations after a
  failure but doesn't cancel ones already running, extending mirror-gha's
  existing "skip not-yet-started, don't abort in-flight" semantic from
  steps to combinations. `concurrency:` (job-level; workflow-level is
  parsed but an intentional no-op — it exists in real GitHub Actions to
  serialize separate workflow *runs*, a concept with no meaning in a
  tool that only ever executes one run per invocation) serializes or
  cancels-in-progress combinations of the same job whose evaluated group
  name (which may reference `matrix.*`) coincides — a real gap in act
  itself (confirmed via source: the field doesn't exist there at all),
  so this is mirror-gha's own design rather than a port.
```

- [ ] **Step 4: Update CHANGELOG.md**

Add to the `### Added` section, above the most recent entry:

```markdown
- **Real concurrent matrix execution and `concurrency:` groups.** Checked
  against act's own job/matrix scheduler (`pkg/runner/runner.go`'s
  `NewPlanExecutor`, `pkg/common/executor.go`'s `NewParallelExecutor`)
  rather than guessed, with two deliberate corrections: act has a genuine
  unguarded data race on matrix combinations of the same job (every
  combination shares one `*model.Job` pointer, mutated concurrently with
  zero mutex) — mirror-gha instead computes each combination's result
  fully independently and merges under a mutex only after all finish.
  act's own default max-parallel-when-unset (4, chosen to respect local
  resource limits rather than GitHub's effectively unbounded cloud
  default) is adopted directly, since mirror-gha shares that same
  local-Docker-daemon constraint. `concurrency:` doesn't exist in act at
  all (confirmed via source, zero hits) — this is mirror-gha's own
  design: job-level groups (evaluated per matrix combination, since the
  group name may reference `matrix.*`) serialize via a blocking queue, or
  cancel-in-progress via a generation-counted per-group cancellation to
  avoid a real correctness hazard (a delayed release from an
  already-superseded holder incorrectly clearing a newer holder's
  state) a naive one-cancel-func-per-group implementation would hit.
  Workflow-level `concurrency:` is parsed but an intentional no-op — it
  serializes separate workflow *runs* in real GitHub Actions, a concept
  with no meaning in a single-run-per-invocation tool. Scope corrected
  from the original ask during design: `max-parallel` only ever governs
  matrix combinations, never independent jobs — real GitHub Actions has
  no job-level parallelism knob either, so jobs stay sequential, matching
  reality rather than leaving a gap. Verified for real: a 3-combination
  matrix with `max-parallel: 3`, each sleeping 2 real seconds, completes
  in ~2 seconds wall-clock (genuine concurrent Docker container
  execution), not the ~6 seconds strictly sequential execution would
  take.
```

- [ ] **Step 5: Final full-suite check**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party && go vet ./... && go test ./... -race 2>&1 | tail -25`
Expected: clean build, no unformatted files, no vet errors, every package `ok` under `-race`.

- [ ] **Step 6: Commit**

```bash
git add examples/workflows/matrix-concurrency.yml docs/usage.md CHANGELOG.md
git commit -m "docs: document real matrix concurrency and concurrency: groups, add example workflow"
```

(If Step 2 surfaced and required a real bug fix, that fix should already be committed as its own commit before this one, with its own regression test — same discipline as every prior sub-project's final verification task this session.)
