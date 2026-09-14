package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mirror-gha/internal/runner"
)

type fakeBackend struct {
	results []runner.StepResult
	lastJob *fakeJob // set by StartJob, lets tests inspect what ran after RunJob returns
}

func (f *fakeBackend) StartJob(ctx context.Context, hostWorkspaceDir string) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	job := &fakeJob{results: f.results, dir: dir, workspaceDir: hostWorkspaceDir}
	f.lastJob = job
	return job, nil
}

type fakeJob struct {
	results      []runner.StepResult
	calls        int
	dir          string
	workspaceDir string
	execSpecs    []runner.StepSpec
}

func (j *fakeJob) FilesRoot() string     { return j.dir }
func (j *fakeJob) WorkspacePath() string { return j.workspaceDir }

func (j *fakeJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	j.execSpecs = append(j.execSpecs, spec)
	r := j.results[j.calls]
	j.calls++
	return r, nil
}

func (j *fakeJob) Stop(ctx context.Context) error {
	return os.RemoveAll(j.dir)
}

func (j *fakeJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	return nil
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Steps[0].Conclusion != "skipped" {
		t.Errorf("Steps[0].Conclusion = %q, want %q", result.Steps[0].Conclusion, "skipped")
	}
}

func TestRunJob_RejectsStepWithBothRunAndUses(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo hi", Uses: "owner/repo@v1"}},
	}
	backend := &fakeBackend{}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err == nil {
		t.Fatal("RunJob() error = nil, want error for a step with both run: and uses:")
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
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

func TestRunJob_LegacyStdoutOutputsFlowToLaterSteps(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			// Simulates an action still using the deprecated stdout-based
			// workflow command (like actions/hello-world-javascript-action@v1)
			// instead of writing to $GITHUB_OUTPUT.
			{ID: "emit", Run: "echo legacy"},
			{ID: "use", Run: "echo got ${{ steps.emit.outputs.time }}"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "Hello!\n##[set-output name=time;]13:31:15 GMT+0000\n"},
		{ExitCode: 0, Stdout: "got 13:31:15 GMT+0000\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
	// The real assertion: the second step's *substituted command* must
	// contain the value the first step emitted via the legacy stdout
	// convention — proving RunJob actually parsed it, not just that
	// nothing errored.
	secondCommand := backend.lastJob.execSpecs[1].Command
	if secondCommand != "echo got 13:31:15 GMT+0000" {
		t.Errorf("second step's command = %q, want %q", secondCommand, "echo got 13:31:15 GMT+0000")
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

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want %q", result.Conclusion, "success")
	}
}

func TestRunJob_RejectsEmptyWorkspaceDir(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{RunsOn: "ubuntu-latest", Steps: []Step{{ID: "one", Run: "echo hi"}}}
	backend := &fakeBackend{}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{})
	if err == nil {
		t.Fatal("RunJob() with empty WorkspaceDir error = nil, want error")
	}
}

func TestRunJob_SetsGithubWorkspaceContext(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo ${{ github.workspace }}"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}
	workspaceDir := t.TempDir()

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	// fakeJob.WorkspacePath() echoes back whatever hostWorkspaceDir it was
	// started with, so a substituted command containing that value proves
	// RunJob actually threads opts.WorkspaceDir through to backend.StartJob
	// and reads it back into the github context.
	spec := backend.lastJob.execSpecs[0]
	if spec.Command != "echo "+workspaceDir {
		t.Errorf("Command = %q, want %q", spec.Command, "echo "+workspaceDir)
	}
	if spec.Env["GITHUB_WORKSPACE"] != workspaceDir {
		t.Errorf(`Env["GITHUB_WORKSPACE"] = %q, want %q`, spec.Env["GITHUB_WORKSPACE"], workspaceDir)
	}
}

func TestRunJob_DefaultWorkingDirectoryIsWorkspace(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "pwd"}}, // no working-directory set anywhere
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}
	workspaceDir := t.TempDir()

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.WorkingDirectory != workspaceDir {
		t.Errorf("WorkingDirectory = %q, want %q (should default to the workspace)", spec.WorkingDirectory, workspaceDir)
	}
}

func TestRunJob_ExplicitWorkingDirectoryOverridesWorkspaceDefault(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "pwd", WorkingDirectory: "/custom"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.WorkingDirectory != "/custom" {
		t.Errorf("WorkingDirectory = %q, want %q (explicit setting should win over the workspace default)", spec.WorkingDirectory, "/custom")
	}
}

func TestRunJob_UsesStepIgnoresWorkingDirectoryDefaults(t *testing.T) {
	requireNetwork(t) // exercises the real prepareUsesStep -> actions.EnsureNode path

	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "my-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Test'\nruns:\n  using: 'node20'\n  main: 'index.js'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn:   "ubuntu-latest",
		Defaults: &Defaults{Run: RunDefaults{WorkingDirectory: "/should-not-be-used"}},
		Steps:    []Step{{ID: "one", Uses: "./my-action"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.WorkingDirectory != workspaceDir {
		t.Errorf("WorkingDirectory = %q, want %q (uses: steps must ignore defaults.run.working-directory)", spec.WorkingDirectory, workspaceDir)
	}
}
