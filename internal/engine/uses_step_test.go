package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireNetwork skips the test if there's no working internet connection.
// prepareUsesStep's happy path calls actions.EnsureNode, which downloads
// the pinned Node runtime on first use — a real network dependency this
// test can't avoid, so it needs the same graceful skip internal/actions'
// own network-dependent tests use (duplicated rather than shared across
// packages for a five-line check).
func requireNetwork(t *testing.T) {
	t.Helper()
	if err := exec.Command("curl", "-sS", "-o", os.DevNull, "--max-time", "5", "https://nodejs.org").Run(); err != nil {
		t.Skipf("no network connectivity, skipping: %v", err)
	}
}

// requireDocker skips the test if the docker CLI isn't installed — mirrors
// internal/runner's and internal/actions' own requireDocker(t) (duplicated
// per package, same as requireNetwork already is).
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker action test")
	}
}

func TestPrepareUsesStep_LocalAction(t *testing.T) {
	requireNetwork(t)

	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "my-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Test Action'
inputs:
  greeting:
    default: 'hello'
runs:
  using: 'node20'
  main: 'index.js'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./my-action", With: map[string]string{"greeting": "hi"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	plan, err := prepareUsesStep(context.Background(), p, "greet", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}

	wantArgs := []string{"/mirror-node/bin/node", "/mirror-actions/greet/index.js"}
	if len(plan.Args) != len(wantArgs) || plan.Args[0] != wantArgs[0] || plan.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", plan.Args, wantArgs)
	}
	if plan.Env["INPUT_GREETING"] != "hi" {
		t.Errorf(`Env["INPUT_GREETING"] = %q, want %q`, plan.Env["INPUT_GREETING"], "hi")
	}
	if plan.Env["GITHUB_ACTION_PATH"] != "/mirror-actions/greet" {
		t.Errorf(`Env["GITHUB_ACTION_PATH"] = %q, want %q`, plan.Env["GITHUB_ACTION_PATH"], "/mirror-actions/greet")
	}
	if plan.Docker != nil {
		t.Error("Docker = non-nil, want nil for a node action")
	}
	if !nodeReady {
		t.Error("nodeReady = false, want true after the first uses: step")
	}
}

func TestPrepareUsesStep_RejectsUnknownRuntime(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "mystery-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Mystery Action'\nruns:\n  using: 'some-future-runtime'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./mystery-action"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	_, err := prepareUsesStep(context.Background(), p, "one", step, actx)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error for an unknown runs.using")
	}
}

func TestPrepareUsesStep_CompositeAction(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "composite-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Composite Action'
inputs:
  who-to-greet:
    default: 'World'
outputs:
  greeting:
    value: '${{ steps.greet.outputs.greeting }}'
runs:
  using: 'composite'
  steps:
    - id: greet
      shell: 'sh'
      run: 'echo hi'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./composite-action", With: map[string]string{"who-to-greet": "mirror-gha"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	plan, err := prepareUsesStep(context.Background(), p, "greet-step", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Composite == nil {
		t.Fatal("Composite = nil, want non-nil for a composite action")
	}
	if len(plan.Composite.Steps) != 1 || plan.Composite.Steps[0].ID != "greet" {
		t.Errorf("Composite.Steps = %+v, want one step with ID=greet", plan.Composite.Steps)
	}
	if plan.Composite.BaseEnv["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`Composite.BaseEnv["INPUT_WHO-TO-GREET"] = %q, want %q`, plan.Composite.BaseEnv["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
	if plan.Composite.BaseEnv["GITHUB_ACTION_PATH"] != "/mirror-actions/greet-step" {
		t.Errorf(`Composite.BaseEnv["GITHUB_ACTION_PATH"] = %q, want %q`, plan.Composite.BaseEnv["GITHUB_ACTION_PATH"], "/mirror-actions/greet-step")
	}
	out, ok := plan.Composite.Outputs["greeting"]
	if !ok || out.Value != "${{ steps.greet.outputs.greeting }}" {
		t.Errorf(`Composite.Outputs["greeting"] = %+v, want Value=${{ steps.greet.outputs.greeting }}`, out)
	}
}

func TestPrepareUsesStep_RawDockerImage(t *testing.T) {
	workspaceDir := t.TempDir()
	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{
		Uses: "docker://alpine:3.19",
		With: map[string]string{"entrypoint": "sh", "args": "-c echo-hi"},
	}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	plan, err := prepareUsesStep(context.Background(), p, "raw", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Docker == nil {
		t.Fatal("Docker = nil, want non-nil for a raw docker:// step")
	}
	if plan.Docker.Image != "alpine:3.19" {
		t.Errorf("Docker.Image = %q, want %q", plan.Docker.Image, "alpine:3.19")
	}
	wantEntrypoint := []string{"sh"}
	if len(plan.Docker.Entrypoint) != 1 || plan.Docker.Entrypoint[0] != wantEntrypoint[0] {
		t.Errorf("Docker.Entrypoint = %v, want %v", plan.Docker.Entrypoint, wantEntrypoint)
	}
	wantArgs := []string{"-c", "echo-hi"}
	if len(plan.Docker.Args) != len(wantArgs) || plan.Docker.Args[0] != wantArgs[0] || plan.Docker.Args[1] != wantArgs[1] {
		t.Errorf("Docker.Args = %v, want %v", plan.Docker.Args, wantArgs)
	}
	if plan.Docker.ActionSourceDir != "" {
		t.Errorf("Docker.ActionSourceDir = %q, want empty (no action.yml for a raw docker:// step)", plan.Docker.ActionSourceDir)
	}
}

func TestPrepareUsesStep_RepoBasedDockerAction(t *testing.T) {
	requireDocker(t)

	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Docker Action'
inputs:
  who-to-greet:
    default: 'World'
runs:
  using: 'docker'
  image: 'docker://alpine:3.19'
  env:
    STATIC_VAR: 'set-by-action-yml'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./docker-action", With: map[string]string{"who-to-greet": "mirror-gha"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false
	p := runStepParams{RunnerJob: job, WorkspaceDir: workspaceDir, NodeReady: &nodeReady}

	plan, err := prepareUsesStep(context.Background(), p, "greet", step, actx)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Docker == nil {
		t.Fatal("Docker = nil, want non-nil for a docker-using action")
	}
	if plan.Docker.Image != "alpine:3.19" {
		t.Errorf("Docker.Image = %q, want %q", plan.Docker.Image, "alpine:3.19")
	}
	if plan.Docker.ActionSourceDir != actionDir {
		t.Errorf("Docker.ActionSourceDir = %q, want %q", plan.Docker.ActionSourceDir, actionDir)
	}
	if plan.Docker.ActionPathInContainer != "/mirror-actions/greet" {
		t.Errorf("Docker.ActionPathInContainer = %q, want %q", plan.Docker.ActionPathInContainer, "/mirror-actions/greet")
	}
	if plan.Env["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`Env["INPUT_WHO-TO-GREET"] = %q, want %q`, plan.Env["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
	if plan.Env["STATIC_VAR"] != "set-by-action-yml" {
		t.Errorf(`Env["STATIC_VAR"] = %q, want %q`, plan.Env["STATIC_VAR"], "set-by-action-yml")
	}
	if nodeReady {
		t.Error("nodeReady = true, want false — a Docker action must never trigger Node setup")
	}
}
