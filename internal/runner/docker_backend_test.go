package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker backend test")
	}
}

func mkStepDir(t *testing.T, root string) string {
	t.Helper()
	dir, err := os.MkdirTemp(root, "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	return dir
}

func TestLinuxDockerBackend_Exec_Success(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "echo hello",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "hello")
	}
}

func TestLinuxDockerBackend_Exec_NonZeroExit(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "exit 7",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}
}

// TestLinuxDockerBackend_Exec_StatePersistsAcrossSteps is the regression
// test for the container-lifecycle fidelity bug found by comparing against
// nektos/act: a job is one continuous environment, not a fresh container
// per step. A file written in one Exec call must be visible to the next.
func TestLinuxDockerBackend_Exec_StatePersistsAcrossSteps(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	_, err = job.Exec(context.Background(), StepSpec{
		Command:  "echo persisted > /tmp/state-test.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("first Exec() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat /tmp/state-test.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("second Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "persisted") {
		t.Errorf("Stdout = %q, want to contain %q (file written by the first step should be visible to the second)", result.Stdout, "persisted")
	}
}

func TestLinuxDockerBackend_Stop_RemovesFilesRoot(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	filesRoot := job.FilesRoot()

	if err := job.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if _, err := os.Stat(filesRoot); !os.IsNotExist(err) {
		t.Errorf("FilesRoot %s still exists after Stop(), want it removed (stat err = %v)", filesRoot, err)
	}
}

func TestLinuxDockerBackend_Exec_RejectsFilesDirOutsideRoot(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	_, err = job.Exec(context.Background(), StepSpec{
		Command:  "echo hello",
		Shell:    "sh",
		FilesDir: filepath.Join(os.TempDir(), "not-under-job-root"),
	})
	if err == nil {
		t.Fatal("Exec() error = nil, want error for a FilesDir outside the job's FilesRoot")
	}
}

func TestLinuxDockerBackend_StartJob_RejectsEmptyWorkspaceDir(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	_, err := backend.StartJob(context.Background(), "")
	if err == nil {
		t.Fatal("StartJob(\"\") error = nil, want error for an empty workspace dir")
	}
}

func TestLinuxDockerBackend_WorkspacePath_IsMountedAndBidirectional(t *testing.T) {
	requireDocker(t)

	hostWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostWorkspace, "from-host.txt"), []byte("hello from host\n"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), hostWorkspace)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	if job.WorkspacePath() != "/github/workspace" {
		t.Errorf("WorkspacePath() = %q, want %q", job.WorkspacePath(), "/github/workspace")
	}

	// A file that already existed on the host before the container
	// started must be visible inside it (proves the mount, not a copy).
	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat " + job.WorkspacePath() + "/from-host.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "hello from host") {
		t.Errorf("Stdout = %q, want to contain the host file's content", result.Stdout)
	}

	// A file written from inside the container must appear on the host —
	// proves this is a real bind mount, not a one-way copy-in.
	_, err = job.Exec(context.Background(), StepSpec{
		Command:  "echo written-from-container > " + job.WorkspacePath() + "/from-container.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	hostContent, err := os.ReadFile(filepath.Join(hostWorkspace, "from-container.txt"))
	if err != nil {
		t.Fatalf("expected file written from container to appear on host: %v", err)
	}
	if !strings.Contains(string(hostContent), "written-from-container") {
		t.Errorf("host file content = %q, want to contain %q", hostContent, "written-from-container")
	}
}
