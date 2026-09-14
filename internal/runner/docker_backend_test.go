package runner

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker backend test")
	}
}

func TestLinuxDockerBackend_RunStep_Success(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	dir := t.TempDir()
	result, err := backend.RunStep(context.Background(), StepSpec{
		Command:  "echo hello",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: dir,
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "hello")
	}
}

func TestLinuxDockerBackend_RunStep_NonZeroExit(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	dir := t.TempDir()
	result, err := backend.RunStep(context.Background(), StepSpec{
		Command:  "exit 7",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: dir,
	})
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}
}
