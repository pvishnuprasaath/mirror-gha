package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// linuxRunnerImage is the base image jobs run in for v1. It's a plain
// Ubuntu image, not yet a rebuild of the actions/runner-images toolchain —
// tracking that catalog is a follow-up fidelity task, not in this slice.
const linuxRunnerImage = "ubuntu:22.04"

type LinuxDockerBackend struct {
	image string
}

func NewLinuxDockerBackend() *LinuxDockerBackend {
	return &LinuxDockerBackend{image: linuxRunnerImage}
}

func (b *LinuxDockerBackend) RunStep(ctx context.Context, spec StepSpec) (StepResult, error) {
	shell := spec.Shell
	if shell == "" {
		shell = "sh"
	}

	args := []string{"run", "--rm", "-v", spec.FilesDir + ":/mirror-files"}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		"-e", "GITHUB_ENV=/mirror-files/github_env",
		"-e", "GITHUB_PATH=/mirror-files/github_path",
		"-e", "GITHUB_OUTPUT=/mirror-files/github_output",
		"-e", "GITHUB_STEP_SUMMARY=/mirror-files/github_step_summary",
		b.image,
		shell, "-c", spec.Command,
	)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("run docker: %w", err)
		}
	}

	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
