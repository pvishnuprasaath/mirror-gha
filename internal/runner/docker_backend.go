package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// linuxRunnerImage is the base image jobs run in for v1. It's a plain
// Ubuntu image, not yet a rebuild of the actions/runner-images toolchain —
// tracking that catalog is a follow-up fidelity task, not in this slice.
const linuxRunnerImage = "ubuntu:22.04"

// registryHostFor parses the registry host out of an image reference —
// duplicated from internal/engine's identical helper rather than
// exported/cross-imported, matching this project's established
// precedent (requireDocker/requireNetwork are also duplicated per
// package) of keeping these packages decoupled.
func registryHostFor(image string) string {
	const dockerHub = "index.docker.io"
	first, _, found := strings.Cut(image, "/")
	if !found {
		return dockerHub
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return dockerHub
}

// dockerRegistryLogin authenticates docker's local credential store to
// registry so a subsequent docker pull/run against a private image
// succeeds. password is piped via stdin (--password-stdin), never passed
// as a -p/--password argument, so it never appears in process args or
// shell history.
func dockerRegistryLogin(ctx context.Context, registry, username, password string) error {
	cmd := exec.CommandContext(ctx, "docker", "login", registry, "-u", username, "--password-stdin")
	cmd.Stdin = strings.NewReader(password)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker login %s: %w: %s", registry, err, stderr.String())
	}
	return nil
}

// dockerRegistryLogout is best-effort cleanup, called from dockerJob.Stop.
func dockerRegistryLogout(ctx context.Context, registry string) error {
	cmd := exec.CommandContext(ctx, "docker", "logout", registry)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker logout %s: %w: %s", registry, err, stderr.String())
	}
	return nil
}

// waitForServiceHealth polls containerID's Docker healthcheck status.
// A container with no HEALTHCHECK defined reports an empty Health.Status
// (docker inspect's format returns "<no value>" for a nonexistent field
// path) — treated as ready immediately, matching act's own behavior of
// not blocking forever on a service that never declared a healthcheck.
func waitForServiceHealth(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Health.Status}}", containerID)
		out, err := cmd.CombinedOutput()
		status := strings.TrimSpace(string(out))
		if err != nil || status == "<no value>" || status == "" {
			return nil
		}
		if status == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service container %s did not become healthy within %s (last status: %s)", containerID, timeout, status)
		}
		time.Sleep(time.Second)
	}
}

// sanitizeDockerName makes s safe to use as a Docker network/container
// name — lowercase, with anything other than [a-z0-9-] replaced by "-".
func sanitizeDockerName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

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
func (b *LinuxDockerBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}

	hostFilesRoot, err := os.MkdirTemp("", "mirror-job-")
	if err != nil {
		return nil, fmt.Errorf("create job files root: %w", err)
	}

	image := b.image
	var registryLogouts []string
	if containerSpec != nil {
		if containerSpec.Image != "" {
			image = containerSpec.Image
		}
		if containerSpec.Username != "" {
			registry := registryHostFor(image)
			if err := dockerRegistryLogin(ctx, registry, containerSpec.Username, containerSpec.Password); err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			registryLogouts = append(registryLogouts, registry)
		}
	}

	var networkName string
	var serviceContainerIDs []string
	if len(services) > 0 {
		networkName = sanitizeDockerName("mirror-svc-" + jobID)
		createNet := exec.CommandContext(ctx, "docker", "network", "create", networkName)
		var netStderr bytes.Buffer
		createNet.Stderr = &netStderr
		if err := createNet.Run(); err != nil && !strings.Contains(netStderr.String(), "already exists") {
			os.RemoveAll(hostFilesRoot)
			return nil, fmt.Errorf("create network %s: %w: %s", networkName, err, netStderr.String())
		}

		names := make([]string, 0, len(services))
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			spec := services[name]
			if spec.Username != "" {
				registry := registryHostFor(spec.Image)
				if err := dockerRegistryLogin(ctx, registry, spec.Username, spec.Password); err != nil {
					return nil, err
				}
				registryLogouts = append(registryLogouts, registry)
			}

			svcArgs := []string{"run", "-d", "--rm",
				"--network", networkName,
				"--network-alias", name,
			}
			for k, v := range spec.Env {
				svcArgs = append(svcArgs, "-e", fmt.Sprintf("%s=%s", k, v))
			}
			for _, p := range spec.Ports {
				svcArgs = append(svcArgs, "-p", p)
			}
			for _, v := range spec.Volumes {
				svcArgs = append(svcArgs, "-v", v)
			}
			if spec.Options != "" {
				opts, err := splitDockerOptions(spec.Options)
				if err != nil {
					return nil, err
				}
				svcArgs = append(svcArgs, opts...)
			}
			svcArgs = append(svcArgs, spec.Image)

			svcCmd := exec.CommandContext(ctx, "docker", svcArgs...)
			var svcStdout, svcStderr bytes.Buffer
			svcCmd.Stdout = &svcStdout
			svcCmd.Stderr = &svcStderr
			if err := svcCmd.Run(); err != nil {
				return nil, fmt.Errorf("start service %s: %w: %s", name, err, svcStderr.String())
			}
			serviceID := strings.TrimSpace(svcStdout.String())
			serviceContainerIDs = append(serviceContainerIDs, serviceID)

			if err := waitForServiceHealth(ctx, serviceID, 5*time.Minute); err != nil {
				return nil, fmt.Errorf("service %s: %w", name, err)
			}
		}
	}

	args := []string{"run", "-d", "--rm",
		"-v", hostFilesRoot + ":" + containerFilesMount,
		"-v", hostWorkspaceDir + ":" + containerWorkspaceMount,
	}
	if networkName != "" {
		args = append(args, "--network", networkName)
	}
	if containerSpec != nil {
		for k, v := range containerSpec.Env {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
		for _, p := range containerSpec.Ports {
			args = append(args, "-p", p)
		}
		for _, v := range containerSpec.Volumes {
			args = append(args, "-v", v)
		}
		if containerSpec.Options != "" {
			opts, err := splitDockerOptions(containerSpec.Options)
			if err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			args = append(args, opts...)
		}
	}
	args = append(args, image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(hostFilesRoot)
		return nil, fmt.Errorf("start job container: %w: %s", err, stderr.String())
	}

	return &dockerJob{
		containerID:         strings.TrimSpace(stdout.String()),
		hostFilesRoot:       hostFilesRoot,
		hostWorkspaceDir:    hostWorkspaceDir,
		registryLogouts:     registryLogouts,
		networkName:         networkName,
		serviceContainerIDs: serviceContainerIDs,
	}, nil
}

// dockerJob is one running container backing a single job.
type dockerJob struct {
	containerID         string
	hostFilesRoot       string
	hostWorkspaceDir    string
	registryLogouts     []string
	networkName         string
	serviceContainerIDs []string
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
		"-e", "GITHUB_STATE="+containerFilesDir+"/github_state",
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

// RunDockerAction runs spec.Image as its own container — docker run --rm,
// blocking until it exits — rather than docker exec into the job's own
// container (see DockerActionSpec's doc comment for why). Binds the same
// host workspace directory the job container itself uses, this step's
// FilesDir for the workflow-command file protocol, the action's own
// source (repo-based actions only), and /var/run/docker.sock
// unconditionally — matching act's and real GitHub-hosted runners' own
// behavior. --network container:<jobContainerID> joins the job
// container's network namespace so localhost service-container access
// keeps working, matching act's NetworkMode exactly.
func (j *dockerJob) RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error) {
	args := []string{"run", "--rm",
		"-v", j.hostWorkspaceDir + ":" + containerWorkspaceMount,
		"-w", containerWorkspaceMount,
		"-v", spec.FilesDir + ":" + containerFilesMount,
		"-e", "GITHUB_WORKSPACE=" + containerWorkspaceMount,
		"-e", "GITHUB_ENV=" + containerFilesMount + "/github_env",
		"-e", "GITHUB_PATH=" + containerFilesMount + "/github_path",
		"-e", "GITHUB_OUTPUT=" + containerFilesMount + "/github_output",
		"-e", "GITHUB_STEP_SUMMARY=" + containerFilesMount + "/github_step_summary",
		"-e", "GITHUB_STATE=" + containerFilesMount + "/github_state",
	}
	if spec.ActionSourceDir != "" {
		args = append(args, "-v", spec.ActionSourceDir+":"+spec.ActionPathInContainer+":ro")
		args = append(args, "-e", "GITHUB_ACTION_PATH="+spec.ActionPathInContainer)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		"--network", "container:"+j.containerID,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
	)

	var command []string
	if len(spec.Entrypoint) > 0 {
		args = append(args, "--entrypoint", spec.Entrypoint[0])
		command = append(append([]string{}, spec.Entrypoint[1:]...), spec.Args...)
	} else {
		command = spec.Args
	}
	args = append(args, spec.Image)
	args = append(args, command...)

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
			return StepResult{}, fmt.Errorf("docker run (docker action %s): %w", spec.Image, err)
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

	for _, serviceID := range j.serviceContainerIDs {
		exec.CommandContext(ctx, "docker", "rm", "-f", serviceID).Run() // best-effort
	}

	if j.networkName != "" {
		exec.CommandContext(ctx, "docker", "network", "rm", j.networkName).Run() // best-effort
	}

	for _, registry := range j.registryLogouts {
		dockerRegistryLogout(ctx, registry) // best-effort, error intentionally ignored
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

// Platform always reports "linux" — the Docker backend's job container is
// always a Linux image, regardless of what OS mirror-gha's own process is
// running on (this project is routinely developed and tested on a Mac
// host that runs Linux job containers via Docker Desktop).
func (j *dockerJob) Platform() string { return "linux" }
