package engine

import (
	"context"
	"os"
	"testing"

	"mirror-gha/internal/runner"
)

type fakeBackend struct {
	results []runner.StepResult
}

func (f *fakeBackend) StartJob(ctx context.Context) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &fakeJob{results: f.results, dir: dir}, nil
}

type fakeJob struct {
	results []runner.StepResult
	calls   int
	dir     string
}

func (j *fakeJob) FilesRoot() string { return j.dir }

func (j *fakeJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	r := j.results[j.calls]
	j.calls++
	return r, nil
}

func (j *fakeJob) Stop(ctx context.Context) error {
	return os.RemoveAll(j.dir)
}

func TestRunJob_AllStepsSucceed(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "echo one"},
			{ID: "two", Run: "echo two"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "one\n"},
		{ExitCode: 0, Stdout: "two\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(result.Steps))
	}
}

func TestRunJob_StepFailsStopsJob(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "exit 1"},
			{ID: "two", Run: "echo unreachable"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 1}}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "failure" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "failure")
	}
	if len(result.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1 (job should stop after a failing step)", len(result.Steps))
	}
}

func TestRunJob_ContinueOnErrorKeepsGoing(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "exit 1", ContinueOnError: true},
			{ID: "two", Run: "echo two"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 1},
		{ExitCode: 0, Stdout: "two\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2 (continue-on-error should not stop the job)", len(result.Steps))
	}
	if result.Steps[0].Conclusion != "failure" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "failure")
	}
}

func TestRunJob_IfConditionSkipsStep(t *testing.T) {
	wf := &Workflow{Name: "test", Env: map[string]string{"SKIP": "true"}}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "echo skipped", If: "${{ env.SKIP != 'true' }}"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Steps[0].Conclusion != "skipped" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "skipped")
	}
}

func TestRunJob_OutputsFlowToLaterSteps(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "emit", Run: `echo "greeting=hi" >> "$GITHUB_OUTPUT"`},
			{ID: "use", Run: "echo got ${{ steps.emit.outputs.greeting }}"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "got hi\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend)
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
}
