package engine

import (
	"context"
	"os"
	"testing"

	"mirror-gha/internal/runner"
)

// alwaysSucceedBackend is a fake Backend for workflow-orchestration tests
// where the point is to verify DAG/needs/matrix wiring, not real step
// execution (that's covered by the real Docker backend tests and by
// executor_test.go's scripted fakeBackend).
type alwaysSucceedBackend struct{}

func (alwaysSucceedBackend) StartJob(ctx context.Context) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &alwaysSucceedJob{dir: dir}, nil
}

type alwaysSucceedJob struct{ dir string }

func (j *alwaysSucceedJob) FilesRoot() string { return j.dir }
func (j *alwaysSucceedJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	return runner.StepResult{ExitCode: 0, Stdout: "ok\n"}, nil
}
func (j *alwaysSucceedJob) Stop(ctx context.Context) error { return os.RemoveAll(j.dir) }

// alwaysFailBackend fails every step it executes.
type alwaysFailBackend struct{}

func (alwaysFailBackend) StartJob(ctx context.Context) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &alwaysFailJob{dir: dir}, nil
}

type alwaysFailJob struct{ dir string }

func (j *alwaysFailJob) FilesRoot() string { return j.dir }
func (j *alwaysFailJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	return runner.StepResult{ExitCode: 1}, nil
}
func (j *alwaysFailJob) Stop(ctx context.Context) error { return os.RemoveAll(j.dir) }

func succeedSelector(runsOn string) (runner.Backend, error) {
	return alwaysSucceedBackend{}, nil
}

func failSelector(runsOn string) (runner.Backend, error) {
	return alwaysFailBackend{}, nil
}

func TestRunWorkflow_RunsInDependencyOrder(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"b": {RunsOn: "ubuntu-latest", Needs: StringOrSlice{"a"}, Steps: []Step{{ID: "s", Run: "echo b"}}},
			"a": {RunsOn: "ubuntu-latest", Steps: []Step{{ID: "s", Run: "echo a"}}},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, succeedSelector)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if len(result.Order) != 2 || result.Order[0] != "a" || result.Order[1] != "b" {
		t.Fatalf("Order = %v, want [a b]", result.Order)
	}
	if result.Jobs["a"].Conclusion != "success" || result.Jobs["b"].Conclusion != "success" {
		t.Fatalf("expected both jobs to succeed: a=%s b=%s", result.Jobs["a"].Conclusion, result.Jobs["b"].Conclusion)
	}
}

func TestRunWorkflow_SkipsJobWhenNeedFails(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {RunsOn: "ubuntu-latest", Steps: []Step{{ID: "s", Run: "exit 1"}}},
			"b": {RunsOn: "ubuntu-latest", Needs: StringOrSlice{"a"}, Steps: []Step{{ID: "s", Run: "echo b"}}},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, failSelector)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["a"].Conclusion != "failure" {
		t.Fatalf("Jobs[a].Conclusion = %q, want failure", result.Jobs["a"].Conclusion)
	}
	if result.Jobs["b"].Conclusion != "skipped" {
		t.Fatalf("Jobs[b].Conclusion = %q, want skipped (its need failed)", result.Jobs["b"].Conclusion)
	}
}

func TestRunWorkflow_PropagatesJobOutputsToNeeds(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {
				RunsOn:  "ubuntu-latest",
				Steps:   []Step{{ID: "s", Run: "echo a"}},
				Outputs: map[string]string{"greeting": "hello-from-a"},
			},
			"b": {
				RunsOn: "ubuntu-latest",
				Needs:  StringOrSlice{"a"},
				Steps: []Step{
					{ID: "s", Run: "echo b", If: "${{ needs.a.outputs.greeting != 'hello-from-a' }}"},
				},
			},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, succeedSelector)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["a"].Outputs["greeting"] != "hello-from-a" {
		t.Fatalf("Jobs[a].Outputs[greeting] = %q, want %q", result.Jobs["a"].Outputs["greeting"], "hello-from-a")
	}
	if result.Jobs["b"].Steps[0].Conclusion != "skipped" {
		t.Fatalf("Jobs[b].Steps[0].Conclusion = %q, want skipped (needs.a.outputs.greeting should equal 'hello-from-a', making the if: false)", result.Jobs["b"].Steps[0].Conclusion)
	}
}

func TestRunWorkflow_MatrixFailFastStopsRemainingCombinations(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"build": {
				RunsOn: "ubuntu-latest",
				Strategy: &Strategy{
					Matrix: map[string]interface{}{
						"version": []interface{}{"1", "2", "3"},
					},
				},
				Steps: []Step{{ID: "s", Run: "exit 1"}},
			},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, failSelector)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["build"].Conclusion != "failure" {
		t.Fatalf("Jobs[build].Conclusion = %q, want failure", result.Jobs["build"].Conclusion)
	}
	skipped := 0
	for _, s := range result.Jobs["build"].Steps {
		if s.Conclusion == "skipped" {
			skipped++
		}
	}
	if skipped == 0 {
		t.Error("expected at least one matrix combination to be skipped after the first failure (fail-fast defaults to true)")
	}
}

func TestTopoSortJobs_CircularDependencyError(t *testing.T) {
	jobs := map[string]Job{
		"a": {Needs: StringOrSlice{"b"}},
		"b": {Needs: StringOrSlice{"a"}},
	}
	_, err := topoSortJobs(jobs)
	if err == nil {
		t.Fatal("topoSortJobs() error = nil, want a circular dependency error")
	}
}

func TestTopoSortJobs_UnknownNeedError(t *testing.T) {
	jobs := map[string]Job{
		"a": {Needs: StringOrSlice{"ghost"}},
	}
	_, err := topoSortJobs(jobs)
	if err == nil {
		t.Fatal("topoSortJobs() error = nil, want an unknown-job error")
	}
}
