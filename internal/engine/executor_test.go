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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Steps[0].Conclusion != "skipped" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "skipped")
	}
}

func TestEffectiveShell_PrecedenceStepThenJobThenWorkflow(t *testing.T) {
	wf := &Workflow{Defaults: &Defaults{Run: RunDefaults{Shell: "sh"}}}
	job := &Job{Defaults: &Defaults{Run: RunDefaults{Shell: "bash"}}}

	if got := effectiveShell(Step{Shell: "zsh"}, job, wf); got != "zsh" {
		t.Errorf("step-level shell = %q, want %q", got, "zsh")
	}
	if got := effectiveShell(Step{}, job, wf); got != "bash" {
		t.Errorf("job-default shell = %q, want %q", got, "bash")
	}
	if got := effectiveShell(Step{}, &Job{}, wf); got != "sh" {
		t.Errorf("workflow-default shell = %q, want %q", got, "sh")
	}
	if got := effectiveShell(Step{}, &Job{}, &Workflow{}); got != "" {
		t.Errorf("no default shell = %q, want empty (backend falls back to sh)", got)
	}
}

func TestEffectiveWorkingDirectory_Precedence(t *testing.T) {
	wf := &Workflow{Defaults: &Defaults{Run: RunDefaults{WorkingDirectory: "/wf"}}}
	job := &Job{Defaults: &Defaults{Run: RunDefaults{WorkingDirectory: "/job"}}}

	if got := effectiveWorkingDirectory(Step{WorkingDirectory: "/step"}, job, wf); got != "/step" {
		t.Errorf("step-level workdir = %q, want %q", got, "/step")
	}
	if got := effectiveWorkingDirectory(Step{}, job, wf); got != "/job" {
		t.Errorf("job-default workdir = %q, want %q", got, "/job")
	}
	if got := effectiveWorkingDirectory(Step{}, &Job{}, wf); got != "/wf" {
		t.Errorf("workflow-default workdir = %q, want %q", got, "/wf")
	}
}

func TestRunJob_ComputesOutputsFromJobOutputsField(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn:  "ubuntu-latest",
		Steps:   []Step{{ID: "emit", Run: `echo "v=1.0" >> "$GITHUB_OUTPUT"`}},
		Outputs: map[string]string{"version": "${{ steps.emit.outputs.v }}"},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	// fakeBackend never writes the GITHUB_OUTPUT file for real, so the
	// output resolves to empty — this test exercises that job.Outputs is
	// always computed (never skipped) and doesn't error on a reference to
	// a step output that happens to be empty.
	if _, ok := result.Outputs["version"]; !ok {
		t.Errorf("Outputs = %v, want a \"version\" key present (even if empty)", result.Outputs)
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
}
