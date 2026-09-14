package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HostBackend runs a job directly on the host process, with no container
// at all — the only architecturally honest option for macOS, which
// cannot be virtualized or containerized on non-Apple hardware. Callers
// (runner.SelectBackend) are responsible for only constructing this when
// runtime.GOOS == "darwin"; HostBackend itself doesn't re-check that,
// matching the precedent that backend selection lives in SelectBackend,
// not in each Backend implementation.
type HostBackend struct{}

func NewHostBackend() *HostBackend { return &HostBackend{} }

// StartJob rejects container: and services: outright — real GitHub
// Actions only supports Docker container/service jobs on Linux runners,
// a constraint this mirrors rather than invents.
func (HostBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}
	if containerSpec != nil {
		return nil, fmt.Errorf("container: is not supported on macOS jobs — real GitHub Actions only supports container jobs on Linux runners")
	}
	if len(services) > 0 {
		return nil, fmt.Errorf("services: is not supported on macOS jobs — real GitHub Actions only supports service containers on Linux runners")
	}

	root, err := os.MkdirTemp("", "mirror-host-job-")
	if err != nil {
		return nil, fmt.Errorf("create job root: %w", err)
	}
	filesRoot := filepath.Join(root, "files")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("create files root: %w", err)
	}

	return &hostJob{root: root, filesRoot: filesRoot, hostWorkspaceDir: hostWorkspaceDir}, nil
}

// translateMirrorPath rewrites the engine layer's synthetic "/mirror-*"
// path convention (ContainerNodePath, ContainerActionPath — meaningful
// today only because the Docker backend bind-mounts them inside an
// isolated container filesystem namespace) onto a real path under this
// job's own temp root. A bare host process has no such namespace: writing
// to the literal path "/mirror-node" on a real Mac would need root and
// would collide across concurrent or repeated runs. Any path NOT using
// this project's own "/mirror-" convention is returned unchanged — this
// only ever rewrites strings mirror-gha itself generates with that exact
// prefix, never workflow-author-controlled content.
func translateMirrorPath(root, p string) string {
	if !strings.HasPrefix(p, "/mirror-") {
		return p
	}
	return filepath.Join(root, p)
}

// hostJob is one job's execution environment when running on HostBackend —
// a real host temp directory tree, not a container.
type hostJob struct {
	root             string // per-job real temp root; "/mirror-*" paths translate under here
	filesRoot        string // real host dir backing workflow-command files directly — no translation needed, there's no container-side/host-side split to bridge
	hostWorkspaceDir string
}

func (j *hostJob) FilesRoot() string { return j.filesRoot }

// WorkspacePath returns the real workspace directory unchanged — there is
// nothing to bind-mount into, matching how a real self-hosted/macOS
// runner operates directly against the real checkout.
func (j *hostJob) WorkspacePath() string { return j.hostWorkspaceDir }

func (j *hostJob) Platform() string { return "darwin" }

func (j *hostJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	dest := translateMirrorPath(j.root, containerPath)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dest, err)
	}
	// The trailing "/." on the source copies hostPath's *contents* into
	// dest rather than nesting hostPath's own basename one level deeper —
	// the same idiom dockerJob.CopyToContainer already relies on for
	// docker cp, and one BSD cp (macOS's default) supports identically.
	cmd := exec.CommandContext(ctx, "cp", "-R", hostPath+"/.", dest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp %s -> %s: %w: %s", hostPath, dest, err, stderr.String())
	}
	return nil
}

func (j *hostJob) Exec(ctx context.Context, spec StepSpec) (StepResult, error) {
	env := map[string]string{}
	for k, v := range spec.Env {
		if k == "GITHUB_ACTION_PATH" {
			v = translateMirrorPath(j.root, v)
		}
		env[k] = v
	}
	env["GITHUB_ENV"] = filepath.Join(spec.FilesDir, "github_env")
	env["GITHUB_PATH"] = filepath.Join(spec.FilesDir, "github_path")
	env["GITHUB_OUTPUT"] = filepath.Join(spec.FilesDir, "github_output")
	env["GITHUB_STEP_SUMMARY"] = filepath.Join(spec.FilesDir, "github_step_summary")
	env["GITHUB_STATE"] = filepath.Join(spec.FilesDir, "github_state")

	var cmd *exec.Cmd
	if len(spec.Args) > 0 {
		args := make([]string, len(spec.Args))
		for i, a := range spec.Args {
			args[i] = translateMirrorPath(j.root, a)
		}
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	} else {
		shell := spec.Shell
		if shell == "" {
			// Real GitHub Actions' own documented default for both Linux
			// and macOS runners is bash, and every real macOS ships one
			// at a fixed path — unlike the Docker backend, which defaults
			// to sh (the lowest common denominator on a bare ubuntu:22.04
			// image). This divergence is scoped to this backend only.
			shell = "bash"
		}
		cmd = exec.CommandContext(ctx, shell, "-c", spec.Command)
	}
	if spec.WorkingDirectory != "" {
		cmd.Dir = spec.WorkingDirectory
	}
	// Real host env (PATH, HOME, etc.) is inherited on top of — a
	// deliberate divergence from the Docker backend's clean-container
	// env: a real self-hosted/macOS runner also executes as the logged-in
	// user with their real environment, and a run: step's command (e.g.
	// `brew`, `npm`) is expected to resolve via the real host PATH.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("exec: %w", err)
		}
	}
	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// RunDockerAction always errors — real GitHub Actions only supports
// Docker container actions on Linux runners. internal/engine's
// prepareUsesStep already rejects these steps earlier via Platform(),
// before ever reaching here; this is a defense-in-depth backstop, not the
// primary enforcement point.
func (j *hostJob) RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error) {
	return StepResult{}, fmt.Errorf("docker actions require a Linux job — real GitHub Actions only supports Docker container actions on Linux runners, not macOS")
}

func (j *hostJob) Stop(ctx context.Context) error {
	return os.RemoveAll(j.root)
}
