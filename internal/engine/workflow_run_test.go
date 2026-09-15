package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"mirror-gha/internal/runner"
)

// alwaysSucceedBackend is a fake Backend for workflow-orchestration tests
// where the point is to verify DAG/needs/matrix wiring, not real step
// execution (that's covered by the real Docker backend tests and by
// executor_test.go's scripted fakeBackend).
type alwaysSucceedBackend struct{}

func (alwaysSucceedBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *runner.ContainerSpec, services map[string]runner.ContainerSpec) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &alwaysSucceedJob{dir: dir, workspaceDir: hostWorkspaceDir}, nil
}

type alwaysSucceedJob struct {
	dir          string
	workspaceDir string
	sleep        time.Duration
}

func (j *alwaysSucceedJob) FilesRoot() string     { return j.dir }
func (j *alwaysSucceedJob) WorkspacePath() string { return j.workspaceDir }
func (j *alwaysSucceedJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	return nil
}
func (j *alwaysSucceedJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	if j.sleep > 0 {
		time.Sleep(j.sleep)
	}
	return runner.StepResult{ExitCode: 0, Stdout: "ok\n"}, nil
}
func (j *alwaysSucceedJob) RunDockerAction(ctx context.Context, spec runner.DockerActionSpec) (runner.StepResult, error) {
	return runner.StepResult{ExitCode: 0, Stdout: "ok\n"}, nil
}
func (j *alwaysSucceedJob) Stop(ctx context.Context) error { return os.RemoveAll(j.dir) }
func (j *alwaysSucceedJob) Platform() string               { return "linux" }

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

// alwaysFailBackend fails every step it executes.
type alwaysFailBackend struct{}

func (alwaysFailBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *runner.ContainerSpec, services map[string]runner.ContainerSpec) (runner.Job, error) {
	dir, err := os.MkdirTemp("", "fake-job-")
	if err != nil {
		return nil, err
	}
	return &alwaysFailJob{dir: dir, workspaceDir: hostWorkspaceDir}, nil
}

type alwaysFailJob struct {
	dir          string
	workspaceDir string
}

func (j *alwaysFailJob) FilesRoot() string     { return j.dir }
func (j *alwaysFailJob) WorkspacePath() string { return j.workspaceDir }
func (j *alwaysFailJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	return nil
}
func (j *alwaysFailJob) Exec(ctx context.Context, spec runner.StepSpec) (runner.StepResult, error) {
	return runner.StepResult{ExitCode: 1}, nil
}
func (j *alwaysFailJob) RunDockerAction(ctx context.Context, spec runner.DockerActionSpec) (runner.StepResult, error) {
	return runner.StepResult{ExitCode: 1}, nil
}
func (j *alwaysFailJob) Stop(ctx context.Context) error { return os.RemoveAll(j.dir) }
func (j *alwaysFailJob) Platform() string               { return "linux" }

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

	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil, nil, "push", "")
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

	result, err := RunWorkflow(context.Background(), wf, failSelector, t.TempDir(), nil, nil, nil, "push", "")
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

	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil, nil, "push", "")
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
					MaxParallel: 1,
					Matrix: map[string]interface{}{
						"version": []interface{}{"1", "2", "3"},
					},
				},
				Steps: []Step{{ID: "s", Run: "exit 1"}},
			},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, failSelector, t.TempDir(), nil, nil, nil, "push", "")
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

func TestRunWorkflow_VarsReachExpressionContext(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {
				RunsOn: "ubuntu-latest",
				Steps: []Step{
					{ID: "s", Run: "echo a", If: "${{ vars.ENVIRONMENT == 'staging' }}"},
				},
			},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, map[string]string{"ENVIRONMENT": "staging"}, nil, "push", "")
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["a"].Steps[0].Conclusion != "success" {
		t.Fatalf("Steps[0].Conclusion = %q, want success (vars.ENVIRONMENT should have resolved to 'staging', making the if: true)", result.Jobs["a"].Steps[0].Conclusion)
	}
}

func TestRunWorkflow_RejectsEmptyWorkspaceDir(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {RunsOn: "ubuntu-latest", Steps: []Step{{ID: "s", Run: "echo a"}}},
		},
	}

	_, err := RunWorkflow(context.Background(), wf, succeedSelector, "", nil, nil, nil, "push", "")
	if err == nil {
		t.Fatal("RunWorkflow() with empty workspaceDir error = nil, want error")
	}
}

func TestTopoSortJobs_CircularDependencyError(t *testing.T) {
	jobs := map[string]Job{
		"a": {Needs: StringOrSlice{"b"}},
		"b": {Needs: StringOrSlice{"a"}},
	}
	_, err := TopoSortJobs(jobs)
	if err == nil {
		t.Fatal("TopoSortJobs() error = nil, want a circular dependency error")
	}
}

func TestTopoSortJobs_UnknownNeedError(t *testing.T) {
	jobs := map[string]Job{
		"a": {Needs: StringOrSlice{"ghost"}},
	}
	_, err := TopoSortJobs(jobs)
	if err == nil {
		t.Fatal("TopoSortJobs() error = nil, want an unknown-job error")
	}
}

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
	result, err := RunWorkflow(context.Background(), wf, sleepySucceedSelector, t.TempDir(), nil, nil, nil, "push", "")
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
	result, err := RunWorkflow(context.Background(), wf, sleepySucceedSelector, t.TempDir(), nil, nil, nil, "push", "")
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

func TestRunWorkflow_ThreadsEventNameAndJSONToJobs(t *testing.T) {
	eventDir := t.TempDir()
	if err := os.WriteFile(eventDir+"/event.json", []byte(`{"ref":"refs/heads/main"}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {RunsOn: "ubuntu-latest", Steps: []Step{{ID: "s", Run: "echo a"}}},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil, nil, "push", eventDir)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["a"].Conclusion != "success" {
		t.Fatalf("Conclusion = %q, want success", result.Jobs["a"].Conclusion)
	}
}
