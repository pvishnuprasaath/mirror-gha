package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// linuxRunnerImage is the base image jobs run in for v1. It's a plain
// Ubuntu image, not yet a rebuild of the actions/runner-images toolchain —
// tracking that catalog is a follow-up fidelity task, not in this slice.
const linuxRunnerImage = "ubuntu:22.04"

// containerFilesMount is where a job's FilesRoot is bind-mounted inside
// the container.
const containerFilesMount = "/mirror-files"

type LinuxDockerBackend struct {
	image string
}

func NewLinuxDockerBackend() *LinuxDockerBackend {
	return &LinuxDockerBackend{image: linuxRunnerImage}
}

// StartJob starts one long-lived container for the whole job. Every step
// execs into this same container (see dockerJob.Exec) so filesystem state
// persists across steps, matching real GitHub Actions/act semantics.
func (b *LinuxDockerBackend) StartJob(ctx context.Context) (Job, error) {
	hostFilesRoot, err := os.MkdirTemp("", "mirror-job-")
	if err != nil {
		return nil, fmt.Errorf("create job files root: %w", err)
	}

	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-v", hostFilesRoot+":"+containerFilesMount,
		b.image, "sleep", "infinity",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("start job container: %w: %s", err, stderr.String())
	}

	return &dockerJob{
		containerID:   strings.TrimSpace(stdout.String()),
		hostFilesRoot: hostFilesRoot,
	}, nil
}

// dockerJob is one running container backing a single job.
type dockerJob struct {
	containerID   string
	hostFilesRoot string
}

func (j *dockerJob) FilesRoot() string {
	return j.hostFilesRoot
}

func (j *dockerJob) Exec(ctx context.Context, spec StepSpec) (StepResult, error) {
	shell := spec.Shell
	if shell == "" {
		shell = "sh"
	}

	rel, err := filepath.Rel(j.hostFilesRoot, spec.FilesDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return StepResult{}, fmt.Errorf("FilesDir %q must be a subdirectory of the job's FilesRoot %q", spec.FilesDir, j.hostFilesRoot)
	}
	containerFilesDir := path.Join(containerFilesMount, filepath.ToSlash(rel))

	args := []string{"exec"}
	if spec.WorkingDirectory != "" {
		args = append(args, "-w", spec.WorkingDirectory)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		"-e", "GITHUB_ENV="+containerFilesDir+"/github_env",
		"-e", "GITHUB_PATH="+containerFilesDir+"/github_path",
		"-e", "GITHUB_OUTPUT="+containerFilesDir+"/github_output",
		"-e", "GITHUB_STEP_SUMMARY="+containerFilesDir+"/github_step_summary",
		j.containerID,
		shell, "-c", spec.Command,
	)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("docker exec: %w", err)
		}
	}

	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

func (j *dockerJob) Stop(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", j.containerID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stopErr := cmd.Run()
	if stopErr != nil {
		stopErr = fmt.Errorf("stop job container %s: %w: %s", j.containerID, stopErr, stderr.String())
	}
	// Always attempt cleanup of the host-side files root, even if the
	// container removal above failed — it's a plain temp directory, not
	// something Docker knows about, and leaving it behind on every job
	// run silently accumulates in the OS temp directory forever.
	if err := os.RemoveAll(j.hostFilesRoot); err != nil && stopErr == nil {
		return fmt.Errorf("remove job files root %s: %w", j.hostFilesRoot, err)
	}
	return stopErr
}
