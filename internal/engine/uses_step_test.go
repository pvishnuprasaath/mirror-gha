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

	args, env, err := prepareUsesStep(context.Background(), job, workspaceDir, "greet", step, actx, &nodeReady)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}

	wantArgs := []string{"/mirror-node/bin/node", "/mirror-actions/greet/index.js"}
	if len(args) != len(wantArgs) || args[0] != wantArgs[0] || args[1] != wantArgs[1] {
		t.Errorf("args = %v, want %v", args, wantArgs)
	}
	if env["INPUT_GREETING"] != "hi" {
		t.Errorf(`env["INPUT_GREETING"] = %q, want %q`, env["INPUT_GREETING"], "hi")
	}
	if env["GITHUB_ACTION_PATH"] != "/mirror-actions/greet" {
		t.Errorf(`env["GITHUB_ACTION_PATH"] = %q, want %q`, env["GITHUB_ACTION_PATH"], "/mirror-actions/greet")
	}
	if !nodeReady {
		t.Error("nodeReady = false, want true after the first uses: step")
	}
}

func TestPrepareUsesStep_RejectsNonNodeRuntime(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Docker Action'
runs:
  using: 'docker'
  image: 'Dockerfile'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./docker-action"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true // already true, so we know the rejection isn't a Node-setup failure

	_, _, err := prepareUsesStep(context.Background(), job, workspaceDir, "one", step, actx, &nodeReady)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error for runs.using: docker")
	}
}
