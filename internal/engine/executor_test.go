package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mirror-gha/internal/runner"
)

type fakeBackend struct {
	results []runner.StepResult
	lastJob *fakeJob // set by StartJob, lets tests inspect what ran after RunJob returns
}

func (f *fakeBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *runner.ContainerSpec, services map[string]runner.ContainerSpec) (runner.Job, error) {
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
	dockerSpecs  []runner.DockerActionSpec
	platform     string // Platform() returns "linux" if this is empty
}

func (j *fakeJob) Platform() string {
	if j.platform == "" {
		return "linux"
	}
	return j.platform
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

func (j *fakeJob) RunDockerAction(ctx context.Context, spec runner.DockerActionSpec) (runner.StepResult, error) {
	j.dockerSpecs = append(j.dockerSpecs, spec)
	r := j.results[j.calls]
	j.calls++
	return r, nil
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

func TestRunJob_DockerActionStepCallsRunDockerAction(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Docker Action'\nruns:\n  using: 'docker'\n  image: 'docker://alpine:3.19'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Uses: "./docker-action"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want success", result.Conclusion)
	}
	if len(backend.lastJob.dockerSpecs) != 1 {
		t.Fatalf("dockerSpecs = %d entries, want 1", len(backend.lastJob.dockerSpecs))
	}
	if backend.lastJob.dockerSpecs[0].Image != "alpine:3.19" {
		t.Errorf("dockerSpecs[0].Image = %q, want %q", backend.lastJob.dockerSpecs[0].Image, "alpine:3.19")
	}
	if len(backend.lastJob.execSpecs) != 0 {
		t.Errorf("execSpecs = %d entries, want 0 (a Docker action step must never call Exec)", len(backend.lastJob.execSpecs))
	}
}

func TestRunJob_ExtraEnvReachesStepExec(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo $SOME_EXTRA_VAR"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		ExtraEnv:     map[string]string{"SOME_EXTRA_VAR": "extra-value"},
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.Env["SOME_EXTRA_VAR"] != "extra-value" {
		t.Errorf(`Env["SOME_EXTRA_VAR"] = %q, want %q`, spec.Env["SOME_EXTRA_VAR"], "extra-value")
	}
}

func TestRunJob_PostActionRunsAfterMainStepsWithState(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "post-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Post Action'\nruns:\n  using: 'node20'\n  main: 'index.js'\n  post: 'post.js'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Uses: "./post-action"},
			{ID: "two", Run: "echo two"},
		},
	}
	// fakeBackend.results feeds Exec calls in call order: step "one"
	// (main), step "two", then the post action last (post runs after
	// every top-level step, in reverse order — only one uses: step
	// here, so reverse order is a single entry).
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0},
	}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	if len(result.Steps) != 3 {
		t.Fatalf("Steps = %d entries, want 3 (main x2 + post x1)", len(result.Steps))
	}
	postReport := result.Steps[2]
	if postReport.ID != "one-post" {
		t.Errorf("Steps[2].ID = %q, want %q", postReport.ID, "one-post")
	}

	postSpec := backend.lastJob.execSpecs[2]
	wantArgs := []string{"/mirror-node/bin/node", "/mirror-actions/one/post.js"}
	if len(postSpec.Args) != 2 || postSpec.Args[0] != wantArgs[0] || postSpec.Args[1] != wantArgs[1] {
		t.Errorf("post Args = %v, want %v", postSpec.Args, wantArgs)
	}
}

func TestRunJob_ExportsGitHubContextAsEnvVars(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo one"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.Env["GITHUB_REF"] == "" {
		t.Error(`Env["GITHUB_REF"] is empty, want a real value (actions/cache@v4's isValidEvent() gates on this existing at all)`)
	}
	if spec.Env["GITHUB_EVENT_NAME"] == "" {
		t.Error(`Env["GITHUB_EVENT_NAME"] is empty, want a real value`)
	}
}

func TestRunJob_ContainerEnvMergesIntoStepEnv(t *testing.T) {
	// A bare RawContainer node representing container: {env: {FOO: bar}}
	// isn't easy to hand-construct outside YAML parsing, so this test
	// parses a real workflow instead of hand-building a Job.
	wf, err := Parse([]byte(`
name: t
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    container:
      image: node:20
      env:
        FOO: bar
    steps:
      - id: s
        run: echo hi
        shell: sh
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]

	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}
	_, err = RunJob(context.Background(), wf, &job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		JobID:        "build",
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if backend.lastJob == nil {
		t.Fatal("backend.lastJob is nil, want StartJob to have been called")
	}
	if len(backend.lastJob.execSpecs) != 1 {
		t.Fatalf("execSpecs = %v, want exactly 1 step executed", backend.lastJob.execSpecs)
	}
	if got := backend.lastJob.execSpecs[0].Env["FOO"]; got != "bar" {
		t.Errorf("step Env[FOO] = %q, want %q (container.env should merge into every step's env)", got, "bar")
	}
}

func TestToRunnerContainerSpec_Nil(t *testing.T) {
	spec, err := toRunnerContainerSpec(nil)
	if err != nil {
		t.Fatalf("toRunnerContainerSpec(nil) error = %v", err)
	}
	if spec != nil {
		t.Errorf("toRunnerContainerSpec(nil) = %v, want nil", spec)
	}
}

func TestToRunnerContainerSpec_ResolvesCredentials(t *testing.T) {
	spec, err := toRunnerContainerSpec(&ContainerSpec{
		Image:       "ghcr.io/owner/image:tag",
		Credentials: map[string]string{"username": "u", "password": "p"},
	})
	if err != nil {
		t.Fatalf("toRunnerContainerSpec() error = %v", err)
	}
	if spec.Image != "ghcr.io/owner/image:tag" || spec.Username != "u" || spec.Password != "p" {
		t.Errorf("toRunnerContainerSpec() = %+v, want Image=ghcr.io/owner/image:tag Username=u Password=p", spec)
	}
}

func TestToRunnerContainerSpec_InvalidCredentialsError(t *testing.T) {
	_, err := toRunnerContainerSpec(&ContainerSpec{
		Image:       "node:20",
		Credentials: map[string]string{"username": "u"},
	})
	if err == nil {
		t.Fatal("toRunnerContainerSpec() error = nil, want error for malformed credentials")
	}
}

func TestRunJob_ExposesEnvironmentName(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
on: push
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - id: s
        run: echo hi
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["deploy"]

	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}
	_, err = RunJob(context.Background(), wf, &job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		JobID:        "deploy",
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if backend.lastJob == nil || len(backend.lastJob.execSpecs) != 1 {
		t.Fatalf("execSpecs = %v, want exactly 1 step executed", backend.lastJob)
	}
	if got := backend.lastJob.execSpecs[0].Env["GITHUB_ENVIRONMENT"]; got != "production" {
		t.Errorf(`step Env["GITHUB_ENVIRONMENT"] = %q, want "production"`, got)
	}
}

func TestRunJob_WorkflowCommandsRenderedAndMaskPropagatesAcrossSteps(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps: []Step{
			{ID: "one", Run: "echo one"},
			{ID: "two", Run: "echo two"},
		},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "::group::setup\n::add-mask::supersecret\ntoken is supersecret\n::endgroup::\n"},
		{ExitCode: 0, Stdout: "later output mentions supersecret again\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(result.Steps))
	}

	first := result.Steps[0].Stdout
	if strings.Contains(first, "::group::") || strings.Contains(first, "::add-mask::") || strings.Contains(first, "::endgroup::") {
		t.Errorf("first step Stdout = %q, want raw workflow commands stripped/rendered", first)
	}
	if !strings.Contains(first, "▶ setup") {
		t.Errorf("first step Stdout = %q, want a rendered group marker", first)
	}
	if strings.Contains(first, "supersecret") {
		t.Errorf("first step Stdout = %q, want the masked value redacted even in the step that registered it", first)
	}
	if !strings.Contains(first, "token is ***") {
		t.Errorf("first step Stdout = %q, want token is *** after redaction", first)
	}

	second := result.Steps[1].Stdout
	if strings.Contains(second, "supersecret") {
		t.Errorf("second step Stdout = %q, want the mask registered in step one redacted here too", second)
	}
	if !strings.Contains(second, "***") {
		t.Errorf("second step Stdout = %q, want *** present", second)
	}
}

func TestRunJob_ErrorAnnotationDoesNotFailStep(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo one"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "::error::something looked wrong but exit code is 0\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Steps[0].Conclusion != "success" {
		t.Errorf("Conclusion = %q, want success (an ::error:: annotation must not fail the step by itself)", result.Steps[0].Conclusion)
	}
	if !strings.Contains(result.Steps[0].Stdout, "❌ something looked wrong") {
		t.Errorf("Stdout = %q, want the rendered error annotation", result.Steps[0].Stdout)
	}
}

func TestRunJob_DebugHiddenUnlessActionsStepDebugSet(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo one"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{
		{ExitCode: 0, Stdout: "::debug::internal detail\nvisible\n"},
	}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		ExtraEnv:     map[string]string{"ACTIONS_STEP_DEBUG": "true"},
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if !strings.Contains(result.Steps[0].Stdout, "🐛 internal detail") {
		t.Errorf("Stdout = %q, want the debug line shown when ACTIONS_STEP_DEBUG=true", result.Steps[0].Stdout)
	}
}
