package runner

import (
	"context"
	"fmt"
	"runtime"
)

// StepResult is what a Job reports after executing one step.
type StepResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// StepSpec is everything a Job needs to execute one step, fully resolved
// (env merged, command already expression-substituted) by the caller.
//
// Exactly one of Command or Args should be set. Command runs through a
// shell (`<shell> -c <command>`), matching real GitHub Actions run: step
// semantics. Args execs directly with no intermediate shell at all —
// required for uses: steps: a POSIX shell (confirmed for real: dash, the
// /bin/sh in Ubuntu images) silently drops inherited environment
// variables whose names aren't valid shell identifiers before it execs
// children, and GitHub Actions' own INPUT_* convention deliberately
// allows dashes in input names. Real GitHub Actions doesn't invoke JS
// actions through a shell either, for exactly this reason.
type StepSpec struct {
	Command          string
	Args             []string
	Shell            string
	Env              map[string]string
	WorkingDirectory string
	FilesDir         string // per-step host dir, must be created under Job.FilesRoot()
}

// DockerActionSpec is everything dockerJob.RunDockerAction needs to run a
// uses: step whose action has runs.using: docker — a genuinely separate
// sibling container, not an exec into the job's own long-lived container.
// A Docker action's image is frequently a completely different base OS
// than the job's own ubuntu:22.04, so it can't be docker-cp'd/exec'd into
// the job container the way a JS action's Node runtime can be. Matches
// act's own model: even act, which also runs one persistent container per
// job, spins up Docker actions as their own container
// (pkg/runner/action.go's execAsDocker).
type DockerActionSpec struct {
	Image      string
	Entrypoint []string
	Args       []string
	Env        map[string]string

	// ActionSourceDir is the host path to a repo-based action's own
	// source, bind-mounted read-only at ActionPathInContainer. Empty for
	// a raw docker://image:tag step, which has no action source at all.
	ActionSourceDir       string
	ActionPathInContainer string

	// FilesDir is this step's host dir for the workflow-command file
	// protocol (GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY) — same convention as
	// StepSpec.FilesDir, must be created under Job.FilesRoot().
	FilesDir string
}

// Job is one job's execution environment: a single long-lived container (or
// equivalent), matching how real GitHub Actions and act both work — every
// step of a job execs into the same environment, so filesystem state
// (checked-out files, installed packages, PATH changes) persists step to
// step within a job. A fresh environment per *step* is a fidelity bug, not
// a simplification: it breaks nearly every real-world multi-step workflow.
type Job interface {
	// FilesRoot is the host directory backing the workflow-command files
	// (GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY). Callers must create each
	// step's FilesDir as a subdirectory of this root so the backend can
	// make it visible inside the running environment.
	FilesRoot() string
	// WorkspacePath is where the job's workspace (the bind-mounted host
	// directory `mirror run` was invoked from, or overridden via
	// --workdir) is visible from inside the running environment. Callers
	// use this for the GITHUB_WORKSPACE env var, the github.workspace
	// context value, and as the default step working directory.
	WorkspacePath() string
	// CopyToContainer injects hostPath into the running environment at
	// containerPath — the mechanism uses: steps use to stage a JS action's
	// source and the pinned Node runtime, matching act's own on-demand
	// docker-cp-style injection rather than a mount declared at StartJob.
	CopyToContainer(ctx context.Context, hostPath, containerPath string) error
	Exec(ctx context.Context, spec StepSpec) (StepResult, error)
	// RunDockerAction runs a Docker action (runs.using: docker) as its own
	// container, joined to the job container's network namespace so
	// localhost service-container access keeps working — see
	// DockerActionSpec's doc comment for why this can't just be an Exec.
	RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error)
	Stop(ctx context.Context) error
	// Platform reports the OS the job's steps actually execute under —
	// "linux" for the Docker backend (regardless of what OS mirror-gha
	// itself runs on) or "darwin" for the macOS host backend. Used by the
	// engine layer to make backend-aware decisions (which Node runtime to
	// download, whether a Docker action step is even possible) without
	// needing to know which concrete backend is running.
	Platform() string
}

// ContainerSpec is one job's `container:` override, or one entry of its
// `services:` map — the runner package's own copy, decoupled from
// internal/engine's identically-shaped type the same way DockerActionSpec
// already is: this package receives fully-resolved values (Username/
// Password already validated and extracted from a raw credentials map),
// not YAML-shaped or credential-validation concerns.
type ContainerSpec struct {
	Image    string
	Env      map[string]string
	Ports    []string
	Volumes  []string
	Options  string
	Username string
	Password string
}

// Backend starts a job's execution environment for a given `runs-on` label.
type Backend interface {
	// StartJob starts the environment, bind-mounting hostWorkspaceDir so
	// its contents are visible at Job.WorkspacePath() from inside it.
	// jobID names the job this environment belongs to (used to name the
	// per-job Docker network created when services is non-empty — safe to
	// pass an arbitrary non-empty string in tests that don't exercise
	// services). containerSpec overrides the runner image when non-nil.
	// services, when non-empty, starts one sidecar container per entry,
	// reachable from the job's own environment by its map key as hostname.
	StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error)
}

// ErrUnsupportedRunner is returned by SelectBackend for runner labels that
// don't have a working backend yet (Windows/macOS are Phase 2/3 of the
// design spec's roadmap).
type ErrUnsupportedRunner struct {
	RunsOn string
}

func (e *ErrUnsupportedRunner) Error() string {
	return fmt.Sprintf("runner %q is not supported yet (only ubuntu-latest/ubuntu-22.04/ubuntu-24.04 run today — see docs/design/specs for the Phase 2/3 roadmap)", e.RunsOn)
}

// SelectBackend maps a job's `runs-on` value to a concrete Backend.
func SelectBackend(runsOn string) (Backend, error) {
	switch runsOn {
	case "ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04":
		return NewLinuxDockerBackend(), nil
	case "macos-latest", "macos-15", "macos-14", "macos-13":
		if runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("runner %q requires running mirror-gha on a Mac host (this host is %s) — macOS cannot be virtualized on non-Apple hardware, so cross-host macOS execution isn't possible", runsOn, runtime.GOOS)
		}
		return NewHostBackend(), nil
	default:
		return nil, &ErrUnsupportedRunner{RunsOn: runsOn}
	}
}
