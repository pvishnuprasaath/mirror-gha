package runner

import (
	"context"
	"os"
	"testing"
)

func TestDryRunBackend_ExecSucceedsWithoutDocker(t *testing.T) {
	backend := DryRunBackend{}
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{Command: "exit 1"})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (dry run never actually executes the command)", result.ExitCode)
	}
}

func TestDryRunBackend_StopRemovesFilesRoot(t *testing.T) {
	backend := DryRunBackend{}
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	filesRoot := job.FilesRoot()

	if err := job.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := os.Stat(filesRoot); !os.IsNotExist(err) {
		t.Errorf("FilesRoot %s still exists after Stop()", filesRoot)
	}
}

func TestDryRunBackend_WorkspacePathReportsGivenDir(t *testing.T) {
	workspaceDir := t.TempDir()
	backend := DryRunBackend{}
	job, err := backend.StartJob(context.Background(), workspaceDir)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	if job.WorkspacePath() != workspaceDir {
		t.Errorf("WorkspacePath() = %q, want %q", job.WorkspacePath(), workspaceDir)
	}
}
