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
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
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
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
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
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
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
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
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
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
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

func TestLinuxDockerBackend_CopyToContainer(t *testing.T) {
	requireDocker(t)

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "hello.txt"), []byte("copied\n"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	if err := job.CopyToContainer(context.Background(), hostDir, "/mirror-copy-test"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat /mirror-copy-test/hello.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "copied") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "copied")
	}
}

func TestLinuxDockerBackend_StartJob_RejectsEmptyWorkspaceDir(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	_, err := backend.StartJob(context.Background(), "test-job", "", nil, nil)
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
	job, err := backend.StartJob(context.Background(), "test-job", hostWorkspace, nil, nil)
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

func TestLinuxDockerBackend_RunDockerAction_WorkspaceMountAndArgs(t *testing.T) {
	requireDocker(t)

	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "marker.txt"), []byte("marker-content\n"), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", workspaceDir, nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:    "alpine:3.19",
		Args:     []string{"cat", "/github/workspace/marker.txt"},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "marker-content") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "marker-content")
	}
}

func TestLinuxDockerBackend_RunDockerAction_EntrypointOverride(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:      "alpine:3.19",
		Entrypoint: []string{"sh", "-c"},
		Args:       []string{"echo entrypoint-override"},
		FilesDir:   mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "entrypoint-override") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "entrypoint-override")
	}
}

func TestLinuxDockerBackend_RunDockerAction_EnvAndOutputFile(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir := mkStepDir(t, job.FilesRoot())
	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:      "alpine:3.19",
		Entrypoint: []string{"sh", "-c"},
		Args:       []string{`echo "greeting=hello $INPUT_NAME" >> "$GITHUB_OUTPUT"`},
		Env:        map[string]string{"INPUT_NAME": "mirror-gha"},
		FilesDir:   filesDir,
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	outputData, err := os.ReadFile(filepath.Join(filesDir, "github_output"))
	if err != nil {
		t.Fatalf("read github_output: %v", err)
	}
	if !strings.Contains(string(outputData), "greeting=hello mirror-gha") {
		t.Errorf("github_output = %q, want to contain %q", outputData, "greeting=hello mirror-gha")
	}
}

func TestLinuxDockerBackend_StartJob_ContainerImageSwap(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{Image: "alpine:3.19"}, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat /etc/os-release",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "Alpine") {
		t.Errorf("Stdout = %q, want it to identify as Alpine (proving the image swap took effect, not the default ubuntu:22.04)", result.Stdout)
	}
}

func TestLinuxDockerBackend_StartJob_ContainerEnvAndOptions(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{
		Options: `--label "mirror-test=yes"`,
	}, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	dj := job.(*dockerJob)
	cmd := exec.CommandContext(context.Background(), "docker", "inspect", "--format", "{{index .Config.Labels \"mirror-test\"}}", dj.containerID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect error = %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "yes" {
		t.Errorf("label mirror-test = %q, want yes (proving Options reached docker run)", strings.TrimSpace(string(out)))
	}
}

func TestRegistryHostFor_RunnerPackage(t *testing.T) {
	cases := map[string]string{
		"node:20":                 "index.docker.io",
		"ghcr.io/owner/image:tag": "ghcr.io",
	}
	for image, want := range cases {
		got := registryHostFor(image)
		if got != want {
			t.Errorf("registryHostFor(%q) = %q, want %q", image, got, want)
		}
	}
}
