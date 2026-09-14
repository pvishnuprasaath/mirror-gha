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

// containerWorkspaceMount is where the host workspace directory is
// bind-mounted inside the container — the same path act itself uses, so
// workflows authored/tested against that mental model transfer directly.
const containerWorkspaceMount = "/github/workspace"

type LinuxDockerBackend struct {
	image string
}

func NewLinuxDockerBackend() *LinuxDockerBackend {
	return &LinuxDockerBackend{image: linuxRunnerImage}
}

// StartJob starts one long-lived container for the whole job. Every step
// execs into this same container (see dockerJob.Exec) so filesystem state
// persists across steps, matching real GitHub Actions/act semantics.
// hostWorkspaceDir is bind-mounted read-write at containerWorkspaceMount —
// this is a direct bind mount, not a copy, so steps operate on (and can
// modify) the real files on disk.
func (b *LinuxDockerBackend) StartJob(ctx context.Context, hostWorkspaceDir string) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}

	hostFilesRoot, err := os.MkdirTemp("", "mirror-job-")
	if err != nil {
		return nil, fmt.Errorf("create job files root: %w", err)
	}

	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-v", hostFilesRoot+":"+containerFilesMount,
		"-v", hostWorkspaceDir+":"+containerWorkspaceMount,
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

func (j *dockerJob) WorkspacePath() string {
	return containerWorkspaceMount
}

func (j *dockerJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	// docker cp only auto-creates the final path component of the
	// destination — it does not create missing intermediate directories
	// (a bare ubuntu:22.04 container has no /mirror-actions, /mirror-node,
	// etc. to begin with). mkdir -p the exact target first, then copy
	// hostPath's *contents* into it (the trailing "/." on the source is
	// what makes docker cp copy contents into an existing directory
	// instead of nesting hostPath's own basename one level deeper inside
	// it).
	mkdirCmd := exec.CommandContext(ctx, "docker", "exec", j.containerID, "mkdir", "-p", containerPath)
	var mkdirStderr bytes.Buffer
	mkdirCmd.Stderr = &mkdirStderr
	if err := mkdirCmd.Run(); err != nil {
		return fmt.Errorf("mkdir -p %s in container: %w: %s", containerPath, err, mkdirStderr.String())
	}

	cmd := exec.CommandContext(ctx, "docker", "cp", hostPath+"/.", j.containerID+":"+containerPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp %s -> %s:%s: %w: %s", hostPath, j.containerID, containerPath, err, stderr.String())
	}
	return nil
}

func (j *dockerJob) Exec(ctx context.Context, spec StepSpec) (StepResult, error) {
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
	)

	if len(spec.Args) > 0 {
		// Direct exec, no shell — required for uses: steps. See StepSpec's
		// doc comment: a shell silently drops inherited env vars with
		// dashed names (confirmed for real: dash, /bin/sh in Ubuntu
		// images), and GitHub Actions' INPUT_* convention uses dashes.
		args = append(args, spec.Args...)
	} else {
		shell := spec.Shell
		if shell == "" {
			shell = "sh"
		}
		args = append(args, shell, "-c", spec.Command)
	}

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
	// run silently accumulates in the OS temp directory forever. Note:
	// the workspace mount is the caller's own directory (e.g. their repo)
	// and must never be removed here.
	if err := os.RemoveAll(j.hostFilesRoot); err != nil && stopErr == nil {
		return fmt.Errorf("remove job files root %s: %w", j.hostFilesRoot, err)
	}
	return stopErr
}
